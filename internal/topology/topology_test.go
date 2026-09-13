package topology_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/marstack-labs/marstack-govern/internal/metrics"
	"github.com/marstack-labs/marstack-govern/internal/topology"
)

func TestTheGraphComesFromTraceMetrics(t *testing.T) {
	source := newVectorSource(t, map[string][]sample{
		"traces_service_graph_request_total": {
			{labels: map[string]string{"client": "payments-dev/api", "server": "payments-dev/ledger"}, value: 12.5},
			{labels: map[string]string{"client": "payments-dev/api", "server": "erp-dev/reporting"}, value: 0.4},
		},
		"traces_service_graph_request_failed_total": {
			{labels: map[string]string{"client": "payments-dev/api", "server": "erp-dev/reporting"}, value: 0.2},
		},
	})

	graph := topology.NewGraph(source, time.Hour)

	edges, err := graph.Edges(t.Context(), []string{"payments-dev"})
	if err != nil {
		t.Fatalf("edges: %v", err)
	}

	if len(edges) != 2 {
		t.Fatalf("got %d edges, want 2", len(edges))
	}

	byServer := map[string]topology.Edge{}
	for _, edge := range edges {
		byServer[edge.Server] = edge
	}

	if got := byServer["payments-dev/ledger"].RequestsPerS; got != 12.5 {
		t.Errorf("requests: got %v", got)
	}
	if got := byServer["erp-dev/reporting"].ErrorRatio(); got < 0.49 || got > 0.51 {
		t.Errorf("error ratio: got %v, want about 0.5", got)
	}
	if byServer["payments-dev/ledger"].ErrorRatio() != 0 {
		t.Errorf("a healthy edge was given errors: %v", byServer["payments-dev/ledger"])
	}
}

func TestWithoutTracesNoGraphIsDrawn(t *testing.T) {
	graph := topology.NewGraph(metrics.New(metrics.Config{}), time.Hour)

	if graph.Available() {
		t.Fatal("an unconfigured graph claims to be available")
	}

	if _, err := graph.Edges(t.Context(), []string{"payments-dev"}); err == nil {
		t.Fatal("a graph was returned without any traces")
	}
}

func TestDefaultDenyMeansDenied(t *testing.T) {
	reachability := topology.NewReachability(newCluster(t,
		namespace("payments-dev", "payments"),
		namespace("erp-dev", "erp"),
		defaultDeny("payments-dev"),
	))

	paths, err := reachability.Paths(t.Context(), []string{"payments-dev"}, nil)
	if err != nil {
		t.Fatalf("paths: %v", err)
	}

	path := find(t, paths, "erp-dev", "payments-dev")
	if path.Allowed {
		t.Fatalf("default deny let traffic through: %+v", path)
	}
	if path.Verdict != topology.VerdictDenied {
		t.Errorf("verdict: got %q", path.Verdict)
	}
}

func TestANamespaceWithoutPoliciesIsWideOpen(t *testing.T) {
	reachability := topology.NewReachability(newCluster(t,
		namespace("payments-dev", "payments"),
		namespace("erp-dev", "erp"),
	))

	paths, err := reachability.Paths(t.Context(), []string{"payments-dev"}, nil)
	if err != nil {
		t.Fatalf("paths: %v", err)
	}

	path := find(t, paths, "erp-dev", "payments-dev")
	if !path.Allowed {
		t.Fatal("a namespace with no ingress policy was reported as closed")
	}
}

func TestAnAllowanceNobodyUsesIsReported(t *testing.T) {
	reachability := topology.NewReachability(newCluster(t,
		namespace("payments-dev", "payments"),
		namespace("erp-dev", "erp"),
		defaultDeny("payments-dev"),
		allowFrom("payments-dev", "allow-erp", map[string]string{"govern.marstack.io/division": "erp"}),
	))

	paths, err := reachability.Paths(t.Context(), []string{"payments-dev"}, nil)
	if err != nil {
		t.Fatalf("paths: %v", err)
	}

	path := find(t, paths, "erp-dev", "payments-dev")
	if !path.Allowed {
		t.Fatal("an explicit allowance did not take effect")
	}
	if path.AllowedBy != "allow-erp" {
		t.Errorf("allowed by: got %q", path.AllowedBy)
	}
	if path.Verdict != topology.VerdictAllowedUnused {
		t.Errorf("verdict: got %q, want the unused allowance", path.Verdict)
	}
}

