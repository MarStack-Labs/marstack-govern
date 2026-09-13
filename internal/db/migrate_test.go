package db_test

import (
	"crypto/sha256"
	"errors"
	"testing"

	"github.com/marstack-labs/marstack-govern/internal/db"
	"github.com/marstack-labs/marstack-govern/internal/db/dbtest"
)

func TestMigrationsAreOrderedAndUnique(t *testing.T) {
	migrations, err := db.Migrations()
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
	pool := dbtest.Pool(t)
	ctx := t.Context()

	first, err := db.Migrate(ctx, pool)
	if err != nil {
		t.Fatalf("first migrate: %v", err)
	}

	all, err := db.Migrations()
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	if len(first) != len(all) {
		t.Fatalf("applied %d migrations, embedded %d", len(first), len(all))
	}

	second, err := db.Migrate(ctx, pool)
	if err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("second run applied %v, expected nothing", second)
	}
}

func TestMigrateBuildsTheReadModel(t *testing.T) {
	pool := dbtest.Migrated(t)
	ctx := t.Context()

	for _, table := range []string{
		"divisions", "namespaces", "division_members",
		"workloads", "workload_containers", "images", "gitops_applications",
		"requests", "decisions",
		"pricing_policies", "cost_samples", "invoices", "invoice_lines",
		"audit_events", "timeline_events", "policy_violations",
	} {
		var found *string
		if err := pool.QueryRow(ctx, `SELECT to_regclass($1)::text`, table).Scan(&found); err != nil {
			t.Fatalf("check %s: %v", table, err)
		}
		if found == nil {
			t.Errorf("table %s is missing", table)
		}
	}
}

func TestMigrateDetectsAnEditedMigration(t *testing.T) {
	pool := dbtest.Migrated(t)
	ctx := t.Context()

	if _, err := pool.Exec(ctx,
		`UPDATE schema_migrations SET checksum = sha256('tampered') WHERE version = (SELECT min(version) FROM schema_migrations)`,
	); err != nil {
		t.Fatalf("tamper: %v", err)
	}

	_, err := db.Migrate(ctx, pool)
	if !errors.Is(err, db.ErrMigrationChanged) {
		t.Fatalf("got %v, want ErrMigrationChanged", err)
	}
}

func TestAuditChainRejectsABrokenLink(t *testing.T) {
	pool := dbtest.Migrated(t)
	ctx := t.Context()

	insert := `INSERT INTO audit_events (audit_id, event_at, stage, actor, verb, resource, payload, prev_hash, hash, canonical)
	           VALUES (gen_random_uuid()::text, now(), 'ResponseComplete', 'a@example.test', $1, 'quotarequests', '{}'::jsonb, $2, $3, '{}'::bytea)`

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

func sha256Of(seed string) []byte {
	sum := sha256.Sum256([]byte(seed))
	return sum[:]
}
