package diagnostics_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/marstack-labs/marstack-govern/internal/diagnostics"
	"github.com/marstack-labs/marstack-govern/internal/metrics"
)

func TestOutOfMemoryIsNamedAsTheCause(t *testing.T) {
	pod := podNamed("api-7d9f-x1")
	pod.Spec.Containers = []corev1.Container{{
		Name: "api",
		Resources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("512Mi")},
		},
	}}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:         "api",
		RestartCount: 4,
		LastTerminationState: corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{
				Reason:     "OOMKilled",
				ExitCode:   137,
				FinishedAt: metav1.NewTime(time.Now().Add(-2 * time.Minute)),
			},
		},
		State: corev1.ContainerState{
			Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
		},
	}}

	explanation := diagnostics.Explain(subject(1, 0), []corev1.Pod{pod}, nil)

	if !strings.Contains(explanation.Cause, "more memory than it was allowed") {
		t.Fatalf("cause: %q", explanation.Cause)
	}
	if !strings.Contains(explanation.Evidence, "512Mi") {
		t.Errorf("evidence does not mention the limit: %q", explanation.Evidence)
	}
	if !strings.Contains(explanation.Evidence, "restarted 4 times") {
		t.Errorf("evidence does not count the restarts: %q", explanation.Evidence)
	}

	joined := strings.Join(explanation.Reproduce, "\n")
	if !strings.Contains(joined, "logs api-7d9f-x1 -c api --previous") {
		t.Errorf("no command to read the logs of the crashed container: %v", explanation.Reproduce)
	}
}

func TestACrashLoopReportsTheExitCode(t *testing.T) {
	pod := podNamed("api-7d9f-x1")
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:         "api",
		RestartCount: 7,
		LastTerminationState: corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Reason: "Error"},
		},
		State: corev1.ContainerState{
			Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
		},
	}}

	explanation := diagnostics.Explain(subject(1, 0), []corev1.Pod{pod}, nil)

	if !strings.Contains(explanation.Cause, "exits with code 1") {
		t.Fatalf("cause: %q", explanation.Cause)
	}
	if !strings.Contains(explanation.Evidence, "restarted 7 times") {
		t.Errorf("evidence: %q", explanation.Evidence)
	}
}

func TestAPodThatCannotBeScheduledSaysWhy(t *testing.T) {
	pod := podNamed("api-7d9f-x1")
	pod.Status.Phase = corev1.PodPending
	pod.Status.Conditions = []corev1.PodCondition{{
		Type:    corev1.PodScheduled,
		Status:  corev1.ConditionFalse,
		Reason:  "Unschedulable",
		Message: "0/6 nodes are available: insufficient cpu. preemption is not helpful",
	}}

	explanation := diagnostics.Explain(subject(3, 2), []corev1.Pod{pod}, nil)

	if !strings.Contains(explanation.Cause, "cannot be scheduled") {
		t.Fatalf("cause: %q", explanation.Cause)
	}
	if !strings.Contains(explanation.Cause, "insufficient cpu") {
		t.Errorf("the scheduler's own words are missing: %q", explanation.Cause)
	}
	if strings.Contains(explanation.Cause, "preemption") {
		t.Errorf("the cause should stop at the first sentence: %q", explanation.Cause)
	}
}

func TestAnImageThatCannotBePulledIsNamed(t *testing.T) {
	pod := podNamed("api-7d9f-x1")
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:  "api",
		Image: "registry.internal/payments/api:1.4.2",
		State: corev1.ContainerState{
			Waiting: &corev1.ContainerStateWaiting{
				Reason:  "ImagePullBackOff",
				Message: "Back-off pulling image",
			},
		},
	}}

	explanation := diagnostics.Explain(subject(1, 0), []corev1.Pod{pod}, nil)

	if !strings.Contains(explanation.Cause, "cannot pull the image registry.internal/payments/api:1.4.2") {
		t.Fatalf("cause: %q", explanation.Cause)
	}
}

