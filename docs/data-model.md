# Data model

PostgreSQL here is a **projection**, not a system of record. Every row can be rebuilt from a platform
API. Dropping the database and replaying must restore it exactly — that is invariant 2 from
[ARCHITECTURE.md](../ARCHITECTURE.md), and it is what keeps the "no typed facts" rule enforceable
instead of aspirational.

## Migration convention

Files live in `internal/db/migrations`, named `NNNN_name.sql`, applied in ascending order, each in
its own transaction, recorded in a `schema_migrations` table the runner creates. Forward-only: there
are no down migrations. A mistake is corrected by a new migration, because rolling a schema backwards
on a projection is never necessary — the projection is disposable.

Migrations carry no SQL comments. The reasoning lives in this document, where it can be read without
`psql`.

## Where each table comes from

| Table | Source of truth | Rebuilt by |
|---|---|---|
| `projection_sources` | the projector itself | reset to empty; replay repopulates |
| `divisions` | `Division` CR + Capsule `Tenant` + `ResourceQuota` | informer list + watch |
| `namespaces` | Namespace, NetworkPolicy, LimitRange | informer list + watch |
| `division_members` | IdP group claims | IdP directory query |
| `images` | OCI registry, cosign, Trivy | registry + scanner query per digest |
| `workloads` | Deployment / StatefulSet / DaemonSet / Rollout / CronJob | informer list + watch |
| `workload_containers` | pod template of the owning workload | same informer event |
| `gitops_applications` | Argo CD `Application` CR | informer list + watch |
| `requests` | request CRs | informer list + watch |
| `decisions` | `Decision` CR | informer list + watch |
| `pricing_policies` | `PricingPolicy` CR | informer list + watch |
| `workload_usage_daily` | Mimir | range query per day |
| `cost_samples` | OpenCost | allocation query per window |
| `invoices`, `invoice_lines` | recomputed from `cost_samples` + `pricing_policies` | deterministic recomputation |
| `audit_events` | Kubernetes audit webhook, archived to WORM storage | re-read the WORM archive |
| `timeline_events` | rollouts, events, alerts, audit, decisions | re-derived from the sources above |
| `policy_violations` | Kyverno `PolicyReport` | informer list + watch |

Note what is **not** here: no table stores a fact a human typed. `division_members` looks like an
exception and is not — it is a cache of IdP claims with a `synced_at`, and the platform never writes
to it as the author. Membership changes travel as a `MembershipRequest` and are applied at the IdP.

## Replay

```
1. drop the schema
2. run migrations
3. list-and-watch every Kubernetes source, writing at the observed resourceVersion
4. re-read the WORM audit archive and re-insert in sequence
5. re-query Mimir and OpenCost for the retention window
6. recompute invoices from cost_samples and the pricing policy revisions they cite
```

Steps 3 to 6 are independent and run concurrently, except that invoices depend on step 5.

The replay test hashes the contents of every table, runs the procedure, and compares. A column whose
only source is a form cannot come back, so the test fails — which is the point. The invariant is
enforced by CI rather than by remembering.

## Freshness is data, not an assumption

`projection_sources` holds a cursor and a health flag per source, and every projected row carries
`observed_at`. The API returns both to the UI, which shows the age of what it is displaying and
degrades loudly when the projector falls behind. A dashboard that cannot say how old it is will
eventually lie.

## Decisions worth explaining

### Money is `numeric`, never float

`numeric(20, 2)` for amounts and rates, `numeric(20, 6)` for consumption quantities. Binary floating
point cannot represent currency exactly, and an invoice that fails to reconcile by a few rupiah is
indistinguishable from a real attribution bug.

### Billing columns are requested-first

`cost_samples` records `cpu_core_hours_requested` alongside `cpu_core_hours_used`, and invoices are
built from the requested columns. Requested capacity is what a division locks away from everyone
else. Usage is kept because it is the only way to compute idle and to justify a right-sizing
recommendation.

### Invoices carry `inputs_digest` and cite a policy revision

`pricing_policies` is keyed by `(name, revision)` and an invoice holds a foreign key to the exact
revision it used. Together with `inputs_digest` — a hash over the cost samples that fed it — an
invoice is reproducible: recompute it and you must get the same number. Revising a rate can never
silently rewrite last month.

### Approvals cannot be unbounded or evidence-free

Two constraints on `decisions` encode policy in the schema:

```sql
CHECK (outcome <> 'approved' OR granted_until IS NOT NULL)
CHECK (jsonb_typeof(evidence) = 'object' AND evidence <> '{}')
```

An approval without an expiry and an approval without recorded evidence are both unrepresentable. A
rule that lives in the schema cannot be forgotten by a new code path.

The same technique appears on `workloads`: a tier that came from a human override must carry
`tier_override_until`, so a manual classification cannot quietly become permanent.

### The audit table defends itself

`audit_events` is append-only and chained:

- triggers reject `UPDATE`, `DELETE` and `TRUNCATE`
- an insert trigger requires `prev_hash` to equal the current tip, so events cannot be reordered or
  slipped in between two existing rows
- `hash` is `sha256(prev_hash || canonical_json(event))`, computed by the projector

This makes tampering detectable inside PostgreSQL, but PostgreSQL is not where the guarantee lives —
anyone with superuser access can drop a trigger. The guarantee is the WORM archive in object storage
with retention set in bucket policy. The table is the queryable copy; the archive is the evidence, and
the chain is what proves the copy matches it.

Because the chain is strictly ordered, audit ingestion is single-writer. That is a deliberate
throughput ceiling in exchange for a property that is otherwise impossible to state honestly.

### A workload carries its division twice

`workloads.division_uid` references the projected `Division`, and `workloads.division_name` holds the
name read straight off the namespace or workload label. The name is available the moment a workload
is discovered; the uid only exists once the `Division` custom resource has been projected. Keeping
both means the catalog is useful before tenancy is configured, and joins stay cheap once it is.

This is also the first example of the forward-only rule in practice: rather than editing
`0002_catalog.sql`, the column arrived in `0006_workload_division_name.sql`. Editing an applied
migration is what the checksum check in the runner exists to catch.

### Foreign keys point at what is stable

`workloads.division_uid` is `ON DELETE SET NULL`, not `CASCADE`: a workload can be observed before
its division has been projected, and a division being removed should not erase the history of what
ran in it. `invoice_lines` cascades from `invoices`, because a line has no meaning without its
invoice.

### Partial indexes for the queries that matter

The expensive screens are narrow: open requests, unhealthy workloads, approvals about to expire.
Each gets a partial index instead of a full one, so the index stays small and the write path stays
cheap.
