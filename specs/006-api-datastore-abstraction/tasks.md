# Tasks: API Datastore Abstraction

**Input**: Design documents from `/specs/006-api-datastore-abstraction/`
**Prerequisites**: plan.md ✅ spec.md ✅ research.md ✅ data-model.md ✅ contracts/ ✅ quickstart.md ✅

**Tests**: Test-First Development (Constitution Principle I — NON-NEGOTIABLE). Tests MUST be written before implementation code and verified failing before implementation starts.

**Alpha Note**: This is an alpha release — backwards compatibility is not required. Existing `cache.Manager`, `GRPCLoader`, and `catalog.Catalog` usage in the resolver layer may be replaced directly without migration shims.

**Organization**: Tasks are grouped by user story to enable independent implementation and testing of each story.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no blocking dependencies)
- **[Story]**: Which user story this task belongs to (US1–US4)

---

## Phase 1: Setup

**Purpose**: Add new dependencies and update project scaffolding.

- [X] T001 Add `github.com/hashicorp/go-memdb` dependency to `gitstore-api/go.mod` via `go get github.com/hashicorp/go-memdb` inside `gitstore-api/`
- [X] T002 Add `github.com/scylladb/gocqlx/v3` and `github.com/gocql/gocql` dependencies with `replace github.com/gocql/gocql => github.com/scylladb/gocql v1.18.0` directive in `gitstore-api/go.mod`; run `go mod tidy`
- [X] T003 [P] Update `gitstore-api/.env.example` with all `GITSTORE_DATASTORE_*` env vars from `quickstart.md` config reference table

**Checkpoint**: Dependencies installed; `.env.example` documents all new config keys.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Core infrastructure that MUST be complete before any user story implementation can begin.

**⚠️ CRITICAL**: No user story work can begin until this phase is complete.

- [X] T004 Define `Datastore` interface, sentinel errors (`ErrNotFound`, `ErrAlreadyExists`, `ErrInvalidArgument`), and `ProductFilter` struct in `gitstore-api/internal/datastore/datastore.go` matching `specs/006-api-datastore-abstraction/contracts/datastore.go`
- [X] T005 [P] Add `DatastoreConfig` (Backend string, Scylla ScyllaConfig) and `ScyllaConfig` (Hosts, Keyspace, Username, Password, TLS) structs to `gitstore-api/internal/config/config.go`; register Viper defaults (`datastore.backend = "memdb"`, `datastore.scylla.hosts = ["localhost:9042"]`, `datastore.scylla.keyspace = "gitstore"`, `datastore.scylla.tls = false`)
- [X] T006 [P] Register `gitstore_datastore_operation_duration_seconds` (`HistogramVec`, labels: `operation`, `backend`) and `gitstore_datastore_operation_errors_total` (`CounterVec`, labels: `operation`, `backend`) Prometheus metrics in `gitstore-api/internal/datastore/metrics.go`
- [X] T007 [P] Write failing `InstrumentedDatastore` unit tests in `gitstore-api/internal/datastore/instrumented_test.go`: verify histogram observation on every call, counter increment on non-nil error, zap error log includes `operation`/`backend`/`error`/`duration_ms` fields (MUST FAIL FIRST)
- [X] T008 [P] Implement `InstrumentedDatastore` decorator in `gitstore-api/internal/datastore/instrumented.go` wrapping any `Datastore` with per-operation latency histogram + error counter + zap structured error log; `NewInstrumentedDatastore(next Datastore, backend string, log *zap.Logger) Datastore` (depends on T007)

**Checkpoint**: `Datastore` interface is defined; `DatastoreConfig` structs and Viper defaults exist; `InstrumentedDatastore` decorator passes its unit tests.

---

## Phase 3: User Story 1 — Select Storage Backend via Config (Priority: P1) 🎯 MVP

**Goal**: `gitstore-api` selects the active `Datastore` implementation purely from configuration; invalid backend values cause immediate startup failure with a clear error message; all resolver reads and writes go through the `Datastore` interface.

