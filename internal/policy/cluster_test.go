package policy_test

import (
	"os"
	"testing"

	"connectrpc.com/connect"
	"sigs.k8s.io/controller-runtime/pkg/client"

	governv1 "github.com/marstack-labs/marstack-govern/gen/marstack/govern/v1"
	"github.com/marstack-labs/marstack-govern/internal/identity"
	"github.com/marstack-labs/marstack-govern/internal/kube"
	"github.com/marstack-labs/marstack-govern/internal/policy"
	"github.com/marstack-labs/marstack-govern/internal/tenancy"
)

const (
	envContext  = "GOVERN_TEST_KUBE_CONTEXT"
	envDivision = "GOVERN_TEST_DIVISION"
)

func TestAgainstARealKyverno(t *testing.T) {
	kubeContext := os.Getenv(envContext)
	if kubeContext == "" {
		t.Skipf("set %s to a cluster running Kyverno to exercise this", envContext)
	}

	division := os.Getenv(envDivision)
	if division == "" {
		division = "payments"
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

	ctx := identity.NewContext(t.Context(), identity.Actor{
		Subject:        "smoke@marstack.test",
		ActiveDivision: division,
	})

	service := policy.NewService(api)

	policies, err := service.ListPolicies(ctx, connect.NewRequest(&governv1.ListPoliciesRequest{}))
	if err != nil {
		t.Fatalf("list policies: %v", err)
	}
	if len(policies.Msg.GetPolicies()) == 0 {
		t.Skip("Kyverno is installed but no policy is applied, so there is nothing to read back")
	}

	for _, guardrail := range policies.Msg.GetPolicies() {
		if guardrail.GetName() == "" {
			t.Errorf("a guardrail came back with no name: %+v", guardrail)
		}
		if len(guardrail.GetRules()) == 0 {
			t.Errorf("%s came back with no rules", guardrail.GetName())
		}
	}

	violations, err := service.ListViolations(ctx, connect.NewRequest(
		&governv1.ListViolationsRequest{Division: division}))
	if err != nil {
		t.Fatalf("list violations: %v", err)
	}

	for _, violation := range violations.Msg.GetViolations() {
		if violation.GetPolicy() == "" || violation.GetMessage() == "" {
			t.Errorf("a violation carries no policy or reason: %+v", violation)
		}
		if violation.GetResourceName() == "" {
			t.Errorf("a violation names nothing: %+v", violation)
		}
	}

	compliance, err := service.GetCompliance(ctx, connect.NewRequest(
		&governv1.GetComplianceRequest{Division: division}))
	if err != nil {
		t.Fatalf("compliance: %v", err)
	}

	summary := compliance.Msg.GetCompliance()

	offending := map[string]bool{}
	for _, violation := range violations.Msg.GetViolations() {
		if violation.GetResult() == governv1.Violation_RESULT_WARN {
			continue
		}
		offending[violation.GetNamespace()+"/"+violation.GetResourceKind()+"/"+violation.GetResourceName()] = true
	}

	if int(summary.GetFailing()) != len(offending) {
		t.Errorf("the summary counts %d failing resources but %d distinct ones were listed",
			summary.GetFailing(), len(offending))
	}
	if int(summary.GetFailing()) > len(violations.Msg.GetViolations()) {
		t.Errorf("more resources are failing (%d) than results were listed (%d)",
			summary.GetFailing(), len(violations.Msg.GetViolations()))
	}

	t.Logf("%d guardrails, %d listed (%d failing, %d warning), %d enforced rather than audited",
		len(policies.Msg.GetPolicies()), len(violations.Msg.GetViolations()),
		summary.GetFailing(), summary.GetWarning(), summary.GetPoliciesEnforced())
}
