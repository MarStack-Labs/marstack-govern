package identity

import (
	"context"
	"slices"
	"time"
)

type contextKey struct{}

type Actor struct {
	Subject        string
	Email          string
	Groups         []string
	ActiveDivision string
	Mode           AuthMode
	ExpiresAt      time.Time
}

func (a Actor) InGroup(group string) bool {
	return slices.Contains(a.Groups, group)
}

func (a Actor) Label() string {
	if a.Email != "" {
		return a.Email
	}

	return a.Subject
}

func ActorFromSession(session Session) Actor {
	return Actor{
		Subject:        session.Subject,
		Email:          session.Email,
		Groups:         session.Groups,
		ActiveDivision: session.ActiveDivision,
		Mode:           session.Mode,
		ExpiresAt:      session.ExpiresAt,
	}
}

func NewContext(ctx context.Context, actor Actor) context.Context {
	return context.WithValue(ctx, contextKey{}, actor)
}

func FromContext(ctx context.Context) (Actor, bool) {
	actor, ok := ctx.Value(contextKey{}).(Actor)
	if !ok || actor.Subject == "" {
		return Actor{}, false
	}

	return actor, true
}
