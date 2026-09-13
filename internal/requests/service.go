package requests

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	governv1alpha1 "github.com/marstack-labs/marstack-govern/api/v1alpha1"
	governv1 "github.com/marstack-labs/marstack-govern/gen/marstack/govern/v1"
	"github.com/marstack-labs/marstack-govern/internal/identity"
	"github.com/marstack-labs/marstack-govern/internal/metrics"
)

const defaultGrant = 90 * 24 * time.Hour

type ClientFactory func(actor identity.Actor) (client.Client, error)

type Service struct {
	reader      client.Client
	asCaller    ClientFactory
	store       *Store
	recommender *Recommender
	preflight   *Preflight
}

func NewService(reader client.Client, asCaller ClientFactory, store *Store, recommender *Recommender, preflight *Preflight) *Service {
	return &Service{
		reader:      reader,
		asCaller:    asCaller,
		store:       store,
		recommender: recommender,
		preflight:   preflight,
	}
}

func (s *Service) RecommendQuota(
	ctx context.Context,
	req *connect.Request[governv1.RecommendQuotaRequest],
) (*connect.Response[governv1.RecommendQuotaResponse], error) {
	if _, err := s.caller(ctx); err != nil {
		return nil, err
	}

	division, err := s.division(ctx, req.Msg.GetDivision())
	if err != nil {
		return nil, err
	}

	if s.recommender == nil || !s.recommender.Available() {
		return nil, connect.NewError(connect.CodeUnavailable,
			errors.New("no metrics source is configured, so no number can be proposed"))
	}

	current := quotaOf(division.Spec.Quota)

	recommendation, err := s.recommender.Recommend(ctx, division.Status.Namespaces, current)
	if err != nil {
		if errors.Is(err, ErrNoUsage) || errors.Is(err, metrics.ErrUnavailable) {
			return nil, connect.NewError(connect.CodeUnavailable, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&governv1.RecommendQuotaResponse{
		Recommendation: protoRecommendation(toAPIRecommendation(recommendation, current)),
		Freshness:      &governv1.Freshness{ObservedAt: timestamppb.New(time.Now())},
	}), nil
}

func (s *Service) Preflight(
	ctx context.Context,
	req *connect.Request[governv1.PreflightRequest],
) (*connect.Response[governv1.PreflightResponse], error) {
	if _, err := s.caller(ctx); err != nil {
		return nil, err
	}

	if s.preflight == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("no capacity source is configured"))
	}

	target := computeFromProto(req.Msg.GetQuota().GetTarget())

	result, err := s.preflight.Evaluate(ctx, req.Msg.GetDivision(), target)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&governv1.PreflightResponse{
		Result:    protoPreflight(result),
		Freshness: &governv1.Freshness{ObservedAt: timestamppb.New(time.Now())},
	}), nil
}

func (s *Service) SubmitRequest(
	ctx context.Context,
	req *connect.Request[governv1.SubmitRequestRequest],
) (*connect.Response[governv1.SubmitRequestResponse], error) {
	actor, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}

	if req.Msg.GetKind() != governv1.ResourceRequest_KIND_QUOTA {
		return nil, connect.NewError(connect.CodeUnimplemented,
			errors.New("only quota requests are accepted today"))
	}

	division, err := s.division(ctx, req.Msg.GetDivision())
	if err != nil {
		return nil, err
	}
	if len(division.Status.Namespaces) == 0 {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("division %s has no namespace to file the request in yet", division.Name))
	}

	reason := strings.TrimSpace(req.Msg.GetReason())
	if len(reason) < 10 {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("a request needs a reason of at least ten characters"))
	}

	writer, err := s.asCaller(actor)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	namespace := division.Status.Namespaces[0]

	if existing, found, err := s.byIdempotencyKey(ctx, namespace, req.Msg.GetIdempotencyKey()); err != nil {
		return nil, err
	} else if found {
		return connect.NewResponse(&governv1.SubmitRequestResponse{Request: protoRequestFromCR(existing)}), nil
	}

	target := computeFromProto(req.Msg.GetQuota().GetTarget())

	request := &governv1alpha1.QuotaRequest{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "quota-",
			Namespace:    namespace,
			Labels: map[string]string{
				"govern.marstack.io/division": division.Name,
			},
		},
		Spec: governv1alpha1.QuotaRequestSpec{
			Division:       division.Name,
			Target:         quotaFromCompute(target),
			Reason:         reason,
			RequestedBy:    actor.Label(),
			IdempotencyKey: req.Msg.GetIdempotencyKey(),
		},
	}

	if err := writer.Create(ctx, request); err != nil {
		return nil, connect.NewError(connect.CodePermissionDenied,
			fmt.Errorf("create the request as %s: %w", actor.Label(), err))
	}

	return connect.NewResponse(&governv1.SubmitRequestResponse{Request: protoRequestFromCR(request)}), nil
}

