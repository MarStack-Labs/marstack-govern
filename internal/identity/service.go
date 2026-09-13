package identity

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	governv1 "github.com/marstack-labs/marstack-govern/gen/marstack/govern/v1"
)

type AccessGrant struct {
	Role  string
	Group string
}

type DivisionAccess struct {
	Division    string
	DisplayName string
	Grants      []AccessGrant
	Namespaces  []string
}

type DivisionReader interface {
	DivisionAccess(ctx context.Context) ([]DivisionAccess, error)
}

type Membership struct {
	Division    string
	DisplayName string
	Roles       []string
	Namespaces  []string
}

type Service struct {
	sealer     *Sealer
	divisions  DivisionReader
	authorizer *Authorizer
}

func NewService(sealer *Sealer, divisions DivisionReader, authorizer *Authorizer) *Service {
	return &Service{sealer: sealer, divisions: divisions, authorizer: authorizer}
}

func (s *Service) GetSession(
	ctx context.Context,
	_ *connect.Request[governv1.GetSessionRequest],
) (*connect.Response[governv1.GetSessionResponse], error) {
	actor, ok := FromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("sign in at /auth/login"))
	}

	session, err := s.describe(ctx, actor)
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&governv1.GetSessionResponse{Session: session}), nil
}

func (s *Service) SwitchDivision(
	ctx context.Context,
	req *connect.Request[governv1.SwitchDivisionRequest],
) (*connect.Response[governv1.SwitchDivisionResponse], error) {
	actor, ok := FromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("sign in at /auth/login"))
	}

	memberships, _, err := s.membershipsOf(ctx, actor)
	if err != nil {
		return nil, err
	}

	wanted := req.Msg.GetDivision()
	if !slices.ContainsFunc(memberships, func(m Membership) bool { return m.Division == wanted }) {
		return nil, connect.NewError(connect.CodePermissionDenied,
			fmt.Errorf("%s is not a member of %s", actor.Label(), wanted))
	}

	actor.ActiveDivision = wanted

	cookie, err := s.sealer.CookieFor(SessionFromActor(actor))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	described, err := s.describe(ctx, actor)
	if err != nil {
		return nil, err
	}

	response := connect.NewResponse(&governv1.SwitchDivisionResponse{Session: described})
	response.Header().Add("Set-Cookie", cookie.String())

	return response, nil
}

func (s *Service) describe(ctx context.Context, actor Actor) (*governv1.Session, error) {
	memberships, approver, err := s.membershipsOf(ctx, actor)
	if err != nil {
		return nil, err
	}

	session := &governv1.Session{
		Actor: &governv1.Actor{
			Subject:        actor.Label(),
			Groups:         actor.Groups,
			ActingDivision: actor.ActiveDivision,
		},
		ActiveDivision:   actor.ActiveDivision,
		PlatformApprover: approver,
		AuthMode:         authMode(actor.Mode),
	}

	if !actor.ExpiresAt.IsZero() {
		session.ExpiresAt = timestamppb.New(actor.ExpiresAt)
	}

	for _, membership := range memberships {
		session.Memberships = append(session.Memberships, &governv1.DivisionMembership{
			Division:    membership.Division,
			DisplayName: membership.DisplayName,
			Roles:       protoRoles(membership.Roles),
			Namespaces:  membership.Namespaces,
		})
	}

	if session.ActiveDivision == "" && len(memberships) > 0 {
		session.ActiveDivision = memberships[0].Division
	}

	return session, nil
}

func (s *Service) membershipsOf(ctx context.Context, actor Actor) ([]Membership, bool, error) {
	access, err := s.divisions.DivisionAccess(ctx)
	if err != nil {
		return nil, false, connect.NewError(connect.CodeInternal, err)
	}

	candidates := []string{}
	for _, division := range access {
		candidates = append(candidates, division.Namespaces...)
	}

	scope, err := s.authorizer.ScopeFor(ctx, actor, candidates)
	if err != nil {
		return nil, false, connect.NewError(connect.CodeUnavailable, err)
	}

	memberships := []Membership{}
	for _, division := range access {
		roles := []string{}
		for _, grant := range division.Grants {
			if actor.InGroup(grant.Group) && !slices.Contains(roles, grant.Role) {
				roles = append(roles, grant.Role)
			}
		}

		visible := []string{}
		for _, namespace := range division.Namespaces {
			if scope.Allows(namespace) {
				visible = append(visible, namespace)
			}
		}

		if len(roles) == 0 && len(visible) == 0 {
			continue
		}

		memberships = append(memberships, Membership{
			Division:    division.Division,
			DisplayName: division.DisplayName,
			Roles:       roles,
			Namespaces:  visible,
		})
	}

	return memberships, scope.AllowAll, nil
}

func (s *Service) Scope(ctx context.Context, actor Actor) (Scope, error) {
	access, err := s.divisions.DivisionAccess(ctx)
	if err != nil {
		return Scope{}, err
	}

	candidates := []string{}
	for _, division := range access {
		for _, grant := range division.Grants {
			if actor.InGroup(grant.Group) {
				candidates = append(candidates, division.Namespaces...)
				break
			}
		}
	}

	return s.authorizer.ScopeFor(ctx, actor, candidates)
}

func authMode(mode AuthMode) governv1.Session_AuthMode {
	switch mode {
	case AuthModeOIDC:
		return governv1.Session_AUTH_MODE_OIDC
	case AuthModeDev:
		return governv1.Session_AUTH_MODE_DEV
	default:
		return governv1.Session_AUTH_MODE_UNSPECIFIED
	}
}

func protoRoles(roles []string) []governv1.Member_Role {
	out := make([]governv1.Member_Role, 0, len(roles))

	for _, role := range roles {
		switch role {
		case "admin":
			out = append(out, governv1.Member_ROLE_ADMIN)
		case "operator":
			out = append(out, governv1.Member_ROLE_OPERATOR)
		case "viewer":
			out = append(out, governv1.Member_ROLE_VIEWER)
		case "approver":
			out = append(out, governv1.Member_ROLE_APPROVER)
		}
	}

	return out
}
