package environments_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	governv1alpha1 "github.com/marstack-labs/marstack-govern/api/v1alpha1"
	"github.com/marstack-labs/marstack-govern/internal/environments"
	"github.com/marstack-labs/marstack-govern/internal/tenancy"
)

var created = time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)

func TestAPreviewGetsItsOwnNamespaceQuotaAndDefaultDeny(t *testing.T) {
	environment := preview("48h")
	reconciler, api := newReconciler(t, created.Add(time.Hour), division(), environment)

	reconcile(t, reconciler, environment)

	stored := reload(t, api, environment)
	if stored.Status.Phase != governv1alpha1.EnvironmentReady {
		t.Fatalf("phase: got %q", stored.Status.Phase)
	}
	if stored.Status.Namespace != "payments-preview-412" {
		t.Fatalf("namespace: got %q", stored.Status.Namespace)
	}

	namespace := &corev1.Namespace{}
	if err := api.Get(t.Context(), types.NamespacedName{Name: stored.Status.Namespace}, namespace); err != nil {
		t.Fatalf("namespace not created: %v", err)
	}
	if namespace.Labels[environments.LabelChange] != "412" {
		t.Errorf("change label: got %q", namespace.Labels[environments.LabelChange])
	}

	quota := &corev1.ResourceQuota{}
	if err := api.Get(t.Context(), types.NamespacedName{
		Name: tenancy.ResourceQuotaName, Namespace: stored.Status.Namespace,
	}, quota); err != nil {
		t.Fatalf("quota not created: %v", err)
	}
	if got := quota.Spec.Hard[corev1.ResourceRequestsCPU]; got.String() != "2" {
		t.Errorf("quota cpu: got %q", got.String())
	}

	policy := &networkingv1.NetworkPolicy{}
	if err := api.Get(t.Context(), types.NamespacedName{
		Name: tenancy.DefaultDenyPolicyName, Namespace: stored.Status.Namespace,
	}, policy); err != nil {
		t.Fatalf("a preview namespace was left without default deny: %v", err)
	}
}

func TestTheLeaseRunsFromCreationNotFromThisReconcile(t *testing.T) {
	environment := preview("48h")
	reconciler, api := newReconciler(t, created.Add(30*time.Hour), division(), environment)

	reconcile(t, reconciler, environment)

	first := reload(t, api, environment)
	if first.Status.ExpiresAt == nil {
		t.Fatal("no expiry was recorded")
	}
	want := created.Add(48 * time.Hour)
	if !first.Status.ExpiresAt.Time.Equal(want) {
		t.Fatalf("expiry: got %s, want %s", first.Status.ExpiresAt.Time, want)
	}

	reconciler.Now = func() time.Time { return created.Add(40 * time.Hour) }
	reconcile(t, reconciler, first)

	second := reload(t, api, environment)
	if !second.Status.ExpiresAt.Time.Equal(want) {
		t.Fatalf("a second reconcile moved the expiry to %s; a lease must not renew itself",
			second.Status.ExpiresAt.Time)
	}
}

func TestAnExpiredLeaseTakesTheNamespaceBack(t *testing.T) {
	environment := preview("48h")
	reconciler, api := newReconciler(t, created.Add(time.Hour), division(), environment)

	reconcile(t, reconciler, environment)

	reconciler.Now = func() time.Time { return created.Add(49 * time.Hour) }
	reconcile(t, reconciler, reload(t, api, environment))

	stored := reload(t, api, environment)
	if stored.Status.Phase != governv1alpha1.EnvironmentReclaiming {
		t.Fatalf("phase: got %q, want the namespace to be on its way out", stored.Status.Phase)
	}

	namespace := &corev1.Namespace{}
	err := api.Get(t.Context(), types.NamespacedName{Name: stored.Status.Namespace}, namespace)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("the namespace outlived its lease: %v", err)
	}
}

