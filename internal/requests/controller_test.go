package requests_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	governv1alpha1 "github.com/marstack-labs/marstack-govern/api/v1alpha1"
	"github.com/marstack-labs/marstack-govern/internal/requests"
)

func TestANewRequestArrivesWithEvidence(t *testing.T) {
	c := newCluster(t, division(8, 16), quotaRequest(12, 24), node(40, 96))
	reconcileRequest(t, c, withMetrics(t))

	request := readRequest(t, c)

	if request.Status.Phase != governv1alpha1.RequestAwaitingDecision {
		t.Errorf("phase: got %q", request.Status.Phase)
	}
	if request.Status.Recommendation == nil {
		t.Fatal("the request carries no recommendation")
	}
	if got := request.Status.Recommendation.Proposed.CPU.MilliValue(); got != 9000 {
		t.Errorf("proposed cpu: got %dm, want 9000m", got)
	}
	if request.Status.Preflight == nil || !request.Status.Preflight.Admitted {
		t.Errorf("preflight: got %+v", request.Status.Preflight)
	}
	if request.Status.EvidenceDigest == "" {
		t.Error("the request carries no evidence digest")
	}
	if !conditionTrue(request.Status.Conditions, governv1alpha1.ConditionRecommended) {
		t.Error("Recommended is not true")
	}
}

func TestAMissingMetricsSourceIsStatedNotGuessed(t *testing.T) {
	c := newCluster(t, division(8, 16), quotaRequest(12, 24), node(40, 96))

	reconciler := &requests.RequestReconciler{
		Client:    c,
		Preflight: &requests.Preflight{Client: c},
	}
	reconcile(t, reconciler)

	request := readRequest(t, c)

	if request.Status.Recommendation != nil {
		t.Errorf("a recommendation was invented without metrics: %+v", request.Status.Recommendation)
	}

	condition := findCondition(request.Status.Conditions, governv1alpha1.ConditionRecommended)
	if condition == nil || condition.Status != metav1.ConditionFalse || condition.Reason != "NoMetrics" {
		t.Fatalf("condition: got %+v", condition)
	}
	if request.Status.Phase != governv1alpha1.RequestAwaitingDecision {
		t.Errorf("phase: got %q, want AwaitingDecision", request.Status.Phase)
	}
}

func TestPreflightBlocksATargetTheClusterCannotHold(t *testing.T) {
	c := newCluster(t, division(8, 16), quotaRequest(64, 24), node(40, 96))
	reconcileRequest(t, c, withMetrics(t))

	request := readRequest(t, c)

	if request.Status.Preflight == nil {
		t.Fatal("the request carries no preflight result")
	}
	if request.Status.Preflight.Admitted {
		t.Fatal("a target larger than the cluster was admitted")
	}

	blocking := false
	for _, finding := range request.Status.Preflight.Findings {
		if finding.Severity == "block" && finding.Check == "cluster-cpu" {
			blocking = true
			if finding.Field != "spec.target.cpu" {
				t.Errorf("the finding names no field: %+v", finding)
			}
		}
	}
	if !blocking {
		t.Errorf("findings: got %+v", request.Status.Preflight.Findings)
	}
}

func TestApprovingAppliesTheQuota(t *testing.T) {
	c := newCluster(t, division(8, 16), quotaRequest(12, 24), node(40, 96))
	reconcileRequest(t, c, withMetrics(t))

	decide(t, c, governv1alpha1.OutcomeApproved, ninetyDays())

	updated := &governv1alpha1.Division{}
	if err := c.Get(t.Context(), types.NamespacedName{Name: "payments"}, updated); err != nil {
		t.Fatalf("get division: %v", err)
	}

	if got := updated.Spec.Quota.CPU.Value(); got != 12 {
		t.Errorf("division cpu: got %d, want 12", got)
	}
	if got := updated.Spec.Quota.Memory.Value(); got != 24*gibibyte {
		t.Errorf("division memory: got %d, want %d", got, 24*gibibyte)
	}

	request := readRequest(t, c)
	if request.Status.Phase != governv1alpha1.RequestApplied {
		t.Errorf("request phase: got %q, want Applied", request.Status.Phase)
	}

	decision := readDecision(t, c)
	if !decision.Status.Applied || decision.Status.AppliedAt == nil {
		t.Errorf("decision status: got %+v", decision.Status)
	}
}

