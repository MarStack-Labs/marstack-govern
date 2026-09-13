package diagnostics

import (
	"context"
	"fmt"
	"sort"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Collector struct {
	client.Client
}

func (c *Collector) Pods(ctx context.Context, namespace string, owner types.UID) ([]corev1.Pod, error) {
	owners := map[types.UID]bool{owner: true}

	replicaSets := &appsv1.ReplicaSetList{}
	if err := c.List(ctx, replicaSets, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("list replica sets in %s: %w", namespace, err)
	}

	for i := range replicaSets.Items {
		if ownedBy(replicaSets.Items[i].OwnerReferences, owners) {
			owners[replicaSets.Items[i].UID] = true
		}
	}

	pods := &corev1.PodList{}
	if err := c.List(ctx, pods, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("list pods in %s: %w", namespace, err)
	}

	owned := []corev1.Pod{}
	for i := range pods.Items {
		if ownedBy(pods.Items[i].OwnerReferences, owners) {
			owned = append(owned, pods.Items[i])
		}
	}

	return owned, nil
}

func (c *Collector) Warnings(ctx context.Context, namespace string, pods []corev1.Pod) ([]corev1.Event, error) {
	names := map[string]bool{}
	for i := range pods {
		names[pods[i].Name] = true
	}

	events := &corev1.EventList{}
	if err := c.List(ctx, events, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("list events in %s: %w", namespace, err)
	}

	warnings := []corev1.Event{}
	for i := range events.Items {
		event := events.Items[i]
		if event.Type != corev1.EventTypeWarning {
			continue
		}
		if !names[event.InvolvedObject.Name] {
			continue
		}
		warnings = append(warnings, event)
	}

	sort.SliceStable(warnings, func(i, j int) bool {
		return eventTime(warnings[i]).After(eventTime(warnings[j]))
	})

	return warnings, nil
}

type Rollout struct {
	Revision string
	At       time.Time
	Image    string
}

func (c *Collector) Rollouts(ctx context.Context, namespace string, owner types.UID) ([]Rollout, error) {
	replicaSets := &appsv1.ReplicaSetList{}
	if err := c.List(ctx, replicaSets, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("list replica sets in %s: %w", namespace, err)
	}

	owners := map[types.UID]bool{owner: true}
	rollouts := []Rollout{}

	for i := range replicaSets.Items {
		item := &replicaSets.Items[i]
		if !ownedBy(item.OwnerReferences, owners) {
			continue
		}

		rollout := Rollout{
			Revision: item.Annotations["deployment.kubernetes.io/revision"],
			At:       item.CreationTimestamp.Time,
		}
		if rollout.Revision == "" {
			rollout.Revision = item.Name
		}
		if len(item.Spec.Template.Spec.Containers) > 0 {
			rollout.Image = item.Spec.Template.Spec.Containers[0].Image
		}

		rollouts = append(rollouts, rollout)
	}

	sort.SliceStable(rollouts, func(i, j int) bool { return rollouts[i].At.After(rollouts[j].At) })

	return rollouts, nil
}

func ownedBy(references []metav1.OwnerReference, owners map[types.UID]bool) bool {
	for _, reference := range references {
		if owners[reference.UID] {
			return true
		}
	}

	return false
}
