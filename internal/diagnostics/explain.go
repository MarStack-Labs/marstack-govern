package diagnostics

import (
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
)

type Explanation struct {
	Cause     string
	Evidence  string
	Reproduce []string
	LogLines  []string
	Since     time.Time
}

type Subject struct {
	Namespace       string
	Name            string
	Kind            string
	ReplicasDesired int32
	ReplicasReady   int32
}

func Explain(subject Subject, pods []corev1.Pod, events []corev1.Event) Explanation {
	sorted := append([]corev1.Pod(nil), pods...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].CreationTimestamp.After(sorted[j].CreationTimestamp.Time)
	})

	for _, rule := range []func(Subject, []corev1.Pod, []corev1.Event) (Explanation, bool){
		unschedulable,
		outOfMemory,
		crashLooping,
		imageUnavailable,
		probeFailing,
	} {
		if explanation, found := rule(subject, sorted, events); found {
			return explanation
		}
	}

	return settled(subject, sorted, events)
}

func unschedulable(subject Subject, pods []corev1.Pod, _ []corev1.Event) (Explanation, bool) {
	for i := range pods {
		pod := &pods[i]
		if pod.Status.Phase != corev1.PodPending || pod.Spec.NodeName != "" {
			continue
		}

		for _, condition := range pod.Status.Conditions {
			if condition.Type != corev1.PodScheduled || condition.Status != corev1.ConditionFalse {
				continue
			}

			return Explanation{
				Cause: fmt.Sprintf("%s cannot be scheduled: %s",
					pod.Name, firstSentence(condition.Message, condition.Reason)),
				Evidence:  fmt.Sprintf("the pod has waited since %s with no node assigned", since(pod.CreationTimestamp.Time)),
				Reproduce: describe(subject, pod.Name),
				Since:     pod.CreationTimestamp.Time,
			}, true
		}
	}

	return Explanation{}, false
}

func outOfMemory(subject Subject, pods []corev1.Pod, _ []corev1.Event) (Explanation, bool) {
	for i := range pods {
		pod := &pods[i]

		for _, status := range pod.Status.ContainerStatuses {
			terminated := lastTerminated(status)
			if terminated == nil || terminated.Reason != "OOMKilled" {
				continue
			}

			limit := memoryLimit(pod, status.Name)

			evidence := fmt.Sprintf("%s has restarted %d times", status.Name, status.RestartCount)
			if limit != "" {
				evidence += fmt.Sprintf(" against a memory limit of %s", limit)
			}

			return Explanation{
				Cause: fmt.Sprintf("%s in %s was killed for using more memory than it was allowed",
					status.Name, pod.Name),
				Evidence:  evidence,
				Reproduce: append(describe(subject, pod.Name), previousLogs(subject, pod.Name, status.Name)),
				Since:     terminated.FinishedAt.Time,
			}, true
		}
	}

	return Explanation{}, false
}

func crashLooping(subject Subject, pods []corev1.Pod, _ []corev1.Event) (Explanation, bool) {
	for i := range pods {
		pod := &pods[i]

		for _, status := range pod.Status.ContainerStatuses {
			waiting := status.State.Waiting
			terminated := lastTerminated(status)

			if waiting == nil || waiting.Reason != "CrashLoopBackOff" {
				continue
			}

			cause := fmt.Sprintf("%s in %s keeps crashing on start", status.Name, pod.Name)
			evidence := fmt.Sprintf("restarted %d times", status.RestartCount)

			at := time.Time{}
			if terminated != nil {
				cause = fmt.Sprintf("%s in %s exits with code %d and is restarted",
					status.Name, pod.Name, terminated.ExitCode)
				evidence = fmt.Sprintf("restarted %d times, last exit code %d",
					status.RestartCount, terminated.ExitCode)
				at = terminated.FinishedAt.Time
			}

			return Explanation{
				Cause:     cause,
				Evidence:  evidence,
				Reproduce: append(describe(subject, pod.Name), previousLogs(subject, pod.Name, status.Name)),
				Since:     at,
			}, true
		}
	}

	return Explanation{}, false
}

