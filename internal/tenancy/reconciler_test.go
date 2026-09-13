package tenancy_test

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	governv1alpha1 "github.com/marstack-labs/marstack-govern/api/v1alpha1"
	"github.com/marstack-labs/marstack-govern/internal/kube"
	"github.com/marstack-labs/marstack-govern/internal/tenancy"
)

func TestReconcileProvisionsEveryEnvironment(t *testing.T) {
	division := newDivision()
	reconciler, c := newReconciler(t, division)
	ctx := t.Context()

	reconcile(t, ctx, reconciler, division.Name)

	for _, name := range []string{"payments-dev", "payments-prod"} {
		namespace := &corev1.Namespace{}
		if err := c.Get(ctx, types.NamespacedName{Name: name}, namespace); err != nil {
			t.Fatalf("namespace %s: %v", name, err)
		}

		if got := namespace.Labels[kube.LabelDivision]; got != "payments" {
			t.Errorf("%s division label: got %q", name, got)
		}
		if len(namespace.OwnerReferences) != 1 || namespace.OwnerReferences[0].Kind != "Division" {
			t.Errorf("%s owner references: got %v", name, namespace.OwnerReferences)
		}
	}

	updated := &governv1alpha1.Division{}
	if err := c.Get(ctx, types.NamespacedName{Name: division.Name}, updated); err != nil {
		t.Fatalf("get division: %v", err)
	}

	if updated.Status.Phase != governv1alpha1.DivisionActive {
		t.Errorf("phase: got %q, want Active", updated.Status.Phase)
	}
	if len(updated.Status.Namespaces) != 2 {
		t.Errorf("status namespaces: got %v", updated.Status.Namespaces)
	}
	if updated.Status.ObservedGeneration != division.Generation {
		t.Errorf("observed generation: got %d, want %d", updated.Status.ObservedGeneration, division.Generation)
	}
	if !conditionTrue(updated, governv1alpha1.ConditionNamespacesReady) {
		t.Error("NamespacesReady is not true")
	}
}

func TestReconcileDeniesTrafficByDefault(t *testing.T) {
	division := newDivision()
	reconciler, c := newReconciler(t, division)
	ctx := t.Context()

	reconcile(t, ctx, reconciler, division.Name)

	policy := &networkingv1.NetworkPolicy{}
	key := types.NamespacedName{Namespace: "payments-dev", Name: tenancy.DefaultDenyPolicyName}
	if err := c.Get(ctx, key, policy); err != nil {
		t.Fatalf("default deny policy: %v", err)
	}

	if len(policy.Spec.PodSelector.MatchLabels) != 0 {
		t.Errorf("the policy does not select every pod: %v", policy.Spec.PodSelector)
	}

	policyTypes := map[networkingv1.PolicyType]bool{}
	for _, policyType := range policy.Spec.PolicyTypes {
		policyTypes[policyType] = true
	}
	if !policyTypes[networkingv1.PolicyTypeIngress] || !policyTypes[networkingv1.PolicyTypeEgress] {
		t.Errorf("policy types: got %v", policy.Spec.PolicyTypes)
	}
	if len(policy.Spec.Ingress) != 0 || len(policy.Spec.Egress) != 0 {
		t.Error("the default policy allows traffic")
	}
}

func TestIsolationCanBeDeclinedExplicitly(t *testing.T) {
	division := newDivision()
	allow := false
	division.Spec.DefaultDeny = &allow

	reconciler, c := newReconciler(t, division)
	ctx := t.Context()

	reconcile(t, ctx, reconciler, division.Name)

	policy := &networkingv1.NetworkPolicy{}
	key := types.NamespacedName{Namespace: "payments-dev", Name: tenancy.DefaultDenyPolicyName}
	if err := c.Get(ctx, key, policy); !apierrors.IsNotFound(err) {
		t.Fatalf("expected no policy, got %v", err)
	}

	updated := &governv1alpha1.Division{}
	if err := c.Get(ctx, types.NamespacedName{Name: division.Name}, updated); err != nil {
		t.Fatalf("get division: %v", err)
	}
	if reason := conditionReason(updated, governv1alpha1.ConditionIsolationReady); reason != "Disabled" {
		t.Errorf("isolation reason: got %q, want Disabled", reason)
	}
}

