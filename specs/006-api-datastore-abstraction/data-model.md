# Data Model: API Datastore Abstraction

**Feature**: `006-api-datastore-abstraction` | **Date**: 2026-05-09

---

## Entities

All entities are re-used from `gitstore-api/internal/catalog/catalog.go`. The datastore abstraction stores and retrieves these structs without modification. No new domain types are introduced for the entity layer.

### Product

| Field               | Type             | Constraints                                     |
|---------------------|------------------|-------------------------------------------------|
| `ID`                | `string` (UUID)  | Required, unique primary key                    |
| `SKU`               | `string`         | Required, unique across all products            |
| `Title`             | `string`         | Required                                        |
| `Price`             | `float64`        | Required, ≥ 0                                   |
| `Currency`          | `string`         | Required (e.g., `"USD"`)                        |
| `InventoryStatus`   | `string`         | Required (e.g., `"in_stock"`, `"out_of_stock"`) |
| `InventoryQuantity` | `*int`           | Optional (nil = untracked)                      |
| `CategoryID`        | `string`         | Optional; references `Category.ID`              |
| `CollectionIDs`     | `[]string`       | Optional; references `Collection.ID` values     |
| `Images`            | `[]string`       | Optional                                        |
| `Metadata`          | `map[string]any` | Optional, freeform                              |
| `CreatedAt`         | `time.Time`      | Set on create                                   |
| `UpdatedAt`         | `time.Time`      | Updated on every write                          |
| `Body`              | `string`         | Optional Markdown content                       |

**Indices (memdb)**:
- `id` (primary) — `UUIDFieldIndex{Field: "ID"}` — unique
- `sku` — `StringFieldIndex{Field: "SKU"}` — unique
- `category_id` — `StringFieldIndex{Field: "CategoryID"}` — non-unique (used for `ListProducts(filter)`)

**ScyllaDB table**:
```cql
CREATE TABLE products (
    id           uuid PRIMARY KEY,
    sku          text,
    title        text,
    price        decimal,
    currency     text,
    inventory_status   text,
    inventory_quantity int,
    category_id  uuid,
    collection_ids list<uuid>,
    images       list<text>,
    metadata     map<text, text>,
    created_at   timestamp,
    updated_at   timestamp,
    body         text
);

-- Secondary index for SKU lookups
CREATE INDEX products_by_sku ON products (sku);

-- Secondary index for category-filtered listing
CREATE INDEX products_by_category ON products (category_id);
```

---

### Category

| Field          | Type            | Constraints                        |
|----------------|-----------------|------------------------------------|
| `ID`           | `string` (UUID) | Required, unique primary key       |
| `Name`         | `string`        | Required                           |
| `Slug`         | `string`        | Required, unique                   |
| `ParentID`     | `*string`       | Optional; references `Category.ID` |
| `DisplayOrder` | `int`           | Optional, defaults 0               |
| `CreatedAt`    | `time.Time`     | Set on create                      |
| `UpdatedAt`    | `time.Time`     | Updated on every write             |
| `Body`         | `string`        | Optional Markdown content          |

**Computed fields** (`Parent`, `Children`, `Path`, `Depth`) are **not stored** by the datastore. They are built by the existing `BuildCategoryHierarchy` function in `catalog.go` after loading from the datastore, preserving the existing behaviour.

**Indices (memdb)**:
- `id` (primary) — `UUIDFieldIndex{Field: "ID"}` — unique
- `slug` — `StringFieldIndex{Field: "Slug", Lowercase: true}` — unique

**ScyllaDB table**:
```cql
CREATE TABLE categories (
    id            uuid PRIMARY KEY,
    name          text,
    slug          text,
    parent_id     uuid,
    display_order int,
    created_at    timestamp,
    updated_at    timestamp,
    body          text
);

CREATE INDEX categories_by_slug ON categories (slug);
```

---

### Collection

| Field          | Type            | Constraints                              |
|----------------|-----------------|------------------------------------------|
| `ID`           | `string` (UUID) | Required, unique primary key             |
| `Name`         | `string`        | Required                                 |
| `Slug`         | `string`        | Required, unique                         |
| `DisplayOrder` | `int`           | Optional, defaults 0                     |
| `ProductIDs`   | `[]string`      | Optional; references `Product.ID` values |
| `CreatedAt`    | `time.Time`     | Set on create                            |
| `UpdatedAt`    | `time.Time`     | Updated on every write                   |
| `Body`         | `string`        | Optional Markdown content                |

**Indices (memdb)**:
- `id` (primary) — `UUIDFieldIndex{Field: "ID"}` — unique
- `slug` — `StringFieldIndex{Field: "Slug", Lowercase: true}` — unique

**ScyllaDB table**:
```cql
CREATE TABLE collections (
    id            uuid PRIMARY KEY,
    name          text,
    slug          text,
    display_order int,
    product_ids   list<uuid>,
    created_at    timestamp,
    updated_at    timestamp,
    body          text
);

CREATE INDEX collections_by_slug ON collections (slug);
```

---

## Supporting Entities

### DatastoreConfig

New config struct added to `gitstore-api/internal/config/config.go`.

