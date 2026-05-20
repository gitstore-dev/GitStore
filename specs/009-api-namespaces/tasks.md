# Tasks: API-Driven Namespace Lifecycle Management

**Input**: Design documents from `/specs/009-api-namespaces/`
**Branch**: `009-api-namespaces`

**Tests**: Test-First Development (Constitution Principle I — NON-NEGOTIABLE). Test tasks within each story MUST be written before implementation tasks and verified to fail before proceeding.

**Organization**: Tasks are grouped by user story to enable independent implementation and testing.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies on incomplete tasks)
- **[Story]**: Which user story this task belongs to (US1, US2, US3)
- Exact file paths are given in each task description

## Key decisions (from research.md)

- GraphQL schema uses `extend type Query` / `extend type Mutation` — do NOT modify `schema.graphqls`
- Schema files use `.graphqls` extension (see `gqlgen.yml`)
- ScyllaDB: modify existing `001_initial_schema.cql` — no new migration file (early alpha, no backwards-compat guarantee)
- Auth: `IsAdmin` is the elevated platform role; ownership checked via `CreatedBy` at query time — no changes to `Claims` struct
- Deletion guard for repositories is a no-op stub (repository table does not exist yet)

---

## Phase 1: Setup

**Purpose**: Create the GraphQL schema contract and regenerate gqlgen code; polish the existing auth resolver.

- [X] T001 Create `shared/schemas/namespace.graphqls` with `NamespaceTier` enum, `Namespace` type (implementing `Node`), `CreateNamespaceInput`, `DeleteNamespaceInput`, `CreateNamespacePayload`, `DeleteNamespacePayload`, and `extend type Query` / `extend type Mutation` entries (do NOT touch `schema.graphqls`; follow the `auth.graphqls` `extend type` pattern)
- [X] T002 Run `cd gitstore-api && go generate ./...` to regenerate `internal/graph/generated/generated.go`, `internal/graph/model/models_gen.go`, and the `namespace.resolvers.go` resolver stub
- [X] T003 [P] Polish `gitstore-api/internal/graph/auth.resolvers.go`: replace the naive implementation with proper structured logging (`r.logger`), `gqlerror.Errorf` error responses, correct sentinel-error mapping (`ErrNotFound` → not-found, `ErrInvalidCredentials` → unauthenticated), and remove any `panic` stubs — consistent with the `handler.NewLoginHandler` pattern in `internal/handler/login.go`

**Checkpoint**: GraphQL schema is defined; gqlgen artifacts reflect the new types; auth resolver is production-quality.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Persistence layer support for namespaces — must be complete before any user story can be implemented.

**⚠️ CRITICAL**: No user story work can begin until this phase is complete.

- [X] T004 [P] Add `NamespaceTier` type and constants (`user`, `organisation`, `enterprise`) plus the `Namespace` struct (all fields from `data-model.md`) to `gitstore-api/internal/datastore/entities.go`
- [X] T005 Extend the `Datastore` interface in `gitstore-api/internal/datastore/datastore.go` with five namespace methods: `CreateNamespace`, `GetNamespace`, `GetNamespaceByIdentifier`, `ListNamespaces`, `DeleteNamespace` — following the existing error contract (`ErrNotFound`, `ErrAlreadyExists`, `ErrInvalidArgument`)
- [X] T006 [P] Add the namespaces table and identifier index to `gitstore-api/internal/datastore/scylla/migrations/001_initial_schema.cql` (append after the collections index block — no new migration file): `CREATE TABLE IF NOT EXISTS namespaces` with columns `id text PRIMARY KEY`, `identifier text`, `display_name text`, `tier text`, `parent_enterprise_id text`, `created_at timestamp`, `created_by text`, `updated_at timestamp`, `updated_by text`; and `CREATE INDEX IF NOT EXISTS namespaces_by_identifier ON namespaces (identifier)`
- [X] T007 Add the `"namespaces"` table to the memdb `DBSchema` in `gitstore-api/internal/datastore/memdb/schema.go` with three indices: `id` (unique, `StringFieldIndex{Field: "ID"}`), `identifier` (unique, `StringFieldIndex{Field: "Identifier"}`), `tier` (non-unique, `StringFieldIndex{Field: "Tier"}`)
- [X] T008 Implement `CreateNamespace`, `GetNamespace`, `GetNamespaceByIdentifier`, `ListNamespaces`, `DeleteNamespace` on `memdbDatastore` in `gitstore-api/internal/datastore/memdb/backend.go` — follow the existing `CreateCategory`/`GetCategoryBySlug`/`ListCategories`/`DeleteCategory` pattern; `CreateNamespace` must check both `id` and `identifier` uniqueness within the same write transaction
- [X] T009 Add `namespaceRow` struct with `db:""` tags, `namespaceTable *table.Table` field to `scyllaDatastore`, initialize the table in `New()`, and implement `CreateNamespace`, `GetNamespace`, `GetNamespaceByIdentifier`, `ListNamespaces`, `DeleteNamespace` in `gitstore-api/internal/datastore/scylla/backend.go` — follow the `CreateCategory`/`GetCategoryBySlug`/`ListCategories`/`DeleteCategory` pattern; timestamps use `time.Time` in the struct mapped to `timestamp` columns via gocqlx
- [X] T010 Add five namespace wrapper methods (`CreateNamespace`, `GetNamespace`, `GetNamespaceByIdentifier`, `ListNamespaces`, `DeleteNamespace`) to `InstrumentedDatastore` in `gitstore-api/internal/datastore/instrumented.go` — each method records latency and error count via `d.observe(opName, start, err)`, identical in structure to the existing category wrappers

