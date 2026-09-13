package topology_test

import (
	"errors"
	"os"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/marstack-labs/marstack-govern/internal/kube"
	"github.com/marstack-labs/marstack-govern/internal/metrics"
	"github.com/marstack-labs/marstack-govern/internal/tenancy"
	"github.com/marstack-labs/marstack-govern/internal/topology"
)

const envContext = "GOVERN_TEST_KUBE_CONTEXT"

func TestReachabilityAgainstARealCluster(t *testing.T) {
	kubeContext := os.Getenv(envContext)
	if kubeContext == "" {
		t.Skipf("set %s to a cluster with network policies to exercise this", envContext)
	}

	namespace := os.Getenv("GOVERN_TEST_NAMESPACE")
	if namespace == "" {
		namespace = "payments-dev"
	}

	scheme, err := tenancy.NewScheme()
	if err != nil {
		t.Fatalf("scheme: %v", err)
	}

	_, restConfig, err := kube.NewClient(kube.ClientConfig{Context: kubeContext})
	if err != nil {
		t.Fatalf("connect to %s: %v", kubeContext, err)
	}

	api, err := client.New(restConfig, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}

	paths, err := topology.NewReachability(api).Paths(t.Context(), []string{namespace}, nil)
	if err != nil {
		t.Fatalf("paths: %v", err)
	}

	if len(paths) == 0 {
		t.Fatalf("the cluster has namespaces but no path to %s was evaluated", namespace)
	}

	denied, allowed := 0, 0
	for _, path := range paths {
		if path.To != namespace {
			t.Errorf("a path targets %s, not %s", path.To, namespace)
		}
		if path.From == namespace {
			t.Error("a namespace was evaluated against itself")
		}

		if path.Allowed {
			allowed++
		} else {
			denied++
		}
	}

	if denied == 0 {
		t.Errorf("the controller installed default-deny in %s but every path reads as open", namespace)
	}

	t.Logf("%d paths into %s: %d denied, %d allowed", len(paths), namespace, denied, allowed)
}

func TestWithoutTraceMetricsNoGraphIsInvented(t *testing.T) {
	baseURL := os.Getenv("GOVERN_TEST_METRICS_URL")
	if baseURL == "" {
		t.Skip("set GOVERN_TEST_METRICS_URL to a Prometheus without a trace pipeline")
	}

	graph := topology.NewGraph(metrics.New(metrics.Config{BaseURL: baseURL}), time.Hour)

	if !graph.Available() {
		t.Fatal("a metrics url was given but the graph reports no source")
	}

	_, err := graph.Edges(t.Context(), []string{"payments-dev"})
	if !errors.Is(err, topology.ErrNoTraces) {
		t.Fatalf("got %v, want a refusal to draw a graph without traces", err)
	}
}
