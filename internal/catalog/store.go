package catalog

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/marstack-labs/marstack-govern/internal/kube"
)

const SourceKubernetes = "kubernetes"

var ErrNotFound = errors.New("workload not found")

type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

type Workload struct {
	UID             string
	Division        string
	Namespace       string
	Name            string
	Kind            string
	ImageRef        string
	ReplicasDesired int32
	ReplicasReady   int32
	Health          string
	CPURequest      int64
	MemoryRequest   int64
	CreatedAt       time.Time
	ResourceVersion string
	ObservedAt      time.Time
}

type Filter struct {
	Division  string
	Namespace string
	Health    string
	Search    string
	Limit     int32
	Cursor    string
}

type Freshness struct {
	ObservedAt      time.Time
	ResourceVersion string
	LagSeconds      int64
	Degraded        []DegradedSource
}

type DegradedSource struct {
	Source        string
	Reason        string
	LastHealthyAt time.Time
}

func (s *Store) UpsertWorkload(ctx context.Context, workload kube.Workload) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin upsert: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`DELETE FROM workloads WHERE namespace = $1 AND kind = $2 AND name = $3 AND uid <> $4`,
		workload.Namespace, workload.Kind, workload.Name, workload.UID,
	); err != nil {
		return fmt.Errorf("evict replaced workload %s/%s: %w", workload.Namespace, workload.Name, err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO workloads (
		    uid, division_name, namespace, name, kind, api_version,
		    image_ref, replicas_desired, replicas_ready, health,
		    created_at, resource_version, observed_at
		) VALUES ($1, nullif($2, ''), $3, $4, $5, $6, nullif($7, ''), $8, $9, $10, $11, $12, now())
		ON CONFLICT (uid) DO UPDATE SET
		    division_name    = excluded.division_name,
		    namespace        = excluded.namespace,
		    name             = excluded.name,
		    kind             = excluded.kind,
		    api_version      = excluded.api_version,
		    image_ref        = excluded.image_ref,
		    replicas_desired = excluded.replicas_desired,
		    replicas_ready   = excluded.replicas_ready,
		    health           = excluded.health,
		    resource_version = excluded.resource_version,
		    observed_at      = now()`,
		workload.UID, workload.Division, workload.Namespace, workload.Name, workload.Kind, workload.APIVersion,
		workload.PrimaryImage(), workload.ReplicasDesired, workload.ReplicasReady, string(workload.Health),
		workload.CreatedAt, workload.ResourceVersion,
	); err != nil {
		return fmt.Errorf("upsert workload %s/%s: %w", workload.Namespace, workload.Name, err)
	}

	if _, err := tx.Exec(ctx, `DELETE FROM workload_containers WHERE workload_uid = $1`, workload.UID); err != nil {
		return fmt.Errorf("clear containers for %s: %w", workload.UID, err)
	}

	for _, container := range workload.Containers {
		if _, err := tx.Exec(ctx, `
			INSERT INTO workload_containers (
			    workload_uid, container, cpu_request_millicores, cpu_limit_millicores,
			    memory_request_bytes, memory_limit_bytes, observed_at
			) VALUES ($1, $2, $3, $4, $5, $6, now())`,
			workload.UID, container.Name,
			container.CPURequestMillicores, container.CPULimitMillicores,
			container.MemoryRequestBytes, container.MemoryLimitBytes,
		); err != nil {
			return fmt.Errorf("insert container %s of %s: %w", container.Name, workload.UID, err)
		}
	}

	return tx.Commit(ctx)
}

func (s *Store) DeleteWorkload(ctx context.Context, uid string) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM workloads WHERE uid = $1`, uid); err != nil {
		return fmt.Errorf("delete workload %s: %w", uid, err)
	}
	return nil
}

func (s *Store) GetWorkload(ctx context.Context, uid string) (Workload, error) {
	rows, err := s.pool.Query(ctx, selectWorkloads+` WHERE w.uid = $1 GROUP BY w.uid`, uid)
	if err != nil {
		return Workload{}, fmt.Errorf("query workload %s: %w", uid, err)
	}

	workloads, err := scanWorkloads(rows)
	if err != nil {
		return Workload{}, err
	}
	if len(workloads) == 0 {
		return Workload{}, ErrNotFound
	}

	return workloads[0], nil
}

