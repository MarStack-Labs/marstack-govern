package identity

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const (
	flowCookieName = "margov_auth"
	flowLifetime   = 10 * time.Minute
	sessionTTL     = 8 * time.Hour
)

type OIDCConfig struct {
	IssuerURL     string
	ClientID      string
	ClientSecret  string
	RedirectURL   string
	Scopes        []string
	GroupsClaim   string
	UsernameClaim string
}

type Authenticator struct {
	verifier      *oidc.IDTokenVerifier
	oauth         oauth2.Config
	sealer        *Sealer
	groupsClaim   string
	usernameClaim string
	logger        *slog.Logger
}

type flowState struct {
	State     string    `json:"state"`
	Verifier  string    `json:"verifier"`
	Redirect  string    `json:"redirect"`
	ExpiresAt time.Time `json:"exp"`
}

func NewAuthenticator(ctx context.Context, cfg OIDCConfig, sealer *Sealer, logger *slog.Logger) (*Authenticator, error) {
	if cfg.IssuerURL == "" || cfg.ClientID == "" || cfg.RedirectURL == "" {
		return nil, fmt.Errorf("oidc needs an issuer, a client id and a redirect url")
	}

	provider, err := oidc.NewProvider(ctx, cfg.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("discover the oidc provider at %s: %w", cfg.IssuerURL, err)
	}

	scopes := cfg.Scopes
	if len(scopes) == 0 {
		scopes = []string{oidc.ScopeOpenID, "profile", "email", "groups"}
	}

	groupsClaim := cfg.GroupsClaim
	if groupsClaim == "" {
		groupsClaim = "groups"
	}

	usernameClaim := cfg.UsernameClaim
	if usernameClaim == "" {
		usernameClaim = "email"
	}

	if logger == nil {
		logger = slog.Default()
	}

	return &Authenticator{
		verifier: provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
		oauth: oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			Endpoint:     provider.Endpoint(),
			RedirectURL:  cfg.RedirectURL,
			Scopes:       scopes,
		},
		sealer:        sealer,
		groupsClaim:   groupsClaim,
		usernameClaim: usernameClaim,
		logger:        logger,
	}, nil
}

func (a *Authenticator) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /auth/login", a.login)
	mux.HandleFunc("GET /auth/callback", a.callback)
	mux.HandleFunc("POST /auth/logout", a.logout)
}

func (a *Authenticator) login(w http.ResponseWriter, r *http.Request) {
	state, err := randomToken()
	if err != nil {
		http.Error(w, "cannot start sign in", http.StatusInternalServerError)
		return
	}

	verifier := oauth2.GenerateVerifier()

	flow := flowState{
		State:     state,
		Verifier:  verifier,
		Redirect:  safeRedirect(r.URL.Query().Get("redirect")),
		ExpiresAt: time.Now().Add(flowLifetime),
	}

	sealed, err := a.sealer.SealJSON(flow)
	if err != nil {
		http.Error(w, "cannot start sign in", http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     flowCookieName,
		Value:    sealed,
		Path:     "/auth",
		Expires:  flow.ExpiresAt,
		HttpOnly: true,
		Secure:   a.sealer.secure,
		SameSite: http.SameSiteLaxMode,
	})

	http.Redirect(w, r, a.oauth.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier)), http.StatusFound)
}

func (a *Authenticator) callback(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(flowCookieName)
	if err != nil {
		http.Error(w, "sign in did not start here", http.StatusBadRequest)
		return
	}

	var flow flowState
	if err := a.sealer.OpenJSON(cookie.Value, &flow); err != nil {
		http.Error(w, "sign in did not start here", http.StatusBadRequest)
		return
	}

	a.clearFlow(w)

	if time.Now().After(flow.ExpiresAt) {
		http.Error(w, "sign in took too long, try again", http.StatusBadRequest)
		return
	}

	if r.URL.Query().Get("state") != flow.State {
		http.Error(w, "the sign in state does not match", http.StatusBadRequest)
		return
	}

	if failure := r.URL.Query().Get("error"); failure != "" {
		http.Error(w, "the identity provider refused: "+failure, http.StatusUnauthorized)
		return
	}

	token, err := a.oauth.Exchange(r.Context(), r.URL.Query().Get("code"), oauth2.VerifierOption(flow.Verifier))
	if err != nil {
		a.logger.Warn("exchange authorization code", "error", err)
		http.Error(w, "the identity provider refused the code", http.StatusUnauthorized)
		return
	}

	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		http.Error(w, "the identity provider returned no id token", http.StatusUnauthorized)
		return
	}

	idToken, err := a.verifier.Verify(r.Context(), rawIDToken)
	if err != nil {
		a.logger.Warn("verify id token", "error", err)
		http.Error(w, "the id token does not verify", http.StatusUnauthorized)
		return
	}

	session, err := a.sessionFrom(idToken)
	if err != nil {
		a.logger.Warn("read id token claims", "error", err)
		http.Error(w, "the id token is missing the claims we need", http.StatusUnauthorized)
		return
	}

	if err := a.sealer.Write(w, session); err != nil {
		http.Error(w, "cannot start the session", http.StatusInternalServerError)
		return
	}

	a.logger.Info("signed in", "subject", session.Subject, "groups", len(session.Groups))

	http.Redirect(w, r, flow.Redirect, http.StatusFound)
}

func (a *Authenticator) logout(w http.ResponseWriter, _ *http.Request) {
	a.sealer.Clear(w)
	w.WriteHeader(http.StatusNoContent)
}

func (a *Authenticator) clearFlow(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     flowCookieName,
		Value:    "",
		Path:     "/auth",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   a.sealer.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (a *Authenticator) sessionFrom(idToken *oidc.IDToken) (Session, error) {
	claims := map[string]any{}
	if err := idToken.Claims(&claims); err != nil {
		return Session{}, fmt.Errorf("decode claims: %w", err)
	}

	now := time.Now()
	expires := idToken.Expiry
	if expires.IsZero() || expires.After(now.Add(sessionTTL)) {
		expires = now.Add(sessionTTL)
	}

	session := Session{
		Subject:   idToken.Subject,
		Email:     stringClaim(claims, a.usernameClaim),
		Groups:    stringsClaim(claims, a.groupsClaim),
		Mode:      AuthModeOIDC,
		IssuedAt:  now,
		ExpiresAt: expires,
	}

	if session.Subject == "" {
		return Session{}, fmt.Errorf("the id token carries no subject")
	}

	return session, nil
}

func stringClaim(claims map[string]any, name string) string {
	value, ok := claims[name].(string)
	if !ok {
		return ""
	}

	return value
}

func stringsClaim(claims map[string]any, name string) []string {
	switch value := claims[name].(type) {
	case []any:
		groups := make([]string, 0, len(value))
		for _, item := range value {
			if group, ok := item.(string); ok && group != "" {
				groups = append(groups, group)
			}
		}
		return groups
	case []string:
		return value
	case string:
		if value == "" {
			return nil
		}
		return strings.Split(value, ",")
	default:
		return nil
	}
}

func safeRedirect(target string) string {
	if target == "" {
		return "/"
	}

	parsed, err := url.Parse(target)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || !strings.HasPrefix(parsed.Path, "/") {
		return "/"
	}

	return parsed.Path
}

func randomToken() (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(raw), nil
}
