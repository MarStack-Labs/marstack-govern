package catalog

import (
	"context"
	"errors"
	"fmt"
	"math"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	governv1 "github.com/marstack-labs/marstack-govern/gen/marstack/govern/v1"
	"github.com/marstack-labs/marstack-govern/internal/identity"
)

type Scoper interface {
	Scope(ctx context.Context, actor identity.Actor) (identity.Scope, error)
}

type Prover interface {
	Of(ctx context.Context, namespace, image string) (*governv1.Provenance, error)
}

type Diagnoser interface {
	Explain(ctx context.Context, workload Workload) (*governv1.FailureExplanation, error)
	Timeline(ctx context.Context, workload Workload) ([]*governv1.TimelineEvent, error)
}

type Service struct {
	store     *Store
	scoper    Scoper
	prover    Prover
	diagnoser Diagnoser
}

func NewService(store *Store) *Service {
	return &Service{store: store}
}

func (s *Service) WithScope(scoper Scoper) *Service {
	s.scoper = scoper

	return s
}

func (s *Service) WithProvenance(prover Prover) *Service {
	s.prover = prover

	return s
}

func (s *Service) WithDiagnostics(diagnoser Diagnoser) *Service {
	s.diagnoser = diagnoser

	return s
}

func (s *Service) visibleWorkload(ctx context.Context, uid string) (Workload, error) {
	workload, err := s.store.GetWorkload(ctx, uid)
	if errors.Is(err, ErrNotFound) {
		return Workload{}, connect.NewError(connect.CodeNotFound, fmt.Errorf("workload %s not found", uid))
	}
	if err != nil {
		return Workload{}, connect.NewError(connect.CodeInternal, err)
	}

	scope, scoped, err := s.scopeOf(ctx)
	if err != nil {
		return Workload{}, err
	}
	if scoped && !scope.Allows(workload.Namespace) {
		return Workload{}, connect.NewError(connect.CodeNotFound, fmt.Errorf("workload %s not found", uid))
	}

	return workload, nil
}

func (s *Service) scopeOf(ctx context.Context) (identity.Scope, bool, error) {
	if s.scoper == nil {
		return identity.Scope{AllowAll: true}, false, nil
	}

	actor, ok := identity.FromContext(ctx)
	if !ok {
		return identity.Scope{}, true, connect.NewError(connect.CodeUnauthenticated, errors.New("sign in at /auth/login"))
	}

	scope, err := s.scoper.Scope(ctx, actor)
	if err != nil {
		return identity.Scope{}, true, connect.NewError(connect.CodeUnavailable, err)
	}

	return scope, true, nil
}

func (s *Service) ListWorkloads(
	ctx context.Context,
	req *connect.Request[governv1.ListWorkloadsRequest],
) (*connect.Response[governv1.ListWorkloadsResponse], error) {
	msg := req.Msg

	filter := Filter{
		Division:  msg.GetDivision(),
		Namespace: msg.GetNamespace(),
		Health:    healthFilter(msg.GetHealth()),
		Search:    msg.GetSearch(),
	}
	if page := msg.GetPage(); page != nil {
		filter.Limit = page.GetSize()
		filter.Cursor = page.GetToken()
	}

	scope, scoped, err := s.scopeOf(ctx)
	if err != nil {
		return nil, err
	}

	workloads := []Workload{}
	next := ""

	if !scoped || scope.AllowAll {
		workloads, next, err = s.store.ListWorkloads(ctx, filter)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	} else if len(scope.Namespaces) > 0 {
		filter.Namespaces = scope.Namespaces

		workloads, next, err = s.store.ListWorkloads(ctx, filter)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}

	freshness, err := s.freshness(ctx)
	if err != nil {
		return nil, err
	}

	total := len(workloads)
	if total > math.MaxInt32 {
		total = math.MaxInt32
	}

	response := &governv1.ListWorkloadsResponse{
		Workloads: make([]*governv1.Workload, 0, len(workloads)),
		Page:      &governv1.PageInfo{NextToken: next, Total: int32(total)},
		Freshness: freshness,
	}
	for _, workload := range workloads {
		response.Workloads = append(response.Workloads, storedWorkload(workload))
	}

	return connect.NewResponse(response), nil
}

func (s *Service) GetWorkload(
	ctx context.Context,
	req *connect.Request[governv1.GetWorkloadRequest],
) (*connect.Response[governv1.GetWorkloadResponse], error) {
	workload, err := s.store.GetWorkload(ctx, req.Msg.GetUid())
	if err == nil {
		scope, scoped, scopeErr := s.scopeOf(ctx)
		if scopeErr != nil {
			return nil, scopeErr
		}
		if scoped && !scope.Allows(workload.Namespace) {
			err = ErrNotFound
		}
	}
	if errors.Is(err, ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workload %s not found", req.Msg.GetUid()))
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	freshness, err := s.freshness(ctx)
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&governv1.GetWorkloadResponse{
		Workload:  storedWorkload(workload),
		Freshness: freshness,
	}), nil
}

