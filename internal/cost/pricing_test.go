package cost_test

import (
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	governv1alpha1 "github.com/marstack-labs/marstack-govern/api/v1alpha1"
	"github.com/marstack-labs/marstack-govern/internal/cost"
	"github.com/marstack-labs/marstack-govern/internal/metrics"
)

func TestAFullMonthOfOneCoreCostsTheMonthlyRate(t *testing.T) {
	policy := policyFrom(t, "150000", "25000", "2026-01-01", "")

	usage := cost.Usage{
		CPUCoreHoursRequested:  big.NewRat(730, 1),
		MemoryGiHoursRequested: new(big.Rat),
		StorageGiHours:         new(big.Rat),
		LoadBalancerHours:      new(big.Rat),
		CPUCoreHoursUsed:       big.NewRat(730, 1),
		MemoryGiHoursUsed:      new(big.Rat),
	}

	bill := cost.Charge(policy, usage, 0, 0)

	if got := bill.Total.String(); got != "150000.00" {
		t.Fatalf("total: got %s, want exactly 150000.00", got)
	}
}

func TestTheArithmeticDoesNotDrift(t *testing.T) {
	policy := policyFrom(t, "150000", "25000", "2026-01-01", "")

	running := cost.NewMoney("IDR", nil)
	for range 730 {
		hour := cost.Usage{
			CPUCoreHoursRequested:  big.NewRat(1, 1),
			MemoryGiHoursRequested: new(big.Rat),
			StorageGiHours:         new(big.Rat),
			LoadBalancerHours:      new(big.Rat),
		}
		running = running.Add(cost.Charge(policy, hour, 0, 0).Total)
	}

	if got := running.String(); got != "150000.00" {
		t.Fatalf("730 separate hours summed to %s, want exactly 150000.00", got)
	}
}

func TestIdleIsWhatWasReservedButNotUsed(t *testing.T) {
	policy := policyFrom(t, "730", "0", "2026-01-01", "")

	usage := cost.Usage{
		CPUCoreHoursRequested:  big.NewRat(100, 1),
		CPUCoreHoursUsed:       big.NewRat(40, 1),
		MemoryGiHoursRequested: new(big.Rat),
		MemoryGiHoursUsed:      new(big.Rat),
		StorageGiHours:         new(big.Rat),
		LoadBalancerHours:      new(big.Rat),
	}

	bill := cost.Charge(policy, usage, 0, 0)

	if got := bill.Total.String(); got != "100.00" {
		t.Errorf("total: got %s, want 100.00 for the reserved capacity", got)
	}
	if got := bill.Idle.String(); got != "60.00" {
		t.Errorf("idle: got %s, want 60.00", got)
	}
}

func TestUsingMoreThanReservedIsNotNegativeIdle(t *testing.T) {
	policy := policyFrom(t, "730", "730", "2026-01-01", "")

	usage := cost.Usage{
		CPUCoreHoursRequested:  big.NewRat(10, 1),
		CPUCoreHoursUsed:       big.NewRat(25, 1),
		MemoryGiHoursRequested: new(big.Rat),
		MemoryGiHoursUsed:      new(big.Rat),
		StorageGiHours:         new(big.Rat),
		LoadBalancerHours:      new(big.Rat),
	}

	bill := cost.Charge(policy, usage, 0, 0)

	if bill.Idle.Negative() {
		t.Fatalf("idle went negative: %s", bill.Idle.String())
	}
	if got := bill.Idle.String(); got != "0.00" {
		t.Errorf("idle: got %s, want 0.00", got)
	}
}

func TestTheMonthEndProjectionScalesWithElapsedTime(t *testing.T) {
	policy := policyFrom(t, "730", "0", "2026-01-01", "")

	usage := cost.Usage{
		CPUCoreHoursRequested:  big.NewRat(100, 1),
		MemoryGiHoursRequested: new(big.Rat),
		StorageGiHours:         new(big.Rat),
		LoadBalancerHours:      new(big.Rat),
	}

	bill := cost.Charge(policy, usage, 240*time.Hour, 720*time.Hour)

	if got := bill.Charged.String(); got != "100.00" {
		t.Errorf("charged so far: got %s, want 100.00", got)
	}
	if got := bill.Projected.String(); got != "300.00" {
		t.Errorf("projected: got %s, want 300.00 after a third of the month", got)
	}
}

