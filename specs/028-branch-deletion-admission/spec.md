# Feature Specification: Branch Deletion Admission

**Feature Branch**: `028-branch-deletion-admission`  
**Created**: 2026-06-18  
**Status**: Closed  
**Input**: When a git branch is deleted the git service currently returns HTTP 503 and the
post-receive admission path is never reached. The API-side branch deletion logic (zero-OID
path in `AdmitResources`) is already implemented but unreachable because the Rust hook
silently rejects branch-delete ref updates before forwarding them.

## Overview

GitStore's admission pipeline handles four ref-update operations: create, fast-forward
update, force-update, and delete. The first three are exercised end-to-end. Branch deletion
(zero new-OID) is recognised by the Go API (`isZeroOID`) and will delete all catalog
resources admitted on that branch — but it is never triggered because:

1. The git service's `receive-pack` handler returns 503 for ref-delete operations instead
   of forwarding them as a zero-OID `AdmitResourcesRequest`.
2. There is no integration-test coverage of the end-to-end path.

This spec defines what must change in the Rust git service and how the integration test
`TestAdmission_BranchDeletion` (already written, currently always-skipped) should pass
as a result.

---

## User Scenarios & Testing *(mandatory)*

### User Story 1 — Deleting a branch removes its catalog resources (Priority: P1)

A catalog author creates a feature branch, pushes catalog resources to it, and then deletes
the branch. The author expects those resources to disappear from the datastore. Resources on
other branches must not be affected.

**Why this priority**: This is the core correctness guarantee of branch lifecycle management.
Without it, deleted branches leave orphaned catalog data that can never be cleaned up through
normal git workflows. All other stories depend on the delete path working end-to-end.

**Independent Test**: `TestAdmission_BranchDeletion` in
`tests/integration/admission_operations_test.go` — currently skipped because the git service
returns 503 on branch delete. After this spec the test must pass.

**Acceptance Scenarios**:

1. **Given** a feature branch with an admitted `Product`, **When** the branch is deleted via
   `git push origin --delete <branch>`, **Then** `AdmitResources` is called with
   `new_commit_sha = "0000000000000000000000000000000000000000"` and the product is removed
   from the datastore.
2. **Given** `main` and a feature branch each with distinct admitted resources, **When** the
   feature branch is deleted, **Then** only the feature-branch resources are removed; the
   `main` resources are untouched.
3. **Given** a branch-delete push followed immediately by a branch re-create push to the same
   ref, **When** both `AdmitResources` calls arrive, **Then** the API's staleness guard
   prevents the delete from removing the re-created resources (already handled in the Go
   layer; the integration test confirms it end-to-end).

---

### User Story 2 — Branch pattern filtering applies to deletes (Priority: P2)

A branch delete on a ref that does not match `GITSTORE_ADMISSION_CONTROL__BRANCH_PATTERN`
must not trigger admission, just like a push to the same ref would be ignored.

**Why this priority**: Pattern filtering is the admission gate that prevents noise from
non-product branches (e.g., `dependabot/*`, `renovate/*`). It must apply symmetrically to
deletions so that operators do not get unexpected datastore side-effects from cleanup of
unrelated branches.

**Independent Test**: Push a product to a matching branch, then delete a non-matching branch
and verify no admission call is made; delete the matching branch and verify resources are
removed.

**Acceptance Scenarios**:

1. **Given** a branch `experiment/foo` that does not match the configured branch pattern,
   **When** it is deleted, **Then** no `AdmitResources` call is made.
2. **Given** a branch `main` that matches the configured branch pattern, **When** it is
   deleted, **Then** `AdmitResources` is called with a zero new-OID.

---

### Edge Cases

- What happens when the deleted branch had no admitted resources? The zero-OID admission call
  is still forwarded; the Go API resolves it as a no-op (no entries to delete) and returns
  success without error.
- What happens when the branch being deleted does not exist on the server? The git client
  reports an error before the server processes the ref update; no admission call is made.
- What happens when the `AdmitResources` API call fails or is unreachable during branch
  deletion? Admission is fire-and-forget; the error is logged but the git client receives a
  success response and the ref is deleted locally. Resources may remain in the datastore
  until a subsequent admission event reconciles them.
- What happens when a branch is deleted and immediately recreated before the admission call
  completes? The API's staleness guard (already implemented) detects that the branch now
  exists again and does not remove the re-created resources.