func TestReconcileAppliesQuotaAndLimits(t *testing.T) {
	division := newDivision()
	reconciler, c := newReconciler(t, division)
	ctx := t.Context()

	reconcile(t, ctx, reconciler, division.Name)

	quota := &corev1.ResourceQuota{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: "payments-dev", Name: tenancy.ResourceQuotaName}, quota); err != nil {
		t.Fatalf("resource quota: %v", err)
	}

	cpu := quota.Spec.Hard[corev1.ResourceRequestsCPU]
	if cpu.String() != "8" {
		t.Errorf("cpu quota: got %s, want 8", cpu.String())
	}
	pods := quota.Spec.Hard[corev1.ResourcePods]
	if pods.String() != "50" {
		t.Errorf("pod quota: got %s, want 50", pods.String())
	}

	limits := &corev1.LimitRange{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: "payments-dev", Name: tenancy.LimitRangeName}, limits); err != nil {
		t.Fatalf("limit range: %v", err)
	}
	if len(limits.Spec.Limits) != 1 {
		t.Fatalf("limit range items: got %d", len(limits.Spec.Limits))
	}

	request := limits.Spec.Limits[0].DefaultRequest[corev1.ResourceCPU]
	if request.String() != "250m" {
		t.Errorf("default cpu request: got %s, want 250m", request.String())
	}
}

func TestReconcileBindsGroupClaimsToRoles(t *testing.T) {
	division := newDivision()
	reconciler, c := newReconciler(t, division)
	ctx := t.Context()

	reconcile(t, ctx, reconciler, division.Name)

	binding := &rbacv1.RoleBinding{}
	key := types.NamespacedName{Namespace: "payments-dev", Name: "govern-admin"}
	if err := c.Get(ctx, key, binding); err != nil {
		t.Fatalf("admin binding: %v", err)
	}

	if binding.RoleRef.Name != "admin" || binding.RoleRef.Kind != "ClusterRole" {
		t.Errorf("role ref: got %+v", binding.RoleRef)
	}
	if len(binding.Subjects) != 1 {
		t.Fatalf("subjects: got %v", binding.Subjects)
	}
	if binding.Subjects[0].Kind != rbacv1.GroupKind || binding.Subjects[0].Name != "payments-admins" {
		t.Errorf("subject: got %+v", binding.Subjects[0])
	}
}

func TestRevokedGrantsAreRemoved(t *testing.T) {
	division := newDivision()
	reconciler, c := newReconciler(t, division)
	ctx := t.Context()

	reconcile(t, ctx, reconciler, division.Name)

	current := &governv1alpha1.Division{}
	if err := c.Get(ctx, types.NamespacedName{Name: division.Name}, current); err != nil {
		t.Fatalf("get division: %v", err)
	}
	current.Spec.Access = []governv1alpha1.Grant{{Role: governv1alpha1.RoleViewer, Group: "payments-readers"}}
	if err := c.Update(ctx, current); err != nil {
		t.Fatalf("update division: %v", err)
	}

	reconcile(t, ctx, reconciler, division.Name)

	binding := &rbacv1.RoleBinding{}
	key := types.NamespacedName{Namespace: "payments-dev", Name: "govern-admin"}
	if err := c.Get(ctx, key, binding); !apierrors.IsNotFound(err) {
		t.Fatalf("the revoked admin binding survived: %v", err)
	}

	if err := c.Get(ctx, types.NamespacedName{Namespace: "payments-dev", Name: "govern-viewer"}, binding); err != nil {
		t.Fatalf("viewer binding: %v", err)
	}
}

