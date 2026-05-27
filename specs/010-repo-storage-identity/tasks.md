---
description: "Task list for Repository Storage Identity and Path Strategy"
---

# Tasks: Repository Storage Identity and Path Strategy

**Input**: Design documents from `/specs/010-repo-storage-identity/`
**Prerequisites**: plan.md ✓, spec.md ✓, research.md ✓, data-model.md ✓, contracts/ ✓

**Tests**: Test-First Development (Constitution Principle I — NON-NEGOTIABLE). Tests MUST be written and confirmed failing before each implementation task.

**Organization**: Tasks are grouped by user story to enable independent implementation and testing.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no incomplete-task dependencies)
- **[Story]**: Which user story this task belongs to (US1–US4)
- Exact file paths included in all task descriptions

---

## Phase 1: Setup (Contracts — blocks everything)

**Purpose**: API-First gate (Constitution Principle II). All contracts must be committed before any implementation.

- [ ] T001 Update `shared/proto/gitstore/git/v1/git_service.proto` from `specs/010-repo-storage-identity/contracts/grpc.git_service.proto` — add `storage_class` field to `CreateRepositoryRequest`, `storage_path` to `CreateRepositoryResponse`, and update `repository_id` comments to reflect UUIDv7 semantics
- [ ] T002 [P] Add `specs/010-repo-storage-identity/contracts/graphql.repository.graphqls` content to `shared/schemas/repository.graphqls` (new file) — `Repository` type, `RepositoryEdge`, `RepositoryConnection`, `RepositoryBy`, `RepositoryNamespacePath`, all four mutations (`createRepository`, `renameRepository`, `transferRepository`, `deleteRepository`) with their input/payload types; add `clientMutationId: String` to every mutation input and payload type
- [ ] T003 Update `gitstore-api/internal/datastore/scylla/migrations/001_initial_schema.cql` in-place — add `repositories` table and `namespace_mappings` table as defined in `specs/010-repo-storage-identity/data-model.md` (D-003: no new migration files)
- [ ] T004 [P] Update `gitstore-api/internal/datastore/scylla/migrations/002_add_initial_indices.cql` in-place — add `repositories_by_namespace` and `mappings_by_repo_id` indices
- [ ] T005 Regenerate gRPC Go bindings from `shared/proto/gitstore/git/v1/git_service.proto` using `protoc` — verify generated files in `api/gen/gitstore/git/v1/`
- [ ] T006 Regenerate gqlgen model/resolver stubs (`go generate ./...` in `gitstore-api/`) — verify new `Repository`, `NamespaceMapping` types and resolver interfaces appear in `gitstore-api/internal/graph/`

**Checkpoint**: Contracts committed; generated stubs compile. No implementation yet.

---

## Phase 2: Foundational (Go data layer — blocks US1, US2, US3)

**Purpose**: `Repository` and `NamespaceMapping` entities plus their datastore interface methods. Must be complete before any GraphQL resolver or gRPC work.

**⚠️ CRITICAL**: Test-First — write failing tests before each implementation step.