func TestAReclaimedEnvironmentIsNotRebuilt(t *testing.T) {
	environment := preview("48h")
	reconciler, api := newReconciler(t, created.Add(time.Hour), division(), environment)

	reconcile(t, reconciler, environment)

	reconciler.Now = func() time.Time { return created.Add(49 * time.Hour) }
	reconcile(t, reconciler, reload(t, api, environment))
	reconcile(t, reconciler, reload(t, api, environment))

	stored := reload(t, api, environment)
	if stored.Status.Phase != governv1alpha1.EnvironmentExpired {
		t.Fatalf("phase: got %q, want it to settle as expired", stored.Status.Phase)
	}
	if stored.Status.ReclaimedAt == nil {
		t.Fatal("no reclaim time was recorded")
	}

	namespace := &corev1.Namespace{}
	if err := api.Get(t.Context(), types.NamespacedName{Name: stored.Status.Namespace}, namespace); !apierrors.IsNotFound(err) {
		t.Fatalf("the namespace came back after being reclaimed: %v", err)
	}
}

func TestANamespaceWeDoNotOwnIsLeftAlone(t *testing.T) {
	environment := preview("48h")
	squatter := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "payments-preview-412"}}
	reconciler, api := newReconciler(t, created.Add(49*time.Hour), division(), environment, squatter)

	reconcile(t, reconciler, environment)

	stored := reload(t, api, environment)
	if stored.Status.Phase != governv1alpha1.EnvironmentOrphaned {
		t.Fatalf("phase: got %q, want the controller to refuse to delete it", stored.Status.Phase)
	}

	namespace := &corev1.Namespace{}
	if err := api.Get(t.Context(), types.NamespacedName{Name: "payments-preview-412"}, namespace); err != nil {
		t.Fatalf("a namespace the platform did not create was deleted: %v", err)
	}
}

func TestARenewalExtendsTheLeaseAndIsAttributed(t *testing.T) {
	environment := preview("48h")
	environment.Spec.Renewals = []governv1alpha1.Renewal{{
		Extend:    "24h",
		Reason:    "the review is still open and the reviewer is on leave",
		GrantedBy: "lead@marstack.test",
		GrantedAt: metav1.NewTime(created.Add(47 * time.Hour)),
	}}

	reconciler, api := newReconciler(t, created.Add(50*time.Hour), division(), environment)

	reconcile(t, reconciler, environment)

	stored := reload(t, api, environment)
	if stored.Status.Phase != governv1alpha1.EnvironmentReady {
		t.Fatalf("phase: got %q, want the renewal to keep it alive", stored.Status.Phase)
	}
	want := created.Add(72 * time.Hour)
	if !stored.Status.ExpiresAt.Time.Equal(want) {
		t.Fatalf("expiry: got %s, want %s", stored.Status.ExpiresAt.Time, want)
	}
}

func TestNoLeaseOutlivesTheCeiling(t *testing.T) {
	environment := preview("240h")
	reconciler, api := newReconciler(t, created.Add(time.Hour), division(), environment)

	reconcile(t, reconciler, environment)

	stored := reload(t, api, environment)
	want := created.Add(environments.MaxTTL)
	if !stored.Status.ExpiresAt.Time.Equal(want) {
		t.Fatalf("expiry: got %s, want it clamped to %s", stored.Status.ExpiresAt.Time, want)
	}
	if condition(stored, governv1alpha1.ConditionLeaseValid).Reason != "Clamped" {
		t.Errorf("the clamp was applied without saying so: %+v", stored.Status.Conditions)
	}
}

func TestAnUnreadableTTLStopsEverything(t *testing.T) {
	environment := preview("soon")
	reconciler, api := newReconciler(t, created.Add(time.Hour), division(), environment)

	reconcile(t, reconciler, environment)

	stored := reload(t, api, environment)
	if stored.Status.Phase != governv1alpha1.EnvironmentPending {
		t.Fatalf("phase: got %q", stored.Status.Phase)
	}

	namespace := &corev1.Namespace{}
	if err := api.Get(t.Context(), types.NamespacedName{Name: "payments-preview-412"}, namespace); !apierrors.IsNotFound(err) {
		t.Fatalf("a namespace was created for a lease nobody can read: %v", err)
	}

	lease := condition(stored, governv1alpha1.ConditionLeaseValid)
	if lease.Status != metav1.ConditionFalse || lease.Reason != "Unreadable" {
		t.Fatalf("condition: %+v", lease)
	}
}

