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

Early. The scaffold, the architecture, the data model and the API contracts are in place; the control
plane is being built slice by slice. Not usable yet.

## Prerequisites

marstack-govern composes rather than reimplements. A target cluster is expected to run:

| Component | Purpose | Required |
|---|---|---|
| [Capsule](https://projectcapsule.dev) | tenants and cross-namespace quota | yes |
| [Kyverno](https://kyverno.io) | admission policy, and preflight evaluation | yes |
| PostgreSQL | the read model | yes |
| An OIDC provider (Keycloak, Dex) | identity and group claims | yes |
| [Cilium](https://cilium.io) | network policy, and Hubble flows for reachability | recommended |
| [OpenCost](https://opencost.io) | consumption data for chargeback | for FinOps features |
| [Argo CD](https://argo-cd.readthedocs.io) | GitOps, and desired-versus-live for drift | for delivery features |
| Mimir / Loki / Tempo | metrics, logs, traces | for signals and diagnostics |

Missing optional components degrade specific features and say so in the UI. They never produce a
guess.

## Quickstart

```sh
make tools     # staticcheck, govulncheck, gosec
make hooks     # point git at .githooks
make build     # ./bin/margov
make check     # vet, cross build, test, staticcheck, security
```

```sh
./bin/margov version
```

A local cluster with the required components, and the control plane running against it, arrives with
the first slice.

## Repository map

```
cmd/margov/        the binary
internal/
  cli/             command tree
  version/         build metadata
ARCHITECTURE.md    design, invariants, and the reasoning behind each boundary
```

Modules land under `internal/` as they are built. Each one owns its custom resources, controller,
projector, queries and handlers as a vertical slice; domain modules do not import one another.

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
