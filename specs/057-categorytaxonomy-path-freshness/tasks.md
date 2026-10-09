# Tasks: CategoryTaxonomy Path Freshness, Git-Backed Mutations, and Descendant Filtering

**Input**: Design documents from `/specs/057-categorytaxonomy-path-freshness/`
**Prerequisites**: plan.md, spec.md, research.md, data-model.md, contracts/ (graphql.md, admission-diagnostics.md, datastore-ancestor-index.md), quickstart.md

**Tests**: Test-First Development (Constitution Principle I — NON-NEGOTIABLE). Every story writes failing tests before implementation.

**Organization**: one phase per user story, in priority order. Story labels match spec.md, which has no User Story 3 (the old deprecation story was folded into US2 when `Category.path`/`depth` were removed).

**Already merged prerequisites** (do not redo): #456, #457 and #458 cover:
- controllers accept `CONFLICT` as well as `RESOURCE_VERSION_CONFLICT`;
- a schema-validation guard for controller queries;
- the fence is always on;
- configuration is grouped by service (`api.watch.journal`, `[push_limits]`).

The working tree already contains the user's schema edit removing `Category.path`/`depth` from `shared/schemas/category.graphqls`.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: can run in parallel (different files, no dependency on an incomplete task)
- **[Story]**: US1, US2, US4–US8 (see spec.md)

## Path Conventions

- API: `gitstore-api/internal/...`, `gitstore-api/cmd/gitctl/`
- Controller: `gitstore-controller-manager/internal/...`
- Git service: `gitstore-git-service/src/...`
- Shared schema: `shared/schemas/*.graphqls`, then run `go generate ./...` in `gitstore-api`
- Integration: `tests/integration/` (a separate Go module; some helpers are generated at test time, so also run `go vet ./...` there)
- Capacity: `tests/capacity/{profiles,preflight,verifiers}/`, `scripts/run-capacity-target.sh`

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: fixtures and scaffolding used by several stories.

- [X] T001 Create the shared rejection golden fixture `tests/fixtures/admission-rejection-golden.json`. Each case holds a manifest path, `catalogv1.ValidationError` entries (`FilePath`, `Field`, `Constraint`, `Message`) and the expected flattened `file: message; …` string, following `gitstore-git-service/src/git/hooks/validation_handler.rs:192-203`. Include empty-path, single-error and multi-error cases.
- [X] T002 [P] Create the category manifest validation fixture set `gitstore-api/internal/cataloggrpc/testdata/category/`, with one Markdown manifest per FR-016 rule:
  - one valid root and one valid child;
  - one invalid manifest each for: missing `spec.title`, self-parent, cross-namespace `parentRef`, `media` without `fileRef.name`, invalid envelope;
  - one manifest whose parent is terminating.

  Push and mutation parity tests use it (SC-006).
- [X] T003 [P] Add an empty Scylla migration `gitstore-api/internal/datastore/scylla/migrations/010_category_ancestor_index.cql` containing the DDL from `contracts/datastore-ancestor-index.md`. Add the table to `migration_schema_test.go` and to the table list in `migration_test.go`.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: the kind-neutral error envelope and the admission decision every mutation story depends on (plan slice 1).

**⚠️ CRITICAL**: no user story work starts until this phase is complete.

### Tests (write first, must fail)

- [X] T004 [P] Unit tests for `admission.Diagnostic`, `admission.Error` and `FormatRejection` in `gitstore-api/internal/admission/error_test.go`. Cover:
  - only the four keys `code`, `diagnostics`, `phase`, `commit`;
  - `phase` only when `code=ADMISSION_REJECTED`;
  - `commit` only when `phase=POST_RECEIVE`;
  - absent keys omitted;
  - `FormatRejection` matching every case in `tests/fixtures/admission-rejection-golden.json`.
- [X] T005 [P] Rust test in `gitstore-git-service/src/git/hooks/validation_handler.rs` asserting that the existing flattening produces the expected strings from `tests/fixtures/admission-rejection-golden.json`, for byte parity with T004.
- [X] T006 [P] Tests in `gitstore-api/internal/cataloggrpc/server_test.go` asserting that `admitCategoryTaxonomyWithContext` returns an `EntryDecision`:
  - **Accepted** for a valid manifest;
  - **NoOp** for an identical re-admission;
  - **Denied** with diagnostics when a policy denies;
  - **Failed** when the store write fails.
- [X] T007 [P] Test in `gitstore-api/internal/cataloggrpc/server_test.go` that a push-path (`AdmitResources`) denial of an update to an existing category:
  - records `AdmissionAccepted=False` with reason `AdmissionReportFailed`;
  - keeps the last accepted spec/body/generation;
  - leaves a denied create with no record.
- [X] T008 [P] Tests in `gitstore-api/internal/cataloggrpc/committed_manifest_test.go` (new) for CategoryTaxonomy. `AdmitCommittedManifest` must:
  - return `*admission.Error{Code: ADMISSION_REJECTED, Phase: POST_RECEIVE, CommitSHA}` on denial;
  - set `result.NoOp=true` on a no-op;
  - return superseded when HEAD has different content.