func TestAMissingDivisionIsNamedNotGuessed(t *testing.T) {
	environment := preview("48h")
	reconciler, api := newReconciler(t, created.Add(time.Hour), environment)

	reconcile(t, reconciler, environment)

	stored := reload(t, api, environment)
	if stored.Status.Phase != governv1alpha1.EnvironmentPending {
		t.Fatalf("phase: got %q", stored.Status.Phase)
	}
	if got := condition(stored, governv1alpha1.ConditionNamespaceReady).Reason; got != "DivisionMissing" {
		t.Fatalf("reason: got %q", got)
	}
}

func newReconciler(
	t *testing.T,
	now time.Time,
	objects ...client.Object,
) (*environments.Reconciler, client.Client) {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("client-go scheme: %v", err)
	}
	if err := governv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("govern scheme: %v", err)
	}

	uid := 0
	api := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&governv1alpha1.EphemeralEnvironment{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, c client.WithWatch, object client.Object, opts ...client.CreateOption) error {
				if object.GetUID() == "" {
					uid++
					object.SetUID(types.UID(fmt.Sprintf("uid-%d", uid)))
				}

				return c.Create(ctx, object, opts...)
			},
		}).
		Build()

	return &environments.Reconciler{
		Client: api,
		Scheme: scheme,
		Now:    func() time.Time { return now },
	}, api
}

func reconcile(t *testing.T, reconciler *environments.Reconciler, environment *governv1alpha1.EphemeralEnvironment) {
	t.Helper()

	if _, err := reconciler.Reconcile(t.Context(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: environment.Name},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
}

func reload(
	t *testing.T,
	api client.Client,
	environment *governv1alpha1.EphemeralEnvironment,
) *governv1alpha1.EphemeralEnvironment {
	t.Helper()

	stored := &governv1alpha1.EphemeralEnvironment{}
	if err := api.Get(t.Context(), types.NamespacedName{Name: environment.Name}, stored); err != nil {
		t.Fatalf("reload: %v", err)
	}

	return stored
}

func preview(ttl string) *governv1alpha1.EphemeralEnvironment {
	return &governv1alpha1.EphemeralEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "payments-412",
			UID:               types.UID("env-412"),
			CreationTimestamp: metav1.NewTime(created),
		},
		Spec: governv1alpha1.EphemeralEnvironmentSpec{
			Division: "payments",
			Change: governv1alpha1.ChangeRef{
				Repository: "payments/ledger",
				Number:     412,
				Branch:     "feat/settlement-window",
			},
			Quota: governv1alpha1.Quota{
				CPU:    resource.MustParse("2"),
				Memory: resource.MustParse("4Gi"),
				Pods:   10,
			},
			TTL:         ttl,
			RequestedBy: "dev@marstack.test",
		},
	}
}

func division() *governv1alpha1.Division {
	return &governv1alpha1.Division{
		ObjectMeta: metav1.ObjectMeta{Name: "payments", UID: types.UID("div-payments")},
		Spec: governv1alpha1.DivisionSpec{
			DisplayName:  "Payments",
			Environments: []string{"dev"},
			Quota: governv1alpha1.Quota{
				CPU:    resource.MustParse("40"),
				Memory: resource.MustParse("80Gi"),
			},
		},
	}
}

func condition(
	environment *governv1alpha1.EphemeralEnvironment,
	conditionType string,
) metav1.Condition {
	for _, found := range environment.Status.Conditions {
		if found.Type == conditionType {
			return found
		}
	}

	return metav1.Condition{}
}
