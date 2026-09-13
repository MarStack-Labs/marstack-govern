package cost_test

import (
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/marstack-labs/marstack-govern/internal/cost"
	"github.com/marstack-labs/marstack-govern/internal/db/dbtest"
)

func TestAnInvoiceSurvivesARoundTrip(t *testing.T) {
	pool := dbtest.Migrated(t)
	store := cost.NewStore(pool)
	seedDivision(t, pool)
	seedPolicy(t, pool)

	invoice := generated(t)

	if err := store.SaveInvoice(t.Context(), invoice); err != nil {
		t.Fatalf("save: %v", err)
	}

	stored, err := store.Invoices(t.Context(), "payments", 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("got %d invoices, want 1", len(stored))
	}

	back := stored[0]
	if back.Total.String() != invoice.Total.String() {
		t.Errorf("total: got %s, want %s", back.Total, invoice.Total)
	}
	if len(back.Lines) != len(invoice.Lines) {
		t.Fatalf("lines: got %d, want %d", len(back.Lines), len(invoice.Lines))
	}
	if !back.Reproduces(invoice) {
		t.Fatal("the stored invoice no longer matches the digest it was saved with")
	}
	if back.PolicyName != "standard" || back.PolicyRevision != 1 {
		t.Errorf("policy: got %s@%d", back.PolicyName, back.PolicyRevision)
	}
}

func TestGeneratingTheSameMonthTwiceDoesNotDuplicateIt(t *testing.T) {
	pool := dbtest.Migrated(t)
	store := cost.NewStore(pool)
	seedDivision(t, pool)
	seedPolicy(t, pool)

	invoice := generated(t)

	for range 2 {
		if err := store.SaveInvoice(t.Context(), invoice); err != nil {
			t.Fatalf("save: %v", err)
		}
	}

	stored, err := store.Invoices(t.Context(), "payments", 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("the same month was billed %d times", len(stored))
	}
	if len(stored[0].Lines) != len(invoice.Lines) {
		t.Fatalf("lines were duplicated: got %d, want %d", len(stored[0].Lines), len(invoice.Lines))
	}
}

func TestAnInvoiceThatWasNeverGeneratedIsNotInvented(t *testing.T) {
	pool := dbtest.Migrated(t)
	store := cost.NewStore(pool)

	_, err := store.InvoiceByUID(t.Context(), "00000000-0000-0000-0000-000000000000")
	if !errors.Is(err, cost.ErrInvoiceNotFound) {
		t.Fatalf("got %v, want a plain not-found", err)
	}
}

func generated(t *testing.T) cost.Invoice {
	t.Helper()

	policy := standardPolicy(t)
	period := cost.MonthBefore(billed)

	invoice, err := cost.GenerateInvoice("payments", period, policy,
		sampleCharges(policy), cost.NewMoney("IDR", nil), billed)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	return invoice
}

func seedDivision(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	_, err := pool.Exec(t.Context(), `
		INSERT INTO divisions (
		    uid, name, display_name, phase,
		    quota_cpu_millicores, quota_memory_bytes, quota_storage_bytes, quota_pods,
		    used_cpu_millicores, used_memory_bytes, used_storage_bytes, used_pods,
		    created_at, resource_version, observed_at
		) VALUES ('22222222-2222-2222-2222-222222222222', 'payments', 'Payments', 'active',
		          40000, 0, 0, 0, 0, 0, 0, 0, now(), '1', now())`)
	if err != nil {
		t.Fatalf("seed division: %v", err)
	}
}

func seedPolicy(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	store := cost.NewStore(pool)

	policy := standardPolicy(t)
	policy.ApprovedBy = "finance@marstack.test"
	policy.ApprovedAt = time.Date(2025, 12, 18, 9, 0, 0, 0, time.UTC)

	if err := store.UpsertPolicy(t.Context(), policy, "1"); err != nil {
		t.Fatalf("seed policy: %v", err)
	}
}

func TestAWorkloadThatNoLongerExistsDoesNotDropTheInvoice(t *testing.T) {
	pool := dbtest.Migrated(t)
	store := cost.NewStore(pool)
	seedDivision(t, pool)
	seedPolicy(t, pool)

	invoice := generated(t)
	invoice.Lines[0].WorkloadUID = "99999999-9999-9999-9999-999999999999"
	invoice.Lines[1].WorkloadUID = "not-a-uuid-at-all"

	if err := store.SaveInvoice(t.Context(), invoice); err != nil {
		t.Fatalf("a deleted workload cost the whole invoice: %v", err)
	}

	stored, err := store.Invoices(t.Context(), "payments", 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(stored[0].Lines) != len(invoice.Lines) {
		t.Fatalf("lines: got %d, want %d", len(stored[0].Lines), len(invoice.Lines))
	}
	if stored[0].Lines[0].WorkloadLabel == "" {
		t.Fatal("the charge lost the only name it had left")
	}
}
