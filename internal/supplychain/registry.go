package supplychain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	manifestAccept = "application/vnd.oci.image.manifest.v1+json," +
		"application/vnd.docker.distribution.manifest.v2+json," +
		"application/vnd.oci.image.index.v1+json," +
		"application/vnd.docker.distribution.manifest.list.v2+json"

	sourceLabel   = "org.opencontainers.image.source"
	revisionLabel = "org.opencontainers.image.revision"
	createdLabel  = "org.opencontainers.image.created"
)

var (
	ErrNeedsCredentials = errors.New("the registry refused an anonymous read")
	ErrNotFound         = errors.New("the registry does not have that image")
)

type Registry struct {
	http     *http.Client
	token    string
	insecure bool
}

type RegistryConfig struct {
	Token    string
	Insecure bool
	Timeout  time.Duration
}

func NewRegistry(cfg RegistryConfig) *Registry {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}

	return &Registry{
		http:     &http.Client{Timeout: timeout},
		token:    cfg.Token,
		insecure: cfg.Insecure,
	}
}

type ImageFacts struct {
	Digest       string
	Source       string
	Revision     string
	Created      time.Time
	Signed       bool
	BaseImageAge time.Duration
}

func (r *Registry) Inspect(ctx context.Context, reference Reference) (ImageFacts, error) {
	digest, manifest, err := r.manifest(ctx, reference, reference.Tag, reference.Digest)
	if err != nil {
		return ImageFacts{}, err
	}

	facts := ImageFacts{Digest: digest}

	if configDigest := manifest.Config.Digest; configDigest != "" {
		labels, created, err := r.config(ctx, reference, configDigest)
		if err == nil {
			facts.Source = labels[sourceLabel]
			facts.Revision = labels[revisionLabel]

			if stamp := labels[createdLabel]; stamp != "" {
				if parsed, parseErr := time.Parse(time.RFC3339, stamp); parseErr == nil {
					created = parsed
				}
			}

			facts.Created = created
			if !created.IsZero() {
				facts.BaseImageAge = time.Since(created)
			}
		}
	}

	facts.Signed = r.signed(ctx, reference, digest)

	return facts, nil
}

type manifestBody struct {
	Config struct {
		Digest string `json:"digest"`
	} `json:"config"`
	Manifests []struct {
		Digest string `json:"digest"`
	} `json:"manifests"`
}

func (r *Registry) manifest(ctx context.Context, reference Reference, tag, digest string) (string, manifestBody, error) {
	target := digest
	if target == "" {
		target = tag
	}

	endpoint := fmt.Sprintf("%s/v2/%s/manifests/%s", r.base(reference.Registry), reference.Repository, target)

	response, err := r.get(ctx, endpoint, manifestAccept)
	if err != nil {
		return "", manifestBody{}, err
	}
	defer func() { _ = response.Body.Close() }()

	resolved := response.Header.Get("Docker-Content-Digest")
	if resolved == "" {
		resolved = digest
	}

	var body manifestBody
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		return resolved, manifestBody{}, fmt.Errorf("read the manifest of %s: %w", reference, err)
	}

	if body.Config.Digest == "" && len(body.Manifests) > 0 {
		return r.manifest(ctx, reference, "", body.Manifests[0].Digest)
	}

	return resolved, body, nil
}

func (r *Registry) config(ctx context.Context, reference Reference, digest string) (map[string]string, time.Time, error) {
	endpoint := fmt.Sprintf("%s/v2/%s/blobs/%s", r.base(reference.Registry), reference.Repository, digest)

	response, err := r.get(ctx, endpoint, "application/json")
	if err != nil {
		return nil, time.Time{}, err
	}
	defer func() { _ = response.Body.Close() }()

	var body struct {
		Created string `json:"created"`
		Config  struct {
			Labels map[string]string `json:"Labels"`
		} `json:"config"`
	}

	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		return nil, time.Time{}, fmt.Errorf("read the image config of %s: %w", reference, err)
	}

	created, _ := time.Parse(time.RFC3339, body.Created)

	labels := body.Config.Labels
	if labels == nil {
		labels = map[string]string{}
	}

	return labels, created, nil
}

func (r *Registry) signed(ctx context.Context, reference Reference, digest string) bool {
	if digest == "" {
		return false
	}

	tag := strings.Replace(digest, ":", "-", 1) + ".sig"
	endpoint := fmt.Sprintf("%s/v2/%s/manifests/%s", r.base(reference.Registry), reference.Repository, tag)

	response, err := r.get(ctx, endpoint, manifestAccept)
	if err != nil {
		return false
	}
	defer func() { _ = response.Body.Close() }()

	return true
}

func (r *Registry) get(ctx context.Context, endpoint, accept string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build registry request: %w", err)
	}

	request.Header.Set("Accept", accept)
	if r.token != "" {
		request.Header.Set("Authorization", "Bearer "+r.token)
	}

	response, err := r.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("call the registry: %w", err)
	}

	switch response.StatusCode {
	case http.StatusOK:
		return response, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		_ = response.Body.Close()
		return nil, ErrNeedsCredentials
	case http.StatusNotFound:
		_ = response.Body.Close()
		return nil, ErrNotFound
	default:
		_ = response.Body.Close()
		return nil, fmt.Errorf("the registry answered %s", response.Status)
	}
}

func (r *Registry) base(registry string) string {
	scheme := "https"
	if r.insecure || strings.HasPrefix(registry, "localhost") || strings.HasPrefix(registry, "127.0.0.1") {
		scheme = "http"
	}

	return scheme + "://" + registry
}
