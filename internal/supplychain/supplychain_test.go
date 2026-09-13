package supplychain_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/marstack-labs/marstack-govern/internal/supplychain"
)

func TestReferencesAreParsedTheWayKubernetesWritesThem(t *testing.T) {
	tests := []struct {
		image      string
		registry   string
		repository string
		tag        string
		digest     string
	}{
		{"nginx", "index.docker.io", "library/nginx", "latest", ""},
		{"nginx:alpine", "index.docker.io", "library/nginx", "alpine", ""},
		{"bitnami/redis:7.2", "index.docker.io", "bitnami/redis", "7.2", ""},
		{"registry.internal/payments/api:1.4.2", "registry.internal", "payments/api", "1.4.2", ""},
		{
			"registry.internal:5000/payments/api@sha256:abc",
			"registry.internal:5000", "payments/api", "", "sha256:abc",
		},
		{"ghcr.io/marstack-labs/margov:v1", "ghcr.io", "marstack-labs/margov", "v1", ""},
	}

	for _, test := range tests {
		t.Run(test.image, func(t *testing.T) {
			reference, err := supplychain.ParseReference(test.image)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}

			if reference.Registry != test.registry {
				t.Errorf("registry: got %q, want %q", reference.Registry, test.registry)
			}
			if reference.Repository != test.repository {
				t.Errorf("repository: got %q, want %q", reference.Repository, test.repository)
			}
			if reference.Tag != test.tag {
				t.Errorf("tag: got %q, want %q", reference.Tag, test.tag)
			}
			if reference.Digest != test.digest {
				t.Errorf("digest: got %q, want %q", reference.Digest, test.digest)
			}
		})
	}
}

func TestProvenanceComesFromTheRegistry(t *testing.T) {
	registry := newRegistry(t, registryState{
		digest:   "sha256:deadbeef",
		source:   "https://github.com/marstack-labs/marstack-govern",
		revision: "1c2186e",
		created:  time.Now().Add(-30 * 24 * time.Hour),
		signed:   true,
	})

	provenance := supplychain.NewProvenance(registry.client, nil)

	facts, err := provenance.Of(t.Context(), "payments-dev", registry.host+"/payments/api:1.4.2")
	if err != nil {
		t.Fatalf("provenance: %v", err)
	}

	if facts.GetImageDigest() != "sha256:deadbeef" {
		t.Errorf("digest: got %q", facts.GetImageDigest())
	}
	if facts.GetSourceRepo() != "https://github.com/marstack-labs/marstack-govern" {
		t.Errorf("source: got %q", facts.GetSourceRepo())
	}
	if facts.GetSourceRevision() != "1c2186e" {
		t.Errorf("revision: got %q", facts.GetSourceRevision())
	}
	if !facts.GetSigned() {
		t.Error("a signed image is reported as unsigned")
	}
	if facts.GetBaseImageAgeDays() < 29 || facts.GetBaseImageAgeDays() > 31 {
		t.Errorf("base image age: got %d days, want about 30", facts.GetBaseImageAgeDays())
	}
	if facts.GetTagMutable() {
		t.Error("a version tag was reported as mutable")
	}
}

func TestAnUnsignedImageIsReportedAsUnsigned(t *testing.T) {
	registry := newRegistry(t, registryState{digest: "sha256:cafebabe", signed: false})

	provenance := supplychain.NewProvenance(registry.client, nil)

	facts, err := provenance.Of(t.Context(), "payments-dev", registry.host+"/payments/api:latest")
	if err != nil {
		t.Fatalf("provenance: %v", err)
	}

	if facts.GetSigned() {
		t.Error("an unsigned image passed as signed")
	}
	if !facts.GetTagMutable() {
		t.Error("the latest tag was not reported as mutable")
	}
}

func TestARegistryThatNeedsCredentialsIsSaidSo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	host := strings.TrimPrefix(server.URL, "http://")
	provenance := supplychain.NewProvenance(
		supplychain.NewRegistry(supplychain.RegistryConfig{Insecure: true}), nil)

	facts, err := provenance.Of(t.Context(), "payments-dev", host+"/payments/api:1.0")
	if err != nil {
		t.Fatalf("provenance: %v", err)
	}

	if facts.GetImageDigest() != "" {
		t.Errorf("a digest was invented: %q", facts.GetImageDigest())
	}
	if !strings.Contains(facts.GetSignatureIssuer(), "anonymous") {
		t.Errorf("the reason is not reported: %q", facts.GetSignatureIssuer())
	}
}

