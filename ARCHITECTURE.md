# Architecture

This document describes what marstack-govern is, the one rule that shapes every part of it, and why
each boundary sits where it does.

## The problem

One Kubernetes cluster, many divisions. Divisions ask for things — a namespace, more quota, a
database, access, a preview environment — and a platform team decides whether to grant them. Today
that conversation runs on tickets, spreadsheets, and hand-edited YAML.

Three things go wrong, and they are well documented across the tools that already exist in this
space:

1. **Catalogs written by hand go stale.** A portal that asks each team to maintain a descriptor file
   ends up describing a cluster that no longer exists. Services are decommissioned; their entries
   live on.
2. **Governance state drifts.** When quota lives in a Git file, RBAC lives in the cluster, and
   request history lives in the portal's own database, three sources disagree and nobody can say
   which is right.
3. **Approvals are made blind.** The decision itself — the only part still performed by a human — is
   the part with the least support. An approver sees the requester's prose, not the consequence.

## Goals

1. **One source of truth: the platform.** Every operational fact shown in the UI is read from a
   platform API and can be traced back to it.
2. **Decisions supported by evidence.** Requests arrive pre-filled from telemetry; approvals show a
   simulation of their own effect before they are granted.
3. **Tenancy that holds without trust.** Isolation is enforced by Kubernetes, not by the portal
   being careful.
4. **Composable, not monolithic.** Build the decision layer; compose the substrate from projects
   that already do their job well.
5. **Auditable end to end.** Every mutation is attributable to a human identity, and the record
   cannot be quietly rewritten.

## Non-goals

- Not an orchestrator. Crossplane, Kratix and KubeVela fulfil requests; this project decides them.
- Not a replacement for Grafana. Free-form investigation stays there; deep links carry context.
- Not CI/CD. The platform reads pipeline results; it does not run pipelines.
- Not a hand-maintained catalog. If a feature needs a YAML file kept in sync by humans, the feature
  is designed wrong.
- Not a SIEM.
- Not the system of record for who belongs to which division. The identity provider is.

## The derivation rule

Humans supply two kinds of input. Everything else is derived.

| Input | Examples | Shape |
|---|---|---|
| **Intent** | raise my quota, grant me access, approve, reject | a Custom Resource carrying requester identity, reason, and an expiry |
| **Policy** | the rupiah rate per vCPU, Kyverno rules, how idle cost is allocated | a versioned, approved object that later records can cite |

Facts — what is running, who owns it, how much it uses, who changed what — are always read from:

```
Kubernetes API      what exists, and its desired spec
Kubernetes audit    who did it, when, from where
OCI registry        image provenance, signatures, attestations
Mimir / Loki / Tempo / Pyroscope   usage, logs, traces, profiles
Kyverno             policy evaluation results
Trivy               vulnerabilities and SBOM
OpenCost            resource consumption in cost terms
Argo CD             desired versus live for GitOps-managed workloads
Hubble              observed network flows
```

If a fact cannot be derived from that list, it is not displayed. There is no free-text field
describing reality.

### Why the split matters

Chargeback needs a price list, and a price list is not in the cluster. That does not break the rule,
it clarifies it: a price is **policy**, not an operational fact. It is written by a human, versioned,
approved, and cited by every invoice that used it. The rule forbids typed facts, not typed policy.

The same reasoning applies to Kyverno rules and to cost allocation choices. Everything else — every
number about a workload, a division, or a person's actions — is derived.

## Invariants

These three are testable, and the tests are part of CI. They exist so the derivation rule survives
contact with future features.

### 1. Derived by default

Human input is intent or policy, never an operational fact.

*Enforced by:* code review against the table above, plus invariant 2, which makes violations visible
mechanically.

### 2. The read model is disposable

PostgreSQL holds only a projection. Dropping the database and replaying from the platform must
restore every row.

*Enforced by:* a test that hashes table contents, drops the schema, replays, and compares. A column
whose only source is a form cannot survive the replay, so the violation fails CI rather than waiting
to be noticed.

### 3. The UI is a client of the public API

There are no private endpoints. Anything the UI can do, the CLI and a `kubectl` plugin can do, with
the same identity and the same authorization.

*Enforced by:* a negative test — a user from division A calling for division B's resources must be
refused by Kubernetes RBAC, not by an application-level check.

