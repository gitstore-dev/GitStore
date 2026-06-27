# Feature Specification: Admission Path Cleanup — changed_paths Population and Legacy Fallback Removal

**Feature Branch**: `034-admission-path-cleanup`
**Created**: 2026-06-27
**Status**: Closed

## Overview

Every push to a GitStore repository triggers a catalog admission call that determines which
resources changed and how to update the datastore. Today the git service always sends an
empty `changed_paths` list, forcing the API to re-read every tracked file on every push
regardless of how many files actually changed. The API also maintains a legacy fallback
path for the case where no previous commit SHA is provided — a safety net that was never
meant to be permanent and carries duplicated database work.

This spec closes both gaps in one coordinated change: the git service computes and sends
the actual set of changed paths, and the API drops the fallback branch that is now
unreachable. The result is that admission cost scales with the size of the push rather than
the size of the repository.

## User Scenarios & Testing *(mandatory)*

### User Story 1 — Admission cost scales with push size, not repository size (Priority: P1)

When a developer pushes a single file change to a large repository, the catalog admission
process examines only that file rather than reading every tracked resource in the repository.
Operators observing the system see admission duration and datastore read counts proportional
to the number of changed files in the push.

**Why this priority**: This is the primary correctness and performance payoff. It directly
eliminates the most expensive redundant work on every push and activates the fast-path
already present in the admission logic.

**Independent Test**: Push a single-file change to a repository that contains 50+ tracked
resources. Observe (via logs or metrics) that the admission operation reads only the changed
file rather than all files. The push completes successfully and only the changed resource
reflects the update in the catalog.

**Acceptance Scenarios**:

1. **Given** a repository with many tracked resources, **When** a push modifies one file, **Then** admission reads only that file (and its previous version) rather than the entire repository tree.
2. **Given** a push that creates a new branch from a non-zero base commit, **When** admission is triggered, **Then** only the files that differ between the base commit and the tip commit are read.
3. **Given** a push that creates a brand-new branch with no prior history (first push), **When** admission is triggered, **Then** all files in the new commit are admitted as new resources.
4. **Given** a push that deletes a branch, **When** admission is triggered, **Then** admission is triggered for deletion with the full previous commit's files as the changed set.

---

### User Story 2 — Legacy fallback path is removed; all admissions follow the diff-aware path (Priority: P2)

All admission calls from the git service include a non-empty previous commit SHA, making
the diff-aware path the only code path through admission. The legacy full-tree snapshot
fallback is no longer present in the codebase. Operators no longer see the warning log
`"admit_resources: old commit absent; falling back to full-tree snapshot admission"`.

**Why this priority**: Depends on US1 — the fallback can only be safely removed after the
git service guarantees a previous SHA on every call. Removing it eliminates dead code and
the double database lookup bugs it carried.

**Independent Test**: Instrument or inspect the API codebase after this phase and confirm
the `OldCommitSha == ""` branch is absent. Trigger a push to a new branch and confirm no
fallback warning appears in API logs. Verify that a push to an existing branch also
produces no fallback log.

**Acceptance Scenarios**:

1. **Given** a push to a brand-new branch (first commit on that branch), **When** the admission call reaches the API, **Then** the API processes it via the diff-aware path (the git service provides all-zeros as the previous SHA, which the API maps to an empty old tree) with no fallback warning in logs.
2. **Given** a push to an existing branch, **When** the admission call reaches the API, **Then** the API processes it via the diff-aware path using the provided previous and new commit SHAs.
3. **Given** the legacy fallback code is removed, **When** the full test suite runs, **Then** all existing admission integration tests pass.

---

### User Story 3 — Per-resource database reads eliminated from admission (Priority: P3)

Within the diff-aware admission path, each resource is looked up in the datastore exactly
once per admission cycle. The previous double-lookup pattern (one lookup to determine the
operation, a second lookup inside the admit function) is eliminated. Category resources are
also processed once rather than twice per entry.

**Why this priority**: This is a correctness and efficiency clean-up that becomes possible
only after the legacy branch is removed. The double-lookup also introduced a time-of-check
to time-of-use window that is closed by this change.

**Independent Test**: Instrument the datastore layer (or review code) and confirm that for
a push touching N resources of any type, the total number of `Get*ByName` calls equals N,
not 2N. For a push touching M category entries, `operationForEntry` is called M times
total, not 2M.

**Acceptance Scenarios**:

