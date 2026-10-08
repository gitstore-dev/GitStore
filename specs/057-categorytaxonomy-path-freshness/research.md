# Research: CategoryTaxonomy Path Freshness, Git-Backed Mutations, and Descendant Filtering

All Technical Context unknowns are resolved below. Code references are relative to the repository root.

## R1. Mutation template and shared helpers

- **Decision**: Add `Service.CommitCategoryManifest` and `Service.DeleteCategoryManifest` in `gitstore-api/internal/graph/resolver/service.go`, modelled on `CommitProductManifest` (`service.go:179-241`) and `DeleteProductManifest` (`:243-288`). Generic logic moves into small shared helpers, used by Category first:
  - `renderManifest(envelope, body)`: the inline `---\n<yaml>---\n<body>` logic Product does at `:214-224`.
  - `markdownBody(content, parsed)`: the generic part of `namespaceMarkdownBody` (`:1065`), used to keep the existing body when `body == nil`.
  - `convergeCommittedResource`: generalised from `convergeCommittedNamespace` (`:1006-1045`).
  - `guardOwnerAnnotationUnchanged` (`ownership.go:36`) and `stringMap` (`:1207`) are reused unchanged.
- **Rationale**: There is no generic "commit and admit" service today, and Product lacks the SHA check and body preservation that the spec requires. Extracting only what Category needs avoids refactoring Product and Repository in this feature. Product can adopt the helpers in its follow-up ticket.
- **Alternatives considered**:
  - A fully generic `CommitManifest[T]` for all kinds. Rejected: it touches four kinds at once, and Product's admission fix is explicitly out of scope.
  - Copy-pasting Product's flow. Rejected: it would duplicate the bugs FR-017 exists to fix.

## R2. Pre-receive (Lane A) checks inside the API process

- **Decision**: Add an `admission.ManifestValidator` interface, implemented by `cataloggrpc.Server`, so the resolver `Service` can run pre-receive checks in-process before committing:
  - `ValidateManifest(ctx, ManifestValidationRequest{RepositoryID, Path, OldContent, NewContent}) ([]Diagnostic, error)` wraps the per-tree body of `ValidateResources` (`cataloggrpc/server.go:192`). For a single blob that covers `validateResourceBlobs` (`:323`, including the terminating-parent check at `:351-363`), `validateImmutableResourceChanges` and `validateNamespacePolicies`. On update, `OldContent` is the file at HEAD from `ReadFileForRepo`, so immutable-field checks work.
  - `ValidateManifestDeletion(ctx, …)` covers the delete precondition. For a single file the owner-reference index is authoritative: `HasBlockingOwnerDependents` sees every admitted child, including same-repository ones. The push-only "proposed child in the same push still points here" check cannot apply to a single-file mutation.
  - It is wired in `internal/app/server.go:250` next to `CommittedManifestAdmitter`.
- **Rationale**: Rust `CommitFile`/`DeleteFile` (`gitstore-git-service/src/grpc/server.rs:617,755`) run no hook pipeline. Calling the same Go functions that pre-receive calls is the only way to guarantee identical decisions (SC-006) without an extra gRPC hop.
- **Alternatives considered**:
  - Route mutations through `receive-pack`. Rejected: it adds heavy transport and makes the Git service a GraphQL dependency.
  - Re-implement the checks in the resolver. Rejected: the two copies would drift.

## R3. Returning the post-receive (Lane B) decision

- **Decision**: Change `admitCategoryTaxonomyWithContext` (`server.go:2684-2876`) to return an `admission.EntryDecision{Outcome: Accepted|NoOp|Denied|Failed, Diagnostics, CommitSHA}`. Today it is void and only logs denials and store errors (`:2697,2746-2751,2795,2821,2866`).
  - `admitParsedEntries` (`:1317`) collects the per-entry decisions.
  - `AdmitCommittedManifest` (`committed_manifest.go:27-112`) returns `*admission.Error{Code: ADMISSION_REJECTED, Phase: POST_RECEIVE, CommitSHA, Diagnostics}` on Denied or Failed. On NoOp it sets `result.NoOp=true`.
  - On the push path (`AdmitResources`), a Denied decision for an existing category records `AdmissionAccepted=False`/`AdmissionReportFailed` through a status patch, so the previous spec is kept. A denied create on push has no record to annotate; it is logged and counted.
