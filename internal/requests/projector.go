package requests

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	governv1alpha1 "github.com/marstack-labs/marstack-govern/api/v1alpha1"
	governv1 "github.com/marstack-labs/marstack-govern/gen/marstack/govern/v1"
)

type Publisher interface {
	Publish(event *governv1.StreamEvent)
}

type Projector struct {
	client.Client
	Store     *Store
	Publisher Publisher
}

func (p *Projector) SetupWithManager(mgr ctrl.Manager) error {
	if err := ctrl.NewControllerManagedBy(mgr).
		For(&governv1alpha1.QuotaRequest{}).
		Named("quota-request-projector").
		Complete(p); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&governv1alpha1.Decision{}).
		Named("decision-projector").
		Complete(&decisionProjector{Client: p.Client, Store: p.Store, Publisher: p.Publisher})
}

func (p *Projector) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	request := &governv1alpha1.QuotaRequest{}
	if err := p.Get(ctx, req.NamespacedName, request); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, p.Store.DeleteRequest(ctx, req.Namespace, req.Name)
		}
		return ctrl.Result{}, err
	}

	projected, err := projectRequest(request)
	if err != nil {
		return ctrl.Result{}, err
	}

	if err := p.Store.UpsertRequest(ctx, projected); err != nil {
		return ctrl.Result{}, err
	}

	publishRequest(p.Publisher, request)

	return ctrl.Result{}, nil
}

type decisionProjector struct {
	client.Client
	Store     *Store
	Publisher Publisher
}

func (p *decisionProjector) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	decision := &governv1alpha1.Decision{}
	if err := p.Get(ctx, req.NamespacedName, decision); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	request := &governv1alpha1.QuotaRequest{}
	key := types.NamespacedName{Namespace: decision.Spec.Request.Namespace, Name: decision.Spec.Request.Name}
	if err := p.Get(ctx, key, request); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	projected, err := projectRequest(request)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := p.Store.UpsertRequest(ctx, projected); err != nil {
		return ctrl.Result{}, err
	}

	evidence, err := json.Marshal(decision.Spec.Evidence)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("encode evidence of %s: %w", decision.Name, err)
	}

	record := Decision{
		UID:           string(decision.UID),
		RequestUID:    string(request.UID),
		Decider:       decision.Spec.DecidedBy,
		Outcome:       strings.ToLower(snake(string(decision.Spec.Outcome))),
		Reason:        decision.Spec.Reason,
		Evidence:      evidence,
		DecidedAt:     decision.CreationTimestamp.Time,
		RevokedReason: decision.Spec.RevokedReason,
	}

	if decision.Spec.GrantedUntil != nil {
		until := decision.Spec.GrantedUntil.Time
		record.GrantedUntil = &until
	}
	if decision.Spec.RevokedReason != "" {
		revoked := time.Now()
		record.RevokedAt = &revoked
	}
	if record.DecidedAt.IsZero() {
		record.DecidedAt = time.Now()
	}

	if err := p.Store.UpsertDecision(ctx, record); err != nil {
		return ctrl.Result{}, err
	}

	publishDecision(p.Publisher, decision)

	return ctrl.Result{}, nil
}

func projectRequest(request *governv1alpha1.QuotaRequest) (Request, error) {
	spec, err := json.Marshal(request.Spec)
	if err != nil {
		return Request{}, fmt.Errorf("encode spec of %s/%s: %w", request.Namespace, request.Name, err)
	}

	projected := Request{
		UID:             string(request.UID),
		Kind:            KindQuota,
		Name:            request.Name,
		Namespace:       request.Namespace,
		Division:        request.Spec.Division,
		Requester:       request.Spec.RequestedBy,
		Reason:          request.Spec.Reason,
		Phase:           phaseOf(request),
		EvidenceDigest:  request.Status.EvidenceDigest,
		Spec:            spec,
		CreatedAt:       request.CreationTimestamp.Time,
		ResourceVersion: request.ResourceVersion,
	}

	if projected.CreatedAt.IsZero() {
		projected.CreatedAt = time.Now()
	}
	if projected.Requester == "" {
		projected.Requester = "unknown"
	}

	if request.Status.Recommendation != nil {
		encoded, err := json.Marshal(request.Status.Recommendation)
		if err != nil {
			return Request{}, fmt.Errorf("encode recommendation: %w", err)
		}
		projected.Recommendation = encoded
	}

	if request.Status.Preflight != nil {
		encoded, err := json.Marshal(request.Status.Preflight)
		if err != nil {
			return Request{}, fmt.Errorf("encode preflight: %w", err)
		}
		projected.Preflight = encoded
	}

	return projected, nil
}

