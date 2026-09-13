package requests_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/marstack-labs/marstack-govern/internal/metrics"
	"github.com/marstack-labs/marstack-govern/internal/requests"
)

func TestRecommendationComesFromObservedUsage(t *testing.T) {
	source := newFakeMetrics(t, map[string]float64{
		"cpuCurrent": 4.9,
		"cpuP95":     6.2,
		"cpuP99":     6.8,
		"memCurrent": 13.1 * gibibyte,
		"memP95":     14.0 * gibibyte,
		"memP99":     14.5 * gibibyte,
	})

	recommender := requests.NewRecommender(source, 30*24*time.Hour)

	got, err := recommender.Recommend(t.Context(), []string{"payments-dev", "payments-prod"}, requests.Compute{
		CPUMillicores: 8000,
		MemoryBytes:   16 * gibibyte,
	})
	if err != nil {
		t.Fatalf("recommend: %v", err)
	}

	if got.ObservedP95.CPUMillicores != 6200 {
		t.Errorf("p95 cpu: got %d, want 6200", got.ObservedP95.CPUMillicores)
	}
	if got.ObservedP99.CPUMillicores != 6800 {
		t.Errorf("p99 cpu: got %d, want 6800", got.ObservedP99.CPUMillicores)
	}

	if got.Proposed.CPUMillicores != 9000 {
		t.Errorf("proposed cpu: got %dm, want 9000m", got.Proposed.CPUMillicores)
	}
	if got.Proposed.MemoryBytes != 19*gibibyte {
		t.Errorf("proposed memory: got %d bytes, want %d", got.Proposed.MemoryBytes, 19*gibibyte)
	}

	if got.HeadroomPercent != 30 {
		t.Errorf("headroom: got %d", got.HeadroomPercent)
	}
	if !strings.Contains(got.Basis, "p99") {
		t.Errorf("basis does not say what it is based on: %q", got.Basis)
	}
}

func TestAProposalNeverShrinksTheQuota(t *testing.T) {
	source := newFakeMetrics(t, map[string]float64{
		"cpuCurrent": 0.2,
		"cpuP95":     0.3,
		"cpuP99":     0.4,
		"memCurrent": 0.5 * gibibyte,
		"memP95":     0.6 * gibibyte,
		"memP99":     0.7 * gibibyte,
	})

	recommender := requests.NewRecommender(source, 30*24*time.Hour)

	got, err := recommender.Recommend(t.Context(), []string{"payments-dev"}, requests.Compute{
		CPUMillicores: 8000,
		MemoryBytes:   16 * gibibyte,
	})
	if err != nil {
		t.Fatalf("recommend: %v", err)
	}

	if got.Proposed.CPUMillicores != 8000 {
		t.Errorf("proposed cpu: got %d, want the current 8000", got.Proposed.CPUMillicores)
	}
	if got.Proposed.MemoryBytes != 16*gibibyte {
		t.Errorf("proposed memory: got %d, want the current %d", got.Proposed.MemoryBytes, 16*gibibyte)
	}
}

func TestGrowthPredictsWhenTheQuotaRunsOut(t *testing.T) {
	source := newFakeMetrics(t, map[string]float64{
		"cpuCurrent": 4.0,
		"cpuP95":     4.2,
		"cpuP99":     4.4,
		"cpuSlope":   1.0 / 86400,
		"memCurrent": 8 * gibibyte,
		"memP95":     8 * gibibyte,
		"memP99":     8 * gibibyte,
	})

	recommender := requests.NewRecommender(source, 30*24*time.Hour)

	got, err := recommender.Recommend(t.Context(), []string{"payments-dev"}, requests.Compute{
		CPUMillicores: 8000,
		MemoryBytes:   16 * gibibyte,
	})
	if err != nil {
		t.Fatalf("recommend: %v", err)
	}

	if got.ExhaustionAt == nil {
		t.Fatal("a growing division was given no exhaustion date")
	}

	days := time.Until(*got.ExhaustionAt).Hours() / 24
	if days < 3.5 || days > 4.5 {
		t.Errorf("exhaustion in %.1f days, want about 4", days)
	}
}

