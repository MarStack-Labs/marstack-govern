package tenancy

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	governv1alpha1 "github.com/marstack-labs/marstack-govern/api/v1alpha1"
	"github.com/marstack-labs/marstack-govern/internal/kube"
)

const (
	LabelEnvironment = "govern.marstack.io/environment"
	LabelManagedBy   = "app.kubernetes.io/managed-by"
	ManagedBy        = "margov"

	DefaultDenyPolicyName = "govern-default-deny"
	LimitRangeName        = "govern-defaults"
	ResourceQuotaName     = "govern-division-quota"

	QuotaBackendResourceQuota = "resourcequota"
)

var clusterRoleForRole = map[governv1alpha1.Role]string{
	governv1alpha1.RoleViewer:   "view",
	governv1alpha1.RoleOperator: "edit",
	governv1alpha1.RoleAdmin:    "admin",
}

type Reconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&governv1alpha1.Division{}).
		Owns(&corev1.Namespace{}).
		Complete(r)
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	division := &governv1alpha1.Division{}
	if err := r.Get(ctx, req.NamespacedName, division); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !division.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.markPhase(ctx, division, governv1alpha1.DivisionTerminating)
	}

	if division.Spec.Suspended {
		return ctrl.Result{}, r.markPhase(ctx, division, governv1alpha1.DivisionSuspended)
	}

	status := division.Status.DeepCopy()
	status.Namespaces = nil
	status.ObservedGeneration = division.Generation
	status.QuotaBackend = QuotaBackendResourceQuota

	namespacesReady := true
	isolationReady := true
	accessReady := true
	quotaReady := true

	for _, environment := range division.Spec.Environments {
		name := division.NamespaceFor(environment)

		if err := r.ensureNamespace(ctx, division, environment, name); err != nil {
			namespacesReady = false
			r.setCondition(status, governv1alpha1.ConditionNamespacesReady, metav1.ConditionFalse, "NamespaceFailed", err.Error())
			continue
		}
		status.Namespaces = append(status.Namespaces, name)

		if err := r.ensureLimitRange(ctx, division, name); err != nil {
			quotaReady = false
			r.setCondition(status, governv1alpha1.ConditionQuotaReady, metav1.ConditionFalse, "LimitRangeFailed", err.Error())
		}

		if err := r.ensureResourceQuota(ctx, division, name); err != nil {
			quotaReady = false
			r.setCondition(status, governv1alpha1.ConditionQuotaReady, metav1.ConditionFalse, "ResourceQuotaFailed", err.Error())
		}

		if division.IsolationEnabled() {
			if err := r.ensureDefaultDeny(ctx, name); err != nil {
				isolationReady = false
				r.setCondition(status, governv1alpha1.ConditionIsolationReady, metav1.ConditionFalse, "NetworkPolicyFailed", err.Error())
			}
		}

		if err := r.ensureRoleBindings(ctx, division, name); err != nil {
			accessReady = false
			r.setCondition(status, governv1alpha1.ConditionAccessReady, metav1.ConditionFalse, "RoleBindingFailed", err.Error())
		}
	}

	if namespacesReady {
		r.setCondition(status, governv1alpha1.ConditionNamespacesReady, metav1.ConditionTrue, "Provisioned",
			fmt.Sprintf("%d namespaces reconciled", len(status.Namespaces)))
	}

	if isolationReady {
		reason, message := "DefaultDeny", "every namespace denies ingress and egress until a peering is approved"
		if !division.IsolationEnabled() {
			reason, message = "Disabled", "the division opted out of default-deny isolation"
		}
		r.setCondition(status, governv1alpha1.ConditionIsolationReady, metav1.ConditionTrue, reason, message)
	}

	if accessReady {
		r.setCondition(status, governv1alpha1.ConditionAccessReady, metav1.ConditionTrue, "Bound",
			fmt.Sprintf("%d group claims bound to namespace roles", len(division.Spec.Access)))
	}

	if quotaReady {
		r.setCondition(status, governv1alpha1.ConditionQuotaReady, metav1.ConditionTrue, "PerNamespace",
			"the quota is enforced in each namespace; a cross-namespace total needs a tenant quota backend")
	}

	status.Phase = governv1alpha1.DivisionPending
	if namespacesReady && isolationReady && accessReady && quotaReady {
		status.Phase = governv1alpha1.DivisionActive
	}

	return ctrl.Result{}, r.writeStatus(ctx, division, status)
}