**Independent Test**: Start `gitstore-api` with `GITSTORE_DATASTORE_BACKEND=invalid_value`; verify process exits < 5 s with message naming the invalid value and listing valid options. After implementing US2, start with `memdb` and execute all CRUD operations via GraphQL; all succeed without any external service running.

### Tests for User Story 1 ⚠️ WRITE FIRST — MUST FAIL BEFORE IMPLEMENTING

- [X] T009 [P] [US1] Write failing config validation tests in `gitstore-api/internal/config/config_test.go`: empty string backend defaults to `"memdb"`; unrecognised value (`"badvalue"`) fails validation with a message containing the value and listing `memdb`, `scylla`; scylla-required fields (`hosts`, `keyspace`) fail validation when `backend="scylla"` and are absent (MUST FAIL FIRST)
- [X] T010 [P] [US1] Write failing factory tests in `gitstore-api/internal/datastore/factory_test.go`: `NewDatastore` with `BackendMemdb` config returns non-nil `Datastore` and nil error; `NewDatastore` with unknown backend returns error whose message contains the invalid value and the strings `"memdb"` and `"scylla"` (MUST FAIL FIRST)
- [X] T011 [P] [US1] Write failing resolver integration tests in `gitstore-api/internal/graph/service_test.go` (or new `gitstore-api/internal/graph/datastore_integration_test.go`): create a product via `Service.CreateProduct`, read it back via `Service.GetProduct`; update it; delete it; verify `ErrNotFound` after deletion — all using a memdb-backed `Datastore` stub (MUST FAIL FIRST)

### Implementation for User Story 1

- [X] T012 [US1] Add `DatastoreConfig` and `ScyllaConfig` validation rules to `gitstore-api/internal/config/config.go`: `backend` must be `"memdb"` or `"scylla"` (case-insensitive); when `backend="scylla"`, `scylla.hosts` and `scylla.keyspace` are required (depends on T009, T005)
- [X] T013 [US1] Implement `factory.NewDatastore(cfg DatastoreConfig, log *zap.Logger) (Datastore, error)` in `gitstore-api/internal/datastore/factory.go`: dispatch `BackendMemdb` to `memdb.New()` and `BackendScylla` to `scylla.New()`; for unknown/empty backend return `fmt.Errorf("invalid datastore backend %q; valid values: memdb, scylla", ...)` (depends on T010, T004, T005, T012)
- [X] T014 [US1] Replace `cache.Manager` with `Datastore` in `graph.Service` struct in `gitstore-api/internal/graph/service.go`: replace `cache *cache.Manager` field with `store datastore.Datastore`; update `NewService` constructor; remove `GRPCLoader` usage from the service layer (depends on T013)
- [X] T015 [US1] Update GraphQL query resolvers in `gitstore-api/internal/graph/` to read from `Datastore` methods (`GetProduct`, `ListProducts`, `GetCategory`, `ListCategories`, `GetCollection`, `ListCollections`, `GetProductBySKU`, `GetCategoryBySlug`, `GetCollectionBySlug`) instead of `catalog.Catalog` methods (depends on T014)
- [X] T016 [US1] Update mutation resolvers in `gitstore-api/internal/graph/service_mutations.go` to write to `Datastore` methods (`CreateProduct`, `UpdateProduct`, `DeleteProduct`, `CreateCategory`, `UpdateCategory`, `DeleteCategory`, `CreateCollection`, `UpdateCollection`, `DeleteCollection`) instead of `CommitFile`/`DeleteFile` git operations; git `CommitFile` calls may be retained for version-history purposes but are no longer the primary write path (depends on T014, T011)
- [X] T017 [US1] Update DataLoaders in `gitstore-api/internal/loader/product_loader.go`, `category_loader.go`, `collection_loader.go` to call `Datastore.GetProduct`, `Datastore.GetCategory`, `Datastore.GetCollection` instead of reading from `*catalog.Catalog` maps (depends on T014)
- [X] T018 [US1] Wire `factory.NewDatastore` into server startup in `gitstore-api/cmd/server/main.go`: call `factory.NewDatastore` with loaded config; wrap result in `NewInstrumentedDatastore`; on error log fatal with structured fields and exit immediately (satisfies SC-002: < 5 s startup failure with clear message) (depends on T013, T008)

