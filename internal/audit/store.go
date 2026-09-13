package audit

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

type Record struct {
	Seq int64
	Event
}

type Query struct {
	Division   string
	Actor      string
	Verb       string
	Resource   string
	ObjectUID  string
	ObjectName string
	Namespace  string
	Since      *time.Time
	Until      *time.Time
	Limit      int32
}

func (s *Store) Tip(ctx context.Context) ([]byte, int64, error) {
	var hash []byte
	var seq int64

	err := s.pool.QueryRow(ctx,
		`SELECT hash, seq FROM audit_events ORDER BY seq DESC LIMIT 1`,
	).Scan(&hash, &seq)

	if err == pgx.ErrNoRows {
		return []byte{}, 0, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("read the audit chain tip: %w", err)
	}

	return hash, seq, nil
}

func (s *Store) Append(ctx context.Context, events []Event) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin audit append: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, event := range events {
		if _, err := tx.Exec(ctx, `
			INSERT INTO audit_events (
			    audit_id, event_at, stage, actor, actor_groups, impersonated_by, acting_division,
			    verb, resource, subresource, namespace, object_name, object_uid,
			    response_code, source_ips, user_agent, payload, prev_hash, hash, canonical
			) VALUES (
			    $1, $2, $3, $4, coalesce($5::text[], '{}'), nullif($6, ''), nullif($7, ''),
			    $8, $9, nullif($10, ''), nullif($11, ''), nullif($12, ''), nullif($13, ''),
			    $14, coalesce($15::text[], '{}'), nullif($16, ''), $17, $18, $19, $20
			)
			ON CONFLICT (audit_id) DO NOTHING`,
			event.AuditID, event.EventAt, event.Stage, event.Actor, event.ActorGroups,
			event.ImpersonatedBy, event.ActingDivision,
			event.Verb, event.Resource, event.Subresource, event.Namespace, event.ObjectName, event.ObjectUID,
			event.ResponseCode, event.SourceIPs, event.UserAgent, event.Payload,
			event.PrevHash, event.Hash, event.CanonicalBytes,
		); err != nil {
			return fmt.Errorf("record audit event %s: %w", event.AuditID, err)
		}
	}

	return tx.Commit(ctx)
}

func (s *Store) List(ctx context.Context, query Query) ([]Record, error) {
	limit := query.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	rows, err := s.pool.Query(ctx, `
		SELECT seq, audit_id, event_at, stage, actor, actor_groups,
		       coalesce(impersonated_by, ''), coalesce(acting_division, ''),
		       verb, resource, coalesce(subresource, ''), coalesce(namespace, ''),
		       coalesce(object_name, ''), coalesce(object_uid, ''),
		       coalesce(response_code, 0), source_ips, coalesce(user_agent, ''),
		       payload, prev_hash, hash, canonical
		FROM audit_events
		WHERE ($1 = '' OR acting_division = $1)
		  AND ($2 = '' OR actor = $2)
		  AND ($3 = '' OR verb = $3)
		  AND ($4 = '' OR resource = $4)
		  AND ($5 = '' OR object_uid = $5)
		  AND ($6 = '' OR object_name = $6)
		  AND ($7 = '' OR namespace = $7)
		  AND ($8::timestamptz IS NULL OR event_at >= $8)
		  AND ($9::timestamptz IS NULL OR event_at <= $9)
		ORDER BY seq DESC
		LIMIT $10`,
		query.Division, query.Actor, query.Verb, query.Resource, query.ObjectUID,
		query.ObjectName, query.Namespace, query.Since, query.Until, limit)
	if err != nil {
		return nil, fmt.Errorf("list audit events: %w", err)
	}

	return scanRecords(rows)
}

func (s *Store) Range(ctx context.Context, from, to int64) ([]Record, []byte, error) {
	if to <= 0 {
		to = int64(1) << 62
	}

	var start []byte
	err := s.pool.QueryRow(ctx,
		`SELECT hash FROM audit_events WHERE seq < $1 ORDER BY seq DESC LIMIT 1`, from,
	).Scan(&start)

	if err == pgx.ErrNoRows {
		start = []byte{}
	} else if err != nil {
		return nil, nil, fmt.Errorf("read the hash before %d: %w", from, err)
	}

	rows, err := s.pool.Query(ctx, `
		SELECT seq, audit_id, event_at, stage, actor, actor_groups,
		       coalesce(impersonated_by, ''), coalesce(acting_division, ''),
		       verb, resource, coalesce(subresource, ''), coalesce(namespace, ''),
		       coalesce(object_name, ''), coalesce(object_uid, ''),
		       coalesce(response_code, 0), source_ips, coalesce(user_agent, ''),
		       payload, prev_hash, hash, canonical
		FROM audit_events
		WHERE seq >= $1 AND seq <= $2
		ORDER BY seq`, from, to)
	if err != nil {
		return nil, nil, fmt.Errorf("read audit range: %w", err)
	}

	records, err := scanRecords(rows)

	return records, start, err
}

func (s *Store) Get(ctx context.Context, seq int64) (Record, error) {
	records, _, err := s.Range(ctx, seq, seq)
	if err != nil {
		return Record{}, err
	}
	if len(records) == 0 {
		return Record{}, pgx.ErrNoRows
	}

	return records[0], nil
}

func scanRecords(rows pgx.Rows) ([]Record, error) {
	defer rows.Close()

	out := []Record{}
	for rows.Next() {
		var record Record
		if err := rows.Scan(
			&record.Seq, &record.AuditID, &record.EventAt, &record.Stage, &record.Actor, &record.ActorGroups,
			&record.ImpersonatedBy, &record.ActingDivision,
			&record.Verb, &record.Resource, &record.Subresource, &record.Namespace,
			&record.ObjectName, &record.ObjectUID,
			&record.ResponseCode, &record.SourceIPs, &record.UserAgent,
			&record.Payload, &record.PrevHash, &record.Hash, &record.CanonicalBytes,
		); err != nil {
			return nil, fmt.Errorf("scan audit event: %w", err)
		}
		out = append(out, record)
	}

	return out, rows.Err()
}
