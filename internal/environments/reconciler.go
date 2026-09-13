package environments

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	governv1alpha1 "github.com/marstack-labs/marstack-govern/api/v1alpha1"
	"github.com/marstack-labs/marstack-govern/internal/kube"
	"github.com/marstack-labs/marstack-govern/internal/tenancy"
)

const (
	LabelChange   = "govern.marstack.io/change"
	LabelPreview  = "govern.marstack.io/preview"
	MaxSweepDelay = 10 * time.Minute
)

type Reconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Now    func() time.Time
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&governv1alpha1.EphemeralEnvironment{}).
		Owns(&corev1.Namespace{}).
		Named("ephemeral-environment").
		Complete(r)
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	environment := &governv1alpha1.EphemeralEnvironment{}
	if err := r.Get(ctx, req.NamespacedName, environment); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !environment.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	now := r.now()
	status := environment.Status.DeepCopy()
	status.ObservedGeneration = environment.Generation
	status.Namespace = NamespaceFor(environment)

	lease, err := LeaseOf(environment)
	if err != nil {
		status.Phase = governv1alpha1.EnvironmentPending
		setCondition(&status.Conditions, governv1alpha1.ConditionLeaseValid, metav1.ConditionFalse,
			"Unreadable", err.Error())

		return ctrl.Result{}, r.writeStatus(ctx, environment, status)
	}

	if status.Phase == governv1alpha1.EnvironmentReclaiming ||
		status.Phase == governv1alpha1.EnvironmentOrphaned {
		return r.reclaim(ctx, environment, status, lease)
	}

	if status.LeaseStartedAt == nil {
		status.LeaseStartedAt = stamp(lease.StartedAt)
	}
	status.ExpiresAt = stamp(lease.ExpiresAt)
	status.GrantedTTL = FormatTTL(lease.Granted)

	if lease.Clamped {
		setCondition(&status.Conditions, governv1alpha1.ConditionLeaseValid, metav1.ConditionTrue, "Clamped",
			fmt.Sprintf("the lease was shortened to the %s ceiling", MaxTTL))
	} else {
		setCondition(&status.Conditions, governv1alpha1.ConditionLeaseValid, metav1.ConditionTrue, "Granted",
			fmt.Sprintf("the lease runs for %s and ends at %s", lease.Granted, lease.ExpiresAt.UTC().Format(time.RFC3339)))
	}

	if lease.Expired(now) {
		status.Phase = governv1alpha1.EnvironmentReclaiming

		return r.reclaim(ctx, environment, status, lease)
	}

	division := &governv1alpha1.Division{}
	if err := r.Get(ctx, types.NamespacedName{Name: environment.Spec.Division}, division); err != nil {
		if apierrors.IsNotFound(err) {
			status.Phase = governv1alpha1.EnvironmentPending
			setCondition(&status.Conditions, governv1alpha1.ConditionNamespaceReady, metav1.ConditionFalse,
				"DivisionMissing", fmt.Sprintf("division %s does not exist", environment.Spec.Division))

			return ctrl.Result{}, r.writeStatus(ctx, environment, status)
		}

		return ctrl.Result{}, err
	}

	if err := r.ensureNamespace(ctx, environment, division, status.Namespace); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.ensureQuota(ctx, environment, status.Namespace); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.ensureDefaultDeny(ctx, division, status.Namespace); err != nil {
		return ctrl.Result{}, err
	}

	setCondition(&status.Conditions, governv1alpha1.ConditionNamespaceReady, metav1.ConditionTrue, "Provisioned",
		fmt.Sprintf("namespace %s previews %s!%d", status.Namespace,
			environment.Spec.Change.Repository, environment.Spec.Change.Number))
	setCondition(&status.Conditions, governv1alpha1.ConditionWithinQuota, metav1.ConditionTrue, "Bounded",
		"the preview carries its own quota, so it cannot grow into the division's headroom")

	status.Phase = governv1alpha1.EnvironmentReady

	if err := r.writeStatus(ctx, environment, status); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: sweepDelay(lease.Remaining(now))}, nil
}

func (r *Reconciler) reclaim(
	ctx context.Context,
	environment *governv1alpha1.EphemeralEnvironment,
	status *governv1alpha1.EphemeralEnvironmentStatus,
	lease Lease,
) (ctrl.Result, error) {
	namespace := &corev1.Namespace{}
	err := r.Get(ctx, types.NamespacedName{Name: status.Namespace}, namespace)

	switch {
	case apierrors.IsNotFound(err):
		status.Phase = governv1alpha1.EnvironmentExpired
		if status.ReclaimedAt == nil {
			status.ReclaimedAt = stamp(r.now())
		}
		setCondition(&status.Conditions, governv1alpha1.ConditionNamespaceReady, metav1.ConditionFalse,
			"Reclaimed", fmt.Sprintf("the lease ended at %s and the namespace is gone",
				lease.ExpiresAt.UTC().Format(time.RFC3339)))

		return ctrl.Result{}, r.writeStatus(ctx, environment, status)

	case err != nil:
		return ctrl.Result{}, err
	}

	if !ownedByUs(namespace, environment) {
		status.Phase = governv1alpha1.EnvironmentOrphaned
		setCondition(&status.Conditions, governv1alpha1.ConditionNamespaceReady, metav1.ConditionFalse,
			"NotOurs", fmt.Sprintf("namespace %s exists but this environment does not own it, so it was left alone",
				status.Namespace))

		return ctrl.Result{}, r.writeStatus(ctx, environment, status)
	}

	if namespace.DeletionTimestamp.IsZero() {
		if err := r.Delete(ctx, namespace); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("reclaim namespace %s: %w", status.Namespace, err)
		}
	}

	status.Phase = governv1alpha1.EnvironmentReclaiming
	setCondition(&status.Conditions, governv1alpha1.ConditionNamespaceReady, metav1.ConditionFalse,
		"Reclaiming", fmt.Sprintf("the lease ended at %s and namespace %s is being removed",
			lease.ExpiresAt.UTC().Format(time.RFC3339), status.Namespace))

	if err := r.writeStatus(ctx, environment, status); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

