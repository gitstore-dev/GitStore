# Tasks: Product Git-Backed Lifecycle, Durable Watch, and Reconciliation

**Input**: Design documents in `specs/055-product-lifecycle/`
**Prerequisites**: `plan.md`, `spec.md`, `research.md`, `data-model.md`,
`contracts/`, and `quickstart.md`

**Tests are required**: The feature specification explicitly requires contract,
integration, multi-replica, capacity, and recovery verification. Write each
listed test before its corresponding implementation and demonstrate that it
fails for the expected missing behavior.

## Format

Every task uses `- [ ] T### [P?] [US#?] Description with file path`. `[P]`
means it can proceed in parallel once its phase prerequisites are complete.

## Phase 1: Setup

- [X] T001 Review and preserve the Product lifecycle decisions in `specs/055-product-lifecycle/{spec.md,plan.md,research.md,data-model.md}` before source changes.
- [X] T002 Add the Product lifecycle capacity profile skeleton to `tests/capacity/profiles/product-lifecycle.js` and register `product/lifecycle` validation in `Makefile`.
- [X] T003 [P] Add the Product lifecycle chaos profile skeleton to `tests/chaos/profiles/product-lifecycle.json`.
- [X] T004 [P] Create focused Product lifecycle test fixtures and authenticated principals in `gitstore-api/internal/graph/resolver/product_lifecycle_test.go`.

---

## Phase 2: Foundational contracts and durable primitives

**Purpose**: Establish the shared Product contract, authorization, datastore,
and Resource Watch prerequisites before any user-story slice.

- [X] T005 Add Product mutation/delete-payload schema contract tests, including `DeleteProductInput { id: ID }` and `ProductDeletionOutcome`, in `gitstore-api/internal/graph/resolver/product_schema_contract_test.go`.
- [X] T006 Update `shared/schemas/product.graphqls` with create/update/delete envelopes, `ProductDeletionOutcome`, Namespace-style delete payload, lifecycle spec, and typed watch envelope; regenerate gqlgen output in `gitstore-api/internal/graph/generated/` and `gitstore-api/internal/graph/model/`.
- [X] T007 [P] Add Product resource-action authorization matrix tests for read, author, update, delete, watch, status, and completion in `gitstore-api/internal/middleware/security/graphql_product_lifecycle_test.go`.
- [X] T008 Add resource-aware Product authorization gates before lookup, node, list, relationships/counts, mutation, typed watch, generic watch, status, and completion disclosure in `gitstore-api/internal/middleware/security/graphql.go`.
- [X] T009 [P] Add datastore contract tests for indexed ProductVariant blockers and expected-version Product termination/completion in `gitstore-api/internal/datastore/product_lifecycle_contract_test.go`.
- [X] T010 Extend Product/ProductVariant datastore interfaces and memdb implementation for blocker lookup, mark termination, complete deletion, and owner-reference projection in `gitstore-api/internal/datastore/{datastore.go,memdb/}`.
- [X] T011 [P] Verify the existing owner-reference projection migration and Product lifecycle columns cover Product; add backend contract tests for lifecycle capability in `gitstore-api/internal/datastore/scylla/product_lifecycle_test.go`.
- [X] T012 Implement Scylla Product lifecycle/blocker persistence with resource-version guards in `gitstore-api/internal/datastore/scylla/`.
- [X] T013 Add Product CDC/journal migration and backend-neutral journal source tests in `gitstore-api/internal/datastore/scylla/{migrations/,repository_watch_migration_test.go}` and `gitstore-api/internal/datastore/memdb/product_watch_test.go`.
- [X] T014 Implement Product CDC normalization/materializer source, memdb equivalent, bounded retention/progress/lease wiring, and Product readiness in `gitstore-api/internal/datastore/{scylla/product_cdc.go,memdb/product_watch.go}` and `gitstore-api/internal/watchjournal/`.
- [ ] T015 Add Product journal metrics, lifecycle/finalizer/blocker metrics, and structured audit-safe logs in `gitstore-api/internal/{watchjournal/,cataloggrpc/,graph/resolver/}`.

