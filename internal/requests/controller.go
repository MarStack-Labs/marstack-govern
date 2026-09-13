package requests

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	governv1alpha1 "github.com/marstack-labs/marstack-govern/api/v1alpha1"
	"github.com/marstack-labs/marstack-govern/internal/metrics"
)

type RequestReconciler struct {
	client.Client
	Recommender *Recommender
	Preflight   *Preflight
}

func (r *RequestReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&governv1alpha1.QuotaRequest{}).
		Named("quota-request").
		Complete(r)
}

func (r *RequestReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	request := &governv1alpha1.QuotaRequest{}
	if err := r.Get(ctx, req.NamespacedName, request); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !request.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	status := request.Status.DeepCopy()
	status.ObservedGeneration = request.Generation

	if request.Spec.Withdrawn {
		status.Phase = governv1alpha1.RequestWithdrawn
		return ctrl.Result{}, r.writeStatus(ctx, request, status)
	}

	if !request.Open() {
		return ctrl.Result{}, nil
	}

	division := &governv1alpha1.Division{}
	if err := r.Get(ctx, types.NamespacedName{Name: request.Spec.Division}, division); err != nil {
		if apierrors.IsNotFound(err) {
			status.Phase = governv1alpha1.RequestPending
			setCondition(&status.Conditions, governv1alpha1.ConditionRecommended, metav1.ConditionFalse,
				"DivisionMissing", fmt.Sprintf("division %s does not exist", request.Spec.Division))
			return ctrl.Result{}, r.writeStatus(ctx, request, status)
		}
		return ctrl.Result{}, err
	}

	current := Compute{
		CPUMillicores: division.Spec.Quota.CPU.MilliValue(),
		MemoryBytes:   division.Spec.Quota.Memory.Value(),
		StorageBytes:  division.Spec.Quota.Storage.Value(),
		Pods:          division.Spec.Quota.Pods,
	}

	status.Recommendation = r.recommend(ctx, division, current, &status.Conditions)
	status.Preflight = r.preflight(ctx, request, division, &status.Conditions)
	status.Phase = governv1alpha1.RequestAwaitingDecision

	digest, err := EvidenceDigest(status.Recommendation, status.Preflight)
	if err != nil {
		return ctrl.Result{}, err
	}
	status.EvidenceDigest = digest

	return ctrl.Result{RequeueAfter: 10 * time.Minute}, r.writeStatus(ctx, request, status)
}

func (r *RequestReconciler) recommend(
	ctx context.Context,
	division *governv1alpha1.Division,
	current Compute,
	conditions *[]metav1.Condition,
) *governv1alpha1.Recommendation {
	if r.Recommender == nil || !r.Recommender.Available() {
		setCondition(conditions, governv1alpha1.ConditionRecommended, metav1.ConditionFalse,
			"NoMetrics", "no metrics source is configured, so the request carries no proposed number")
		return nil
	}

	recommendation, err := r.Recommender.Recommend(ctx, division.Status.Namespaces, current)
	if err != nil {
		reason := "MetricsUnavailable"
		if errors.Is(err, ErrNoUsage) {
			reason = "NoUsage"
		}
		if errors.Is(err, metrics.ErrUnavailable) {
			reason = "NoMetrics"
		}

		setCondition(conditions, governv1alpha1.ConditionRecommended, metav1.ConditionFalse, reason, err.Error())

		return nil
	}

	setCondition(conditions, governv1alpha1.ConditionRecommended, metav1.ConditionTrue, "Observed", recommendation.Basis)

	return toAPIRecommendation(recommendation, current)
}

func (r *RequestReconciler) preflight(
	ctx context.Context,
	request *governv1alpha1.QuotaRequest,
	division *governv1alpha1.Division,
	conditions *[]metav1.Condition,
) *governv1alpha1.PreflightResult {
	if r.Preflight == nil {
		setCondition(conditions, governv1alpha1.ConditionPreflight, metav1.ConditionFalse,
			"NotEvaluated", "no capacity source is configured")
		return nil
	}

	result, err := r.Preflight.Evaluate(ctx, division.Name, quotaOf(request.Spec.Target))
	if err != nil {
		setCondition(conditions, governv1alpha1.ConditionPreflight, metav1.ConditionFalse, "Failed", err.Error())
		return nil
	}

	reason, message := "Admitted", "the target fits the cluster"
	state := metav1.ConditionTrue
	if !result.Admitted {
		state = metav1.ConditionFalse
		reason, message = "Blocked", firstBlocking(result)
	}

	setCondition(conditions, governv1alpha1.ConditionPreflight, state, reason, message)

	return result
}

