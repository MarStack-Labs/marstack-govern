package tenancy

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	governv1 "github.com/marstack-labs/marstack-govern/gen/marstack/govern/v1"
	"github.com/marstack-labs/marstack-govern/internal/identity"
)

type Scoper interface {
	Scope(ctx context.Context, actor identity.Actor) (identity.Scope, error)
}

type Service struct {
	store  *Store
	scoper Scoper
}

func NewService(store *Store) *Service {
	return &Service{store: store}
}

func (s *Service) WithScope(scoper Scoper) *Service {
	s.scoper = scoper

	return s
}

func (s *Service) visible(ctx context.Context, namespaces []string) (bool, error) {
	if s.scoper == nil {
		return true, nil
	}

	actor, ok := identity.FromContext(ctx)
	if !ok {
		return false, connect.NewError(connect.CodeUnauthenticated, errors.New("sign in at /auth/login"))
	}

	scope, err := s.scoper.Scope(ctx, actor)
	if err != nil {
		return false, connect.NewError(connect.CodeUnavailable, err)
	}

	if scope.AllowAll {
		return true, nil
	}

	for _, namespace := range namespaces {
		if scope.Allows(namespace) {
			return true, nil
		}
	}

	return false, nil
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
		visible, err := s.visible(ctx, division.Namespaces)
		if err != nil {
			return nil, err
		}
		if !visible {
			continue
		}

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
	if err == nil {
		visible, scopeErr := s.visible(ctx, division.Namespaces)
		if scopeErr != nil {
			return nil, scopeErr
		}
		if !visible {
			err = ErrNotFound
		}
	}
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
		visible, err := s.visible(ctx, []string{namespace.Name})
		if err != nil {
			return nil, err
		}
		if !visible {
			continue
		}

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
		errors.New("group claims decide membership; listing the people inside a group needs a directory integration with the identity provider"))
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
