package identity

import (
	"context"
	"errors"
	"net/http"
	"time"

	"connectrpc.com/connect"
)

func (s *Sealer) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		session, err := s.Read(r, time.Now())
		if err != nil {
			if errors.Is(err, ErrTampered) || errors.Is(err, ErrSessionExpired) {
				s.Clear(w)
			}
			next.ServeHTTP(w, r)
			return
		}

		next.ServeHTTP(w, r.WithContext(NewContext(r.Context(), ActorFromSession(session))))
	})
}

func RequireActor() connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if _, ok := FromContext(ctx); !ok {
				return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("sign in at /auth/login"))
			}

			return next(ctx, req)
		}
	}
}
