package requests

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	governv1alpha1 "github.com/marstack-labs/marstack-govern/api/v1alpha1"
	governv1 "github.com/marstack-labs/marstack-govern/gen/marstack/govern/v1"
)

type DecisionService struct {
	service *Service
}

func NewDecisionService(service *Service) *DecisionService {
	return &DecisionService{service: service}
}

func (d *DecisionService) ListQueue(
	ctx context.Context,
	req *connect.Request[governv1.ListQueueRequest],
) (*connect.Response[governv1.ListQueueResponse], error) {
	if _, err := d.service.caller(ctx); err != nil {
		return nil, err
	}

	var divisions []string
	if division := req.Msg.GetDivision(); division != "" {
		divisions = []string{division}
	}

	open, err := d.service.store.ListRequests(ctx, divisions, "", true)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	response := &governv1.ListQueueResponse{
		Requests:  make([]*governv1.ResourceRequest, 0, len(open)),
		Page:      &governv1.PageInfo{},
		Freshness: &governv1.Freshness{},
	}
	for _, request := range open {
		converted, err := protoRequest(request)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		response.Requests = append(response.Requests, converted)
	}

	return connect.NewResponse(response), nil
}

func (d *DecisionService) Simulate(
	ctx context.Context,
	req *connect.Request[governv1.SimulateRequest],
) (*connect.Response[governv1.SimulateResponse], error) {
	if _, err := d.service.caller(ctx); err != nil {
		return nil, err
	}

	stored, err := d.service.store.GetRequest(ctx, req.Msg.GetRequestUid())
	if errors.Is(err, ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound,
			fmt.Errorf("request %s not found", req.Msg.GetRequestUid()))
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	request := &governv1alpha1.QuotaRequest{}
	key := types.NamespacedName{Namespace: stored.Namespace, Name: stored.Name}
	if err := d.service.reader.Get(ctx, key, request); err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}

	if request.Status.Simulation == nil {
		return nil, connect.NewError(connect.CodeUnavailable,
			errors.New("the cluster has not been simulated for this request yet"))
	}

	return connect.NewResponse(&governv1.SimulateResponse{
		Simulation: protoSimulation(request),
		Freshness: &governv1.Freshness{
			ObservedAt: timestamppb.New(request.Status.Simulation.SimulatedAt.Time),
		},
	}), nil
}

func protoSimulation(request *governv1alpha1.QuotaRequest) *governv1.ImpactSimulation {
	simulation := request.Status.Simulation

	out := &governv1.ImpactSimulation{
		DivisionQuotaBefore:     protoCompute(recommendedCurrent(request)),
		DivisionQuotaAfter:      protoCompute(request.Spec.Target),
		ClusterCommitmentBefore: float64(simulation.CommitmentBeforePercent) / 100,
		ClusterCommitmentAfter:  float64(simulation.CommitmentAfterPercent) / 100,
		Schedulable:             simulation.Schedulable,
		Verdict:                 simulation.Verdict,
	}

	for _, name := range simulation.NodesExhausted {
		out.NodesNoLongerFitting = append(out.NodesNoLongerFitting, &governv1.NodeCapacity{
			Name:        name,
			Schedulable: false,
		})
	}

	for _, pending := range simulation.PendingNow {
		out.PodsAtRisk = append(out.PodsAtRisk, &governv1.PendingPod{
			Namespace: pending.Namespace,
			Name:      pending.Name,
			Reason:    pending.Reason,
		})
	}

	for range simulation.UnplacedPods {
		out.PodsAtRisk = append(out.PodsAtRisk, &governv1.PendingPod{
			Namespace: request.Spec.Division,
			Name:      "a pod of the division's typical size",
			Reason:    "no node has room for it, even though the quota would allow it",
		})
	}

	if simulation.SimulatedAt != nil {
		out.SimulatedAt = timestamppb.New(simulation.SimulatedAt.Time)
	}

	return out
}

func recommendedCurrent(request *governv1alpha1.QuotaRequest) governv1alpha1.Quota {
	if request.Status.Recommendation != nil {
		return request.Status.Recommendation.Current
	}

	return governv1alpha1.Quota{}
}

