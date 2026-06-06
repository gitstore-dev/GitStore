# Implementation Plan: Fix git clone and git fetch over HTTP

**Branch**: `019-fix-upload-pack` | **Date**: 2026-06-06 | **Spec**: [spec.md](spec.md)
**Input**: Feature specification from `/specs/019-fix-upload-pack/spec.md`

## Summary

`git clone` and `git fetch` over HTTP fail because `handle_upload_pack` in
`gitstore-git-service/src/git/pack_server.rs` sends `NAK + 0000` (no pack)
instead of the required `NAK + sideband-pack + 0000`. Two root causes: (1)
`parse_wants_and_haves` discards `done` lines so the server cannot distinguish
a mid-negotiation body from a complete one, and (2) `build_pack_for_wants`
silently returns empty bytes when `rev_walk` yields no commits, producing an
indistinguishable-from-nothing response. The fix is three targeted edits in one
file plus a unit test suite that currently has zero coverage.

## Technical Context

**Language/Version**: Rust edition 2021, MSRV 1.82 (`gitstore-git-service`)
**Primary Dependencies**: `gix 0.84.0`, `gix-pack 0.71.0`, `tokio 1.35`, `anyhow 1.0`, `tracing 0.1`
**Storage**: N/A (reads existing bare git repositories on local filesystem)
**Testing**: `cargo test` (unit); `go test -tags=integration` for the HTTP path (existing `TestGitClone`, `TestGitFetch`)
**Target Platform**: Linux server (Docker); macOS dev via `make dev`
**Project Type**: In-process git smart-HTTP server (Rust)
**Performance Goals**: No regression to existing receive-pack throughput; upload-pack must complete within existing HTTP timeouts
**Constraints**: Must not change the wire format — only the conditions under which the pack is generated. Push path (receive-pack) must be unaffected (FR-007).
**Scale/Scope**: Single file, three edits, one new test module (~10 test functions)

## Constitution Check

| Principle | Status | Notes |
|-----------|--------|-------|
| I. Test-First | PASS | T050–T059 unit tests written before implementation; `TestGitClone` / `TestGitFetch` integration tests exist and currently fail (red phase confirmed). |
| II. API-First | PASS | No new external interfaces introduced. Existing gRPC `UploadPackRequest` / `UploadPackResponse` are unchanged. |
| III. Clear Contracts | PASS | `parse_wants_and_haves` signature change is internal; no public proto or GraphQL schema change. |
| IV. Observability | PASS | Error path now propagates a descriptive message through the gRPC error channel rather than silently returning empty bytes. |
| V. User Story Driven | PASS | Two P1 stories (clone, fetch) with independent test criteria mapped to T054–T059. |
| VI. Incremental Delivery | PASS | Single self-contained fix; no follow-on features required for the fix to deliver value. |
| VII. Simplicity | PASS | Three edits in one file. No new types, no new dependencies, no new files except unit tests. |

**No gate violations. Proceeding.**

## Project Structure

### Documentation (this feature)

```text
specs/019-fix-upload-pack/
├── plan.md           # This file
├── research.md       # Phase 0 — root cause analysis, wire format, fix decisions
├── data-model.md     # Phase 1 — changed signatures, new tests
├── quickstart.md     # Phase 1 — verification steps
└── tasks.md          # Phase 2 output (/speckit.tasks — NOT created by /speckit.plan)
```

### Source Code

```text
gitstore-git-service/
└── src/
    └── git/
        └── pack_server.rs   # THREE edits + new #[cfg(test)] module
```

No other files change. The Go API, proto definitions, compose files, and
CI workflows are all unaffected.

**Structure Decision**: Single-file fix. The bug, its root cause, and all tests
live in `pack_server.rs`. No new modules or crates are introduced.

## Complexity Tracking

> No constitution violations. No complexity justification required.

The only complexity in this fix is the annotated tag dereferencing in
`build_pack_for_wants` — adding a pre-walk peel loop. This is necessary
because `rev_walk` only traverses commits; without it, a `want` for an
annotated tag OID silently triggers the empty-walk error path. The peel loop
adds ~15 lines and no new dependencies.
