package tenancy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("division not found")

type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

type Division struct {
	UID             string
	Name            string
	DisplayName     string
	Phase           string
	QuotaBackend    string
	QuotaCPU        int64
	QuotaMemory     int64
	QuotaStorage    int64
	QuotaPods       int32
	UsedCPU         int64
	UsedMemory      int64
	UsedStorage     int64
	UsedPods        int32
	Namespaces      []string
	MemberCount     int32
	CreatedAt       time.Time
	ResourceVersion string
	ObservedAt      time.Time
}

type Grant struct {
	Role  string
	Group string
}

type Access struct {
	Division    string
	DisplayName string
	Grants      []Grant
	Namespaces  []string
}

type Namespace struct {
	Name               string
	Division           string
	Environment        string
	Phase              string
	DefaultDenyPresent bool
	LimitRangePresent  bool
	RoleBindings       int32
	CreatedAt          time.Time
	ObservedAt         time.Time
}

func (s *Store) UpsertDivision(ctx context.Context, division Division, namespaces []Namespace, grants []Grant) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin division upsert: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		INSERT INTO divisions (
		    uid, name, display_name, tenant_name, phase, quota_backend,
		    quota_cpu_millicores, quota_memory_bytes, quota_storage_bytes, quota_pods,
		    used_cpu_millicores, used_memory_bytes, used_storage_bytes, used_pods,
		    created_at, resource_version, observed_at
		) VALUES ($1, $2, $3, NULL, $4, nullif($5, ''), $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, now())
		ON CONFLICT (name) DO UPDATE SET
		    uid                  = excluded.uid,
		    display_name         = excluded.display_name,
		    phase                = excluded.phase,
		    quota_backend        = excluded.quota_backend,
		    quota_cpu_millicores = excluded.quota_cpu_millicores,
		    quota_memory_bytes   = excluded.quota_memory_bytes,
		    quota_storage_bytes  = excluded.quota_storage_bytes,
		    quota_pods           = excluded.quota_pods,
		    used_cpu_millicores  = excluded.used_cpu_millicores,
		    used_memory_bytes    = excluded.used_memory_bytes,
		    used_storage_bytes   = excluded.used_storage_bytes,
		    used_pods            = excluded.used_pods,
		    resource_version     = excluded.resource_version,
		    observed_at          = now()`,
		division.UID, division.Name, division.DisplayName, division.Phase, division.QuotaBackend,
		division.QuotaCPU, division.QuotaMemory, division.QuotaStorage, division.QuotaPods,
		division.UsedCPU, division.UsedMemory, division.UsedStorage, division.UsedPods,
		division.CreatedAt, division.ResourceVersion,
	); err != nil {
		return fmt.Errorf("upsert division %s: %w", division.Name, err)
	}

	names := make([]string, 0, len(namespaces))
	for _, namespace := range namespaces {
		names = append(names, namespace.Name)

		if _, err := tx.Exec(ctx, `
			INSERT INTO namespaces (
			    name, division_uid, environment, phase,
			    default_deny_present, limit_range_present, role_bindings,
			    created_at, resource_version, observed_at
			) VALUES ($1, $2, nullif($3, ''), $4, $5, $6, $7, $8, '', now())
			ON CONFLICT (name) DO UPDATE SET
			    division_uid         = excluded.division_uid,
			    environment          = excluded.environment,
			    phase                = excluded.phase,
			    default_deny_present = excluded.default_deny_present,
			    limit_range_present  = excluded.limit_range_present,
			    role_bindings        = excluded.role_bindings,
			    observed_at          = now()`,
			namespace.Name, division.UID, namespace.Environment, namespace.Phase,
			namespace.DefaultDenyPresent, namespace.LimitRangePresent, namespace.RoleBindings,
			namespace.CreatedAt,
		); err != nil {
			return fmt.Errorf("upsert namespace %s: %w", namespace.Name, err)
		}
	}

	if _, err := tx.Exec(ctx,
		`DELETE FROM namespaces WHERE division_uid = $1 AND name <> ALL($2)`,
		division.UID, names,
	); err != nil {
		return fmt.Errorf("prune namespaces of %s: %w", division.Name, err)
	}

	if _, err := tx.Exec(ctx, `DELETE FROM division_grants WHERE division_uid = $1`, division.UID); err != nil {
		return fmt.Errorf("clear grants of %s: %w", division.Name, err)
	}

	for _, grant := range grants {
		if _, err := tx.Exec(ctx,
			`INSERT INTO division_grants (division_uid, role, group_claim) VALUES ($1, $2, $3)
			 ON CONFLICT DO NOTHING`,
			division.UID, grant.Role, grant.Group,
		); err != nil {
			return fmt.Errorf("record grant %s/%s of %s: %w", grant.Role, grant.Group, division.Name, err)
		}
	}

	return tx.Commit(ctx)
}

func (s *Store) DeleteDivision(ctx context.Context, uid string) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM divisions WHERE uid = $1`, uid); err != nil {
		return fmt.Errorf("delete division %s: %w", uid, err)
	}

	return nil
}