- [X] T009 [P] Namespace error migration tests in `gitstore-api/internal/graph/resolver/namespace_error_test.go` (new or extended). Cover every row of the "Namespace and status-write migration" table in `contracts/admission-diagnostics.md`:
  - new code, `phase` presence and `diagnostics[].reason`;
  - `NAMESPACE_DELETION_BLOCKED` becomes `FAILED_PRECONDITION` with one diagnostic per blocker, in order.
- [X] T010 [P] Status-write conflict tests in `gitstore-api/internal/graph/resolver/status_generic_test.go`: `statusConflictError` emits `code: CONFLICT` with `diagnostics[{reason: RESOURCE_VERSION_CONFLICT}]` and the current version in `message`, and has no `resourceVersion` key.

### Implementation

- [X] T011 Implement the shared types in `gitstore-api/internal/admission/error.go`:
  - `Phase`, `DiagnosticLevel`, `Diagnostic{Reason, Message, Level, File, Field}`;
  - `Code` constants;
  - `Error{Code, Phase, CommitSHA, Diagnostics}` with `Error()` and `ToGQLError()`;
  - `FormatRejection`;
  - a `FromValidationErrors([]*catalogv1.ValidationError)` converter that maps `Constraint` to `Reason` and falls back to `VALIDATION_FAILED`.

  Makes T004 pass.
- [X] T012 Add `NoOp bool` and `Warnings []Diagnostic` to `CommittedManifestResult`, and add the `EntryDecision` type, in `gitstore-api/internal/admission/committed_manifest.go`.
- [X] T013 Make `admitCategoryTaxonomyWithContext` (`gitstore-api/internal/cataloggrpc/server.go:2684-2876`) return `EntryDecision` instead of logging and dropping denials and store errors (`:2697,2746-2751,2795,2821,2866`), and report NoOp from the early return at `:2841-2843`. Collect the decisions in `admitParsedEntries` (`:1317`). Makes T006 pass.
- [X] T014 On the push path (`AdmitResources`), record a Denied decision for an existing category as `AdmissionAccepted=False`/`AdmissionReportFailed` through a status patch, without touching spec/body/generation. Denied creates are logged and counted with `gitstore_admission_rejections_total{kind,phase}`. File: `gitstore-api/internal/cataloggrpc/server.go`. Makes T007 pass.
- [X] T015 Make `AdmitCommittedManifest` (`gitstore-api/internal/cataloggrpc/committed_manifest.go:27-112`) map Denied/Failed to `*admission.Error` (POST_RECEIVE, with the commit) and NoOp to `result.NoOp`. Makes T008 pass.
- [X] T016 Rebuild `gitstore-api/internal/graph/resolver/namespace_error.go` on `admission.Error`:
  - choose codes by reason;
  - pass `phase` explicitly (preflight is `PRE_RECEIVE`; errors after `AdmitCommittedManifest` are `POST_RECEIVE`);
  - drop the `phase: STRUCTURAL|POLICY`, `reason` and `reasons` keys.

  Remove `Phase`/`Code*` from `gitstore-api/internal/namespace/decision.go`. Relabel the `phase` label of `gitstore_namespace_validation_*` in `internal/namespace/metrics.go` as `code`. Update `recordNamespaceGraphQLError` (`service.go:1103`) to read `code` and `diagnostics[0].reason`. Makes T009 pass.
- [X] T017 Change `statusConflictError` (`gitstore-api/internal/graph/resolver/status_generic.go:19`) and the `completeNamespaceDeletion` conflict path (`namespace.resolvers.go`) to the folded `CONFLICT` envelope. Update the `RESOURCE_VERSION_CONFLICT` mentions in the `update*Status` docstrings across `shared/schemas/*.graphqls`. Makes T010 pass. Merged #456 already accepts both codes in controllers.
- [X] T018 [P] Update the namespace error consumers: tests under `gitstore-api/internal/graph/resolver/` and `internal/middleware/security/`, `tests/integration/namespace_*_test.go`, and any `gitstore-admin` code that reads `extensions.phase`/`reason`.
- [X] T019 Add `gitstore-api/internal/admissionreport/` (new package):
  - a per-request `Collector` in the context, holding `{path, commit, diagnostics}` entries;
  - an `AroundResponses` extension that merges `response.Extensions["admission"]` without overwriting other keys and omits it when empty.

  Register it in `gitstore-api/internal/app/server.go:387-398`. Add a unit test in `admissionreport/extension_test.go` covering aliases, multiple mutations and coexisting keys.

**Checkpoint**: the envelope, admission decisions and Namespace migration are done. `make build lint test` pass.

---

## Phase 3: User Story 1 — Reading a category's current position after a re-parent (Priority: P1) 🎯 MVP

**Goal**: the hierarchy is visible only through `status.resolved`, which reflects re-parents once reconciled. There is no admission-time fallback.

