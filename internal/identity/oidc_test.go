package identity_test

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/marstack-labs/marstack-govern/internal/identity"
)

func TestSigningInWithOidcStartsASession(t *testing.T) {
	provider := newFakeProvider(t)
	client, server, sealer := newAuthServer(t, provider)

	login := get(t, client, server.URL+"/auth/login?redirect=/services")
	if login.StatusCode != http.StatusFound {
		t.Fatalf("login status: got %d, want 302", login.StatusCode)
	}

	authorize, err := url.Parse(login.Header.Get("Location"))
	if err != nil {
		t.Fatalf("parse authorize url: %v", err)
	}

	if authorize.Query().Get("code_challenge") == "" {
		t.Error("the authorize request carries no pkce challenge")
	}
	if authorize.Query().Get("code_challenge_method") != "S256" {
		t.Errorf("challenge method: got %q", authorize.Query().Get("code_challenge_method"))
	}

	state := authorize.Query().Get("state")
	if state == "" {
		t.Fatal("the authorize request carries no state")
	}

	callback := get(t, client, server.URL+"/auth/callback?code=good&state="+state)
	if callback.StatusCode != http.StatusFound {
		t.Fatalf("callback status: got %d, want 302 (%s)", callback.StatusCode, readBody(t, callback))
	}
	if location := callback.Header.Get("Location"); location != "/services" {
		t.Errorf("redirect: got %q, want /services", location)
	}

	session := sessionFromJar(t, sealer, client, server.URL)
	if session.Subject != "u-42" {
		t.Errorf("subject: got %q, want u-42", session.Subject)
	}
	if session.Email != "dev@example.test" {
		t.Errorf("email: got %q", session.Email)
	}
	if len(session.Groups) != 2 || session.Groups[0] != "payments-admins" {
		t.Errorf("groups: got %v", session.Groups)
	}
	if session.Mode != identity.AuthModeOIDC {
		t.Errorf("mode: got %q", session.Mode)
	}
}

func TestACallbackWithTheWrongStateIsRefused(t *testing.T) {
	provider := newFakeProvider(t)
	client, server, _ := newAuthServer(t, provider)

	if login := get(t, client, server.URL+"/auth/login"); login.StatusCode != http.StatusFound {
		t.Fatalf("login status: got %d", login.StatusCode)
	}

	callback := get(t, client, server.URL+"/auth/callback?code=good&state=someone-elses-state")
	if callback.StatusCode != http.StatusBadRequest {
		t.Fatalf("callback status: got %d, want 400", callback.StatusCode)
	}
}

func TestACallbackWithoutStartingSignInIsRefused(t *testing.T) {
	provider := newFakeProvider(t)
	client, server, _ := newAuthServer(t, provider)

	callback := get(t, client, server.URL+"/auth/callback?code=good&state=made-up")
	if callback.StatusCode != http.StatusBadRequest {
		t.Fatalf("callback status: got %d, want 400", callback.StatusCode)
	}
}

func TestAnIdTokenFromAnotherIssuerIsRefused(t *testing.T) {
	provider := newFakeProvider(t)
	provider.issuerOverride = "https://attacker.example.test"

	client, server, _ := newAuthServer(t, provider)

	login := get(t, client, server.URL+"/auth/login")
	authorize, err := url.Parse(login.Header.Get("Location"))
	if err != nil {
		t.Fatalf("parse authorize url: %v", err)
	}

	callback := get(t, client, server.URL+"/auth/callback?code=good&state="+authorize.Query().Get("state"))
	if callback.StatusCode != http.StatusUnauthorized {
		t.Fatalf("callback status: got %d, want 401", callback.StatusCode)
	}
}

func TestSignInRedirectsStayOnThisSite(t *testing.T) {
	provider := newFakeProvider(t)
	client, server, _ := newAuthServer(t, provider)

	login := get(t, client, server.URL+"/auth/login?redirect=https://attacker.example.test/steal")
	authorize, err := url.Parse(login.Header.Get("Location"))
	if err != nil {
		t.Fatalf("parse authorize url: %v", err)
	}

	callback := get(t, client, server.URL+"/auth/callback?code=good&state="+authorize.Query().Get("state"))
	if location := callback.Header.Get("Location"); location != "/" {
		t.Errorf("redirect: got %q, want /", location)
	}
}