func (s *Store) DeleteDivisionByName(ctx context.Context, name string) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM divisions WHERE name = $1`, name); err != nil {
		return fmt.Errorf("delete division %s: %w", name, err)
	}

	return nil
}

func (s *Store) ListDivisions(ctx context.Context, names []string) ([]Division, error) {
	query := selectDivisions
	args := []any{}

	if names != nil {
		query += ` WHERE d.name = ANY($1)`
		args = append(args, names)
	}

	query += ` GROUP BY d.uid ORDER BY d.name`

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list divisions: %w", err)
	}

	return scanDivisions(rows)
}

func (s *Store) GetDivision(ctx context.Context, name string) (Division, error) {
	rows, err := s.pool.Query(ctx, selectDivisions+` WHERE d.name = $1 GROUP BY d.uid`, name)
	if err != nil {
		return Division{}, fmt.Errorf("get division %s: %w", name, err)
	}

	divisions, err := scanDivisions(rows)
	if err != nil {
		return Division{}, err
	}
	if len(divisions) == 0 {
		return Division{}, ErrNotFound
	}

	return divisions[0], nil
}

func (s *Store) ListNamespaces(ctx context.Context, division string) ([]Namespace, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT n.name, coalesce(d.name, ''), coalesce(n.environment, ''), n.phase,
		       n.default_deny_present, n.limit_range_present, n.role_bindings,
		       n.created_at, n.observed_at
		FROM namespaces n
		LEFT JOIN divisions d ON d.uid = n.division_uid
		WHERE $1 = '' OR d.name = $1
		ORDER BY n.name`, division)
	if err != nil {
		return nil, fmt.Errorf("list namespaces: %w", err)
	}
	defer rows.Close()

	namespaces := []Namespace{}
	for rows.Next() {
		var n Namespace
		if err := rows.Scan(
			&n.Name, &n.Division, &n.Environment, &n.Phase,
			&n.DefaultDenyPresent, &n.LimitRangePresent, &n.RoleBindings,
			&n.CreatedAt, &n.ObservedAt,
		); err != nil {
			return nil, fmt.Errorf("scan namespace: %w", err)
		}
		namespaces = append(namespaces, n)
	}

	return namespaces, rows.Err()
}

const selectDivisions = `
	SELECT d.uid, d.name, d.display_name, d.phase, coalesce(d.quota_backend, ''),
	       coalesce(d.quota_cpu_millicores, 0), coalesce(d.quota_memory_bytes, 0),
	       coalesce(d.quota_storage_bytes, 0), coalesce(d.quota_pods, 0),
	       coalesce(d.used_cpu_millicores, 0), coalesce(d.used_memory_bytes, 0),
	       coalesce(d.used_storage_bytes, 0), coalesce(d.used_pods, 0),
	       d.created_at, d.resource_version, d.observed_at,
	       coalesce(array_agg(n.name ORDER BY n.name) FILTER (WHERE n.name IS NOT NULL), '{}') AS namespaces
	FROM divisions d
	LEFT JOIN namespaces n ON n.division_uid = d.uid`

func scanDivisions(rows pgx.Rows) ([]Division, error) {
	defer rows.Close()

	divisions := []Division{}
	for rows.Next() {
		var d Division
		if err := rows.Scan(
			&d.UID, &d.Name, &d.DisplayName, &d.Phase, &d.QuotaBackend,
			&d.QuotaCPU, &d.QuotaMemory, &d.QuotaStorage, &d.QuotaPods,
			&d.UsedCPU, &d.UsedMemory, &d.UsedStorage, &d.UsedPods,
			&d.CreatedAt, &d.ResourceVersion, &d.ObservedAt, &d.Namespaces,
		); err != nil {
			return nil, fmt.Errorf("scan division: %w", err)
		}
		divisions = append(divisions, d)
	}

	return divisions, rows.Err()
}

func (s *Store) ListAccess(ctx context.Context) ([]Access, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT d.name, d.display_name,
		       coalesce(array_agg(DISTINCT g.role || ':' || g.group_claim)
		                FILTER (WHERE g.role IS NOT NULL), '{}'),
		       coalesce(array_agg(DISTINCT n.name) FILTER (WHERE n.name IS NOT NULL), '{}')
		FROM divisions d
		LEFT JOIN division_grants g ON g.division_uid = d.uid
		LEFT JOIN namespaces n ON n.division_uid = d.uid
		GROUP BY d.uid
		ORDER BY d.name`)
	if err != nil {
		return nil, fmt.Errorf("list division access: %w", err)
	}
	defer rows.Close()

	access := []Access{}
	for rows.Next() {
		var entry Access
		var pairs []string

		if err := rows.Scan(&entry.Division, &entry.DisplayName, &pairs, &entry.Namespaces); err != nil {
			return nil, fmt.Errorf("scan division access: %w", err)
		}

		for _, pair := range pairs {
			role, group, found := strings.Cut(pair, ":")
			if !found {
				continue
			}
			entry.Grants = append(entry.Grants, Grant{Role: role, Group: group})
		}

		access = append(access, entry)
	}

	return access, rows.Err()
}