**Independent Test**: re-parent a category by push, wait for reconcile, then query `status.resolved.path`/`depth` on it and on a pre-existing descendant.

### Tests (write first)

- [X] T020 [P] [US1] Resolver test in `gitstore-api/internal/graph/resolver/category_resolver_test.go`:
  - `status.resolved` is `null` for an unreconciled category, even when `AncestorPath` is set;
  - after a status write it returns the resolved `path`/`depth`;
  - no resolver reads `AncestorPath`.
- [X] T021 [P] [US1] Integration test `tests/integration/category_taxonomy_test.go` (`TestCategoryResolvedPathAfterReparent`): push A→B, push a re-parent of B under C, poll until the cascade converges, then assert `status.resolved.path` for B and for a descendant that wasn't pushed.

### Implementation

- [X] T022 [US1] Run `go generate ./...` in `gitstore-api` against the user's schema edit that removed `Category.path`/`depth` from `shared/schemas/category.graphqls`.
- [X] T023 [US1] Remove the `AncestorPath` split and the `Path`/`Depth` assignment from `DatastoreCategoryTaxonomyToGraphQL` in `gitstore-api/internal/graph/resolver/converters.go:432-438,546-547` (and the unused `strings` import). Keep the `status.resolved` mapping at `:677-678`.
- [X] T024 [US1] Fix any compile or test fallout from the removed fields across `gitstore-api`, `tests/integration` and `gitstore-admin` (grep for `path`/`depth` selections on `Category`).

**Checkpoint**: US1 is independently verifiable.

---

## Phase 4: User Story 4 — Creating a category through the API (Priority: P1)

**Goal**: `createCategory` commits `categories/<name>.md` to `gitstore-system`, runs pre-receive checks in-process first, and admits synchronously.

**Independent Test**: create a root and a child through the API; the commits exist, and admission accepts both with the same decisions a push would make.

### Tests (write first)

- [X] T025 [P] [US4] GraphQL schema contract test in `gitstore-api/internal/graph/resolver/category_schema_contract_test.go` (new) for `createCategory`, `CreateCategoryInput`, `CategorySpecInput` and `CreateCategoryPayload` per `contracts/graphql.md`. No `clientMutationId`.
- [X] T026 [P] [US4] `ManifestValidator` tests in `gitstore-api/internal/cataloggrpc/manifest_validator_test.go` (new). Every fixture in `testdata/category/` must give the same accept/reject decision and the same diagnostics as `ValidateResources` does for an equivalent push tree (SC-006).
- [X] T027 [P] [US4] Service tests in `gitstore-api/internal/graph/resolver/category_lifecycle_test.go` (new; fake `GitWriter`/admitter modelled on `product_lifecycle_test.go:52`). Cover:
  - the commit goes to `categories/<name>.md` in `<ns>/gitstore-system` with message `Create CategoryTaxonomy <name>` and the caller as author;
  - an existing name returns `ALREADY_EXISTS` with no commit;
  - a pre-receive denial returns `ADMISSION_REJECTED`/`PRE_RECEIVE` with diagnostics and no commit;
  - a post-receive denial returns `POST_RECEIVE` with the commit, and no stale record;
  - an unknown parent is admitted with `ParentResolved=False`;
  - a byte-identical re-submit doesn't commit (FR-019).
- [X] T028 [P] [US4] Authorization tests in `gitstore-api/internal/middleware/security/graphql_category_lifecycle_test.go` (new; model on `graphql_product_lifecycle_test.go:54`): `createCategory` requires an authenticated principal and `categoryTaxonomy.create`, scoped to the input namespace and name, and is denied before any side effect.
- [X] T029 [P] [US4] Replica test in `gitstore-api/internal/cataloggrpc/category_replica_test.go` (new; model on `file_replica_test.go`): two services create the same category concurrently, giving exactly one success and one `ALREADY_EXISTS`/`CONFLICT`; a mutation racing a push to the same file gives `CONFLICT` (SC-007).

### Implementation

- [X] T030 [US4] Add `createCategory`, `CreateCategoryInput`, `CategorySpecInput` and `CreateCategoryPayload` to `shared/schemas/category.graphqls` (docstrings without internal references), then run `go generate ./...`.
- [X] T031 [US4] Define `admission.ManifestValidator` and `ManifestValidationRequest` in `gitstore-api/internal/admission/manifest_validator.go`. Implement `ValidateManifest` on `cataloggrpc.Server` in `gitstore-api/internal/cataloggrpc/manifest_validator.go` by wrapping the per-tree body of `ValidateResources` (`validateResourceBlobs`, `validateImmutableResourceChanges`, `validateNamespacePolicies`) for a single blob. Return `[]admission.Diagnostic`. Makes T026 pass.
- [X] T032 [US4] Wire the `ManifestValidator` into the resolver `Service` deps at `gitstore-api/internal/app/server.go:227-254` and `gitstore-api/internal/graph/resolver/service.go` (`ServiceDeps`).
- [X] T033 [US4] Extract the shared helpers in `gitstore-api/internal/graph/resolver/manifest_helpers.go` (new):
  - `renderManifest(envelope, body)`, generalised from the inline Product code (`service.go:214-224`);
  - `markdownBody`, generalised from `namespaceMarkdownBody` (`:1065`);
  - `convergeCommittedResource`, generalised from `convergeCommittedNamespace` (`:1006-1045`). It accepts a record only if `GitCommitSHA == result.CommitSHA` or `result.NoOp`.

  Leave the Product and Namespace callers unchanged.
