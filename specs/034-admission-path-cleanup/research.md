# Research: Admission Path Cleanup

**Branch**: `034-admission-path-cleanup` | **Date**: 2026-06-27

## Decision 1 — How to compute `changed_paths` in the Rust admission handler

**Decision**: Reuse the tree-diff helpers already in `mod.rs` (`collect_changed_blobs_from_trees`, `collect_blobs_from_commit`) by extracting the path-only variant of the same logic into the `AdmissionControlHandler`.

**Rationale**: `mod.rs` already implements a fully working gix-based tree diff for schema validation blob extraction (see `collect_changed_blobs` and `collect_blobs_from_commit`). The admission handler needs the same diff but only the file paths, not the blob content. Duplicating the tree traversal into a path-only helper avoids reading blob data twice while keeping the logic co-located with the handler.

**Alternatives considered**:
- Use `gix`'s high-level `Repository::diff_tree_to_tree` API: requires the `diff` feature flag to be enabled in `Cargo.toml`; the existing code uses manual tree decoding which is already proven correct. Deferred for now.
- Pass the pre-computed blob list from `run_post_receive` into the admission handler: `run_post_receive` doesn't have blob data — that's only computed in `run_schema_validation` which is a separate phase. Not viable without significant refactor.

## Decision 2 — Where to open the gix repository for path computation

**Decision**: Pass `git_dir: &Path` into `AdmissionControlHandler::admit`. The handler opens the repository via `gix::open(git_dir)` per call, matching the existing pattern in `extract_resource_blobs` and all gRPC server methods (`gix::open(&repo_path)`).

**Rationale**: `AdmissionHandler::admit` already receives `repository_id: &str` but not the filesystem path. The `git_dir` parameter is passed to `run_post_receive` and is the resolved bare repository path. Extending the trait signature to include `git_dir` is the minimal change; the alternative of storing the data root on the handler and resolving it internally was rejected because the handler should not know the storage layout.

**How this propagates**:
1. `AdmissionHandler::admit` trait gains `git_dir: &Path` parameter.
2. `HookPipeline::run_post_receive` already receives `git_dir` and passes it through.
3. `AdmissionControlHandler::admit` opens the repo and computes `changed_paths`.
4. `NoopAdmissionHandler::admit` ignores `git_dir` (no-op).

## Decision 3 — All-zeros `old_oid` handling (new branch)

**Decision**: When `old_oid` is all-zeros, emit all file paths from the `new_oid` tree as `changed_paths` (no diff, full tree scan). This mirrors the existing `extract_resource_blobs` logic at `mod.rs:520–522`.

**Rationale**: All-zeros is the standard git wire protocol signal for "this ref did not exist before". There is no prior tree to diff against, so all files in the new commit are by definition new. The Go side handles this correctly: `loadParsedEntries` returns `nil` for all-zeros or empty refs (line 399), so the admission derives a full create operation.

## Decision 4 — All-zeros `new_oid` handling (branch deletion)

**Decision**: When `new_oid` is all-zeros (branch deletion), emit all file paths from the `old_oid` tree as `changed_paths`. This already matches how the Go side treats deletions: `isZeroOID(newCommit)` causes `newEntries` to be `nil`, and `deriveResourceAdmissionOperations` maps all old entries without a new counterpart to `OperationDelete`.

**Rationale**: The admission handler already fires for branch deletions (test T019f). The only change is populating `changed_paths` with the old tree's files so the Go fast-path activates.

## Decision 5 — Failure mode for gix repo open / diff failure

**Decision**: On any gix error (repo open failure, object lookup failure), log at `error!` level and fall back to `changed_paths: vec![]`. The push is not rejected (admission is fire-and-forget). The Go side will fall back to full-tree scan as it does today.

**Rationale**: Admission is fire-and-forget post-receive — errors must not affect push outcome. A degraded fallback to `vec![]` is already safe (Go handles it). The error log ensures operators can diagnose ODB issues.

## Decision 6 — Go legacy path removal: what changes in `server.go`

**Decision**: Delete the `if req.GetOldCommitSha() == ""` branch (lines 380–388) entirely. The main path (`oldEntries`/`newEntries` diff) becomes the only path.

**Rationale**: With Phase 1 shipped, `old_commit_sha` is always non-empty (either a real SHA or all-zeros for new branches). The all-zeros case is handled correctly by `loadParsedEntries` (returns `nil` for zero OIDs, line 399), producing a full create operation — which is the correct behaviour for new branch first push.

## Decision 7 — Double DB lookup fix: how to eliminate the second `Get*ByName` call

**Decision**: The second `store.GetXByName` call inside `admitProduct`, `admitCollection`, `admitProductVariant`, and `admitCategoryTaxonomyWithContext` exists to support the legacy path where `explicitOps` is `nil`. When the legacy path is removed, `explicitOps` is always populated by `applyResourceOperations`, so `operationForEntry` always returns from the map without a DB read. The admit functions then receive a pre-determined `op` and the `existing` object should come from `operationForEntry`'s lookup result — not a second call.

**Approach**: Refactor `operationForEntry` to return both the operation and the looked-up existing object, so each admit function receives it without re-fetching. This eliminates the second DB call and closes the TOCTOU window.

## Decision 8 — Double `operationForEntry` for CategoryTaxonomy

**Decision**: The first call happens in `admitParsedEntries` when building `catPushSet` (lines 524–537). The second happens in the topo-order loop (line 545). With the legacy path removed, `explicitOps` is always populated, so the first `operationForEntry` call hits the map (no DB). However, for correctness and clarity, consolidate: build `catPushSet` from `explicitOps` directly (no `operationForEntry` needed there), leaving only one call in the topo-order loop.

**Alternatives considered**: Leave both calls — since the first hits the map after Phase 1, there's no DB cost, but it's confusing code. The cleanup removes the redundancy.

## Decision 9 — Test approach

**Phase 1 (Rust)**: Add unit tests to `admission_handler.rs` that spin up a real in-process gix bare repo (matching the existing `make_repo_with_files` pattern in `mod.rs` tests), push a single-file update, and assert `changed_paths` is populated correctly. Cover: new branch (all-zeros old), deletion (all-zeros new), regular update, gix open failure fallback.

**Phase 2 (Go)**: Extend `server_test.go` / `admission_operations_test.go`. The legacy path test (if any) must be removed. Add a test that sends an `AdmitResourcesRequest` with non-empty `old_commit_sha` and non-empty `changed_paths` and verifies only the changed resource is read. Verify no call is made to `GetXByName` more than once per resource.

## Decision 10 — No proto changes required

The `changed_paths` field (field 5 of `AdmitResourcesRequest`) already exists in `shared/proto/gitstore/catalog/v1/catalog_service.proto`. No schema evolution is needed.