func (s *Service) ListRequests(
	ctx context.Context,
	req *connect.Request[governv1.ListRequestsRequest],
) (*connect.Response[governv1.ListRequestsResponse], error) {
	if _, err := s.caller(ctx); err != nil {
		return nil, err
	}

	var divisions []string
	if division := req.Msg.GetDivision(); division != "" {
		divisions = []string{division}
	}

	found, err := s.store.ListRequests(ctx, divisions, "", false)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	response := &governv1.ListRequestsResponse{
		Requests:  make([]*governv1.ResourceRequest, 0, len(found)),
		Page:      &governv1.PageInfo{},
		Freshness: &governv1.Freshness{},
	}
	for _, request := range found {
		converted, err := protoRequest(request)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		response.Requests = append(response.Requests, converted)
	}

	return connect.NewResponse(response), nil
}

func (s *Service) GetRequest(
	ctx context.Context,
	req *connect.Request[governv1.GetRequestRequest],
) (*connect.Response[governv1.GetRequestResponse], error) {
	if _, err := s.caller(ctx); err != nil {
		return nil, err
	}

	found, err := s.store.GetRequest(ctx, req.Msg.GetUid())
	if errors.Is(err, ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("request %s not found", req.Msg.GetUid()))
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	converted, err := protoRequest(found)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&governv1.GetRequestResponse{Request: converted}), nil
}

func (s *Service) WithdrawRequest(
	ctx context.Context,
	req *connect.Request[governv1.WithdrawRequestRequest],
) (*connect.Response[governv1.WithdrawRequestResponse], error) {
	actor, err := s.caller(ctx)
	if err != nil {
		return nil, err
	}

	stored, err := s.store.GetRequest(ctx, req.Msg.GetUid())
	if errors.Is(err, ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("request %s not found", req.Msg.GetUid()))
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	writer, err := s.asCaller(actor)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	request := &governv1alpha1.QuotaRequest{}
	key := types.NamespacedName{Namespace: stored.Namespace, Name: stored.Name}
	if err := writer.Get(ctx, key, request); err != nil {
		return nil, connect.NewError(connect.CodePermissionDenied, err)
	}

	patched := request.DeepCopy()
	patched.Spec.Withdrawn = true

	if err := writer.Patch(ctx, patched, client.MergeFrom(request)); err != nil {
		return nil, connect.NewError(connect.CodePermissionDenied,
			fmt.Errorf("withdraw as %s: %w", actor.Label(), err))
	}

	return connect.NewResponse(&governv1.WithdrawRequestResponse{Request: protoRequestFromCR(patched)}), nil
}

func (s *Service) byIdempotencyKey(ctx context.Context, namespace, key string) (*governv1alpha1.QuotaRequest, bool, error) {
	if key == "" {
		return nil, false, nil
	}

	list := &governv1alpha1.QuotaRequestList{}
	if err := s.reader.List(ctx, list, client.InNamespace(namespace)); err != nil {
		return nil, false, connect.NewError(connect.CodeInternal, err)
	}

	for i := range list.Items {
		if list.Items[i].Spec.IdempotencyKey == key {
			return &list.Items[i], true, nil
		}
	}

	return nil, false, nil
}

func (s *Service) division(ctx context.Context, name string) (*governv1alpha1.Division, error) {
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("name the division explicitly"))
	}

	division := &governv1alpha1.Division{}
	if err := s.reader.Get(ctx, types.NamespacedName{Name: name}, division); err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("division %s not found", name))
	}

	return division, nil
}

func (s *Service) caller(ctx context.Context) (identity.Actor, error) {
	actor, ok := identity.FromContext(ctx)
	if !ok {
		return identity.Actor{}, connect.NewError(connect.CodeUnauthenticated, errors.New("sign in at /auth/login"))
	}

	return actor, nil
}

func computeFromProto(compute *governv1.Compute) Compute {
	return Compute{
		CPUMillicores: compute.GetCpuMillicores(),
		MemoryBytes:   compute.GetMemoryBytes(),
		StorageBytes:  compute.GetStorageBytes(),
		Pods:          compute.GetPods(),
	}
}

