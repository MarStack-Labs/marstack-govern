package cost

import (
	"errors"
	"fmt"
	"math/big"
	"time"

	governv1alpha1 "github.com/marstack-labs/marstack-govern/api/v1alpha1"
)

var (
	ErrNoPricingPolicy = errors.New("no pricing policy is effective for this period")
	ErrNoUsageSource   = errors.New("no metrics source is configured, so nothing can be charged")
)

type Resource string

const (
	ResourceCPU          Resource = "cpu"
	ResourceMemory       Resource = "memory"
	ResourceStorage      Resource = "storage"
	ResourceLoadBalancer Resource = "loadbalancer"
)

type Policy struct {
	Name        string
	Revision    int64
	Currency    string
	CPUCore     Money
	MemoryGi    Money
	StorageGi   Money
	LoadBalance Money
	EgressGi    Money
	Unallocated governv1alpha1.UnallocatedStrategy
	ApprovedBy  string
	ApprovedAt  time.Time
	From        time.Time
	To          *time.Time
}

func PolicyFrom(policy *governv1alpha1.PricingPolicy) (Policy, error) {
	currency := policy.Spec.Currency
	if currency == "" {
		currency = "IDR"
	}

	from, err := time.Parse(time.DateOnly, policy.Spec.EffectiveFrom)
	if err != nil {
		return Policy{}, fmt.Errorf("effectiveFrom %q is not a date: %w", policy.Spec.EffectiveFrom, err)
	}

	built := Policy{
		Name:        policy.Name,
		Revision:    policy.Generation,
		Currency:    currency,
		Unallocated: policy.Spec.Unallocated,
		ApprovedBy:  policy.Spec.ApprovedBy,
		ApprovedAt:  policy.Spec.ApprovedAt.Time,
		From:        from,
	}

	if policy.Spec.EffectiveTo != "" {
		to, err := time.Parse(time.DateOnly, policy.Spec.EffectiveTo)
		if err != nil {
			return Policy{}, fmt.Errorf("effectiveTo %q is not a date: %w", policy.Spec.EffectiveTo, err)
		}
		built.To = &to
	}

	rates := []struct {
		value  string
		target *Money
	}{
		{policy.Spec.Rates.CPUCoreMonth, &built.CPUCore},
		{policy.Spec.Rates.MemoryGiMonth, &built.MemoryGi},
		{policy.Spec.Rates.StorageGiMonth, &built.StorageGi},
		{policy.Spec.Rates.LoadBalancerMonth, &built.LoadBalance},
		{policy.Spec.Rates.EgressGi, &built.EgressGi},
	}

	for _, rate := range rates {
		parsed, err := ParseMoney(currency, rate.value)
		if err != nil {
			return Policy{}, err
		}
		*rate.target = parsed
	}

	return built, nil
}

func (p Policy) EffectiveOn(day time.Time) bool {
	if day.Before(p.From) {
		return false
	}
	if p.To != nil && day.After(*p.To) {
		return false
	}

	return true
}

func Effective(policies []Policy, day time.Time) (Policy, error) {
	var chosen Policy
	found := false

	for _, policy := range policies {
		if !policy.EffectiveOn(day) {
			continue
		}
		if !found || policy.From.After(chosen.From) {
			chosen, found = policy, true
		}
	}

	if !found {
		return Policy{}, ErrNoPricingPolicy
	}

	return chosen, nil
}

type Line struct {
	Resource Resource
	Quantity *big.Rat
	Unit     string
	Rate     Money
	Amount   Money
}

type Bill struct {
	Policy    Policy
	Lines     []Line
	Total     Money
	Idle      Money
	Charged   Money
	Projected Money
}

func Charge(policy Policy, usage Usage, elapsed, period time.Duration) Bill {
	hourly := func(monthly Money) Money {
		return monthly.Times(big.NewRat(1, hoursPerMonth))
	}

	lines := []Line{
		line(ResourceCPU, usage.CPUCoreHoursRequested, "core-hours", hourly(policy.CPUCore)),
		line(ResourceMemory, usage.MemoryGiHoursRequested, "GiB-hours", hourly(policy.MemoryGi)),
		line(ResourceStorage, usage.StorageGiHours, "GiB-hours", hourly(policy.StorageGi)),
		line(ResourceLoadBalancer, usage.LoadBalancerHours, "hours", hourly(policy.LoadBalance)),
	}

	total := NewMoney(policy.Currency, nil)
	for _, item := range lines {
		total = total.Add(item.Amount)
	}

	idleCPU := difference(usage.CPUCoreHoursRequested, usage.CPUCoreHoursUsed)
	idleMemory := difference(usage.MemoryGiHoursRequested, usage.MemoryGiHoursUsed)

	idle := hourly(policy.CPUCore).Times(idleCPU).Add(hourly(policy.MemoryGi).Times(idleMemory))

	bill := Bill{
		Policy:  policy,
		Lines:   lines,
		Total:   total,
		Idle:    idle,
		Charged: total,
	}

	bill.Projected = project(total, elapsed, period)

	return bill
}

func line(resource Resource, quantity *big.Rat, unit string, rate Money) Line {
	if quantity == nil {
		quantity = new(big.Rat)
	}

	return Line{
		Resource: resource,
		Quantity: quantity,
		Unit:     unit,
		Rate:     rate,
		Amount:   rate.Times(quantity),
	}
}

func difference(requested, used *big.Rat) *big.Rat {
	if requested == nil {
		return new(big.Rat)
	}
	if used == nil {
		return new(big.Rat).Set(requested)
	}

	delta := new(big.Rat).Sub(requested, used)
	if delta.Sign() < 0 {
		return new(big.Rat)
	}

	return delta
}

func project(total Money, elapsed, period time.Duration) Money {
	if elapsed <= 0 || period <= 0 || elapsed >= period {
		return total
	}

	factor := new(big.Rat).Quo(
		new(big.Rat).SetInt64(int64(period)),
		new(big.Rat).SetInt64(int64(elapsed)),
	)

	return total.Times(factor)
}