## Tenancy

```
Division (tenant)
 ├── div-payments-dev
 ├── div-payments-staging
 └── div-payments-prod
```

A `Division` custom resource is reconciled into namespaces, a Capsule `Tenant`, quota, limit ranges,
and a default-deny network policy.

### Capsule, not HNC

The Hierarchical Namespace Controller was archived to `kubernetes-retired` in April 2025 for lack of
maintainers and adopters. It is not a foundation to build on.

Capsule is a CNCF Sandbox project and covers the requirement directly: its resource pools compute
quota **across** a tenant's namespaces at admission time, without creating a duplicate
`ResourceQuota` in each namespace. A division's quota is therefore shared by its environments
without any distribution logic of our own.

The consequence: a division's environments are a tenant grouping plus labels, not a namespace tree.

### Where this stands today

The `Division` controller provisions namespaces, limit ranges, default-deny network policies and
role bindings, and it enforces quota with a `ResourceQuota` **per namespace**. Each namespace is
therefore capped, but the division's total is not yet enforced across its namespaces — that is what
Capsule's resource pools provide, and wiring them in is a separate step, verified against a cluster
that actually runs Capsule.

The division reports which backend it is using in `status.quotaBackend`, and the `QuotaReady`
condition says plainly that a cross-namespace total is missing. A gap that is visible in
`kubectl get division` is a gap; a gap hidden behind a green checkmark is a lie.

### Isolation defaults

| Control | Default |
|---|---|
| Network policy | default-deny per namespace, installed by the controller, undeletable by tenants (enforced by Kyverno) |
| Cross-division traffic | only via an approved `PeeringRequest`, recorded like any other decision |
| Limit range | 0.25 CPU / 512Mi default request; per-pod maximum from tenant policy |
| Data plane | Cilium, which also supplies observed flows through Hubble |

## Identity

A user belongs to many divisions, and a division has many users. **Role is a property of the pair
(user, division)**, never of the user alone.

| Concern | Mechanism |
|---|---|
| Authentication | OIDC against the corporate IdP (Keycloak, or Dex fronting an existing one) |
| Membership | group claims from the IdP |
| Effective role | group claim mapped to a RoleBinding in the division's namespaces |
| Calls to Kubernetes | impersonation using the caller's identity and groups |
| Direct `kubectl` | short-lived tokens from the TokenRequest API via an exec credential plugin |
| Active division | held in the session; every mutation records which division it was made on behalf of |
| UI permission checks | `SelfSubjectAccessReview`; the UI never guesses what a user may do |

### Membership is not ours to store

Keeping a membership table would make the platform the author of a fact, which invariant 1 forbids —
and it would drift from the IdP within weeks. Instead the platform accepts a `MembershipRequest`,
writes the outcome back to the IdP or the group repository, and continues to read effective
membership from claims.

### No mandatory gateway

Routing all Kubernetes access through the platform would kill `kubectl`, make the platform a single
point of failure, and turn it into an attractive target holding broad credentials. What is actually
needed is not a gateway but *one identity on every path*: the UI and `kubectl` reach the same API
server, are stopped by the same RBAC, and land in the same audit log.

This choice collapses what would otherwise be two audit trails into one. See below.

## Data flow

```
Kubernetes API ─┐
Kubernetes audit┤
OCI registry ───┤
Mimir/Loki/Tempo┼─► projector ─► PostgreSQL ─► ConnectRPC ─┬─► web UI (SSE)
Kyverno / Trivy ┤                (read model)              └─► CLI, kubectl plugin
OpenCost ───────┤
Argo CD, Hubble ┘

intent ──► custom resource ──► controller ──► Capsule / Kubernetes / Argo CD
policy ──► PricingPolicy (CR) · Kyverno policies (Git)
```

Reads and writes take different paths on purpose. Writes go through the Kubernetes API, so they are
authorized by RBAC, validated by admission, and recorded in the audit log. Reads go through a
projection, so a dashboard listing two thousand workloads does not hammer the API server.

### The read model

PostgreSQL is a cache with a rebuild procedure, not a system of record. It exists because three
kinds of question are impractical against the Kubernetes API directly:

- historical (what did this division use over 30 days)
- cross-cutting (every workload in the cluster, sorted by cost)
- expensive-to-compute (simulation results, correlation windows)

