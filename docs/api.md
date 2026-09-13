# API contracts

One set of `.proto` files in `proto/marstack/govern/v1` defines the whole surface. ConnectRPC serves
them three ways from a single handler — gRPC, gRPC-Web, and Connect's plain HTTP/JSON — so the web
UI, the CLI and a `kubectl` plugin share one contract and one authorization path. That is invariant 3
from [ARCHITECTURE.md](../ARCHITECTURE.md) made concrete: there is no endpoint the UI can reach that
a CLI cannot.

Protos carry no comments; the reasoning lives here.

## Services

| Service | File | Covers |
|---|---|---|
| `TenancyService` | `tenancy.proto` | divisions, namespaces, members, cluster capacity |
| `CatalogService` | `catalog.proto` | discovered workloads, provenance, timeline, failure explanation, drift |
| `RequestService` | `requests.proto` | recommendation, preflight, submission, lifecycle |
| `DecisionService` | `decisions.proto` | approval queue, impact simulation, decisions, revocation |
| `FinOpsService` | `finops.proto` | cost, invoices, right-sizing, pricing policy |
| `AuditService` | `audit.proto` | audit stream, chain verification |
| `PolicyService` | `policy.proto` | guardrail catalog, violations, compliance |
| `TopologyService` | `topology.proto` | traced service graph, network reachability |
| `EnvironmentService` | `environments.proto` | preview environments, leases, renewals |

`common.proto` holds shared types; `events.proto` defines the live stream payloads.

## Every response carries `Freshness`

```proto
message Freshness {
  google.protobuf.Timestamp observed_at = 1;
  string resource_version = 2;
  int64 projection_lag_seconds = 3;
  repeated DegradedSource degraded = 4;
}
```

A read served from the projection must say how old it is and which sources are unhealthy. Clients
render that; they never present projected data as if it were live. A dashboard that cannot state its
own age will eventually lie, and the lie will be believed exactly when it matters — during an
incident.

`degraded` is how a missing dependency reaches the user. If Mimir is down, a recommendation is absent
and the reason is named. Nothing is guessed to fill the gap.

## Authorization: no ambient authority

Every division-scoped RPC takes the division as an explicit field. The server never infers it from
the session, even though the session holds a default.

Two reasons. A user who belongs to several divisions would otherwise have their actions attributed to
whichever division the UI happened to have selected — and the audit record would be ambiguous
forever. And an explicit field is checkable: the server authorizes by impersonating the caller
against the Kubernetes API, so permission is decided by RBAC, not by application code remembering to
check.

The practical consequence: the API can never do more than the caller's own `kubectl` could. A
confused-deputy bug is not merely unlikely, it has nowhere to live.

## Errors

Connect status codes carry the class; `ErrorDetail` carries the specifics.

| Situation | Code | `ErrorDetail.Reason` |
|---|---|---|
| RBAC refused | `permission_denied` | `REASON_NOT_PERMITTED` |
| Kyverno or admission rejected the shape | `failed_precondition` | `REASON_POLICY_REJECTED` |
| Quota would be exceeded | `failed_precondition` | `REASON_QUOTA_EXCEEDED` |
| Preflight did not pass | `failed_precondition` | `REASON_PREFLIGHT_FAILED` |
| Projection too stale to answer | `unavailable` | `REASON_STALE_PROJECTION` |
| Mimir, OpenCost, Kyverno unreachable | `unavailable` | `REASON_SOURCE_UNAVAILABLE` |
| Approval lapsed before it was applied | `failed_precondition` | `REASON_DECISION_EXPIRED` |
| Resource version conflict | `aborted` | `REASON_CONFLICT` |
| Unknown uid | `not_found` | `REASON_NOT_FOUND` |

`ErrorDetail` names the offending `fields`, the `policy` that rejected them, and a `remediation`
string. A rejection that cannot point at a field is not specific enough to be worth returning.

Preflight failures are **not** errors. `Preflight` returns `PreflightResult` with `admitted = false`
and the full list of rejections, because being told exactly why a request will fail is a successful
answer to the question that was asked.

## The write path

Writes are intent, never fact. Each write becomes a custom resource created under the caller's own
identity, so it passes through admission and lands in the Kubernetes audit log.

```
RecommendQuota   →  a proposed number, derived from 30 days of usage
Preflight        →  dry-run through the real admission chain
SubmitRequest    →  create the custom resource
Simulate         →  what granting it would do
Decide           →  approve or reject, with evidence and an expiry
```

### `SubmitRequest` takes an `idempotency_key`

A retried submission after a timeout must not produce a second request. The key is stored on the
resource and a repeat returns the original.

### `Decide` takes an `evidence_digest`

The digest identifies the simulation and recommendation the approver was looking at. If the cluster
has moved since — a new workload landed, capacity changed — the digest no longer matches and the call
is rejected with `REASON_CONFLICT`.

This is optimistic concurrency applied to a human decision. Without it, an approver can study an
impact simulation, take five minutes to think, and approve against a world that no longer exists.
Their reasoning would be recorded as evidence for an outcome it never actually justified.

### Approvals must be bounded and evidenced

`DecideRequest.grant_duration` is required for approvals, and the resulting `Decision` stores the
`Evidence` — recommendation, preflight result, simulation, and when it was captured. The same two
rules are enforced again as database constraints, because a rule worth having is worth enforcing in
more than one place.

## The live stream

Unary RPCs for reads and writes; one server-sent events endpoint for live updates:

```
GET /v1/events?division=payments&cursor=<cursor>
```

Each frame is a JSON-encoded `StreamEvent` whose `id` is the cursor. On reconnect the browser sends
`Last-Event-ID` automatically and the server resumes from that point.

SSE rather than Connect server-streaming, deliberately: the resume semantics match what is underneath.
A Kubernetes watch resumes from a `resourceVersion`, and `Last-Event-ID` carries exactly that through
a reconnect without any application-level bookkeeping. Connect streaming would share the proto
contract but would need its own resume protocol invented on top.

The payloads stay in proto — `StreamEvent` and its `oneof` bodies — so the stream and the RPCs cannot
drift apart. Clients decode SSE frames with the same generated types they use everywhere else.

Two event types exist for the health of the stream itself. `Heartbeat` keeps intermediaries from
closing an idle connection and lets the client detect a dead link. `ProjectionDegraded` pushes source
failures without waiting for the next poll, so the UI's degradation banner appears when the problem
starts rather than when the user next clicks something.

## Pagination

`Page { size, token }` in, `PageInfo { next_token, total }` out. Cursor-based, because offsets skip
or duplicate rows when the underlying set changes between pages — and on a live cluster it always
does.

## Versioning

The package is `marstack.govern.v1`. `buf` is configured with `breaking.use: FILE`, so a change that
would break a generated client fails CI. Breaking changes mean `v2` alongside `v1`, not an edit to
`v1`.

## Code generation

```sh
make proto      # buf lint, buf format, buf generate
```

`buf.gen.yaml` produces Go message and ConnectRPC handler code into `gen/`, and TypeScript for the
web client into `web/src/gen`. Generated code is committed, so a clone builds without `buf` installed
and a diff shows exactly what a contract change did to the generated surface.
