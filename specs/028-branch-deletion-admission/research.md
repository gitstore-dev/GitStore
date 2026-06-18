# Research: Branch Deletion Admission (spec 028)

## 1. Root Cause Analysis

**Decision**: The 503 is produced by the gRPC `receive_pack` handler in
`gitstore-git-service/src/grpc/server.rs` because `stage_pack_from_reader` (line 851–857)
is called unconditionally, even for branch-delete pushes that contain no PACK data. When
`gix_pack::Bundle::write_to_directory` is invoked on an empty byte stream it returns an
error; that error propagates as a `Status::internal` response which the HTTP proxy surfaced
as 503.

**Fix location**: `gitstore-git-service/src/grpc/server.rs` in the `receive_pack` handler.
The handler must detect whether all ref commands are deletes (all `new_oid == zero-OID`)
before attempting to stage a pack. If there is no pack data (all-delete push), skip
`stage_pack_from_reader` and pass `None` as the quarantine to the ref-transaction path.
The `pack_server::handle_receive_pack` in the HTTP path already handles this correctly
(line 121: `if !pack_data.is_empty()`) — the gRPC path missed the equivalent guard.

**Rationale**: Both paths (HTTP and gRPC) parse the same `RefUpdate` structs and build the
same `gix::refs::transaction::Change::Delete` for zero-new-OID entries. The gRPC path just
never guarded the pack-staging step. The HTTP path already short-circuits `quarantine = None`
for empty pack data.

**Alternatives considered**:
- *Return early on all-delete push before pipeline*: Would skip the hook pipeline and
  post-receive `AdmitResources` call — incorrect; admission must still fire.
- *Treat pack-staging error as warning and continue*: Fragile; doesn't distinguish
  genuine pack corruption from intentional absence.

---

## 2. `AdmissionControlHandler` Filter for Zero New-OID

**Decision**: The `AdmissionControlHandler` in
`gitstore-git-service/src/git/hooks/admission_handler.rs` already iterates `updates` and
calls `AdmitResources` for each update that matches `branch_pattern` (line 56–84). The
current filter is `update.ref_name != self.branch_pattern` which skips non-matching refs.
It does **not** filter out zero new-OID — so branch deletes on matching refs would already
be forwarded if the PACK staging error were fixed. **No change needed in the admission
handler itself.**

However, the `branch_pattern` comparison is a string equality check
(`update.ref_name != self.branch_pattern`), which correctly handles exact branch names like
`refs/heads/main`. Branch pattern glob matching is not implemented in spec 028 — the
existing equality check is preserved.

---

## 3. Go API Zero-OID Path Correctness

**Decision**: The `AdmitResources` handler in
`gitstore-api/internal/cataloggrpc/server.go` already correctly handles a zero `new_commit_sha`:

1. `loadParsedEntries` returns `nil` for a zero/empty SHA (line 399: `if ref == "" || isZeroOID(ref)`).
2. With `oldEntries` populated from the pre-delete commit and `newEntries = nil`, 
   `deriveResourceAdmissionOperations` emits `OperationDelete` for every resource in `oldEntries`.
3. `applyResourceOperations` processes those deletes.
4. The staleness guard (lines 328–337) correctly skips a zero-OID delete if the ref was
   subsequently recreated.

**No Go-side changes are required.** The fix is entirely in the Rust git service.

---

## 4. Integration Test Current State

`TestAdmission_BranchDeletion` in `tests/integration/admission_operations_test.go` (line 396)
uses `t.Skipf` on push rejection, meaning the test currently skips on the feature branch push
because the remote returns an error. Once the PACK-staging guard is fixed, the test will run
through to `deleteRemoteBranch` and reach the admission assertions.

The test verifies:
1. `main` product is still present after feature branch deletion.
2. Feature product is absent after deletion (logged as a failure if present, not hard-fail,
   because branch admission is fire-and-forget with eventual consistency).

The test structure is already correct — no test changes are required.

---

## 5. Rust Unit Test Coverage Gap

The existing `admission_handler.rs` tests (T019a–T019e) cover matching, non-matching,
transport error, new-branch creation (zero old-OID), and prefix-extension cases. They do **not**
cover the zero new-OID (branch delete) case. A new test `T019f` must be added to verify that a
zero new-OID update on a matching ref produces exactly one `AdmitResources` call with
`new_commit_sha = "0000...0"`.

No `pack_server.rs` tests need to change — the guard is in `grpc/server.rs` which is not
unit-tested at the handler level (it's an async gRPC service; the existing tests use mocks
at a higher level).

---

## 6. Constitution Compliance

| Principle | Status |
|---|---|
| I. Test-First | T019f written before the guard is removed; integration test already exists |
| II. API-First | No new proto fields or service contracts — existing `AdmitResourcesRequest` is sufficient |
| IV. Observability | Structured log at `info` level when a branch-delete admission is forwarded |
| VII. Simplicity | Single-line guard addition; no new abstractions or dependencies |
