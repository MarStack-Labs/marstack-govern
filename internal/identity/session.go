package identity

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

const (
	CookieName  = "margov_session"
	KeyLength   = 32
	maxLifetime = 12 * time.Hour
)

var (
	ErrNoSession      = errors.New("no session")
	ErrSessionExpired = errors.New("the session has expired")
	ErrTampered       = errors.New("the session cookie does not verify")
)

type AuthMode string

const (
	AuthModeOIDC AuthMode = "oidc"
	AuthModeDev  AuthMode = "dev"
)

type Session struct {
	Subject        string    `json:"sub"`
	Email          string    `json:"email,omitempty"`
	Groups         []string  `json:"groups,omitempty"`
	ActiveDivision string    `json:"division,omitempty"`
	Mode           AuthMode  `json:"mode"`
	IssuedAt       time.Time `json:"iat"`
	ExpiresAt      time.Time `json:"exp"`
}

func (s Session) Valid(now time.Time) error {
	if s.Subject == "" {
		return ErrNoSession
	}
	if now.After(s.ExpiresAt) {
		return ErrSessionExpired
	}

	return nil
}

type Sealer struct {
	aead   cipher.AEAD
	secure bool
}

func NewSealer(key []byte, secureCookies bool) (*Sealer, error) {
	if len(key) != KeyLength {
		return nil, fmt.Errorf("a session key must be %d bytes, got %d", KeyLength, len(key))
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("build session cipher: %w", err)
	}

	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("build session aead: %w", err)
	}

	return &Sealer{aead: aead, secure: secureCookies}, nil
}

func NewKey() ([]byte, error) {
	key := make([]byte, KeyLength)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate session key: %w", err)
	}

	return key, nil
}

func (s *Sealer) Seal(session Session) (string, error) {
	return s.SealJSON(session)
}

func (s *Sealer) Open(value string) (Session, error) {
	var session Session
	if err := s.OpenJSON(value, &session); err != nil {
		return Session{}, err
	}

	return session, nil
}

func (s *Sealer) SealJSON(value any) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode sealed value: %w", err)
	}

	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate session nonce: %w", err)
	}

	sealed := s.aead.Seal(nonce, nonce, payload, nil)

	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

func (s *Sealer) OpenJSON(value string, target any) error {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return ErrTampered
	}

	if len(raw) < s.aead.NonceSize() {
		return ErrTampered
	}

	nonce, ciphertext := raw[:s.aead.NonceSize()], raw[s.aead.NonceSize():]

	payload, err := s.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return ErrTampered
	}

	if err := json.Unmarshal(payload, target); err != nil {
		return ErrTampered
	}

	return nil
}

func (s *Sealer) CookieFor(session Session) (*http.Cookie, error) {
	if session.ExpiresAt.IsZero() || session.ExpiresAt.Sub(session.IssuedAt) > maxLifetime {
		session.ExpiresAt = session.IssuedAt.Add(maxLifetime)
	}

	value, err := s.Seal(session)
	if err != nil {
		return nil, err
	}

	return &http.Cookie{
		Name:     CookieName,
		Value:    value,
		Path:     "/",
		Expires:  session.ExpiresAt,
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteLaxMode,
	}, nil
}

func (s *Sealer) Write(w http.ResponseWriter, session Session) error {
	cookie, err := s.CookieFor(session)
	if err != nil {
		return err
	}

	http.SetCookie(w, cookie)

	return nil
}

func (s *Sealer) Clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Sealer) Read(r *http.Request, now time.Time) (Session, error) {
	cookie, err := r.Cookie(CookieName)
	if err != nil {
		return Session{}, ErrNoSession
	}

	session, err := s.Open(cookie.Value)
	if err != nil {
		return Session{}, err
	}

	if err := session.Valid(now); err != nil {
		return Session{}, err
	}

	return session, nil
}
