# Implementation Plan: Admission Path Cleanup — `changed_paths` Population and Legacy Fallback Removal

**Branch**: `034-admission-path-cleanup` | **Date**: 2026-06-27 | **Spec**: [spec.md](spec.md)
**Input**: Feature specification from `/specs/034-admission-path-cleanup/spec.md`

## Summary

Populate `AdmitResourcesRequest.changed_paths` in the Rust admission handler by diffing
the old and new commit trees using gix (Phase 1), then remove the legacy
`OldCommitSha == ""` fallback branch in the Go API and eliminate the resulting double DB
lookup and double `operationForEntry` per category (Phase 2). Phase 2 must not be merged
before Phase 1 is deployed and verified.

## Technical Context

**Language/Version**: Rust 1.x (`gitstore-git-service`) + Go 1.25 (`gitstore-api`)
**Primary Dependencies**:
- Rust: `gix 0.84.0` (already in Cargo.toml, `max-performance-safe` + `tree-editor` features); no new deps
- Go: no new deps
**New Dependencies**: None
**Storage**: No datastore changes
**Testing**: `cargo test --verbose` (Rust unit), `go test ./internal/cataloggrpc/...` (Go unit)
**Target Platform**: Linux server (CI: ubuntu-latest)
**Project Type**: gRPC backend service (Rust) + gRPC server handler (Go)
**Performance Goals**: Admission reads scale with push size, not repository size; single-file push reads 1 file, not N
**Constraints**: Phase 2 tasks must not be merged before Phase 1 is deployed; no proto changes; no breaking changes to gRPC contract; admission remains fire-and-forget; license headers required on any new files

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle | Status | Notes |
|-----------|--------|-------|
| I. Test-First | PASS | Rust T020* tests written before implementation; Go server_test.go updated before removing legacy branch |
| II. API-First | PASS | `contracts/admission-changed-paths.md` defines the sender/receiver contract before any code changes |
| III. Clear Contracts | PASS | `changed_paths` field already in proto; no schema evolution; contract doc covers ordering constraint |
| IV. Observability | PASS | Rust: `error!` on gix failure; Go: fallback warning log disappears (its absence is the observable signal Phase 2 is safe) |
| V. User Story Driven | PASS | US1 (changed_paths), US2 (legacy removal), US3 (single DB read) from spec.md |
| VI. Incremental Delivery | PASS | Phase 1 and Phase 2 are independently deployable; Phase 1 delivers value immediately; Phase 2 is a cleanup |
| VII. Simplicity/YAGNI | PASS | Reuse existing `collect_changed_blobs_from_trees` / `collect_blobs_from_commit` patterns; no new abstractions |

**Complexity Justification**: None — no violations.

## Project Structure

### Documentation (this feature)

```text
specs/034-admission-path-cleanup/
├── plan.md                              # This file
├── spec.md                              # Feature specification
├── research.md                          # Phase 0 — all decisions resolved
├── data-model.md                        # Phase 1 — changed types and invariants
├── quickstart.md                        # Phase 1 — local dev verification
├── contracts/
│   └── admission-changed-paths.md      # Phase 1 — sender/receiver contract
└── tasks.md                             # Phase 2 output (/speckit.tasks command)
```

### Source Code

```text
gitstore-git-service/
└── src/
    └── git/
        └── hooks/
            ├── mod.rs                  # MODIFIED — AdmissionHandler trait: add git_dir: &Path param
            │                           #           NoopAdmissionHandler: ignore git_dir
            │                           #           HookPipeline::run_post_receive: pass git_dir
            │                           #           HookPipeline::run_schema_validation: pass git_dir (blocking path)
            └── admission_handler.rs    # MODIFIED — compute_changed_paths() helper (private)
                                        #           AdmissionControlHandler::admit: call compute_changed_paths
                                        #           AdmitResourcesRequest.changed_paths: populated

gitstore-api/
└── internal/
    └── cataloggrpc/
        ├── server.go                   # MODIFIED — remove OldCommitSha=="" legacy branch (lines 380–388)
        │                               #           operationForEntry: return (Operation, any, bool)
        │                               #           admitParsedEntries: catPushSet built from explicitOps directly
        │                               #           admitProduct/Collection/ProductVariant/CategoryTaxonomy:
        │                               #             accept existing object from operationForEntry, remove 2nd GetXByName
        └── server_test.go              # MODIFIED — remove legacy path tests; add changed_paths fast-path tests
```

## Phase 0 Research Summary

Research complete. See [research.md](research.md) for all 10 decisions.

Key resolved decisions:
1. Reuse existing `collect_changed_blobs_from_trees` / `collect_blobs_from_commit` patterns — path-only variant, no blob content read
2. Extend `AdmissionHandler::admit` trait with `git_dir: &Path`; pass through from `run_post_receive`
3. All-zeros `old_oid` → full `new_oid` tree paths (new branch)
4. All-zeros `new_oid` → full `old_oid` tree paths (branch deletion)
5. gix error → fallback to `vec![]`, log `error!`, do not reject push
6. Go: delete lines 380–388 (`OldCommitSha == ""` branch) entirely
7. `operationForEntry` returns existing object to eliminate second `GetXByName`
8. `catPushSet` construction uses `explicitOps` map directly (no `operationForEntry` call)
9. No proto changes — `changed_paths` field 5 already exists
10. Phase 2 deployment gated on absence of fallback warning log in production

## Phase 1 Design Summary

### Modified Rust signatures

| Component | Change |
|-----------|--------|
| `AdmissionHandler::admit` trait | Add `git_dir: &Path` parameter (4th arg) |
| `NoopAdmissionHandler::admit` | Accept and ignore `git_dir` |
| `AdmissionControlHandler::admit` | Open repo, call `compute_changed_paths`, populate field |
| `HookPipeline::run_post_receive` | Pass `git_dir` to `handler.admit(...)` |
| `HookPipeline::run_schema_validation` | Pass `git_dir` to `self.admission_handler.admit(...)` (blocking path) |

New private helper in `admission_handler.rs`:
```rust
fn compute_changed_paths(git_dir: &Path, old_oid: &str, new_oid: &str) -> Vec<String>
```

### Modified Go types

| Component | Change |
|-----------|--------|
| `operationForEntry` | Returns `(admission.Operation, any, bool)` — third return is the looked-up existing object |
| `admitProduct` | Receives `existing any` from caller; removes internal `GetProductByName` call |
| `admitCollection` | Receives `existing any` from caller; removes internal `GetCollectionByName` call |
| `admitProductVariant` | Receives `existing any` from caller; removes internal `GetProductVariantByName` call |
| `admitCategoryTaxonomyWithContext` | Receives `existing any` from caller; removes internal `GetCategoryTaxonomyByName` call |
| `admitParsedEntries` | `catPushSet` loop uses `explicitOps[key]` directly, not `operationForEntry` |

### No GraphQL schema changes

The public GraphQL schema is untouched. This is a pure internal plumbing change.

## Complexity Tracking

No constitution violations.