- [X] T034 [US4] Implement `Service.CommitCategoryManifest(ctx, input, caller, create bool)` in `gitstore-api/internal/graph/resolver/category_service.go` (new). Steps:
  1. Check the envelope.
  2. Look up `<ns>/gitstore-system`.
  3. Render the manifest.
  4. Compare with the file at HEAD; if byte-identical, skip the commit (FR-019).
  5. Run `ValidateManifest`; a denial is a `PRE_RECEIVE` `admission.Error`.
  6. `CommitFileForRepo`.
  7. `AdmitCommittedManifest`.
  8. `convergeCommittedResource`.
  9. Add any warnings to the `admissionreport.Collector`.

  Map superseded to `CONFLICT`/`SUPERSEDED`. Makes T027 pass.
- [X] T035 [US4] Implement the `CreateCategory` resolver in `gitstore-api/internal/graph/resolver/category.resolvers.go`, returning `CreateCategoryPayload{category}` and `admission.Error.ToGQLError()` on failure.
- [X] T036 [US4] Authorize `createCategory` as `categoryTaxonomy.create` in `gitstore-api/internal/middleware/security/graphql.go`, and add it to `graphqlFieldRequiresAuthorization` (`:763`). Grant the action in `config/policy.yaml` and `gitstore-api/policy.yaml.example`. Makes T028 pass.
- [X] T037 [US4] Add `gitstore_category_mutation_total{operation,outcome}` and `gitstore_category_mutation_duration_seconds{operation}` in `gitstore-api/internal/graph/resolver/category_metrics.go` (new), plus structured log fields `commit`, `phase` and `diagnostic_count`.

**Checkpoint**: API-only clients can build a taxonomy. T029 passes.

---

## Phase 5: User Story 5 — Updating or re-parenting a category through the API (Priority: P1)

**Goal**: `updateCategory` commits to stored provenance, keeps the Markdown body when it's omitted, and triggers the existing cascade.

**Independent Test**: push `categories/laptops.md` to a non-system repository, then `updateCategory` it with a new `parentRef`. The commit lands at that path, the body is kept, and descendants converge.

### Tests (write first)

- [X] T038 [P] [US5] Schema contract test in `category_schema_contract_test.go` for `updateCategory`, `UpdateCategoryInput` and `UpdateCategoryPayload`.
- [X] T039 [P] [US5] Service tests in `category_lifecycle_test.go` for update:
  - the commit goes to `RepositoryID`/`SourcePath` with message `Update CategoryTaxonomy <name>`;
  - an omitted `body` is preserved and an explicit `body` replaces it;
  - name/namespace changes return `ADMISSION_REJECTED` (`IMMUTABLE_NAME`/`IMMUTABLE_NAMESPACE`) with no commit;
  - a terminating category returns `FAILED_PRECONDITION`/`CATEGORY_TERMINATING`;
  - missing provenance returns `FAILED_PRECONDITION`/`PROVENANCE_UNAVAILABLE`;
  - a changed owner annotation is rejected;
  - superseded returns `CONFLICT`.
- [X] T040 [P] [US5] Authorization tests in `graphql_category_lifecycle_test.go`: `updateCategory` requires `categoryTaxonomy.update`, scoped from the stored record (namespace, name, owner, repositoryID), never from input fields.
- [X] T041 [P] [US5] Integration test `tests/integration/category_taxonomy_test.go` (`TestUpdateCategoryReparentsAcrossRepository`), covering the independent test above end to end.

### Implementation

- [X] T042 [US5] Add `updateCategory`, `UpdateCategoryInput` and `UpdateCategoryPayload` to `shared/schemas/category.graphqls`, then run `go generate ./...`.
- [X] T043 [US5] Extend `CommitCategoryManifest` for update in `gitstore-api/internal/graph/resolver/category_service.go`:
  - load by name and require provenance;
  - reject a terminating category;
  - run `guardOwnerAnnotationUnchanged` (`ownership.go:36`);
  - read the current file with `ReadFileForRepo` for body preservation and as `OldContent` for `ValidateManifest`;
  - commit to stored provenance.

  Makes T039 pass.
- [X] T044 [US5] Implement the `UpdateCategory` resolver in `category.resolvers.go`.
- [X] T045 [US5] Authorize `updateCategory` as `categoryTaxonomy.update` from the stored record in `internal/middleware/security/graphql.go`, modelled on the `deleteCategory` stored check (`:527-550`). Add it to `graphqlFieldRequiresAuthorization`, and grant it in `config/policy.yaml` and `policy.yaml.example`. Makes T040 pass.

