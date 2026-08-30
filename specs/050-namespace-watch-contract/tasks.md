# Tasks: Namespace Watch Contract and Durable Journal

**Input**: Design documents from `/specs/050-namespace-watch-contract/`
**Prerequisites**: `plan.md`, `spec.md`, `research.md`, `data-model.md`, `contracts/`, `quickstart.md`

**Tests**: Test-first development is required by the constitution and FR-013. Write each test task before its corresponding implementation task and confirm the new assertion fails for the expected reason.

**Organization**: Tasks are grouped by user story. Feature 050 owns the durable Namespace watch infrastructure; shipped spec 047 remains the mutation and lifecycle authority and is exercised only through regression tests.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel because it touches different files and has no dependency on another incomplete task in the same phase
- **[Story]**: Maps the task to a user story from `spec.md`
- Every task names an exact repository-relative file path

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Add the dependency and package surfaces needed by the durable journal implementation.

- [X] T001 Add `github.com/scylladb/scylla-cdc-go v1.2.1` to `gitstore-api/go.mod` and `gitstore-api/go.sum`
- [X] T002 [P] Create the Namespace watch journal package skeleton and package documentation in `gitstore-api/internal/watchjournal/doc.go`
- [X] T003 [P] Add Namespace watch configuration types, environment-key defaults, and validation test cases in `gitstore-api/internal/config/config_test.go`
- [X] T004 [P] Add deterministic fake CDC source, journal store, lease clock, and subscriber sink test helpers in `gitstore-api/internal/watchjournal/testutil_test.go`

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Build the shared, bounded, replica-safe CDC and journal substrate required by every user story.

**Critical**: No user story implementation begins until this phase passes its backend-neutral and Scylla-focused tests.

### Foundational tests (write and fail first)

- [X] T005 [P] Add table-driven `nwv1:<epoch>:<base36-sequence>` cursor round-trip, kind isolation, monotonicity, and bootstrap-sentinel tests in `gitstore-api/internal/watchjournal/journal_test.go`
- [X] T006 [P] Add migration contract tests for full-preimage/postimage CDC, 14-day CDC TTL, seven-day journal TTL tables, clock, progress, and lease schema in `gitstore-api/internal/datastore/scylla/namespace_watch_migration_test.go`
- [X] T007 [P] Add backend-neutral journal append, high-water, batch-size, ordering, and retention contract tests in `gitstore-api/tests/contract/datastore/namespace_watch_journal_test.go`
- [X] T008 [P] Add CDC classification and append-before-progress crash-recovery tests, including safe duplicate replay, in `gitstore-api/internal/watchjournal/materializer_test.go`
- [X] T009 [P] Add lease acquisition, renewal, handoff, and stale-fencing-token rejection tests in `gitstore-api/internal/watchjournal/lease_test.go`
- [X] T010 [P] Add bounded configuration, readiness, and low-cardinality metric registration tests in `gitstore-api/internal/watchjournal/operability_test.go`

### Foundational implementation

- [X] T011 Implement journal event, cursor, bounds, store, progress, and source interfaces in `gitstore-api/internal/watchjournal/journal.go`
- [X] T012 [P] Implement validated watch defaults for retention, CDC TTL, buckets, reads, replay, buffers, polling, bookmarks, lease, and feature gates in `gitstore-api/internal/config/config.go`
- [X] T013 Add the Namespace watch journal capability and supporting records to `gitstore-api/internal/datastore/datastore.go` and `gitstore-api/internal/datastore/entities.go`
- [X] T014 [P] Add Scylla migration 006 for authoritative Namespace CDC, journal clock/events, CDC progress, and fenced lease tables in `gitstore-api/internal/datastore/scylla/migrations/006_namespace_watch_cdc.cql`
- [X] T015 Implement the in-memory journal contract with bounded history and a stable process epoch in `gitstore-api/internal/datastore/memdb/namespace_watch.go`
- [X] T016 Implement Scylla journal allocation, bucketed append/read, bounds, CDC progress, and LWT lease operations in `gitstore-api/internal/datastore/scylla/namespace_watch.go`
- [X] T017 Implement the official Scylla CDC reader adapter and logical-change identity extraction in `gitstore-api/internal/datastore/scylla/namespace_cdc.go`
- [X] T018 Implement lease ownership, renewal, loss cancellation, and fencing-token propagation in `gitstore-api/internal/watchjournal/lease.go`
- [X] T019 Implement CDC normalization, sequence allocation, journal-before-progress ordering, duplicate accounting, and idle durable BOOKMARK production in `gitstore-api/internal/watchjournal/materializer.go`
- [X] T020 Implement journal leadership, lag, bounds, replay, subscriber, expiry, overflow, bookmark, readiness, and delivery-latency metrics in `gitstore-api/internal/watchjournal/metrics.go`
- [X] T021 Implement materializer and journal-continuity readiness evaluation with bounded `MATERIALIZER_NOT_READY` reasons in `gitstore-api/internal/watchjournal/readiness.go`
- [X] T022 Wire backend-specific journal construction through datastore creation in `gitstore-api/internal/datastore/factory/factory.go`
- [X] T023 Wire the feature-gated materializer lifecycle, shutdown, metrics, and readiness into `gitstore-api/internal/app/server.go`
- [X] T024 Configure local Scylla CDC and durable-journal integration settings in `compose.scylla.yml`