**Checkpoint**: `go build ./...` passes in `gitstore-api/`; all existing tests still pass; namespace interface is fully implemented in both backends.

---

## Phase 3: User Story 1 — Create a Namespace (Priority: P1) 🎯 MVP

**Goal**: A permitted caller can create a namespace; duplicate and invalid-identifier requests are rejected deterministically; enterprise creation is gated on `isAdmin`.

**Independent Test**: Submit a `createNamespace` mutation and verify the response contains `id`, `identifier`, `tier`, `createdAt`, `createdBy`; verify a second call with the same identifier returns a conflict error.

### Tests for User Story 1 ⚠️ Write FIRST — verify they FAIL before T013

- [X] T011 [P] [US1] Extend `gitstore-api/tests/contract/datastore/contract_test.go` with namespace datastore contract tests: `TestCreateNamespace_success`, `TestCreateNamespace_duplicateIdentifier` (returns `ErrAlreadyExists`), `TestCreateNamespace_acrossAllTiers` (same identifier conflicts regardless of tier), `TestGetNamespaceByIdentifier_notFound`
- [X] T012 [P] [US1] Create `gitstore-api/internal/graph/namespace_service_test.go` with GraphQL-level integration tests for `createNamespace`: success (user tier), success (org tier), conflict (duplicate identifier), invalid identifier format (spaces, uppercase, leading hyphen), reserved identifier (`admin`), enterprise tier without `isAdmin` returns permission-denied

### Implementation for User Story 1

- [X] T013 [US1] Add `CreateNamespace(ctx, input, callerUsername, isAdmin)` to `gitstore-api/internal/graph/service.go`: lowercase + validate identifier against regex `^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`, check reserved names list (from `research.md`), enforce enterprise-tier-requires-admin gate, resolve `parentEnterpriseIdentifier` → `ParentEnterpriseID` via `GetNamespaceByIdentifier`, set `CreatedAt`/`CreatedBy`/`UpdatedAt`/`UpdatedBy`, call `store.CreateNamespace`; map `ErrAlreadyExists` → GraphQL conflict error, `ErrInvalidArgument` → validation error
- [X] T014 [US1] Implement the `createNamespace` mutation resolver body in `gitstore-api/internal/graph/namespace.resolvers.go`: extract `claims` from context via `middleware.GetUserFromContext`, require authentication (`claims == nil` → unauthenticated error), call `r.service.CreateNamespace`, return `CreateNamespacePayload{Namespace: model}`
- [X] T015 [P] [US1] Add `datastoreNamespaceToModel(ns *datastore.Namespace) *model.Namespace` converter to `gitstore-api/internal/graph/converters.go` mapping all fields including `Tier` (`NamespaceTier` string → `model.NamespaceTier` enum) and `ParentEnterpriseID` (`*string` → `*string`)

**Checkpoint**: `createNamespace` mutation works end-to-end; all T011/T012 tests pass; User Story 1 is independently testable via quickstart.md.

