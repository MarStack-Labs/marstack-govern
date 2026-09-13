package tenancy_test

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	governv1alpha1 "github.com/marstack-labs/marstack-govern/api/v1alpha1"
	"github.com/marstack-labs/marstack-govern/internal/kube"
	"github.com/marstack-labs/marstack-govern/internal/tenancy"
)

var globalQuotaGVK = schema.GroupVersionKind{
	Group:   "capsule.clastix.io",
	Version: "v1beta2",
	Kind:    "GlobalResourceQuota",
}

func TestWithCapsuleTheTotalIsHeldAcrossNamespaces(t *testing.T) {
	reconciler, api := newCapsuleReconciler(t, true, newDivision())

	reconcile(t, t.Context(), reconciler, "payments")

	quota := &unstructured.Unstructured{}
	quota.SetGroupVersionKind(globalQuotaGVK)
	if err := api.Get(t.Context(), types.NamespacedName{Name: "govern-division-quota-payments"}, quota); err != nil {
		t.Fatalf("no global quota was created: %v", err)
	}

	selectors, found, err := unstructured.NestedSlice(quota.Object, "spec", "namespaceSelectors")
	if err != nil || !found || len(selectors) != 1 {
		t.Fatalf("namespaceSelectors: %v found=%v err=%v", selectors, found, err)
	}

	labels, _, err := unstructured.NestedStringMap(
		selectors[0].(map[string]any), "matchLabels")
	if err != nil {
		t.Fatalf("matchLabels: %v", err)
	}
	if labels[kube.LabelDivision] != "payments" {
		t.Fatalf("the quota selects %v, not this division's namespaces", labels)
	}

	hard, _, err := unstructured.NestedStringMap(quota.Object, "spec", "quota", "hard")
	if err != nil {
		t.Fatalf("hard: %v", err)
	}
	if hard["requests.cpu"] != "8" || hard["requests.memory"] != "16Gi" {
		t.Errorf("hard: got %v, want the division's own totals", hard)
	}
	if hard["pods"] != "50" {
		t.Errorf("pods: got %q", hard["pods"])
	}

	stored := reloadDivision(t, api)
	if stored.Status.QuotaBackend != tenancy.QuotaBackendCapsule {
		t.Fatalf("backend: got %q", stored.Status.QuotaBackend)
	}
}

func TestWithCapsuleThePerNamespaceQuotaIsRemoved(t *testing.T) {
	reconciler, api := newCapsuleReconciler(t, true, newDivision())

	reconcile(t, t.Context(), reconciler, "payments")

	for _, namespace := range []string{"payments-dev", "payments-staging"} {
		leftover := &corev1.ResourceQuota{}
		err := api.Get(t.Context(), types.NamespacedName{
			Name: tenancy.ResourceQuotaName, Namespace: namespace,
		}, leftover)

		if !apierrors.IsNotFound(err) {
			t.Fatalf("%s still caps each namespace at the whole division total: %v", namespace, err)
		}
	}
}

func TestAPerNamespaceQuotaFromBeforeCapsuleIsCleanedUp(t *testing.T) {
	stale := &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{Name: tenancy.ResourceQuotaName, Namespace: "payments-dev"},
		Spec: corev1.ResourceQuotaSpec{
			Hard: corev1.ResourceList{corev1.ResourceRequestsCPU: resource.MustParse("8")},
		},
	}

	reconciler, api := newCapsuleReconciler(t, true, newDivision(), stale)

	reconcile(t, t.Context(), reconciler, "payments")

	err := api.Get(t.Context(), types.NamespacedName{
		Name: tenancy.ResourceQuotaName, Namespace: "payments-dev",
	}, &corev1.ResourceQuota{})

	if !apierrors.IsNotFound(err) {
		t.Fatalf("the quota written before Capsule arrived was left in place: %v", err)
	}
}

func TestWithoutCapsuleNothingChanges(t *testing.T) {
	reconciler, api := newCapsuleReconciler(t, false, newDivision())

	reconcile(t, t.Context(), reconciler, "payments")

	quota := &corev1.ResourceQuota{}
	if err := api.Get(t.Context(), types.NamespacedName{
		Name: tenancy.ResourceQuotaName, Namespace: "payments-dev",
	}, quota); err != nil {
		t.Fatalf("the per-namespace quota is gone with no backend to replace it: %v", err)
	}

	stored := reloadDivision(t, api)
	if stored.Status.QuotaBackend != tenancy.QuotaBackendResourceQuota {
		t.Fatalf("backend: got %q", stored.Status.QuotaBackend)
	}

	if got := quotaCondition(t, stored).Reason; got != "PerNamespace" {
		t.Fatalf("reason: got %q, want the gap named", got)
	}
}

func TestTheDivisionSaysWhichBackendIsActuallyHoldingTheTotal(t *testing.T) {
	reconciler, api := newCapsuleReconciler(t, true, newDivision())

	reconcile(t, t.Context(), reconciler, "payments")

	condition := quotaCondition(t, reloadDivision(t, api))
	if condition.Reason != "AcrossNamespaces" {
		t.Fatalf("reason: got %q", condition.Reason)
	}
	if condition.Message == "" {
		t.Fatal("the condition claims a cross-namespace total without saying anything about it")
	}
}

func newCapsuleReconciler(
	t *testing.T,
	installed bool,
	objects ...client.Object,
) (*tenancy.Reconciler, client.Client) {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add client-go scheme: %v", err)
	}
	if err := governv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add govern scheme: %v", err)
	}
	scheme.AddKnownTypeWithName(globalQuotaGVK, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(
		globalQuotaGVK.GroupVersion().WithKind(globalQuotaGVK.Kind+"List"),
		&unstructured.UnstructuredList{},
	)

	mapper := meta.NewDefaultRESTMapper(nil)
	if installed {
		mapper.Add(globalQuotaGVK, meta.RESTScopeRoot)
	}

	api := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithRESTMapper(mapper).
		WithStatusSubresource(&governv1alpha1.Division{}, &corev1.ResourceQuota{}).
		Build()

	return &tenancy.Reconciler{Client: api, Scheme: scheme}, api
}

func reloadDivision(t *testing.T, api client.Client) *governv1alpha1.Division {
	t.Helper()

	stored := &governv1alpha1.Division{}
	if err := api.Get(t.Context(), types.NamespacedName{Name: "payments"}, stored); err != nil {
		t.Fatalf("reload: %v", err)
	}

	return stored
}

func quotaCondition(t *testing.T, division *governv1alpha1.Division) metav1.Condition {
	t.Helper()

	for _, condition := range division.Status.Conditions {
		if condition.Type == governv1alpha1.ConditionQuotaReady {
			return condition
		}
	}

	t.Fatalf("no quota condition: %+v", division.Status.Conditions)

	return metav1.Condition{}
}