func (r *Reconciler) ensureNamespace(ctx context.Context, division *governv1alpha1.Division, environment, name string) error {
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, namespace, func() error {
		if namespace.Labels == nil {
			namespace.Labels = map[string]string{}
		}
		namespace.Labels[kube.LabelDivision] = division.Name
		namespace.Labels[LabelEnvironment] = environment
		namespace.Labels[LabelManagedBy] = ManagedBy

		return controllerutil.SetControllerReference(division, namespace, r.Scheme)
	})
	if err != nil {
		return fmt.Errorf("namespace %s: %w", name, err)
	}

	return nil
}

func (r *Reconciler) ensureLimitRange(ctx context.Context, division *governv1alpha1.Division, namespace string) error {
	limits := division.Spec.Limits

	defaultRequest := corev1.ResourceList{}
	addQuantity(defaultRequest, corev1.ResourceCPU, limits.DefaultRequestCPU)
	addQuantity(defaultRequest, corev1.ResourceMemory, limits.DefaultRequestMemory)

	maximum := corev1.ResourceList{}
	addQuantity(maximum, corev1.ResourceCPU, limits.MaxCPUPerPod)
	addQuantity(maximum, corev1.ResourceMemory, limits.MaxMemoryPerPod)

	if len(defaultRequest) == 0 && len(maximum) == 0 {
		return r.deleteIfPresent(ctx, &corev1.LimitRange{
			ObjectMeta: metav1.ObjectMeta{Name: LimitRangeName, Namespace: namespace},
		})
	}

	limitRange := &corev1.LimitRange{
		ObjectMeta: metav1.ObjectMeta{Name: LimitRangeName, Namespace: namespace},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, limitRange, func() error {
		item := corev1.LimitRangeItem{Type: corev1.LimitTypeContainer}
		if len(defaultRequest) > 0 {
			item.DefaultRequest = defaultRequest
		}
		if len(maximum) > 0 {
			item.Max = maximum
		}

		limitRange.Spec.Limits = []corev1.LimitRangeItem{item}
		markManaged(&limitRange.ObjectMeta, division)

		return nil
	})
	if err != nil {
		return fmt.Errorf("limit range in %s: %w", namespace, err)
	}

	return nil
}

func (r *Reconciler) ensureResourceQuota(ctx context.Context, division *governv1alpha1.Division, namespace string) error {
	hard := corev1.ResourceList{
		corev1.ResourceRequestsCPU:    division.Spec.Quota.CPU,
		corev1.ResourceRequestsMemory: division.Spec.Quota.Memory,
	}

	if !division.Spec.Quota.Storage.IsZero() {
		hard[corev1.ResourceRequestsStorage] = division.Spec.Quota.Storage
	}
	if division.Spec.Quota.Pods > 0 {
		hard[corev1.ResourcePods] = *resource.NewQuantity(int64(division.Spec.Quota.Pods), resource.DecimalSI)
	}

	quota := &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{Name: ResourceQuotaName, Namespace: namespace},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, quota, func() error {
		quota.Spec.Hard = hard
		markManaged(&quota.ObjectMeta, division)

		return nil
	})
	if err != nil {
		return fmt.Errorf("resource quota in %s: %w", namespace, err)
	}

	return nil
}

func (r *Reconciler) ensureDefaultDeny(ctx context.Context, namespace string) error {
	policy := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: DefaultDenyPolicyName, Namespace: namespace},
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
		policy.Labels[LabelManagedBy] = ManagedBy

		return nil
	})
	if err != nil {
		return fmt.Errorf("default deny policy in %s: %w", namespace, err)
	}

	return nil
}

func (r *Reconciler) ensureRoleBindings(ctx context.Context, division *governv1alpha1.Division, namespace string) error {
	wanted := map[string]bool{}

	for _, grant := range division.Spec.Access {
		clusterRole, mapped := clusterRoleForRole[grant.Role]
		if !mapped {
			continue
		}

		name := roleBindingName(grant.Role)
		wanted[name] = true

		binding := &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		}

		_, err := controllerutil.CreateOrUpdate(ctx, r.Client, binding, func() error {
			binding.RoleRef = rbacv1.RoleRef{
				APIGroup: rbacv1.GroupName,
				Kind:     "ClusterRole",
				Name:     clusterRole,
			}
			binding.Subjects = appendSubject(binding.Subjects, grant.Group)
			markManaged(&binding.ObjectMeta, division)

			return nil
		})
		if err != nil {
			return fmt.Errorf("role binding %s in %s: %w", name, namespace, err)
		}
	}

	return r.pruneRoleBindings(ctx, division, namespace, wanted)
}