| Field     | Viper key           | Env var                      | Default   | Constraints                               |
|-----------|---------------------|------------------------------|-----------|-------------------------------------------|
| `Backend` | `datastore.backend` | `GITSTORE_DATASTORE_BACKEND` | `"memdb"` | Must be `"memdb"` or `"scylla"`           |
| `Scylla`  | `datastore.scylla`  | —                            | —         | Only validated when `Backend == "scylla"` |

### ScyllaConfig

| Field      | Viper key                   | Env var                              | Default              | Constraints                     |
|------------|-----------------------------|--------------------------------------|----------------------|---------------------------------|
| `Hosts`    | `datastore.scylla.hosts`    | `GITSTORE_DATASTORE_SCYLLA_HOSTS`    | `["localhost:9042"]` | Required when backend is scylla |
| `Keyspace` | `datastore.scylla.keyspace` | `GITSTORE_DATASTORE_SCYLLA_KEYSPACE` | `"gitstore"`         | Required when backend is scylla |
| `Username` | `datastore.scylla.username` | `GITSTORE_DATASTORE_SCYLLA_USERNAME` | `""`                 | Optional                        |
| `Password` | `datastore.scylla.password` | `GITSTORE_DATASTORE_SCYLLA_PASSWORD` | `""`                 | Optional                        |
| `TLS`      | `datastore.scylla.tls`      | `GITSTORE_DATASTORE_SCYLLA_TLS`      | `false`              | Optional                        |

---

## Migration State (ScyllaDB only)

### `gocqlx_migrate` (managed by `gocqlx/v3/migrate`)

| Field        | Type        | Notes                              |
|--------------|-------------|------------------------------------|
| `name`       | `text` (PK) | Migration filename                 |
| `checksum`   | `text`      | SHA of the file; detects tampering |
| `done`       | `int`       | Statement-level progress counter   |
| `start_time` | `timestamp` | When migration began               |
| `end_time`   | `timestamp` | When migration completed           |

### `schema_migrations_lock` (custom, for FR-008a)

| Field         | Type        | Notes                                 |
|---------------|-------------|---------------------------------------|
| `lock_key`    | `text` (PK) | Always `"migration"`                  |
| `holder`      | `text`      | UUID of the instance holding the lock |
| `acquired_at` | `timestamp` | For observability / debugging         |

Lock is acquired via LWT `INSERT IF NOT EXISTS USING TTL 120`. Released via LWT `DELETE IF holder = ?`. TTL ensures self-expiry after 120 s if the holder crashes before release.

---

## State Transitions

### Backend Selection (startup)

```
Config read
   │
   ├─ backend == "memdb"  → NewMemdbDatastore()  → ready
   ├─ backend == "scylla" → connect to ScyllaDB
   │                          ├─ unreachable  → fail fast (SC-002)
   │                          └─ reachable    → acquireMigrationLock()
   │                                              ├─ lock held → wait / timeout → fail
   │                                              └─ lock acquired → RunMigrations()
   │                                                  ├─ up-to-date → releaseLock → ready
   │                                                  └─ migrations applied → releaseLock → ready
   └─ unknown value → fail immediately with ErrInvalidBackend (SC-002)
```

### Error Propagation

```
Backend error
   │
   └─ Wrapped into sentinel error (ErrNotFound / ErrAlreadyExists / ErrInvalidArgument)
         │
         └─ Caller receives sentinel; backend-specific error available via errors.Unwrap()
```

The abstraction never retries internally (FR-007a). Transient ScyllaDB errors propagate to the caller as-is (wrapped in a `fmt.Errorf("...: %w", backendErr)`).

---

## Query Patterns Supported

| Operation                          | Lookup path                 | Backend implementation                                                                                         |
|------------------------------------|-----------------------------|----------------------------------------------------------------------------------------------------------------|
| `GetProduct(id)`                   | Primary key                 | memdb: `txn.First("product","id",id)` / ScyllaDB: `SELECT … WHERE id = ?`                                      |
| `GetProductBySKU(sku)`             | SKU secondary index         | memdb: `txn.First("product","sku",sku)` / ScyllaDB: `SELECT … WHERE sku = ?` (secondary index)                 |
| `ListProducts(filter{categoryID})` | CategoryID non-unique index | memdb: `txn.Get("product","category_id",catID)` / ScyllaDB: `SELECT … WHERE category_id = ?` (secondary index) |
| `ListProducts(filter{})`           | Full scan                   | memdb: `txn.Get("product","id")` / ScyllaDB: `SELECT … FROM products`                                          |
| `GetCategory(id)`                  | Primary key                 | Standard single-row lookup                                                                                     |
| `GetCategoryBySlug(slug)`          | Slug secondary index        | memdb slug index / ScyllaDB secondary index                                                                    |
| `ListCategories()`                 | Full scan                   | All rows in table                                                                                              |
| `GetCollection(id)`                | Primary key                 | Standard single-row lookup                                                                                     |
| `GetCollectionBySlug(slug)`        | Slug secondary index        | memdb slug index / ScyllaDB secondary index                                                                    |
| `ListCollections()`                | Full scan                   | All rows in table                                                                                              |
