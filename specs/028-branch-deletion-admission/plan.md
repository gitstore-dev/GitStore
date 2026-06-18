# Implementation Plan: Branch Deletion Admission

**Branch**: `028-branch-deletion-admission` | **Date**: 2026-06-18 | **Spec**: [spec.md](spec.md)  
**Input**: Feature specification from `/specs/028-branch-deletion-admission/spec.md`

## Summary

Branch-delete git pushes fail with HTTP 503 because the gRPC `receive_pack` handler in the
Rust git service unconditionally calls `stage_pack_from_reader` even when the push contains
no PACK data (branch deletions carry no objects). The fix adds a single guard: when all ref
commands carry a zero new-OID, skip pack staging and pass `None` as the quarantine to the
ref-transaction path. The Go API's existing zero-OID handling in `AdmitResources` is already
correct — no Go changes are required. A Rust unit test (T019f) and the already-written
integration test `TestAdmission_BranchDeletion` complete the test-first coverage.

## Technical Context

**Language/Version**: Rust 1.x (`gitstore-git-service`); Go 1.25 (`gitstore-api`)  
**Primary Dependencies**: `gix 0.84.0`, `tonic 0.14`, `tokio 1.35` (Rust); no new Go deps  
**Storage**: No datastore changes  
**Testing**: `cargo test` (Rust unit); `go test ./...` (Go integration)  
**Target Platform**: Linux server (CI) / macOS (local dev)  
**Project Type**: Multi-service (Rust git service + Go API)  
**Performance Goals**: Branch delete must complete in the same wall-clock window as a
normal push (< 5s end-to-end including fire-and-forget admission)  
**Constraints**: Zero new production dependencies; no proto changes; no Go-side changes  
**Scale/Scope**: Single-file Rust change; single new unit test

## Constitution Check

| Principle | Status | Notes |
|---|---|---|
| I. Test-First | ✅ PASS | T019f written before the guard is removed; integration test already exists and currently skips |
| II. API-First | ✅ PASS | No new service contracts; existing `AdmitResourcesRequest` proto is sufficient |
| III. Clear Contracts | ✅ PASS | No interface changes |
| IV. Observability | ✅ PASS | Structured `info` log emitted when branch-delete admission is forwarded |
| V. User Story Driven | ✅ PASS | Two user stories; both are independently testable |
| VI. Incremental Delivery | ✅ PASS | P1 (deletion removes resources) is independently deployable |
| VII. Simplicity | ✅ PASS | One conditional guard; no new abstractions |

**No gate violations. Proceeding.**

## Project Structure

### Documentation (this feature)

```text
specs/028-branch-deletion-admission/
├── plan.md           ← this file
├── research.md       ← root cause analysis, affected code paths, constitution check
├── data-model.md     ← RefUpdate / AdmitResourcesRequest cross-reference; control-flow diff
├── quickstart.md     ← verification steps and changed files
└── tasks.md          ← Phase 2 output (/speckit.tasks command)
```

### Source Code (affected files only)

```text
gitstore-git-service/
└── src/
    ├── grpc/
    │   └── server.rs              ← CHANGE: is_delete_only guard in receive_pack()
    └── git/
        └── hooks/
            └── admission_handler.rs  ← CHANGE: add T019f unit test

tests/
└── integration/
    └── admission_operations_test.go  ← NO CHANGE (already written; will pass after fix)
```

**Structure Decision**: Single-project Rust service change. Only `grpc/server.rs` and
`admission_handler.rs` are modified. No new files.

## Phase 0: Research Output

See [research.md](research.md) for full findings. Summary of resolved questions:

| Question | Resolution |
|---|---|
| Where does the 503 originate? | `grpc/server.rs` line 851–857: unconditional `stage_pack_from_reader` on empty stream |
| Does `AdmissionControlHandler` need changing? | No — it already forwards zero new-OID on matching refs; only the pack-staging guard is missing |
| Does the Go API need changing? | No — `isZeroOID` path in `AdmitResources` is already correct |
| Is the integration test already written? | Yes — `TestAdmission_BranchDeletion` in `tests/integration/admission_operations_test.go` |
| What unit tests are missing? | T019f: zero new-OID on matching ref triggers one `AdmitResources` call |

## Phase 1: Design

### Change 1 — Pack-staging guard in `receive_pack` (Rust)

**File**: `gitstore-git-service/src/grpc/server.rs`

After extracting `ref_commands` from the first chunk (current line 813), compute:

```rust
let is_delete_only = ref_commands.iter().all(|c| {
    c.new_oid == "0000000000000000000000000000000000000000"
});
```

Then gate the channel-bridge and `stage_pack_from_reader` block:

```rust
let quarantine = if is_delete_only {
    None
} else {
    // existing channel bridge + spawn + stage_pack_from_reader → Some(q)
};
```

The subsequent `promote_quarantine` call inside the transaction commit arm is already
wrapped in the `quarantine` value via the existing `TxnOutcome::Committed` arm — the
existing `promote_quarantine(&repo, quarantine)` on line 1022 must be gated on
`if let Some(q) = quarantine`. Verify the existing code already does this; if not, add the
guard.

Emit a structured log before the admission call for branch deletes:

```rust
if is_delete_only {
    info!(
        repo_id = %repo_id,
        refs = ?ref_commands.iter().map(|c| &c.ref_name).collect::<Vec<_>>(),
        "receive_pack: branch deletion forwarded to admission"
    );
}
```

### Change 2 — Unit test T019f (Rust)

**File**: `gitstore-git-service/src/git/hooks/admission_handler.rs`

Add after the existing T019e test:

```rust
// T019f: zero new_oid (branch delete) on matching ref triggers AdmitResources
#[tokio::test]
async fn test_branch_deletion_triggers_admit() {
    let count = Arc::new(AtomicU32::new(0));
    let addr = start_mock_server(count.clone()).await;

    let handler = AdmissionControlHandler::connect(&addr, "refs/heads/main".to_string())
        .await
        .unwrap();

    // new_oid all-zeros = branch deletion
    let update = RefUpdate {
        ref_name: "refs/heads/main".to_string(),
        old_oid: "a".repeat(40),
        new_oid: "0".repeat(40), // zero new-OID
    };
    let result = handler
        .admit("post-receive", &[update], "repo-1")
        .await
        .unwrap();
    assert!(matches!(result, AdmissionDecision::Accept));

    tokio::time::sleep(Duration::from_millis(100)).await;
    assert_eq!(
        count.load(Ordering::SeqCst),
        1,
        "branch deletion must trigger exactly one AdmitResources call"
    );
}
```

The mock server's `admit_resources` increments `admit_call_count` regardless of the
`new_commit_sha` value, so T019f re-uses the existing `MockCatalogService` without change.

### No contracts directory needed

This spec introduces no new public interfaces, proto fields, GraphQL schema changes, or
CLI commands. The contracts directory is omitted.

## Complexity Tracking

No constitution violations. No complexity justification required.
