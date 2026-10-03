# Controller Manager

Go controller runtime for GitStore reconciliation loops.

## Purpose

`gitstore-controller-manager` owns shared controller runtime mechanics:

- Level-triggered work queues.
- Worker pools.
- Retry and backoff.
- Poison-item quarantine.
- Panic recovery and stack capture.
- Per-kind health state.
- Prometheus metrics.
- HTTP management endpoints for requeueing quarantined work.

Controllers reconcile through `gitstore-api`; the manager does not talk directly to `gitstore-git-service`.

## Boundaries

- Reads desired state from the API.
- Writes controller-owned status through the API.
- Exposes health, metrics, and poison-item operations on port `5001`.
- Keeps queues, caches and quarantine state in memory; optionally persists
  watch snapshots and replay cursors through the filesystem checkpoint store.

## Replica-safety boundary

The implemented model is duplicate-tolerant, at-least-once reconciliation, not
distributed work partitioning or exactly-once execution. Each process has its
own queue/cache and may reconcile the same resource. Current version-checked
status and lifecycle paths rely on API-side optimistic concurrency, conflict
requeues and idempotent operations; those guarantees must be verified for each
new reconciler and its downstream operations.

Use separate filesystem checkpoint roots per concurrent replica, as
`compose.capacity.yml` does. Atomic rename prevents partial files; it is not
cross-process checkpoint coordination or a monotonic compare-and-swap.

[ADR 0018](../docs/ADRs/0018-controller-ownership-concurrency-and-fencing.md)
is proposed: its general side-effect leases/fencing are not implemented.
Non-idempotent external effects must not assume that only one controller acts.
Deployment safety also depends on the API/datastore/AuthN/AuthZ paths supporting
the same replica topology; these runtime mechanics alone do not establish
blanket production HA. Git service remains singleton-only.

## HTTP Surface

| Route                                                          | Purpose                           |
|----------------------------------------------------------------|-----------------------------------|
| `GET /health`                                                  | Health status per kind            |
| `GET /metrics`                                                 | Prometheus metrics                |
| `GET /controller/v1/poison/{kind}`                             | List quarantined items for a kind |
| `GET /controller/v1/poison/_all`                               | List all quarantined items        |
| `POST /controller/v1/poison/{namespace}/{kind}/{name}/requeue` | Requeue a quarantined item        |

## Configuration Highlights

Pass `--config-file PATH` to require and load an explicit TOML file. Environment
variables override file values. Root `make compose` supplies the shared local
configuration. The controller resolves its private signing key from its own
bootstrap provider and exchanges an assertion for a token; shared config never
contains a bearer token or private key.

Settings use nested `serviceaccount`, `secret_providers.bootstrap`,
`checkpoint`, `reconcile` and `watch` groups, decoded once into typed structs.
Environment names follow `GITSTORE_` plus `__` for dots. Obsolete flat keys fail
with a replacement path, even when canonical environment settings override them.
Keep each binary paired with its matching configuration when replacing replicas.

| Variable                                          | Default                         | Purpose                       |
|---------------------------------------------------|---------------------------------|-------------------------------|
| `GITSTORE_CONTROLLER__PORT`                       | `5001`                          | HTTP listen port              |
| `GITSTORE_CONTROLLER__API_URI`                    | `http://localhost:4000/graphql` | API endpoint                  |
| `GITSTORE_CONTROLLER__RECONCILE__MAX_ATTEMPTS`    | `5`                             | Retry limit before quarantine |
| `GITSTORE_CONTROLLER__RECONCILE__STALL_THRESHOLD` | `5m`                            | Worker stall threshold        |
| `GITSTORE_LOG__LEVEL`                             | `info`                          | Log level                     |
| `GITSTORE_LOG__FORMAT`                            | `json`                          | `json` or `text`              |

Copy the example file for local development:

```bash
cp gitstore-controller-manager/.env.example gitstore-controller-manager/.env
```

## Project Structure

```text
gitstore-controller-manager/
├── cmd/controller/     # Entry point
├── internal/
│   ├── api/            # Poison-item HTTP API
│   ├── cache/          # In-memory accessor/cache helpers
│   ├── config/         # Configuration loading
│   ├── health/         # Health and metrics handlers
│   ├── manager/        # Runtime registration and dispatch
│   ├── queue/          # Work queue
│   ├── retry/          # Retry and quarantine
│   ├── status/         # Status patch helpers
│   ├── types/          # Shared runtime types
│   └── worker/         # Worker pool
├── tests/contract/     # Runtime contract tests
└── go.mod
```

## Commands

From the repository root:

```bash
make controller
make compose DETACH=1
```

From this module:

```bash
go test ./...
go build ./...
```

## Deeper Docs

- [Developer Guide](../docs/developer-guide.md#controller-manager-runtime)
- [Configuration](../docs/configuration.md)
- [025 controller-manager runtime](../specs/025-controller-manager-runtime/quickstart.md)
- [026 reconcile handler](../specs/026-reconcile-handler/quickstart.md)

## License

AGPL-3.0-or-later. See [LICENSE](../LICENSE).
