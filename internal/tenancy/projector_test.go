package tenancy_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	governv1 "github.com/marstack-labs/marstack-govern/gen/marstack/govern/v1"
	"github.com/marstack-labs/marstack-govern/internal/db/dbtest"
	"github.com/marstack-labs/marstack-govern/internal/tenancy"
)

func TestADivisionReachesTheApiThroughTheProjection(t *testing.T) {
	pool := dbtest.Migrated(t)
	store := tenancy.NewStore(pool)
	service := tenancy.NewService(store)
	ctx := t.Context()

	division := newDivision()
	reconciler, c := newReconciler(t, division)
	reconcile(t, ctx, reconciler, division.Name)

	recorder := &recordingPublisher{}
	project(t, ctx, c, store, recorder, division.Name)

	response, err := service.ListDivisions(ctx, connect.NewRequest(&governv1.ListDivisionsRequest{}))
	if err != nil {
		t.Fatalf("list divisions: %v", err)
	}

	if len(response.Msg.GetDivisions()) != 1 {
		t.Fatalf("got %d divisions, want 1", len(response.Msg.GetDivisions()))
	}

	got := response.Msg.GetDivisions()[0]
	if got.GetName() != "payments" || got.GetDisplayName() != "Payments" {
		t.Errorf("identity: got %q / %q", got.GetName(), got.GetDisplayName())
	}
	if got.GetPhase() != governv1.Division_PHASE_ACTIVE {
		t.Errorf("phase: got %v", got.GetPhase())
	}
	if got.GetQuota().GetCpuMillicores() != 8000 {
		t.Errorf("cpu quota: got %d, want 8000", got.GetQuota().GetCpuMillicores())
	}
	if len(got.GetNamespaces()) != 2 {
		t.Errorf("namespaces: got %v", got.GetNamespaces())
	}
	if response.Msg.GetFreshness().GetObservedAt() == nil {
		t.Error("the response carries no projection time")
	}

	if len(recorder.events) != 1 {
		t.Fatalf("published %d events, want 1", len(recorder.events))
	}
	if recorder.events[0].GetType() != governv1.StreamEvent_TYPE_DIVISION_CHANGED {
		t.Errorf("event type: got %v", recorder.events[0].GetType())
	}
}

func TestIsolationIsReportedFromTheCluster(t *testing.T) {
	pool := dbtest.Migrated(t)
	store := tenancy.NewStore(pool)
	service := tenancy.NewService(store)
	ctx := t.Context()

	division := newDivision()
	reconciler, c := newReconciler(t, division)
	reconcile(t, ctx, reconciler, division.Name)
	project(t, ctx, c, store, nil, division.Name)

	response, err := service.ListNamespaces(ctx, connect.NewRequest(&governv1.ListNamespacesRequest{Division: "payments"}))
	if err != nil {
		t.Fatalf("list namespaces: %v", err)
	}

	if len(response.Msg.GetNamespaces()) != 2 {
		t.Fatalf("got %d namespaces, want 2", len(response.Msg.GetNamespaces()))
	}

	for _, namespace := range response.Msg.GetNamespaces() {
		if !namespace.GetDefaultDenyPresent() {
			t.Errorf("%s reports no default-deny policy", namespace.GetName())
		}
		if !namespace.GetLimitRangePresent() {
			t.Errorf("%s reports no limit range", namespace.GetName())
		}
		if namespace.GetEnvironment() == "" {
			t.Errorf("%s reports no environment", namespace.GetName())
		}
	}
}