func (r *RequestReconciler) writeStatus(
	ctx context.Context,
	request *governv1alpha1.QuotaRequest,
	status *governv1alpha1.QuotaRequestStatus,
) error {
	if equalRequestStatus(&request.Status, status) {
		return nil
	}

	request.Status = *status
	if err := r.Status().Update(ctx, request); err != nil {
		return fmt.Errorf("update status of request %s/%s: %w", request.Namespace, request.Name, err)
	}

	return nil
}

type DecisionReconciler struct {
	client.Client
	Now func() time.Time
}

func (r *DecisionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&governv1alpha1.Decision{}).
		Named("decision").
		Complete(r)
}

func (r *DecisionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	decision := &governv1alpha1.Decision{}
	if err := r.Get(ctx, req.NamespacedName, decision); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	now := metav1.NewTime(r.now())

	request := &governv1alpha1.QuotaRequest{}
	key := types.NamespacedName{Namespace: decision.Spec.Request.Namespace, Name: decision.Spec.Request.Name}
	if err := r.Get(ctx, key, request); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	status := decision.Status.DeepCopy()
	status.ObservedGeneration = decision.Generation

	switch decision.Spec.Outcome {
	case governv1alpha1.OutcomeRejected:
		if err := r.setRequestPhase(ctx, request, governv1alpha1.RequestRejected, decision.Name); err != nil {
			return ctrl.Result{}, err
		}
		status.Message = "the request was rejected"

	case governv1alpha1.OutcomeChangesRequested:
		if err := r.setRequestPhase(ctx, request, governv1alpha1.RequestChangesRequested, decision.Name); err != nil {
			return ctrl.Result{}, err
		}
		status.Message = "the requester was asked for changes"

	case governv1alpha1.OutcomeApproved:
		if decision.Lapsed(now) {
			status.Expired = true
			status.Message = "the grant lapsed and is due for review"

			if err := r.setRequestPhase(ctx, request, governv1alpha1.RequestExpired, decision.Name); err != nil {
				return ctrl.Result{}, err
			}

			break
		}

		if !status.Applied {
			if err := r.applyQuota(ctx, request); err != nil {
				return ctrl.Result{}, err
			}

			status.Applied = true
			status.AppliedAt = &now
			status.Message = fmt.Sprintf("the quota of %s now matches the request", request.Spec.Division)
		}

		if err := r.setRequestPhase(ctx, request, governv1alpha1.RequestApplied, decision.Name); err != nil {
			return ctrl.Result{}, err
		}
	}

	if err := r.writeStatus(ctx, decision, status); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: r.nextCheck(decision, now)}, nil
}

func (r *DecisionReconciler) applyQuota(ctx context.Context, request *governv1alpha1.QuotaRequest) error {
	division := &governv1alpha1.Division{}
	if err := r.Get(ctx, types.NamespacedName{Name: request.Spec.Division}, division); err != nil {
		return fmt.Errorf("read division %s: %w", request.Spec.Division, err)
	}

	patched := division.DeepCopy()
	patched.Spec.Quota = request.Spec.Target

	if err := r.Patch(ctx, patched, client.MergeFrom(division)); err != nil {
		return fmt.Errorf("apply the approved quota to %s: %w", division.Name, err)
	}

	return nil
}

func (r *DecisionReconciler) setRequestPhase(
	ctx context.Context,
	request *governv1alpha1.QuotaRequest,
	phase governv1alpha1.RequestPhase,
	decisionName string,
) error {
	if request.Status.Phase == phase && request.Status.DecisionRef == decisionName {
		return nil
	}

	request.Status.Phase = phase
	request.Status.DecisionRef = decisionName
	setCondition(&request.Status.Conditions, governv1alpha1.ConditionDecided, metav1.ConditionTrue,
		string(phase), fmt.Sprintf("decision %s", decisionName))

	if err := r.Status().Update(ctx, request); err != nil {
		return fmt.Errorf("update status of request %s/%s: %w", request.Namespace, request.Name, err)
	}

	return nil
}

