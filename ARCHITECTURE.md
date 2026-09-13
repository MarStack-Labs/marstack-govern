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
role bindings. How it enforces quota depends on what the cluster actually has, decided by a
`RESTMapper` lookup for `GlobalResourceQuota` rather than by configuration:

| Capsule | Enforcement | `status.quotaBackend` |
|---|---|---|
| installed | one `GlobalResourceQuota` selecting every namespace labelled with the division; Capsule keeps a single ledger across them | `capsule` |
| absent | a `ResourceQuota` in each namespace | `resourcequota` |

The two are mutually exclusive, and switching is not additive. When Capsule appears, the controller
**deletes** the per-namespace quotas it wrote earlier. Leaving them would cap each namespace at the
whole division total on its own, which is the failure the global quota exists to prevent — a division
allowed 40 cpu could then run 40 in each of four namespaces.

`QuotaReady` reports which of the two is actually holding the line, and with Capsule it quotes the
shared ledger: *"Capsule enforces the division total across 4 namespaces; 26500m of 40 cpu is
committed."* A gap visible in `kubectl get division` is a gap; a gap hidden behind a green checkmark
is a lie.

Capsule's own types are read through `unstructured` rather than by importing its Go module. An
optional dependency that is compiled in is not optional, and the platform must keep running on
clusters that do not have it.

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

### Reads are scoped by asking Kubernetes

Reads are served from PostgreSQL, which has no idea about RBAC. Rather than reimplementing
authorization over the projection, the platform asks Kubernetes and filters by the answer:

```
group claims  ─►  divisions the caller has a grant in  ─►  their namespaces
                                                              │
                        SelfSubjectAccessReview (impersonated) ┤  may I list deployments here?
                                                              ▼
                                             the namespaces that survive become the scope
```

The first review is cluster-wide with an empty namespace. A caller who may list workloads everywhere
— a platform approver, a cluster admin — gets `AllowAll` and sees the whole estate, because
Kubernetes said so and not because the application special-cased them.

Decisions are cached per subject and namespace for a minute, so a dashboard refresh does not turn
into a burst of reviews. A caller has a handful of namespaces, so the first request costs a handful
of reviews and the rest are free.

The same scope filters the event stream. Without it a viewer would receive live changes for
namespaces they cannot read — the exact leak that a scoped list would otherwise have prevented.

### No mandatory gateway

Routing all Kubernetes access through the platform would kill `kubectl`, make the platform a single
point of failure, and turn it into an attractive target holding broad credentials. What is actually
needed is not a gateway but *one identity on every path*: the UI and `kubectl` reach the same API
server, are stopped by the same RBAC, and land in the same audit log.

This choice collapses what would otherwise be two audit trails into one. See below.

### Who you are is never taken from the request body

Having no mandatory gateway leaves one hole that has to be closed at the API server: `kubectl` can
create a `QuotaRequest` with any `spec.requestedBy` it likes. The control plane setting that field
correctly is not enough — it is only one of the paths in, and it is not the one an attacker would
use.

A mutating admission webhook stamps every attribution field from `userInfo.username` on the
admission request itself, which is the authenticated identity the API server has already verified.
A claimed value is overwritten, an empty one is filled in, and an unauthenticated request is
refused outright.

```
spec.requestedBy   QuotaRequest, EphemeralEnvironment
spec.decidedBy     Decision
spec.renewals[].grantedBy   each entry, as it is appended
```

On update, the field is immutable: rewriting it is denied, naming the value it was created with.
Renewals are append-only for the same reason — an existing entry cannot be edited and the list
cannot shrink, so a lease extension cannot be quietly disowned or erased.

Two consequences follow. Attribution is stored as the Kubernetes username, not the friendlier email
claim, because that is the name that appears in the audit trail and the one RBAC actually evaluated;
the control plane writes the same value so the record is stable whether or not the webhook is
installed. And without a certificate the webhook is not served at all — the platform logs that
attribution is written but *not enforced*, rather than implying a guarantee it is not providing.

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
| `simulate` | the shadow world: cluster snapshot and bin packing, with admission dry-run and cost projection to follow |
| `timeline` | ordered events per workload: rollouts, Kubernetes events, alerts, audit entries |
| `identity` | who the caller is, and what Kubernetes will let them see |
| `admission` | the webhooks that make platform invariants hold for every client, not only ours |