---

## Phase 4: User Story 2 — List and Retrieve Namespaces (Priority: P2)

**Goal**: A caller can list all namespaces and retrieve one by identifier; not-found is returned deterministically.

**Independent Test**: Create two namespaces, call `namespaces` query, verify both appear; call `namespace(by: {identifier: "..."})` query, verify full metadata is returned; call `namespace(by: {identifier: "unknown-ns"})`, verify not-found error.

### Tests for User Story 2 ⚠️ Write FIRST — verify they FAIL before T018

- [X] T016 [P] [US2] Extend `gitstore-api/tests/contract/datastore/contract_test.go` with `TestListNamespaces_empty`, `TestListNamespaces_multiple`, `TestGetNamespace_byID_success`, `TestGetNamespaceByIdentifier_success`, `TestGetNamespaceByIdentifier_notFound`
- [X] T017 [P] [US2] Add `namespaces` query and `namespace(by: {identifier})` query integration tests to `gitstore-api/internal/graph/namespace_service_test.go`: list returns all created namespaces, get by identifier returns correct metadata, get unknown identifier returns not-found error

### Implementation for User Story 2

- [X] T018 [US2] Add `GetNamespaceByIdentifier(ctx, identifier)`, `GetNamespaceByID(ctx, id)`, and `ListNamespaces(ctx)` service methods to `gitstore-api/internal/graph/service.go` — map `ErrNotFound` to GraphQL not-found error; log `zap.String("identifier", ...)` on not-found
- [X] T019 [US2] Implement `namespace(by:)` and `namespaces` query resolver bodies in `gitstore-api/internal/graph/namespace.resolvers.go` — all reads are unauthenticated (no auth gate needed per spec); call respective service methods; return `nil, gqlerror` on not-found

**Checkpoint**: All three query resolvers work; T016/T017 tests pass; User Stories 1 and 2 are independently functional.

---

## Phase 5: User Story 3 — Delete a Namespace (Priority: P3)

**Goal**: An authorised caller can delete an empty namespace; deletion is blocked when repositories exist; unauthorised callers are rejected; not-found is returned for non-existent namespaces.

**Independent Test**: Create a namespace, delete it (authorised caller), verify it no longer appears in list; attempt delete as non-owner/non-admin, verify permission-denied; attempt delete of non-existent namespace, verify not-found.

### Tests for User Story 3 ⚠️ Write FIRST — verify they FAIL before T022

- [X] T020 [P] [US3] Extend `gitstore-api/tests/contract/datastore/contract_test.go` with `TestDeleteNamespace_success`, `TestDeleteNamespace_notFound` (returns `ErrNotFound`), `TestDeleteNamespace_thenGetReturnsNotFound`
- [X] T021 [P] [US3] Add `deleteNamespace` mutation integration tests to `gitstore-api/internal/graph/namespace_service_test.go`: owner can delete own namespace, admin can delete any namespace, non-owner non-admin gets permission-denied, unknown identifier gets not-found, unauthenticated caller gets unauthenticated error

### Implementation for User Story 3

- [X] T022 [US3] Add `DeleteNamespace(ctx, identifier, callerUsername, isAdmin)` service method to `gitstore-api/internal/graph/service.go`: look up namespace by identifier (not-found → error), check ownership (`ns.CreatedBy == callerUsername || isAdmin` — else permission-denied), call `hasRepositories` stub (always returns `false`; comment `// TODO: enforce when repositories table exists`), call `store.DeleteNamespace`; map errors appropriately
- [X] T023 [US3] Implement the `deleteNamespace` mutation resolver body in `gitstore-api/internal/graph/namespace.resolvers.go`: require authentication, call `r.service.DeleteNamespace`, return `DeleteNamespacePayload{DeletedIdentifier: input.Identifier}`

**Checkpoint**: All three user stories are functional; T020/T021 tests pass; `deleteNamespace` works end-to-end.

---

## Phase 6: Polish & Cross-Cutting Concerns

