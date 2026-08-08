# Tasks: Controller Watch API and Status Subresource Contract

**Input**: Design documents from `/specs/040-controller-watch-status-api/`
**Prerequisites**: plan.md, spec.md, research.md, data-model.md, contracts/, quickstart.md

**Tests**: Test-First Development (Constitution Principle I — NON-NEGOTIABLE). Tests MUST be written before implementation and MUST fail before the corresponding implementation task begins.

**Organization**: Tasks are grouped by user story (US1 = watch, US2 = status write, US3 = operator docs) to enable independent implementation and testing of each story.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: Which user story this task belongs to (US1, US2, US3)
- File paths are exact, per plan.md's Project Structure section

## Path Conventions

Two existing Go modules, each with its own `internal/` and `tests/` tree:
- `gitstore-api/` (GraphQL server side)
- `gitstore-controller-manager/` (client/consumer side)
- `shared/schemas/*.graphqls` (schema source, shared by both via gqlgen codegen on the `gitstore-api` side)

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Schema scaffolding shared by every user story — nothing here is testable on its own, but every story's contract tests depend on the generated types existing.

- [x] T001 Add `WatchEventType` enum, `WatchEvent` type, `LabelSelectorInput`/`LabelSelectorRequirementInput`/`KeyValuePairInput` input types, and the `watchResources(kind:, namespace:, selector:, resourceVersion:)` Subscription field to `shared/schemas/schema.graphqls` per `contracts/watch-api.graphql`
- [x] T002 Add `watchCategories(namespace:, selector:, resourceVersion:)` Subscription field and `CategoryWatchEvent` type to `shared/schemas/category.graphqls` per `contracts/watch-api.graphql`
- [x] T003 Add `ConditionInput` (schema.graphqls, shared), `ResolvedCategoryTaxonomyInput`, `UpdateCategoryStatusInput`, `UpdateCategoryStatusPayload`, `StatusConflict` (schema.graphqls, shared per research.md R11), and the `updateCategoryStatus(input:)` Mutation field to `shared/schemas/category.graphqls` per `contracts/status-api.graphql`. Also applied the R9 rename (`ancestorPath` → `path: [String!]!`) to the pre-existing output type `ResolvedCategoryTaxonomy`, resolving its TODO.
- [x] T004 Add `UpdateResourceStatusInput`, `UpdateResourceStatusPayload`, and the `updateResourceStatus(input:)` Mutation field to `shared/schemas/schema.graphqls` per `contracts/status-api.graphql`
- [x] T005 Ran gqlgen codegen (`cd gitstore-api && go generate ./...`); regenerated `internal/graph/generated/`, `internal/graph/model/models_gen.go`, and stub resolvers in `category.resolvers.go`/`schema.resolvers.go` (`UpdateCategoryStatus`, `WatchCategories`, `UpdateResourceStatus`, `WatchResources`, all `panic("not implemented")`); `go build ./...` compiles clean

**Checkpoint**: Schema compiles and generates stub resolver signatures. No behavior yet — this only unblocks writing contract tests against real generated types.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: The in-process event bus and datastore/authz plumbing every watch and status-write resolver depends on. Both US1 and US2 need this; neither can be meaningfully tested without it.

**⚠️ CRITICAL**: No user story implementation can begin until this phase is complete.