func TestFlatUsageHasNoExhaustionDate(t *testing.T) {
	source := newFakeMetrics(t, map[string]float64{
		"cpuCurrent": 4.0,
		"cpuP95":     4.0,
		"cpuP99":     4.0,
		"cpuSlope":   0,
		"memCurrent": 8 * gibibyte,
		"memP95":     8 * gibibyte,
		"memP99":     8 * gibibyte,
	})

	recommender := requests.NewRecommender(source, 30*24*time.Hour)

	got, err := recommender.Recommend(t.Context(), []string{"payments-dev"}, requests.Compute{
		CPUMillicores: 8000,
		MemoryBytes:   16 * gibibyte,
	})
	if err != nil {
		t.Fatalf("recommend: %v", err)
	}

	if got.ExhaustionAt != nil {
		t.Errorf("flat usage was given an exhaustion date: %v", got.ExhaustionAt)
	}
}

func TestNoMetricsSourceIsReportedNotGuessed(t *testing.T) {
	recommender := requests.NewRecommender(metrics.New(metrics.Config{}), 0)

	if recommender.Available() {
		t.Fatal("an unconfigured recommender claims to be available")
	}

	_, err := recommender.Recommend(t.Context(), []string{"payments-dev"}, requests.Compute{})
	if !errors.Is(err, metrics.ErrUnavailable) {
		t.Fatalf("got %v, want ErrUnavailable", err)
	}
}

func TestADivisionWithoutSamplesIsReported(t *testing.T) {
	source := newFakeMetrics(t, map[string]float64{})
	recommender := requests.NewRecommender(source, 30*24*time.Hour)

	_, err := recommender.Recommend(t.Context(), []string{"payments-dev"}, requests.Compute{})
	if !errors.Is(err, requests.ErrNoUsage) {
		t.Fatalf("got %v, want ErrNoUsage", err)
	}
}

func TestTheQueryIsScopedToTheDivisionNamespaces(t *testing.T) {
	seen := []string{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Query().Get("query"))
		writeSamples(t, w, nil)
	}))
	defer server.Close()

	recommender := requests.NewRecommender(metrics.New(metrics.Config{BaseURL: server.URL}), 30*24*time.Hour)
	_, _ = recommender.Recommend(t.Context(), []string{"payments-dev", "payments-prod"}, requests.Compute{})

	if len(seen) == 0 {
		t.Fatal("no query was sent")
	}

	for _, query := range seen {
		if !strings.Contains(query, `namespace=~"payments-dev|payments-prod"`) {
			t.Fatalf("a query is not scoped to the division: %s", query)
		}
	}
}

const gibibyte = 1024 * 1024 * 1024

func newFakeMetrics(t *testing.T, values map[string]float64) *metrics.Client {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		value, found := values[classify(query)]

		if !found {
			writeSamples(t, w, nil)
			return
		}

		writeSamples(t, w, &value)
	}))
	t.Cleanup(server.Close)

	return metrics.New(metrics.Config{BaseURL: server.URL, Tenant: "payments"})
}

func classify(query string) string {
	cpu := strings.Contains(query, "container_cpu_usage_seconds_total")

	switch {
	case strings.HasPrefix(query, "deriv("):
		if cpu {
			return "cpuSlope"
		}
		return "memSlope"
	case strings.HasPrefix(query, "quantile_over_time(0.95"):
		if cpu {
			return "cpuP95"
		}
		return "memP95"
	case strings.HasPrefix(query, "quantile_over_time(0.99"):
		if cpu {
			return "cpuP99"
		}
		return "memP99"
	default:
		if cpu {
			return "cpuCurrent"
		}
		return "memCurrent"
	}
}

func writeSamples(t *testing.T, w http.ResponseWriter, value *float64) {
	t.Helper()

	payload := map[string]any{
		"status": "success",
		"data": map[string]any{
			"resultType": "vector",
			"result":     []any{},
		},
	}

	if value != nil {
		payload["data"] = map[string]any{
			"resultType": "vector",
			"result": []any{
				map[string]any{
					"metric": map[string]string{},
					"value":  []any{float64(1), formatFloat(*value)},
				},
			},
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Errorf("write samples: %v", err)
	}
}

func formatFloat(value float64) string {
	return strings.TrimRight(strings.TrimRight(formatted(value), "0"), ".")
}

func formatted(value float64) string {
	return fmt.Sprintf("%.9f", value)
}