Freshness is visible rather than assumed: every response carries the `resourceVersion` and the age of
the projection behind it, and the UI degrades loudly when the projector falls behind.

## Modules

Each module owns its custom resources, controller, projector, queries and handlers — a vertical
slice, not a horizontal layer. Domain modules may not import one another; they compose through the
kernel or through a contract a module publishes.

### Kernel

| Module | Responsibility |
|---|---|
| `kube` | informers and a watch multiplexer, impersonating client factory, server-side apply dry-run client |
| `projector` | platform events into the read model, and the replay procedure invariant 2 depends on |
| `api` | ConnectRPC handlers, the SSE hub, authorization middleware |
| `simulate` | the shadow world: admission dry-run, scheduler simulation, quota and cost projection |
| `timeline` | ordered events per workload: rollouts, Kubernetes events, alerts, audit entries |

`simulate` and `timeline` exist as kernel modules because several features are the same machine
pointed at different questions.

- `simulate` serves preflight, the approval impact simulator, right-sizing, and billing projection.
  All four are "run this decision in a shadow world and show the difference".
- `timeline` serves the failure explainer and deploy-to-regression correlation. Both need one thing:
  an ordered, queryable history of what happened to a workload.

Building either twice would be the first step toward two answers to the same question.

### Domain

| Module | Responsibility | Custom resources |
|---|---|---|
| `identity` | OIDC, session and active division, claims to RBAC | `MembershipRequest` |
| `tenancy` | divisions, namespaces, quota via Capsule, limit ranges, default-deny policy | `Division` |
| `requests` | request lifecycle, recommender, preflight | `QuotaRequest`, `AccessRequest`, `PeeringRequest` |
| `decisions` | approval queue, impact simulation, decision record | `Decision` |
| `finops` | consumption, rates, chargeback, invoices, idle, right-sizing | `PricingPolicy` |
| `catalog` | workload discovery, classification, ownership | `TierOverride` |
| `signals` | golden signals, SLOs, error budgets, burn rate | `ServiceLevelObjective` |
| `diagnostics` | failure explainer, timeline views, deploy correlation, logs and exec | — |
| `delivery` | curated templates, Argo CD integration, rollout status | — |
| `supplychain` | provenance, signatures, SBOM, vulnerabilities | — |
| `policy` | Kyverno policy catalog, evaluation results, violations per division | — |
| `access` | just-in-time token broker, TTL, revocation | `AccessGrant` |
| `audit` | audit webhook ingest, hash chain, WORM export, streaming | — |
| `topology` | dependency graph, network reachability | — |
| `environments` | ephemeral environments and their TTL controller | `EphemeralEnvironment` |

## The decision layer

This is the part that does not exist elsewhere, and the reason the project is worth building.

### Requests arrive filled in

A quota form does not open empty. The recommender reads 30 days of usage from Mimir, computes p95 and
p99 plus trend, and proposes a target with headroom — along with the date the current quota runs out.
The requester agrees with a number derived from their own behaviour instead of inventing one.

### Preflight runs against the real admission chain

Before anything is applied, the platform performs a server-side apply with `dryRun=All` and evaluates
Kyverno. The result names the rejected field, the policy that rejected it, and the exact diff. This is
categorically different from rendering a template and hoping: the same admission chain that will
judge the real apply has already judged this one.

### Approvals show their consequence

When an approver opens a request, they see what granting it does: how cluster commitment changes,
which nodes no longer fit, which pods would go `Pending`, and how the division's monthly bill moves.
Scheduler simulation, not arithmetic — arithmetic cannot tell you that 4 spare cores spread across
6 nodes will not fit a 2-core pod.

### Decisions are time-bound and evidenced

Every approval carries an expiry and a reason, and a controller revokes it when it lapses. The
decision record stores the evidence the approver saw at that moment — the metric snapshot and the
simulation result — not merely that someone clicked Approve. An audit trail that records actions
without their basis cannot answer the question anyone actually asks later: *why was this allowed?*

## FinOps

```
cost = consumption (derived from OpenCost) × rate (signed policy)
```