- [ ] T007 Write failing tests for `Repository` and `NamespaceMapping` CRUD operations in `gitstore-api/internal/datastore/memdb/backend_test.go` — cover `CreateRepository`, `GetRepository`, `ListRepositoriesByNamespace`, `CreateNamespaceMapping`, `LookupRepository` (found + not-found), `LookupNamespaceByRepoID`, `RenameRepository` (old name returns `ErrNotFound`, new name returns original `repo_id`), `TransferRepository` (old ns returns `ErrNotFound`, new ns returns same `repo_id`)
- [ ] T008 Add `Repository` and `NamespaceMapping` structs to `gitstore-api/internal/datastore/entities.go` — fields exactly as in `specs/010-repo-storage-identity/data-model.md` (`ID`, `NamespaceID`, `Name`, `DefaultBranch`, `StorageClass`, `CreatedAt`, `CreatedBy`, `UpdatedAt`, `UpdatedBy` for Repository; `NamespaceID`, `Name`, `RepoID` for NamespaceMapping)
- [ ] T009 Add repository and mapping interface methods to `gitstore-api/internal/datastore/datastore.go` — exactly the eleven methods defined in `specs/010-repo-storage-identity/data-model.md` under "Datastore Interface Additions"
- [ ] T010 Add `repository` and `namespace_mapping` table schemas to `gitstore-api/internal/datastore/memdb/schema.go` — use `memdb.UUIDFieldIndex` for the `id` index on `repository`, `memdb.CompoundIndex` with `UUIDFieldIndex{Field:"NamespaceID"}` + `StringFieldIndex{Field:"Name"}` for the `id` index on `namespace_mapping`, and `memdb.UUIDFieldIndex{Field:"RepoID"}` for the `repo_id` index; exactly matching `specs/010-repo-storage-identity/data-model.md`
- [ ] T011 Implement all eleven datastore interface methods on the memdb backend in `gitstore-api/internal/datastore/memdb/backend.go` — `CreateRepository`, `GetRepository`, `ListRepositoriesByNamespace`, `UpdateRepository`, `DeleteRepository`, `CreateNamespaceMapping`, `LookupRepository`, `LookupNamespaceByRepoID`, `RenameRepository` (delete-old + insert-new), `TransferRepository` (delete-old-ns + insert-new-ns), `DeleteNamespaceMapping`
- [ ] T012 [P] Add Scylla table models for `Repository` and `NamespaceMapping` to `gitstore-api/internal/datastore/scylla/models.go` — mirror Go struct tags compatible with `gocqlx/v3`
- [ ] T013 [P] Implement repository CRUD operations in new file `gitstore-api/internal/datastore/scylla/repository.go` — `CreateRepository`, `GetRepository`, `ListRepositoriesByNamespace`, `UpdateRepository`, `DeleteRepository`
- [ ] T014 [P] Implement mapping operations in new file `gitstore-api/internal/datastore/scylla/namespace_mapping.go` — `CreateNamespaceMapping`, `LookupRepository`, `LookupNamespaceByRepoID`, `RenameRepository`, `TransferRepository`, `DeleteNamespaceMapping`
- [ ] T015 Confirm T007 tests now pass: `cd gitstore-api && go test -count=1 -v -race ./internal/datastore/...`

**Checkpoint**: `go test ./internal/datastore/...` green. Foundation ready — US1–US3 can proceed.

---

## Phase 3: User Story 1 — Rename a repository without moving data (Priority: P1) 🎯 MVP

**Goal**: A namespace owner renames a repository; physical storage is untouched; fresh `git clone` via new name succeeds.

**Independent Test**: Rename a repository via the GraphQL API, verify the old storage path on disk is unchanged, confirm `LookupRepository(ns, old_name)` returns `ErrNotFound` and `LookupRepository(ns, new_name)` returns the same `repo_id`.

### Tests for User Story 1

> **NOTE: Write these FIRST and confirm they FAIL before implementation**

- [ ] T016 [P] [US1] Write failing resolver test for `renameRepository` mutation in `gitstore-api/internal/graph/repository_resolver_test.go` — verify `renameRepository` updates the mapping, old name returns not-found, new name returns the same repository
- [ ] T017 [P] [US1] Write failing resolver test that `createRepository` produces a `Repository` with a UUIDv7 `id` and calls gRPC `CreateRepository` with that UUID as `repository_id` in `gitstore-api/internal/graph/repository_resolver_test.go`

### Implementation for User Story 1

