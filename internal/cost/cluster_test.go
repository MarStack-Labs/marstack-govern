package cost_test

import (
	"os"
	"testing"
	"time"

	"github.com/marstack-labs/marstack-govern/internal/cost"
	"github.com/marstack-labs/marstack-govern/internal/metrics"
)

const envMetrics = "GOVERN_TEST_METRICS_URL"

func TestAgainstARealPrometheus(t *testing.T) {
	baseURL := os.Getenv(envMetrics)
	if baseURL == "" {
		t.Skipf("set %s to a Prometheus scraping the cluster to exercise this", envMetrics)
	}

	namespace := os.Getenv("GOVERN_TEST_NAMESPACE")
	if namespace == "" {
		namespace = "payments-dev"
	}

	reader := cost.NewReader(metrics.New(metrics.Config{BaseURL: baseURL}))
	if !reader.Available() {
		t.Fatal("a metrics url was given but the reader reports no source")
	}

	usage, err := reader.Usage(t.Context(), []string{namespace}, time.Hour)
	if err != nil {
		t.Fatalf("read usage: %v", err)
	}

	if !usage.Observed {
		t.Fatalf("Prometheus is scraping %s but no usage came back", namespace)
	}

	if usage.CPUCoreHoursRequested == nil || usage.CPUCoreHoursRequested.Sign() <= 0 {
		t.Errorf("cpu requested: got %v, want the requests these deployments carry",
			usage.CPUCoreHoursRequested)
	}
	if usage.MemoryGiHoursRequested == nil || usage.MemoryGiHoursRequested.Sign() <= 0 {
		t.Errorf("memory requested: got %v", usage.MemoryGiHoursRequested)
	}

	t.Logf("%s over %s: cpu requested=%s used=%s, memory requested=%s used=%s",
		namespace, usage.Window,
		usage.CPUCoreHoursRequested.FloatString(3), floatOrDash(usage.CPUCoreHoursUsed),
		usage.MemoryGiHoursRequested.FloatString(3), floatOrDash(usage.MemoryGiHoursUsed))
}

func TestRightSizingReadsRealPerWorkloadUsage(t *testing.T) {
	baseURL := os.Getenv(envMetrics)
	if baseURL == "" {
		t.Skipf("set %s to a Prometheus scraping the cluster to exercise this", envMetrics)
	}

	namespace := os.Getenv("GOVERN_TEST_NAMESPACE")
	if namespace == "" {
		namespace = "payments-dev"
	}

	reader := cost.NewReader(metrics.New(metrics.Config{BaseURL: baseURL}))

	observed, err := reader.ObservedByWorkload(t.Context(), []string{namespace}, time.Hour)
	if err != nil {
		t.Fatalf("read per-workload usage: %v", err)
	}

	if len(observed) == 0 {
		t.Fatal("no workload usage came back, so no request could ever be questioned")
	}

	for key, workload := range observed {
		if workload.Namespace != namespace {
			t.Errorf("%s reports namespace %s", key, workload.Namespace)
		}
		if workload.Name == "" {
			t.Errorf("%s came back with no workload name", key)
		}
	}

	t.Logf("%d workloads measured in %s", len(observed), namespace)
}

func floatOrDash(value interface{ FloatString(int) string }) string {
	if value == nil {
		return "—"
	}

	return value.FloatString(3)
}
