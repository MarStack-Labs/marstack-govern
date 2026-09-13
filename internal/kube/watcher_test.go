package kube

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestWatcherStopsCleanlyWhenCancelledBeforeCachesSync(t *testing.T) {
	client := fake.NewSimpleClientset(deployment(1, 1, 1, 1))
	watcher := NewWatcher(client, time.Hour, 1)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := watcher.Run(ctx); err != nil {
		t.Fatalf("shutting down during startup is not a failure: %v", err)
	}

	if _, open := <-watcher.Events(); open {
		t.Fatal("the event channel was left open after shutdown")
	}
}

func TestWatcherEmitsExistingWorkloads(t *testing.T) {
	client := fake.NewSimpleClientset(
		deployment(2, 2, 1, 1),
		&appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{
				UID:             "22222222-2222-2222-2222-222222222222",
				Name:            "db",
				Namespace:       "payments-dev",
				Labels:          map[string]string{LabelDivision: "payments"},
				ResourceVersion: "7",
			},
		},
	)

	watcher := NewWatcher(client, time.Hour, 16)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- watcher.Run(ctx) }()

	seen := map[string]WorkloadEvent{}
	deadline := time.After(10 * time.Second)

	for len(seen) < 2 {
		select {
		case event := <-watcher.Events():
			seen[event.Workload.Kind+"/"+event.Workload.Name] = event
		case <-deadline:
			t.Fatalf("timed out with %d events: %v", len(seen), seen)
		}
	}

	api, ok := seen["Deployment/api"]
	if !ok {
		t.Fatal("no event for the deployment")
	}
	if api.Type != EventAdded {
		t.Errorf("deployment event type: got %q, want %q", api.Type, EventAdded)
	}
	if api.Workload.Health != HealthHealthy {
		t.Errorf("deployment health: got %q", api.Workload.Health)
	}

	db, ok := seen["StatefulSet/db"]
	if !ok {
		t.Fatal("no event for the statefulset")
	}
	if db.Workload.Division != "payments" {
		t.Errorf("statefulset division: got %q, want payments", db.Workload.Division)
	}

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("watcher returned: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("watcher did not stop")
	}
}