- [ ] T018 [US1] Implement `createRepository` resolver in `gitstore-api/internal/graph/repository.resolvers.go` — generate UUIDv7 via `uuid.NewV7()`, call `datastore.CreateRepository`, call `datastore.CreateNamespaceMapping`, call gRPC `CreateRepository(repository_id=repo_id, storage_class)`, log lookup call (namespace_id, name, resolved repo_id) per Principle IV
- [ ] T019 [US1] Implement `renameRepository` resolver in `gitstore-api/internal/graph/repository.resolvers.go` — fetch `Repository` by Relay ID, call `datastore.RenameRepository(namespaceID, oldName, newName)`, no gRPC call (storage unchanged), log rename (old path, new path, repo_id)
- [ ] T020 [US1] Implement `repository` query resolver (`RepositoryBy.namespacePath` and `RepositoryBy.id` lookup) in `gitstore-api/internal/graph/repository.resolvers.go` — resolve namespace slug via `GetNamespaceByIdentifier`, then `LookupRepository(namespace_id, name)`, log lookup per Principle IV
- [ ] T021 [US1] Implement `repositories` query resolver in `gitstore-api/internal/graph/repository.resolvers.go` — call `ListRepositoriesByNamespace`, return `RepositoryConnection` with pagination
- [ ] T022 [US1] Wire the `Repository.namespace` field resolver to return the owning `Namespace` object in `gitstore-api/internal/graph/repository.resolvers.go`
- [ ] T023 [US1] Confirm T016–T017 tests now pass: `cd gitstore-api && go test -count=1 -v -race ./internal/graph/...`

**Checkpoint**: `createRepository` and `renameRepository` work end-to-end. US1 independently testable.

---

## Phase 4: User Story 2 — Resolve a repository by namespace path (Priority: P1)

**Goal**: A git protocol handler in `gitstore-api` resolves `namespace/repo-name → repo_id` and delegates to `gitstore-git-service` with only the `repo_id`; the Rust service derives the storage path via the fanout formula.

**Independent Test**: Send a `git fetch` over HTTP for a known namespace path; confirm the gRPC call carries a pre-resolved UUIDv7 (not a namespace string) and `gitstore-git-service` opens the correct fanout path on disk.

### Tests for User Story 2

> **NOTE: Write these FIRST and confirm they FAIL before implementation**

- [ ] T024 [P] [US2] Write failing Rust unit tests for `fanout_path(data_dir, repo_id)` in `gitstore-git-service/src/git/repo.rs` — cover: same UUID → same path (stability), two distinct UUIDs → distinct paths (collision-free), malformed inputs (`""`, wrong-length, `/`-containing, `..`-containing) → `Status::invalid_argument`
- [ ] T025 [P] [US2] Write failing Rust integration test in `gitstore-git-service/src/grpc/server.rs` (or a `tests/` file) — `CreateRepository` with a valid UUIDv7 creates the fanout directory structure and returns `storage_path` matching the formula

### Implementation for User Story 2

- [ ] T026 [US2] Extract `fanout_path(data_root: &Path, repo_id: &str) -> Result<PathBuf, Status>` into `gitstore-git-service/src/git/repo.rs` — strip hyphens, take `hex[0..2]` as `l1` and `hex[2..4]` as `l2`, return `data_root/l1/l2/{repo_id}.git`; add UUID-format validation (36-char hyphenated) rejecting non-UUID strings
- [ ] T027 [US2] Replace `resolve_repo_path` in `gitstore-git-service/src/grpc/server.rs` with calls to `fanout_path` from `git/repo.rs` — update `CreateRepository`, `DeleteRepository`, `GetFile`, `GetFileStream`, `ListFiles`, `CommitFile`, `DeleteFile`, `CreateTag`, `ListTags`, `GetLatestTag` handlers to use the new function
- [ ] T028 [US2] Update `gitstore-git-service/src/grpc/server.rs` `CreateRepository` handler to create the two-level fanout directory structure (`data_root/l1/l2/`) if it does not exist before calling `git init --bare` and return `storage_path` in the response
- [ ] T029 [US2] Add structured tracing spans to `gitstore-git-service/src/grpc/server.rs` for each RPC handler — include `repo_id` and derived `storage_path` in span fields per Principle IV
- [ ] T030 [US2] Confirm T024–T025 tests pass: `cd gitstore-git-service && cargo test --verbose`

**Checkpoint**: Fanout path resolver stable and all gRPC handlers use UUIDv7-based paths. US2 independently testable.

---

## Phase 5: User Story 3 — Transfer a repository to a different namespace (Priority: P2)

**Goal**: An administrator transfers a repo between namespaces; internal identity is preserved; storage does not move; old namespace mapping is invalidated.

**Independent Test**: Transfer a repository between two namespaces; verify `LookupRepository(old_ns, name)` returns `ErrNotFound`, `LookupRepository(new_ns, name)` returns the same `repo_id`, and the storage path on disk is unchanged.

