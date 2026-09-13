package catalog_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"connectrpc.com/connect"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	governv1 "github.com/marstack-labs/marstack-govern/gen/marstack/govern/v1"
	"github.com/marstack-labs/marstack-govern/internal/api"
	"github.com/marstack-labs/marstack-govern/internal/catalog"
	"github.com/marstack-labs/marstack-govern/internal/db/dbtest"
	"github.com/marstack-labs/marstack-govern/internal/kube"
)

func TestClusterReachesTheApiWithoutAnyoneTypingAnything(t *testing.T) {
	pool := dbtest.Migrated(t)
	store := catalog.NewStore(pool)
	service := catalog.NewService(store)
	hub := api.NewHub(16)

	client := fake.NewSimpleClientset(deployment("api", "payments-dev", "payments", 2, 2))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	watcher := kube.NewWatcher(client, time.Hour, 64)
	projector := catalog.NewProjector(store, watcher.Events(), hub, slog.New(slog.DiscardHandler))

	go func() { _ = watcher.Run(ctx) }()
	go func() { _ = projector.Run(ctx) }()

	stream, _ := hub.Subscribe(ctx, "")

	workloads := waitForWorkloads(t, ctx, service, 1)

	got := workloads[0]
	if got.GetName() != "api" {
		t.Errorf("name: got %q, want api", got.GetName())
	}
	if got.GetDivision() != "payments" {
		t.Errorf("division: got %q, want payments", got.GetDivision())
	}
	if got.GetHealth() != governv1.Workload_HEALTH_HEALTHY {
		t.Errorf("health: got %v", got.GetHealth())
	}
	if got.GetRequested().GetCpuMillicores() != 250 {
		t.Errorf("cpu request: got %d, want 250", got.GetRequested().GetCpuMillicores())
	}

	select {
	case event := <-stream:
		if event.GetType() != governv1.StreamEvent_TYPE_WORKLOAD_CHANGED {
			t.Errorf("stream event type: got %v", event.GetType())
		}
		if event.GetCursor() == "" {
			t.Error("the stream event carries no cursor")
		}
	case <-time.After(10 * time.Second):
		t.Error("the change never reached the stream")
	}
}

func TestFreshnessReportsTheProjection(t *testing.T) {
	pool := dbtest.Migrated(t)
	store := catalog.NewStore(pool)
	service := catalog.NewService(store)
	ctx := t.Context()

	if err := store.MarkSourceHealthy(ctx, catalog.SourceKubernetes, "4711", time.Now()); err != nil {
		t.Fatalf("mark healthy: %v", err)
	}

	response, err := service.ListWorkloads(ctx, connect.NewRequest(&governv1.ListWorkloadsRequest{}))
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	freshness := response.Msg.GetFreshness()
	if freshness.GetResourceVersion() != "4711" {
		t.Errorf("resource version: got %q, want 4711", freshness.GetResourceVersion())
	}
	if freshness.GetObservedAt() == nil {
		t.Error("freshness carries no observation time")
	}
	if len(freshness.GetDegraded()) != 0 {
		t.Errorf("unexpected degraded sources: %v", freshness.GetDegraded())
	}

	if err := store.MarkSourceDegraded(ctx, "mimir", "connection refused"); err != nil {
		t.Fatalf("mark degraded: %v", err)
	}

	response, err = service.ListWorkloads(ctx, connect.NewRequest(&governv1.ListWorkloadsRequest{}))
	if err != nil {
		t.Fatalf("list after degradation: %v", err)
	}

	degraded := response.Msg.GetFreshness().GetDegraded()
	if len(degraded) != 1 || degraded[0].GetSource() != "mimir" {
		t.Fatalf("degraded sources: got %v", degraded)
	}
	if degraded[0].GetReason() != "connection refused" {
		t.Errorf("reason: got %q", degraded[0].GetReason())
	}
}

