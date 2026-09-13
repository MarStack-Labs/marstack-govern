package topology

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	governv1alpha1 "github.com/marstack-labs/marstack-govern/api/v1alpha1"
	governv1 "github.com/marstack-labs/marstack-govern/gen/marstack/govern/v1"
	"github.com/marstack-labs/marstack-govern/internal/identity"
)

type Service struct {
	reader       client.Client
	graph        *Graph
	reachability *Reachability
}

func NewService(reader client.Client, graph *Graph) *Service {
	return &Service{reader: reader, graph: graph, reachability: NewReachability(reader)}
}

func (s *Service) GetServiceGraph(
	ctx context.Context,
	req *connect.Request[governv1.GetServiceGraphRequest],
) (*connect.Response[governv1.GetServiceGraphResponse], error) {
	if _, err := s.caller(ctx); err != nil {
		return nil, err
	}

	namespaces, err := s.namespaces(ctx, req.Msg.GetDivision())
	if err != nil {
		return nil, err
	}

	edges, err := s.graph.Edges(ctx, namespaces)
	if errors.Is(err, ErrNoTraces) {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	response := &governv1.GetServiceGraphResponse{
		Edges:      make([]*governv1.ServiceEdge, 0, len(edges)),
		Freshness:  &governv1.Freshness{ObservedAt: timestamppb.New(time.Now())},
		ObservedAt: timestamppb.New(time.Now()),
	}
	for _, edge := range edges {
		response.Edges = append(response.Edges, &governv1.ServiceEdge{
			Client:            edge.Client,
			Server:            edge.Server,
			RequestsPerSecond: edge.RequestsPerS,
			ErrorRatio:        edge.ErrorRatio(),
		})
	}

	return connect.NewResponse(response), nil
}

func (s *Service) GetReachability(
	ctx context.Context,
	req *connect.Request[governv1.GetReachabilityRequest],
) (*connect.Response[governv1.GetReachabilityResponse], error) {
	if _, err := s.caller(ctx); err != nil {
		return nil, err
	}

	namespaces, err := s.namespaces(ctx, req.Msg.GetDivision())
	if err != nil {
		return nil, err
	}

	observed := []Edge{}
	traffic := false

	if s.graph.Available() {
		edges, err := s.graph.Edges(ctx, namespaces)
		if err == nil {
			observed = namespaceEdges(edges)
			traffic = true
		}
	}

	paths, err := s.reachability.Paths(ctx, namespaces, observed)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	response := &governv1.GetReachabilityResponse{
		Paths:           make([]*governv1.NetworkPath, 0, len(paths)),
		TrafficObserved: traffic,
		Freshness:       &governv1.Freshness{ObservedAt: timestamppb.New(time.Now())},
	}
	for _, path := range paths {
		response.Paths = append(response.Paths, &governv1.NetworkPath{
			From:      path.From,
			To:        path.To,
			Allowed:   path.Allowed,
			Observed:  path.Observed,
			AllowedBy: path.AllowedBy,
			Verdict:   protoVerdict(path.Verdict),
		})
	}

	return connect.NewResponse(response), nil
}

func (s *Service) namespaces(ctx context.Context, division string) ([]string, error) {
	if division == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("name the division explicitly"))
	}

	found := &governv1alpha1.Division{}
	if err := s.reader.Get(ctx, types.NamespacedName{Name: division}, found); err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("division %s not found", division))
	}

	return found.Status.Namespaces, nil
}

func (s *Service) caller(ctx context.Context) (identity.Actor, error) {
	actor, ok := identity.FromContext(ctx)
	if !ok {
		return identity.Actor{}, connect.NewError(connect.CodeUnauthenticated, errors.New("sign in at /auth/login"))
	}

	return actor, nil
}

func namespaceEdges(edges []Edge) []Edge {
	collapsed := map[string]Edge{}

	for _, edge := range edges {
		client := namespaceOf(edge.Client)
		server := namespaceOf(edge.Server)
		key := client + "\x00" + server

		existing := collapsed[key]
		existing.Client = client
		existing.Server = server
		existing.RequestsPerS += edge.RequestsPerS
		existing.FailedPerS += edge.FailedPerS

		collapsed[key] = existing
	}

	out := make([]Edge, 0, len(collapsed))
	for _, edge := range collapsed {
		out = append(out, edge)
	}

	return out
}

func namespaceOf(service string) string {
	for i := range service {
		if service[i] == '/' {
			return service[:i]
		}
	}

	return service
}

func protoVerdict(verdict Verdict) governv1.NetworkPath_Verdict {
	switch verdict {
	case VerdictAllowedAndUsed:
		return governv1.NetworkPath_VERDICT_ALLOWED_AND_USED
	case VerdictAllowedUnused:
		return governv1.NetworkPath_VERDICT_ALLOWED_UNUSED
	case VerdictObservedNotAllowed:
		return governv1.NetworkPath_VERDICT_OBSERVED_NOT_ALLOWED
	default:
		return governv1.NetworkPath_VERDICT_DENIED
	}
}