func TestVulnerabilitiesComeFromTheScanner(t *testing.T) {
	scanner := supplychain.NewScanner(newCluster(t, true,
		vulnerabilityReport("payments-dev", "sha256:deadbeef", 2, 5, 9, 20),
	))

	found, err := scanner.Vulnerabilities(t.Context(), "payments-dev", "sha256:deadbeef")
	if err != nil {
		t.Fatalf("vulnerabilities: %v", err)
	}

	if !found.Found {
		t.Fatal("a report exists but nothing was found")
	}
	if found.Critical != 2 || found.High != 5 || found.Medium != 9 || found.Low != 20 {
		t.Errorf("counts: got %+v", found)
	}
}

func TestReportsForOtherImagesAreNotCounted(t *testing.T) {
	scanner := supplychain.NewScanner(newCluster(t, true,
		vulnerabilityReport("payments-dev", "sha256:somethingelse", 7, 7, 7, 7),
	))

	found, err := scanner.Vulnerabilities(t.Context(), "payments-dev", "sha256:deadbeef")
	if err != nil {
		t.Fatalf("vulnerabilities: %v", err)
	}

	if found.Found {
		t.Fatalf("another image's report was counted: %+v", found)
	}
}

func TestWithoutAScannerNothingIsClaimed(t *testing.T) {
	scanner := supplychain.NewScanner(newCluster(t, false))

	if scanner.Available() {
		t.Fatal("a cluster without trivy reports a scanner")
	}

	_, err := scanner.Vulnerabilities(t.Context(), "payments-dev", "sha256:deadbeef")
	if err == nil {
		t.Fatal("the absent scanner answered anyway")
	}
}

type registryState struct {
	digest   string
	source   string
	revision string
	created  time.Time
	signed   bool
}

type fakeRegistry struct {
	host   string
	client *supplychain.Registry
}

func newRegistry(t *testing.T, state registryState) fakeRegistry {
	t.Helper()

	mux := http.NewServeMux()

	mux.HandleFunc("GET /v2/{repository...}", func(w http.ResponseWriter, r *http.Request) {
		path := r.PathValue("repository")

		switch {
		case strings.Contains(path, "/manifests/") && strings.HasSuffix(path, ".sig"):
			if !state.signed {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			writeJSON(t, w, map[string]any{"schemaVersion": 2})

		case strings.Contains(path, "/manifests/"):
			w.Header().Set("Docker-Content-Digest", state.digest)
			writeJSON(t, w, map[string]any{
				"schemaVersion": 2,
				"config":        map[string]any{"digest": "sha256:config"},
			})

		case strings.Contains(path, "/blobs/"):
			labels := map[string]string{}
			if state.source != "" {
				labels["org.opencontainers.image.source"] = state.source
			}
			if state.revision != "" {
				labels["org.opencontainers.image.revision"] = state.revision
			}

			body := map[string]any{"config": map[string]any{"Labels": labels}}
			if !state.created.IsZero() {
				body["created"] = state.created.Format(time.RFC3339)
			}

			writeJSON(t, w, body)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return fakeRegistry{
		host:   strings.TrimPrefix(server.URL, "http://"),
		client: supplychain.NewRegistry(supplychain.RegistryConfig{Insecure: true}),
	}
}

func newCluster(t *testing.T, withScanner bool, objects ...client.Object) client.Client {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	mapper := meta.NewDefaultRESTMapper(nil)

	if withScanner {
		for _, gvk := range []schema.GroupVersionKind{
			supplychain.VulnerabilityReportGVK, supplychain.SbomReportGVK,
		} {
			scheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
			list := gvk
			list.Kind += "List"
			scheme.AddKnownTypeWithName(list, &unstructured.UnstructuredList{})
			mapper.Add(gvk, meta.RESTScopeNamespace)
		}
	}

	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithRESTMapper(mapper).
		WithObjects(objects...).
		Build()
}

func vulnerabilityReport(namespace, digest string, critical, high, medium, low int64) *unstructured.Unstructured {
	object := &unstructured.Unstructured{Object: map[string]any{
		"report": map[string]any{
			"artifact":        map[string]any{"digest": digest},
			"updateTimestamp": time.Now().Format(time.RFC3339),
			"summary": map[string]any{
				"criticalCount": critical,
				"highCount":     high,
				"mediumCount":   medium,
				"lowCount":      low,
			},
		},
	}}

	object.SetGroupVersionKind(supplychain.VulnerabilityReportGVK)
	object.SetNamespace(namespace)
	object.SetName(fmt.Sprintf("report-%s", strings.TrimPrefix(digest, "sha256:")))

	return object
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Errorf("write json: %v", err)
	}
}