- **Rationale**: This satisfies FR-017 on both lanes with one code path. The NoOp flag fixes a latent problem: the no-op early return (`:2841-2843`) never updates `GitCommitSHA`, so a strict SHA check would wrongly report superseded.
- **Alternatives considered**:
  - Infer the outcome by re-reading and comparing SHA only. Rejected: it cannot distinguish a denial from a no-op, and it loses diagnostics.

## R4. Own-commit verification, superseded handling, idempotency

- **Decision**:
  - **Pre-commit check**: before committing, read the file at HEAD. If the rendered bytes are identical, skip the commit and return the current record (FR-019).
  - **Post-commit check**: after admission, accept only if `record.GitCommitSHA == result.CommitSHA` or `result.NoOp`. `AdmitCommittedManifest` already adopts HEAD's SHA when the HEAD content is byte-identical (`committed_manifest.go` superseded check).
  - Superseded content maps to the existing conflict code.
- **Rationale**: This mirrors Namespace (`convergeCommittedNamespace`), which already passes the replica tests in `namespace_commit_order_test.go`.
- **Alternatives considered**: An `expectedResourceVersion` input. Deferred by the spec's assumptions; it would apply to all Git-backed kinds.

## R5. Git-backed delete

- **Decision**:
  - `DeleteCategoryManifest` follows these steps:
    1. If the category is already terminating, return `ALREADY_TERMINATING` without a commit.
    2. Run `HasBlockingOwnerDependents`.
    3. `DeleteFileForRepo(RepositoryID, SourcePath)`.
    4. `AdmitCommittedManifest{Operation: Delete, Kind: CategoryTaxonomy}`.
  - The Product-only guard at `committed_manifest.go:114-137` is extended with a CategoryTaxonomy branch that delegates to the existing `deleteResource` category case (`server.go:1573-1606`). That case already does the blocker check, metrics and `MarkCategoryTaxonomyDeletion`.
  - The payload becomes `{ category, outcome }`.
  - The current datastore-only `Service.DeleteCategory` (`service.go:388-428`) is removed.
- **Rationale**: This reuses push-path deletion semantics exactly and closes the Git/datastore divergence (SC-008).
- **Alternatives considered**: None viable. The user chose Git removal.

## R6. Diagnostics wire contract

- **Decision**:
  - **Shared type**: `admission.Diagnostic{Reason, Message, Level, File, Field}` with `Level ∈ {FAILURE, WARNING, NOTICE}`. It converts from `catalogv1.ValidationError{FilePath, Field, Constraint, Message}`; `Reason` is derived from `Constraint` through a mapping table, falling back to `VALIDATION_FAILED`.
  - **Error type**: `admission.Error` renders as a gqlerror with the four-key envelope (R7). Admission denials use `code: ADMISSION_REJECTED` plus `phase`, and `commit` when post-receive.
  - **Message text**: `FormatRejection(diags)` produces `file: message` entries joined by `"; "`, byte-identical to Rust `validation_handler.rs:192-203`. A shared golden fixture is asserted from both Go and Rust tests.
  - **Warnings**: a per-request `admissionreport.Collector` stored in context gathers `{path, commit, diagnostics}` for successful mutations. An `AroundResponses` extension registered in `internal/app/server.go:387-398` writes `response.Extensions["admission"]` when the collector is non-empty, merging with any existing keys.
- **Rationale**: Design 038 (cost reporting) is not implemented yet: nothing writes top-level `extensions` today. A collector plus one response interceptor is the same mechanism 038 will need, and merging keys keeps the two independent. `graphql.RegisterExtension` replaces the whole key on each call, so it would drop entries when a document runs more than one mutation.
- **Alternatives considered**: A typed payload `userErrors`. Rejected by the user; no other mutation uses errors-as-data.

## R7. Kind-neutral error envelope and Namespace migration

