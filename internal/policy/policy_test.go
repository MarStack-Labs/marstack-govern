package policy_test

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	governv1alpha1 "github.com/marstack-labs/marstack-govern/api/v1alpha1"
	governv1 "github.com/marstack-labs/marstack-govern/gen/marstack/govern/v1"
	"github.com/marstack-labs/marstack-govern/internal/identity"
	"github.com/marstack-labs/marstack-govern/internal/policy"
)

func TestPoliciesAreReadWithTheirEnforcement(t *testing.T) {
	service := policy.NewService(newCluster(t,
		clusterPolicy("require-requests", "Enforce", "Pods must declare requests and limits"),
		clusterPolicy("no-root", "Audit", "Containers must not run as root"),
	))

	response, err := service.ListPolicies(signedIn(t), connect.NewRequest(&governv1.ListPoliciesRequest{}))
	if err != nil {
		t.Fatalf("list policies: %v", err)
	}

	policies := response.Msg.GetPolicies()
	if len(policies) != 2 {
		t.Fatalf("got %d policies, want 2", len(policies))
	}

	byName := map[string]*governv1.GuardrailPolicy{}
	for _, item := range policies {
		byName[item.GetName()] = item
	}

	if got := byName["require-requests"].GetEnforcement(); got != governv1.GuardrailPolicy_ENFORCEMENT_ENFORCE {
		t.Errorf("enforcement: got %v, want enforce", got)
	}
	if got := byName["no-root"].GetEnforcement(); got != governv1.GuardrailPolicy_ENFORCEMENT_AUDIT {
		t.Errorf("enforcement: got %v, want audit", got)
	}
	if byName["require-requests"].GetDescription() == "" {
		t.Error("the policy is served without its description")
	}
	if !byName["require-requests"].GetReady() {
		t.Error("a ready policy is reported as not ready")
	}
	if len(byName["require-requests"].GetRules()) == 0 {
		t.Error("the policy is served without its rules")
	}
}

func TestViolationsAreScopedToTheDivision(t *testing.T) {
	service := policy.NewService(newCluster(t,
		division("payments", "payments-dev"),
		report("payments-dev", failure("require-requests", "Deployment", "api", "high")),
		report("erp-dev", failure("require-requests", "Deployment", "ledger", "high")),
	))

	response, err := service.ListViolations(signedIn(t), connect.NewRequest(&governv1.ListViolationsRequest{
		Division: "payments",
	}))
	if err != nil {
		t.Fatalf("list violations: %v", err)
	}

	violations := response.Msg.GetViolations()
	if len(violations) != 1 {
		t.Fatalf("got %d violations, want only the division's own", len(violations))
	}
	if violations[0].GetResourceName() != "api" {
		t.Errorf("resource: got %s", violations[0].GetResourceName())
	}
	if violations[0].GetDivision() != "payments" {
		t.Errorf("division: got %s", violations[0].GetDivision())
	}
	if violations[0].GetSeverity() != governv1.Violation_SEVERITY_HIGH {
		t.Errorf("severity: got %v", violations[0].GetSeverity())
	}
}

func TestPassingResultsAreNotViolations(t *testing.T) {
	passing := map[string]any{
		"policy": "require-requests",
		"rule":   "check-requests",
		"result": "pass",
		"resources": []any{
			map[string]any{"kind": "Deployment", "name": "healthy", "namespace": "payments-dev"},
		},
	}

	service := policy.NewService(newCluster(t,
		division("payments", "payments-dev"),
		report("payments-dev", passing, failure("no-root", "Deployment", "api", "critical")),
	))

	response, err := service.ListViolations(signedIn(t), connect.NewRequest(&governv1.ListViolationsRequest{
		Division: "payments",
	}))
	if err != nil {
		t.Fatalf("list violations: %v", err)
	}

	if len(response.Msg.GetViolations()) != 1 {
		t.Fatalf("got %d violations, want 1", len(response.Msg.GetViolations()))
	}
	if response.Msg.GetViolations()[0].GetPolicy() != "no-root" {
		t.Errorf("policy: got %s", response.Msg.GetViolations()[0].GetPolicy())
	}
}

