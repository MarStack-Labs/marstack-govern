package delivery_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/marstack-labs/marstack-govern/internal/audit"
	"github.com/marstack-labs/marstack-govern/internal/delivery"
)

func TestASyncedApplicationHasNoDrift(t *testing.T) {
	argo := delivery.NewArgo(newCluster(t, true, application("payments", "Synced", "abc123", "main", resource("api", "Synced"))))

	drift := delivery.NewDrift(argo, nil)

	response, err := drift.Of(t.Context(), "payments-dev", "Deployment", "api")
	if err != nil {
		t.Fatalf("drift: %v", err)
	}

	if response.GetDrifted() {
		t.Fatalf("a synced application reported drift: %v", response.GetFields())
	}
	if !strings.HasPrefix(response.GetApplication(), "payments") {
		t.Errorf("application: got %q", response.GetApplication())
	}
}

func TestAnOutOfSyncResourceIsNamed(t *testing.T) {
	argo := delivery.NewArgo(newCluster(t, true,
		application("payments", "OutOfSync", "abc123", "main", resource("api", "OutOfSync")),
	))

	drift := delivery.NewDrift(argo, nil)

	response, err := drift.Of(t.Context(), "payments-dev", "Deployment", "api")
	if err != nil {
		t.Fatalf("drift: %v", err)
	}

	if !response.GetDrifted() {
		t.Fatal("an out-of-sync resource did not register as drift")
	}

	found := false
	for _, field := range response.GetFields() {
		if strings.Contains(field.GetPath(), "Deployment/api") {
			found = true
			if field.GetDesired() != "Synced" || field.GetLive() != "OutOfSync" {
				t.Errorf("field: got %+v", field)
			}
		}
	}
	if !found {
		t.Errorf("the drifted resource was not named: %v", response.GetFields())
	}
}

func TestTheAuditTrailNamesWhoChangedIt(t *testing.T) {
	argo := delivery.NewArgo(newCluster(t, true,
		application("payments", "OutOfSync", "abc123", "main", resource("api", "OutOfSync")),
	))

	changed := time.Now().Add(-20 * time.Minute)

	drift := delivery.NewDrift(argo, mutations{records: []audit.Record{
		{Seq: 2, Event: audit.Event{Actor: "dev@example.test", Verb: "patch", EventAt: changed}},
		{Seq: 1, Event: audit.Event{Actor: "someone@example.test", Verb: "get", EventAt: changed.Add(-time.Hour)}},
	}})

	response, err := drift.Of(t.Context(), "payments-dev", "Deployment", "api")
	if err != nil {
		t.Fatalf("drift: %v", err)
	}

	for _, field := range response.GetFields() {
		if !strings.Contains(field.GetPath(), "Deployment/api") {
			continue
		}

		if field.GetChangedBy() != "dev@example.test" {
			t.Errorf("changed by: got %q, want the actor who patched it", field.GetChangedBy())
		}
		if field.GetChangedAt() == nil {
			t.Error("the change carries no time")
		}

		return
	}

	t.Fatal("the drifted resource was not reported")
}

func TestTrackingABranchIsNotDrift(t *testing.T) {
	argo := delivery.NewArgo(newCluster(t, true,
		application("payments", "Synced", "0f1e2d3c4b5a6978", "main", resource("api", "Synced")),
	))

	response, err := delivery.NewDrift(argo, nil).Of(t.Context(), "payments-dev", "Deployment", "api")
	if err != nil {
		t.Fatalf("drift: %v", err)
	}

	if response.GetDrifted() {
		t.Fatalf("an app tracking a branch was called drifted because its revision is a commit: %v",
			response.GetFields())
	}
	if !strings.Contains(response.GetApplication(), "0f1e2d3c4b5a") {
		t.Errorf("the synced revision is not reported as context: %q", response.GetApplication())
	}
}

func TestAnApplicationArgoCallsOutOfSyncIsDrift(t *testing.T) {
	argo := delivery.NewArgo(newCluster(t, true,
		application("payments", "OutOfSync", "abc123", "main", resource("api", "Synced")),
	))

	response, err := delivery.NewDrift(argo, nil).Of(t.Context(), "payments-dev", "Deployment", "api")
	if err != nil {
		t.Fatalf("drift: %v", err)
	}

	if !response.GetDrifted() {
		t.Fatal("argo said OutOfSync and the platform disagreed")
	}
	if response.GetFields()[0].GetPath() != "status.sync.status" {
		t.Errorf("field: got %+v", response.GetFields()[0])
	}
}

func TestAWorkloadOutsideGitOpsSaysSo(t *testing.T) {
	argo := delivery.NewArgo(newCluster(t, true,
		application("payments", "Synced", "abc123", "main", resource("other", "Synced")),
	))

	drift := delivery.NewDrift(argo, nil)

	_, err := drift.Of(t.Context(), "payments-dev", "Deployment", "api")
	if err == nil {
		t.Fatal("a workload with no application was reported on anyway")
	}
	if !strings.Contains(err.Error(), "not managed by argo cd") {
		t.Errorf("error: %v", err)
	}
}

func TestWithoutArgoNothingIsCompared(t *testing.T) {
	argo := delivery.NewArgo(newCluster(t, false))

	if argo.Available() {
		t.Fatal("a cluster without argo reported it as present")
	}

	_, err := delivery.NewDrift(argo, nil).Of(t.Context(), "payments-dev", "Deployment", "api")
	if err == nil || !strings.Contains(err.Error(), "argo cd is not installed") {
		t.Fatalf("got %v, want the absent-argo error", err)
	}
}

type mutations struct {
	records []audit.Record
}

func (m mutations) List(context.Context, audit.Query) ([]audit.Record, error) {
	return m.records, nil
}

func newCluster(t *testing.T, withArgo bool, objects ...client.Object) client.Client {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	mapper := meta.NewDefaultRESTMapper(nil)

	if withArgo {
		scheme.AddKnownTypeWithName(delivery.ApplicationGVK, &unstructured.Unstructured{})

		list := delivery.ApplicationGVK
		list.Kind += "List"
		scheme.AddKnownTypeWithName(list, &unstructured.UnstructuredList{})

		mapper.Add(delivery.ApplicationGVK, meta.RESTScopeNamespace)
	}

	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithRESTMapper(mapper).
		WithObjects(objects...).
		Build()
}

func application(name, syncStatus, syncedRevision, target string, resources ...map[string]any) *unstructured.Unstructured {
	items := make([]any, 0, len(resources))
	for _, item := range resources {
		items = append(items, item)
	}

	object := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{
			"source": map[string]any{
				"repoURL":        "https://gitlab.example.test/payments/manifests",
				"path":           "overlays/dev",
				"targetRevision": target,
			},
			"destination": map[string]any{"namespace": "payments-dev"},
		},
		"status": map[string]any{
			"sync":      map[string]any{"status": syncStatus, "revision": syncedRevision},
			"health":    map[string]any{"status": "Healthy"},
			"resources": items,
		},
	}}

	object.SetGroupVersionKind(delivery.ApplicationGVK)
	object.SetName(name)
	object.SetNamespace("argocd")

	return object
}

func resource(name, status string) map[string]any {
	return map[string]any{
		"group":     "apps",
		"kind":      "Deployment",
		"namespace": "payments-dev",
		"name":      name,
		"status":    status,
		"health":    map[string]any{"status": "Healthy"},
	}
}