func TestTheDevIdentityIsParsedAndIssued(t *testing.T) {
	dev, err := identity.ParseDevIdentity("dev@example.test:payments-admins, platform-approvers")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if dev.Subject != "dev@example.test" || dev.Email != "dev@example.test" {
		t.Errorf("subject: got %+v", dev)
	}
	if len(dev.Groups) != 2 || dev.Groups[1] != "platform-approvers" {
		t.Errorf("groups: got %v", dev.Groups)
	}
	if !strings.Contains(dev.Warning(), "authentication is disabled") {
		t.Errorf("warning: got %q", dev.Warning())
	}

	sealer := newSealer(t)
	mux := http.NewServeMux()
	dev.WithSealer(sealer, slog.New(slog.DiscardHandler)).Routes(mux)

	server := httptest.NewServer(sealer.Middleware(mux))
	defer server.Close()

	client := newClient(t)

	if login := get(t, client, server.URL+"/auth/login"); login.StatusCode != http.StatusFound {
		t.Fatalf("login status: got %d", login.StatusCode)
	}

	session := sessionFromJar(t, sealer, client, server.URL)
	if session.Subject != "dev@example.test" || session.Mode != identity.AuthModeDev {
		t.Errorf("session: got %+v", session)
	}
}

func TestAnEmptyDevIdentityIsRejected(t *testing.T) {
	if _, err := identity.ParseDevIdentity("  :group"); err == nil {
		t.Fatal("an identity without a subject was accepted")
	}
}

type fakeProvider struct {
	server         *httptest.Server
	key            *rsa.PrivateKey
	clientID       string
	issuerOverride string
}

func newFakeProvider(t *testing.T) *fakeProvider {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate provider key: %v", err)
	}

	provider := &fakeProvider{key: key, clientID: "margov"}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{
			"issuer":                                provider.server.URL,
			"authorization_endpoint":                provider.server.URL + "/authorize",
			"token_endpoint":                        provider.server.URL + "/token",
			"jwks_uri":                              provider.server.URL + "/keys",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})

	mux.HandleFunc("GET /keys", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
			Key:       key.Public(),
			KeyID:     "test",
			Algorithm: string(jose.RS256),
			Use:       "sig",
		}}})
	})

	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse token request: %v", err)
		}
		if r.Form.Get("code_verifier") == "" {
			t.Error("the token request carries no pkce verifier")
		}

		writeJSON(t, w, map[string]any{
			"access_token": "access",
			"token_type":   "Bearer",
			"expires_in":   3600,
			"id_token":     provider.idToken(t),
		})
	})

	provider.server = httptest.NewServer(mux)
	t.Cleanup(provider.server.Close)

	return provider
}

func (p *fakeProvider) issuer() string {
	if p.issuerOverride != "" {
		return p.issuerOverride
	}

	return p.server.URL
}

func (p *fakeProvider) idToken(t *testing.T) string {
	t.Helper()

	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: p.key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "test"),
	)
	if err != nil {
		t.Fatalf("build signer: %v", err)
	}

	now := time.Now()
	claims := map[string]any{
		"iss":    p.issuer(),
		"sub":    "u-42",
		"aud":    p.clientID,
		"exp":    now.Add(time.Hour).Unix(),
		"iat":    now.Unix(),
		"email":  "dev@example.test",
		"groups": []string{"payments-admins", "platform-approvers"},
	}

	token, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatalf("sign id token: %v", err)
	}

	return token
}

func newAuthServer(t *testing.T, provider *fakeProvider) (*http.Client, *httptest.Server, *identity.Sealer) {
	t.Helper()

	sealer := newSealerInsecure(t)
	mux := http.NewServeMux()
	server := httptest.NewServer(sealer.Middleware(mux))
	t.Cleanup(server.Close)

	authenticator, err := identity.NewAuthenticator(t.Context(), identity.OIDCConfig{
		IssuerURL:   provider.server.URL,
		ClientID:    provider.clientID,
		RedirectURL: server.URL + "/auth/callback",
	}, sealer, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("build authenticator: %v", err)
	}

	authenticator.Routes(mux)

	return newClient(t), server, sealer
}

func newClient(t *testing.T) *http.Client {
	t.Helper()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("build cookie jar: %v", err)
	}

	return &http.Client{
		Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func newSealerInsecure(t *testing.T) *identity.Sealer {
	t.Helper()

	key, err := identity.NewKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	sealer, err := identity.NewSealer(key, false)
	if err != nil {
		t.Fatalf("build sealer: %v", err)
	}

	return sealer
}

func get(t *testing.T, client *http.Client, target string) *http.Response {
	t.Helper()

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}

	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("request %s: %v", target, err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })

	return response
}

func readBody(t *testing.T, response *http.Response) string {
	t.Helper()

	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	return string(raw)
}

func sessionFromJar(t *testing.T, sealer *identity.Sealer, client *http.Client, target string) identity.Session {
	t.Helper()

	parsed, err := url.Parse(target)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}

	for _, cookie := range client.Jar.Cookies(parsed) {
		if cookie.Name != identity.CookieName {
			continue
		}

		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.AddCookie(cookie)

		session, err := sealer.Read(request, time.Now())
		if err != nil {
			t.Fatalf("read session: %v", err)
		}

		return session
	}

	t.Fatal("no session cookie was set")

	return identity.Session{}
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Errorf("write json: %v", err)
	}
}
