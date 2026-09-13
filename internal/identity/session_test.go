package identity_test

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/marstack-labs/marstack-govern/internal/identity"
)

func TestSessionsSurviveARoundTrip(t *testing.T) {
	sealer := newSealer(t)
	now := time.Now().Truncate(time.Second)

	session := identity.Session{
		Subject:        "u-1",
		Email:          "dev@example.test",
		Groups:         []string{"payments-admins"},
		ActiveDivision: "payments",
		Mode:           identity.AuthModeOIDC,
		IssuedAt:       now,
		ExpiresAt:      now.Add(time.Hour),
	}

	sealed, err := sealer.Seal(session)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	opened, err := sealer.Open(sealed)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	if opened.Subject != session.Subject || opened.Email != session.Email {
		t.Errorf("identity: got %+v", opened)
	}
	if opened.ActiveDivision != "payments" || len(opened.Groups) != 1 {
		t.Errorf("scope: got %+v", opened)
	}
	if !opened.ExpiresAt.Equal(session.ExpiresAt) {
		t.Errorf("expiry: got %v, want %v", opened.ExpiresAt, session.ExpiresAt)
	}
}

func TestATamperedCookieIsRejected(t *testing.T) {
	sealer := newSealer(t)

	sealed, err := sealer.Seal(identity.Session{Subject: "u-1", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	raw, err := base64.RawURLEncoding.DecodeString(sealed)
	if err != nil {
		t.Fatalf("decode sealed session: %v", err)
	}
	raw[len(raw)-1] ^= 0xff

	tampered := base64.RawURLEncoding.EncodeToString(raw)

	if _, err := sealer.Open(tampered); !errors.Is(err, identity.ErrTampered) {
		t.Fatalf("got %v, want ErrTampered", err)
	}
}

func TestAnotherKeyCannotOpenTheSession(t *testing.T) {
	first := newSealer(t)
	second := newSealer(t)

	sealed, err := first.Seal(identity.Session{Subject: "u-1", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	if _, err := second.Open(sealed); !errors.Is(err, identity.ErrTampered) {
		t.Fatalf("got %v, want ErrTampered", err)
	}
}

func TestExpiredSessionsAreRefused(t *testing.T) {
	sealer := newSealer(t)
	now := time.Now()

	recorder := httptest.NewRecorder()
	if err := sealer.Write(recorder, identity.Session{
		Subject:   "u-1",
		IssuedAt:  now.Add(-2 * time.Hour),
		ExpiresAt: now.Add(-time.Hour),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, cookie := range recorder.Result().Cookies() {
		request.AddCookie(cookie)
	}

	if _, err := sealer.Read(request, now); !errors.Is(err, identity.ErrSessionExpired) {
		t.Fatalf("got %v, want ErrSessionExpired", err)
	}
}

func TestTheCookieIsNotReadableByScripts(t *testing.T) {
	sealer := newSealer(t)
	recorder := httptest.NewRecorder()

	if err := sealer.Write(recorder, identity.Session{
		Subject:   "u-1",
		IssuedAt:  time.Now(),
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("got %d cookies", len(cookies))
	}

	cookie := cookies[0]
	if !cookie.HttpOnly {
		t.Error("the session cookie is readable by scripts")
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("same site: got %v", cookie.SameSite)
	}
	if strings.Contains(cookie.Value, "u-1") {
		t.Error("the cookie carries the subject in the clear")
	}
}

func TestALifetimeCannotBeStretched(t *testing.T) {
	sealer := newSealer(t)
	now := time.Now()
	recorder := httptest.NewRecorder()

	if err := sealer.Write(recorder, identity.Session{
		Subject:   "u-1",
		IssuedAt:  now,
		ExpiresAt: now.Add(30 * 24 * time.Hour),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, cookie := range recorder.Result().Cookies() {
		request.AddCookie(cookie)
	}

	session, err := sealer.Read(request, now)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if session.ExpiresAt.After(now.Add(13 * time.Hour)) {
		t.Errorf("the session outlives the maximum lifetime: %v", session.ExpiresAt)
	}
}

func TestMiddlewareAttachesTheActor(t *testing.T) {
	sealer := newSealer(t)
	now := time.Now()

	var seen identity.Actor
	var found bool

	handler := sealer.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen, found = identity.FromContext(r.Context())
	}))

	recorder := httptest.NewRecorder()
	if err := sealer.Write(recorder, identity.Session{
		Subject:        "u-1",
		Email:          "dev@example.test",
		Groups:         []string{"payments-admins"},
		ActiveDivision: "payments",
		IssuedAt:       now,
		ExpiresAt:      now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, cookie := range recorder.Result().Cookies() {
		request.AddCookie(cookie)
	}

	handler.ServeHTTP(httptest.NewRecorder(), request)

	if !found {
		t.Fatal("no actor reached the handler")
	}
	if seen.Label() != "dev@example.test" || !seen.InGroup("payments-admins") {
		t.Errorf("actor: got %+v", seen)
	}
}

func TestMiddlewarePassesAnonymousRequestsThrough(t *testing.T) {
	sealer := newSealer(t)
	reached := false

	handler := sealer.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		reached = true
		if _, ok := identity.FromContext(r.Context()); ok {
			t.Error("an anonymous request carried an actor")
		}
	}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	if !reached {
		t.Fatal("the request never reached the handler")
	}
}

func TestRpcsRefuseAnonymousCallers(t *testing.T) {
	interceptor := identity.RequireActor()

	called := false
	next := connect.UnaryFunc(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		called = true
		return nil, nil
	})

	_, err := interceptor(next)(context.Background(), nil)

	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("got %v, want unauthenticated", err)
	}
	if called {
		t.Error("the handler ran for an anonymous caller")
	}
}

func newSealer(t *testing.T) *identity.Sealer {
	t.Helper()

	key, err := identity.NewKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	sealer, err := identity.NewSealer(key, true)
	if err != nil {
		t.Fatalf("build sealer: %v", err)
	}

	return sealer
}