`simulate` and `timeline` exist as kernel modules because several features are the same machine
pointed at different questions.

- `simulate` serves preflight, the approval impact simulator, right-sizing, and billing projection.
  All four are "run this decision in a shadow world and show the difference".
- `timeline` serves the failure explainer and deploy-to-regression correlation. Both need one thing:
  an ordered, queryable history of what happened to a workload.

Building either twice would be the first step toward two answers to the same question.

### Domain

`identity` sits in the kernel rather than beside the domain modules: every read has to know who is
asking, so putting it anywhere else would mean domain modules importing one another.

| Module | Responsibility | Custom resources |
|---|---|---|
| `tenancy` | divisions, namespaces, quota via Capsule, limit ranges, default-deny policy | `Division` |
| `requests` | request lifecycle, recommender, preflight, decisions | `QuotaRequest`, `Decision` |
| `finops` | consumption, rates, chargeback, invoices, idle, right-sizing | `PricingPolicy` |
| `catalog` | workload discovery, classification, ownership | `TierOverride` |
| `signals` | golden signals, SLOs, error budgets, burn rate | `ServiceLevelObjective` |
| `diagnostics` | failure explainer, timeline, rollout correlation | — |
| `delivery` | desired-versus-live from Argo CD, attributed to the actor who changed it | — |
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
which nodes fill up, and which pods would have nowhere to run.

This is packing, not arithmetic. The simulator snapshots every node's free capacity, measures the
division's **typical pod** from what it already runs, fills the requested headroom with pods of that
size, and places them with a best-fit pass that skips cordoned, tainted and not-ready nodes.

That is the difference that matters: arithmetic says 4 spare cores are enough for a 2-core pod, and
it is wrong whenever those cores are 700m at a time across six nodes. The simulation says so, names
the nodes, and the verdict is stored with the decision.

The monthly cost delta joins this view with the FinOps slice; until then it is absent rather than
estimated.

### A grant that lapses is flagged, not reverted

Every approval carries an expiry. When it passes, the decision is marked expired and the request
moves to `Expired` — but the quota itself is left alone. Silently shrinking a live division's quota
because a calendar date passed would turn a governance control into an outage.

The expiry therefore means *this grant is due for review*, and the review is visible rather than
automatic. Grants that genuinely should revoke themselves — access, peering — behave differently,
because taking those back breaks nothing that was not already borrowed.

### Drift trusts Argo, and only adds what Argo cannot say

Argo CD already decides whether live state matches the manifests; re-deriving that would mean two
answers to one question. The platform reports what Argo reports, and adds the one thing Argo has no
way to know: **who changed it**, taken from the audit trail for that exact object.

One heuristic was written and then deleted: comparing `targetRevision` against the synced revision.
For an application tracking a branch, the target is `main` and the revision is a commit SHA, so the
comparison marks every branch-tracking application as drifted forever. The revisions are now shown as
context beside the application name, and the drift verdict comes from Argo alone. A test pins that
behaviour so the heuristic cannot come back.

### A failure is explained, not displayed

`Explain` is a pure function over pods, container states and warning events, and its rules are tried
in order of how much they actually explain:

```
unschedulable   → the scheduler's own sentence, verbatim
out of memory   → the container, its restart count, the limit it hit
crash loop      → the exit code, not just "CrashLoopBackOff"
image pull      → which image, and the registry's reason
failing probe   → running but never ready, with the probe's own message
otherwise       → how many replicas are ready, and the newest warning
```

Order is the design. A container that is OOM killed also shows `CrashLoopBackOff`, and a UI that
renders the first status it finds reports the symptom instead of the cause. The rule that explains
more wins, and there is a test that feeds both signals and asserts the answer is memory.

Every explanation carries the `kubectl` commands that reproduce it — including `logs --previous`
when the evidence is in a container that already died.

### Reachability is policy compared against traffic, and neither alone

Two questions are usually answered separately and badly. *What can talk to what* is read from
NetworkPolicy, and it always over-states: an allowance written two years ago for a service that no
longer exists still reads as a live path. *What does talk to what* is read from trace metrics, and it
always under-states: a path used once a month looks closed.

Putting them side by side is what makes either useful, so a path carries both bits and a verdict
drawn from the pair:

```
allowed  + observed     → allowed and used
allowed  + not observed → an allowance nobody uses — a candidate for removal
denied   + observed     → traffic no policy permits — either a gap or an out-of-band path
denied   + not observed → denied
```

