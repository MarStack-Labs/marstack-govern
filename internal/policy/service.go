package policy

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	governv1alpha1 "github.com/marstack-labs/marstack-govern/api/v1alpha1"
	governv1 "github.com/marstack-labs/marstack-govern/gen/marstack/govern/v1"
	"github.com/marstack-labs/marstack-govern/internal/identity"
)

type Service struct {
	reader *Reader
	client client.Client
}

func NewService(c client.Client) *Service {
	return &Service{reader: NewReader(c), client: c}
}

func (s *Service) ListPolicies(
	ctx context.Context,
	_ *connect.Request[governv1.ListPoliciesRequest],
) (*connect.Response[governv1.ListPoliciesResponse], error) {
	if _, err := s.caller(ctx); err != nil {
		return nil, err
	}

	guardrails, err := s.reader.Policies(ctx)
	if errors.Is(err, ErrKyvernoAbsent) {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	response := &governv1.ListPoliciesResponse{
		Policies:  make([]*governv1.GuardrailPolicy, 0, len(guardrails)),
		Freshness: &governv1.Freshness{ObservedAt: timestamppb.New(time.Now())},
	}
	for _, guardrail := range guardrails {
		response.Policies = append(response.Policies, protoPolicy(guardrail))
	}

	return connect.NewResponse(response), nil
}

func (s *Service) ListViolations(
	ctx context.Context,
	req *connect.Request[governv1.ListViolationsRequest],
) (*connect.Response[governv1.ListViolationsResponse], error) {
	if _, err := s.caller(ctx); err != nil {
		return nil, err
	}

	namespaces, division, err := s.scope(ctx, req.Msg.GetDivision(), req.Msg.GetNamespace())
	if err != nil {
		return nil, err
	}

	findings, err := s.reader.Findings(ctx, namespaces)
	if errors.Is(err, ErrKyvernoAbsent) {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	wanted := req.Msg.GetSeverity()

	response := &governv1.ListViolationsResponse{
		Violations: make([]*governv1.Violation, 0, len(findings)),
		Page:       &governv1.PageInfo{},
		Freshness:  &governv1.Freshness{ObservedAt: timestamppb.New(time.Now())},
	}

	for _, finding := range findings {
		violation := protoViolation(finding, division)
		if wanted != governv1.Violation_SEVERITY_UNSPECIFIED && violation.GetSeverity() != wanted {
			continue
		}
		response.Violations = append(response.Violations, violation)
	}

	return connect.NewResponse(response), nil
}

func (s *Service) GetCompliance(
	ctx context.Context,
	req *connect.Request[governv1.GetComplianceRequest],
) (*connect.Response[governv1.GetComplianceResponse], error) {
	if _, err := s.caller(ctx); err != nil {
		return nil, err
	}

	namespaces, division, err := s.scope(ctx, req.Msg.GetDivision(), "")
	if err != nil {
		return nil, err
	}

	findings, err := s.reader.Findings(ctx, namespaces)
	if errors.Is(err, ErrKyvernoAbsent) {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	guardrails, err := s.reader.Policies(ctx)
	if err != nil && !errors.Is(err, ErrKyvernoAbsent) {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	compliance := &governv1.Compliance{
		Division:   division,
		ObservedAt: timestamppb.New(time.Now()),
	}

	offending := map[string]bool{}

	for _, finding := range findings {
		switch finding.Result {
		case "warn":
			compliance.Warning++
		default:
			compliance.Failing++
		}

		if finding.Severity == "critical" {
			compliance.Critical++
		}

		offending[finding.Namespace+"/"+finding.ResourceKind+"/"+finding.ResourceName] = true
	}

	for _, guardrail := range guardrails {
		if guardrail.Enforcement == "enforce" {
			compliance.PoliciesEnforced++
		}
	}

	failing := len(offending)
	if failing > math.MaxInt32 {
		failing = math.MaxInt32
	}
	compliance.Failing = int32(failing)
	if compliance.Failing == 0 {
		compliance.CompliantRatio = 1
	} else {
		compliance.CompliantRatio = math.Max(0, 1-float64(compliance.Critical)/float64(compliance.Failing))
	}

	return connect.NewResponse(&governv1.GetComplianceResponse{
		Compliance: compliance,
		Freshness:  &governv1.Freshness{ObservedAt: timestamppb.New(time.Now())},
	}), nil
}

func (s *Service) scope(ctx context.Context, divisionName, namespace string) ([]string, string, error) {
	if namespace != "" {
		return []string{namespace}, divisionName, nil
	}
	if divisionName == "" {
		return nil, "", nil
	}

	division := &governv1alpha1.Division{}
	if err := s.client.Get(ctx, types.NamespacedName{Name: divisionName}, division); err != nil {
		return nil, "", connect.NewError(connect.CodeNotFound,
			fmt.Errorf("division %s not found", divisionName))
	}

	return division.Status.Namespaces, division.Name, nil
}

func (s *Service) caller(ctx context.Context) (identity.Actor, error) {
	actor, ok := identity.FromContext(ctx)
	if !ok {
		return identity.Actor{}, connect.NewError(connect.CodeUnauthenticated, errors.New("sign in at /auth/login"))
	}

	return actor, nil
}

func protoPolicy(guardrail Guardrail) *governv1.GuardrailPolicy {
	enforcement := governv1.GuardrailPolicy_ENFORCEMENT_AUDIT
	if guardrail.Enforcement == "enforce" {
		enforcement = governv1.GuardrailPolicy_ENFORCEMENT_ENFORCE
	}

	return &governv1.GuardrailPolicy{
		Name:          guardrail.Name,
		Kind:          guardrail.Kind,
		ClusterScoped: guardrail.ClusterScoped,
		Enforcement:   enforcement,
		Description:   guardrail.Description,
		Categories:    guardrail.Categories,
		Severity:      guardrail.Severity,
		Rules:         guardrail.Rules,
		Ready:         guardrail.Ready,
	}
}

func protoViolation(finding Finding, division string) *governv1.Violation {
	return &governv1.Violation{
		Policy:       finding.Policy,
		Rule:         finding.Rule,
		Namespace:    finding.Namespace,
		Division:     division,
		ResourceKind: finding.ResourceKind,
		ResourceName: finding.ResourceName,
		Severity:     severityOf(finding.Severity),
		Result:       resultOf(finding.Result),
		Message:      finding.Message,
		ObservedAt:   timestamppb.New(finding.ObservedAt),
	}
}

func severityOf(severity string) governv1.Violation_Severity {
	switch severity {
	case "critical":
		return governv1.Violation_SEVERITY_CRITICAL
	case "high":
		return governv1.Violation_SEVERITY_HIGH
	case "low":
		return governv1.Violation_SEVERITY_LOW
	default:
		return governv1.Violation_SEVERITY_MEDIUM
	}
}

func resultOf(result string) governv1.Violation_Result {
	switch result {
	case "warn":
		return governv1.Violation_RESULT_WARN
	case "error":
		return governv1.Violation_RESULT_ERROR
	default:
		return governv1.Violation_RESULT_FAIL
	}
}
