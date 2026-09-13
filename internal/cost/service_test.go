package cost_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	governv1alpha1 "github.com/marstack-labs/marstack-govern/api/v1alpha1"
	governv1 "github.com/marstack-labs/marstack-govern/gen/marstack/govern/v1"
	"github.com/marstack-labs/marstack-govern/internal/cost"
	"github.com/marstack-labs/marstack-govern/internal/identity"
	"github.com/marstack-labs/marstack-govern/internal/metrics"
)

func TestADivisionIsChargedForWhatItReserved(t *testing.T) {
	c := newCluster(t, division(), pricingPolicy("150000", "25000", "2020-01-01"))

	service := cost.NewService(c, readerReturning(t, map[string]float64{
		"cpuRequested":    730,
		"cpuUsed":         365,
		"memoryRequested": 730 * gibibyte,
		"memoryUsed":      730 * gibibyte,
	}), nil)

	response, err := service.GetDivisionCost(signedIn(t), connect.NewRequest(&governv1.GetDivisionCostRequest{
		Division: "payments",
	}))
	if err != nil {
		t.Fatalf("cost: %v", err)
	}

	bill := response.Msg.GetCost()

	if got := bill.GetCharged().GetAmount(); got != "175000.00" {
		t.Errorf("charged: got %s, want 175000.00", got)
	}
	if got := bill.GetCharged().GetCurrency(); got != "IDR" {
		t.Errorf("currency: got %s", got)
	}
	if got := bill.GetIdle().GetAmount(); got != "75000.00" {
		t.Errorf("idle: got %s, want 75000.00 for the half of the cpu nobody used", got)
	}
	if bill.GetPricingPolicy() != "standard" {
		t.Errorf("policy: got %s", bill.GetPricingPolicy())
	}
	if len(bill.GetComponents()) != 4 {
		t.Errorf("components: got %d, want one per resource", len(bill.GetComponents()))
	}
}

func TestWithoutAPolicyNothingIsInvented(t *testing.T) {
	c := newCluster(t, division())

	service := cost.NewService(c, readerReturning(t, map[string]float64{"cpuRequested": 730}), nil)

	_, err := service.GetDivisionCost(signedIn(t), connect.NewRequest(&governv1.GetDivisionCostRequest{
		Division: "payments",
	}))

	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("got %v, want failed precondition", err)
	}
	if !strings.Contains(err.Error(), "pricing policy") {
		t.Errorf("the error does not say what is missing: %v", err)
	}
}

func TestWithoutMetricsTheBillIsRefusedNotEstimated(t *testing.T) {
	c := newCluster(t, division(), pricingPolicy("150000", "25000", "2020-01-01"))

	service := cost.NewService(c, cost.NewReader(metrics.New(metrics.Config{})), nil)

	_, err := service.GetDivisionCost(signedIn(t), connect.NewRequest(&governv1.GetDivisionCostRequest{
		Division: "payments",
	}))

	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("got %v, want unavailable", err)
	}
}

func TestTopConsumersAreOrderedByWhatTheyCost(t *testing.T) {
	c := newCluster(t, division(), pricingPolicy("730", "0", "2020-01-01"))

	service := cost.NewService(c, readerReturning(t, map[string]float64{"cpuRequested": 10}), workloads{
		items: []cost.WorkloadRequest{
			{UID: "1", Namespace: "payments-dev", Name: "small", CPUMillicores: 100},
			{UID: "2", Namespace: "payments-dev", Name: "large", CPUMillicores: 4000},
			{UID: "3", Namespace: "payments-dev", Name: "medium", CPUMillicores: 1000},
		},
	})

	response, err := service.GetDivisionCost(signedIn(t), connect.NewRequest(&governv1.GetDivisionCostRequest{
		Division: "payments",
	}))
	if err != nil {
		t.Fatalf("cost: %v", err)
	}

	consumers := response.Msg.GetCost().GetTopConsumers()
	if len(consumers) != 3 {
		t.Fatalf("consumers: got %d", len(consumers))
	}

	names := []string{consumers[0].GetName(), consumers[1].GetName(), consumers[2].GetName()}
	want := []string{"large", "medium", "small"}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("order: got %v, want %v", names, want)
		}
	}
}

func TestAnonymousCallersGetNoBill(t *testing.T) {
	c := newCluster(t, division(), pricingPolicy("150000", "25000", "2020-01-01"))
	service := cost.NewService(c, readerReturning(t, nil), nil)

	_, err := service.GetDivisionCost(t.Context(), connect.NewRequest(&governv1.GetDivisionCostRequest{
		Division: "payments",
	}))

	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("got %v, want unauthenticated", err)
	}
}