func TestTrafficNoPolicyPermitsIsReported(t *testing.T) {
	reachability := topology.NewReachability(newCluster(t,
		namespace("payments-dev", "payments"),
		namespace("erp-dev", "erp"),
		defaultDeny("payments-dev"),
	))

	observed := []topology.Edge{{Client: "erp-dev", Server: "payments-dev", RequestsPerS: 3}}

	paths, err := reachability.Paths(t.Context(), []string{"payments-dev"}, observed)
	if err != nil {
		t.Fatalf("paths: %v", err)
	}

	path := find(t, paths, "erp-dev", "payments-dev")
	if path.Verdict != topology.VerdictObservedNotAllowed {
		t.Fatalf("verdict: got %q, want traffic that no policy permits", path.Verdict)
	}
}

func TestAnAllowanceThatIsUsedLooksHealthy(t *testing.T) {
	reachability := topology.NewReachability(newCluster(t,
		namespace("payments-dev", "payments"),
		namespace("erp-dev", "erp"),
		defaultDeny("payments-dev"),
		allowFrom("payments-dev", "allow-erp", map[string]string{"govern.marstack.io/division": "erp"}),
	))

	observed := []topology.Edge{{Client: "erp-dev", Server: "payments-dev", RequestsPerS: 3}}

	paths, err := reachability.Paths(t.Context(), []string{"payments-dev"}, observed)
	if err != nil {
		t.Fatalf("paths: %v", err)
	}

	if got := find(t, paths, "erp-dev", "payments-dev").Verdict; got != topology.VerdictAllowedAndUsed {
		t.Fatalf("verdict: got %q", got)
	}
}

func TestAnEgressOnlyPolicyDoesNotCloseIngress(t *testing.T) {
	egressOnly := defaultDeny("payments-dev")
	egressOnly.Spec.PolicyTypes = []networkingv1.PolicyType{networkingv1.PolicyTypeEgress}

	reachability := topology.NewReachability(newCluster(t,
		namespace("payments-dev", "payments"),
		namespace("erp-dev", "erp"),
		egressOnly,
	))

	paths, err := reachability.Paths(t.Context(), []string{"payments-dev"}, nil)
	if err != nil {
		t.Fatalf("paths: %v", err)
	}

	if !find(t, paths, "erp-dev", "payments-dev").Allowed {
		t.Fatal("a policy that only governs egress was read as closing ingress")
	}
}

type sample struct {
	labels map[string]string
	value  float64
}

func newVectorSource(t *testing.T, samples map[string][]sample) *metrics.Client {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")

		matched := []sample{}
		for metric, values := range samples {
			if strings.Contains(query, metric) {
				matched = values
				break
			}
		}

		results := make([]any, 0, len(matched))
		for _, item := range matched {
			results = append(results, map[string]any{
				"metric": item.labels,
				"value":  []any{float64(1), strconv.FormatFloat(item.value, 'f', -1, 64)},
			})
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data":   map[string]any{"resultType": "vector", "result": results},
		}); err != nil {
			t.Errorf("write samples: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	return metrics.New(metrics.Config{BaseURL: server.URL})
}

func newCluster(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}

func namespace(name, division string) *corev1.Namespace {
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{"govern.marstack.io/division": division},
		},
	}
}

func defaultDeny(namespace string) *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "govern-default-deny", Namespace: namespace},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []networkingv1.PolicyType{
				networkingv1.PolicyTypeIngress,
				networkingv1.PolicyTypeEgress,
			},
		},
	}
}

func allowFrom(namespace, name string, selector map[string]string) *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{{
				From: []networkingv1.NetworkPolicyPeer{{
					NamespaceSelector: &metav1.LabelSelector{MatchLabels: selector},
				}},
			}},
		},
	}
}

func find(t *testing.T, paths []topology.Path, from, to string) topology.Path {
	t.Helper()

	for _, path := range paths {
		if path.From == from && path.To == to {
			return path
		}
	}

	t.Fatalf("no path from %s to %s in %v", from, to, paths)

	return topology.Path{}
}