func (r *Reconciler) ensureNamespace(
	ctx context.Context,
	environment *governv1alpha1.EphemeralEnvironment,
	division *governv1alpha1.Division,
	name string,
) error {
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, namespace, func() error {
		if namespace.Labels == nil {
			namespace.Labels = map[string]string{}
		}
		namespace.Labels[kube.LabelDivision] = division.Name
		namespace.Labels[tenancy.LabelEnvironment] = "preview"
		namespace.Labels[tenancy.LabelManagedBy] = tenancy.ManagedBy
		namespace.Labels[LabelPreview] = "true"
		namespace.Labels[LabelChange] = fmt.Sprintf("%d", environment.Spec.Change.Number)

		return controllerutil.SetControllerReference(environment, namespace, r.Scheme)
	})
	if err != nil {
		return fmt.Errorf("namespace %s: %w", name, err)
	}

	return nil
}

func (r *Reconciler) ensureQuota(
	ctx context.Context,
	environment *governv1alpha1.EphemeralEnvironment,
	namespace string,
) error {
	hard := corev1.ResourceList{
		corev1.ResourceRequestsCPU:    environment.Spec.Quota.CPU,
		corev1.ResourceRequestsMemory: environment.Spec.Quota.Memory,
	}

	if !environment.Spec.Quota.Storage.IsZero() {
		hard[corev1.ResourceRequestsStorage] = environment.Spec.Quota.Storage
	}
	if environment.Spec.Quota.Pods > 0 {
		hard[corev1.ResourcePods] = *resource.NewQuantity(int64(environment.Spec.Quota.Pods), resource.DecimalSI)
	}

	quota := &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{Name: tenancy.ResourceQuotaName, Namespace: namespace},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, quota, func() error {
		quota.Spec.Hard = hard
		if quota.Labels == nil {
			quota.Labels = map[string]string{}
		}
		quota.Labels[tenancy.LabelManagedBy] = tenancy.ManagedBy
		quota.Labels[kube.LabelDivision] = environment.Spec.Division

		return nil
	})
	if err != nil {
		return fmt.Errorf("quota in %s: %w", namespace, err)
	}

	return nil
}

func (r *Reconciler) ensureDefaultDeny(
	ctx context.Context,
	division *governv1alpha1.Division,
	namespace string,
) error {
	if !division.IsolationEnabled() {
		return nil
	}

	policy := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: tenancy.DefaultDenyPolicyName, Namespace: namespace},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, policy, func() error {
		policy.Spec = networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []networkingv1.PolicyType{
				networkingv1.PolicyTypeIngress,
				networkingv1.PolicyTypeEgress,
			},
		}
		if policy.Labels == nil {
			policy.Labels = map[string]string{}
		}
		policy.Labels[tenancy.LabelManagedBy] = tenancy.ManagedBy

		return nil
	})
	if err != nil {
		return fmt.Errorf("default deny in %s: %w", namespace, err)
	}

	return nil
}

func (r *Reconciler) writeStatus(
	ctx context.Context,
	environment *governv1alpha1.EphemeralEnvironment,
	status *governv1alpha1.EphemeralEnvironmentStatus,
) error {
	environment.Status = *status

	return r.Status().Update(ctx, environment)
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}

	return time.Now()
}

func NamespaceFor(environment *governv1alpha1.EphemeralEnvironment) string {
	return fmt.Sprintf("%s-preview-%d", environment.Spec.Division, environment.Spec.Change.Number)
}

func ownedByUs(namespace *corev1.Namespace, environment *governv1alpha1.EphemeralEnvironment) bool {
	for _, owner := range namespace.OwnerReferences {
		if owner.UID == environment.UID && owner.Kind == "EphemeralEnvironment" {
			return true
		}
	}

	return false
}

func sweepDelay(remaining time.Duration) time.Duration {
	if remaining <= 0 {
		return time.Second
	}
	if remaining > MaxSweepDelay {
		return MaxSweepDelay
	}

	return remaining
}

func setCondition(conditions *[]metav1.Condition, conditionType string, state metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(conditions, metav1.Condition{
		Type:    conditionType,
		Status:  state,
		Reason:  reason,
		Message: message,
	})
}