- **Decision**: All Git-backed mutation errors use one envelope: `code` always; `diagnostics[]` when there is detail; `phase` only for `ADMISSION_REJECTED`; `commit` only for `POST_RECEIVE`. Each diagnostic is `{reason, message, level, file?, field?}`, and the code is chosen by cause (see `contracts/admission-diagnostics.md`).
  - A shared `admission.Error{Code, Phase, CommitSHA, Diagnostics}` in `internal/admission`, rendered by one `ToGQLError`, replaces the per-kind helpers.
  - `resolver/namespace_error.go` is rewritten on top of it:
    - `NAMESPACE_STRUCTURAL_VALIDATION_FAILED`, `NAMESPACE_IMMUTABLE_FIELD` and `NAMESPACE_POLICY_REJECTED` map by reason to `ADMISSION_REJECTED`, `FAILED_PRECONDITION`, `ALREADY_EXISTS` or `NOT_FOUND`.
    - `NAMESPACE_CONFLICT` becomes `CONFLICT`.
    - `NAMESPACE_DELETION_BLOCKED` becomes `FAILED_PRECONDITION`, with one diagnostic per ordered blocker.
    - Reason values are unchanged.
  - `namespace.Phase`/`Code*` (`decision.go:11-42`) are removed.
  - The `phase` label on `gitstore_namespace_validation_*` metrics becomes `code`.
  - `recordNamespaceGraphQLError` (`service.go:1103`) reads `code` and the first diagnostic's `reason`.
  - Consumers updated: the runbook's "Stable response codes", `gitstore-admin`, and resolver/security tests.
  - Status writes and deletion completion (`statusConflictError`, `resolver/status_generic.go:19`) fold into the same envelope. `RESOURCE_VERSION_CONFLICT` and its `resourceVersion` key become `code: CONFLICT` with `diagnostics[{reason: RESOURCE_VERSION_CONFLICT, message: "… current resourceVersion <rv>"}]`.
    - Controllers use the extension only in error text: they wrap `types.ErrConflict`, re-read and retry. So the change is to match `CONFLICT` in the four controller clients (`status/graphql_resource_status_client.go`, `graphql_product_status_client.go`, `graphql_namespace_status_client.go`, `repository/graphql_client.go`) and in `namespace/graphql_client.go`.
    - Rolling upgrade: the standalone controller fix (`fix/controller-namespace-deletion-completion`) already teaches controllers to accept both codes. Ship it before the API stops emitting `RESOURCE_VERSION_CONFLICT`, so old and new controllers both work during the overlap.
- **Rationale**:
  - The user rejected a seven-key surface (`code`, `check`, `reason`, `reasons`, `phase`, `commit`, `diagnostics`) as unclear about which key applies where.
  - `check` is redundant: the code and reason already encode it.
  - Putting `reason`/`reasons` inside diagnostics gives one place for machine-readable detail.
  - Collapsing the codes gives clients a single vocabulary across kinds.
- **Prerequisite**: a separate PR, merged before slice 1:
  - removes `features.namespace_repository_fence` (the fence is always on);
  - removes `NAMESPACE_REPOSITORY_FENCE_DISABLED`;
  - removes the unused `TransferRepository` datastore method and its tests;
  - updates the runbook and the compose/config files (with a follow-up for the quickstart repository).

  This feature's mapping therefore never handles the fence code.
- **Alternatives considered**:
  - A separate `hook` key.
  - Keeping `NAMESPACE_*` codes with the new envelope.
  - Keeping `check`.

  The user rejected all three.

## R8. Ancestor index storage (Scylla)

- **Decision**: New migration `010_category_ancestor_index.cql`:

  ```cql
  CREATE TABLE category_ancestor_index (
    namespace text, ancestor text, depth tinyint, descendant text, descendant_uid uuid,
    resource_version text,
    PRIMARY KEY ((namespace, ancestor), depth, descendant)
  ) WITH CLUSTERING ORDER BY (depth ASC, descendant ASC);
  ```

  There is no CDC, matching the other projections (`catalog_cdc.go:32-43`).
  - Each category with `status.resolved.path = [a0…ak]` owns k+1 rows `(ns, ai, k−i, self)`. The depth-0 self row serves `includeSelf`.
  - **Key choice**: rows are keyed by name, not UID. The filter takes a name, and the children-block invariant guarantees a deleted category's partition is empty apart from its own self row, which is removed on final removal. A same-name re-create therefore starts clean.
