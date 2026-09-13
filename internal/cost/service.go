package cost

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	governv1alpha1 "github.com/marstack-labs/marstack-govern/api/v1alpha1"
	governv1 "github.com/marstack-labs/marstack-govern/gen/marstack/govern/v1"
	"github.com/marstack-labs/marstack-govern/internal/identity"
)

const topConsumers = 10

type WorkloadRequest struct {
	UID           string
	Namespace     string
	Name          string
	CPUMillicores int64
	MemoryBytes   int64
}

type WorkloadReader interface {
	RequestedByWorkload(ctx context.Context, namespaces []string) ([]WorkloadRequest, error)
}

type Service struct {
	reader    client.Client
	usage     *Reader
	workloads WorkloadReader
	invoices  InvoiceStore
	now       func() time.Time
}

func NewService(reader client.Client, usage *Reader, workloads WorkloadReader) *Service {
	return &Service{reader: reader, usage: usage, workloads: workloads, now: time.Now}
}

func (s *Service) WithInvoices(invoices InvoiceStore) *Service {
	s.invoices = invoices

	return s
}

func (s *Service) GetDivisionCost(
	ctx context.Context,
	req *connect.Request[governv1.GetDivisionCostRequest],
) (*connect.Response[governv1.GetDivisionCostResponse], error) {
	if _, err := s.caller(ctx); err != nil {
		return nil, err
	}

	division, err := s.division(ctx, req.Msg.GetDivision())
	if err != nil {
		return nil, err
	}

	start, end, elapsed := monthToDate(s.now())

	policy, err := s.effective(ctx, start)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}

	usage, err := s.usage.Usage(ctx, division.Status.Namespaces, elapsed)
	if err != nil {
		if errors.Is(err, ErrNoUsageSource) {
			return nil, connect.NewError(connect.CodeUnavailable, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	bill := Charge(policy, usage, elapsed, end.Sub(start))

	consumers, err := s.consumers(ctx, division.Status.Namespaces, policy, elapsed)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	cost := &governv1.DivisionCost{
		Division:              division.Name,
		Period:                start.Format("2006-01"),
		Charged:               money(bill.Charged),
		ProjectedMonthEnd:     money(bill.Projected),
		Idle:                  money(bill.Idle),
		PricingPolicy:         policy.Name,
		PricingPolicyRevision: boundedRevision(policy.Revision),
		TopConsumers:          consumers,
	}

	for _, item := range bill.Lines {
		cost.Components = append(cost.Components, &governv1.CostComponent{
			Resource: resourceOf(item.Resource),
			Quantity: item.Quantity.FloatString(2),
			Unit:     item.Unit,
			Rate:     money(item.Rate),
			Amount:   money(item.Amount),
		})
	}

	return connect.NewResponse(&governv1.GetDivisionCostResponse{
		Cost:      cost,
		Freshness: &governv1.Freshness{ObservedAt: timestamppb.New(s.now())},
	}), nil
}

func (s *Service) ListWorkloadCost(
	ctx context.Context,
	req *connect.Request[governv1.ListWorkloadCostRequest],
) (*connect.Response[governv1.ListWorkloadCostResponse], error) {
	if _, err := s.caller(ctx); err != nil {
		return nil, err
	}

	division, err := s.division(ctx, req.Msg.GetDivision())
	if err != nil {
		return nil, err
	}

	start, _, elapsed := monthToDate(s.now())

	policy, err := s.effective(ctx, start)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}

	consumers, err := s.consumers(ctx, division.Status.Namespaces, policy, elapsed)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&governv1.ListWorkloadCostResponse{
		Workloads: consumers,
		Page:      &governv1.PageInfo{},
		Freshness: &governv1.Freshness{ObservedAt: timestamppb.New(s.now())},
	}), nil
}

func (s *Service) GetPricingPolicy(
	ctx context.Context,
	req *connect.Request[governv1.GetPricingPolicyRequest],
) (*connect.Response[governv1.GetPricingPolicyResponse], error) {
	if _, err := s.caller(ctx); err != nil {
		return nil, err
	}

	name := req.Msg.GetName()
	if name == "" {
		policy, err := s.effective(ctx, s.now())
		if err != nil {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}

		return connect.NewResponse(&governv1.GetPricingPolicyResponse{Policy: protoPolicy(policy)}), nil
	}

	stored := &governv1alpha1.PricingPolicy{}
	if err := s.reader.Get(ctx, types.NamespacedName{Name: name}, stored); err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("pricing policy %s not found", name))
	}

	policy, err := PolicyFrom(stored)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&governv1.GetPricingPolicyResponse{Policy: protoPolicy(policy)}), nil
}

