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

- [ ] T001 Add `WatchEventType` enum, `WatchEvent` type, `LabelSelectorInput`/`LabelSelectorRequirementInput`/`KeyValuePairInput` input types, and the `watchResources(kind:, namespace:, selector:, resourceVersion:)` Subscription field to `shared/schemas/schema.graphqls` per `contracts/watch-api.graphql`
- [ ] T002 Add `watchCategories(namespace:, selector:, resourceVersion:)` Subscription field and `CategoryWatchEvent` type to `shared/schemas/category.graphqls` per `contracts/watch-api.graphql`
- [ ] T003 Add `ConditionInput`, `ResolvedCategoryTaxonomyInput`, `UpdateCategoryStatusInput`, `UpdateCategoryStatusPayload`, `StatusConflict`, and the `updateCategoryStatus(input:)` Mutation field to `shared/schemas/category.graphqls` per `contracts/status-api.graphql`
- [ ] T004 Add `UpdateResourceStatusInput`, `UpdateResourceStatusPayload`, and the `updateResourceStatus(input:)` Mutation field to `shared/schemas/schema.graphqls` per `contracts/status-api.graphql`
- [ ] T005 Run gqlgen codegen (`cd gitstore-api && go generate ./...` or the project's documented gqlgen invocation) to regenerate `internal/graph/generated/` and `internal/graph/model/models_gen.go` from T001-T004; confirm the build compiles with stub resolvers returning `panic("not implemented")` for the new fields (depends on T001-T004)

**Checkpoint**: Schema compiles and generates stub resolver signatures. No behavior yet — this only unblocks writing contract tests against real generated types.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: The in-process event bus and datastore/authz plumbing every watch and status-write resolver depends on. Both US1 and US2 need this; neither can be meaningfully tested without it.

**⚠️ CRITICAL**: No user story implementation can begin until this phase is complete.

- [ ] T006 [P] Implement `Event`, `EventType`, and the bounded per-kind ring-buffer `EventBus` (`Publish`, `Subscribe`, `Unsubscribe`, expiry detection) in `gitstore-api/internal/eventbus/eventbus.go`, per data-model.md's "EventBus (per-kind ring buffer)" section and research.md R2/R3
- [ ] T007 [P] Unit tests for `EventBus` (publish/subscribe delivery order, per-kind isolation, ring-buffer eviction, expired-cursor detection when a requested `resourceVersion` is older than the oldest retained event) in `gitstore-api/internal/eventbus/eventbus_test.go` — write and confirm failing before T006 is complete, then confirm passing after
- [ ] T008 Add `UpdateCategoryTaxonomyStatus(ctx, patch)` to the `datastore.Datastore` interface (`gitstore-api/internal/datastore/datastore.go`) — a status-only partial-merge write distinct from the existing full-object `UpdateCategoryTaxonomy`, per research.md R6's structural-enforcement decision
- [ ] T009 [P] Implement `UpdateCategoryTaxonomyStatus` for the `memdb` backend in `gitstore-api/internal/datastore/memdb/backend.go` (depends on T008)
- [ ] T010 [P] Implement `UpdateCategoryTaxonomyStatus` for the `scylla` backend in `gitstore-api/internal/datastore/scylla/backend.go`, reusing the existing `resource_version` column and `nextResourceVersion`-style increment (depends on T008)
- [ ] T011 [P] Implement `UpdateCategoryTaxonomyStatus` on the instrumented wrapper in `gitstore-api/internal/datastore/instrumented.go` (depends on T008)
- [ ] T012 Wire `AdmitResources`/`admitCategoryTaxonomyWithContext` in `gitstore-api/internal/cataloggrpc/server.go` to publish an `eventbus.Event` (Added/Modified/Deleted) after every successful `CategoryTaxonomy` create/update/delete, per research.md R2 (depends on T006)
- [ ] T013 Add the `category.status.write` action check to `GraphQLFieldAuthorizer` in `gitstore-api/internal/middleware/security/graphql.go` (new `switch` case for `updateCategoryStatus`, calling `authz.Authorize(ctx, principal, "category.status.write", ...)`), per research.md R5 — write as a case that returns `FORBIDDEN` on deny, mirroring the existing `createNamespace`/`deleteNamespace` cases

**Checkpoint**: Event production, status-only datastore write path, and the authorization hook all exist and are unit-tested. User story implementation can now begin.

---

## Phase 3: User Story 1 — A controller can list-then-watch a resource kind's changes (Priority: P1) 🎯 MVP (half)

**Goal**: A controller can open `watchCategories`/`watchResources`, receive live `Added`/`Modified`/`Deleted` events in admission order, resume from a `resourceVersion` cursor, and get an unambiguous expired-cursor signal when its cursor is too old.

**Independent Test**: Open a `watchCategories` subscription with no cursor, push a new `CategoryTaxonomy` via the existing git-admission pipeline, confirm the event arrives; disconnect, push another change, reconnect with the last-seen `resourceVersion`, confirm only the missed change is delivered; reconnect with an artificially old cursor, confirm a `WATCH_EXPIRED` error terminates the subscription instead of a silent empty stream.

### Tests for User Story 1 ⚠️

> Write these first; confirm they fail (resolver panics/returns not-implemented) before starting implementation below.

- [ ] T014 [P] [US1] Contract test: `watchCategories` delivers `Added`/`Modified`/`Deleted` events in admission order for a live `CategoryTaxonomy` push, in `gitstore-api/tests/contract/watch_status_test.go`
- [ ] T015 [P] [US1] Contract test: `watchCategories` resumed with a valid `resourceVersion` delivers only events after that cursor, not the full current set, in `gitstore-api/tests/contract/watch_status_test.go`
- [ ] T016 [P] [US1] Contract test: `watchCategories` opened with an expired `resourceVersion` terminates with a `WATCH_EXPIRED`-extension GraphQL error, in `gitstore-api/tests/contract/watch_status_test.go`
- [ ] T017 [P] [US1] Contract test: `watchResources(kind: "CategoryTaxonomy", ...)` exhibits the same list-then-watch/resume/expiry behavior as `watchCategories` (validates the generic path per FR-006), in `gitstore-api/tests/contract/watch_status_test.go`
- [ ] T018 [P] [US1] Contract test: a resource transitioning into/out of an active namespace or label-selector filter emits a synthetic ADDED/DELETED event (FR-013), in `gitstore-api/tests/contract/watch_status_test.go`
- [ ] T019 [P] [US1] Integration test: full list (`categories` query) then watch (`watchCategories`) bootstrap against a running `gitstore-api`, confirming zero missed/duplicated changes across the list→watch transition (SC-001), in `gitstore-controller-manager/tests/integration/watch_status_integration_test.go`

### Implementation for User Story 1

- [ ] T020 [US1] Implement the `watchCategories` subscription resolver in `gitstore-api/internal/graph/resolver/category_resolver.go` — subscribes to the `eventbus.EventBus` for kind `CategoryTaxonomy`, applies namespace/selector filtering (including the enter/exit synthetic-event behavior), maps `eventbus.Event` → `CategoryWatchEvent`, and returns a `WATCH_EXPIRED` GraphQL error when the requested `resourceVersion` predates the retained window (depends on T006, T012)
- [ ] T021 [US1] Implement the generic `watchResources(kind:)` subscription resolver in `gitstore-api/internal/graph/resolver/watch_resources_resolver.go` — same event-bus subscription/filtering/expiry logic as T020 but dispatched by the `kind` argument and mapping to the JSON-boxed `WatchEvent` (depends on T006, T012)
- [ ] T022 [US1] Implement label-selector matching helper (shared by T020/T021) reusing the existing `catalog.LabelSelector` evaluation logic already used for `Collection` membership (`ListProductsByLabelSelector`), in `gitstore-api/internal/graph/resolver/label_selector.go` (or extend the existing selector-evaluation location if one is found during implementation)
- [ ] T023 [P] [US1] Implement the concrete `CategoryTaxonomyListWatcher` (`List`/`Watch` satisfying `internal/listwatch.ListWatcher[T]`/`Watcher[T]`) in `gitstore-controller-manager/internal/listwatch/graphql_listwatcher.go`, per quickstart.md step 1 — `List` calls the existing `categories` query, `Watch` opens the `watchCategories` subscription and maps a `WATCH_EXPIRED` GraphQL error to `errors.Is(err, listwatch.ErrWatchExpired)`
- [ ] T024 [US1] Wire a `listwatch.Runner[CategoryTaxonomy]` using T023's `ListWatcher` and the existing `checkpoint.FilesystemStore` in `gitstore-controller-manager/cmd/controller/main.go`, per `specs/036-controller-startup-resume/quickstart.md`'s pattern (depends on T023)

**Checkpoint**: User Story 1 is independently functional and testable — a controller can list-then-watch `CategoryTaxonomy` (and, via the generic path, any kind) end-to-end.

---

## Phase 4: User Story 2 — A controller can write status back safely (Priority: P1) 🎯 MVP (other half)

**Goal**: A controller can submit a partial status update with a `resourceVersion` precondition; conflicting, spec-altering, or unauthorized writes are all rejected distinctly and safely.

**Independent Test**: Fetch a `CategoryTaxonomy`'s current `resourceVersion`, submit `updateCategoryStatus` with that version, confirm the status changed and only the supplied fields changed; resubmit with the same now-stale version, confirm a `StatusConflict` is returned and status is unchanged; attempt the mutation with a caller lacking `category.status.write`, confirm `FORBIDDEN` regardless of `resourceVersion` correctness.

### Tests for User Story 2 ⚠️

> Write these first; confirm they fail before starting implementation below.

- [ ] T025 [P] [US2] Contract test: `updateCategoryStatus` with a correct `resourceVersion` applies only the supplied fields and leaves unsupplied status fields unchanged (FR-008), in `gitstore-api/tests/contract/watch_status_test.go`
- [ ] T026 [P] [US2] Contract test: `updateCategoryStatus` with a stale `resourceVersion` returns a non-null `conflict` with `currentResourceVersion` set, and leaves status unchanged (FR-009), in `gitstore-api/tests/contract/watch_status_test.go`
- [ ] T027 [P] [US2] Contract test: `updateCategoryStatus` targeting a deleted/nonexistent resource returns a distinct `NOT_FOUND` error, not a `conflict` payload (FR-012), in `gitstore-api/tests/contract/watch_status_test.go`
- [ ] T028 [P] [US2] Contract test: a caller without `category.status.write` authorization is rejected with `FORBIDDEN` even when `resourceVersion` is correct (FR-011, US2 AC5), in `gitstore-api/tests/contract/watch_status_test.go`
- [ ] T029 [P] [US2] Contract test: `updateResourceStatus` (generic CRD path) exhibits the same partial-merge/conflict/authz semantics as `updateCategoryStatus` for a CRD-style kind (FR-006, SC-005), in `gitstore-api/tests/contract/watch_status_test.go`
- [ ] T030 [P] [US2] Integration test: a `graphqlStatusClient.Apply` call round-trips through a running `gitstore-api`, confirming `types.ErrConflict` is returned on a stale-version retry (SC-003), in `gitstore-controller-manager/tests/integration/watch_status_integration_test.go`

### Implementation for User Story 2

- [ ] T031 [US2] Implement the `updateCategoryStatus` mutation resolver in `gitstore-api/internal/graph/resolver/category_resolver.go` — reads current resource, checks `resourceVersion` precondition (returns `conflict` on mismatch, `NOT_FOUND` error if the resource doesn't exist), applies non-nil input fields, calls the new `store.UpdateCategoryTaxonomyStatus` (T008-T011), and publishes the resulting change to the `EventBus` (T006) so watchers observe the status update too (depends on T008-T013, T020)
- [ ] T032 [US2] Implement the generic `updateResourceStatus` mutation resolver in `gitstore-api/internal/graph/resolver/watch_resources_resolver.go` — same precondition/conflict/not-found logic as T031, generalized over `kind` with a JSON-boxed `resolved`/response object (depends on T008-T013)
- [ ] T033 Add `Resolved json.RawMessage` field to `gitstore-controller-manager/internal/status.StatusPatch` (`internal/status/patch.go`) and update `IsNoOp`'s comparison logic to treat a nil `Resolved` as "unchanged" per research.md R8 — `StatusClient.Apply`'s method signature is unchanged
- [ ] T034 [P] [US2] Unit test for the updated `StatusPatch.IsNoOp` (nil `Resolved` vs. present `Resolved`) in `gitstore-controller-manager/internal/status/patch_test.go` (depends on T033)
- [ ] T035 [US2] [P] Implement the concrete `graphqlStatusClient` (`Apply` satisfying `internal/status.StatusClient`) in `gitstore-controller-manager/internal/status/graphql_status_client.go`, per quickstart.md step 3 — calls `updateCategoryStatus`, translates a non-null `conflict` response to `types.ErrConflict`, `NOT_FOUND` to `types.ErrNotFound`, and unmarshals `patch.Resolved` into the `resolved: ResolvedCategoryTaxonomyInput` argument (depends on T033)
- [ ] T036 [US2] Add a `controller` role granting `category.status.write` (and the generic `*.status.write` pattern, if the rbac-local policy syntax supports wildcards) to the deployment's `policy.yaml`, and document the `gitctl`-issued bearer-token bootstrap step for the controller-manager identity, per quickstart.md step 5 (depends on T013)

**Checkpoint**: User Stories 1 AND 2 both work independently and together — a full reconciler can list-then-watch and write status back end-to-end. This is the MVP that unblocks spec 039.

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