**Checkpoint**: Both backends satisfy the same journal contract; Scylla materialization can recover and hand off without losing an acknowledged Namespace transition.

---

## Phase 3: User Story 1 - Establish an Accurate Live Namespace View (Priority: P1) MVP

**Goal**: A controller bootstraps a race-free full Namespace view and receives typed or generic ADDED/MODIFIED/DELETED/BOOKMARK events through any API replica.

**Independent Test**: Open the bootstrap subscription, receive cursor C, keep it open while listing all Namespaces, mutate the full shipped spec-047 lifecycle through either of two replicas, and verify the queued stream after C is ordered, complete, correctly shaped, and resumable through the other replica.

### Tests for User Story 1 (write and fail first)

- [X] T025 [P] [US1] Add schema contract tests for cluster-scoped `watchNamespaces` and nullable typed `NamespaceWatchEvent.namespace` in `gitstore-api/internal/graph/resolver/namespace_watch_schema_test.go`
- [X] T026 [P] [US1] Add typed and generic `namespace.watch` authorization-order, denial-disclosure, and pluggable-provider tests in `gitstore-api/internal/middleware/security/graphql_namespace_watch_test.go`
- [X] T027 [P] [US1] Add resolver tests for bootstrap BOOKMARK, selector filtering, shared cursor order, and typed/generic payload parity in `gitstore-api/internal/graph/resolver/namespace_watch_test.go`
- [X] T028 [P] [US1] Add Namespace-specific ADDED/MODIFIED/DELETED/BOOKMARK contract tests, including full/null payload rules, in `gitstore-api/tests/contract/namespace_watch_test.go`
- [X] T029 [P] [US1] Add tagged Scylla CDC tests proving successful spec-047 commits create the correct journal events while rejected, conflicting, and idempotent no-op operations create none in `gitstore-api/internal/datastore/scylla/namespace_watch_integration_test.go`
- [X] T030 [P] [US1] Add two-replica bootstrap/list/drain and cross-replica mutation visibility tests in `tests/integration/namespace_watch_test.go`
- [X] T031 [P] [US1] Add regression tests preserving spec-047 admission outcomes, repository fencing, authored state, generation, resourceVersion, status ownership, and deletion semantics in `gitstore-api/internal/graph/resolver/namespace_watch_spec047_regression_test.go`

### Implementation for User Story 1

- [X] T032 [US1] Add the additive `watchNamespaces` and `NamespaceWatchEvent` schema contract to `shared/schemas/namespace.graphqls`
- [X] T033 [US1] Regenerate the gqlgen Namespace subscription model and execution bindings in `gitstore-api/internal/graph/model/models_gen.go`, `gitstore-api/internal/graph/generated/namespace.generated.go`, `gitstore-api/internal/graph/generated/schema.generated.go`, and `gitstore-api/internal/graph/generated/root_.generated.go`
- [X] T034 [US1] Add the shared journal dependency to resolver construction while retaining the event bus for non-Namespace kinds in `gitstore-api/internal/graph/resolver/resolver.go`
- [X] T035 [US1] Implement strongly typed journal-to-GraphQL Namespace event conversion and generic JSON conversion with DELETED/BOOKMARK null payloads in `gitstore-api/internal/graph/resolver/watch.go`
- [X] T036 [US1] Implement the cluster-scoped typed `watchNamespaces` resolver using the durable bootstrap registration in `gitstore-api/internal/graph/resolver/namespace.resolvers.go`
- [X] T037 [US1] Route only `watchResources(kind: "Namespace")` to the durable journal, reject a Namespace namespace-filter, and retain the event bus for other kinds in `gitstore-api/internal/graph/resolver/schema.resolvers.go`
- [X] T038 [US1] Enforce cluster-scoped `namespace.watch` before cursor parsing or journal access for typed and generic entry points in `gitstore-api/internal/middleware/security/graphql.go`
- [X] T039 [US1] Pass the configured journal through application resolver dependencies and keep Namespace watch unavailable until readiness is proven in `gitstore-api/internal/app/server.go`
- [X] T040 [US1] Remove Namespace mutation-side process-local event publication while preserving spec-047 results and non-Namespace publication in `gitstore-api/internal/graph/resolver/watch.go`, `gitstore-api/internal/graph/resolver/namespace.resolvers.go`, and `gitstore-api/internal/graph/resolver/status_generic.go`

