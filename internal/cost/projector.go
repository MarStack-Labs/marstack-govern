package cost

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	governv1alpha1 "github.com/marstack-labs/marstack-govern/api/v1alpha1"
)

type Projector struct {
	client.Client
	Store *Store
	Now   func() time.Time
}

func (p *Projector) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&governv1alpha1.PricingPolicy{}).
		Named("pricing-policy").
		Complete(p)
}

func (p *Projector) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	policy := &governv1alpha1.PricingPolicy{}
	if err := p.Get(ctx, req.NamespacedName, policy); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, p.Store.DeletePolicy(ctx, req.Name)
		}
		return ctrl.Result{}, err
	}

	parsed, err := PolicyFrom(policy)
	if err != nil {
		return ctrl.Result{}, p.reject(ctx, policy, err)
	}

	if err := p.Store.UpsertPolicy(ctx, parsed, policy.ResourceVersion); err != nil {
		return ctrl.Result{}, err
	}

	effective := parsed.EffectiveOn(p.now())

	status := policy.Status.DeepCopy()
	status.Effective = effective
	status.Revision = policy.Generation
	status.ObservedGeneration = policy.Generation

	reason, message := "Active", fmt.Sprintf("rates approved by %s apply today", parsed.ApprovedBy)
	state := metav1.ConditionTrue
	if !effective {
		state, reason = metav1.ConditionFalse, "OutsideWindow"
		message = fmt.Sprintf("these rates apply from %s", parsed.From.Format(time.DateOnly))
	}

	setCondition(&status.Conditions, governv1alpha1.ConditionEffective, state, reason, message)

	return ctrl.Result{RequeueAfter: time.Hour}, p.writeStatus(ctx, policy, status)
}

func (p *Projector) reject(ctx context.Context, policy *governv1alpha1.PricingPolicy, cause error) error {
	status := policy.Status.DeepCopy()
	status.Effective = false
	status.ObservedGeneration = policy.Generation

	setCondition(&status.Conditions, governv1alpha1.ConditionEffective, metav1.ConditionFalse,
		"Unreadable", cause.Error())

	return p.writeStatus(ctx, policy, status)
}

func (p *Projector) writeStatus(
	ctx context.Context,
	policy *governv1alpha1.PricingPolicy,
	status *governv1alpha1.PricingPolicyStatus,
) error {
	if policy.Status.Effective == status.Effective &&
		policy.Status.Revision == status.Revision &&
		policy.Status.ObservedGeneration == status.ObservedGeneration &&
		len(policy.Status.Conditions) == len(status.Conditions) {
		return nil
	}

	policy.Status = *status
	if err := p.Status().Update(ctx, policy); err != nil {
		return fmt.Errorf("update status of pricing policy %s: %w", policy.Name, err)
	}

	return nil
}

func (p *Projector) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}

	return time.Now()
}

func setCondition(conditions *[]metav1.Condition, conditionType string, state metav1.ConditionStatus, reason, message string) {
	condition := metav1.Condition{
		Type:               conditionType,
		Status:             state,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	}

	for i := range *conditions {
		if (*conditions)[i].Type != conditionType {
			continue
		}
		if (*conditions)[i].Status == state {
			condition.LastTransitionTime = (*conditions)[i].LastTransitionTime
		}
		(*conditions)[i] = condition

		return
	}

	*conditions = append(*conditions, condition)
}
