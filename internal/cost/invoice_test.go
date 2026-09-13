package cost_test

import (
	"testing"
	"time"

	"github.com/marstack-labs/marstack-govern/internal/cost"
)

var billed = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

func TestAnInvoiceForAPeriodStillRunningIsRefused(t *testing.T) {
	period := cost.Period{
		Start: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
	}

	_, err := cost.GenerateInvoice("payments", period, standardPolicy(t), nil,
		cost.NewMoney("IDR", nil), time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC))

	if err == nil {
		t.Fatal("a forecast was issued as an invoice")
	}
}

func TestTheSameMonthBilledTwiceProducesTheSameDigest(t *testing.T) {
	period := cost.MonthBefore(billed)
	policy := standardPolicy(t)
	charges := sampleCharges(policy)

	first, err := cost.GenerateInvoice("payments", period, policy, charges,
		cost.NewMoney("IDR", nil), billed)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	second, err := cost.GenerateInvoice("payments", period, policy, sampleCharges(policy),
		cost.NewMoney("IDR", nil), billed.Add(72*time.Hour))
	if err != nil {
		t.Fatalf("generate again: %v", err)
	}

	if !first.Reproduces(second) {
		t.Fatalf("the same inputs produced different digests:\n%x\n%x",
			first.InputsDigest, second.InputsDigest)
	}
}

func TestARepricedMonthDoesNotReproduceTheOldInvoice(t *testing.T) {
	period := cost.MonthBefore(billed)

	cheap := standardPolicy(t)
	dear := standardPolicy(t)
	dear.Revision = 2
	dear.CPUCore = mustMoney(t, "300000")

	first, err := cost.GenerateInvoice("payments", period, cheap, sampleCharges(cheap),
		cost.NewMoney("IDR", nil), billed)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	second, err := cost.GenerateInvoice("payments", period, dear, sampleCharges(dear),
		cost.NewMoney("IDR", nil), billed)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	if first.Reproduces(second) {
		t.Fatal("a rate change left the invoice digest untouched")
	}
	if first.Total.Rat().Cmp(second.Total.Rat()) == 0 {
		t.Fatal("doubling the cpu rate did not change the total")
	}
}

func TestTheInvoiceCitesThePolicyRevisionItUsed(t *testing.T) {
	period := cost.MonthBefore(billed)
	policy := standardPolicy(t)
	policy.Revision = 7

	invoice, err := cost.GenerateInvoice("payments", period, policy, sampleCharges(policy),
		cost.NewMoney("IDR", nil), billed)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	if invoice.PolicyName != "standard" || invoice.PolicyRevision != 7 {
		t.Fatalf("the invoice cannot say what it was priced with: %s@%d",
			invoice.PolicyName, invoice.PolicyRevision)
	}
}

func TestTheLinesAddUpToTheSubtotal(t *testing.T) {
	period := cost.MonthBefore(billed)
	policy := standardPolicy(t)

	invoice, err := cost.GenerateInvoice("payments", period, policy, sampleCharges(policy),
		mustMoney(t, "50000"), billed)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	sum := cost.NewMoney("IDR", nil)
	for _, line := range invoice.Lines {
		sum = sum.Add(line.Amount)
	}

	if sum.Rat().Cmp(invoice.Subtotal.Rat()) != 0 {
		t.Fatalf("lines sum to %s but the subtotal says %s", sum, invoice.Subtotal)
	}

	if invoice.Total.Rat().Cmp(invoice.Subtotal.Add(mustMoney(t, "50000")).Rat()) != 0 {
		t.Fatalf("the unallocated share was dropped: subtotal %s, total %s",
			invoice.Subtotal, invoice.Total)
	}
}

func TestAMonthWithNoPolicyIsNotGuessedAtZero(t *testing.T) {
	period := cost.MonthBefore(billed)

	_, err := cost.GenerateInvoice("payments", period, cost.Policy{}, nil,
		cost.NewMoney("IDR", nil), billed)

	if err == nil {
		t.Fatal("an invoice was issued with no rates behind it")
	}
}

func TestMonthBeforeIsTheWholeMonthThatEnded(t *testing.T) {
	period := cost.MonthBefore(time.Date(2026, 3, 14, 9, 0, 0, 0, time.UTC))

	if period.Start.Format("2006-01-02") != "2026-02-01" {
		t.Errorf("start: got %s", period.Start.Format("2006-01-02"))
	}
	if period.End.Format("2006-01-02") != "2026-03-01" {
		t.Errorf("end: got %s", period.End.Format("2006-01-02"))
	}
	if !period.Over(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)) {
		t.Error("a period ending today is reported as still running")
	}
}

func standardPolicy(t *testing.T) cost.Policy {
	t.Helper()

	return cost.Policy{
		Name:     "standard",
		Revision: 1,
		Currency: "IDR",
		CPUCore:  mustMoney(t, "150000"),
		MemoryGi: mustMoney(t, "25000"),
		From:     time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func sampleCharges(policy cost.Policy) []cost.Chargeable {
	return cost.ChargesFor([]cost.WorkloadRequest{
		{UID: "w-1", Namespace: "payments-dev", Name: "ledger", CPUMillicores: 2000, MemoryBytes: 4 << 30},
		{UID: "w-2", Namespace: "payments-dev", Name: "api", CPUMillicores: 500, MemoryBytes: 1 << 30},
	}, cost.MonthBefore(billed), policy)
}

func mustMoney(t *testing.T, amount string) cost.Money {
	t.Helper()

	money, err := cost.ParseMoney("IDR", amount)
	if err != nil {
		t.Fatalf("parse %s: %v", amount, err)
	}

	return money
}
