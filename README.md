# marstack-govern

Multi-tenancy and governance for a Kubernetes cluster shared by many divisions.

Divisions ask for things — a namespace, more quota, access, a preview environment — and a platform
team decides. marstack-govern runs that loop with one rule: **every fact it shows is derived from the
platform**. Nobody types what is running, who owns it, or how much it costs.

The binary is `margov`.

## Why

Three failures repeat across internal developer platforms:

- **Hand-written catalogs go stale.** A portal that asks teams to maintain a descriptor file ends up
  describing a cluster that no longer exists.
- **Governance state drifts.** Quota in a Git file, RBAC in the cluster, history in the portal's
  database — three sources that disagree.
- **Approvals are made blind.** The only step still performed by a human is the step with the least
  support. An approver sees prose, not consequence.

marstack-govern answers those in order: the catalog is discovered, governance state lives in the
Kubernetes API, and an approval shows a simulation of its own effect before it is granted.

## What it does

| | |
|---|---|
| **Discovers** | every workload, its owner, its source repository and its signature — from informers, audit, and image provenance. No registration step, no catalog file |
| **Fills in requests** | a quota request opens pre-filled from 30 days of usage, with the date the current quota runs out |
| **Runs preflight** | a server-side apply with `dryRun=All` through the real admission chain plus Kyverno, before anything is applied |
| **Simulates approvals** | cluster commitment, which nodes stop fitting, which pods go `Pending`, and how the monthly bill moves |
| **Charges back** | consumption from OpenCost times rates from a versioned `PricingPolicy`, billed on requests, in rupiah |
| **Records decisions** | with the evidence the approver saw, in a hash-chained, WORM-backed audit trail |
| **Explains failures** | events, exit codes, OOM kills, probe failures and scheduler reasons into one causal sentence, with the `kubectl` command to reproduce it |

## The rule

Humans supply **intent** (raise my quota, approve, reject) and **policy** (the rupiah rate per vCPU,
Kyverno rules). Everything else is read from the Kubernetes API, the audit log, the OCI registry,
Mimir, Loki, Tempo, Kyverno, Trivy, OpenCost, Argo CD and Hubble.

The rule is enforced mechanically, not by discipline: the PostgreSQL read model is a projection, and
a test drops it, replays it from the platform, and compares. A column whose only source is a form
cannot survive that replay.

See [ARCHITECTURE.md](ARCHITECTURE.md) for the full design.

## Status

Working end to end: workloads are discovered, divisions are reconciled into namespaces with quota and
isolation, sign-in is OIDC with Kubernetes deciding what each person may see, and quota requests
arrive with a number proposed from observed usage and are decided against recorded evidence.

Scheduler simulation, FinOps, audit integrity, supply chain and diagnostics are the slices after
this one.

## Prerequisites

marstack-govern composes rather than reimplements. A target cluster is expected to run:

| Component | Purpose | Required |
|---|---|---|
| [Capsule](https://projectcapsule.dev) | tenants and cross-namespace quota | yes |
| [Kyverno](https://kyverno.io) | admission policy, and preflight evaluation | yes |
| PostgreSQL | the read model | yes |
| An OIDC provider (Keycloak, Dex) | identity and group claims | yes |
| [Cilium](https://cilium.io) | network policy, and Hubble flows for reachability | recommended |
| [Argo CD](https://argo-cd.readthedocs.io) | GitOps, and desired-versus-live for drift | for delivery features |
| Mimir / Loki / Tempo | metrics, logs, traces | for signals, recommendations and chargeback |

Missing optional components degrade specific features and say so in the UI. They never produce a
guess.

## Quickstart

```sh
make tools     # staticcheck, govulncheck, gosec, buf
make hooks     # point git at .githooks
make build     # ./bin/margov, with the web UI embedded
```

Point it at a database and a cluster:

```sh
createdb govern

./bin/margov serve \
  --database-url postgres://localhost:5432/govern \
  --kube-context kind-govern \
  --secure-cookies=false \
  --insecure-dev-identity "you@example.test:payments-admins"
```

`--insecure-dev-identity` signs every visitor in as that subject, with those group claims, and says
so loudly in the log and across the top of the page. It exists so a local run does not need an
identity provider. Against a real cluster, use OIDC instead:

```sh
./bin/margov serve \
  --database-url "$GOVERN_DATABASE_URL" \
  --oidc-issuer https://keycloak.example.test/realms/platform \
  --oidc-client-id margov \
  --oidc-redirect-url https://govern.example.test/auth/callback \
  --session-key "$GOVERN_SESSION_KEY"
```

Group claims decide everything: they are matched against the `access` grants on each `Division`, and
what a signed-in user actually sees is then confirmed with Kubernetes through a
`SelfSubjectAccessReview` under their own identity. The portal can never show more than the same
person's `kubectl` would.

To have quota requests arrive with a proposed number, point it at Prometheus or Mimir:

```sh
  --metrics-url http://mimir.monitoring:9009/prometheus \
  --metrics-tenant payments
```

Without it, requests still work — they simply say that no number could be proposed, rather than
inventing one. The same metrics feed chargeback, and the rates come from a `PricingPolicy`:

```yaml
apiVersion: govern.marstack.io/v1alpha1
kind: PricingPolicy
metadata:
  name: standard
spec:
  currency: IDR
  rates:
    cpuCoreMonth: "150000"
    memoryGiMonth: "25000"
    storageGiMonth: "2000"
  effectiveFrom: "2026-01-01"
  unallocated: Platform
  approvedBy: finance@example.test
  approvedAt: "2025-12-18T09:00:00Z"
```

Divisions are billed for what they **reserved**, not what they used — reserved capacity is what other
divisions cannot have. What was reserved and never used is shown beside the bill as idle.

Migrations run on start. Open `http://localhost:8080` and the table fills itself from whatever the
cluster is already running — scale a deployment in another terminal and the row updates without a
refresh.

A throwaway cluster to try it against:

```sh
kind create cluster --name govern
kubectl apply -f deploy/crd
```

Declare a division and the namespaces provision themselves:

```yaml
apiVersion: govern.marstack.io/v1alpha1
kind: Division
metadata:
  name: payments
spec:
  displayName: Payments
  environments: [dev, staging, prod]
  quota:
    cpu: "8"
    memory: 16Gi
    storage: 100Gi
    pods: 50
  limits:
    defaultRequestCpu: 250m
    defaultRequestMemory: 512Mi
    maxCpuPerPod: "2"
    maxMemoryPerPod: 4Gi
  access:
    - role: admin
      group: payments-admins
    - role: viewer
      group: payments-readers
```

```sh
kubectl apply -f division.yaml
kubectl get division payments
kubectl -n payments-dev create deployment api --image ghcr.io/nginxinc/nginx-unprivileged:alpine
```

Three namespaces appear, each with a default-deny network policy, a limit range, a quota, and role
bindings for the two group claims. Nothing about them was typed twice.

Working on the UI:

```sh
make web-dev   # vite on :5173, proxying the API to :8080
make web       # production build into internal/web/dist
```

## Repository map

```
cmd/margov/          the binary
api/v1alpha1/        the Division custom resource
deploy/crd/          generated CRD manifests
proto/               service contracts, the single source for Go and TypeScript clients
gen/                 generated Go bindings
internal/
  api/               ConnectRPC handler, SSE hub, static assets
  catalog/           workload discovery: store, projector, service
  cli/               command tree
  db/                connection pool, embedded migrations, migration runner
  identity/          sessions, OIDC, impersonation, authorization
  kube/              client, impersonation, informers, workload conversion
  metrics/           Prometheus and Mimir queries
  requests/          quota requests, recommender, preflight, decisions
  tenancy/           division controller, projector, service
  version/           build metadata
  web/dist/          built UI, embedded into the binary
web/                 the UI source
docs/                data model and API contracts
ARCHITECTURE.md      design, invariants, and the reasoning behind each boundary
```

Modules land under `internal/` as they are built. Each one owns its custom resources, controller,
projector, queries and handlers as a vertical slice; domain modules do not import one another.

## Tests

```sh
make test
```

Tests that need PostgreSQL skip themselves unless `GOVERN_TEST_DATABASE_URL` points at a throwaway
database — they reset its `public` schema on every run.

```sh
GOVERN_TEST_DATABASE_URL=postgres://localhost:5432/govern_test make test
```

The one worth reading first is `TestClusterReachesTheApiWithoutAnyoneTypingAnything` in
`internal/catalog`: it starts a fake Kubernetes API, runs the real informers, projector, store and
handler, and asserts that a deployment reaches the API and the event stream without any registration
step.

## Development

```sh
make test         # go test -race ./...
make fmt          # gofmt -l -w .
make check        # everything CI runs
```

The pre-commit hook runs gofmt, vet, a cross build, tests, staticcheck, gitleaks and gosec. Install
it with `make hooks`.

## License

Apache 2.0. See [LICENSE](LICENSE).