func (r *Reconciler) pruneRoleBindings(ctx context.Context, division *governv1alpha1.Division, namespace string, wanted map[string]bool) error {
	bindings := &rbacv1.RoleBindingList{}
	if err := r.List(ctx, bindings,
		client.InNamespace(namespace),
		client.MatchingLabels{LabelManagedBy: ManagedBy, kube.LabelDivision: division.Name},
	); err != nil {
		return fmt.Errorf("list role bindings in %s: %w", namespace, err)
	}

	for i := range bindings.Items {
		binding := &bindings.Items[i]
		if wanted[binding.Name] {
			continue
		}
		if err := r.Delete(ctx, binding); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("revoke role binding %s in %s: %w", binding.Name, namespace, err)
		}
	}

	return nil
}

func (r *Reconciler) deleteIfPresent(ctx context.Context, object client.Object) error {
	if err := r.Delete(ctx, object); err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("delete %s: %w", object.GetName(), err)
	}

	return nil
}

func (r *Reconciler) markPhase(ctx context.Context, division *governv1alpha1.Division, phase governv1alpha1.DivisionPhase) error {
	status := division.Status.DeepCopy()
	status.Phase = phase
	status.ObservedGeneration = division.Generation

	return r.writeStatus(ctx, division, status)
}

func (r *Reconciler) writeStatus(ctx context.Context, division *governv1alpha1.Division, status *governv1alpha1.DivisionStatus) error {
	if equalStatus(&division.Status, status) {
		return nil
	}

	division.Status = *status
	if err := r.Status().Update(ctx, division); err != nil {
		return fmt.Errorf("update status of division %s: %w", division.Name, err)
	}

	return nil
}

func (r *Reconciler) setCondition(status *governv1alpha1.DivisionStatus, conditionType string, state metav1.ConditionStatus, reason, message string) {
	for i := range status.Conditions {
		if status.Conditions[i].Type != conditionType {
			continue
		}
		if status.Conditions[i].Status == state && status.Conditions[i].Reason == reason && status.Conditions[i].Message == message {
			return
		}
	}

	condition := metav1.Condition{
		Type:               conditionType,
		Status:             state,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	}

	for i := range status.Conditions {
		if status.Conditions[i].Type == conditionType {
			if status.Conditions[i].Status == state {
				condition.LastTransitionTime = status.Conditions[i].LastTransitionTime
			}
			status.Conditions[i] = condition
			return
		}
	}

	status.Conditions = append(status.Conditions, condition)
}

func appendSubject(subjects []rbacv1.Subject, group string) []rbacv1.Subject {
	wanted := rbacv1.Subject{
		APIGroup: rbacv1.GroupName,
		Kind:     rbacv1.GroupKind,
		Name:     group,
	}

	for _, subject := range subjects {
		if subject == wanted {
			return subjects
		}
	}

	return append(subjects, wanted)
}

func markManaged(meta *metav1.ObjectMeta, division *governv1alpha1.Division) {
	if meta.Labels == nil {
		meta.Labels = map[string]string{}
	}
	meta.Labels[LabelManagedBy] = ManagedBy
	meta.Labels[kube.LabelDivision] = division.Name
}

func addQuantity(list corev1.ResourceList, name corev1.ResourceName, quantity resource.Quantity) {
	if quantity.IsZero() {
		return
	}
	list[name] = quantity
}

func roleBindingName(role governv1alpha1.Role) string {
	return "govern-" + string(role)
}

func equalStatus(current, next *governv1alpha1.DivisionStatus) bool {
	if current.Phase != next.Phase ||
		current.ObservedGeneration != next.ObservedGeneration ||
		current.QuotaBackend != next.QuotaBackend ||
		len(current.Namespaces) != len(next.Namespaces) ||
		len(current.Conditions) != len(next.Conditions) {
		return false
	}

	for i := range current.Namespaces {
		if current.Namespaces[i] != next.Namespaces[i] {
			return false
		}
	}

	for i := range current.Conditions {
		if current.Conditions[i].Type != next.Conditions[i].Type ||
			current.Conditions[i].Status != next.Conditions[i].Status ||
			current.Conditions[i].Reason != next.Conditions[i].Reason ||
			current.Conditions[i].Message != next.Conditions[i].Message {
			return false
		}
	}

	return true
}
