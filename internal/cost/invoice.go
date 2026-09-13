package cost

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"time"
)

var (
	ErrPeriodNotOver = errors.New("the period has not ended yet, so an invoice would be a forecast")
	ErrNoPolicy      = errors.New("no pricing policy was in force for that period, so nothing can be charged")
)

type Invoice struct {
	UID              string
	Division         string
	PeriodStart      time.Time
	PeriodEnd        time.Time
	PolicyName       string
	PolicyRevision   int64
	Currency         string
	Subtotal         Money
	UnallocatedShare Money
	Total            Money
	Lines            []InvoiceItem
	InputsDigest     []byte
	GeneratedAt      time.Time
}

type InvoiceItem struct {
	Number        int
	WorkloadUID   string
	WorkloadLabel string
	Resource      Resource
	Quantity      *big.Rat
	Unit          string
	Rate          Money
	Amount        Money
}

type Period struct {
	Start time.Time
	End   time.Time
}

func MonthBefore(now time.Time) Period {
	firstOfThis := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)

	return Period{Start: firstOfThis.AddDate(0, -1, 0), End: firstOfThis}
}

func (p Period) Over(now time.Time) bool {
	return !now.Before(p.End)
}

func (p Period) Hours() *big.Rat {
	return new(big.Rat).SetFloat64(p.End.Sub(p.Start).Hours())
}

func (p Period) String() string {
	return p.Start.Format("2006-01-02") + "/" + p.End.Format("2006-01-02")
}

type Chargeable struct {
	WorkloadUID   string
	WorkloadLabel string
	Resource      Resource
	Quantity      *big.Rat
	Unit          string
	Rate          Money
}

func GenerateInvoice(
	division string,
	period Period,
	policy Policy,
	charges []Chargeable,
	unallocated Money,
	now time.Time,
) (Invoice, error) {
	if !period.Over(now) {
		return Invoice{}, fmt.Errorf("%s: %w", period, ErrPeriodNotOver)
	}
	if policy.Name == "" {
		return Invoice{}, fmt.Errorf("%s: %w", period, ErrNoPolicy)
	}

	sort.SliceStable(charges, func(i, j int) bool {
		if charges[i].WorkloadLabel != charges[j].WorkloadLabel {
			return charges[i].WorkloadLabel < charges[j].WorkloadLabel
		}
		return charges[i].Resource < charges[j].Resource
	})

	subtotal := NewMoney(policy.Currency, nil)
	lines := make([]InvoiceItem, 0, len(charges))

	for i, charge := range charges {
		amount := charge.Rate.Times(charge.Quantity)
		subtotal = subtotal.Add(amount)

		lines = append(lines, InvoiceItem{
			Number:        i + 1,
			WorkloadUID:   charge.WorkloadUID,
			WorkloadLabel: charge.WorkloadLabel,
			Resource:      charge.Resource,
			Quantity:      charge.Quantity,
			Unit:          charge.Unit,
			Rate:          charge.Rate,
			Amount:        amount,
		})
	}

	invoice := Invoice{
		Division:         division,
		PeriodStart:      period.Start,
		PeriodEnd:        period.End,
		PolicyName:       policy.Name,
		PolicyRevision:   policy.Revision,
		Currency:         policy.Currency,
		Subtotal:         subtotal,
		UnallocatedShare: unallocated,
		Total:            subtotal.Add(unallocated),
		Lines:            lines,
		GeneratedAt:      now,
	}
	invoice.InputsDigest = invoice.digest()

	return invoice, nil
}

func (i Invoice) digest() []byte {
	hash := sha256.New()

	_, _ = fmt.Fprintf(hash, "division=%s\n", i.Division)
	_, _ = fmt.Fprintf(hash, "period=%s/%s\n",
		i.PeriodStart.Format(time.RFC3339), i.PeriodEnd.Format(time.RFC3339))
	_, _ = fmt.Fprintf(hash, "policy=%s@%d\n", i.PolicyName, i.PolicyRevision)
	_, _ = fmt.Fprintf(hash, "currency=%s\n", i.Currency)

	for _, line := range i.Lines {
		_, _ = fmt.Fprintf(hash, "line=%d|%s|%s|%s|%s|%s|%s\n",
			line.Number, line.WorkloadLabel, line.Resource,
			line.Quantity.RatString(), line.Unit, line.Rate.String(), line.Amount.String())
	}

	_, _ = fmt.Fprintf(hash, "unallocated=%s\n", i.UnallocatedShare.String())
	_, _ = fmt.Fprintf(hash, "total=%s\n", i.Total.String())

	return hash.Sum(nil)
}

func (i Invoice) Reproduces(other Invoice) bool {
	if len(i.InputsDigest) == 0 || len(other.InputsDigest) == 0 {
		return false
	}

	return string(i.InputsDigest) == string(other.InputsDigest)
}

func ChargesFor(requested []WorkloadRequest, period Period, policy Policy) []Chargeable {
	hours := period.Hours()
	hourlyCPU := policy.CPUCore.Times(big.NewRat(1, hoursPerMonth))
	hourlyMemory := policy.MemoryGi.Times(big.NewRat(1, hoursPerMonth))

	charges := make([]Chargeable, 0, len(requested)*2)

	for _, workload := range requested {
		label := workload.Namespace + "/" + workload.Name

		if workload.CPUMillicores > 0 {
			charges = append(charges, Chargeable{
				WorkloadUID:   workload.UID,
				WorkloadLabel: label,
				Resource:      ResourceCPU,
				Quantity:      new(big.Rat).Mul(Ratio(workload.CPUMillicores, 1000), hours),
				Unit:          "core-hour",
				Rate:          hourlyCPU,
			})
		}

		if workload.MemoryBytes > 0 {
			charges = append(charges, Chargeable{
				WorkloadUID:   workload.UID,
				WorkloadLabel: label,
				Resource:      ResourceMemory,
				Quantity:      new(big.Rat).Mul(Ratio(workload.MemoryBytes, gibibyte), hours),
				Unit:          "gibibyte-hour",
				Rate:          hourlyMemory,
			})
		}
	}

	return charges
}

type InvoiceStore interface {
	SaveInvoice(ctx context.Context, invoice Invoice) error
	InvoiceByUID(ctx context.Context, uid string) (Invoice, error)
	Invoices(ctx context.Context, division string, limit int) ([]Invoice, error)
}