**Checkpoint**: Schema, authorization, lifecycle datastore operations, and a
durable Product event source are available. All following stories depend on
this phase.

---

## Phase 3: User Story 1 — Git and GraphQL authoring lifecycle (Priority: P1)

**Goal**: Authors create/update/delete through Git or GraphQL with one
canonical admission path and stable Product identity.

**Independent test**: Create/update via Git and GraphQL, compare admitted
revision/identity, then submit invalid input and observe no partial Product.

- [ ] T016 [P] [US1] Add Git-service SchemaValidation and Product-admission tests for generic `OperationDelete`, Product provenance, stable UID/mutable generation, author-system-field rejection, and deletion transition in `gitstore-git-service/src/git/hooks/` and `gitstore-api/internal/cataloggrpc/product_lifecycle_test.go`.
- [X] T017 [US1] Generalize the CategoryTaxonomy proposed-tree SchemaValidation path to `OperationDelete` for every supported resource type; refactor Product admission/deletion to preserve provenance, use lifecycle state rather than hard delete, and emit every committed transition in `gitstore-git-service/src/git/hooks/` and `gitstore-api/internal/cataloggrpc/server.go`.
- [ ] T018 [P] [US1] Add resolver contract tests for create/update/delete commit-and-wait behavior, non-system provenance update routing, implicit `gitstore-system` create routing, and ID delete input in `gitstore-api/internal/graph/resolver/product_lifecycle_test.go`.
- [X] T019 [US1] Implement Product GraphQL create/update/delete in `gitstore-api/internal/graph/resolver/product.resolvers.go`: create targets `gitstore-system`; update resolves stored repository/source path; all await admitted revision.
- [X] T020 [US1] Return `ProductDeletionOutcome` and the terminating Product envelope from `deleteProduct` in `gitstore-api/internal/graph/resolver/product.resolvers.go`.
- [ ] T021 [US1] Add Git-push versus GraphQL end-to-end admission parity coverage in `tests/integration/product_lifecycle_test.go`.

---

## Phase 4: User Story 2 — Secure Product reads and durable watch (Priority: P1)

**Goal**: Authorized clients read and resume complete Product state through
typed and generic durable watches without replica-local gaps.

**Independent test**: Bootstrap/list/drain, replace an API replica, resume the
cursor, and verify create/spec/status/terminating/final-delete coverage.

- [ ] T022 [P] [US2] Add typed/generic Product watch contract tests for full envelopes, selectors, bootstrap, replay, expiry, bookmarks, and authorization-before-cursor behavior in `tests/contract/product_watch_test.go`.
- [X] T023 [US2] Add Product event conversion and generic `watchResources(kind: "Product")` routing from the Resource Watch journal in `gitstore-api/internal/graph/resolver/{watch.go,product_watch.go}`.
- [X] T024 [US2] Replace eventbus-backed `watchProducts` with the durable typed Product adapter in `gitstore-api/internal/graph/resolver/product.resolvers.go`.
- [ ] T025 [P] [US2] Add Product lookup/list/node/relationship/count cross-namespace authorization and no-disclosure tests in `gitstore-api/internal/graph/resolver/product_authorization_test.go`.
- [X] T026 [US2] Enforce private authorized Product reads and full lifecycle metadata conversion in `gitstore-api/internal/graph/resolver/{product.resolvers.go,converters.go}`.
- [X] T027 [US2] Migrate Product controller ListWatcher bootstrap/list/drain/recovery from eventbus cursors to the typed durable `watchProducts` stream in `gitstore-controller-manager/internal/listwatch/graphql_listwatcher.go` and `gitstore-controller-manager/tests/contract/product_listwatcher_test.go`.
- [X] T028 [US2] Add two-API-replica Product watch replacement, replay, expiry, and materializer-unavailability integration coverage in `tests/integration/product_watch_test.go`.
- [X] T053 [US2] Correct the Namespace controller ListWatcher to use the typed durable `watchNamespaces` stream rather than `watchResources(kind: "Namespace")`; preserve bootstrap/replay/expiry behavior and add typed-stream contract coverage in `gitstore-controller-manager/internal/listwatch/{namespace_listwatcher.go,namespace_listwatcher_test.go}`.