- **Rationale**: A subtree query is a single-partition, natively ordered range. `maxDepth` is a clustering range `depth <= ?`. The size of a partition is the size of that subtree. With the assumed envelope of up to 100,000 categories per namespace and about 100 bytes per row, even a root partition stays around 10 MB, below Scylla's large-partition warnings.
- **Alternatives considered**:
  - `categories_by_path` with path as the clustering key. Rejected in ADR-0006: one partition per namespace, and `maxDepth` would need in-memory filtering.
  - Keying by `ancestor_uid`. Rejected: an O(depth) name→UID lookup on every write, with no correctness gain given the invariant above.

## R9. Index maintenance and consistency

- **Decision**:
  - `UpdateCategoryTaxonomyStatus` maintains the index in the datastore layer. This covers both call sites: `category.resolvers.go:79` and generic `status_generic.go:52`.
  - **Scylla** (`backend.go:972-1005`), when the patch carries `Resolved`:
    1. Decode the old `status.resolved.path`.
    2. CAS the authoritative row (unchanged behaviour).
    3. Through `mutationExecutor.executeUpdate` (`recovery.go:69-130`), upsert all current rows (idempotent, O(depth)) and delete rows for old (ancestor, depth) pairs no longer present.
    4. If projection writes still fail after retries, return the existing `RepairRequiredError`.
  - **memdb**: rows are rewritten inside the same write transaction, under `categoryMutationMu`, like `syncOwnerReferenceProjections`.
  - **Final removal**: `CompleteCategoryTaxonomyDeletion` deletes the category's own rows, through `executeDelete` (projections first).
  - **Admission**: admission writes no rows (FR-027).
- **Rationale**:
  - Upserting on every resolved write makes the index self-healing. The controller sends the full `resolved` block whenever any status field changes (`reconciler.go:218-220`), so a dropped write is repaired by the next status change, without depending on a path diff.
  - The cost is at most 129 small upserts per status write; real taxonomies are typically depth 6 or less.
  - Deletes only happen when the path changed.
  - This reuses the existing roll-forward-or-repair contract for projections.
- **Alternatives considered**:
  - Writing only on a path diff. Rejected: one failed write would never self-heal, because `IsNoOp` suppresses identical status writes.
  - Logged batches across partitions. Rejected: unused anywhere in the codebase, with a coordinator cost of up to 129 partitions.

## R10. Backfill and repair

- **Decision**: Extend `gitctl scylla-projection-audit` / `scylla-projection-repair --dry-run|--confirm` (`cmd/gitctl/main.go:85-93,277-340`; `scylla/repair.go`) with an `ancestor_index` projection.
  - `Snapshot` additionally reads `namespace, name, uid, status` from `category_taxonomies_by_namespace` and scans `category_ancestor_index`.
  - `expectedProjections` derives the rows from `status.resolved.path`.
  - `BuildRepairPlan` emits insert, delete and update actions using conditional writes.
  - Paging and a rate limit come from the existing repair flags.
  - The release notes document running audit and then repair after the rollout.
- **Rationale**:
  - This one mechanism provides both the upgrade backfill (FR-029) and divergence repair (FR-028).
  - Commit a97363a removed the standalone backfill binary ("keep data-transfer tooling outside the repository"); this is projection repair, which stays in-repo.
  - The controller's resync cannot backfill, because `IsNoOp` suppresses unchanged status writes.
  - memdb needs no backfill: it is in-process and rebuilt on start.
- **Alternatives considered**:
  - A controller-side forced status rewrite on upgrade. Rejected: it couples the controller to a datastore projection and floods writes.
  - Lazy index-on-read. Rejected: unbounded work on the read path.

## R11. Filtered list, cursors, children and parent

