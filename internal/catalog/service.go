package catalog

import (
	"context"
	"errors"
	"fmt"
	"math"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	governv1 "github.com/marstack-labs/marstack-govern/gen/marstack/govern/v1"
)

type Service struct {
	store *Store
}

func NewService(store *Store) *Service {
	return &Service{store: store}
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

	workloads, next, err := s.store.ListWorkloads(ctx, filter)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
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
	context.Context,
	*connect.Request[governv1.GetProvenanceRequest],
) (*connect.Response[governv1.GetProvenanceResponse], error) {
	return nil, unimplemented("provenance")
}

func (s *Service) GetTimeline(
	context.Context,
	*connect.Request[governv1.GetTimelineRequest],
) (*connect.Response[governv1.GetTimelineResponse], error) {
	return nil, unimplemented("timeline")
}

func (s *Service) ExplainFailure(
	context.Context,
	*connect.Request[governv1.ExplainFailureRequest],
) (*connect.Response[governv1.ExplainFailureResponse], error) {
	return nil, unimplemented("failure explanation")
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