**Chargeback is based on `requests`, not `usage`.** Requested resources are capacity locked away from
every other division; that is what a division actually consumes from the cluster. Billing on usage
would penalise exactly the teams that size their requests honestly, and would make bills wobble with
traffic rather than with commitment. Usage is still shown — as the measure of idle, and as the input
to right-sizing advice.

| Concern | Handling |
|---|---|
| Rates | `PricingPolicy`, effective-dated and approved |
| Invoices | immutable snapshot citing the `PricingPolicy` version used, so revising a rate never rewrites last month |
| Idle | `requested − used` per division, in rupiah, with the concrete patch that would fix it |
| Unallocated | capacity no division reserved, allocated per `PricingPolicy` (absorbed by the platform, or pro rata) |
| Attribution | a `division_id` label required by Kyverno at admission |

Reconciliation test: the sum of every division's bill plus unallocated must equal total cluster cost.
A non-zero difference means attribution is leaking.

## Audit

| Layer | Source |
|---|---|
| Control plane | Kubernetes audit webhook |
| Platform actions | the same webhook |

There is only one layer because every platform mutation is a custom resource written under the
caller's own identity. "When did a division change its quota" is already a `QuotaRequest` mutation in
the Kubernetes audit log. A second, application-written trail would add nothing except the
possibility of disagreeing with the first.

### Tamper evidence

Loki is a query engine, not a guarantee. Anyone with object storage access can delete chunks. The
integrity guarantee comes from two properties Loki does not provide:

```
hash chain   every event carries the hash of its predecessor
WORM         S3 Object Lock, with retention set in bucket policy rather than app config
```

Testable: alter one stored event and chain verification must fail.

## Failure modes

An architecture is defined as much by how it degrades as by how it works.

| Dependency | When it is unavailable |
|---|---|
| Projector falls behind | UI shows projection age and a degradation banner; it never presents stale data as current |
| Mimir | recommender and golden signals unavailable; requests may still be filed, but without a proposed number, and the absence is stated |
| OpenCost | cost views show the last complete sample and its timestamp; invoices are not generated from partial data |
| Kyverno | preflight fails closed — no result is reported as "passed" |
| PostgreSQL | reads degrade to direct Kubernetes API queries for small result sets; history and search are unavailable |
| The platform itself | `kubectl` keeps working, because it never depended on the platform being up |

That last row is the point of refusing a mandatory gateway.

## Stack decisions

| Component | Choice | Why |
|---|---|---|
| Language | Go | controller-runtime, the Kubernetes client, and the whole operator ecosystem live here |
| API | ConnectRPC | one proto yields gRPC, gRPC-Web and plain HTTP/JSON; browsers and CLIs share a contract |
| Live updates | SSE | one-directional server-to-client is exactly the shape of a watch; it survives proxies that break WebSockets |
| Read model | PostgreSQL with pgx and sqlc | generated, type-checked queries; no runtime ORM reflection |
| Frontend | Vite, React 19, TanStack Router and Query, Tailwind, shadcn/ui | embedded into the Go binary; no Node runtime in production |
| Multi-tenancy | Capsule | HNC is archived; Capsule computes cross-namespace quota at admission |
| Data plane | Cilium | network policy plus Hubble flow data for real reachability |
| Policy | Kyverno | declarative YAML, and the easiest engine to evaluate ahead of time for preflight |
| Cost | OpenCost | vendor-neutral consumption data; rates stay in `PricingPolicy` |
| GitOps | Argo CD | its `Application` resource is also the desired-versus-live source for drift |
| Identity | Keycloak or Dex | group claims as the membership source |
| Telemetry | Mimir, Loki, Tempo, Pyroscope, Grafana | `division_id` carried across all pillars |
| Audit integrity | S3 Object Lock plus hash chain | Loki queries; these two guarantee |
| Distribution | single binary, distroless image, non-root, read-only root filesystem, Helm chart | |

## What we build versus what we compose

Composed: isolation and quota (Capsule), policy (Kyverno), network (Cilium), delivery (Argo CD),
consumption (OpenCost), identity (Keycloak), telemetry (Grafana stack).

Built here: the derived catalog, the recommender, preflight, the impact simulator, the decision
record, chargeback on top of consumption, the failure explainer, and deploy-to-regression
correlation.

The dividing line is deliberate. Every composed component answers *how something is enforced or
executed*; everything built here answers *what should be allowed, and what it will cost*.