- [X] T024 [P] Update `docs/architecture.md` with a namespace lifecycle management section: three tiers, global identifier uniqueness, auth model (`IsAdmin` as elevated role), `gitstore-git-service` boundary (FR-011), and `curl` / GraphQL Playground examples referencing `quickstart.md`
- [X] T025 [P] Verify SPDX license headers on all new files (`shared/schemas/namespace.graphqls`, new Go files): run `./scripts/check-go-license-headers.sh --diff-base origin/main` and `./scripts/check-js-license-headers.sh --diff-base origin/main`; add missing `// SPDX-License-Identifier: AGPL-3.0-or-later` headers
- [X] T026 Run full pre-PR validation in `gitstore-api/`: `go vet ./...`, `staticcheck ./...`, `go build -v ./...`, `go test -v -race -coverprofile=coverage.txt -covermode=atomic ./...`; fix any failures before opening the PR

---

## Dependencies & Execution Order

### Phase Dependencies

- **Phase 1 (Setup)**: No prerequisites — start immediately; T003 is fully independent of T001/T002
- **Phase 2 (Foundational)**: Requires T001 and T002 complete; T004 and T006 can run in parallel; T005 requires T004; T007–T010 require T005
- **User Story Phases (3–5)**: All require Phase 2 complete; stories can then proceed in sequence (P1 → P2 → P3) or in parallel by different developers
- **Phase 6 (Polish)**: Requires all desired stories complete

### User Story Dependencies

- **US1 (P1)**: Requires Phase 2 — no other story dependency
- **US2 (P2)**: Requires Phase 2 — no dependency on US1 (reads are independent of create at the datastore level; but testing US2 in isolation requires a created namespace, so implement US1 first for practical reasons)
- **US3 (P3)**: Requires Phase 2 — deletion test requires a namespace to exist, so US1 must be complete first

### Within Each User Story

1. Test tasks (T01x, T01x) MUST be written and verified to FAIL
2. Converter (T015) can be written in parallel with service (T013)
3. Service (T013) must complete before resolver (T014)
4. All tasks within a story complete before moving to next priority

---

## Parallel Opportunities

```
Phase 1:
  T001 (schema) → T002 (generate)     [sequential — generate reads schema]
  T003 (auth polish)                   [fully independent, any time]

Phase 2:
  T004 (entities.go)  ─────────────────────────────────────────────────────┐
  T006 (migration)    [parallel with T004]                                  │
                      → T005 (interface) → T007 (memdb schema)              │
                                         → T008 (memdb backend)             │
                                         → T009 (scylla backend)            │
                                         → T010 (instrumented)  ────────────┘

Phase 3 (US1):
  T011 (datastore tests)   [parallel]
  T012 (GraphQL tests)     [parallel]
  T015 (converter)         [parallel with T013]
  T013 (service) → T014 (resolver)
```

---

## Implementation Strategy

### MVP First (User Story 1 Only)

1. Complete Phase 1 (T001–T003)
2. Complete Phase 2 (T004–T010) — BLOCKS all stories
3. Complete Phase 3 (T011–T015)
4. **STOP and VALIDATE**: run `go test ./...`; exercise `createNamespace` via playground
5. Ship if ready; continue to US2/US3 on next iteration

### Incremental Delivery

1. Phase 1 + Phase 2 → persistence foundation ready
2. Phase 3 (US1: create) → **MVP** — namespaces can be created
3. Phase 4 (US2: list/get) → operational visibility — admins can inspect namespaces
4. Phase 5 (US3: delete) → full lifecycle complete
5. Phase 6 → polish and documentation

Each story adds measurable value and does not break previous stories.

---

## Notes

- `[P]` tasks operate on different files with no incomplete-task dependencies — safe to run in parallel
- `[Story]` label maps task to its user story for traceability and independent testing
- The `gqlgen`-generated `namespace.resolvers.go` will contain panic stubs after T002; do not edit it manually before implementing — gqlgen will overwrite it on the next `go generate`
- After T002, the file `gitstore-api/internal/graph/namespace.resolvers.go` will contain auto-generated stubs; implement the resolver bodies in that file without regenerating (or use `preserve_resolver: true` if re-running gqlgen mid-implementation)
- ScyllaDB contract tests (in `scylla_test.go`) require Docker via testcontainers; they run automatically if `SCYLLA_TEST=1` is set; memdb tests run without Docker
- The deletion guard for repositories (`hasRepositories` stub) must remain as a no-op in this spec; do NOT implement it — it will be filled when the repository spec lands