func TestAFailingProbeIsReportedAsNeverReady(t *testing.T) {
	pod := podNamed("api-7d9f-x1")
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:  "api",
		Ready: false,
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
	}}

	event := corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "api.1", Namespace: "payments-dev"},
		Type:           corev1.EventTypeWarning,
		Reason:         "Unhealthy",
		Message:        "Readiness probe failed: HTTP probe failed with statuscode: 503",
		Count:          12,
		InvolvedObject: corev1.ObjectReference{Name: "api-7d9f-x1"},
		LastTimestamp:  metav1.NewTime(time.Now().Add(-time.Minute)),
	}

	explanation := diagnostics.Explain(subject(1, 0), []corev1.Pod{pod}, []corev1.Event{event})

	if !strings.Contains(explanation.Cause, "never becomes ready") {
		t.Fatalf("cause: %q", explanation.Cause)
	}
	if !strings.Contains(explanation.Evidence, "failed 12 times") {
		t.Errorf("evidence: %q", explanation.Evidence)
	}
}

func TestAHealthyWorkloadSaysSo(t *testing.T) {
	pod := podNamed("api-7d9f-x1")
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:  "api",
		Ready: true,
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
	}}

	explanation := diagnostics.Explain(subject(2, 2), []corev1.Pod{pod}, nil)

	if !strings.Contains(explanation.Cause, "is healthy") {
		t.Fatalf("cause: %q", explanation.Cause)
	}
	if len(explanation.Reproduce) == 0 {
		t.Error("even a healthy workload should say how to check it")
	}
}

func TestAProgressingRolloutIsNotBlamedOnAContainer(t *testing.T) {
	pod := podNamed("api-7d9f-x1")
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:  "api",
		Ready: false,
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
	}}

	explanation := diagnostics.Explain(subject(3, 1), []corev1.Pod{pod}, nil)

	if !strings.Contains(explanation.Cause, "1 of 3 replicas ready") {
		t.Fatalf("cause: %q", explanation.Cause)
	}
	if !strings.Contains(explanation.Evidence, "still in progress") {
		t.Errorf("evidence: %q", explanation.Evidence)
	}
}

func TestPodsAreFoundThroughOwnershipNotNames(t *testing.T) {
	deploymentUID := types.UID("11111111-1111-1111-1111-111111111111")
	replicaSetUID := types.UID("22222222-2222-2222-2222-222222222222")

	owned := podNamed("api-7d9f-x1")
	owned.OwnerReferences = []metav1.OwnerReference{{UID: replicaSetUID, Kind: "ReplicaSet", Name: "api-7d9f"}}

	lookalike := podNamed("api-7d9f-decoy")
	lookalike.OwnerReferences = []metav1.OwnerReference{{UID: "33333333-3333-3333-3333-333333333333", Kind: "ReplicaSet"}}

	replicaSet := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "api-7d9f",
			Namespace:       "payments-dev",
			UID:             replicaSetUID,
			OwnerReferences: []metav1.OwnerReference{{UID: deploymentUID, Kind: "Deployment", Name: "api"}},
		},
	}

	collector := &diagnostics.Collector{Client: newCluster(t, replicaSet, &owned, &lookalike)}

	pods, err := collector.Pods(t.Context(), "payments-dev", deploymentUID)
	if err != nil {
		t.Fatalf("pods: %v", err)
	}

	if len(pods) != 1 {
		t.Fatalf("got %d pods, want only the one this deployment owns", len(pods))
	}
	if pods[0].Name != "api-7d9f-x1" {
		t.Errorf("pod: got %s", pods[0].Name)
	}
}

func TestRolloutsComeBackNewestFirst(t *testing.T) {
	deploymentUID := types.UID("11111111-1111-1111-1111-111111111111")

	older := replicaSet("api-old", deploymentUID, "1", time.Now().Add(-2*time.Hour), "registry.internal/api:1.4.1")
	newer := replicaSet("api-new", deploymentUID, "2", time.Now().Add(-10*time.Minute), "registry.internal/api:1.4.2")

	collector := &diagnostics.Collector{Client: newCluster(t, older, newer)}

	rollouts, err := collector.Rollouts(t.Context(), "payments-dev", deploymentUID)
	if err != nil {
		t.Fatalf("rollouts: %v", err)
	}

	if len(rollouts) != 2 {
		t.Fatalf("got %d rollouts", len(rollouts))
	}
	if rollouts[0].Revision != "2" {
		t.Errorf("newest revision: got %s", rollouts[0].Revision)
	}
	if rollouts[0].Image != "registry.internal/api:1.4.2" {
		t.Errorf("image: got %s", rollouts[0].Image)
	}
}