func (s *Service) ListInvoices(
	ctx context.Context,
	req *connect.Request[governv1.ListInvoicesRequest],
) (*connect.Response[governv1.ListInvoicesResponse], error) {
	if _, err := s.caller(ctx); err != nil {
		return nil, err
	}
	if s.invoices == nil {
		return nil, connect.NewError(connect.CodeUnavailable,
			errors.New("no read model is attached, so no invoice can be looked up"))
	}

	division, err := s.division(ctx, req.Msg.GetDivision())
	if err != nil {
		return nil, err
	}

	stored, err := s.invoices.Invoices(ctx, division.Name, int(req.Msg.GetPage().GetSize()))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	response := &governv1.ListInvoicesResponse{
		Invoices: make([]*governv1.Invoice, 0, len(stored)),
	}
	for _, invoice := range stored {
		response.Invoices = append(response.Invoices, protoInvoice(invoice))
	}

	return connect.NewResponse(response), nil
}

func (s *Service) GetInvoice(
	ctx context.Context,
	req *connect.Request[governv1.GetInvoiceRequest],
) (*connect.Response[governv1.GetInvoiceResponse], error) {
	if _, err := s.caller(ctx); err != nil {
		return nil, err
	}
	if s.invoices == nil {
		return nil, connect.NewError(connect.CodeUnavailable,
			errors.New("no read model is attached, so no invoice can be looked up"))
	}

	invoice, err := s.invoices.InvoiceByUID(ctx, req.Msg.GetUid())
	if errors.Is(err, ErrInvoiceNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&governv1.GetInvoiceResponse{Invoice: protoInvoice(invoice)}), nil
}

func (s *Service) ListRightSizing(
	ctx context.Context,
	req *connect.Request[governv1.ListRightSizingRequest],
) (*connect.Response[governv1.ListRightSizingResponse], error) {
	if _, err := s.caller(ctx); err != nil {
		return nil, err
	}

	division, err := s.division(ctx, req.Msg.GetDivision())
	if err != nil {
		return nil, err
	}

	if s.usage == nil || !s.usage.Available() {
		return nil, connect.NewError(connect.CodeUnavailable,
			errors.New("no metrics source is configured, so no request can be questioned"))
	}
	if s.workloads == nil {
		return nil, connect.NewError(connect.CodeUnavailable,
			errors.New("no workload read model is attached"))
	}

	observed, err := s.usage.ObservedByWorkload(ctx, division.Status.Namespaces, RightSizingWindow)
	if errors.Is(err, ErrNoWorkloadUsage) || errors.Is(err, ErrNoUsageSource) {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	requested, err := s.workloads.RequestedByWorkload(ctx, division.Status.Namespaces)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	policy, err := s.effective(ctx, s.now())
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}

	proposals := Propose(requested, observed, policy)

	response := &governv1.ListRightSizingResponse{
		Recommendations: make([]*governv1.RightSizing, 0, len(proposals)),
		Freshness:       &governv1.Freshness{ObservedAt: timestamppb.New(s.now())},
	}
	for _, proposal := range proposals {
		response.Recommendations = append(response.Recommendations, &governv1.RightSizing{
			WorkloadUid: proposal.UID,
			Namespace:   proposal.Namespace,
			Name:        proposal.Name,
			Container:   proposal.Name,
			Requested: &governv1.Compute{
				CpuMillicores: proposal.CPUMillicores,
				MemoryBytes:   proposal.MemoryBytes,
			},
			ObservedP95: &governv1.Compute{
				CpuMillicores: proposal.ObservedCPUMillicores,
				MemoryBytes:   proposal.ObservedMemoryBytes,
			},
			Proposed: &governv1.Compute{
				CpuMillicores: proposal.ProposedCPUMillicores,
				MemoryBytes:   proposal.ProposedMemoryBytes,
			},
			MonthlySaving: money(proposal.MonthlySaving),
			Patch:         proposal.Patch(),
		})
	}

	return connect.NewResponse(response), nil
}

func protoInvoice(invoice Invoice) *governv1.Invoice {
	out := &governv1.Invoice{
		Uid:                   invoice.UID,
		Division:              invoice.Division,
		PeriodStart:           invoice.PeriodStart.Format("2006-01-02"),
		PeriodEnd:             invoice.PeriodEnd.Format("2006-01-02"),
		PricingPolicy:         invoice.PolicyName,
		PricingPolicyRevision: boundedRevision(invoice.PolicyRevision),
		Subtotal:              money(invoice.Subtotal),
		UnallocatedShare:      money(invoice.UnallocatedShare),
		Total:                 money(invoice.Total),
		InputsDigest:          hex.EncodeToString(invoice.InputsDigest),
		GeneratedAt:           timestamppb.New(invoice.GeneratedAt),
		Lines:                 make([]*governv1.InvoiceLine, 0, len(invoice.Lines)),
	}

	for _, line := range invoice.Lines {
		number := line.Number
		if number > math.MaxInt32 {
			number = math.MaxInt32
		}

		out.Lines = append(out.Lines, &governv1.InvoiceLine{
			LineNo:        int32(number),
			WorkloadLabel: line.WorkloadLabel,
			Resource:      resourceOf(line.Resource),
			Quantity:      line.Quantity.FloatString(4),
			Unit:          line.Unit,
			Rate:          money(line.Rate),
			Amount:        money(line.Amount),
		})
	}

	return out
}

