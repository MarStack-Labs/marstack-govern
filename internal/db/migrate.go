package db

import (
	"context"
	"crypto/sha256"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

const migrationLockID = 7_246_181_913_004_211

var ErrMigrationChanged = errors.New("an applied migration has changed on disk")

type Migration struct {
	Version  string
	Checksum []byte
	Body     string
}

func Migrations() ([]Migration, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}

	migrations := make([]Migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}

		body, err := migrationsFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}

		sum := sha256.Sum256(body)
		migrations = append(migrations, Migration{
			Version:  strings.TrimSuffix(entry.Name(), ".sql"),
			Checksum: sum[:],
			Body:     string(body),
		})
	}

	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].Version < migrations[j].Version
	})

	return migrations, nil
}

func Migrate(ctx context.Context, pool *pgxpool.Pool) (applied []string, err error) {
	migrations, err := Migrations()
	if err != nil {
		return nil, err
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLockID); err != nil {
		return nil, fmt.Errorf("take migration lock: %w", err)
	}
	defer func() {
		if _, unlockErr := conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, migrationLockID); unlockErr != nil && err == nil {
			err = fmt.Errorf("release migration lock: %w", unlockErr)
		}
	}()

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
		    version    text PRIMARY KEY,
		    checksum   bytea NOT NULL,
		    applied_at timestamptz NOT NULL DEFAULT now()
		)`); err != nil {
		return nil, fmt.Errorf("create schema_migrations: %w", err)
	}

	recorded, err := recordedChecksums(ctx, conn.Conn())
	if err != nil {
		return nil, err
	}

	for _, migration := range migrations {
		known, seen := recorded[migration.Version]
		if seen {
			if string(known) != string(migration.Checksum) {
				return applied, fmt.Errorf("%w: %s", ErrMigrationChanged, migration.Version)
			}
			continue
		}

		if err := applyOne(ctx, conn.Conn(), migration); err != nil {
			return applied, err
		}
		applied = append(applied, migration.Version)
	}

	return applied, nil
}

func recordedChecksums(ctx context.Context, conn *pgx.Conn) (map[string][]byte, error) {
	rows, err := conn.Query(ctx, `SELECT version, checksum FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	defer rows.Close()

	recorded := map[string][]byte{}
	for rows.Next() {
		var version string
		var checksum []byte
		if err := rows.Scan(&version, &checksum); err != nil {
			return nil, fmt.Errorf("scan schema_migrations: %w", err)
		}
		recorded[version] = checksum
	}

	return recorded, rows.Err()
}

func applyOne(ctx context.Context, conn *pgx.Conn, migration Migration) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin %s: %w", migration.Version, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, migration.Body); err != nil {
		return fmt.Errorf("apply %s: %w", migration.Version, err)
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO schema_migrations (version, checksum) VALUES ($1, $2)`,
		migration.Version, migration.Checksum,
	); err != nil {
		return fmt.Errorf("record %s: %w", migration.Version, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit %s: %w", migration.Version, err)
	}

	return nil
}