---

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The git service MUST forward branch-delete ref updates (zero new-OID) that
  match the configured branch pattern to the API as an `AdmitResources` request with
  `new_commit_sha` set to `"0000000000000000000000000000000000000000"`.
- **FR-002**: The git service MUST NOT return an error (503 or any non-success status) to
  the git client for branch-delete operations on branches that match the configured pattern.
- **FR-003**: Branch deletion forwarding MUST use the same
  `GITSTORE_ADMISSION_CONTROL__BRANCH_PATTERN` configuration that governs push admission —
  no separate configuration key is introduced.
- **FR-004**: When the API receives an `AdmitResources` request with a zero new-OID, it MUST
  remove all catalog resources (products, variants, collections, category taxonomies) that
  were admitted on the deleted branch.
- **FR-005**: Branch deletion admission MUST NOT affect resources admitted on other branches;
  only resources associated with the deleted ref are removed.
- **FR-006**: Branch deletion admission MUST be fire-and-forget (non-blocking) — the git
  client receives a success response without waiting for the API admission call to complete.
- **FR-007**: A branch delete on a ref that does not match the configured branch pattern MUST
  NOT trigger an `AdmitResources` call.
- **FR-008**: The integration test `TestAdmission_BranchDeletion` in
  `tests/integration/admission_operations_test.go` MUST pass end-to-end without being
  skipped.

### Key Entities

- **RefUpdate (zero new-OID)**: A ref update record where `new_oid` is
  `"0000000000000000000000000000000000000000"`, signalling a branch deletion in git
  protocol. The git service already constructs this record correctly; it is not being
  forwarded to the admission pipeline.
- **AdmitResourcesRequest (delete)**: The gRPC message sent to the catalog API for a branch
  deletion. Carries the repository ID, the ref name, the previous commit SHA (`old_commit_sha`),
  and `new_commit_sha` = zero OID. The API interprets a zero `new_commit_sha` as a deletion
  of all resources on that ref.

---

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: `git push origin --delete <branch>` completes with exit code 0 for any branch
  matching the configured admission pattern — no protocol-level errors or 503 responses are
  returned to the git client.
- **SC-002**: After deleting a branch that had admitted catalog resources, querying those
  resources via the API returns not-found within the time window expected for fire-and-forget
  admission processing (consistent with push admission latency in the same environment).
- **SC-003**: Catalog resources admitted on branches other than the deleted branch remain
  fully present and unmodified after the deletion, verified by querying each resource before
  and after the delete operation.
- **SC-004**: `TestAdmission_BranchDeletion` passes end-to-end in the integration test suite
  without the skip guard being triggered (i.e., the remote accepts both the feature-branch
  push and the subsequent branch-delete push).
- **SC-005**: Deleting a branch that does not match the configured admission pattern produces
  no observable admission activity — no new entries appear in the datastore and no `AdmitResources`
  call is logged by the API for that ref.

---

## Assumptions

- The zero-OID path in `loadParsedEntries` (which returns nil for a zero SHA) already
  correctly causes `deriveResourceAdmissionOperations` to treat all prior entries as deletes.
  No ScyllaDB scan or separate enumeration of branch resources is required; the existing Go
  logic is correct. Only the Rust forwarding of branch-delete ref updates is broken.
- Branch deletion admission uses the same `GITSTORE_ADMISSION_CONTROL__BRANCH_PATTERN`
  configuration as branch-push admission. A separate delete-branch pattern is unnecessary
  because the operational intent is symmetric: if a branch is admitted on push, its resources
  must be cleaned up on delete.
- Branch deletion admission is and remains non-blocking (fire-and-forget), consistent with
  all other post-receive admission calls. A failure to reach the API does not fail the git
  push.
- Tag deletion is out of scope and must not trigger admission. Only `refs/heads/*` deletes
  that match the branch pattern are forwarded.
- The `AdmissionControlHandler` in the Rust git service already handles zero old-OID
  (branch creation) correctly. The branch-delete case (zero new-OID) requires an analogous
  change to avoid filtering it out before the gRPC call.

---

## Out of Scope

- Tag deletion (separate concern; tags are not a source of admitted resources today).
- Protected-branch enforcement (no branch protection model exists yet).
- Re-admission on branch re-create (handled by existing create-branch path; no new work).
- Reconciliation of resources left orphaned by admission failures during deletion (future
  spec; the fire-and-forget model accepts this risk).