func imageUnavailable(subject Subject, pods []corev1.Pod, _ []corev1.Event) (Explanation, bool) {
	for i := range pods {
		pod := &pods[i]

		for _, status := range pod.Status.ContainerStatuses {
			waiting := status.State.Waiting
			if waiting == nil {
				continue
			}
			if waiting.Reason != "ImagePullBackOff" && waiting.Reason != "ErrImagePull" {
				continue
			}

			return Explanation{
				Cause: fmt.Sprintf("%s cannot pull the image %s for %s",
					pod.Name, status.Image, status.Name),
				Evidence:  firstSentence(waiting.Message, waiting.Reason),
				Reproduce: describe(subject, pod.Name),
				Since:     pod.CreationTimestamp.Time,
			}, true
		}
	}

	return Explanation{}, false
}

func probeFailing(subject Subject, pods []corev1.Pod, events []corev1.Event) (Explanation, bool) {
	for _, event := range events {
		if event.Reason != "Unhealthy" {
			continue
		}

		return Explanation{
			Cause: fmt.Sprintf("%s is running but never becomes ready: %s",
				event.InvolvedObject.Name, firstSentence(event.Message, event.Reason)),
			Evidence:  fmt.Sprintf("the probe has failed %d times since %s", event.Count, since(eventTime(event))),
			Reproduce: describe(subject, event.InvolvedObject.Name),
			Since:     eventTime(event),
		}, true
	}

	return Explanation{}, false
}

func settled(subject Subject, pods []corev1.Pod, events []corev1.Event) Explanation {
	if subject.ReplicasReady == subject.ReplicasDesired && subject.ReplicasDesired > 0 {
		return Explanation{
			Cause:     fmt.Sprintf("%s is healthy: %d of %d replicas are ready", subject.Name, subject.ReplicasReady, subject.ReplicasDesired),
			Evidence:  "no pod reports a failing container, and no warning has been recorded",
			Reproduce: []string{rollout(subject)},
		}
	}

	explanation := Explanation{
		Cause: fmt.Sprintf("%s has %d of %d replicas ready and no single failing container to blame",
			subject.Name, subject.ReplicasReady, subject.ReplicasDesired),
		Evidence:  "the pods are neither crashing nor unschedulable, so the rollout is probably still in progress",
		Reproduce: []string{rollout(subject)},
	}

	if len(events) > 0 {
		explanation.Evidence = fmt.Sprintf("the newest warning is %q: %s",
			events[0].Reason, firstSentence(events[0].Message, events[0].Reason))
		explanation.Since = eventTime(events[0])
	}

	if len(pods) > 0 {
		explanation.Reproduce = append(explanation.Reproduce, describe(subject, pods[0].Name)...)
	}

	return explanation
}

func describe(subject Subject, pod string) []string {
	return []string{
		fmt.Sprintf("kubectl -n %s describe pod %s", subject.Namespace, pod),
		fmt.Sprintf("kubectl -n %s get events --field-selector involvedObject.name=%s --sort-by .lastTimestamp",
			subject.Namespace, pod),
	}
}

func previousLogs(subject Subject, pod, container string) string {
	return fmt.Sprintf("kubectl -n %s logs %s -c %s --previous", subject.Namespace, pod, container)
}

func rollout(subject Subject) string {
	return fmt.Sprintf("kubectl -n %s rollout status %s/%s",
		subject.Namespace, strings.ToLower(subject.Kind), subject.Name)
}

func lastTerminated(status corev1.ContainerStatus) *corev1.ContainerStateTerminated {
	if status.LastTerminationState.Terminated != nil {
		return status.LastTerminationState.Terminated
	}

	return status.State.Terminated
}

func memoryLimit(pod *corev1.Pod, container string) string {
	for i := range pod.Spec.Containers {
		if pod.Spec.Containers[i].Name != container {
			continue
		}
		if limit, ok := pod.Spec.Containers[i].Resources.Limits[corev1.ResourceMemory]; ok {
			return limit.String()
		}
	}

	return ""
}

func firstSentence(message, fallback string) string {
	trimmed := strings.TrimSpace(message)
	if trimmed == "" {
		return fallback
	}

	if index := strings.IndexAny(trimmed, ".\n"); index > 0 {
		return trimmed[:index]
	}

	return trimmed
}

func since(at time.Time) string {
	if at.IsZero() {
		return "an unknown time"
	}

	return at.Format(time.RFC3339)
}

func eventTime(event corev1.Event) time.Time {
	if !event.LastTimestamp.IsZero() {
		return event.LastTimestamp.Time
	}
	if event.EventTime.Time.IsZero() {
		return event.CreationTimestamp.Time
	}

	return event.EventTime.Time
}