1. **Given** a push that creates or updates a product resource, **When** admission runs, **Then** the datastore is queried for that resource exactly once to determine the operation and supply the previous state.
2. **Given** a push that creates or updates category taxonomy entries, **When** admission runs, **Then** `operationForEntry` is called once per category entry, not twice.

---

### Edge Cases

- What happens when `old_oid` is all-zeros (new branch)? → The git service treats this as "no prior tree"; the full file list of the new commit is emitted as `changed_paths` rather than diffing against a non-existent parent.
- What happens when `new_oid` is all-zeros (branch deletion)? → No files exist in the new commit; `changed_paths` contains all files from the previous commit, allowing the API to admit all resources as deletions.
- What happens when both `old_oid` and `new_oid` are non-zero but identical (no-op push)? → The diff produces an empty set; `changed_paths` is empty and no admission work is done.
- What happens when the diff computation in the git service fails (corrupt object, IO error)? → Admission is skipped for that update and an error is logged; the push itself is not rejected (admission is fire-and-forget post-receive).
- What happens when a push touches a very large number of files? → All changed paths are included in `changed_paths`; no truncation occurs. Admission cost scales linearly with the change set.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The git service MUST compute the set of repository-relative file paths that changed between `old_oid` and `new_oid` and send them as `changed_paths` on every `AdmitResourcesRequest`; the field MUST NOT be empty unless the diff is genuinely empty.
- **FR-002**: When `old_oid` is all-zeros (new branch), the git service MUST treat the entire file tree of `new_oid` as `changed_paths` (no prior tree to diff against).
- **FR-003**: When `new_oid` is all-zeros (branch deletion), the git service MUST treat the entire file tree of `old_oid` as `changed_paths`.
- **FR-004**: The API MUST process all `AdmitResourcesRequest` calls through the diff-aware code path; the legacy full-tree snapshot fallback path (`OldCommitSha == ""`) MUST be removed.
- **FR-005**: Each resource type's admit function MUST read the resource from the datastore at most once per admission cycle; the second unconditional `Get*ByName` call present in the legacy path MUST be eliminated.
- **FR-006**: `operationForEntry` for `CategoryTaxonomy` resources MUST be called exactly once per category entry per admission cycle.
- **FR-007**: All existing admission behaviour for creates, updates, and deletes MUST be preserved after the cleanup; no resource type loses admission coverage.
- **FR-008**: The diff computation failure in the git service MUST NOT cause the push to be rejected; errors MUST be logged and admission skipped for the affected update only.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A push that modifies one file in a repository with 50 tracked resources produces exactly as many datastore reads as a push touching all 50 files divided by 50 — admission cost is linear in push size, not repository size.
- **SC-002**: The warning log `"admit_resources: old commit absent; falling back to full-tree snapshot admission"` never appears in API logs after this feature ships.
- **SC-003**: For any push touching N resources, the total number of datastore read operations attributable to admission is N (not 2N); verified by test or log instrumentation.
- **SC-004**: All existing admission integration tests pass without modification.
- **SC-005**: A push to a brand-new branch (first commit) is admitted correctly — all files in the commit are processed and appear in the catalog.
- **SC-006**: A branch deletion push is admitted correctly — all resources from the deleted branch tip are removed from the catalog.

## Assumptions

- The git service's gRPC proto field `changed_paths` (field 5 of `AdmitResourcesRequest`) already exists and is accepted by the API; no proto changes are required.
- The API's `loadParsedEntries` fast-path (skip files not in `changed_paths` when the field is non-empty) is already implemented and correct; this spec activates it, not implements it.
- Admission is fire-and-forget post-receive: a failure in admission does not reject the push. This assumption is unchanged by this spec.
- The all-zeros OID convention for new/deleted branches is the standard git wire protocol behaviour and is already handled in other parts of the admission handler.

## Dependencies

- **Requires**: Phase 1 (034 Rust changed_paths) MUST be merged and deployed before Phase 2 (034 Go legacy removal) is merged. The Go side can be developed in parallel but must not be deployed first.
- **Unblocks**: Future `changed_paths`-dependent optimisations (e.g., skipping gRPC `ListFiles` entirely when `changed_paths` already covers the full change set).
- **Related deferred work**: `CategoryTaxonomy` `AncestorPath` staleness on parent delete/move is a separate concern tracked for the CategoryTaxonomy controller spec; it is not addressed here.
