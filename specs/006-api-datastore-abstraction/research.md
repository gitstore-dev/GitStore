# Research: API Datastore Abstraction

**Feature**: `006-api-datastore-abstraction` | **Date**: 2026-05-09

---

## 1. In-Memory Backend: `hashicorp/go-memdb` vs Plain Maps

**Decision**: Use `github.com/hashicorp/go-memdb` for the in-memory backend.

**Rationale**: The catalogue has three entity types (`Product`, `Category`, `Collection`), each requiring multiple lookup paths:

- Product: by UUID, by SKU, by `CategoryID`
- Category: by UUID, by slug
- Collection: by UUID, by slug

With plain `map[string]T + sync.RWMutex`, each secondary index requires a separate map and all three must be kept in sync under a global lock on every write. `go-memdb` declares all indices in a schema and updates them atomically in a single `txn.Insert` call, using an MVCC radix-tree internally. Read transactions via `db.Txn(false)` are free, lock-free snapshots — reads never block writes.

**Specific go-memdb features used**:
- `StringFieldIndex` for slug, SKU, CategoryID lookups
- `UUIDFieldIndex` for primary ID index
- `CompoundIndex` (future: if composite queries are needed)
- `txn.First(table, index, args...)` for single-item lookups
- `txn.Get(table, index)` for list/scan operations

**Alternatives considered**:
- `sync.Map` — no secondary indices, no atomic multi-table writes, worse ergonomics than plain maps
- Plain maps with `sync.RWMutex` — manual index management, no MVCC, acceptable for small scale but fragile at table growth

**SpiceDB datastore patterns influence**:

SpiceDB's `internal/datastore/memdb` uses `go-memdb` with a `CompoundIndex` for its 6-field relationship primary key. Its key architectural insight — separating `Reader` (snapshot reads) from `ReadWriteTransaction` (writes that own commit/rollback) — informs our `Datastore` interface design:

```go
// Datastore owns lifecycle; callers call Read* methods directly.
// Writes go through RunInTx to ensure the backend owns commit/rollback.
type Datastore interface {
    ReadProduct(ctx, id)     (*catalog.Product, error)
    WriteProduct(ctx, p)     error
    DeleteProduct(ctx, id)   error
    // ... similar for Category, Collection
    RunInTx(ctx, func(DatastoreTx) error) error
    Close() error
}
```

---

## 2. ScyllaDB Driver

**Decision**: `github.com/gocql/gocql` import path, redirected to `github.com/scylladb/gocql v1.18.0` via `go.mod replace` directive.

**Rationale**: The ScyllaDB fork is a drop-in API replacement for the upstream Cassandra driver with shard-aware routing built in. Shard-aware routing eliminates inter-shard network hops — each query is routed directly to the shard that owns the partition key token. For production ScyllaDB workloads this is mandatory, not optional.

**go.mod change**:
```
require github.com/gocql/gocql v1.7.0
replace github.com/gocql/gocql => github.com/scylladb/gocql v1.18.0
```

All `.go` imports remain `github.com/gocql/gocql` unchanged.

**Session configuration**:
```go
cluster := gocql.NewCluster(cfg.Hosts...)
cluster.Keyspace = cfg.Keyspace
cluster.Consistency = gocql.LocalQuorum
cluster.PoolConfig.HostSelectionPolicy = gocql.TokenAwareHostPolicy(
    gocql.DCAwareRoundRobinPolicy(""),
)
if cfg.Username != "" {
    cluster.Authenticator = gocql.PasswordAuthenticator{
        Username: cfg.Username,
        Password: cfg.Password,
    }
}
if cfg.TLS {
    cluster.SslOpts = &gocql.SslOptions{EnableHostVerification: true}
}
```

**Alternatives considered**:
- `gocql/gocql` upstream — works but no shard-aware routing; leaves performance on the table for ScyllaDB targets
- `scylladb/gocqlx` as primary driver — `gocqlx` is a convenience wrapper, not a driver; still requires `gocql` underneath

---

## 3. ScyllaDB Schema Migrations

**Decision**: `github.com/scylladb/gocqlx/v3/migrate` for migration execution, wrapped with a custom LWT distributed lock (FR-008a).

**Rationale**: `gocqlx/v3/migrate` is the ScyllaDB-native migration runner. It:
- Reads `.cql` files from any `fs.FS` (supports `embed.FS` for bundled migrations)
- Tracks per-statement progress in a `gocqlx_migrate` table, enabling safe resume after partial failure
- Awaits schema agreement between statements (configurable)
- Requires `scylladb/gocql` v1.x+ (already selected above)

`golang-migrate` was evaluated but rejected: it has no distributed lock, uses the upstream `gocql` driver internally (meaning we would need two drivers), and has naive CQL multi-statement parsing.

**Custom LWT lock wrapping `gocqlx/v3/migrate`**:

```go
// Lock table schema (created in migration 001_initial_schema.cql)
// CREATE TABLE schema_migrations_lock (
//   lock_key   text PRIMARY KEY,
//   holder     text,
//   acquired_at timestamp
// );

func acquireLock(s *gocql.Session, instanceID string) (bool, error) {
    var holder string
    applied, err := s.Query(
        `INSERT INTO schema_migrations_lock (lock_key, holder, acquired_at)
         VALUES ('migration', ?, toTimestamp(now()))
         IF NOT EXISTS USING TTL 120`,
        instanceID,
    ).ScanCAS(&holder)
    return applied, err
}

func releaseLock(s *gocql.Session, instanceID string) error {
    applied, err := s.Query(
        `DELETE FROM schema_migrations_lock
         WHERE lock_key = 'migration'
         IF holder = ?`,
        instanceID,
    ).ScanCAS()
    if !applied { return errors.New("lock not held by this instance") }
    return err
}
```