### Tests for User Story 3

> **NOTE: Write these FIRST and confirm they FAIL before implementation**

- [ ] T031 [P] [US3] Write failing resolver test for `transferRepository` mutation in `gitstore-api/internal/graph/repository_resolver_test.go` — verify old namespace lookup returns not-found, new namespace lookup returns same repository, storage path unchanged (no gRPC call)
- [ ] T032 [P] [US3] Write failing resolver test for `deleteRepository` mutation in `gitstore-api/internal/graph/repository_resolver_test.go` — verify mapping deleted, repository record deleted, gRPC `DeleteRepository` called

### Implementation for User Story 3

- [ ] T033 [US3] Implement `transferRepository` resolver in `gitstore-api/internal/graph/repository.resolvers.go` — call `datastore.TransferRepository(repoID, fromNamespaceID, toNamespaceID)` + update `Repository.NamespaceID` via `UpdateRepository`, no gRPC call, log transfer (old namespace_id, new namespace_id, repo_id) per Principle IV
- [ ] T034 [US3] Implement `deleteRepository` resolver in `gitstore-api/internal/graph/repository.resolvers.go` — call `datastore.DeleteNamespaceMapping`, call `datastore.DeleteRepository`, call gRPC `DeleteRepository(repository_id=repo_id)`, log deletion
- [ ] T035 [US3] Confirm T031–T032 tests pass: `cd gitstore-api && go test -count=1 -v -race ./internal/graph/...`

**Checkpoint**: Transfer and delete mutations work end-to-end. US3 independently testable.

---

## Phase 6: User Story 4 — Operator inspects storage location (Priority: P3)

**Goal**: An operator can retrieve a repository's `repo_id` and derive/query its exact storage path from the namespace path using admin tooling.

**Independent Test**: Given `alice/configs`, an operator can call the `repository` query (or admin tool) to retrieve the `repo_id` and compute `{data_dir}/{xx}/{yy}/{repo_id}.git` within one minute.

### Tests for User Story 4

> **NOTE: Write these FIRST and confirm they FAIL before implementation**

- [ ] T036 [P] [US4] Write failing resolver test for `LookupNamespaceByRepoID` reverse lookup via `repositoryById`/admin path in `gitstore-api/internal/graph/repository_resolver_test.go` — given a `repo_id`, resolver returns namespace path

### Implementation for User Story 4

- [ ] T037 [US4] Implement `storagePath` derived field on the `Repository` GraphQL type in `gitstore-api/internal/graph/repository.resolvers.go` — compute `{data_dir}/{xx}/{yy}/{repo_id}.git` in the resolver using the same fanout formula (hyphens stripped for prefix, full UUID with hyphens for filename) and return as `String`
- [ ] T038 [US4] Add `storagePath: String!` field to the `Repository` type in `shared/schemas/repository.graphqls` and regenerate gqlgen stubs
- [ ] T039 [US4] Confirm T036 test passes: `cd gitstore-api && go test -count=1 -v -race ./internal/graph/...`

**Checkpoint**: Operators can query storage path via GraphQL. US4 independently testable.

---

## Phase 7: Polish & Cross-Cutting Concerns

**Purpose**: Documentation, observability completeness, and pre-PR quality checks.