func TestViolationsCanBeFilteredBySeverity(t *testing.T) {
	service := policy.NewService(newCluster(t,
		division("payments", "payments-dev"),
		report("payments-dev",
			failure("no-root", "Deployment", "api", "critical"),
			failure("require-labels", "Deployment", "worker", "low"),
		),
	))

	response, err := service.ListViolations(signedIn(t), connect.NewRequest(&governv1.ListViolationsRequest{
		Division: "payments",
		Severity: governv1.Violation_SEVERITY_CRITICAL,
	}))
	if err != nil {
		t.Fatalf("list violations: %v", err)
	}

	if len(response.Msg.GetViolations()) != 1 {
		t.Fatalf("got %d violations, want the critical one", len(response.Msg.GetViolations()))
	}
	if response.Msg.GetViolations()[0].GetPolicy() != "no-root" {
		t.Errorf("policy: got %s", response.Msg.GetViolations()[0].GetPolicy())
	}
}

func TestComplianceCountsWhatIsFailing(t *testing.T) {
	service := policy.NewService(newCluster(t,
		division("payments", "payments-dev"),
		clusterPolicy("require-requests", "Enforce", "Pods must declare requests"),
		report("payments-dev",
			failure("no-root", "Deployment", "api", "critical"),
			failure("require-labels", "Deployment", "api", "low"),
			failure("require-labels", "Deployment", "worker", "low"),
		),
	))

	response, err := service.GetCompliance(signedIn(t), connect.NewRequest(&governv1.GetComplianceRequest{
		Division: "payments",
	}))
	if err != nil {
		t.Fatalf("compliance: %v", err)
	}

	compliance := response.Msg.GetCompliance()
	if compliance.GetFailing() != 2 {
		t.Errorf("failing resources: got %d, want 2 distinct workloads", compliance.GetFailing())
	}
	if compliance.GetCritical() != 1 {
		t.Errorf("critical: got %d", compliance.GetCritical())
	}
	if compliance.GetPoliciesEnforced() != 1 {
		t.Errorf("enforced policies: got %d", compliance.GetPoliciesEnforced())
	}
}

func TestWithoutKyvernoNothingIsClaimed(t *testing.T) {
	service := policy.NewService(newBareCluster(t))

	_, err := service.ListPolicies(signedIn(t), connect.NewRequest(&governv1.ListPoliciesRequest{}))
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("got %v, want unavailable", err)
	}
	if err == nil || !contains(err.Error(), "kyverno is not installed") {
		t.Errorf("the error does not say what is missing: %v", err)
	}
}

func TestPoliciesAreNotPublic(t *testing.T) {
	service := policy.NewService(newCluster(t))

	_, err := service.ListPolicies(t.Context(), connect.NewRequest(&governv1.ListPoliciesRequest{}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("got %v, want unauthenticated", err)
	}
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

	mapper := meta.NewDefaultRESTMapper(nil)

	for _, gvk := range []schema.GroupVersionKind{
		policy.ClusterPolicyGVK, policy.PolicyGVK,
		policy.PolicyReportGVK, policy.ClusterPolicyReportGVK,
	} {
		scheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
		list := gvk
		list.Kind += "List"
		scheme.AddKnownTypeWithName(list, &unstructured.UnstructuredList{})

		scope := meta.RESTScopeNamespace
		if gvk.Kind == "ClusterPolicy" || gvk.Kind == "ClusterPolicyReport" {
			scope = meta.RESTScopeRoot
		}
		mapper.Add(gvk, scope)
	}

	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithRESTMapper(mapper).
		WithObjects(objects...).
		Build()
}

func newBareCluster(t *testing.T) client.Client {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithRESTMapper(meta.NewDefaultRESTMapper(nil)).
		Build()
}

func division(name string, namespaces ...string) *governv1alpha1.Division {
	return &governv1alpha1.Division{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status:     governv1alpha1.DivisionStatus{Namespaces: namespaces},
	}
}

func clusterPolicy(name, action, description string) *unstructured.Unstructured {
	object := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{
			"validationFailureAction": action,
			"rules": []any{
				map[string]any{"name": "check-" + name},
			},
		},
		"status": map[string]any{
			"conditions": []any{
				map[string]any{"type": "Ready", "status": "True"},
			},
		},
	}}

	object.SetGroupVersionKind(policy.ClusterPolicyGVK)
	object.SetName(name)
	object.SetAnnotations(map[string]string{
		"policies.kyverno.io/description": description,
		"policies.kyverno.io/category":    "Pod Security, Best Practices",
		"policies.kyverno.io/severity":    "medium",
	})

	return object
}