**Checkpoint**: Server rejects invalid backend config at startup. With memdb backend (from Phase 4), all GraphQL CRUD operations work without external services. With scylla backend (from Phase 5), all operations work against ScyllaDB.

---

## Phase 4: User Story 2 — In-Memory Backend for Local Development (Priority: P2)

**Goal**: `go-memdb`-backed `Datastore` implementation satisfies the full `Datastore` contract; all 18 CRUD methods work in-process with no external dependencies; `ProductFilter.CategoryID` scoping works.

**Independent Test**: `go test ./gitstore-api/internal/datastore/memdb/...` passes with no build tags; start `gitstore-api` with `GITSTORE_DATASTORE_BACKEND=memdb` and run the full GraphQL CRUD test suite; all pass without any Docker or external service.

### Tests for User Story 2 ⚠️ WRITE FIRST — MUST FAIL BEFORE IMPLEMENTING

- [X] T019 [P] [US2] Write failing memdb backend unit tests in `gitstore-api/internal/datastore/memdb/backend_test.go` (no build tag): cover all 18 `Datastore` methods; `ErrNotFound` on missing ID; `ErrAlreadyExists` on duplicate `CreateProduct` (same ID or same SKU); `ErrAlreadyExists` on duplicate `CreateCategory` (same slug); `ListProducts(ProductFilter{CategoryID: "x"})` returns only products with that category; `GetProductBySKU` returns correct product; restart-equivalent (new `New()` call) has no prior state (MUST FAIL FIRST)

### Implementation for User Story 2

- [X] T020 [P] [US2] Define `go-memdb` table schema in `gitstore-api/internal/datastore/memdb/schema.go`: products table with `id` (`UUIDFieldIndex`), `sku` (`StringFieldIndex`, unique), `category_id` (`StringFieldIndex`, non-unique) indices; categories table with `id` and `slug` (`StringFieldIndex{Lowercase:true}`, unique) indices; collections table with `id` and `slug` indices
- [X] T021 [US2] Implement `memdbDatastore` in `gitstore-api/internal/datastore/memdb/backend.go`: `New() (datastore.Datastore, error)` creates `*memdb.MemDB` from schema; all 18 CRUD methods use read-write `txn` from `db.Txn(true/false)`; `txn.Insert` on create/update; `txn.Delete` on delete; `txn.First` for single-key lookups; `txn.Get` for list operations; wrap `nil` results and key-not-found returns as `datastore.ErrNotFound`; wrap duplicate insert errors as `datastore.ErrAlreadyExists` (depends on T019, T020)
- [X] T022 [US2] Register `memdb.New` in `factory.NewDatastore` for `BackendMemdb` case in `gitstore-api/internal/datastore/factory.go` (depends on T021, T013)

**Checkpoint**: `go test ./gitstore-api/internal/datastore/memdb/...` passes. `gitstore-api` starts with `GITSTORE_DATASTORE_BACKEND=memdb`; all CRUD API calls succeed; data is not present after process restart (expected).

---

## Phase 5: User Story 3 — ScyllaDB Backend with Schema Migrations (Priority: P2)

**Goal**: ScyllaDB-backed `Datastore` satisfies the full contract; schema initialises automatically on first run; pending migrations apply on subsequent runs; distributed LWT lock prevents concurrent migration races; unreachable ScyllaDB causes fast startup failure.

**Independent Test**: Start `gitstore-api` against a fresh ScyllaDB container; schema appears automatically; restart and verify idempotent startup; run `go test -tags scylla ./gitstore-api/internal/datastore/scylla/...` with testcontainers-go.