func TestTheEffectivePolicyIsReportedWithItsApprover(t *testing.T) {
	c := newCluster(t, division(), pricingPolicy("150000", "25000", "2020-01-01"))
	service := cost.NewService(c, readerReturning(t, nil), nil)

	response, err := service.GetPricingPolicy(signedIn(t), connect.NewRequest(&governv1.GetPricingPolicyRequest{}))
	if err != nil {
		t.Fatalf("policy: %v", err)
	}

	policy := response.Msg.GetPolicy()
	if policy.GetCpuCoreMonth().GetAmount() != "150000.00" {
		t.Errorf("cpu rate: got %s", policy.GetCpuCoreMonth().GetAmount())
	}
	if policy.GetApprovedBy() != "finance@example.test" {
		t.Errorf("approver: got %s", policy.GetApprovedBy())
	}
}

func TestWithoutAReadModelInvoicesSaySoRatherThanReturningNone(t *testing.T) {
	c := newCluster(t, division(), pricingPolicy("150000", "25000", "2020-01-01"))
	service := cost.NewService(c, readerReturning(t, nil), nil)

	_, err := service.ListInvoices(signedIn(t), connect.NewRequest(
		&governv1.ListInvoicesRequest{Division: "payments"}))

	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("got %v, want unavailable rather than an empty list", err)
	}
}

func TestRightSizingWithoutMetricsRefusesToGuess(t *testing.T) {
	c := newCluster(t, division(), pricingPolicy("150000", "25000", "2020-01-01"))
	service := cost.NewService(c, cost.NewReader(nil), workloads{})

	_, err := service.ListRightSizing(signedIn(t), connect.NewRequest(
		&governv1.ListRightSizingRequest{Division: "payments"}))

	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("got %v, want a refusal to propose numbers from nothing", err)
	}
}

const gibibyte = 1024 * 1024 * 1024

type workloads struct {
	items []cost.WorkloadRequest
}

func (w workloads) RequestedByWorkload(context.Context, []string) ([]cost.WorkloadRequest, error) {
	return w.items, nil
}

func signedIn(t *testing.T) context.Context {
	t.Helper()

	return identity.NewContext(t.Context(), identity.Actor{Subject: "lead@example.test"})
}

func newCluster(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	if err := governv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add govern scheme: %v", err)
	}

	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&governv1alpha1.PricingPolicy{}).
		Build()
}

func division() *governv1alpha1.Division {
	return &governv1alpha1.Division{
		ObjectMeta: metav1.ObjectMeta{Name: "payments"},
		Spec:       governv1alpha1.DivisionSpec{DisplayName: "Payments"},
		Status:     governv1alpha1.DivisionStatus{Namespaces: []string{"payments-dev"}},
	}
}

func pricingPolicy(cpu, memory, from string) *governv1alpha1.PricingPolicy {
	return &governv1alpha1.PricingPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "standard", Generation: 1},
		Spec: governv1alpha1.PricingPolicySpec{
			Currency:      "IDR",
			Rates:         governv1alpha1.Rates{CPUCoreMonth: cpu, MemoryGiMonth: memory},
			EffectiveFrom: from,
			Unallocated:   governv1alpha1.UnallocatedPlatform,
			ApprovedBy:    "finance@example.test",
			ApprovedAt:    metav1.NewTime(time.Now()),
		},
	}
}

func readerReturning(t *testing.T, values map[string]float64) *cost.Reader {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeScalar(t, w, values[classifyCostQuery(r.URL.Query().Get("query"))])
	}))
	t.Cleanup(server.Close)

	return cost.NewReader(metrics.New(metrics.Config{BaseURL: server.URL}))
}

func classifyCostQuery(query string) string {
	switch {
	case strings.Contains(query, "kube_persistentvolumeclaim"):
		return "storage"
	case strings.Contains(query, "kube_service_spec_type"):
		return "loadBalancers"
	case strings.Contains(query, `resource="cpu"`):
		return "cpuRequested"
	case strings.Contains(query, `resource="memory"`):
		return "memoryRequested"
	case strings.Contains(query, "container_cpu_usage_seconds_total"):
		return "cpuUsed"
	case strings.Contains(query, "container_memory_working_set_bytes"):
		return "memoryUsed"
	default:
		return "unknown"
	}
}
