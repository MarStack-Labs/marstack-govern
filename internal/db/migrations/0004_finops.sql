CREATE TABLE pricing_policies (
    name                text NOT NULL,
    revision            integer NOT NULL,
    effective_from      date NOT NULL,
    effective_to        date,
    currency            text NOT NULL DEFAULT 'IDR',
    rate_cpu_core_month         numeric(20, 2) NOT NULL,
    rate_memory_gb_month        numeric(20, 2) NOT NULL,
    rate_storage_gb_month       numeric(20, 2) NOT NULL,
    rate_loadbalancer_month     numeric(20, 2) NOT NULL,
    rate_egress_gb              numeric(20, 2) NOT NULL,
    unallocated_strategy        text NOT NULL,
    approved_by         text NOT NULL,
    approved_at         timestamptz NOT NULL,
    resource_version    text NOT NULL,
    observed_at         timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (name, revision),
    CONSTRAINT pricing_policies_unallocated_known CHECK (unallocated_strategy IN ('platform', 'pro_rata')),
    CONSTRAINT pricing_policies_period_ordered CHECK (effective_to IS NULL OR effective_to > effective_from)
);

CREATE TABLE workload_usage_daily (
    workload_uid            uuid NOT NULL REFERENCES workloads (uid) ON DELETE CASCADE,
    day                     date NOT NULL,
    cpu_used_p50_millicores bigint,
    cpu_used_p95_millicores bigint,
    cpu_used_p99_millicores bigint,
    memory_used_p50_bytes   bigint,
    memory_used_p95_bytes   bigint,
    memory_used_p99_bytes   bigint,
    sampled_at              timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workload_uid, day)
);

CREATE INDEX workload_usage_daily_day_idx ON workload_usage_daily (day DESC);

CREATE TABLE cost_samples (
    division_uid                uuid REFERENCES divisions (uid) ON DELETE CASCADE,
    workload_uid                uuid REFERENCES workloads (uid) ON DELETE CASCADE,
    window_start                timestamptz NOT NULL,
    window_end                  timestamptz NOT NULL,
    cpu_core_hours_requested    numeric(20, 6) NOT NULL DEFAULT 0,
    cpu_core_hours_used         numeric(20, 6) NOT NULL DEFAULT 0,
    memory_gb_hours_requested   numeric(20, 6) NOT NULL DEFAULT 0,
    memory_gb_hours_used        numeric(20, 6) NOT NULL DEFAULT 0,
    storage_gb_hours            numeric(20, 6) NOT NULL DEFAULT 0,
    loadbalancer_hours          numeric(20, 6) NOT NULL DEFAULT 0,
    egress_gb                   numeric(20, 6) NOT NULL DEFAULT 0,
    sampled_at                  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT cost_samples_window_ordered CHECK (window_end > window_start),
    CONSTRAINT cost_samples_has_subject CHECK (division_uid IS NOT NULL OR workload_uid IS NOT NULL)
);

CREATE UNIQUE INDEX cost_samples_workload_window_idx
    ON cost_samples (workload_uid, window_start) WHERE workload_uid IS NOT NULL;
CREATE UNIQUE INDEX cost_samples_division_window_idx
    ON cost_samples (division_uid, window_start) WHERE workload_uid IS NULL;

CREATE TABLE invoices (
    uid                 uuid PRIMARY KEY,
    division_uid        uuid NOT NULL REFERENCES divisions (uid) ON DELETE CASCADE,
    period_start        date NOT NULL,
    period_end          date NOT NULL,
    pricing_policy_name text NOT NULL,
    pricing_policy_revision integer NOT NULL,
    currency            text NOT NULL DEFAULT 'IDR',
    subtotal            numeric(20, 2) NOT NULL,
    unallocated_share   numeric(20, 2) NOT NULL DEFAULT 0,
    total               numeric(20, 2) NOT NULL,
    inputs_digest       bytea NOT NULL,
    generated_at        timestamptz NOT NULL DEFAULT now(),
    UNIQUE (division_uid, period_start, period_end),
    FOREIGN KEY (pricing_policy_name, pricing_policy_revision)
        REFERENCES pricing_policies (name, revision)
);

CREATE TABLE invoice_lines (
    invoice_uid         uuid NOT NULL REFERENCES invoices (uid) ON DELETE CASCADE,
    line_no             integer NOT NULL,
    workload_uid        uuid REFERENCES workloads (uid) ON DELETE SET NULL,
    workload_label      text NOT NULL,
    resource            text NOT NULL,
    quantity            numeric(20, 6) NOT NULL,
    unit                text NOT NULL,
    rate                numeric(20, 2) NOT NULL,
    amount              numeric(20, 2) NOT NULL,
    PRIMARY KEY (invoice_uid, line_no),
    CONSTRAINT invoice_lines_resource_known CHECK (resource IN ('cpu', 'memory', 'storage', 'loadbalancer', 'egress'))
);

CREATE INDEX invoice_lines_workload_uid_idx ON invoice_lines (workload_uid);