**Checkpoint**: re-parenting through the API is verified by T041.

---

## Phase 6: User Story 2 — Removed legacy hierarchy fields (Priority: P2)

**Goal**: one hierarchy source, discoverable by introspection, with the removal documented as a breaking change.

**Independent Test**: introspect `Category`: there is no `path`/`depth`, and selecting them fails validation.

### Tests (write first)

- [X] T046 [P] [US2] Introspection test in `category_schema_contract_test.go`: `Category` has no `path`/`depth` field, and a query selecting `path` fails validation (SC-005).

### Implementation

- [X] T047 [US2] Update the `status.resolved` field descriptions in `shared/schemas/category.graphqls`: `null` until first reconcile, and eventually consistent after an ancestor moves (FR-006). Run `go generate ./...`.
- [X] T048 [P] [US2] Document the hierarchy source in `docs/categories/category-taxonomy-spec.md` and `docs/api-reference.md`, with no internal spec or ADR references.

**Checkpoint**: US2 is verifiable.

---

## Phase 7: User Story 6 — Deleting a category through Git-backed removal (Priority: P2)

**Goal**: `deleteCategory` removes the manifest in Git and returns `{ category, outcome }`. A controller-only `completeCategoryDeletion` finishes removal.

**Independent Test**: delete a leaf with assigned products. The file is removed in one commit, the outcome is `TERMINATION_STARTED`, products decouple, and the controller completes deletion through `completeCategoryDeletion`. A repeat returns `ALREADY_TERMINATING` with no commit.

### Tests (write first)

- [X] T049 [P] [US6] Schema contract test in `category_schema_contract_test.go`:
  - `DeleteCategoryPayload` is `{ category, outcome: ResourceDeletionOutcome! }` with no `deletedCategoryId`/`orphanedProductIds`;
  - `completeCategoryDeletion`, `CompleteCategoryDeletionInput{namespace,name,resourceVersion}` and `CompleteCategoryDeletionPayload{id}` exist;
  - `UpdateCategoryStatusInput.completeDeletion` is deprecated with a reason naming `completeCategoryDeletion`.
- [X] T050 [P] [US6] Service tests in `category_lifecycle_test.go` for delete:
  - children present returns `FAILED_PRECONDITION`/`CHILD_CATEGORIES_PRESENT` with no commit;
  - an already-terminating category returns `ALREADY_TERMINATING` with no commit;
  - otherwise `DeleteFileForRepo` runs at stored provenance with message `Delete CategoryTaxonomy <name>`, admission marks the category Terminating, and the result is `TERMINATION_STARTED`.
- [X] T051 [P] [US6] Committed-manifest delete test in `cataloggrpc/committed_manifest_test.go`: `Operation=Delete`/`Kind=CategoryTaxonomy` delegates to the `deleteResource` category branch (`server.go:1573-1606`), which checks blockers and calls `MarkCategoryTaxonomyDeletion`.
- [X] T052 [P] [US6] `completeCategoryDeletion` tests in `category_resolver_test.go`:
  - success removes the record and returns `{id}`;
  - a resource-version mismatch returns `CONFLICT`;
  - the category not terminating, children present, or products still to decouple return `FAILED_PRECONDITION` with `CATEGORY_NOT_TERMINATING`, `CHILD_CATEGORIES_PRESENT` or `PRODUCT_DECOUPLING_INCOMPLETE` respectively;
  - the deprecated `updateCategoryStatus.completeDeletion` flag still works.
- [X] T053 [P] [US6] Authorization tests in `graphql_category_lifecycle_test.go`:
  - `deleteCategory` is checked as `categoryTaxonomy.delete` from the stored record; `category.delete` is no longer checked;
  - `completeCategoryDeletion` is checked as `categoryTaxonomy.purge`;
  - both require authentication.
- [X] T054 [P] [US6] Controller test in `gitstore-controller-manager/internal/categorytaxonomy/deletion_client_test.go`: `CompleteDeletion` sends `completeCategoryDeletion`, and a conflict maps through `graphqlclient.IsConflictCode`. Add the new operation to `graphql_client_schema_test.go`.

### Implementation

- [X] T055 [US6] Schema changes in `shared/schemas/category.graphqls`, then run `go generate ./...`:
  - replace `DeleteCategoryPayload` with `{ category, outcome }`;
  - add `completeCategoryDeletion` and its input and payload;
  - mark `UpdateCategoryStatusInput.completeDeletion` `@deprecated(reason: "Use completeCategoryDeletion. Removed in the next release.")`.
- [X] T056 [US6] Extend the delete branch of `AdmitCommittedManifest` (`cataloggrpc/committed_manifest.go:114-137`) to support CategoryTaxonomy: check provenance, then delegate to the category case of `deleteResource`. Makes T051 pass.
- [X] T057 [US6] Replace the datastore-only `Service.DeleteCategory` (`service.go:388-428`) with `Service.DeleteCategoryManifest` in `category_service.go`:
  1. If already terminating, return `ALREADY_TERMINATING`.
  2. `HasBlockingOwnerDependents`.
  3. `DeleteFileForRepo`.
  4. `AdmitCommittedManifest` with Delete.
  5. Re-read and return `TERMINATION_STARTED`.

  Update the `DeleteCategory` resolver in `category.resolvers.go`. Makes T050 pass.