func (s *Store) ListWorkloads(ctx context.Context, filter Filter) ([]Workload, string, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	conditions := []string{}
	args := []any{}

	add := func(condition string, value any) {
		args = append(args, value)
		conditions = append(conditions, fmt.Sprintf(condition, len(args)))
	}

	if filter.Division != "" {
		add("w.division_name = $%d", filter.Division)
	}
	if filter.Namespace != "" {
		add("w.namespace = $%d", filter.Namespace)
	}
	if filter.Health != "" {
		add("w.health = $%d", filter.Health)
	}
	if filter.Search != "" {
		add("w.name ILIKE '%%' || $%d || '%%'", filter.Search)
	}
	if filter.Cursor != "" {
		namespace, kind, name, err := decodeCursor(filter.Cursor)
		if err != nil {
			return nil, "", err
		}
		args = append(args, namespace, kind, name)
		conditions = append(conditions, fmt.Sprintf("(w.namespace, w.kind, w.name) > ($%d, $%d, $%d)", len(args)-2, len(args)-1, len(args)))
	}

	query := selectWorkloads
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}

	args = append(args, limit+1)
	query += fmt.Sprintf(" GROUP BY w.uid ORDER BY w.namespace, w.kind, w.name LIMIT $%d", len(args))

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, "", fmt.Errorf("list workloads: %w", err)
	}

	workloads, err := scanWorkloads(rows)
	if err != nil {
		return nil, "", err
	}

	next := ""
	if size := int(limit); len(workloads) > size {
		last := workloads[size-1]
		next = encodeCursor(last.Namespace, last.Kind, last.Name)
		workloads = workloads[:size]
	}

	return workloads, next, nil
}

func (s *Store) MarkSourceHealthy(ctx context.Context, source, cursor string, eventAt time.Time) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO projection_sources (source, cursor, last_event_at, last_synced_at, healthy, last_error)
		VALUES ($1, nullif($2, ''), $3, now(), true, NULL)
		ON CONFLICT (source) DO UPDATE SET
		    cursor         = coalesce(nullif(excluded.cursor, ''), projection_sources.cursor),
		    last_event_at  = excluded.last_event_at,
		    last_synced_at = now(),
		    healthy        = true,
		    last_error     = NULL`,
		source, cursor, eventAt)
	if err != nil {
		return fmt.Errorf("mark source %s healthy: %w", source, err)
	}
	return nil
}

func (s *Store) MarkSourceDegraded(ctx context.Context, source, reason string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO projection_sources (source, last_synced_at, healthy, last_error)
		VALUES ($1, now(), false, $2)
		ON CONFLICT (source) DO UPDATE SET
		    healthy    = false,
		    last_error = excluded.last_error`,
		source, reason)
	if err != nil {
		return fmt.Errorf("mark source %s degraded: %w", source, err)
	}
	return nil
}

func (s *Store) Freshness(ctx context.Context) (Freshness, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT source, cursor, last_event_at, last_synced_at, healthy, last_error
		FROM projection_sources`)
	if err != nil {
		return Freshness{}, fmt.Errorf("read projection sources: %w", err)
	}
	defer rows.Close()

	freshness := Freshness{}
	for rows.Next() {
		var source, cursor, lastError *string
		var lastEventAt, lastSyncedAt *time.Time
		var healthy bool

		if err := rows.Scan(&source, &cursor, &lastEventAt, &lastSyncedAt, &healthy, &lastError); err != nil {
			return Freshness{}, fmt.Errorf("scan projection source: %w", err)
		}

		if source != nil && *source == SourceKubernetes {
			if lastSyncedAt != nil {
				freshness.ObservedAt = *lastSyncedAt
				freshness.LagSeconds = int64(time.Since(*lastSyncedAt).Seconds())
			}
			if cursor != nil {
				freshness.ResourceVersion = *cursor
			}
		}

		if !healthy && source != nil {
			degraded := DegradedSource{Source: *source}
			if lastError != nil {
				degraded.Reason = *lastError
			}
			if lastSyncedAt != nil {
				degraded.LastHealthyAt = *lastSyncedAt
			}
			freshness.Degraded = append(freshness.Degraded, degraded)
		}
	}

	return freshness, rows.Err()
}

const selectWorkloads = `
	SELECT w.uid, coalesce(w.division_name, ''), w.namespace, w.name, w.kind,
	       coalesce(w.image_ref, ''), coalesce(w.replicas_desired, 0), coalesce(w.replicas_ready, 0),
	       w.health, w.created_at, w.resource_version, w.observed_at,
	       coalesce(sum(c.cpu_request_millicores), 0), coalesce(sum(c.memory_request_bytes), 0)
	FROM workloads w
	LEFT JOIN workload_containers c ON c.workload_uid = w.uid`

func scanWorkloads(rows pgx.Rows) ([]Workload, error) {
	defer rows.Close()

	workloads := []Workload{}
	for rows.Next() {
		var w Workload
		if err := rows.Scan(
			&w.UID, &w.Division, &w.Namespace, &w.Name, &w.Kind,
			&w.ImageRef, &w.ReplicasDesired, &w.ReplicasReady,
			&w.Health, &w.CreatedAt, &w.ResourceVersion, &w.ObservedAt,
			&w.CPURequest, &w.MemoryRequest,
		); err != nil {
			return nil, fmt.Errorf("scan workload: %w", err)
		}
		workloads = append(workloads, w)
	}

	return workloads, rows.Err()
}

func encodeCursor(namespace, kind, name string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(namespace + "\x00" + kind + "\x00" + name))
}

func decodeCursor(cursor string) (namespace, kind, name string, err error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", "", "", fmt.Errorf("malformed page token: %w", err)
	}

	parts := strings.Split(string(raw), "\x00")
	if len(parts) != 3 {
		return "", "", "", errors.New("malformed page token")
	}

	return parts[0], parts[1], parts[2], nil
}
