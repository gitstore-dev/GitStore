# gitstore-controller-manager

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
- Keeps runtime state in memory; this module does not add persistence.

## HTTP Surface

| Route | Purpose |
|---|---|
| `GET /health` | Health status per kind |
| `GET /metrics` | Prometheus metrics |
| `GET /controller/v1/poison/{kind}` | List quarantined items for a kind |
| `GET /controller/v1/poison/_all` | List all quarantined items |
| `POST /controller/v1/poison/{kind}/{namespace}/{name}/requeue` | Requeue a quarantined item |

## Configuration Highlights

| Variable | Default | Purpose |
|---|---|---|
| `GITSTORE_CONTROLLER__PORT` | `5001` | HTTP listen port |
| `GITSTORE_CONTROLLER__API__URI` | `http://localhost:4000/graphql` | API endpoint |
| `GITSTORE_CONTROLLER__DEFAULT_MAX_ATTEMPTS` | `5` | Retry limit before quarantine |
| `GITSTORE_CONTROLLER__DEFAULT_STALL_THRESHOLD` | `5m` | Worker stall threshold |
| `GITSTORE_LOG__LEVEL` | `info` | Log level |
| `GITSTORE_LOG__FORMAT` | `json` | `json` or `text` |

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