func TestStoreReplacesARecreatedWorkload(t *testing.T) {
	pool := dbtest.Migrated(t)
	store := catalog.NewStore(pool)
	ctx := t.Context()

	first := kube.WorkloadFromDeployment(deployment("api", "payments-dev", "payments", 1, 1))
	if err := store.UpsertWorkload(ctx, first); err != nil {
		t.Fatalf("upsert first: %v", err)
	}

	recreated := first
	recreated.UID = "99999999-9999-9999-9999-999999999999"
	if err := store.UpsertWorkload(ctx, recreated); err != nil {
		t.Fatalf("upsert recreated: %v", err)
	}

	workloads, _, err := store.ListWorkloads(ctx, catalog.Filter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(workloads) != 1 {
		t.Fatalf("got %d workloads, want 1", len(workloads))
	}
	if workloads[0].UID != recreated.UID {
		t.Errorf("uid: got %s, want %s", workloads[0].UID, recreated.UID)
	}
}

func TestListPaginatesWithACursor(t *testing.T) {
	pool := dbtest.Migrated(t)
	store := catalog.NewStore(pool)
	ctx := t.Context()

	for _, name := range []string{"alpha", "bravo", "charlie"} {
		workload := kube.WorkloadFromDeployment(deployment(name, "payments-dev", "payments", 1, 1))
		workload.UID = uidFor(name)
		if err := store.UpsertWorkload(ctx, workload); err != nil {
			t.Fatalf("upsert %s: %v", name, err)
		}
	}

	first, next, err := store.ListWorkloads(ctx, catalog.Filter{Limit: 2})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(first) != 2 || next == "" {
		t.Fatalf("first page: got %d rows, cursor %q", len(first), next)
	}

	second, next, err := store.ListWorkloads(ctx, catalog.Filter{Limit: 2, Cursor: next})
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(second) != 1 {
		t.Fatalf("second page: got %d rows, want 1", len(second))
	}
	if next != "" {
		t.Errorf("cursor after the last page: got %q, want empty", next)
	}
	if second[0].Name != "charlie" {
		t.Errorf("last row: got %q, want charlie", second[0].Name)
	}
}

func TestListFiltersByDivisionAndHealth(t *testing.T) {
	pool := dbtest.Migrated(t)
	store := catalog.NewStore(pool)
	ctx := t.Context()

	healthy := kube.WorkloadFromDeployment(deployment("api", "payments-dev", "payments", 1, 1))
	healthy.UID = uidFor("api")

	broken := kube.WorkloadFromDeployment(deployment("worker", "erp-dev", "erp", 2, 0))
	broken.UID = uidFor("worker")

	for _, workload := range []kube.Workload{healthy, broken} {
		if err := store.UpsertWorkload(ctx, workload); err != nil {
			t.Fatalf("upsert %s: %v", workload.Name, err)
		}
	}

	byDivision, _, err := store.ListWorkloads(ctx, catalog.Filter{Division: "erp"})
	if err != nil {
		t.Fatalf("filter by division: %v", err)
	}
	if len(byDivision) != 1 || byDivision[0].Name != "worker" {
		t.Fatalf("filter by division: got %v", byDivision)
	}

	byHealth, _, err := store.ListWorkloads(ctx, catalog.Filter{Health: "degraded"})
	if err != nil {
		t.Fatalf("filter by health: %v", err)
	}
	if len(byHealth) != 1 || byHealth[0].Name != "worker" {
		t.Fatalf("filter by health: got %v", byHealth)
	}
}

func waitForWorkloads(t *testing.T, ctx context.Context, service *catalog.Service, want int) []*governv1.Workload {
	t.Helper()

	deadline := time.Now().Add(15 * time.Second)
	for {
		response, err := service.ListWorkloads(ctx, connect.NewRequest(&governv1.ListWorkloadsRequest{}))
		if err != nil {
			t.Fatalf("list workloads: %v", err)
		}

		if len(response.Msg.GetWorkloads()) >= want {
			return response.Msg.GetWorkloads()
		}

		if time.Now().After(deadline) {
			t.Fatalf("only %d workloads reached the api, want %d", len(response.Msg.GetWorkloads()), want)
		}

		time.Sleep(50 * time.Millisecond)
	}
}

func uidFor(name string) string {
	sum := sha256.Sum256([]byte(name))
	return fmt.Sprintf("%x-%x-%x-%x-%x", sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}

func uidType(uid string) types.UID {
	return types.UID(uid)
}

func deployment(name, namespace, division string, desired, ready int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			UID:               uidType(uidFor(name)),
			Name:              name,
			Namespace:         namespace,
			Labels:            map[string]string{kube.LabelDivision: division},
			Generation:        1,
			ResourceVersion:   "100",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Hour)),
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &desired,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  name,
						Image: "registry.internal/" + name + "@sha256:abc",
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("250m"),
								corev1.ResourceMemory: resource.MustParse("512Mi"),
							},
						},
					}},
				},
			},
		},
		Status: appsv1.DeploymentStatus{
			ReadyReplicas:      ready,
			ObservedGeneration: 1,
		},
	}
}