func protoCompute(quota governv1alpha1.Quota) *governv1.Compute {
	return &governv1.Compute{
		CpuMillicores: quota.CPU.MilliValue(),
		MemoryBytes:   quota.Memory.Value(),
		StorageBytes:  quota.Storage.Value(),
		Pods:          quota.Pods,
	}
}

func protoRecommendation(recommendation *governv1alpha1.Recommendation) *governv1.QuotaRecommendation {
	if recommendation == nil {
		return nil
	}

	out := &governv1.QuotaRecommendation{
		Current:       protoCompute(recommendation.Current),
		ObservedP95:   protoCompute(recommendation.ObservedP95),
		ObservedP99:   protoCompute(recommendation.ObservedP99),
		Proposed:      protoCompute(recommendation.Proposed),
		HeadroomRatio: float64(recommendation.HeadroomPercent) / 100,
		Window:        recommendation.Window,
		Basis:         recommendation.Basis,
	}

	if recommendation.ExhaustionAt != nil {
		out.ExhaustionAt = timestamppb.New(recommendation.ExhaustionAt.Time)
	}

	return out
}

func protoPreflight(result *governv1alpha1.PreflightResult) *governv1.PreflightResult {
	if result == nil {
		return nil
	}

	out := &governv1.PreflightResult{
		Admitted:              result.Admitted,
		QuotaUtilisationAfter: float64(result.ClusterCommitmentPercent) / 100,
	}

	for _, finding := range result.Findings {
		severity := governv1.PolicyRejection_SEVERITY_WARN
		if finding.Severity == "block" {
			severity = governv1.PolicyRejection_SEVERITY_BLOCK
		}

		out.Rejections = append(out.Rejections, &governv1.PolicyRejection{
			Policy:   finding.Check,
			Rule:     finding.Check,
			Field:    finding.Field,
			Message:  finding.Message,
			Severity: severity,
		})
	}

	if result.EvaluatedAt != nil {
		out.EvaluatedAt = timestamppb.New(result.EvaluatedAt.Time)
	}

	return out
}

func protoRequestFromCR(request *governv1alpha1.QuotaRequest) *governv1.ResourceRequest {
	return &governv1.ResourceRequest{
		Uid:            string(request.UID),
		Kind:           governv1.ResourceRequest_KIND_QUOTA,
		Name:           request.Name,
		Namespace:      request.Namespace,
		Division:       request.Spec.Division,
		Reason:         request.Spec.Reason,
		Phase:          protoPhase(phaseOf(request)),
		Requester:      &governv1.Actor{Subject: request.Spec.RequestedBy},
		Quota:          &governv1.QuotaSpec{Target: protoCompute(request.Spec.Target)},
		Recommendation: protoRecommendation(request.Status.Recommendation),
		Preflight:      protoPreflight(request.Status.Preflight),
		CreatedAt:      timestamppb.New(request.CreationTimestamp.Time),
	}
}

func protoRequest(request Request) (*governv1.ResourceRequest, error) {
	var spec governv1alpha1.QuotaRequestSpec
	if err := json.Unmarshal(request.Spec, &spec); err != nil {
		return nil, fmt.Errorf("decode stored spec of %s: %w", request.UID, err)
	}

	out := &governv1.ResourceRequest{
		Uid:       request.UID,
		Kind:      governv1.ResourceRequest_KIND_QUOTA,
		Name:      request.Name,
		Namespace: request.Namespace,
		Division:  request.Division,
		Reason:    request.Reason,
		Phase:     protoPhase(request.Phase),
		Requester: &governv1.Actor{Subject: request.Requester},
		Quota:     &governv1.QuotaSpec{Target: protoCompute(spec.Target)},
		CreatedAt: timestamppb.New(request.CreatedAt),
	}

	if len(request.Recommendation) > 0 && string(request.Recommendation) != "null" {
		var recommendation governv1alpha1.Recommendation
		if err := json.Unmarshal(request.Recommendation, &recommendation); err != nil {
			return nil, fmt.Errorf("decode stored recommendation of %s: %w", request.UID, err)
		}
		out.Recommendation = protoRecommendation(&recommendation)
	}

	if len(request.Preflight) > 0 && string(request.Preflight) != "null" {
		var preflight governv1alpha1.PreflightResult
		if err := json.Unmarshal(request.Preflight, &preflight); err != nil {
			return nil, fmt.Errorf("decode stored preflight of %s: %w", request.UID, err)
		}
		out.Preflight = protoPreflight(&preflight)
	}

	return out, nil
}