func TestRejectingLeavesTheQuotaAlone(t *testing.T) {
	c := newCluster(t, division(8, 16), quotaRequest(12, 24), node(40, 96))
	reconcileRequest(t, c, withMetrics(t))

	decide(t, c, governv1alpha1.OutcomeRejected, nil)

	updated := &governv1alpha1.Division{}
	if err := c.Get(t.Context(), types.NamespacedName{Name: "payments"}, updated); err != nil {
		t.Fatalf("get division: %v", err)
	}

	if got := updated.Spec.Quota.CPU.Value(); got != 8 {
		t.Errorf("division cpu: got %d, want the untouched 8", got)
	}

	request := readRequest(t, c)
	if request.Status.Phase != governv1alpha1.RequestRejected {
		t.Errorf("request phase: got %q, want Rejected", request.Status.Phase)
	}
}

func TestALapsedGrantIsFlaggedWithoutRevertingTheQuota(t *testing.T) {
	c := newCluster(t, division(8, 16), quotaRequest(12, 24), node(40, 96))
	reconcileRequest(t, c, withMetrics(t))

	granted := metav1.NewTime(time.Now().Add(24 * time.Hour))
	decide(t, c, governv1alpha1.OutcomeApproved, &granted)

	later := &requests.DecisionReconciler{
		Client: c,
		Now:    func() time.Time { return granted.Add(time.Hour) },
	}
	reconcileNamed(t, later, "", "decision-1")

	decision := readDecision(t, c)
	if !decision.Status.Expired {
		t.Error("a lapsed grant was not marked expired")
	}

	request := readRequest(t, c)
	if request.Status.Phase != governv1alpha1.RequestExpired {
		t.Errorf("request phase: got %q, want Expired", request.Status.Phase)
	}

	updated := &governv1alpha1.Division{}
	if err := c.Get(t.Context(), types.NamespacedName{Name: "payments"}, updated); err != nil {
		t.Fatalf("get division: %v", err)
	}
	if got := updated.Spec.Quota.CPU.Value(); got != 12 {
		t.Errorf("the quota was silently reverted to %d", got)
	}
}

func TestAWithdrawnRequestStopsThere(t *testing.T) {
	request := quotaRequest(12, 24)
	request.Spec.Withdrawn = true

	c := newCluster(t, division(8, 16), request, node(40, 96))
	reconcileRequest(t, c, withMetrics(t))

	updated := readRequest(t, c)
	if updated.Status.Phase != governv1alpha1.RequestWithdrawn {
		t.Errorf("phase: got %q, want Withdrawn", updated.Status.Phase)
	}
	if updated.Status.Recommendation != nil {
		t.Error("a withdrawn request was still given a recommendation")
	}
}

func TestTheEvidenceDigestChangesWithTheEvidence(t *testing.T) {
	first, err := requests.EvidenceDigest(nil, &governv1alpha1.PreflightResult{Admitted: true})
	if err != nil {
		t.Fatalf("digest: %v", err)
	}

	second, err := requests.EvidenceDigest(nil, &governv1alpha1.PreflightResult{Admitted: false})
	if err != nil {
		t.Fatalf("digest: %v", err)
	}

	if first == second {
		t.Fatal("two different evidence sets share a digest")
	}
}

func newCluster(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add client-go scheme: %v", err)
	}
	if err := governv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add govern scheme: %v", err)
	}

	assigned := 0

	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, c client.WithWatch, object client.Object, opts ...client.CreateOption) error {
				if object.GetUID() == "" {
					assigned++
					object.SetUID(types.UID(fmt.Sprintf("00000000-0000-0000-0000-%012d", assigned)))
				}
				return c.Create(ctx, object, opts...)
			},
		}).
		WithStatusSubresource(
			&governv1alpha1.Division{},
			&governv1alpha1.QuotaRequest{},
			&governv1alpha1.Decision{},
		).
		Build()
}

func withMetrics(t *testing.T) *requests.Recommender {
	t.Helper()

	return requests.NewRecommender(newFakeMetrics(t, map[string]float64{
		"cpuCurrent": 4.9,
		"cpuP95":     6.2,
		"cpuP99":     6.8,
		"memCurrent": 13.1 * gibibyte,
		"memP95":     14.0 * gibibyte,
		"memP99":     14.5 * gibibyte,
	}), 30*24*time.Hour)
}

func reconcileRequest(t *testing.T, c client.Client, recommender *requests.Recommender) {
	t.Helper()

	reconcile(t, &requests.RequestReconciler{
		Client:      c,
		Recommender: recommender,
		Preflight:   &requests.Preflight{Client: c},
	})
}