func TestUsageIsSummedFromNamespaceQuotas(t *testing.T) {
	pool := dbtest.Migrated(t)
	store := tenancy.NewStore(pool)
	ctx := t.Context()

	division := newDivision()
	reconciler, c := newReconciler(t, division)
	reconcile(t, ctx, reconciler, division.Name)

	for _, namespace := range []string{"payments-dev", "payments-prod"} {
		quota := &corev1.ResourceQuota{}
		key := types.NamespacedName{Namespace: namespace, Name: tenancy.ResourceQuotaName}
		if err := c.Get(ctx, key, quota); err != nil {
			t.Fatalf("get quota in %s: %v", namespace, err)
		}

		quota.Status.Used = corev1.ResourceList{
			corev1.ResourceRequestsCPU:    resource.MustParse("1500m"),
			corev1.ResourceRequestsMemory: resource.MustParse("2Gi"),
			corev1.ResourcePods:           resource.MustParse("4"),
		}
		if err := c.Status().Update(ctx, quota); err != nil {
			t.Fatalf("update quota status in %s: %v", namespace, err)
		}
	}

	project(t, ctx, c, store, nil, division.Name)

	projected, err := store.GetDivision(ctx, "payments")
	if err != nil {
		t.Fatalf("get division: %v", err)
	}

	if projected.UsedCPU != 3000 {
		t.Errorf("used cpu: got %d, want 3000", projected.UsedCPU)
	}
	if projected.UsedPods != 8 {
		t.Errorf("used pods: got %d, want 8", projected.UsedPods)
	}
}

func TestDeletingADivisionClearsTheProjection(t *testing.T) {
	pool := dbtest.Migrated(t)
	store := tenancy.NewStore(pool)
	ctx := t.Context()

	division := newDivision()
	reconciler, c := newReconciler(t, division)
	reconcile(t, ctx, reconciler, division.Name)
	project(t, ctx, c, store, nil, division.Name)

	if err := c.Delete(ctx, division); err != nil {
		t.Fatalf("delete division: %v", err)
	}

	project(t, ctx, c, store, nil, division.Name)

	divisions, err := store.ListDivisions(ctx, nil)
	if err != nil {
		t.Fatalf("list divisions: %v", err)
	}
	if len(divisions) != 0 {
		t.Fatalf("the projection kept %d divisions", len(divisions))
	}

	namespaces, err := store.ListNamespaces(ctx, "")
	if err != nil {
		t.Fatalf("list namespaces: %v", err)
	}
	if len(namespaces) != 0 {
		t.Fatalf("the projection kept %d namespaces", len(namespaces))
	}
}

func TestMembershipIsNotOursToAnswer(t *testing.T) {
	pool := dbtest.Migrated(t)
	service := tenancy.NewService(tenancy.NewStore(pool))

	_, err := service.ListMembers(t.Context(), connect.NewRequest(&governv1.ListMembersRequest{Division: "payments"}))
	if connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("got %v, want unimplemented", err)
	}
}

func project(
	t *testing.T,
	ctx context.Context,
	c client.Client,
	store *tenancy.Store,
	publisher tenancy.Publisher,
	name string,
) {
	t.Helper()

	projector := &tenancy.Projector{Client: c, Store: store, Publisher: publisher}
	if _, err := projector.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: name}}); err != nil {
		t.Fatalf("project: %v", err)
	}
}

type recordingPublisher struct {
	events []*governv1.StreamEvent
}

func (p *recordingPublisher) Publish(event *governv1.StreamEvent) {
	p.events = append(p.events, event)
}

func TestADivisionRecreatedWithTheSameNameReplacesTheOldRow(t *testing.T) {
	pool := dbtest.Migrated(t)
	store := tenancy.NewStore(pool)
	ctx := t.Context()

	first := newDivision()
	first.UID = "11111111-1111-1111-1111-111111111111"

	reconciler, c := newReconciler(t, first)
	reconcile(t, ctx, reconciler, first.Name)
	project(t, ctx, c, store, &recordingPublisher{}, first.Name)

	second := newDivision()
	second.UID = "22222222-2222-2222-2222-222222222222"

	reconciler, c = newReconciler(t, second)
	reconcile(t, ctx, reconciler, second.Name)
	project(t, ctx, c, store, &recordingPublisher{}, second.Name)

	var (
		rows int
		uid  string
	)

	if err := pool.QueryRow(ctx,
		`SELECT count(*), max(uid::text) FROM divisions WHERE name = $1`, first.Name,
	).Scan(&rows, &uid); err != nil {
		t.Fatalf("read divisions: %v", err)
	}

	if rows != 1 {
		t.Fatalf("got %d rows for one division, want the old one replaced", rows)
	}
	if uid != string(second.UID) {
		t.Fatalf("uid: got %s, want the division that actually exists now", uid)
	}
}