func (s *Service) GetProvenance(
	ctx context.Context,
	req *connect.Request[governv1.GetProvenanceRequest],
) (*connect.Response[governv1.GetProvenanceResponse], error) {
	if s.prover == nil {
		return nil, connect.NewError(connect.CodeUnavailable,
			errors.New("no registry is configured, so nothing is known about where this image came from"))
	}

	workload, err := s.store.GetWorkload(ctx, req.Msg.GetUid())
	if errors.Is(err, ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workload %s not found", req.Msg.GetUid()))
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	scope, scoped, err := s.scopeOf(ctx)
	if err != nil {
		return nil, err
	}
	if scoped && !scope.Allows(workload.Namespace) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workload %s not found", req.Msg.GetUid()))
	}

	if workload.ImageRef == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("%s/%s runs no container image we can trace", workload.Namespace, workload.Name))
	}

	provenance, err := s.prover.Of(ctx, workload.Namespace, workload.ImageRef)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}

	freshness, err := s.freshness(ctx)
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&governv1.GetProvenanceResponse{
		Provenance: provenance,
		Freshness:  freshness,
	}), nil
}

func (s *Service) GetTimeline(
	ctx context.Context,
	req *connect.Request[governv1.GetTimelineRequest],
) (*connect.Response[governv1.GetTimelineResponse], error) {
	if s.diagnoser == nil {
		return nil, unimplemented("timeline")
	}

	workload, err := s.visibleWorkload(ctx, req.Msg.GetUid())
	if err != nil {
		return nil, err
	}

	events, err := s.diagnoser.Timeline(ctx, workload)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}

	freshness, err := s.freshness(ctx)
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&governv1.GetTimelineResponse{
		Events:    events,
		Page:      &governv1.PageInfo{},
		Freshness: freshness,
	}), nil
}

func (s *Service) ExplainFailure(
	ctx context.Context,
	req *connect.Request[governv1.ExplainFailureRequest],
) (*connect.Response[governv1.ExplainFailureResponse], error) {
	if s.diagnoser == nil {
		return nil, unimplemented("failure explanation")
	}

	workload, err := s.visibleWorkload(ctx, req.Msg.GetUid())
	if err != nil {
		return nil, err
	}

	explanation, err := s.diagnoser.Explain(ctx, workload)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}

	freshness, err := s.freshness(ctx)
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&governv1.ExplainFailureResponse{
		Explanation: explanation,
		Freshness:   freshness,
	}), nil
}

func (s *Service) GetDrift(
	context.Context,
	*connect.Request[governv1.GetDriftRequest],
) (*connect.Response[governv1.GetDriftResponse], error) {
	return nil, unimplemented("drift detection")
}

func (s *Service) freshness(ctx context.Context) (*governv1.Freshness, error) {
	freshness, err := s.store.Freshness(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	out := &governv1.Freshness{
		ResourceVersion:      freshness.ResourceVersion,
		ProjectionLagSeconds: freshness.LagSeconds,
	}
	if !freshness.ObservedAt.IsZero() {
		out.ObservedAt = timestamppb.New(freshness.ObservedAt)
	}
	for _, degraded := range freshness.Degraded {
		source := &governv1.DegradedSource{Source: degraded.Source, Reason: degraded.Reason}
		if !degraded.LastHealthyAt.IsZero() {
			source.LastHealthyAt = timestamppb.New(degraded.LastHealthyAt)
		}
		out.Degraded = append(out.Degraded, source)
	}

	return out, nil
}

func storedWorkload(workload Workload) *governv1.Workload {
	return &governv1.Workload{
		Uid:             workload.UID,
		Division:        workload.Division,
		Namespace:       workload.Namespace,
		Name:            workload.Name,
		Kind:            workload.Kind,
		Health:          protoHealth(workload.Health),
		ReplicasDesired: workload.ReplicasDesired,
		ReplicasReady:   workload.ReplicasReady,
		ImageRef:        workload.ImageRef,
		Requested: &governv1.Compute{
			CpuMillicores: workload.CPURequest,
			MemoryBytes:   workload.MemoryRequest,
		},
		CreatedAt: timestamppb.New(workload.CreatedAt),
	}
}

func healthFilter(health governv1.Workload_Health) string {
	switch health {
	case governv1.Workload_HEALTH_HEALTHY:
		return "healthy"
	case governv1.Workload_HEALTH_PROGRESSING:
		return "progressing"
	case governv1.Workload_HEALTH_DEGRADED:
		return "degraded"
	default:
		return ""
	}
}

func unimplemented(feature string) error {
	return connect.NewError(connect.CodeUnimplemented, fmt.Errorf("%s arrives in a later slice", feature))
}