func subject(desired, ready int32) diagnostics.Subject {
	return diagnostics.Subject{
		Namespace:       "payments-dev",
		Name:            "api",
		Kind:            "Deployment",
		ReplicasDesired: desired,
		ReplicasReady:   ready,
	}
}

func podNamed(name string) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         "payments-dev",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-10 * time.Minute)),
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

func replicaSet(name string, owner types.UID, revision string, at time.Time, image string) *appsv1.ReplicaSet {
	return &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         "payments-dev",
			UID:               types.UID(name),
			CreationTimestamp: metav1.NewTime(at),
			Annotations:       map[string]string{"deployment.kubernetes.io/revision": revision},
			OwnerReferences:   []metav1.OwnerReference{{UID: owner, Kind: "Deployment", Name: "api"}},
		},
		Spec: appsv1.ReplicaSetSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "api", Image: image}},
				},
			},
		},
	}
}

func newCluster(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}

func TestARolloutThatMadeThingsWorseIsFlagged(t *testing.T) {
	rolloutAt := time.Now().Add(-30 * time.Minute)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		at, _ := strconv.ParseInt(r.URL.Query().Get("time"), 10, 64)
		after := at > rolloutAt.Unix()

		value := 0.0
		switch {
		case strings.Contains(query, "histogram_quantile"):
			value = 0.12
			if after {
				value = 0.41
			}
		default:
			value = 0.002
			if after {
				value = 0.031
			}
		}

		writeSample(t, w, value)
	}))
	defer server.Close()

	correlator := &diagnostics.Correlator{
		Metrics: metrics.New(metrics.Config{BaseURL: server.URL}),
	}

	regression, err := correlator.Around(t.Context(), "payments-dev", "api", diagnostics.Rollout{
		Revision: "7",
		At:       rolloutAt,
		Image:    "registry.internal/api:1.4.2",
	})
	if err != nil {
		t.Fatalf("correlate: %v", err)
	}

	if !regression.Observed {
		t.Fatal("the series were read but nothing was observed")
	}
	if !regression.Worse() {
		t.Fatalf("a rollout that tripled p99 was not flagged: %+v", regression)
	}
	if regression.LatencyBefore != 0.12 || regression.LatencyAfter != 0.41 {
		t.Errorf("latency: got %v then %v", regression.LatencyBefore, regression.LatencyAfter)
	}
}

func TestARolloutThatChangedNothingIsNotBlamed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeSample(t, w, 0.1)
	}))
	defer server.Close()

	correlator := &diagnostics.Correlator{
		Metrics: metrics.New(metrics.Config{BaseURL: server.URL}),
	}

	regression, err := correlator.Around(t.Context(), "payments-dev", "api", diagnostics.Rollout{
		Revision: "7",
		At:       time.Now().Add(-time.Hour),
	})
	if err != nil {
		t.Fatalf("correlate: %v", err)
	}

	if regression.Worse() {
		t.Fatalf("a flat series was called a regression: %+v", regression)
	}
}

func TestWithoutMetricsNoRegressionIsClaimed(t *testing.T) {
	correlator := &diagnostics.Correlator{Metrics: metrics.New(metrics.Config{})}

	regression, err := correlator.Around(t.Context(), "payments-dev", "api", diagnostics.Rollout{
		Revision: "7",
		At:       time.Now().Add(-time.Hour),
	})
	if err != nil {
		t.Fatalf("correlate: %v", err)
	}

	if regression.Observed || regression.Worse() {
		t.Fatalf("a verdict was reached without any series: %+v", regression)
	}
	if regression.Revision != "7" {
		t.Errorf("the rollout is still named: got %q", regression.Revision)
	}
}

func writeSample(t *testing.T, w http.ResponseWriter, value float64) {
	t.Helper()

	w.Header().Set("Content-Type", "application/json")

	payload := map[string]any{
		"status": "success",
		"data": map[string]any{
			"resultType": "vector",
			"result": []any{
				map[string]any{
					"metric": map[string]string{},
					"value":  []any{float64(1), strconv.FormatFloat(value, 'f', -1, 64)},
				},
			},
		},
	}

	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Errorf("write sample: %v", err)
	}
}