func report(namespace string, results ...map[string]any) *unstructured.Unstructured {
	items := make([]any, 0, len(results))
	for _, result := range results {
		items = append(items, result)
	}

	object := &unstructured.Unstructured{Object: map[string]any{"results": items}}
	object.SetGroupVersionKind(policy.PolicyReportGVK)
	object.SetName("report-" + namespace)
	object.SetNamespace(namespace)

	return object
}

func failure(policyName, kind, name, severity string) map[string]any {
	return map[string]any{
		"policy":   policyName,
		"rule":     "check-" + policyName,
		"result":   "fail",
		"severity": severity,
		"message":  "the resource does not satisfy " + policyName,
		"resources": []any{
			map[string]any{"kind": kind, "name": name},
		},
	}
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}

func TestAReportThatNamesItsSubjectInScopeIsRead(t *testing.T) {
	scoped := &unstructured.Unstructured{Object: map[string]any{
		"results": []any{
			map[string]any{
				"policy":  "require-resources",
				"rule":    "autogen-requests-and-limits",
				"result":  "fail",
				"message": "every container must declare resources.requests and limits",
			},
		},
		"scope": map[string]any{
			"apiVersion": "apps/v1",
			"kind":       "ReplicaSet",
			"name":       "offender-84c4f46d47",
			"namespace":  "payments-dev",
		},
	}}
	scoped.SetGroupVersionKind(policy.PolicyReportGVK)
	scoped.SetName("269e4ac8-14ad-47d8-a60c-2255e23ad8b1")
	scoped.SetNamespace("payments-dev")

	service := policy.NewService(newCluster(t, division("payments", "payments-dev"), scoped))

	response, err := service.ListViolations(signedIn(t), connect.NewRequest(
		&governv1.ListViolationsRequest{Division: "payments"}))
	if err != nil {
		t.Fatalf("list violations: %v", err)
	}

	if len(response.Msg.GetViolations()) != 1 {
		t.Fatalf("a per-resource report was dropped: got %d violations",
			len(response.Msg.GetViolations()))
	}

	violation := response.Msg.GetViolations()[0]
	if violation.GetResourceKind() != "ReplicaSet" || violation.GetResourceName() != "offender-84c4f46d47" {
		t.Fatalf("the subject was not taken from scope: %s/%s",
			violation.GetResourceKind(), violation.GetResourceName())
	}
	if violation.GetNamespace() != "payments-dev" {
		t.Errorf("namespace: got %s", violation.GetNamespace())
	}
}

func TestAResourceWithOnlyWarningsIsNotCountedAsFailing(t *testing.T) {
	warning := map[string]any{
		"policy":  "prefer-probes",
		"rule":    "check-probes",
		"result":  "warn",
		"message": "the container has no readiness probe",
		"resources": []any{
			map[string]any{"kind": "Deployment", "name": "api", "namespace": "payments-dev"},
		},
	}

	service := policy.NewService(newCluster(t,
		division("payments", "payments-dev"),
		report("payments-dev", warning),
	))

	response, err := service.GetCompliance(signedIn(t), connect.NewRequest(
		&governv1.GetComplianceRequest{Division: "payments"}))
	if err != nil {
		t.Fatalf("compliance: %v", err)
	}

	summary := response.Msg.GetCompliance()
	if summary.GetFailing() != 0 {
		t.Fatalf("a resource with only warnings was reported as failing: %d", summary.GetFailing())
	}
	if summary.GetWarning() != 1 {
		t.Fatalf("the warning was lost: %d", summary.GetWarning())
	}
}