**Checkpoint**: User Story 1 is independently usable as the MVP: bootstrap/list/drain is race-free and both GraphQL paths observe the same durable ordered events across replicas.

---

## Phase 4: User Story 2 - Distinguish Caught Up from Must Start Over (Priority: P1)

**Goal**: A consumer resumes strictly after a valid cursor and receives an explicit, bounded failure whenever continuity cannot be proven.

**Independent Test**: Resume through another replica from a retained cursor and receive only later events; then exercise retention, epoch, invalid/future cursor, replay-limit, discontinuity, and buffer-overflow cases and verify `WATCH_EXPIRED`, while pre-registration materializer failure returns `WATCH_UNAVAILABLE`.

### Tests for User Story 2 (write and fail first)

- [X] T041 [P] [US2] Add valid-cursor strict-after replay, 256-row paging, and 100,000-event replay-cap tests in `gitstore-api/internal/watchjournal/subscriber_test.go`
- [X] T042 [P] [US2] Add bounded reason tests for retention, epoch, incompatible/invalid/future cursor, discontinuity, replay limit, and subscriber overflow in `gitstore-api/internal/watchjournal/expiry_test.go`
- [X] T043 [P] [US2] Add GraphQL transport tests proving `WATCH_EXPIRED` and `WATCH_UNAVAILABLE` extensions survive `graphql-transport-ws` and overflow never appears as normal closure in `gitstore-api/internal/middleware/security/graphql_namespace_watch_transport_test.go`
- [X] T044 [P] [US2] Add restart, rolling-replacement, lease-handoff, cross-replica resume, forced-expiry, and overflow integration tests in `tests/integration/namespace_watch_recovery_test.go`

### Implementation for User Story 2

- [X] T045 [US2] Implement bounded replay, live tailing, adaptive polling, selector application, channel capacity 64, and terminal overflow propagation in `gitstore-api/internal/watchjournal/subscriber.go`
- [X] T046 [US2] Implement cursor validation and stable `WATCH_EXPIRED` / `WATCH_UNAVAILABLE` reason mapping in `gitstore-api/internal/watchjournal/errors.go`
- [X] T047 [US2] Propagate terminal journal errors through typed and generic subscriptions instead of silently closing channels in `gitstore-api/internal/graph/resolver/watch.go`
- [X] T048 [US2] Add cursor-safe reconnect and materializer readiness handling to the typed and generic resolver paths in `gitstore-api/internal/graph/resolver/namespace.resolvers.go` and `gitstore-api/internal/graph/resolver/schema.resolvers.go`

**Checkpoint**: User Story 2 is independently verifiable: valid resume never replays the cursor event, and every unprovable continuity case fails closed with a stable machine-readable reason.

---

## Phase 5: User Story 3 - Implement a Consumer from Documentation Alone (Priority: P2)

**Goal**: A reader can list, bootstrap, watch, resume, recover from expiry, and interpret Namespace lifecycle events without consulting server source.

**Independent Test**: Follow only `docs/namespace/namespace-watch.md` to implement the minimal consumer journey and correctly explain payload presence, Terminating detection, at-least-once handling, authorization, and expiry recovery.

### Tests for User Story 3 (write and fail first)

- [X] T049 [P] [US3] Add documentation contract tests requiring complete typed bootstrap/list/resume/expiry examples and bounded error reasons in `gitstore-api/tests/contract/namespace_watch_docs_test.go`
- [X] T050 [P] [US3] Add an executable minimal list/bootstrap/drain/resume consumer example test in `tests/integration/namespace_watch_consumer_test.go`

### Implementation for User Story 3

- [X] T051 [P] [US3] Document the canonical typed consumer algorithm, event payload matrix, selectors, Terminating derivation, duplicates, generic compatibility, and non-goals in `docs/namespace/namespace-watch.md`
- [X] T052 [P] [US3] Document every Namespace journal/materializer feature gate, bound, default, and environment variable in `docs/configuration.md`
- [X] T053 [P] [US3] Document migration-first rollout, fleet-wide mixed-version deny, readiness gates, rollback, lag/lease/expiry diagnosis, and re-list recovery in `docs/runbooks/controller-watch-status.md`
- [X] T054 [US3] Align the consumer-facing examples and validation sequence with the final API and operational names in `specs/050-namespace-watch-contract/quickstart.md`

**Checkpoint**: User Story 3 passes without source-code knowledge and documents spec 047 as an immutable upstream lifecycle contract.

---

## Phase 6: Polish & Cross-Cutting Concerns

**Purpose**: Validate production-scale behavior, rollout safety, repository guidance, and the complete quality gate.