- [x] T006 [P] Implement `Event`, `EventType`, and the bounded per-kind circular-buffer `Bus` (`Publish`, `Subscribe`/unsubscribe closure, `ErrWatchExpired`) in `gitstore-api/internal/eventbus/eventbus.go`, per data-model.md's "EventBus (per-kind ring buffer)" section and research.md R2/R3
- [x] T007 [P] Unit tests for `Bus` (publish/subscribe delivery order, per-kind isolation, buffer eviction, resume-from-valid-cursor, expired-cursor detection) in `gitstore-api/internal/eventbus/eventbus_test.go` — written first, confirmed failing (no implementation), then passing; `-race` clean
- [x] T008 Added `datastore.ErrConflict`, `CategoryTaxonomyStatusPatch`, `ApplyCategoryTaxonomyStatusPatch` (shared merge/precondition helper), and `UpdateCategoryTaxonomyStatus(ctx, namespace, name, patch)` on the `Datastore` interface (`gitstore-api/internal/datastore/datastore.go`) — status-only partial-merge write distinct from the existing full-object `UpdateCategoryTaxonomy`, per research.md R6. Also applied the R9 rename to the Go struct `catalog.ResolvedCategoryTaxonomy.AncestorPath` → `Path []string` (confirmed unused elsewhere, per R10).
- [x] T009 [P] Implemented `UpdateCategoryTaxonomyStatus` for the `memdb` backend in `gitstore-api/internal/datastore/memdb/backend.go`; added 4 unit tests (apply-patch, stale-conflict, not-found, partial-merge-preserves-unset-fields) in `backend_test.go`, all passing
- [x] T010 [P] Implemented `UpdateCategoryTaxonomyStatus` for the `scylla` backend in `gitstore-api/internal/datastore/scylla/backend.go` using a lightweight transaction (`IF resource_version=?` / `ExecCASRelease`) to close the read-then-write race and return `ErrConflict` on a failed CAS
- [x] T011 [P] Implemented `UpdateCategoryTaxonomyStatus` on the instrumented wrapper in `gitstore-api/internal/datastore/instrumented.go`; updated `StubStore`/`stubDatastore` test doubles to satisfy the expanded interface. `go build ./...`, `go vet ./...`, and `go test ./...` all pass.
- [x] T012 Wired `Server.eventBus *eventbus.Bus` (new field, `ServerDeps.EventBus`) and `publishCategoryTaxonomyEvent` in `gitstore-api/internal/cataloggrpc/server.go`; publishes Added/Modified/Deleted at the three `admitCategoryTaxonomyWithContext`/`deleteResource` call sites. Shared `eventbus.Bus` instance constructed in `app.NewServer` and threaded to both `cataloggrpc.NewServer` (publisher) and `resolver.NewResolver` (subscriber side, via new `ResolverDeps.EventBus`)
- [x] T013 Added `updateCategoryStatus` (action `category.status.write`) and `updateResourceStatus` (action `<lowerCamelKind>.status.write`) cases to `GraphQLFieldAuthorizer` in `gitstore-api/internal/middleware/security/graphql.go`; deny returns a `FORBIDDEN`-extension `*gqlerror.Error`. 4 new tests in `graphql_test.go`, all passing

**Checkpoint**: Event production, status-only datastore write path, and the authorization hook all exist and are unit-tested. User story implementation can now begin.

---

## Phase 3: User Story 1 — A controller can list-then-watch a resource kind's changes (Priority: P1) 🎯 MVP (half)

**Goal**: A controller can open `watchCategories`/`watchResources`, receive live `Added`/`Modified`/`Deleted` events in admission order, resume from a `resourceVersion` cursor, and get an unambiguous expired-cursor signal when its cursor is too old.

**Independent Test**: Open a `watchCategories` subscription with no cursor, push a new `CategoryTaxonomy` via the existing git-admission pipeline, confirm the event arrives; disconnect, push another change, reconnect with the last-seen `resourceVersion`, confirm only the missed change is delivered; reconnect with an artificially old cursor, confirm a `WATCH_EXPIRED` error terminates the subscription instead of a silent empty stream.

### Tests for User Story 1 ⚠️

> Write these first; confirm they fail (resolver panics/returns not-implemented) before starting implementation below.

- [x] T014 [P] [US1] Contract test: `watchCategories` delivers `Added`/`Modified`/`Deleted` events in admission order, in `gitstore-api/tests/contract/watch_status_test.go`
- [x] T015 [P] [US1] Contract test: `watchCategories` resumed with a valid `resourceVersion` delivers only events after that cursor
- [x] T016 [P] [US1] Contract test: `watchCategories` opened with an expired `resourceVersion` terminates with a `WATCH_EXPIRED`-extension GraphQL error
- [x] T017 [P] [US1] Contract test: `watchResources(kind: "CategoryTaxonomy", ...)` exhibits the same list-then-watch/resume/expiry behavior (validates the generic path per FR-006)
- [x] T018 [P] [US1] Contract tests: namespace filter transitions and label-selector filtering (FR-007/FR-013), covering both the enter case (matching event delivered) and exit case (non-matching event suppressed)
- [ ] T019 [P] [US1] Integration test: full list (`categories` query) then watch (`watchCategories`) bootstrap against a running `gitstore-api` (SC-001), in `gitstore-controller-manager/tests/integration/watch_status_integration_test.go` — deferred with T023/T024 (controller-manager client adapter)

