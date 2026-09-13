package requests

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const KindQuota = "QuotaRequest"

var ErrNotFound = errors.New("request not found")

type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

type Request struct {
	UID             string
	Kind            string
	Name            string
	Namespace       string
	Division        string
	Requester       string
	Reason          string
	Phase           string
	Spec            json.RawMessage
	Recommendation  json.RawMessage
	Preflight       json.RawMessage
	CreatedAt       time.Time
	ResourceVersion string
	ObservedAt      time.Time
	Decision        *Decision
}

type Decision struct {
	UID           string
	RequestUID    string
	Decider       string
	Outcome       string
	Reason        string
	Evidence      json.RawMessage
	GrantedUntil  *time.Time
	DecidedAt     time.Time
	RevokedAt     *time.Time
	RevokedReason string
}

func (s *Store) UpsertRequest(ctx context.Context, request Request) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO requests (
		    uid, kind, name, namespace, division_uid, requester, acting_division,
		    reason, spec, recommendation, preflight, phase, created_at, resource_version, observed_at
		) VALUES (
		    $1, $2, $3, $4, (SELECT uid FROM divisions WHERE name = $5), $6, $5,
		    $7, $8, $9, $10, $11, $12, $13, now()
		)
		ON CONFLICT (uid) DO UPDATE SET
		    division_uid   = excluded.division_uid,
		    requester      = excluded.requester,
		    reason         = excluded.reason,
		    spec           = excluded.spec,
		    recommendation = excluded.recommendation,
		    preflight      = excluded.preflight,
		    phase          = excluded.phase,
		    resource_version = excluded.resource_version,
		    observed_at    = now()`,
		request.UID, request.Kind, request.Name, request.Namespace, request.Division,
		request.Requester, request.Reason, request.Spec,
		nullableJSON(request.Recommendation), nullableJSON(request.Preflight),
		request.Phase, request.CreatedAt, request.ResourceVersion,
	)
	if err != nil {
		return fmt.Errorf("upsert request %s/%s: %w", request.Namespace, request.Name, err)
	}

	return nil
}

func (s *Store) DeleteRequest(ctx context.Context, namespace, name string) error {
	if _, err := s.pool.Exec(ctx,
		`DELETE FROM requests WHERE namespace = $1 AND name = $2 AND kind = $3`,
		namespace, name, KindQuota,
	); err != nil {
		return fmt.Errorf("delete request %s/%s: %w", namespace, name, err)
	}

	return nil
}

func (s *Store) UpsertDecision(ctx context.Context, decision Decision) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO decisions (
		    uid, request_uid, decider, outcome, reason, evidence,
		    granted_until, decided_at, revoked_at, revoked_reason, resource_version, observed_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, nullif($10, ''), $11, now())
		ON CONFLICT (uid) DO UPDATE SET
		    outcome        = excluded.outcome,
		    reason         = excluded.reason,
		    evidence       = excluded.evidence,
		    granted_until  = excluded.granted_until,
		    revoked_at     = excluded.revoked_at,
		    revoked_reason = excluded.revoked_reason,
		    resource_version = excluded.resource_version,
		    observed_at    = now()`,
		decision.UID, decision.RequestUID, decision.Decider, decision.Outcome, decision.Reason,
		decision.Evidence, decision.GrantedUntil, decision.DecidedAt,
		decision.RevokedAt, decision.RevokedReason, "1",
	)
	if err != nil {
		return fmt.Errorf("upsert decision %s: %w", decision.UID, err)
	}

	return nil
}

func (s *Store) ListRequests(ctx context.Context, divisions []string, phase string, openOnly bool) ([]Request, error) {
	query := selectRequests + ` WHERE ($1::text[] IS NULL OR coalesce(d.name, r.acting_division) = ANY($1))
		  AND ($2 = '' OR r.phase = $2)
		  AND (NOT $3 OR r.phase IN ('pending', 'awaiting_decision', 'changes_requested'))
		ORDER BY r.created_at DESC`

	rows, err := s.pool.Query(ctx, query, divisions, phase, openOnly)
	if err != nil {
		return nil, fmt.Errorf("list requests: %w", err)
	}

	return scanRequests(rows)
}

func (s *Store) GetRequest(ctx context.Context, uid string) (Request, error) {
	rows, err := s.pool.Query(ctx, selectRequests+` WHERE r.uid = $1`, uid)
	if err != nil {
		return Request{}, fmt.Errorf("get request %s: %w", uid, err)
	}

	found, err := scanRequests(rows)
	if err != nil {
		return Request{}, err
	}
	if len(found) == 0 {
		return Request{}, ErrNotFound
	}

	return found[0], nil
}

const selectRequests = `
	SELECT r.uid, r.kind, r.name, r.namespace, coalesce(d.name, r.acting_division),
	       r.requester, r.reason, r.phase, r.spec,
	       coalesce(r.recommendation, 'null'::jsonb), coalesce(r.preflight, 'null'::jsonb),
	       r.created_at, r.resource_version, r.observed_at,
	       dec.uid, dec.decider, dec.outcome, dec.reason, dec.evidence,
	       dec.granted_until, dec.decided_at, dec.revoked_at, coalesce(dec.revoked_reason, '')
	FROM requests r
	LEFT JOIN divisions d ON d.uid = r.division_uid
	LEFT JOIN LATERAL (
	    SELECT * FROM decisions WHERE decisions.request_uid = r.uid
	    ORDER BY decided_at DESC LIMIT 1
	) dec ON true`

func scanRequests(rows pgx.Rows) ([]Request, error) {
	defer rows.Close()

	out := []Request{}
	for rows.Next() {
		var request Request
		var decisionUID, decider, outcome, decisionReason, revokedReason *string
		var evidence []byte
		var grantedUntil, decidedAt, revokedAt *time.Time

		if err := rows.Scan(
			&request.UID, &request.Kind, &request.Name, &request.Namespace, &request.Division,
			&request.Requester, &request.Reason, &request.Phase, &request.Spec,
			&request.Recommendation, &request.Preflight,
			&request.CreatedAt, &request.ResourceVersion, &request.ObservedAt,
			&decisionUID, &decider, &outcome, &decisionReason, &evidence,
			&grantedUntil, &decidedAt, &revokedAt, &revokedReason,
		); err != nil {
			return nil, fmt.Errorf("scan request: %w", err)
		}

		if decisionUID != nil {
			decision := Decision{
				UID:        *decisionUID,
				RequestUID: request.UID,
				Evidence:   evidence,
			}
			if decider != nil {
				decision.Decider = *decider
			}
			if outcome != nil {
				decision.Outcome = *outcome
			}
			if decisionReason != nil {
				decision.Reason = *decisionReason
			}
			if revokedReason != nil {
				decision.RevokedReason = *revokedReason
			}
			decision.GrantedUntil = grantedUntil
			decision.RevokedAt = revokedAt
			if decidedAt != nil {
				decision.DecidedAt = *decidedAt
			}

			request.Decision = &decision
		}

		out = append(out, request)
	}

	return out, rows.Err()
}

func nullableJSON(raw json.RawMessage) any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}

	return []byte(raw)
}
