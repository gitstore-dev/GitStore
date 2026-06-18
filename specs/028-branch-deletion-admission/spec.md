# Feature Specification: Branch Deletion Admission

**Feature Branch**: `028-branch-deletion-admission`  
**Created**: 2026-06-18  
**Status**: Draft  
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

**Acceptance Scenarios**:

1. **Given** a branch `experiment/foo` that does not match the configured branch pattern,
   **When** it is deleted, **Then** no `AdmitResources` call is made.
2. **Given** a branch `main` that matches the configured branch pattern, **When** it is
   deleted, **Then** `AdmitResources` is called with a zero new-OID.

---

## Out of Scope

- Tag deletion (separate concern; tags are not a source of admitted resources today).
- Protected-branch enforcement (no branch protection model exists yet).
- Re-admission on branch re-create (handled by existing create-branch path; no new work).

---

## Dependencies

| Spec | Title | Status | Relationship |
|------|-------|--------|--------------|
| 027-admission-contracts | Admission Control Contract | Closed | Provides `AdmitResources` zero-OID handling in Go |
| codex/operation-aware-git-admission | Operation-aware admission | Branch | Implements `old_commit_sha` / `new_commit_sha` fields used here |

---

## Open Questions

1. Should branch deletion admission be gated by the same
   `GITSTORE_ADMISSION_CONTROL__BRANCH_PATTERN` that governs pushes, or a separate
   `GITSTORE_ADMISSION_CONTROL__DELETE_BRANCH_PATTERN`? (Default assumption: same pattern.)
2. Is there a ScyllaDB scan needed to enumerate all resources admitted on the deleted branch,
   or does the zero-OID path in `loadParsedEntries` (which returns nil for zero SHA and lets
   `deriveResourceAdmissionOperations` treat all old entries as deletes) already handle it
   correctly?