- [X] T055 [P] Add a threshold-enforcing two-replica 60-minute soak for 10 transitions/s, 100/s bursts, 1,000 subscribers, 10,000-event replay, rolling replacement, CPU, and retained memory in `gitstore-api/internal/watchjournal/namespace_watch_capacity_test.go`
- [X] T056 [P] Add migration-006 compatibility and supported-artifact rollback assertions in `gitstore-api/internal/datastore/scylla/namespace_watch_rolling_upgrade_test.go`
- [X] T057 Add the executable Namespace watch capacity and cross-replica probe commands to `Makefile`
- [X] T058 [P] Add alert thresholds and bounded-cardinality Namespace watch metric guidance to `docs/runbooks/controller-watch-status.md`
- [X] T059 Run the focused schema, security, resolver, journal, memdb, and Scylla-hardening suites described in `specs/050-namespace-watch-contract/quickstart.md`
- [X] T060 Run the tagged Scylla CDC/materializer integration suite and cross-replica recovery probe described in `specs/050-namespace-watch-contract/quickstart.md`
- [ ] T061 Run the 60-minute Namespace watch capacity gate and record threshold evidence in `specs/050-namespace-watch-contract/quickstart.md`
- [X] T062 Run `make pr-ready`, resolve all failures, and record the final validation commands in `specs/050-namespace-watch-contract/quickstart.md`
- [X] T063 Run `graphify update .` and verify the refreshed architecture graph captures the Namespace CDC, journal, materializer, and GraphQL watch paths in `graphify-out/graph.json`

---

## Dependencies & Execution Order

### Phase Dependencies

- **Phase 1 — Setup**: Starts immediately.
- **Phase 2 — Foundational**: Depends on Phase 1 and blocks every user story. Complete its failing tests before the implementations they specify.
- **Phase 3 — US1**: Depends on Phase 2 and is the MVP.
- **Phase 4 — US2**: Depends on the Phase 2 journal interfaces and may be developed alongside US1 after foundation, but final transport integration uses the resolver paths completed in US1.
- **Phase 5 — US3**: Can draft after Phase 2, but its executable examples and final validation depend on US1 and US2 behavior.
- **Phase 6 — Polish**: Depends on all selected user stories.

### User Story Dependency Graph

```text
Setup → Foundational → US1 (MVP) ─┬→ Polish
                      US2 ─────────┤
                      US3 ─────────┘
```

- **US1 (P1)**: No dependency on another story after the foundation; delivers bootstrap plus the live typed/generic stream.
- **US2 (P1)**: Independently tests journal continuity after the foundation; resolver-level completion integrates with US1's entry points.
- **US3 (P2)**: Documentation can be drafted independently, then verified against the completed US1/US2 contract.

### Within Each Phase

- Write each listed test first and confirm it fails for the missing behavior.
- Define interfaces and storage schema before backend implementations.
- Complete backend implementations before materializer and application wiring.
- Generate GraphQL bindings after changing the schema and before writing resolver implementations.
- Preserve spec-047 mutation semantics; only replace Namespace's watch-event source.
- Run focused tests before integration, capacity, and `make pr-ready`.

## Parallel Opportunities

### User Story 1

```text
T025 schema contract | T026 authorization | T027 resolver | T028 API contract | T029 Scylla CDC | T030 replicas | T031 spec-047 regression
After T033–T034: T035 conversion | T038 authorization
```

### User Story 2

```text
T041 replay tests | T042 expiry tests | T043 transport errors | T044 recovery integration
After tests fail: T045 subscriber | T046 error mapping
```

### User Story 3

```text
T049 documentation contract | T050 executable consumer
After API names settle: T051 consumer guide | T052 configuration | T053 operations
```

## Implementation Strategy

### MVP First

1. Complete Setup and Foundational phases.
2. Complete US1, including cross-replica bootstrap/list/drain.
3. Stop and validate the typed `watchNamespaces` path plus generic parity.
4. Keep the rollout deny in place until US2 continuity failures and operational readiness are complete.

### Incremental Delivery

1. **Foundation**: Migration, CDC reader, fenced materializer, shared journal, metrics, readiness.
2. **US1 MVP**: Typed and generic race-free live Namespace streams across replicas.
3. **US2 safety**: Resume, explicit expiry, overflow, restart, and rolling replacement.
4. **US3 adoption**: Consumer and operator documentation proven by executable examples.
5. **Production gate**: Soak, rollout/rollback validation, graph refresh, and `make pr-ready`.

## Notes

- At-least-once means a crash-safe duplicate is acceptable; a missing acknowledged transition is not.
- The durable Namespace cursor is distinct from `Namespace.metadata.resourceVersion`.
- Both Namespace watch entry points share the same journal and authorize before cursor inspection.
- Product, category, and file watch backends remain outside feature 050.
- Migration 006 remains present during rollback; spec-047 mutation and lifecycle code is not reopened.
