package tenancy

import (
	"context"
	"errors"
	"fmt"

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

func (s *Service) ListDivisions(
	ctx context.Context,
	req *connect.Request[governv1.ListDivisionsRequest],
) (*connect.Response[governv1.ListDivisionsResponse], error) {
	divisions, err := s.store.ListDivisions(ctx, nil)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	response := &governv1.ListDivisionsResponse{
		Divisions: make([]*governv1.Division, 0, len(divisions)),
		Page:      &governv1.PageInfo{},
		Freshness: freshnessOf(divisions),
	}
	for _, division := range divisions {
		response.Divisions = append(response.Divisions, protoDivision(division))
	}

	_ = req

	return connect.NewResponse(response), nil
}

func (s *Service) GetDivision(
	ctx context.Context,
	req *connect.Request[governv1.GetDivisionRequest],
) (*connect.Response[governv1.GetDivisionResponse], error) {
	division, err := s.store.GetDivision(ctx, req.Msg.GetDivision())
	if errors.Is(err, ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("division %s not found", req.Msg.GetDivision()))
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&governv1.GetDivisionResponse{
		Division:  protoDivision(division),
		Freshness: freshnessOf([]Division{division}),
	}), nil
}

func (s *Service) ListNamespaces(
	ctx context.Context,
	req *connect.Request[governv1.ListNamespacesRequest],
) (*connect.Response[governv1.ListNamespacesResponse], error) {
	namespaces, err := s.store.ListNamespaces(ctx, req.Msg.GetDivision())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	response := &governv1.ListNamespacesResponse{
		Namespaces: make([]*governv1.Namespace, 0, len(namespaces)),
		Page:       &governv1.PageInfo{},
		Freshness:  &governv1.Freshness{},
	}
	for _, namespace := range namespaces {
		response.Namespaces = append(response.Namespaces, &governv1.Namespace{
			Name:               namespace.Name,
			Division:           namespace.Division,
			Environment:        namespace.Environment,
			DefaultDenyPresent: namespace.DefaultDenyPresent,
			LimitRangePresent:  namespace.LimitRangePresent,
			CreatedAt:          timestamppb.New(namespace.CreatedAt),
		})
		if namespace.ObservedAt.After(response.Freshness.GetObservedAt().AsTime()) {
			response.Freshness.ObservedAt = timestamppb.New(namespace.ObservedAt)
		}
	}

	return connect.NewResponse(response), nil
}

func (s *Service) ListMembers(
	context.Context,
	*connect.Request[governv1.ListMembersRequest],
) (*connect.Response[governv1.ListMembersResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented,
		errors.New("membership is read from the identity provider, which arrives with the identity slice"))
}

func (s *Service) GetCapacity(
	context.Context,
	*connect.Request[governv1.GetCapacityRequest],
) (*connect.Response[governv1.GetCapacityResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented,
		errors.New("cluster capacity arrives with the simulation slice"))
}

func protoDivision(division Division) *governv1.Division {
	return &governv1.Division{
		Uid:         division.UID,
		Name:        division.Name,
		DisplayName: division.DisplayName,
		TenantName:  division.QuotaBackend,
		Phase:       protoPhase(division.Phase),
		Quota: &governv1.Compute{
			CpuMillicores: division.QuotaCPU,
			MemoryBytes:   division.QuotaMemory,
			StorageBytes:  division.QuotaStorage,
			Pods:          division.QuotaPods,
		},
		Used: &governv1.Compute{
			CpuMillicores: division.UsedCPU,
			MemoryBytes:   division.UsedMemory,
			StorageBytes:  division.UsedStorage,
			Pods:          division.UsedPods,
		},
		Namespaces:  division.Namespaces,
		MemberCount: division.MemberCount,
		CreatedAt:   timestamppb.New(division.CreatedAt),
	}
}

func freshnessOf(divisions []Division) *governv1.Freshness {
	freshness := &governv1.Freshness{}

	for _, division := range divisions {
		if division.ObservedAt.After(freshness.GetObservedAt().AsTime()) {
			freshness.ObservedAt = timestamppb.New(division.ObservedAt)
			freshness.ResourceVersion = division.ResourceVersion
		}
	}

	return freshness
}