func (d *DecisionService) Decide(
	ctx context.Context,
	req *connect.Request[governv1.DecideRequest],
) (*connect.Response[governv1.DecideResponse], error) {
	actor, err := d.service.caller(ctx)
	if err != nil {
		return nil, err
	}

	stored, err := d.service.store.GetRequest(ctx, req.Msg.GetRequestUid())
	if errors.Is(err, ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound,
			fmt.Errorf("request %s not found", req.Msg.GetRequestUid()))
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	writer, err := d.service.asCaller(actor)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	request := &governv1alpha1.QuotaRequest{}
	key := types.NamespacedName{Namespace: stored.Namespace, Name: stored.Name}
	if err := writer.Get(ctx, key, request); err != nil {
		return nil, connect.NewError(connect.CodePermissionDenied, err)
	}

	if !request.Open() {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("request %s is %s and cannot be decided again", request.Name, phaseOf(request)))
	}

	if request.Status.EvidenceDigest == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("the request has no evidence yet, so there is nothing to decide against"))
	}

	if seen := req.Msg.GetEvidenceDigest(); seen != request.Status.EvidenceDigest {
		return nil, connect.NewError(connect.CodeAborted,
			fmt.Errorf("the evidence changed since you read it: you saw %q, the request now carries %q",
				short(seen), short(request.Status.EvidenceDigest)))
	}

	outcome, err := outcomeFrom(req.Msg.GetOutcome())
	if err != nil {
		return nil, err
	}

	decision := &governv1alpha1.Decision{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "decision-",
			Labels: map[string]string{
				"govern.marstack.io/division": request.Spec.Division,
			},
		},
		Spec: governv1alpha1.DecisionSpec{
			Request: governv1alpha1.RequestRef{
				Kind:      KindQuota,
				Name:      request.Name,
				Namespace: request.Namespace,
				UID:       string(request.UID),
			},
			Outcome:   outcome,
			Reason:    req.Msg.GetReason(),
			DecidedBy: actor.Label(),
			Evidence: governv1alpha1.Evidence{
				Recommendation: request.Status.Recommendation,
				Preflight:      request.Status.Preflight,
				Simulation:     request.Status.Simulation,
				Digest:         request.Status.EvidenceDigest,
				CapturedAt:     metav1.Now(),
			},
		},
	}

	if outcome == governv1alpha1.OutcomeApproved {
		until, err := grantUntil(req.Msg.GetGrantDuration())
		if err != nil {
			return nil, err
		}
		decision.Spec.GrantedUntil = until
	}

	if err := writer.Create(ctx, decision); err != nil {
		return nil, connect.NewError(connect.CodePermissionDenied,
			fmt.Errorf("record the decision as %s: %w", actor.Label(), err))
	}

	return connect.NewResponse(&governv1.DecideResponse{Decision: protoDecision(decision)}), nil
}

func (d *DecisionService) ListDecisions(
	ctx context.Context,
	req *connect.Request[governv1.ListDecisionsRequest],
) (*connect.Response[governv1.ListDecisionsResponse], error) {
	if _, err := d.service.caller(ctx); err != nil {
		return nil, err
	}

	var divisions []string
	if division := req.Msg.GetDivision(); division != "" {
		divisions = []string{division}
	}

	requests, err := d.service.store.ListRequests(ctx, divisions, "", false)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	response := &governv1.ListDecisionsResponse{
		Page:      &governv1.PageInfo{},
		Freshness: &governv1.Freshness{},
	}
	for _, request := range requests {
		if request.Decision == nil {
			continue
		}
		response.Decisions = append(response.Decisions, protoStoredDecision(*request.Decision))
	}

	return connect.NewResponse(response), nil
}

func (d *DecisionService) GetDecision(
	ctx context.Context,
	req *connect.Request[governv1.GetDecisionRequest],
) (*connect.Response[governv1.GetDecisionResponse], error) {
	if _, err := d.service.caller(ctx); err != nil {
		return nil, err
	}

	decision := &governv1alpha1.Decision{}
	if err := d.service.reader.Get(ctx, types.NamespacedName{Name: req.Msg.GetUid()}, decision); err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("decision %s not found", req.Msg.GetUid()))
	}

	return connect.NewResponse(&governv1.GetDecisionResponse{Decision: protoDecision(decision)}), nil
}