- [X] T058 [US6] Implement the `CompleteCategoryDeletion` resolver in `category.resolvers.go` by reusing `Service.CompleteCategoryDeletion` (`service.go:433`). Keep the deprecated flag path delegating to the same code. Makes T052 pass.
- [X] T059 [US6] Authorization in `internal/middleware/security/graphql.go`:
  - `deleteCategory` is checked as `categoryTaxonomy.delete`;
  - `completeCategoryDeletion` is checked as `categoryTaxonomy.purge`, with input namespace and name;
  - add the new mutation to `graphqlFieldRequiresAuthorization`;
  - grant both in `config/policy.yaml` and `policy.yaml.example`, giving `categoryTaxonomy.purge` to the controller role.

  Makes T053 pass.
- [X] T060 [US6] Switch the controller deletion client `gitstore-controller-manager/internal/categorytaxonomy/deletion_client.go:74-90` from `updateCategoryStatus { completeDeletion: true }` to `completeCategoryDeletion`. Makes T054 pass.

**Checkpoint**: Git and the datastore can't diverge on delete (SC-008).

---

## Phase 8: User Story 7 — Listing a category subtree (Priority: P2)

**Goal**: `categories(filter: { descendantOf, includeSelf, maxDepth })`, served from the closure ancestor index, which is maintained by status writes and final removal and is repairable.

**Independent Test**: for `electronics → computers → laptops` plus `computers-refurb`:
- `descendantOf: computers` returns only `laptops`;
- `includeSelf` adds `computers`;
- `descendantOf: electronics, maxDepth: 1` returns `computers` and `computers-refurb`.

### Tests (write first)

- [X] T061 [P] [US7] Datastore contract suite: a `t.Run("CategoryAncestorIndex", …)` block in `gitstore-api/tests/contract/datastore/contract_test.go` and `pagination_test.go`, run against memdb and Scylla. Cover:
  - segment-only matching;
  - `includeSelf` and `maxDepth`;
  - re-parent removes stale rows;
  - final removal removes rows;
  - `status.resolved=null` gives no rows;
  - repeated status writes are idempotent;
  - concurrent writes keep the higher resource version;
  - cursor round trip;
  - a cursor from the other list mode is rejected.
- [X] T062 [P] [US7] Scylla repair tests in `gitstore-api/internal/datastore/scylla/repair_test.go`:
  - `BuildRepairPlan` reports missing, dangling and stale `category_ancestor_index` rows derived from `status.resolved.path`;
  - `--confirm` converges them;
  - audit is clean afterwards.
- [X] T063 [P] [US7] Resolver tests in `category_resolver_test.go` for `categories(filter:)`:
  - depth-then-name ordering;
  - an unknown `descendantOf` gives an empty connection;
  - `maxDepth` outside 1–128 gives `BAD_USER_INPUT`;
  - a keyset cursor on a filtered query (and the reverse) gives `BAD_USER_INPUT`;
  - stale index rows whose UID no longer matches are dropped;
  - the unfiltered query is unchanged (SC-004).
- [X] T064 [P] [US7] Authorization test in `graphql_category_lifecycle_test.go`: a filtered list requires exactly `categoryTaxonomy.list` in the namespace.

### Implementation

- [X] T065 [US7] Add `CategoryAncestorIndex`, `CategoryDescendant`, `CategoryDescendantQuery` and `MaxCategoryHierarchyDepth` to `gitstore-api/internal/datastore/datastore.go`, plus the closure cursor encoding (`closure|<depth>|<name>`) in `gitstore-api/internal/datastore/` next to the keyset cursor helpers. Forward the interface in `gitstore-api/internal/datastore/instrumented.go` (`:580-657` pattern).
- [X] T066 [US7] memdb:
  - add the `category_ancestor_index` table with `id`, `ancestor` and `descendant` indexes in `gitstore-api/internal/datastore/memdb/schema.go`;
  - maintain rows in the same transaction as `UpdateCategoryTaxonomyStatus` and `CompleteCategoryTaxonomyDeletion` (`backend.go:864-912`, `owner_references.go:214-243`);
  - implement `ListCategoryDescendants`.
- [X] T067 [US7] Scylla:
  - in `UpdateCategoryTaxonomyStatus` (`backend.go:972-1005`), decode the old resolved path, CAS the row, then through `mutationExecutor.executeUpdate` (`recovery.go:69-130`) upsert all current rows and delete obsolete (ancestor, depth) rows, returning `RepairRequiredError` if that still fails;
  - delete rows projections-first in `CompleteCategoryTaxonomyDeletion` and `DeleteCategoryTaxonomy`, via `executeDelete`;
  - implement `ListCategoryDescendants` with the range query from the contract.

  Files: `gitstore-api/internal/datastore/scylla/category_ancestor_index.go` (new) and `backend.go`.
