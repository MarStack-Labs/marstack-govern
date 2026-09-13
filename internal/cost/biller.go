package cost

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	governv1alpha1 "github.com/marstack-labs/marstack-govern/api/v1alpha1"
)

type Biller struct {
	Client    client.Client
	Store     InvoiceStore
	Workloads WorkloadReader
	Logger    *slog.Logger
	Interval  time.Duration
	Now       func() time.Time
}

func (b *Biller) Run(ctx context.Context) error {
	interval := b.Interval
	if interval <= 0 {
		interval = time.Hour
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	b.sweep(ctx)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			b.sweep(ctx)
		}
	}
}

func (b *Biller) sweep(ctx context.Context) {
	now := b.now()
	period := MonthBefore(now)

	divisions := &governv1alpha1.DivisionList{}
	if err := b.Client.List(ctx, divisions); err != nil {
		b.log().Error("list divisions for billing", "error", err)

		return
	}

	for i := range divisions.Items {
		division := &divisions.Items[i]

		generated, err := b.Generate(ctx, division, period, now)
		switch {
		case errors.Is(err, ErrAlreadyBilled):
			continue
		case err != nil:
			b.log().Error("generate invoice",
				"division", division.Name, "period", period.String(), "error", err)
		default:
			b.log().Info("invoice generated",
				"division", division.Name, "period", period.String(),
				"total", generated.Total.String(), "lines", len(generated.Lines))
		}
	}
}

var ErrAlreadyBilled = errors.New("an invoice already exists for that period and reproduces the same inputs")

func (b *Biller) Generate(
	ctx context.Context,
	division *governv1alpha1.Division,
	period Period,
	now time.Time,
) (Invoice, error) {
	if b.Store == nil || b.Workloads == nil {
		return Invoice{}, errors.New("the biller has no store or workload reader")
	}

	policy, err := b.policy(ctx, period.End.AddDate(0, 0, -1))
	if err != nil {
		return Invoice{}, err
	}

	requested, err := b.Workloads.RequestedByWorkload(ctx, division.Status.Namespaces)
	if err != nil {
		return Invoice{}, fmt.Errorf("read workload requests: %w", err)
	}

	invoice, err := GenerateInvoice(
		division.Name, period, policy,
		ChargesFor(requested, period, policy),
		NewMoney(policy.Currency, nil),
		now,
	)
	if err != nil {
		return Invoice{}, err
	}

	existing, err := b.existing(ctx, division.Name, period)
	if err == nil && existing.Reproduces(invoice) {
		return existing, ErrAlreadyBilled
	}

	if err := b.Store.SaveInvoice(ctx, invoice); err != nil {
		return Invoice{}, err
	}

	return invoice, nil
}

func (b *Biller) existing(ctx context.Context, division string, period Period) (Invoice, error) {
	stored, err := b.Store.Invoices(ctx, division, 60)
	if err != nil {
		return Invoice{}, err
	}

	for _, invoice := range stored {
		if invoice.PeriodStart.Equal(period.Start) && invoice.PeriodEnd.Equal(period.End) {
			return invoice, nil
		}
	}

	return Invoice{}, ErrInvoiceNotFound
}

func (b *Biller) policy(ctx context.Context, day time.Time) (Policy, error) {
	list := &governv1alpha1.PricingPolicyList{}
	if err := b.Client.List(ctx, list); err != nil {
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

func (b *Biller) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}

	return time.Now()
}

func (b *Biller) log() *slog.Logger {
	if b.Logger != nil {
		return b.Logger
	}

	return slog.Default()
}
