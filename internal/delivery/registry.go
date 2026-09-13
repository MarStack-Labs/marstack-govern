package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

type RegistryConfig struct {
	Host      string
	Namespace string
	Token     string
	Insecure  bool
	Client    *http.Client
}

type Registry struct {
	host      string
	namespace string
	token     string
	insecure  bool
	client    *http.Client
}

func NewRegistry(cfg RegistryConfig) *Registry {
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}

	return &Registry{
		host:      strings.TrimRight(cfg.Host, "/"),
		namespace: strings.Trim(cfg.Namespace, "/"),
		token:     cfg.Token,
		insecure:  cfg.Insecure,
		client:    client,
	}
}

func (r *Registry) Available() bool {
	return r != nil && r.host != ""
}

func (r *Registry) Charts(ctx context.Context) ([]Chart, error) {
	if !r.Available() {
		return nil, ErrNoCatalogue
	}

	var catalogue struct {
		Repositories []string `json:"repositories"`
	}

	if err := r.get(ctx, "/v2/_catalog?n=200", "application/json", &catalogue); err != nil {
		return nil, err
	}

	charts := []Chart{}

	for _, repository := range catalogue.Repositories {
		if r.namespace != "" && !strings.HasPrefix(repository, r.namespace+"/") {
			continue
		}

		chart, err := r.chart(ctx, repository)
		if err != nil {
			continue
		}

		charts = append(charts, chart)
	}

	return charts, nil
}

func (r *Registry) chart(ctx context.Context, repository string) (Chart, error) {
	var tags struct {
		Tags []string `json:"tags"`
	}

	if err := r.get(ctx, "/v2/"+repository+"/tags/list", "application/json", &tags); err != nil {
		return Chart{}, err
	}

	version := newest(tags.Tags)
	if version == "" {
		return Chart{}, fmt.Errorf("%s has no usable tag", repository)
	}

	var manifest struct {
		Annotations map[string]string `json:"annotations"`
		Config      struct {
			Digest string `json:"digest"`
		} `json:"config"`
	}

	accept := strings.Join([]string{
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.docker.distribution.manifest.v2+json",
	}, ", ")

	if err := r.get(ctx, "/v2/"+repository+"/manifests/"+version, accept, &manifest); err != nil {
		return Chart{}, err
	}

	name := repository
	if slash := strings.LastIndex(name, "/"); slash >= 0 {
		name = name[slash+1:]
	}

	chart := Chart{
		Name:        name,
		Version:     version,
		Description: manifest.Annotations["org.opencontainers.image.description"],
		Reference:   r.host + "/" + repository + ":" + version,
		Values:      map[string]string{},
	}

	if manifest.Config.Digest != "" {
		chart.Values = r.values(ctx, repository, manifest.Config.Digest)
	}

	return chart, nil
}

func (r *Registry) values(ctx context.Context, repository, digest string) map[string]string {
	var config struct {
		Name        string            `json:"name"`
		Description string            `json:"description"`
		Annotations map[string]string `json:"annotations"`
	}

	if err := r.get(ctx, "/v2/"+repository+"/blobs/"+digest, "application/json", &config); err != nil {
		return map[string]string{}
	}

	values := map[string]string{}
	for key, value := range config.Annotations {
		name, found := strings.CutPrefix(key, "io.marstack.govern.value.")
		if !found {
			continue
		}

		values[name] = value
	}

	return values
}

func (r *Registry) get(ctx context.Context, endpoint, accept string, into any) error {
	scheme := "https"
	if r.insecure || strings.HasPrefix(r.host, "localhost") || strings.HasPrefix(r.host, "127.0.0.1") {
		scheme = "http"
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, scheme+"://"+r.host+endpoint, nil)
	if err != nil {
		return fmt.Errorf("build registry request: %w", err)
	}

	request.Header.Set("Accept", accept)
	if r.token != "" {
		request.Header.Set("Authorization", "Bearer "+r.token)
	}

	response, err := r.client.Do(request)
	if err != nil {
		return fmt.Errorf("call the registry: %w", err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("the registry answered %s for %s", response.Status, endpoint)
	}

	if err := json.NewDecoder(response.Body).Decode(into); err != nil {
		return fmt.Errorf("decode %s: %w", endpoint, err)
	}

	return nil
}

func newest(tags []string) string {
	usable := make([]string, 0, len(tags))
	for _, tag := range tags {
		if strings.HasSuffix(tag, ".sig") || strings.HasSuffix(tag, ".att") {
			continue
		}
		usable = append(usable, tag)
	}

	if len(usable) == 0 {
		return ""
	}

	sort.SliceStable(usable, func(i, j int) bool {
		return compareVersions(usable[i], usable[j]) > 0
	})

	return usable[0]
}

func compareVersions(left, right string) int {
	leftParts := strings.Split(strings.TrimPrefix(left, "v"), ".")
	rightParts := strings.Split(strings.TrimPrefix(right, "v"), ".")

	for i := 0; i < len(leftParts) && i < len(rightParts); i++ {
		a, aok := number(leftParts[i])
		b, bok := number(rightParts[i])

		if !aok || !bok {
			return strings.Compare(left, right)
		}
		if a != b {
			if a > b {
				return 1
			}

			return -1
		}
	}

	return len(leftParts) - len(rightParts)
}

func number(value string) (int, bool) {
	total := 0
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, false
		}
		total = total*10 + int(r-'0')
	}

	return total, value != ""
}