### Implementation for User Story 1

- [x] T020 [US1] Implemented the `watchCategories` subscription resolver in `gitstore-api/internal/graph/resolver/category.resolvers.go` — subscribes to `r.eventBus` for kind `CategoryTaxonomy`, applies namespace/selector filtering via helpers in the new `watch.go`, maps `eventbus.Event` → `*model.CategoryWatchEvent` over a buffered channel goroutine, returns a `WATCH_EXPIRED`-extension `*gqlerror.Error` on an expired cursor
- [x] T021 [US1] Implemented the generic `watchResources(kind:)` subscription resolver in `gitstore-api/internal/graph/resolver/schema.resolvers.go` — same subscribe/filter/expiry logic, dispatched by the `kind` argument, maps to the JSON-boxed `*model.WatchEvent` via `toGenericWatchEvent`
- [x] T022 [US1] Implemented `matchesWatchSelector` in `gitstore-api/internal/graph/resolver/label_selector.go` — wraps `catalog.MatchesLabels` but treats nil/empty selector as "matches everything" (watch semantics, opposite of Collection-membership semantics) and fixes an operator-casing bug found while wiring it (GraphQL enum is upper-snake `"NOT_IN"`, `catalog.MatchesLabels` switches on PascalCase `"NotIn"` — a direct cast would have silently matched nothing). 7 unit tests.
- [ ] T023 [P] [US1] Concrete `CategoryTaxonomyListWatcher` in `gitstore-controller-manager` — deferred to a follow-up pass (requires a GraphQL WebSocket client library not yet a dependency of `gitstore-controller-manager`)
- [ ] T024 [US1] Wire a `listwatch.Runner[CategoryTaxonomy]` in `cmd/controller/main.go` — depends on T023

**Checkpoint**: `watchCategories`/`watchResources` resolvers are implemented, tested, and independently functional on the `gitstore-api` side. The `gitstore-controller-manager`-side `ListWatcher` adapter (T023/T024) and its integration test (T019) are deferred — see Notes at the end of this file.

---

## Phase 4: User Story 2 — A controller can write status back safely (Priority: P1) 🎯 MVP (other half)

**Goal**: A controller can submit a partial status update with a `resourceVersion` precondition; conflicting, spec-altering, or unauthorized writes are all rejected distinctly and safely.

**Independent Test**: Fetch a `CategoryTaxonomy`'s current `resourceVersion`, submit `updateCategoryStatus` with that version, confirm the status changed and only the supplied fields changed; resubmit with the same now-stale version, confirm a `StatusConflict` is returned and status is unchanged; attempt the mutation with a caller lacking `category.status.write`, confirm `FORBIDDEN` regardless of `resourceVersion` correctness.

### Tests for User Story 2 ⚠️

> Write these first; confirm they fail before starting implementation below.