func (d *DecisionService) Revoke(
	ctx context.Context,
	req *connect.Request[governv1.RevokeRequest],
) (*connect.Response[governv1.RevokeResponse], error) {
	actor, err := d.service.caller(ctx)
	if err != nil {
		return nil, err
	}

	if req.Msg.GetReason() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("say why the grant is being revoked"))
	}

	writer, err := d.service.asCaller(actor)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	decision := &governv1alpha1.Decision{}
	if err := writer.Get(ctx, types.NamespacedName{Name: req.Msg.GetUid()}, decision); err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("decision %s not found", req.Msg.GetUid()))
	}

	patched := decision.DeepCopy()
	patched.Spec.RevokedReason = req.Msg.GetReason()
	now := metav1.Now()
	patched.Spec.GrantedUntil = &now

	if err := writer.Patch(ctx, patched, client.MergeFrom(decision)); err != nil {
		return nil, connect.NewError(connect.CodePermissionDenied,
			fmt.Errorf("revoke as %s: %w", actor.Label(), err))
	}

	return connect.NewResponse(&governv1.RevokeResponse{Decision: protoDecision(patched)}), nil
}

func outcomeFrom(outcome governv1.Decision_Outcome) (governv1alpha1.Outcome, error) {
	switch outcome {
	case governv1.Decision_OUTCOME_APPROVED:
		return governv1alpha1.OutcomeApproved, nil
	case governv1.Decision_OUTCOME_REJECTED:
		return governv1alpha1.OutcomeRejected, nil
	case governv1.Decision_OUTCOME_CHANGES_REQUESTED:
		return governv1alpha1.OutcomeChangesRequested, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("name the outcome"))
	}
}

func grantUntil(duration string) (*metav1.Time, error) {
	window := defaultGrant

	if duration != "" {
		parsed, err := time.ParseDuration(duration)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument,
				fmt.Errorf("grant duration %q is not a duration like 720h", duration))
		}
		if parsed <= 0 {
			return nil, connect.NewError(connect.CodeInvalidArgument,
				errors.New("a grant has to last longer than zero"))
		}
		window = parsed
	}

	until := metav1.NewTime(time.Now().Add(window))

	return &until, nil
}

func protoDecision(decision *governv1alpha1.Decision) *governv1.Decision {
	out := &governv1.Decision{
		Uid:        string(decision.UID),
		RequestUid: decision.Spec.Request.UID,
		Decider:    &governv1.Actor{Subject: decision.Spec.DecidedBy},
		Outcome:    protoOutcome(decision.Spec.Outcome),
		Reason:     decision.Spec.Reason,
		DecidedAt:  timestamppb.New(decision.CreationTimestamp.Time),
		Evidence: &governv1.Evidence{
			Recommendation: protoRecommendation(decision.Spec.Evidence.Recommendation),
			Preflight:      protoPreflight(decision.Spec.Evidence.Preflight),
			CapturedAt:     timestamppb.New(decision.Spec.Evidence.CapturedAt.Time),
		},
	}

	if decision.Spec.GrantedUntil != nil {
		out.GrantedUntil = timestamppb.New(decision.Spec.GrantedUntil.Time)
	}
	if decision.Spec.RevokedReason != "" {
		out.RevokedReason = decision.Spec.RevokedReason
	}

	return out
}

func protoStoredDecision(decision Decision) *governv1.Decision {
	out := &governv1.Decision{
		Uid:           decision.UID,
		RequestUid:    decision.RequestUID,
		Decider:       &governv1.Actor{Subject: decision.Decider},
		Outcome:       storedOutcome(decision.Outcome),
		Reason:        decision.Reason,
		RevokedReason: decision.RevokedReason,
		DecidedAt:     timestamppb.New(decision.DecidedAt),
	}

	if decision.GrantedUntil != nil {
		out.GrantedUntil = timestamppb.New(*decision.GrantedUntil)
	}
	if decision.RevokedAt != nil {
		out.RevokedAt = timestamppb.New(*decision.RevokedAt)
	}

	return out
}

func storedOutcome(outcome string) governv1.Decision_Outcome {
	switch outcome {
	case "approved":
		return governv1.Decision_OUTCOME_APPROVED
	case "rejected":
		return governv1.Decision_OUTCOME_REJECTED
	case "changes_requested":
		return governv1.Decision_OUTCOME_CHANGES_REQUESTED
	default:
		return governv1.Decision_OUTCOME_UNSPECIFIED
	}
}

func short(digest string) string {
	if len(digest) <= 12 {
		return digest
	}

	return digest[:12]
}