func TestSuspendedDivisionsAreNotProvisioned(t *testing.T) {
	division := newDivision()
	division.Spec.Suspended = true

	reconciler, c := newReconciler(t, division)
	ctx := t.Context()

	reconcile(t, ctx, reconciler, division.Name)

	namespace := &corev1.Namespace{}
	if err := c.Get(ctx, types.NamespacedName{Name: "payments-dev"}, namespace); !apierrors.IsNotFound(err) {
		t.Fatalf("a suspended division provisioned a namespace: %v", err)
	}

	updated := &governv1alpha1.Division{}
	if err := c.Get(ctx, types.NamespacedName{Name: division.Name}, updated); err != nil {
		t.Fatalf("get division: %v", err)
	}
	if updated.Status.Phase != governv1alpha1.DivisionSuspended {
		t.Errorf("phase: got %q, want Suspended", updated.Status.Phase)
	}
}

func TestReconcileIsIdempotent(t *testing.T) {
	division := newDivision()
	reconciler, c := newReconciler(t, division)
	ctx := t.Context()

	reconcile(t, ctx, reconciler, division.Name)

	first := &governv1alpha1.Division{}
	if err := c.Get(ctx, types.NamespacedName{Name: division.Name}, first); err != nil {
		t.Fatalf("get division: %v", err)
	}

	reconcile(t, ctx, reconciler, division.Name)

	second := &governv1alpha1.Division{}
	if err := c.Get(ctx, types.NamespacedName{Name: division.Name}, second); err != nil {
		t.Fatalf("get division: %v", err)
	}

	if first.ResourceVersion != second.ResourceVersion {
		t.Errorf("a second reconcile rewrote the status: %s then %s", first.ResourceVersion, second.ResourceVersion)
	}
}

func reconcile(t *testing.T, ctx context.Context, reconciler *tenancy.Reconciler, name string) {
	t.Helper()

	if _, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: name}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
}

func newReconciler(t *testing.T, objects ...client.Object) (*tenancy.Reconciler, client.Client) {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add client-go scheme: %v", err)
	}
	if err := governv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add govern scheme: %v", err)
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&governv1alpha1.Division{}, &corev1.ResourceQuota{}).
		Build()

	return &tenancy.Reconciler{Client: c, Scheme: scheme}, c
}

func newDivision() *governv1alpha1.Division {
	return &governv1alpha1.Division{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "payments",
			UID:        "11111111-1111-1111-1111-111111111111",
			Generation: 1,
		},
		Spec: governv1alpha1.DivisionSpec{
			DisplayName:  "Payments",
			Environments: []string{"dev", "prod"},
			Quota: governv1alpha1.Quota{
				CPU:     resource.MustParse("8"),
				Memory:  resource.MustParse("16Gi"),
				Storage: resource.MustParse("100Gi"),
				Pods:    50,
			},
			Limits: governv1alpha1.Limits{
				DefaultRequestCPU:    resource.MustParse("250m"),
				DefaultRequestMemory: resource.MustParse("512Mi"),
				MaxCPUPerPod:         resource.MustParse("2"),
				MaxMemoryPerPod:      resource.MustParse("4Gi"),
			},
			Access: []governv1alpha1.Grant{
				{Role: governv1alpha1.RoleAdmin, Group: "payments-admins"},
				{Role: governv1alpha1.RoleViewer, Group: "payments-readers"},
			},
		},
	}
}

func conditionTrue(division *governv1alpha1.Division, conditionType string) bool {
	for _, condition := range division.Status.Conditions {
		if condition.Type == conditionType {
			return condition.Status == metav1.ConditionTrue
		}
	}

	return false
}

func conditionReason(division *governv1alpha1.Division, conditionType string) string {
	for _, condition := range division.Status.Conditions {
		if condition.Type == conditionType {
			return condition.Reason
		}
	}

	return ""
}