- [x] T025 [P] [US2] Contract test: `updateCategoryStatus` with a correct `resourceVersion` applies only the supplied fields (FR-008)
- [x] T026 [P] [US2] Contract test: `updateCategoryStatus` with a stale `resourceVersion` returns a non-null `conflict` with `currentResourceVersion` set (FR-009)
- [x] T027 [P] [US2] Contract test: `updateCategoryStatus` targeting a nonexistent resource returns a distinct `NOT_FOUND` error, not a `conflict` payload (FR-012)
- [x] T028 [P] [US2] Authorization rejection covered at the middleware layer (see Phase 2/T013's tests in `graphql_test.go`) — the resolver itself has no authz logic by design (enforced in `GraphQLFieldAuthorizer` before the resolver runs); a skip-with-cross-reference test documents this split in `watch_status_test.go`
- [x] T029 [P] [US2] Contract test: `updateResourceStatus` (generic CRD path) exhibits the same partial-merge/conflict semantics for kind `"CategoryTaxonomy"` (FR-006, SC-005) — a true third-party CRD kind has no datastore backend yet (research.md R7), so parity is demonstrated via the one kind that does
- [ ] T030 [P] [US2] Integration test: `graphqlStatusClient.Apply` round-trip — deferred with T035 (controller-manager client adapter)

### Implementation for User Story 2

- [x] T031 [US2] Implemented the `updateCategoryStatus` mutation resolver in `gitstore-api/internal/graph/resolver/category.resolvers.go` — converts input via new `status_patch.go` helpers, calls `store.UpdateCategoryTaxonomyStatus`, maps `ErrConflict`→`StatusConflict` payload and `ErrNotFound`→`NOT_FOUND`-extension error, publishes a `Modified` event via `publishCategoryTaxonomyStatusEvent` so watchers observe status writes too
- [x] T032 [US2] Implemented the generic `updateResourceStatus` mutation resolver in `gitstore-api/internal/graph/resolver/schema.resolvers.go` — dispatches on `input.Kind` (only `"CategoryTaxonomy"` has a backend today; other kinds get a `NOT_FOUND`-extension "kind not registered" error), same precondition/conflict/not-found semantics as T031, JSON-boxed `resolved`/response object via `resolvedFromJSONMap`/`categoryTaxonomyToJSONMap`
- [x] T033 Added `Resolved json.RawMessage` to both `StatusPatch` and `ResourceStatus` in `gitstore-controller-manager/internal/status/patch.go`; `IsNoOp` now byte-compares `Resolved` when the patch supplies it, nil = unchanged (research.md R8). `Apply`'s signature is unchanged.
- [x] T034 [P] [US2] 3 new tests in `gitstore-controller-manager/tests/contract/status_patch_test.go` (nil-is-unchanged, differs, matches), all passing
- [ ] T035 [US2] [P] Concrete `graphqlStatusClient` in `gitstore-controller-manager` — deferred (requires a GraphQL client library not yet a dependency of `gitstore-controller-manager`)
- [ ] T036 [US2] `policy.yaml` `controller` role + `gitctl` bootstrap doc — deferred with T035 (no live controller-manager caller to authorize yet)

**Checkpoint**: `updateCategoryStatus`/`updateResourceStatus` resolvers are implemented and tested on the `gitstore-api` side, including authorization (T013) and event-bus integration with the watch resolvers (T020/T021). The `gitstore-controller-manager`-side `StatusClient` adapter (T033-T036) and its integration test (T030) are deferred — see Notes.

---

## Phase 5: User Story 3 — Operators can diagnose watch and status-write problems from documented signals (Priority: P2)

**Goal**: An operator can tell, from logs/metrics alone, whether a watch disruption is transient or an expired-cursor condition, and can interpret a status-write-conflict rate for a given kind.

**Independent Test**: Deliberately force a watch-cursor expiry and a status-write conflict in a test/staging environment; confirm the documented signals (log lines, metric names) match what's described in the runbook and let an operator unfamiliar with the code correctly classify each incident.

### Tests for User Story 3 ⚠️

- [ ] T037 [P] [US3] Integration test: forcing a watch-cursor expiry produces a structured log line (or metric increment) distinguishable from an ordinary transient reconnect, in `gitstore-controller-manager/tests/integration/watch_status_integration_test.go`
- [ ] T038 [P] [US3] Integration test: forcing repeated `resourceVersion` conflicts for one kind produces a per-kind-labeled signal (log field or metric label) an operator can filter on, in `gitstore-api/tests/contract/watch_status_test.go`

### Implementation for User Story 3

- [ ] T039 [US3] Add structured zap logging (distinct log messages/fields for "watch reconnect" vs. "watch cursor expired") to the `EventBus`/subscription resolver path in `gitstore-api/internal/eventbus/eventbus.go` and `gitstore-api/internal/graph/resolver/category_resolver.go` (depends on T006, T020)
- [ ] T040 [US3] Add structured zap logging and/or a Prometheus counter (per-kind labeled) for status-write conflicts in the `updateCategoryStatus`/`updateResourceStatus` resolvers, following the existing `prometheus/client_golang` pattern already used in `gitstore-controller-manager/internal/health` (depends on T031, T032)
- [ ] T041 [US3] Write the operator runbook at `docs/runbooks/controller-watch-status.md` — signals to check (log fields/metric names from T039/T040), how to distinguish transient reconnect from expired-cursor, and remediation guidance for a sustained status-write-conflict rate (FR-014) (depends on T039, T040)

**Checkpoint**: All three user stories are independently functional. Operators have documented signals for both watch and status-write failure modes.

---

## Phase 6: Polish & Cross-Cutting Concerns

**Purpose**: Validation and documentation that spans all three stories.

- [ ] T042 [P] Run `quickstart.md` end-to-end manually (or via a smoke-test script) against a locally running `gitstore-api` + `gitstore-controller-manager`, confirming every code snippet in the quickstart reflects the actual implemented signatures
- [ ] T043 [P] Update `docs/` with the new `watchCategories`/`watchResources`/`updateCategoryStatus`/`updateResourceStatus` schema additions, per the constitution's "after implementing a feature update the documentation in docs/" rule
- [ ] T044 Run `make pr-ready` (full lint/test/license-check aggregate) and fix any findings before marking the spec complete
- [ ] T045 Run `graphify update .` to refresh the knowledge graph with the new packages/resolvers

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies — can start immediately. T005 depends on T001-T004 (schema must be written before codegen runs).
- **Foundational (Phase 2)**: Depends on Setup (Phase 1) completion — BLOCKS both User Story 1 and User Story 2. T009/T010/T011 depend on T008 (interface method must exist before backends implement it). T012 depends on T006 (event bus must exist before admission can publish to it).
- **User Story 1 (Phase 3)**: Depends on Foundational (Phase 2) completion, specifically T006 and T012 (event production).
- **User Story 2 (Phase 4)**: Depends on Foundational (Phase 2) completion, specifically T008-T011 (status-only datastore write) and T013 (authz hook). T031 also depends on T020 (publishing a status-change event through the same bus watchers use) — so in practice User Story 2's resolver work follows User Story 1's, though both stories' *tests* can be written in parallel with each other from the start.
- **User Story 3 (Phase 5)**: Depends on User Story 1 (T006, T020) and User Story 2 (T031, T032) implementation existing, since it adds observability on top of both.
- **Polish (Phase 6)**: Depends on all three user stories being complete.

### User Story Dependencies

- **User Story 1 (P1)**: No dependency on User Story 2 for its own independent test (watch works even if nothing ever writes status back through the new mutation — admission-driven `Added`/`Modified`/`Deleted` events are sufficient).
- **User Story 2 (P2 in priority but P1 in spec.md)**: Its resolver (T031) reuses the event bus from User Story 1 (T006/T012) to publish status-change events so watchers see them — this is a shared-infrastructure dependency, not a story-order dependency; User Story 2's own independent test (submit a status update, confirm it applies/conflicts) does not require User Story 1's resolvers to exist, only the underlying event bus and datastore method.
- **User Story 3 (P2)**: Builds observability on top of both US1 and US2's resolvers — cannot be meaningfully tested until both exist.

### Within Each User Story

- Tests MUST be written and FAIL before implementation (constitution Principle I, non-negotiable)
- Resolver implementation before controller-manager-side adapter implementation (server contract must exist before a client can be written against it)
- Core implementation before the operator-facing observability layer (US3)

### Parallel Opportunities

- T001-T004 (schema additions across two files) can be done in parallel, then T005 (codegen) runs once after all four land
- T009, T010, T011 (three backend implementations of the same new interface method) are parallelizable — different files, same interface contract
- All contract-test tasks within a phase (T014-T019, T025-T030) marked [P] can be written in parallel — same test file but independent test functions with no shared mutable state
- T023 (controller-manager `ListWatcher`) can be built in parallel with T020/T021 (API-side resolvers) once the schema (Phase 1) is fixed, since it only needs the *contract*, not the running implementation, to start — though its integration test (T019) needs both sides working to actually pass

---

## Parallel Example: User Story 1

```bash
# Launch all contract tests for User Story 1 together (write first, confirm failing):
Task: "Contract test: watchCategories delivers events in order in gitstore-api/tests/contract/watch_status_test.go"
Task: "Contract test: watchCategories resume from valid cursor in gitstore-api/tests/contract/watch_status_test.go"
Task: "Contract test: watchCategories expired-cursor signal in gitstore-api/tests/contract/watch_status_test.go"
Task: "Contract test: watchResources generic path parity in gitstore-api/tests/contract/watch_status_test.go"
Task: "Contract test: namespace/selector filter transition events in gitstore-api/tests/contract/watch_status_test.go"

# Launch parallelizable backend datastore implementations for User Story 2's foundation together:
Task: "Implement UpdateCategoryTaxonomyStatus for memdb backend in gitstore-api/internal/datastore/memdb/backend.go"
Task: "Implement UpdateCategoryTaxonomyStatus for scylla backend in gitstore-api/internal/datastore/scylla/backend.go"
Task: "Implement UpdateCategoryTaxonomyStatus on instrumented wrapper in gitstore-api/internal/datastore/instrumented.go"
```

---

## Implementation Strategy

### MVP First (User Stories 1 + 2 together)

Unlike a typical spec where US1 alone is a viable MVP, spec.md explicitly marks *both* User Story 1 (watch) and User Story 2 (status write) as **P1** — a reconciler that can only watch but never report status, or only write status but never observe changes, delivers no value on its own (this is spec.md's own framing: "a reconciler that can observe changes but cannot report its findings back provides no value"). Treat Phases 1-4 as the MVP unit:

1. Complete Phase 1: Setup (schema + codegen)
2. Complete Phase 2: Foundational (event bus, status datastore method, authz hook) — CRITICAL, blocks both stories
3. Complete Phase 3: User Story 1 (watch)
4. Complete Phase 4: User Story 2 (status write)
5. **STOP and VALIDATE**: Run quickstart.md end-to-end — a reconciler should be able to list, watch, compute, and write status back against a real `gitstore-api` instance. This unblocks spec 039.
6. Add Phase 5 (User Story 3 — operator docs) once the MVP is validated
7. Complete Phase 6: Polish

### Incremental Delivery

1. Setup + Foundational → schema and plumbing ready, nothing user-visible yet
2. User Story 1 + User Story 2 together → MVP: a full reconciler loop is possible → this directly unblocks spec 039
3. User Story 3 → operational maturity, can ship after the MVP is already in use
4. Polish → documentation and validation sweep

## Implementation Status Notes (as of this pass)

**Done and tested** (`gitstore-api` side, fully functional): T001-T022, T025-T029, all of Phase 2. The server-side GraphQL contract — `watchCategories`, `watchResources`, `updateCategoryStatus`, `updateResourceStatus` — is fully implemented, unit- and contract-tested, and can be exercised today via any GraphQL client (including the `/playground` endpoint) without any controller-manager changes.

**Deferred** (`gitstore-controller-manager` client-adapter side): T019, T023, T024, T030, T035, T036. These require adding a GraphQL client capable of driving `transport.Websocket` subscriptions to `gitstore-controller-manager`'s dependency set — no such client exists yet in that module (`go.mod` has no GraphQL/WebSocket client library). This is a deliberate scope boundary for this pass, not an oversight: T033/T034 (the `StatusPatch.Resolved` field the adapter will need) were completed since they required no new dependency. A follow-up pass should:
1. Choose and add a GraphQL WebSocket client dependency to `gitstore-controller-manager/go.mod`
2. Implement `CategoryTaxonomyListWatcher` (T023) and `graphqlStatusClient` (T035) against it
3. Wire both into `cmd/controller/main.go` (T024) and `policy.yaml` (T036)
4. Write the two integration tests (T019, T030) against a real running `gitstore-api`

**Phase 5 (US3 — observability/runbook) and Phase 6 (Polish)**: not started. Phase 5's structured-logging tasks (T039/T040) can proceed independently of the deferred controller-manager work since they only touch `gitstore-api`-side code already implemented; the runbook (T041) and quickstart validation (T042) are better done once the controller-manager adapter exists so the runbook can describe real operator-observable behavior rather than a hypothetical client.