func reconcile(t *testing.T, reconciler *requests.RequestReconciler) {
	t.Helper()

	reconcileNamed(t, reconciler, "payments-dev", "raise-quota")
}

type reconciler interface {
	Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error)
}

func reconcileNamed(t *testing.T, target reconciler, namespace, name string) {
	t.Helper()

	request := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: namespace, Name: name}}
	if _, err := target.Reconcile(t.Context(), request); err != nil {
		t.Fatalf("reconcile %s/%s: %v", namespace, name, err)
	}
}

func decide(t *testing.T, c client.Client, outcome governv1alpha1.Outcome, until *metav1.Time) {
	t.Helper()

	request := readRequest(t, c)

	decision := &governv1alpha1.Decision{
		ObjectMeta: metav1.ObjectMeta{Name: "decision-1"},
		Spec: governv1alpha1.DecisionSpec{
			Request: governv1alpha1.RequestRef{
				Kind:      "QuotaRequest",
				Name:      request.Name,
				Namespace: request.Namespace,
			},
			Outcome:      outcome,
			Reason:       "approved for the quarter",
			DecidedBy:    "lead@example.test",
			GrantedUntil: until,
			Evidence: governv1alpha1.Evidence{
				Digest:     request.Status.EvidenceDigest,
				CapturedAt: metav1.Now(),
			},
		},
	}

	if err := c.Create(t.Context(), decision); err != nil {
		t.Fatalf("create decision: %v", err)
	}

	reconcileNamed(t, &requests.DecisionReconciler{Client: c}, "", "decision-1")
}

func readRequest(t *testing.T, c client.Client) *governv1alpha1.QuotaRequest {
	t.Helper()

	request := &governv1alpha1.QuotaRequest{}
	key := types.NamespacedName{Namespace: "payments-dev", Name: "raise-quota"}
	if err := c.Get(t.Context(), key, request); err != nil {
		t.Fatalf("get request: %v", err)
	}

	return request
}

func readDecision(t *testing.T, c client.Client) *governv1alpha1.Decision {
	t.Helper()

	decision := &governv1alpha1.Decision{}
	if err := c.Get(t.Context(), types.NamespacedName{Name: "decision-1"}, decision); err != nil {
		t.Fatalf("get decision: %v", err)
	}

	return decision
}

func division(cpu int64, memoryGi int64) *governv1alpha1.Division {
	return &governv1alpha1.Division{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "payments",
			UID:        "11111111-1111-1111-1111-111111111111",
			Generation: 1,
		},
		Spec: governv1alpha1.DivisionSpec{
			DisplayName:  "Payments",
			Environments: []string{"dev"},
			Quota: governv1alpha1.Quota{
				CPU:    *resource.NewQuantity(cpu, resource.DecimalSI),
				Memory: *resource.NewQuantity(memoryGi*gibibyte, resource.BinarySI),
			},
		},
		Status: governv1alpha1.DivisionStatus{
			Phase:      governv1alpha1.DivisionActive,
			Namespaces: []string{"payments-dev"},
		},
	}
}

func quotaRequest(cpu int64, memoryGi int64) *governv1alpha1.QuotaRequest {
	return &governv1alpha1.QuotaRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "raise-quota",
			Namespace:  "payments-dev",
			UID:        "22222222-2222-2222-2222-222222222222",
			Generation: 1,
		},
		Spec: governv1alpha1.QuotaRequestSpec{
			Division:    "payments",
			Reason:      "onboarding two backends for the quarter",
			RequestedBy: "dev@example.test",
			Target: governv1alpha1.Quota{
				CPU:    *resource.NewQuantity(cpu, resource.DecimalSI),
				Memory: *resource.NewQuantity(memoryGi*gibibyte, resource.BinarySI),
			},
		},
	}
}

func node(cpu int64, memoryGi int64) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    *resource.NewQuantity(cpu, resource.DecimalSI),
				corev1.ResourceMemory: *resource.NewQuantity(memoryGi*gibibyte, resource.BinarySI),
			},
		},
	}
}

func ninetyDays() *metav1.Time {
	at := metav1.NewTime(time.Now().Add(90 * 24 * time.Hour))

	return &at
}

func conditionTrue(conditions []metav1.Condition, conditionType string) bool {
	condition := findCondition(conditions, conditionType)

	return condition != nil && condition.Status == metav1.ConditionTrue
}

func findCondition(conditions []metav1.Condition, conditionType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return &conditions[i]
		}
	}

	return nil
}