- [X] T068 [US7] Scylla repair in `gitstore-api/internal/datastore/scylla/repair.go`:
  - add the `category_ancestor_index` projection (`knownProjectionTable` `:648-670`);
  - have `Snapshot` read `namespace, name, uid, resource_version, status` (paged) and scan the index;
  - have `expectedProjections` derive the rows;
  - use the conditional writers.

  Make sure `gitctl scylla-projection-audit|repair` (`gitstore-api/cmd/gitctl/main.go:85-93,277-340`) includes it. Makes T062 pass.
- [X] T069 [US7] Add `filter: CategoryFilterInput` and its docstrings to `categories` in `shared/schemas/category.graphqls`, then run `go generate ./...`.
- [X] T070 [US7] Implement the filtered branch of the `Categories` resolver (`category.resolvers.go:129-137`):
  - validate `maxDepth`;
  - call `ListCategoryDescendants`;
  - fetch records by UID with bounded concurrency (≤ 16), up to the page size (≤ 100);
  - drop mismatched rows;
  - build the connection with closure cursors.

  Makes T063 pass.

**Checkpoint**: subtree listing works on memdb and Scylla, and repair converges.

---

## Phase 9: User Story 8 — Navigating parent and children (Priority: P3)

**Goal**: `Category.parent` and `Category.children` return real data.

**Independent Test**: `computers { parent { name } children { name } }` returns `electronics` and `[laptops]`.

### Tests (write first)

- [X] T071 [P] [US8] Resolver tests in `category_resolver_test.go`:
  - `parent` is the resolved parent when `ParentResolved=True`, and `null` for roots, unresolved parents or unreconciled categories;
  - `children` lists direct children ordered by name, capped at 100, and is empty for unreconciled children.

### Implementation

- [X] T072 [US8] Set `parent: {resolver: true}` and `children: {resolver: true}` for `Category` in `gitstore-api/gqlgen.yml`, then run `go generate ./...`. Remove the hardcoded `Parent: nil`/`Children: []` from `converters.go:544-545`.
- [X] T073 [US8] Implement the `Parent` resolver (look up `spec.parentRef.name` when `ParentResolved=True`) and the `Children` resolver (`ListCategoryDescendants` with `maxDepth=1`, limit 100) in `category.resolvers.go`. Makes T071 pass.

**Checkpoint**: all user stories are complete.

---

## Phase 10: Polish & Cross-Cutting Concerns

- [X] T074 [P] Operator and API docs, with no internal spec, ADR or FR references:
  - `docs/api-reference.md`: the new mutations, the filter, the error envelope and codes, the reason table, `extensions.admission`;
  - `docs/categories/category-taxonomy-spec.md`;
  - the Namespace error changes in `docs/runbooks/namespace-admission.md` ("Stable response codes");
  - the ancestor-index audit/repair step in `docs/configuration.md` or the relevant runbook.
- [X] T075 [P] Release notes and upgrade notes for the breaking changes:
  - `Category.path`/`depth` removed;
  - `DeleteCategoryPayload` fields replaced;
  - Namespace error envelope and codes;
  - `RESOURCE_VERSION_CONFLICT` folded into `CONFLICT`;
  - `updateCategoryStatus.completeDeletion` deprecated;
  - after rollout, run `gitctl scylla-projection-audit` and then `scylla-projection-repair --confirm`.
- [X] T076 Replica and process-replacement tests in `gitstore-api/internal/cataloggrpc/category_replica_test.go`:
  - concurrent status writes on two replicas converge the index to the higher resource version;
  - an API restart between commit and admission, followed by a retry, is a no-op and returns the record.
- [X] T077 Rolling-upgrade test `gitstore-api/internal/cataloggrpc/category_rolling_upgrade_test.go` (model on `file_rolling_upgrade_test.go`): status writes from an "old" replica that doesn't maintain the index leave gaps, and repair leaves zero audit findings (FR-029, SC-010).
- [X] T078 [P] Multi-user isolation tests in `tests/integration/category_taxonomy_test.go`:
  - user A can't create, update, delete or filter categories in user B's namespace;
  - diagnostics for a cross-namespace `parentRef` don't reveal whether the target exists.
- [X] T079 Capacity profile `tests/capacity/profiles/category-hierarchy.js`, `tests/capacity/preflight/category-hierarchy.sh` and `tests/capacity/verifiers/category-hierarchy.sh`, with a `category/hierarchy` case in `scripts/run-capacity-target.sh:35-70`. The scenario uses two API and two controller replicas:
  - build a 10,000-descendant subtree;
  - sustain `createCategory`/`updateCategory` concurrently with Git pushes;
  - re-parent the subtree root.

  Thresholds come from plan.md Performance Goals. The verifier checks SC-007 and SC-009. List the new pair in AGENTS.md and `tests/capacity/README.md`.
