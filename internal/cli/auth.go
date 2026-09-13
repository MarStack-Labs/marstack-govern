package cli

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/marstack-labs/marstack-govern/internal/identity"
)

type authOptions struct {
	sessionKey    string
	secureCookies bool
	devIdentity   string
	issuer        string
	clientID      string
	clientSecret  string
	redirectURL   string
	groupsClaim   string
	usernameClaim string
}

func buildAuth(ctx context.Context, opts authOptions, logger *slog.Logger) (*identity.Sealer, func(*http.ServeMux), error) {
	key, err := sessionKey(opts.sessionKey, logger)
	if err != nil {
		return nil, nil, err
	}

	sealer, err := identity.NewSealer(key, opts.secureCookies)
	if err != nil {
		return nil, nil, err
	}

	if opts.devIdentity != "" {
		dev, err := identity.ParseDevIdentity(opts.devIdentity)
		if err != nil {
			return nil, nil, err
		}

		bound := dev.WithSealer(sealer, logger)
		logger.Warn(bound.Warning())

		return sealer, bound.Routes, nil
	}

	if opts.issuer == "" {
		return nil, nil, errors.New(
			"no authentication configured: pass --oidc-issuer with its client settings, " +
				"or --insecure-dev-identity for a local run")
	}

	secret := opts.clientSecret
	if secret == "" {
		secret = os.Getenv("GOVERN_OIDC_CLIENT_SECRET")
	}

	authenticator, err := identity.NewAuthenticator(ctx, identity.OIDCConfig{
		IssuerURL:     opts.issuer,
		ClientID:      opts.clientID,
		ClientSecret:  secret,
		RedirectURL:   opts.redirectURL,
		GroupsClaim:   opts.groupsClaim,
		UsernameClaim: opts.usernameClaim,
	}, sealer, logger)
	if err != nil {
		return nil, nil, err
	}

	return sealer, authenticator.Routes, nil
}

func sessionKey(configured string, logger *slog.Logger) ([]byte, error) {
	if configured == "" {
		configured = os.Getenv("GOVERN_SESSION_KEY")
	}

	if configured == "" {
		key, err := identity.NewKey()
		if err != nil {
			return nil, err
		}

		logger.Warn("no session key configured: generated a temporary one, so every restart signs everybody out")

		return key, nil
	}

	if decoded, err := hex.DecodeString(configured); err == nil && len(decoded) == identity.KeyLength {
		return decoded, nil
	}

	decoded, err := base64.StdEncoding.DecodeString(configured)
	if err != nil || len(decoded) != identity.KeyLength {
		return nil, fmt.Errorf("a session key must be %d bytes, hex or base64 encoded", identity.KeyLength)
	}

	return decoded, nil
}
