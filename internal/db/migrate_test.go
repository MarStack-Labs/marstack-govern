package db

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMigrationsAreOrderedAndUnique(t *testing.T) {
	migrations, err := Migrations()
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	if len(migrations) == 0 {
		t.Fatal("no migrations were embedded")
	}

	seen := map[string]bool{}
	previous := ""
	for _, migration := range migrations {
		if seen[migration.Version] {
			t.Fatalf("duplicate migration version %q", migration.Version)
		}
		if migration.Version <= previous {
			t.Fatalf("migration %q is out of order after %q", migration.Version, previous)
		}
		if len(migration.Checksum) != 32 {
			t.Fatalf("migration %q has a %d byte checksum", migration.Version, len(migration.Checksum))
		}
		seen[migration.Version] = true
		previous = migration.Version
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	pool := testPool(t)
	ctx := t.Context()

	first, err := Migrate(ctx, pool)
	if err != nil {
		t.Fatalf("first migrate: %v", err)
	}

	all, err := Migrations()
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	if len(first) != len(all) {
		t.Fatalf("applied %d migrations, embedded %d", len(first), len(all))
	}

	second, err := Migrate(ctx, pool)
	if err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("second run applied %v, expected nothing", second)
	}
}

func TestMigrateBuildsTheReadModel(t *testing.T) {
	pool := testPool(t)
	ctx := t.Context()

	if _, err := Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	for _, table := range []string{
		"divisions", "namespaces", "division_members",
		"workloads", "workload_containers", "images", "gitops_applications",
		"requests", "decisions",
		"pricing_policies", "cost_samples", "invoices", "invoice_lines",
		"audit_events", "timeline_events", "policy_violations",
	} {
		var exists bool
		err := pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = $1)`,
			table,
		).Scan(&exists)
		if err != nil {
			t.Fatalf("check %s: %v", table, err)
		}
		if !exists {
			t.Errorf("table %s is missing", table)
		}
	}
}

func TestMigrateDetectsAnEditedMigration(t *testing.T) {
	pool := testPool(t)
	ctx := t.Context()

	if _, err := Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	if _, err := pool.Exec(ctx,
		`UPDATE schema_migrations SET checksum = sha256('tampered') WHERE version = (SELECT min(version) FROM schema_migrations)`,
	); err != nil {
		t.Fatalf("tamper: %v", err)
	}

	_, err := Migrate(ctx, pool)
	if !errors.Is(err, ErrMigrationChanged) {
		t.Fatalf("got %v, want ErrMigrationChanged", err)
	}
}

func TestAuditChainRejectsABrokenLink(t *testing.T) {
	pool := testPool(t)
	ctx := t.Context()

	if _, err := Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	insert := `INSERT INTO audit_events (audit_id, event_at, stage, actor, verb, resource, payload, prev_hash, hash)
	           VALUES (gen_random_uuid(), now(), 'ResponseComplete', 'a@example.test', $1, 'quotarequests', '{}'::jsonb, $2, $3)`

	if _, err := pool.Exec(ctx, insert, "create", []byte{}, sha256Of("first")); err != nil {
		t.Fatalf("first event: %v", err)
	}
	if _, err := pool.Exec(ctx, insert, "update", sha256Of("first"), sha256Of("second")); err != nil {
		t.Fatalf("chained event: %v", err)
	}

	if _, err := pool.Exec(ctx, insert, "delete", sha256Of("wrong"), sha256Of("third")); err == nil {
		t.Fatal("a broken chain link was accepted")
	}

	if _, err := pool.Exec(ctx, `UPDATE audit_events SET actor = 'forged'`); err == nil {
		t.Fatal("an update to audit_events was accepted")
	}

	if _, err := pool.Exec(ctx, `DELETE FROM audit_events`); err == nil {
		t.Fatal("a delete from audit_events was accepted")
	}
}

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	dsn := os.Getenv("GOVERN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set GOVERN_TEST_DATABASE_URL to a throwaway database to run migration tests")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	pool, err := Open(ctx, Config{DSN: dsn, MaxConns: 4})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(pool.Close)

	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatalf("reset test schema: %v", err)
	}

	return pool
}

func sha256Of(seed string) []byte {
	sum := sha256.Sum256([]byte(seed))
	return sum[:]
}