- **Decision**:
  - **Filtered list**: `categories(namespace, filter, first, after, last, before)`. With `filter` set, the resolver runs these steps:
    1. Validate `maxDepth ∈ [1,128]`.
    2. Call the new `CategoryAncestorIndex.ListCategoryDescendants(ctx, ns, ancestor, includeSelf, maxDepth, page)`. It returns `(depth, name, uid)` rows using a new cursor `closure|<depth>|<name>`.
    3. Fetch records by UID with bounded concurrency, up to the page size.
    4. Drop rows whose record is missing or whose UID differs, which happens transiently during a cascade.
  - **Cursors**: a filtered cursor is rejected on an unfiltered query and vice versa, with `BAD_USER_INPUT`, because `parsePageCursor` would otherwise silently ignore it.
  - **children**: `Category.children` is `ListCategoryDescendants(self, maxDepth=1)`, capped at `DefaultPageSize` (100), with no pagination arguments.
  - **parent**: `Category.parent` looks up `spec.parentRef.name` when the `ParentResolved` condition is True, otherwise `null`.
  - `gqlgen.yml` marks `parent` and `children` as `resolver: true`.
- **Rationale**:
  - Every read is bounded by page size.
  - There are no namespace scans and no live aggregates (ADR 0017).
  - Fetching by UID reuses `GetCategoryTaxonomy`.
- **Alternatives considered**:
  - Denormalising full records into index rows. Rejected: write amplification across depth.
  - A paginated `children` connection. Deferred, because the typed `filter` is interim and the planned `query` syntax supersedes it.

## R12. Removing `Category.path`/`depth` from the GraphQL code

- **Decision**: Delete `converters.go:432-438,546-547` and regenerate (`go generate ./...` in `gitstore-api`). `status.resolved` stays `null` until the first reconcile (`converters.go:677-678` are kept). The controller selects only `status.resolved { depth path … }` (`gitstore-controller-manager/internal/listwatch/graphql_listwatcher.go:32`), so it is unaffected. Other consumers (`gitstore-admin`, docs, integration tests) are grepped and updated.
- **Rationale**: The user removed the fields, with no fallback.

## R13. Authorization

- **Decision**: In `internal/middleware/security/graphql.go`:
  - `createCategory` authorizes `categoryTaxonomy.create` from input `metadata.{namespace,name}`.
  - `updateCategory` and `deleteCategory` use a stored-resource check modelled on `deleteCategory` (`:527-550`): `categoryTaxonomy.update` and `categoryTaxonomy.delete` with `ResourceContext{Kind:"categoryTaxonomy", Name, OwnerSub, Attrs{namespace, repositoryID}}`.
  - All three mutation names are added to `graphqlFieldRequiresAuthorization` (`:763`).
  - `category.delete` is replaced.
  - `config/policy.yaml` and `policy.yaml.example` grant the new actions wherever `category.delete` was granted. This is a content change, not a format change, but it is still flagged for the quickstart repository.
  - `updateCategoryStatus` keeps `category.status.write`; spec 064 renames it.
- **Rationale**: These are the ADR-0010 actions, enforced before any side effect.

## R14. Capacity and fault evidence

- **Decision**:
  - **Capacity**: add `TARGET=category PROFILE=hierarchy` (`tests/capacity/profiles/category-hierarchy.js`, `preflight/category-hierarchy.sh`, `verifiers/category-hierarchy.sh`) and a case branch in `scripts/run-capacity-target.sh:35-70`. The scenario runs on two API replicas and two controller replicas:
    - build a 10,000-descendant subtree;
    - sustain `createCategory`/`updateCategory` concurrently with Git pushes to the same `gitstore-system` repository;
    - re-parent the subtree root.
  - **Measured**: mutation p95/p99, filtered-list p95 during the cascade, and cascade convergence time.
  - **Verifier**: the filtered results equal a walk over `status.resolved.path` for every category (SC-009). Every successful mutation's record carries its own commit SHA (SC-007).
  - **Fault**: reuse `tests/chaos/profiles/controller-restart.json` and `api-restart.json`, declared by the profile, with these assertions:
    - the cascade resumes after a controller restart;
    - an index write interrupted by an API restart self-heals on the next status write or is reported by audit.
  - CLAUDE.md and AGENTS.md list the new pair.
- **Rationale**: The constitution requires repeatable `make capacity`/`make chaos` evidence for load-bearing paths. This uses the existing target/profile selector family rather than a new public target.