func (r *DecisionReconciler) writeStatus(
	ctx context.Context,
	decision *governv1alpha1.Decision,
	status *governv1alpha1.DecisionStatus,
) error {
	if decision.Status.Applied == status.Applied &&
		decision.Status.Expired == status.Expired &&
		decision.Status.Message == status.Message &&
		decision.Status.ObservedGeneration == status.ObservedGeneration {
		return nil
	}

	decision.Status = *status
	if err := r.Status().Update(ctx, decision); err != nil {
		return fmt.Errorf("update status of decision %s: %w", decision.Name, err)
	}

	return nil
}

func (r *DecisionReconciler) nextCheck(decision *governv1alpha1.Decision, now metav1.Time) time.Duration {
	if decision.Spec.Outcome != governv1alpha1.OutcomeApproved || decision.Spec.GrantedUntil == nil {
		return 0
	}

	remaining := decision.Spec.GrantedUntil.Sub(now.Time)
	if remaining <= 0 {
		return 0
	}
	if remaining > time.Hour {
		return time.Hour
	}

	return remaining
}

func (r *DecisionReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}

	return time.Now()
}

func EvidenceDigest(recommendation *governv1alpha1.Recommendation, preflight *governv1alpha1.PreflightResult) (string, error) {
	payload, err := json.Marshal(struct {
		Recommendation *governv1alpha1.Recommendation  `json:"recommendation"`
		Preflight      *governv1alpha1.PreflightResult `json:"preflight"`
	}{Recommendation: recommendation, Preflight: preflight})
	if err != nil {
		return "", fmt.Errorf("encode evidence: %w", err)
	}

	sum := sha256.Sum256(payload)

	return hex.EncodeToString(sum[:]), nil
}

func toAPIRecommendation(recommendation Recommendation, current Compute) *governv1alpha1.Recommendation {
	out := &governv1alpha1.Recommendation{
		Current:         quotaFromCompute(current),
		ObservedP95:     quotaFromCompute(recommendation.ObservedP95),
		ObservedP99:     quotaFromCompute(recommendation.ObservedP99),
		Proposed:        quotaFromCompute(recommendation.Proposed),
		Window:          recommendation.Window,
		Basis:           recommendation.Basis,
		HeadroomPercent: recommendation.HeadroomPercent,
	}

	if recommendation.ExhaustionAt != nil {
		at := metav1.NewTime(*recommendation.ExhaustionAt)
		out.ExhaustionAt = &at
	}

	return out
}

func quotaFromCompute(compute Compute) governv1alpha1.Quota {
	return governv1alpha1.Quota{
		CPU:     *resource.NewMilliQuantity(compute.CPUMillicores, resource.DecimalSI),
		Memory:  *resource.NewQuantity(compute.MemoryBytes, resource.BinarySI),
		Storage: *resource.NewQuantity(compute.StorageBytes, resource.BinarySI),
		Pods:    compute.Pods,
	}
}

func quotaOf(quota governv1alpha1.Quota) Compute {
	return Compute{
		CPUMillicores: quota.CPU.MilliValue(),
		MemoryBytes:   quota.Memory.Value(),
		StorageBytes:  quota.Storage.Value(),
		Pods:          quota.Pods,
	}
}

func firstBlocking(result *governv1alpha1.PreflightResult) string {
	for _, finding := range result.Findings {
		if finding.Severity == "block" {
			return finding.Message
		}
	}

	return "the target does not fit"
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
		if (*conditions)[i].Status == state && (*conditions)[i].Reason == reason && (*conditions)[i].Message == message {
			return
		}
		if (*conditions)[i].Status == state {
			condition.LastTransitionTime = (*conditions)[i].LastTransitionTime
		}
		(*conditions)[i] = condition

		return
	}

	*conditions = append(*conditions, condition)
}

func equalRequestStatus(current, next *governv1alpha1.QuotaRequestStatus) bool {
	if current.Phase != next.Phase ||
		current.EvidenceDigest != next.EvidenceDigest ||
		current.DecisionRef != next.DecisionRef ||
		current.ObservedGeneration != next.ObservedGeneration ||
		len(current.Conditions) != len(next.Conditions) {
		return false
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