### Tests for User Story 3 ⚠️ WRITE FIRST — MUST FAIL BEFORE IMPLEMENTING

- [X] T023 [P] [US3] Write failing ScyllaDB backend integration tests (`//go:build scylla`) in `gitstore-api/internal/datastore/scylla/backend_test.go`: use testcontainers-go to start a ScyllaDB container; cover all 18 `Datastore` methods; `ErrNotFound` / `ErrAlreadyExists` sentinel wrapping; `Close()` releases session (MUST FAIL FIRST)
- [X] T024 [P] [US3] Write failing migration runner tests (`//go:build scylla`) in `gitstore-api/internal/datastore/scylla/migration_test.go`: fresh DB creates all tables; second run is a no-op and succeeds; LWT lock is acquired before migrations run; a second concurrent `RunMigrations` call waits or returns a descriptive error when lock is held; unreachable host at connect time returns a clear error within the connect timeout (MUST FAIL FIRST)

### Implementation for User Story 3

- [X] T025 [P] [US3] Write CQL migration files embedded via `//go:embed` in `gitstore-api/internal/datastore/scylla/migrations/`: `001_initial_schema.cql` (CREATE KEYSPACE IF NOT EXISTS, CREATE TABLE products/categories/collections/schema_migrations_lock per `data-model.md`); `002_add_indexes.cql` (CREATE INDEX products_by_sku, products_by_category, categories_by_slug, collections_by_slug)
- [X] T026 [US3] Implement ScyllaDB migration runner with LWT distributed lock in `gitstore-api/internal/datastore/scylla/migration.go`: `acquireLock` via `INSERT INTO schema_migrations_lock … IF NOT EXISTS USING TTL 120` with `ScanCAS`; `releaseLock` via `DELETE … IF holder = ?`; `RunMigrations(ctx, session, instanceID, log)` acquires lock, calls `gocqlx/v3/migrate.FromFS` with embedded `.cql` files, releases lock; logs each migration step at INFO, skipped step at DEBUG, failure at ERROR; exponential back-off retry (max 3 attempts) when lock is held by another instance (depends on T024, T025)
- [X] T027 [US3] Implement `scyllaDatastore` in `gitstore-api/internal/datastore/scylla/backend.go`: `New(cfg ScyllaConfig, log *zap.Logger) (datastore.Datastore, error)` creates `gocql.Cluster` with shard-aware `TokenAwareHostPolicy`; optionally sets `PasswordAuthenticator` and `SslOptions`; calls `RunMigrations`; all 18 CRUD methods use `gocqlx` session queries; wrap `gocql.ErrNotFound` and zero-rows results as `datastore.ErrNotFound`; wrap LWT `applied=false` on insert as `datastore.ErrAlreadyExists` (depends on T023, T026)
- [X] T028 [US3] Register `scylla.New` in `factory.NewDatastore` for `BackendScylla` case in `gitstore-api/internal/datastore/factory.go`; surface ScyllaDB connect or migration errors as fatal startup errors (depends on T027, T013)
- [X] T029 [US3] Add ScyllaDB container service to `gitstore-api/docker-compose.test.yml` (or create it) for use by the `scylla` build-tag integration tests; document start command in `quickstart.md` under "Running the contract test suite / ScyllaDB" section

**Checkpoint**: `go test -tags scylla ./gitstore-api/internal/datastore/scylla/...` passes against a live ScyllaDB container. `gitstore-api` starts with `GITSTORE_DATASTORE_BACKEND=scylla`; fresh schema created; restart is idempotent; unreachable ScyllaDB exits fast with actionable error.

---

## Phase 6: User Story 4 — Consistent Behaviour Across Backends (Priority: P3)

**Goal**: A single backend-agnostic contract test suite passes for both memdb and ScyllaDB backends, proving behavioural parity for all CRUD operations and error categories.

**Independent Test**: `go test ./gitstore-api/tests/contract/datastore/...` passes (memdb); `go test -tags scylla ./gitstore-api/tests/contract/datastore/...` passes (ScyllaDB).