- [ ] T040 [P] Update `docs/` with storage architecture documentation — add diagram: `namespace → namespace_id → (namespace_id, name) → repo_id → fanout path`, lookup flow description, operator runbook (how to find a repository's storage path from namespace path), common failure modes (orphaned storage, missing mapping, malformed UUID) per FR-012
- [ ] T041 [P] Update `specs/010-repo-storage-identity/quickstart.md` with any implementation deviations found during T018–T039
- [ ] T042 Run full pre-PR checklist per AGENTS.md:
  ```bash
  cd gitstore-git-service && cargo fmt --all -- --check && cargo clippy --all-targets --all-features -- -D warnings && cargo build --verbose && cargo test --verbose
  cd ../gitstore-api && go vet ./... && staticcheck ./... && go build -v ./... && go test -count=1 -v -race -coverprofile=coverage.txt -covermode=atomic ./...
  cd .. && ./scripts/check-go-license-headers.sh --all && ./scripts/check-go-license-headers.sh --diff-base origin/main
  ./scripts/check-rust-license-headers.sh --all && ./scripts/check-rust-license-headers.sh --diff-base origin/main
  ```

**Checkpoint**: All checks green. Ready for PR.

---

## Dependencies & Execution Order

### Phase Dependencies

- **Phase 1 (Contracts)**: No dependencies — start immediately
- **Phase 2 (Foundational Go data layer)**: Depends on Phase 1 (T005, T006 generate stubs needed by T008–T015)
- **Phase 3–6 (User Stories)**: All depend on Phase 2 completion; can then proceed in priority order or in parallel
- **Phase 7 (Polish)**: Depends on all story phases being complete

### User Story Dependencies

| Story | Priority | Depends on              | Independently testable                    |
|-------|----------|-------------------------|-------------------------------------------|
| US1   | P1       | Phase 2 (complete)      | Yes — rename + create via GraphQL         |
| US2   | P1       | Phase 2 (T009)          | Yes — fanout resolver in Rust, gRPC calls |
| US3   | P2       | Phase 2 + US1 resolvers | Yes — transfer + delete via GraphQL       |
| US4   | P3       | US1 resolvers           | Yes — storagePath field query             |

### Parallel Opportunities

- T002, T003, T004 can run in parallel within Phase 1 (different files)
- T012, T013, T014 can run in parallel within Phase 2 (different Scylla files)
- T016, T017 can run in parallel (both test files, no code dependency)
- T024, T025 can run in parallel (Rust test files)
- T031, T032 can run in parallel (both test files)
- T041, T042 can run in parallel (different doc files)
- US2 Rust work (T024–T030) can proceed in parallel with US1 Go work (T016–T023) once Phase 2 is complete

---

## Parallel Execution Examples

### Phase 2 Parallel

```
Agent A: T007 (write memdb tests)
Agent B: T012 (Scylla Repository model) + T013 (Scylla repository.go) + T014 (Scylla namespace_mapping.go)
→ Merge: T008 (entities.go), T009 (interface), T010 (memdb schema), T011 (memdb impl)
```

### US1 + US2 Parallel (after Phase 2)

```
Agent A: T016–T023 (Go resolver work for US1)
Agent B: T024–T030 (Rust fanout resolver for US2)
```

---

## Implementation Strategy

### MVP First (US1 + US2 — Priority: P1 stories)

1. Complete Phase 1: Contracts
2. Complete Phase 2: Go data layer
3. Complete Phase 3: US1 — rename without moving data
4. Complete Phase 4: US2 — fanout path resolver in Rust
5. **STOP and VALIDATE**: `git clone` flow resolves `repo_id` and serves from fanout path
6. Proceed to US3 (transfer) → US4 (operator tooling) → Migration → Docs

### Incremental Delivery

1. Phase 1 + Phase 2 → datastore foundation ready
2. Phase 3 → rename-without-move working (core user promise)
3. Phase 4 → gRPC uses fanout paths (git protocol ready)
4. Phase 5 → transfer across namespaces
5. Phase 6 → operator visibility
6. Phase 7 → docs + polish + PR

---

## Notes

- Constitution Principle I (Test-First): every implementation task must be preceded by a failing test
- Constitution Principle II (API-First): Phase 1 contracts must be committed before Phase 2+
- Constitution Principle IV (Observability): all lookup, rename, transfer, and gRPC delegation calls must emit structured log entries
- `clientMutationId: String` must appear in every GraphQL mutation input type AND every payload type
- `StoragePath` is **derived, never stored** (D-005): compute it from `repo_id` on the fly in both Go and Rust
- No migration files created — update `001_initial_schema.cql` and `002_add_initial_indices.cql` in-place (D-003, ALPHA)
- `namespace` memdb table index uses `memdb.UUIDFieldIndex{Field: "ID"}` (already updated in last commit)
- `createdAt`/`updatedAt` GraphQL schema type is `DateTime` (already reflected in contracts)