func TestTheLatestEffectivePolicyWins(t *testing.T) {
	old := policyFrom(t, "100000", "20000", "2026-01-01", "2026-05-31")
	current := policyFrom(t, "150000", "25000", "2026-06-01", "")

	chosen, err := cost.Effective([]cost.Policy{old, current}, date(t, "2026-09-13"))
	if err != nil {
		t.Fatalf("effective: %v", err)
	}
	if got := chosen.CPUCore.String(); got != "150000.00" {
		t.Errorf("chosen rate: got %s, want the current 150000.00", got)
	}

	earlier, err := cost.Effective([]cost.Policy{old, current}, date(t, "2026-03-01"))
	if err != nil {
		t.Fatalf("effective: %v", err)
	}
	if got := earlier.CPUCore.String(); got != "100000.00" {
		t.Errorf("historical rate: got %s, want 100000.00", got)
	}
}

func TestAPeriodWithoutAPolicyIsRefusedNotGuessed(t *testing.T) {
	current := policyFrom(t, "150000", "25000", "2026-06-01", "")

	_, err := cost.Effective([]cost.Policy{current}, date(t, "2026-01-15"))
	if !errors.Is(err, cost.ErrNoPricingPolicy) {
		t.Fatalf("got %v, want ErrNoPricingPolicy", err)
	}
}

func TestUsageIsReadPerNamespaceAndConvertedToGibibytes(t *testing.T) {
	seen := []string{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		seen = append(seen, query)

		value := 0.0
		switch {
		case strings.Contains(query, "kube_pod_container_resource_requests") && strings.Contains(query, `resource="memory"`):
			value = 4 * 1024 * 1024 * 1024
		case strings.Contains(query, "kube_pod_container_resource_requests"):
			value = 12
		}

		writeScalar(t, w, value)
	}))
	defer server.Close()

	reader := cost.NewReader(metrics.New(metrics.Config{BaseURL: server.URL}))

	usage, err := reader.Usage(t.Context(), []string{"payments-dev", "payments-prod"}, 720*time.Hour)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}

	if got := usage.CPUCoreHoursRequested.FloatString(0); got != "12" {
		t.Errorf("cpu core-hours: got %s, want 12", got)
	}
	if got := usage.MemoryGiHoursRequested.FloatString(0); got != "4" {
		t.Errorf("memory GiB-hours: got %s, want 4", got)
	}
	if !usage.Observed {
		t.Error("usage was read but not reported as observed")
	}

	for _, query := range seen {
		if !strings.Contains(query, `namespace=~"payments-dev|payments-prod"`) {
			t.Fatalf("a query escaped the division: %s", query)
		}
	}
}

func TestWithoutMetricsNothingIsCharged(t *testing.T) {
	reader := cost.NewReader(metrics.New(metrics.Config{}))

	if reader.Available() {
		t.Fatal("an unconfigured reader claims to be available")
	}

	_, err := reader.Usage(t.Context(), []string{"payments-dev"}, time.Hour)
	if !errors.Is(err, cost.ErrNoUsageSource) {
		t.Fatalf("got %v, want ErrNoUsageSource", err)
	}
}

func policyFrom(t *testing.T, cpu, memory, from, to string) cost.Policy {
	t.Helper()

	policy, err := cost.PolicyFrom(&governv1alpha1.PricingPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "standard", Generation: 3},
		Spec: governv1alpha1.PricingPolicySpec{
			Currency: "IDR",
			Rates: governv1alpha1.Rates{
				CPUCoreMonth:  cpu,
				MemoryGiMonth: memory,
			},
			EffectiveFrom: from,
			EffectiveTo:   to,
			Unallocated:   governv1alpha1.UnallocatedPlatform,
			ApprovedBy:    "finance@example.test",
			ApprovedAt:    metav1.Now(),
		},
	})
	if err != nil {
		t.Fatalf("build policy: %v", err)
	}

	return policy
}

func date(t *testing.T, value string) time.Time {
	t.Helper()

	parsed, err := time.Parse(time.DateOnly, value)
	if err != nil {
		t.Fatalf("parse date: %v", err)
	}

	return parsed
}

func writeScalar(t *testing.T, w http.ResponseWriter, value float64) {
	t.Helper()

	w.Header().Set("Content-Type", "application/json")

	payload := map[string]any{
		"status": "success",
		"data": map[string]any{
			"resultType": "vector",
			"result": []any{
				map[string]any{
					"metric": map[string]string{},
					"value":  []any{float64(1), big.NewRat(int64(value*1000), 1000).FloatString(6)},
				},
			},
		},
	}

	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Errorf("write scalar: %v", err)
	}
}