func (s *Service) consumers(
	ctx context.Context,
	namespaces []string,
	policy Policy,
	elapsed time.Duration,
) ([]*governv1.WorkloadCost, error) {
	if s.workloads == nil {
		return nil, nil
	}

	requested, err := s.workloads.RequestedByWorkload(ctx, namespaces)
	if err != nil {
		return nil, err
	}

	hours := new(big.Rat).SetFloat64(elapsed.Hours())
	if hours == nil {
		hours = new(big.Rat)
	}

	hourlyCPU := policy.CPUCore.Times(big.NewRat(1, hoursPerMonth))
	hourlyMemory := policy.MemoryGi.Times(big.NewRat(1, hoursPerMonth))

	out := make([]*governv1.WorkloadCost, 0, len(requested))
	for _, workload := range requested {
		cores := new(big.Rat).Mul(Ratio(workload.CPUMillicores, 1000), hours)
		gibibytes := new(big.Rat).Mul(Ratio(workload.MemoryBytes, gibibyte), hours)

		charged := hourlyCPU.Times(cores).Add(hourlyMemory.Times(gibibytes))

		out = append(out, &governv1.WorkloadCost{
			WorkloadUid: workload.UID,
			Namespace:   workload.Namespace,
			Name:        workload.Name,
			Charged:     money(charged),
		})
	}

	sort.SliceStable(out, func(i, j int) bool {
		left, _ := new(big.Rat).SetString(out[i].GetCharged().GetAmount())
		right, _ := new(big.Rat).SetString(out[j].GetCharged().GetAmount())
		if left == nil || right == nil {
			return false
		}
		return left.Cmp(right) > 0
	})

	if len(out) > topConsumers {
		out = out[:topConsumers]
	}

	return out, nil
}

func (s *Service) effective(ctx context.Context, day time.Time) (Policy, error) {
	list := &governv1alpha1.PricingPolicyList{}
	if err := s.reader.List(ctx, list); err != nil {
		return Policy{}, fmt.Errorf("list pricing policies: %w", err)
	}

	policies := make([]Policy, 0, len(list.Items))
	for i := range list.Items {
		policy, err := PolicyFrom(&list.Items[i])
		if err != nil {
			continue
		}
		policies = append(policies, policy)
	}

	return Effective(policies, day)
}

func (s *Service) division(ctx context.Context, name string) (*governv1alpha1.Division, error) {
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("name the division explicitly"))
	}

	division := &governv1alpha1.Division{}
	if err := s.reader.Get(ctx, types.NamespacedName{Name: name}, division); err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("division %s not found", name))
	}

	return division, nil
}

func (s *Service) caller(ctx context.Context) (identity.Actor, error) {
	actor, ok := identity.FromContext(ctx)
	if !ok {
		return identity.Actor{}, connect.NewError(connect.CodeUnauthenticated, errors.New("sign in at /auth/login"))
	}

	return actor, nil
}

func monthToDate(now time.Time) (start, end time.Time, elapsed time.Duration) {
	start = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	end = start.AddDate(0, 1, 0)
	elapsed = now.Sub(start)

	if elapsed <= 0 {
		elapsed = time.Hour
	}

	return start, end, elapsed
}

func money(amount Money) *governv1.Money {
	return &governv1.Money{Currency: amount.Currency(), Amount: amount.String()}
}

func protoPolicy(policy Policy) *governv1.PricingPolicy {
	out := &governv1.PricingPolicy{
		Name:                policy.Name,
		Revision:            boundedRevision(policy.Revision),
		EffectiveFrom:       policy.From.Format(time.DateOnly),
		Currency:            policy.Currency,
		CpuCoreMonth:        money(policy.CPUCore),
		MemoryGbMonth:       money(policy.MemoryGi),
		StorageGbMonth:      money(policy.StorageGi),
		LoadbalancerMonth:   money(policy.LoadBalance),
		EgressGb:            money(policy.EgressGi),
		ApprovedBy:          policy.ApprovedBy,
		ApprovedAt:          timestamppb.New(policy.ApprovedAt),
		UnallocatedStrategy: governv1.PricingPolicy_UNALLOCATED_STRATEGY_PLATFORM,
	}

	if policy.Unallocated == governv1alpha1.UnallocatedProRata {
		out.UnallocatedStrategy = governv1.PricingPolicy_UNALLOCATED_STRATEGY_PRO_RATA
	}
	if policy.To != nil {
		out.EffectiveTo = policy.To.Format(time.DateOnly)
	}

	return out
}

func resourceOf(resource Resource) governv1.CostComponent_Resource {
	switch resource {
	case ResourceCPU:
		return governv1.CostComponent_RESOURCE_CPU
	case ResourceMemory:
		return governv1.CostComponent_RESOURCE_MEMORY
	case ResourceStorage:
		return governv1.CostComponent_RESOURCE_STORAGE
	case ResourceLoadBalancer:
		return governv1.CostComponent_RESOURCE_LOADBALANCER
	default:
		return governv1.CostComponent_RESOURCE_UNSPECIFIED
	}
}

func boundedRevision(revision int64) int32 {
	if revision > 2147483647 {
		return 2147483647
	}
	if revision < 0 {
		return 0
	}

	return int32(revision)
}