func phaseOf(request *governv1alpha1.QuotaRequest) string {
	if request.Status.Phase == "" {
		return "pending"
	}

	return snake(string(request.Status.Phase))
}

func snake(value string) string {
	out := strings.Builder{}

	for i, char := range value {
		if char >= 'A' && char <= 'Z' {
			if i > 0 {
				out.WriteByte('_')
			}
			out.WriteRune(char + 32)
			continue
		}
		out.WriteRune(char)
	}

	return out.String()
}

func publishRequest(publisher Publisher, request *governv1alpha1.QuotaRequest) {
	if publisher == nil {
		return
	}

	publisher.Publish(&governv1.StreamEvent{
		Type:      governv1.StreamEvent_TYPE_REQUEST_CHANGED,
		EmittedAt: timestamppb.New(time.Now()),
		Body: &governv1.StreamEvent_RequestChanged{
			RequestChanged: &governv1.RequestChanged{
				Request: &governv1.ResourceRequest{
					Uid:       string(request.UID),
					Kind:      governv1.ResourceRequest_KIND_QUOTA,
					Name:      request.Name,
					Namespace: request.Namespace,
					Division:  request.Spec.Division,
					Reason:    request.Spec.Reason,
					Phase:     protoPhase(phaseOf(request)),
					Requester: &governv1.Actor{Subject: request.Spec.RequestedBy},
					CreatedAt: timestamppb.New(request.CreationTimestamp.Time),
				},
			},
		},
	})
}

func publishDecision(publisher Publisher, decision *governv1alpha1.Decision) {
	if publisher == nil {
		return
	}

	publisher.Publish(&governv1.StreamEvent{
		Type:      governv1.StreamEvent_TYPE_DECISION_MADE,
		EmittedAt: timestamppb.New(time.Now()),
		Body: &governv1.StreamEvent_DecisionMade{
			DecisionMade: &governv1.DecisionMade{
				Decision: &governv1.Decision{
					Uid:       string(decision.UID),
					Outcome:   protoOutcome(decision.Spec.Outcome),
					Reason:    decision.Spec.Reason,
					Decider:   &governv1.Actor{Subject: decision.Spec.DecidedBy},
					DecidedAt: timestamppb.New(decision.CreationTimestamp.Time),
				},
			},
		},
	})
}

func protoPhase(phase string) governv1.ResourceRequest_Phase {
	switch phase {
	case "awaiting_decision":
		return governv1.ResourceRequest_PHASE_AWAITING_DECISION
	case "approved":
		return governv1.ResourceRequest_PHASE_APPROVED
	case "rejected":
		return governv1.ResourceRequest_PHASE_REJECTED
	case "changes_requested":
		return governv1.ResourceRequest_PHASE_CHANGES_REQUESTED
	case "applied":
		return governv1.ResourceRequest_PHASE_APPLIED
	case "expired":
		return governv1.ResourceRequest_PHASE_EXPIRED
	case "withdrawn":
		return governv1.ResourceRequest_PHASE_WITHDRAWN
	default:
		return governv1.ResourceRequest_PHASE_PENDING
	}
}

func protoOutcome(outcome governv1alpha1.Outcome) governv1.Decision_Outcome {
	switch outcome {
	case governv1alpha1.OutcomeApproved:
		return governv1.Decision_OUTCOME_APPROVED
	case governv1alpha1.OutcomeRejected:
		return governv1.Decision_OUTCOME_REJECTED
	case governv1alpha1.OutcomeChangesRequested:
		return governv1.Decision_OUTCOME_CHANGES_REQUESTED
	default:
		return governv1.Decision_OUTCOME_UNSPECIFIED
	}
}