---

## Phase 5: User Story 3 — ProductVariant-safe foreground deletion (Priority: P1)

**Goal**: Product deletion never orphans variants and converges only after a
fresh indexed blocker check.

**Independent test**: Reject deletion with a blocker; after removal observe
termination; reject a newly resolved variant targeting the terminating Product;
finalize exactly once after a fresh clear check.

- [X] T029 [P] [US3] Add ProductVariant admission tests for canonical blocking Product owner references and terminating-parent rejection in `gitstore-api/internal/cataloggrpc/server_test.go`.
- [X] T030 [US3] Resolve and persist ProductVariant-to-Product blocking owner references, including deferred resolution and terminating-target rejection, in `gitstore-api/internal/cataloggrpc/server.go` and `gitstore-api/internal/catalog/product_variant_policy.go`.
- [ ] T031 [P] [US3] Add resolver deletion matrix tests for blockers, ID lookup authorization, started/already-terminating outcomes, and no cascade in `gitstore-api/internal/graph/resolver/product_deletion_resolver_test.go`.
- [X] T032 [US3] Enforce indexed pre-mark blocker rejection and expected-version terminating state in Product Git/GraphQL deletion handling in `gitstore-api/internal/{cataloggrpc/server.go,graph/resolver/product.resolvers.go}`.
- [X] T033 [P] [US3] Add Product controller finalizer/retry/conflict tests in `gitstore-controller-manager/internal/product/reconciler_test.go`.
- [X] T034 [US3] Implement dedicated Product reconciliation, fresh blocker check, bounded requeue, status ownership, and finalizer completion client in `gitstore-controller-manager/internal/product/{reconciler.go,graphql_client.go}`.
- [X] T035 [US3] Register Product list/watch/cache/reconciler ownership in `gitstore-controller-manager/cmd/controller/main.go`.
- [ ] T036 [US3] Add deletion race, controller-replacement, and final-removal-once integration coverage in `tests/integration/product_deletion_lifecycle_test.go`.

---

## Phase 6: User Story 4 — Controller convergence and category fan-out (Priority: P1)

**Goal**: Product lifecycle reconciliation converges without taking ownership
of CategoryTaxonomy status or breaking category counts.

**Independent test**: Run two controllers through Product add/delete/category
reassignment and replacement; only affected category counts converge.

- [X] T037 [P] [US4] Add durable Product-event category enqueue tests for create, delete, category reassignment, and status/finalizer non-fan-out in `gitstore-controller-manager/internal/categorytaxonomy/products_test.go`.
- [X] T038 [US4] Route the existing Product-to-CategoryTaxonomy enqueue handler through durable Product events while preserving affected-only behavior in `gitstore-controller-manager/internal/categorytaxonomy/{products.go,watch.go}`.
- [ ] T039 [US4] Add two-controller stale-status and replica-handoff integration tests in `tests/integration/product_controller_convergence_test.go`.
- [ ] T040 [US4] Make Product controller status writes preserve other system-owned status and recompute after optimistic conflicts in `gitstore-controller-manager/internal/product/reconciler.go` and `gitstore-api/internal/graph/resolver/product_status.go`.

---

## Phase 7: User Story 5 — Least-privilege and auditability (Priority: P2)

**Goal**: Human, service-account, and controller identities get only their
authorized Product capabilities with attributable audit evidence.

**Independent test**: Exercise all Product paths as reader, author, controller,
and unauthorized subjects in two namespaces.

- [ ] T041 [P] [US5] Add cross-provider Product capability/audit tests for human, service-account, and controller principals in `gitstore-api/internal/middleware/security/graphql_product_lifecycle_test.go`.
- [ ] T042 [US5] Wire Product lifecycle authorization decisions and redacted audit fields through configured providers in `gitstore-api/internal/{middleware/security/graphql.go,auth/,graph/resolver/}`.
- [ ] T043 [US5] Add end-to-end multi-namespace Product authorization regression coverage in `tests/integration/product_authorization_test.go`.

