package identity

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

type DevIdentity struct {
	Subject string
	Email   string
	Groups  []string
	sealer  *Sealer
	logger  *slog.Logger
}

func ParseDevIdentity(spec string) (DevIdentity, error) {
	subject, rest, _ := strings.Cut(spec, ":")
	subject = strings.TrimSpace(subject)

	if subject == "" {
		return DevIdentity{}, errors.New(`a dev identity looks like "someone@example.test:group-a,group-b"`)
	}

	groups := []string{}
	for _, group := range strings.Split(rest, ",") {
		if trimmed := strings.TrimSpace(group); trimmed != "" {
			groups = append(groups, trimmed)
		}
	}

	identity := DevIdentity{Subject: subject, Groups: groups}
	if strings.Contains(subject, "@") {
		identity.Email = subject
	}

	return identity, nil
}

func (d DevIdentity) WithSealer(sealer *Sealer, logger *slog.Logger) DevIdentity {
	if logger == nil {
		logger = slog.Default()
	}

	d.sealer = sealer
	d.logger = logger

	return d
}

func (d DevIdentity) Warning() string {
	return fmt.Sprintf(
		"authentication is disabled: every visitor is signed in as %s with groups %v",
		d.Subject, d.Groups,
	)
}

func (d DevIdentity) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /auth/login", d.login)
	mux.HandleFunc("POST /auth/logout", d.logout)
}

func (d DevIdentity) login(w http.ResponseWriter, r *http.Request) {
	now := time.Now()

	session := Session{
		Subject:   d.Subject,
		Email:     d.Email,
		Groups:    d.Groups,
		Mode:      AuthModeDev,
		IssuedAt:  now,
		ExpiresAt: now.Add(sessionTTL),
	}

	if err := d.sealer.Write(w, session); err != nil {
		http.Error(w, "cannot start the session", http.StatusInternalServerError)
		return
	}

	d.logger.Warn("issued a development session", "subject", d.Subject)

	http.Redirect(w, r, "/", http.StatusFound)
}

func (d DevIdentity) logout(w http.ResponseWriter, _ *http.Request) {
	d.sealer.Clear(w)
	w.WriteHeader(http.StatusNoContent)
}