The middle two are the whole point. The first is how a default-deny cluster silently loosens over
time; the second is how you learn a policy is not doing what its author believed.

Two rules keep the verdict honest. A namespace is only treated as guarded when some policy in it
actually lists the `Ingress` policy type — an egress-only policy leaves ingress wide open, and
reading it as closed would report a namespace as protected when it is not. And when no trace metrics
exist, the graph is not drawn at all: `Edges` returns `ErrNoTraces` and the service answers
`Unavailable`. Guessing edges from Services and Endpoints would produce a plausible diagram of calls
that may never happen, which is worse than no diagram. Reachability still answers in that case, with
`traffic_observed` false so the page can say the verdicts come from policy alone.

### A lease is measured from creation, never from the last reconcile

A preview environment holds a real namespace with a real quota, so what makes it ephemeral is the
one thing that must not drift: when it ends. The expiry is `creationTimestamp + granted`, and the
reconciler recomputes that same value every time rather than adding the remaining time to *now*.

The trap this avoids is quiet and common. A controller that writes `expiresAt = now + ttl` on each
pass gives a preview an unbounded life: every restart, every resync, every no-op edit to the spec
renews it. Nobody notices, because the object still says it expires in 48 hours — it has just said
that for three weeks. `TestTheLeaseRunsFromCreationNotFromThisReconcile` reconciles twice with the
clock moved forward between them and asserts the expiry did not move.

Extending a lease is therefore not a clock operation but a recorded one. A renewal is an entry in
`spec.renewals` carrying the extension, a reason, and who granted it; the lease is creation plus the
sum of the grants, capped at a seven-day ceiling. The renewal history survives in the object, so
"why is this preview still alive after five days" has an answer instead of a shrug.

Two more rules keep reclamation honest:

```
namespace missing        → the environment settles as Expired, and stays there
namespace we do not own  → Orphaned: reported, never deleted
```

The second matters more than it looks. The controller derives the namespace name from the division
and the change number, so a name collision with something a human created by hand is possible. Owner
reference, not name, decides whether the controller may delete — a TTL sweeper that deletes by naming
convention is one collision away from removing production.

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

### No cloud price list

Consumption comes from the same Prometheus that decides quota — requested core-hours and GiB-hours,
summed over the period. There is no OpenCost dependency, because billing on *reserved* capacity at
*declared* rates never needs to know what a node cost the company.

That also removes a whole class of disagreement: the number on the invoice and the number in the
quota screen are computed from one series.

| Concern | Handling |
|---|---|
| Rates | `PricingPolicy`, effective-dated and approved |
| Invoices | immutable snapshot citing the `PricingPolicy` version used, so revising a rate never rewrites last month |
| Idle | `requested − used` per division, in rupiah, with the concrete patch that would fix it |
| Unallocated | capacity no division reserved, allocated per `PricingPolicy` (absorbed by the platform, or pro rata) |
| Attribution | a `division_id` label required by Kyverno at admission |

Money is exact. Amounts are rational numbers internally and are only rounded when they are rendered,
so a month billed as 730 separate hours sums to exactly the monthly rate. A test adds those 730 hours
up and fails on the last rupiah if the arithmetic ever drifts.

Month-end invoices — the immutable snapshot citing a policy revision — are not built yet; the running
total is available and says which policy revision produced it.

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

```
hash chain   every event carries the hash of its predecessor
canonical    the exact bytes that were hashed are stored beside the row
archive      append-only segments, written before the batch is acknowledged
WORM         retention set on the storage the archive lives on, not in app config
```

The canonical blob exists because the queryable columns cannot be trusted to round-trip: `jsonb`
reorders keys and `timestamptz` truncates to microseconds, so re-hashing what came back out of the
database would fail on data nobody touched. The blob is what the hash covers; the columns are an
index over it, and verification checks that the identifying fields in the index still agree with what
was signed.

What this buys, stated precisely: the chain makes a rewrite **detectable**, including by someone with
database superuser rights, because they would have to recompute every subsequent hash and still
disagree with the archive. Making a rewrite **impossible** is the storage layer's job.

Ingestion is single-writer because the chain is strictly ordered. That is a deliberate throughput
ceiling in exchange for a property that cannot otherwise be stated honestly.

Testable, and tested: alter one event and verification names it; drop an event from the archive and
verification says which one is missing.

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