- [ ] T080 Record a passing `make capacity TARGET=category PROFILE=hierarchy MODE=alpha` evidence bundle, including topology and dataset proof and the verifier result. **Deferred to GH#451** (combined File/secret/category capacity acceptance on a live production topology; the companion push driver is `tests/integration/capacity_push_driver_test.go`).
- [ ] T081 Record passing `make chaos` runs of `tests/chaos/profiles/controller-restart.json` and `api-restart.json` while the capacity workload is active. After recovery, audit must report zero findings. **Deferred to GH#451** (combined File/secret/category capacity acceptance on a live production topology; the companion push driver is `tests/integration/capacity_push_driver_test.go`).
- [X] T082 [P] Metrics and log review: `gitstore_category_ancestor_index_writes_total{result}`, `gitstore_category_ancestor_index_repair_required_total`, `gitstore_admission_rejections_total{kind,phase}`, and dashboard/alert notes in the runbook.
- [X] T083 Flip ADR-0006 from `Proposed` to `Accepted (<merge date>)` in `docs/ADRs/0006-category-taxonomy-lifecycle.md` and in the index in `docs/ADRs/README.md`, keeping media `fileRef` resolution (GH#244) as an open item. Set spec.md `**Status**` per the spec roll-up convention.
- [ ] T084 Run every `quickstart.md` section against `make compose` and `make compose DATASTORE=scylla`. Then run `make pr-ready`, `cd tests/integration && go vet ./...`, and `graphify update .`. **Deferred to GH#451** (combined File/secret/category capacity acceptance on a live production topology; the companion push driver is `tests/integration/capacity_push_driver_test.go`).
- [ ] T085 After merge, file the follow-up GitHub issues:
  - Product admission reports denials as success; it should adopt `EntryDecision` and `convergeCommittedResource`;
  - `Category.products` returns all products.

  Check for existing issues first.

---

## Dependencies & Execution Order

### Phase dependencies

- **Setup (Phase 1)** → **Foundational (Phase 2)** → user stories.
- **US1 (Phase 3)**: needs only Phase 2, for the build to be green. It is the MVP.
- **US4 (Phase 4)**: needs Phase 2.
- **US5 (Phase 5)**: needs US4 (`CommitCategoryManifest`, helpers, `ManifestValidator`).
- **US2 (Phase 6)**: needs US1 (removal of the fields).
- **US6 (Phase 7)**: needs Phase 2 and the US4 helpers (T033). Its index cleanup on final removal is completed in US7 (T066, T067).
- **US7 (Phase 8)**: needs Phase 2 and T003. It is independent of US4–US6.
- **US8 (Phase 9)**: needs US7 (the index) for `children`.
- **Polish (Phase 10)**: needs all stories. T083 belongs in the PR that completes US4–US7.

### Within each story

Tests are written first and must fail. Then schema and codegen, then datastore/admission, then service, then resolver, then authorization and policy, then metrics.

### Parallel opportunities

- **Phase 1:** T002 and T003 alongside T001.
- **Phase 2:** all test tasks T004–T010 in parallel. T011 → T012 → (T013, T016, T017, T019 in parallel) → T014 and T015. T018 after T016.
- **After Phase 2:** US1, US4 and US7 can proceed in parallel, in different files apart from `category.resolvers.go` and `shared/schemas/category.graphqls`. Serialize edits to those two files, or merge carefully.
- **Within a story:** the `[P]` test tasks run together.

## Parallel Example: User Story 7

```text
Task: "Datastore contract suite for CategoryAncestorIndex (T061)"
Task: "Scylla repair tests for category_ancestor_index (T062)"
Task: "Resolver tests for categories(filter:) (T063)"
Task: "Authorization test for filtered list (T064)"
```

## Implementation Strategy

### MVP first

Phases 1–2, then Phase 3 (US1). That gives a fresh hierarchy through `status.resolved` and the new error envelope.

### Incremental delivery (matches plan slices)

1. Slice 1 = Phase 2: the envelope, the admission decision and the Namespace/CONFLICT migration.
2. Slice 2 = US1 + US2: removal of `path`/`depth`.
3. Slice 3 = US4 + US5 + US6: Git-backed mutations and `completeCategoryDeletion`.
4. Slice 4 = US7, index part (T061–T068): the ancestor index and repair. After it ships, run audit and repair.
5. Slice 5 = US7 filter (T069–T070) + US8: the subtree filter, `parent` and `children`.
6. Slice 6 = Phase 10: capacity, chaos and docs. ADR-0006 flips with the PR that completes slices 3–5.

## Notes

- `[P]` means different files and no unfinished dependency. `category.resolvers.go`, `category.graphqls` and `service.go` are shared hot spots.
- After any `.graphqls` edit, run `go generate ./...` in `gitstore-api`. Schema docstrings must not cite specs, ADRs or FR IDs.
- `tests/integration` helper sources are generated and compiled at test time. Run its tests, not only `go vet`.
- Never run `git config --global`.