The lock TTL (120 s) prevents deadlock from a crashed holder. The runner waits with exponential back-off and a configurable timeout when the lock is held.

---

## 4. Datastore Interface Design

**Decision**: Single flat `Datastore` interface with CRUD methods per entity type and a `RunInTx` for atomic multi-entity writes. No snapshot-revision concept (unlike SpiceDB) — the catalogue workload does not require multi-revision consistency.

**Sentinel errors** (backend-agnostic, defined in `datastore.go`):
- `ErrNotFound` — entity does not exist
- `ErrAlreadyExists` — entity with same primary key already present
- `ErrInvalidArgument` — bad input (empty ID, nil entity, etc.)

All backend errors are wrapped into one of these sentinels using `errors.Is` chains so callers never import backend-specific error types.

**Key methods**:
```go
type Datastore interface {
    // Product
    CreateProduct(ctx context.Context, p *catalog.Product) error
    GetProduct(ctx context.Context, id string) (*catalog.Product, error)
    GetProductBySKU(ctx context.Context, sku string) (*catalog.Product, error)
    ListProducts(ctx context.Context, filter ProductFilter) ([]*catalog.Product, error)
    UpdateProduct(ctx context.Context, p *catalog.Product) error
    DeleteProduct(ctx context.Context, id string) error

    // Category
    CreateCategory(ctx context.Context, c *catalog.Category) error
    GetCategory(ctx context.Context, id string) (*catalog.Category, error)
    GetCategoryBySlug(ctx context.Context, slug string) (*catalog.Category, error)
    ListCategories(ctx context.Context) ([]*catalog.Category, error)
    UpdateCategory(ctx context.Context, c *catalog.Category) error
    DeleteCategory(ctx context.Context, id string) error

    // Collection
    CreateCollection(ctx context.Context, c *catalog.Collection) error
    GetCollection(ctx context.Context, id string) (*catalog.Collection, error)
    GetCollectionBySlug(ctx context.Context, slug string) (*catalog.Collection, error)
    ListCollections(ctx context.Context) ([]*catalog.Collection, error)
    UpdateCollection(ctx context.Context, c *catalog.Collection) error
    DeleteCollection(ctx context.Context, id string) error

    // Lifecycle
    Close() error
}
```

`ProductFilter` is a struct carrying optional `CategoryID string` and pagination cursors to support existing `products(filter:{categoryId:})` GraphQL field.

---

## 5. Observability Pattern

**Decision**: `InstrumentedDatastore` decorator (wraps any `Datastore` implementation).

**Rationale**: Putting instrumentation in each backend creates duplication and makes it easy to miss a new method. A decorator wraps the interface once and intercepts every call. The backend stays focused on storage logic.

**Prometheus metrics** (registered on the existing `prometheus.DefaultRegisterer`):
- `gitstore_datastore_operation_duration_seconds` — `HistogramVec`, labels: `operation`, `backend`
- `gitstore_datastore_operation_errors_total` — `CounterVec`, labels: `operation`, `backend`

Histogram is chosen over Summary because histograms are aggregatable across multiple API instances (`histogram_quantile()` works server-side on Prometheus), which Summaries are not.

**zap structured error log** (on any non-nil error returned):
```go
logger.Error("datastore operation failed",
    zap.String("operation", op),
    zap.String("backend", backend),
    zap.Error(err),
    zap.Int64("duration_ms", dur.Milliseconds()),
)
```

---

## 6. Configuration Keys

Following feature 005 conventions (`GITSTORE_*` prefix, Viper mapstructure):

```toml
[datastore]
backend = "memdb"         # or "scylla"

[datastore.scylla]
hosts    = ["localhost:9042"]
keyspace = "gitstore"
username = ""             # optional
password = ""             # optional
tls      = false          # optional, defaults to false
```

Environment variable equivalents:
- `GITSTORE_DATASTORE_BACKEND`
- `GITSTORE_DATASTORE_SCYLLA_HOSTS`
- `GITSTORE_DATASTORE_SCYLLA_KEYSPACE`
- `GITSTORE_DATASTORE_SCYLLA_USERNAME`
- `GITSTORE_DATASTORE_SCYLLA_PASSWORD`
- `GITSTORE_DATASTORE_SCYLLA_TLS`

Validation rule: `GITSTORE_DATASTORE_BACKEND` must be one of `{"memdb", "scylla"}` (case-insensitive). An empty or unrecognised value causes `factory.NewDatastore` to return an error before the HTTP server starts (satisfying SC-002: startup failure < 5 s with actionable message).

---

## 7. Contract Test Strategy

**Decision**: A single `contract_test.go` file defines a `RunContractSuite(t, ds Datastore)` function. Two wiring files — `memdb_test.go` (no build tag) and `scylla_test.go` (`//go:build scylla`) — call this suite with their respective backend instances.

This pattern ensures:
- The in-memory suite runs in every `go test ./...` invocation in CI (no external service required)
- The ScyllaDB suite runs when `go test -tags scylla` is specified (Docker Compose / testcontainers in CI)
- Adding a third backend means adding a single new wiring file — no modification to the contract suite

The suite covers all CRUD operations, `ErrNotFound` on missing IDs, `ErrAlreadyExists` on duplicate creates, and `ProductFilter.CategoryID` scoping.