---

## Phase 8: User Story 6 — Retirement compatibility (Priority: P2)

**Goal**: Product retirement remains private desired state, excludes future
release candidates, and never rewrites active snapshots.

**Independent test**: Retire an active Product, verify management visibility and
unchanged active snapshot, then reject a release candidate that explicitly
includes it or its child variant.

- [X] T044 [P] [US6] Add Product lifecycle-state validation and admission tests in `gitstore-api/internal/cataloggrpc/product_retirement_test.go`.
- [X] T045 [US6] Implement `spec.lifecycle.state` validation/defaulting and persisted admission semantics in `gitstore-api/internal/cataloggrpc/server.go` and `gitstore-api/internal/catalog/product.go`.
- [ ] T046 [P] [US6] Add release-preparation compatibility contract tests without implementing publication resources in `tests/contract/product_retirement_contract_test.go`.
- [X] T047 [US6] Document Product retirement’s release-candidate rejection contract and immutable-publication boundary in `docs/products/product-spec.md` and `docs/products/publication-lifecycle.md`.

---

## Phase 9: Polish, rollout, and production evidence

- [X] T048 [P] Add Product lifecycle/watch rollout, mixed-version deny, rollback, cursor recovery, and finalizer operator guidance in `docs/runbooks/product-lifecycle.md` and `docs/configuration.md`.
- [ ] T049 [P] Complete bounded overload, observability, and alert assertions for Product journal/materializer/controller metrics in `tests/contract/product_observability_test.go`.
- [ ] T050 Implement the full two-API/two-controller Git-push/GraphQL/watch/deletion-race capacity verifier in `tests/capacity/profiles/product-lifecycle.js` and Makefile evidence export in `Makefile`.
- [ ] T051 Implement Product lifecycle replacement/materializer/mid-deletion chaos assertions in `tests/chaos/profiles/product-lifecycle.json`.
- [X] T052 Run focused suites and production-readiness validation, recording results in `specs/055-product-lifecycle/quickstart.md`: `make test`, `make build`, `make capacity TARGET=product PROFILE=lifecycle MODE=alpha`, and `make pr-ready`.

## Dependencies and execution order

```text
Setup (T001–T004)
  → Foundational contracts/primitives (T005–T015)
  → US1 authoring (T016–T021)
  → US2 durable reads/watch (T022–T028)
  → US3 safe deletion (T029–T036)
  → US4 controller/category convergence (T037–T040)
  → US5 authorization/audit (T041–T043)
  → US6 retirement compatibility (T044–T047)
  → Polish/evidence (T048–T052)
```

US1 and US2 can begin after foundational work; US3 depends on US1 admission
and the durable state primitives; US4 depends on US2/US3 Product controller
registration. US5 may run alongside US1–US4 after T008, and US6 may run after
US1 because it is admission-contract work only.

## Parallel opportunities

- After T006, run T007, T009, T011, and T013 in parallel.
- After T015, run US1 admission tests (T016) and US2 watch tests (T022) in
  parallel.
- In US3, T029, T031, and T033 are independent test-first workstreams.
- In US4–US6, T037, T041, T044, and T046 are parallel test workstreams.
- In polish, T048, T049, and T051 can run in parallel before T050/T052.

## Implementation strategy

1. Deliver the foundational schema, security, indexed lifecycle, and durable
   journal prerequisites first.
2. Deliver US1 as the first functional slice: Git/GraphQL parity with no
   direct desired-state write.
3. Add durable reads/watches, then foreground deletion and Product controller
   convergence before enabling Product durable watch fleet-wide.
4. Preserve CategoryTaxonomy fan-out, harden least privilege, and add
   retirement compatibility without widening into publication implementation.
5. Enable only after capacity, recovery, observability, documentation, and
   `make pr-ready` evidence pass.
