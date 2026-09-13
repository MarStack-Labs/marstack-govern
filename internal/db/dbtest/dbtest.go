package dbtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/marstack-labs/marstack-govern/internal/db"
)

const envDSN = "GOVERN_TEST_DATABASE_URL"

var unsafeInName = regexp.MustCompile(`[^a-z0-9_]+`)

func Pool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	dsn := os.Getenv(envDSN)
	if dsn == "" {
		t.Skipf("set %s to a throwaway database to run tests that need PostgreSQL", envDSN)
	}

	schema := schemaName(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	admin, err := db.Open(ctx, db.Config{DSN: dsn, MaxConns: 2})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	defer admin.Close()

	if _, err := admin.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatalf("create schema %s: %v", schema, err)
	}

	scoped, err := withSearchPath(dsn, schema)
	if err != nil {
		t.Fatalf("scope dsn to %s: %v", schema, err)
	}

	pool, err := db.Open(ctx, db.Config{DSN: scoped, MaxConns: 8})
	if err != nil {
		t.Fatalf("open scoped pool: %v", err)
	}

	t.Cleanup(func() {
		pool.Close()

		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancelCleanup()

		dropper, err := db.Open(cleanupCtx, db.Config{DSN: dsn, MaxConns: 1})
		if err != nil {
			t.Logf("drop schema %s: %v", schema, err)
			return
		}
		defer dropper.Close()

		if _, err := dropper.Exec(cleanupCtx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`); err != nil {
			t.Logf("drop schema %s: %v", schema, err)
		}
	})

	return pool
}

func Migrated(t *testing.T) *pgxpool.Pool {
	t.Helper()

	pool := Pool(t)

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate test schema: %v", err)
	}

	return pool
}

func schemaName(t *testing.T) string {
	t.Helper()

	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatalf("generate schema suffix: %v", err)
	}

	name := unsafeInName.ReplaceAllString(strings.ToLower(t.Name()), "_")
	if len(name) > 40 {
		name = name[:40]
	}

	return fmt.Sprintf("govern_%s_%s", name, hex.EncodeToString(suffix))
}

func withSearchPath(dsn, schema string) (string, error) {
	parsed, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("parse dsn: %w", err)
	}

	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()

	return parsed.String(), nil
}