### Tests for User Story 4 ⚠️ THIS PHASE IS THE TESTS — WRITE FIRST

- [X] T030 [P] [US4] Write backend-agnostic CRUD contract suite as `RunContractSuite(t *testing.T, ds datastore.Datastore)` in `gitstore-api/tests/contract/datastore/contract_test.go`: all 18 CRUD operations; `ErrNotFound` for missing ID (all entity types); `ErrAlreadyExists` for duplicate create (all entity types); `ListProducts(ProductFilter{CategoryID: id})` returns only matching products (and empty list for no match); `GetProductBySKU` / `GetCategoryBySlug` / `GetCollectionBySlug` lookups; `UpdateProduct` on non-existent ID returns `ErrNotFound`; `DeleteProduct` on non-existent ID returns `ErrNotFound` (MUST FAIL FIRST)
- [X] T031 [P] [US4] Write memdb contract wiring in `gitstore-api/tests/contract/datastore/memdb_test.go` (no build tag): `TestContractMemdb` calls `RunContractSuite(t, newMemdbDatastore(t))` (MUST FAIL before T030 is implemented)
- [X] T032 [P] [US4] Write ScyllaDB contract wiring (`//go:build scylla`) in `gitstore-api/tests/contract/datastore/scylla_test.go`: `TestContractScylla` uses testcontainers-go to start ScyllaDB; calls `RunContractSuite(t, newScyllaDatastore(t))` (MUST FAIL before T030 is implemented)

### Implementation for User Story 4

- [X] T033 [US4] Run `go test ./gitstore-api/tests/contract/datastore/...` (memdb) and `go test -tags scylla ./gitstore-api/tests/contract/datastore/...` (ScyllaDB); fix any parity gaps between backends (error type mismatches, missing sentinel wrapping, ordering differences) in `gitstore-api/internal/datastore/memdb/backend.go` and `gitstore-api/internal/datastore/scylla/backend.go` until both suites pass

**Checkpoint**: Both `TestContractMemdb` and `TestContractScylla` pass with identical results for all 18 CRUD operations and all error categories. Behavioural parity is verified.

---

## Phase 7: Polish & Cross-Cutting Concerns

- [X] T034 [P] Add `ScyllaDB password` redaction to the existing `config.Redact()` function in `gitstore-api/internal/config/config.go`; add corresponding test in `gitstore-api/internal/config/config_test.go` verifying password is masked in log output
- [X] T035 [P] Write feature documentation in `docs/implementation/`: datastore configuration options (all env vars, defaults, valid values); extension guide for adding a third backend (4-step process from `quickstart.md`); migration behaviour and distributed lock; per-operation observability signals
- [X] T036 Run the full pre-PR checklist from `CLAUDE.md` GitOps section: `go fmt`, `go vet`, `staticcheck`, Go licence header checks (`./scripts/check-go-license-headers.sh --all` and `--diff-base origin/main`), `go build -v ./...`, `go test -v -race -coverprofile=coverage.txt ./...`; fix all reported issues

---

## Dependencies & Execution Order

### Phase Dependencies

```
Phase 1 (Setup)
  └── Phase 2 (Foundational) — blocked by: Phase 1 complete
        └── Phase 3 (US1) — blocked by: Phase 2 complete
              ├── Phase 4 (US2 / memdb) — blocked by: Phase 2 + US1 interface in place
              ├── Phase 5 (US3 / ScyllaDB) — blocked by: Phase 2 + US1 interface in place
              └── Phase 6 (US4 / parity) — blocked by: Phase 4 AND Phase 5 both complete
                    └── Phase 7 (Polish) — blocked by: all story phases complete
```

### User Story Dependencies

- **US1 (P1)**: Depends on Foundational phase (T004–T008). Full independent test requires US2 or US3 backend.
- **US2 (P2a)**: Depends on Foundational + US1 interface (T004, T013). Independent of US3.
- **US3 (P2b)**: Depends on Foundational + US1 interface (T004, T013). Independent of US2.
- **US4 (P3)**: Depends on US2 AND US3 both complete. Cannot verify parity without both backends.

