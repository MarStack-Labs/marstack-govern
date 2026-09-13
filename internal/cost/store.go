package cost

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	governv1alpha1 "github.com/marstack-labs/marstack-govern/api/v1alpha1"
)

type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

func (s *Store) UpsertPolicy(ctx context.Context, policy Policy, resourceVersion string) error {
	var effectiveTo *time.Time
	if policy.To != nil {
		to := *policy.To
		effectiveTo = &to
	}

	_, err := s.pool.Exec(ctx, `
		INSERT INTO pricing_policies (
		    name, revision, effective_from, effective_to, currency,
		    rate_cpu_core_month, rate_memory_gb_month, rate_storage_gb_month,
		    rate_loadbalancer_month, rate_egress_gb,
		    unallocated_strategy, approved_by, approved_at, resource_version, observed_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, now())
		ON CONFLICT (name, revision) DO UPDATE SET
		    effective_from          = excluded.effective_from,
		    effective_to            = excluded.effective_to,
		    currency                = excluded.currency,
		    rate_cpu_core_month     = excluded.rate_cpu_core_month,
		    rate_memory_gb_month    = excluded.rate_memory_gb_month,
		    rate_storage_gb_month   = excluded.rate_storage_gb_month,
		    rate_loadbalancer_month = excluded.rate_loadbalancer_month,
		    rate_egress_gb          = excluded.rate_egress_gb,
		    unallocated_strategy    = excluded.unallocated_strategy,
		    approved_by             = excluded.approved_by,
		    approved_at             = excluded.approved_at,
		    resource_version        = excluded.resource_version,
		    observed_at             = now()`,
		policy.Name, policy.Revision, policy.From, effectiveTo, policy.Currency,
		policy.CPUCore.String(), policy.MemoryGi.String(), policy.StorageGi.String(),
		policy.LoadBalance.String(), policy.EgressGi.String(),
		strategyOf(policy.Unallocated), policy.ApprovedBy, policy.ApprovedAt, resourceVersion,
	)
	if err != nil {
		return fmt.Errorf("upsert pricing policy %s revision %d: %w", policy.Name, policy.Revision, err)
	}

	return nil
}

func (s *Store) DeletePolicy(ctx context.Context, name string) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM pricing_policies WHERE name = $1`, name); err != nil {
		return fmt.Errorf("delete pricing policy %s: %w", name, err)
	}

	return nil
}

func strategyOf(strategy governv1alpha1.UnallocatedStrategy) string {
	if strategy == governv1alpha1.UnallocatedProRata {
		return "pro_rata"
	}

	return "platform"
}
