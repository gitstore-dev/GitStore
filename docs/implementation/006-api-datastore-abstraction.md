# Feature 006: API Datastore Abstraction

## Overview

Introduces a pluggable `Datastore` interface that decouples the GraphQL API from
its persistence layer. The first two backends are **memdb** (in-memory, default)
and **ScyllaDB** (production). All resolver reads and writes go through the
interface; backends are selected at startup via configuration.

---

## Configuration

All settings follow the `GITSTORE_` Viper prefix (feature 005).

| Config key                  | Env var                              | Default          | Description                              |
|-----------------------------|--------------------------------------|------------------|------------------------------------------|
| `datastore.backend`         | `GITSTORE_DATASTORE_BACKEND`         | `memdb`          | `memdb` or `scylla`                      |
| `datastore.scylla.hosts`    | `GITSTORE_DATASTORE_SCYLLA_HOSTS`    | `localhost:9042` | Comma-separated host:port pairs          |
| `datastore.scylla.keyspace` | `GITSTORE_DATASTORE_SCYLLA_KEYSPACE` | `gitstore`       | ScyllaDB keyspace                        |
| `datastore.scylla.username` | `GITSTORE_DATASTORE_SCYLLA_USERNAME` | _(empty)_        | Optional authentication username         |
| `datastore.scylla.password` | `GITSTORE_DATASTORE_SCYLLA_PASSWORD` | _(empty)_        | Optional authentication password         |
| `datastore.scylla.tls`      | `GITSTORE_DATASTORE_SCYLLA_TLS`      | `false`          | Enable TLS for ScyllaDB connections      |

An unrecognised `backend` value causes the service to exit at startup with a
clear message naming the invalid value and listing valid options.
The `password` field is always redacted in structured startup logs.

---

## Schema migrations (ScyllaDB only)

Migrations are embedded via `//go:embed migrations/*.cql` and applied
automatically at startup via `gocqlx/v3/migrate.FromFS`.

**Distributed lock** — before applying migrations each instance acquires
a lease with `INSERT INTO gitstore.schema_migrations_lock … IF NOT EXISTS USING TTL 120`.
If the lock is already held, the runner retries up to 3 times with
exponential back-off (2 s, 4 s, 8 s). After success the lock row is deleted.
The 120-second TTL self-expires if the holder crashes before releasing.

Migration files live in `internal/datastore/scylla/migrations/`:

| File                     | Purpose                                                                                                |
|--------------------------|--------------------------------------------------------------------------------------------------------|
| `001_initial_schema.cql` | Creates `gitstore` keyspace + `products`, `categories`, `collections`, `schema_migrations_lock` tables |
| `002_add_indexes.cql`    | Secondary indexes for SKU, category, and slug lookups                                                  |

---

## Observability

Both backends are wrapped in `InstrumentedDatastore` which records:

| Metric                                          | Type      | Labels                 |
|-------------------------------------------------|-----------|------------------------|
| `gitstore_datastore_operation_duration_seconds` | Histogram | `operation`, `backend` |
| `gitstore_datastore_operation_errors_total`     | Counter   | `operation`, `backend` |

On error, the decorator additionally logs a structured `ERROR` entry with
`operation`, `backend`, `error`, and `duration_ms` fields via zap.

---

## Adding a fourth backend

1. Create `internal/datastore/<name>/backend.go` implementing all 19 methods of
   `datastore.Datastore` (18 CRUD + `Close`).
2. Add a `case "<name>":` branch in `internal/datastore/factory/factory.go`.
3. Add `tests/contract/datastore/<name>_test.go` calling
   `RunContractSuite(t, newYourBackend(t))`.
4. Optionally add a build tag (e.g. `//go:build <name>`) if the backend requires
   a live external service.

No existing backend code changes are needed. `InstrumentedDatastore` wraps the
new backend automatically via the factory.

---

## Contract test suite

The shared suite in `tests/contract/datastore/contract_test.go` verifies
behavioural parity between backends:

```bash
# memdb (no external services)
go test ./tests/contract/datastore/...

# ScyllaDB contract tests (requires an external ScyllaDB on 127.0.0.1:9042)
GITSTORE_TEST_SCYLLA_ADDR=127.0.0.1:9042 go test -tags scylla -timeout 10m ./tests/contract/datastore/...

# ScyllaDB backend unit tests + migration tests
GITSTORE_TEST_SCYLLA_ADDR=127.0.0.1:9042 go test -tags scylla -timeout 10m ./internal/datastore/scylla/...
```

Start the override compose stack at the repo root before running ScyllaDB tests:

```bash
docker compose -f compose.yml -f compose.scylla.yml up -d scylla
```