### Within Each Phase

- Test tasks (marked MUST FAIL FIRST) are written before implementation tasks in the same phase
- Models/schema before service implementations within a phase
- Factory registration last within each backend phase (ensures backend is complete before wiring)

---

## Parallel Opportunities

### Phase 2 (Foundational)
```
T005 (config structs)   ─────┐
T006 (metrics)          ─────┤→ T008 (InstrumentedDatastore)
T007 (instrumented tests)─────┘
```

### Phase 3 (US1)
```
T009 (config tests) ──→ T012 (config validation)
T010 (factory tests)──→ T013 (factory impl)    ──→ T018 (server wiring)
T011 (resolver tests)──────────────────────────→ T016 (mutation resolvers)
```
T015, T016, T017 (resolver updates) are independent of each other after T014.

### Phase 4 (US2)
```
T019 (memdb tests)   ──→ T021 (memdb impl)
T020 (schema.go)     ──┘
```

### Phase 5 (US3)
```
T023 (scylla backend tests) ──→ T027 (scylla backend impl)
T024 (migration tests) ──→ T026 (migration runner)
T025 (CQL files)       ──┘
```
T025 and T024 are independent of T023.

### Phase 6 (US4)
```
T030, T031, T032 — all three contract test files can be written in parallel
```

### Phase 7 (Polish)
```
T034 (config redaction) ──┐
T035 (docs)              ──┤→ T036 (pre-PR checks, depends on all)
```

---

## Implementation Strategy

### MVP First (User Stories 1 + 2 Only)

1. Complete Phase 1: Setup (T001–T003)
2. Complete Phase 2: Foundational (T004–T008)
3. Complete Phase 3: US1 (T009–T018) — factory, wiring, resolver integration
4. Complete Phase 4: US2 (T019–T022) — memdb backend
5. **STOP and VALIDATE**: Start server with `GITSTORE_DATASTORE_BACKEND=memdb`; run full GraphQL CRUD test suite; all pass without external services
6. Deploy/demo the in-memory-backed service

### Incremental Delivery

1. Setup + Foundational → Interface and config in place
2. US1 + US2 → Fully functional service with memdb backend (**MVP shippable**)
3. US3 → ScyllaDB backend + migrations → Production-ready
4. US4 → Parity verified between backends
5. Polish → Pre-PR checks pass, docs complete

### Parallel Team Strategy

After Phase 2 (Foundational) completes:
- Developer A: US1 (factory + server wiring + resolver integration)
- Developer B: US2 (memdb backend) — can start as soon as T004 (interface) is done
- Developer C: US3 (ScyllaDB backend) — can start as soon as T004 (interface) is done

US4 parity tests require both US2 and US3 complete.

---

## Summary

| Phase                 | User Story                | Tasks        | Parallel Opportunities             |
|-----------------------|---------------------------|--------------|------------------------------------|
| Phase 1: Setup        | —                         | T001–T003    | T003                               |
| Phase 2: Foundational | —                         | T004–T008    | T005, T006, T007, T008             |
| Phase 3: US1 (P1) 🎯  | Select Backend via Config | T009–T018    | T009, T010, T011; T015, T016, T017 |
| Phase 4: US2 (P2a)    | In-Memory Backend         | T019–T022    | T019, T020                         |
| Phase 5: US3 (P2b)    | ScyllaDB Backend          | T023–T029    | T023, T024, T025                   |
| Phase 6: US4 (P3)     | Parity Contract Tests     | T030–T033    | T030, T031, T032                   |
| Phase 7: Polish       | —                         | T034–T036    | T034, T035                         |
| **Total**             |                           | **36 tasks** |                                    |

- **Suggested MVP scope**: Phases 1–4 (T001–T022, US1 + US2)
- **All tests are written before implementation** (Constitution Principle I)
- **Each user story is independently testable** after its phase completes
