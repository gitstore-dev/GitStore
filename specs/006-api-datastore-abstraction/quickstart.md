# Quickstart: API Datastore Abstraction

**Feature**: `006-api-datastore-abstraction` | **Date**: 2026-05-09

---

## Running locally with the in-memory backend (default)

No external services required. The `memdb` backend is the default.

```bash
# From the repo root
cd gitstore-api

# Copy the example env file
cp .env.example .env
# Ensure GITSTORE_DATASTORE_BACKEND is absent or set to "memdb" in .env

# Start the API
go run ./cmd/server
```

The service starts immediately with an empty in-memory store. Data is lost on restart (expected and documented — see spec.md User Story 2).

---

## Running locally with ScyllaDB

### Prerequisites

- Docker (or Podman) installed
- `gitstore-git-service` running (or mocked)

### 1. Start a local ScyllaDB node

```bash
docker run -d --name scylla-dev \
  -p 9042:9042 \
  scylladb/scylla:5.4 \
  --developer-mode=1 --overprovisioned=1
```

Wait until the node is ready:

```bash
docker exec -it scylla-dev nodetool status
# Wait until the node shows "UN" (Up/Normal)
```

### 2. Configure the API to use ScyllaDB

```bash
# .env (or export these vars)
GITSTORE_DATASTORE_BACKEND=scylla
GITSTORE_DATASTORE_SCYLLA_HOSTS=localhost:9042
GITSTORE_DATASTORE_SCYLLA_KEYSPACE=gitstore
```

Optional credentials and TLS:

```bash
GITSTORE_DATASTORE_SCYLLA_USERNAME=gitstore
GITSTORE_DATASTORE_SCYLLA_PASSWORD=secret
GITSTORE_DATASTORE_SCYLLA_TLS=true
```

### 3. Start the API

```bash
go run ./cmd/server
```

On first start the migration runner:
1. Acquires the distributed migration lock (LWT `INSERT IF NOT EXISTS`)
2. Creates the keyspace and all tables
3. Releases the lock
4. Logs `"all migrations applied"` at INFO level

On subsequent starts the migration runner detects the schema is up-to-date and skips all migration steps, logging `"schema is current, skipping migrations"`.

---

## Configuration reference

All config keys follow the Viper `GITSTORE_` prefix convention from feature 005.

| Config key                  | Env var                              | Default          | Description                    |
|-----------------------------|--------------------------------------|------------------|--------------------------------|
| `datastore.backend`         | `GITSTORE_DATASTORE_BACKEND`         | `memdb`          | `memdb` or `scylla`            |
| `datastore.scylla.hosts`    | `GITSTORE_DATASTORE_SCYLLA_HOSTS`    | `localhost:9042` | Comma-separated ScyllaDB hosts |
| `datastore.scylla.keyspace` | `GITSTORE_DATASTORE_SCYLLA_KEYSPACE` | `gitstore`       | ScyllaDB keyspace              |
| `datastore.scylla.username` | `GITSTORE_DATASTORE_SCYLLA_USERNAME` | _(empty)_        | Optional authentication        |
| `datastore.scylla.password` | `GITSTORE_DATASTORE_SCYLLA_PASSWORD` | _(empty)_        | Optional authentication        |
| `datastore.scylla.tls`      | `GITSTORE_DATASTORE_SCYLLA_TLS`      | `false`          | Enable TLS                     |

An unrecognised backend value causes the service to exit at startup with a message like:

```
FATAL: invalid datastore backend "badvalue"; valid values: memdb, scylla
```

---

## Running the contract test suite

### memdb (no external dependencies)

```bash
cd gitstore-api
go test ./tests/contract/datastore/...
```

### ScyllaDB (requires a running ScyllaDB node)

```bash
# Start a ScyllaDB node via Docker Compose (override file at repo root)
docker compose -f compose.yml -f compose.scylla.yml up -d scylla

# Run with the scylla build tag
go test -tags scylla ./tests/contract/datastore/...
```

Or via testcontainers (automatically starts/stops a Docker container):

```bash
go test -tags scylla -run TestContractScylla ./tests/contract/datastore/...
```

---

## Adding a third backend (extension guide)

The `Datastore` interface is defined in `gitstore-api/internal/datastore/datastore.go`. To add a new backend:

1. Create `gitstore-api/internal/datastore/<name>/backend.go` implementing all methods of `Datastore`.
2. Add a new `BackendType` constant (e.g., `BackendPostgres BackendType = "postgres"`).
3. Add a case to `factory.NewDatastore` in `gitstore-api/internal/datastore/factory.go`.
4. Add a wiring file `gitstore-api/tests/contract/datastore/<name>_test.go` that calls `contract.RunContractSuite(t, newYourBackend(t))`.

No existing backend code needs modification. The `InstrumentedDatastore` decorator wraps the new backend automatically.

---

## Observability

When either backend is active, the following Prometheus metrics are available at `/metrics`:

| Metric                                          | Type      | Labels                 |
|-------------------------------------------------|-----------|------------------------|
| `gitstore_datastore_operation_duration_seconds` | Histogram | `operation`, `backend` |
| `gitstore_datastore_operation_errors_total`     | Counter   | `operation`, `backend` |

Example Prometheus query to alert on elevated datastore error rate:

```promql
rate(gitstore_datastore_operation_errors_total[5m]) > 0.01
```

Example query for p99 read latency:

```promql
histogram_quantile(0.99,
  sum(rate(gitstore_datastore_operation_duration_seconds_bucket{operation=~"Get.*"}[5m]))
  by (le, backend)
)
```
