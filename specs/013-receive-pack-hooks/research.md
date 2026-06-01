# Research: In-Process git-receive-pack Hook Pipeline

**Branch**: `013-receive-pack-hooks` | **Date**: 2026-06-01

## Decision 1 — gix Two-Phase Reference Transaction

**Decision**: Use `gix_ref::file::Store::transaction()` directly (low-level API) to split `prepare()` from `commit()`, giving a natural "prepared" veto point for the reference-transaction hook phase.

**Rationale**: The high-level `Repository::edit_references()` / `edit_references_as()` fuses prepare and commit in one call with no hook injection point. The low-level `gix_ref::file::Store` (accessible via `repo.refs`) exposes the two-phase API:

```
let txn = repo.refs.transaction()
    .prepare(edits, file_lock_fail, packed_lock_fail)?;  // ← "prepared" state, locks held
// run reference-transaction hook here — veto by drop(txn), allow by txn.commit(committer)?
```

- `prepare()` — acquires `.lock` files for every ref, validates old-value constraints, writes new values into lock files. After this call, all lock files are held but nothing is committed.
- `commit()` — renames lock files into place (atomic).
- `rollback()` / `Drop` — releases all lock files with no write (aborted state).

**Alternatives considered**: Patching gix to add a callback (too invasive, external dep); using `edit_references()` and faking the prepared state (incorrect — objects would be staged but refs rolled back manually, race-prone); skipping reference-transaction veto entirely and making it observation-only (rejected — spec requires FR-008 veto in prepared state).

**Note**: Actual gix version in Cargo.toml is `0.84.0` (not 0.83.0 stated in CLAUDE.md — the lock file is canonical).

---

## Decision 2 — AdmissionHandler Trait Design

**Decision**: Add `async-trait = "0.1"` to `gitstore-git-service` Cargo.toml and define:

```rust
#[async_trait]
pub trait AdmissionHandler: Send + Sync {
    async fn admit(&self, phase: &str, updates: &[RefUpdate]) -> anyhow::Result<AdmissionDecision>;
}
```

with `AdmissionDecision::Accept` / `AdmissionDecision::Reject(String)`.

**Rationale**: The codebase is on Rust edition 2021 with MSRV 1.82. RPITIT (`async fn` in traits, stable Rust 1.75+) is not dyn-compatible without additional machinery. Since `Arc<dyn AdmissionHandler>` is required so the handler can be passed into both `HttpPackServer` and the gRPC `receive_pack` handler, `#[async_trait]` is the cleanest path — it desugars to `Pin<Box<dyn Future<...> + Send>>` which is dyn-compatible. The codebase already uses `#[tonic::async_trait]` (a re-export), so the pattern is established.

**Alternatives considered**: Manual `BoxFuture` return type (works but verbose, no ergonomic benefit over async-trait); RPITIT with `dyn-async-trait` shim (extra dep, less standard); synchronous trait (forces blocking inside async context, defeats tokio::time::timeout usage).

---

## Decision 3 — 5-Second Admission Timeout

**Decision**: Use `tokio::time::timeout(Duration::from_secs(5), handler.admit(...).await)` in the async `receive_pack` gRPC handler. Timeout → treat as `Reject("admission service timeout")`.

**Rationale**: tokio 1.35 with `features = ["full"]` already includes `tokio::time`. The `receive_pack` RPC is a real `async fn` (not inside `spawn_blocking`), so the timeout wraps the admission await directly without thread-pool contention. Hardcoded 5 s for alpha; a configurable value is deferred.

**Alternatives considered**: `spawn_blocking` with `std::thread` timeout (works but wastes a blocking thread slot for async network I/O); no timeout (violates FR-009a); configurable timeout in this feature (deferred to a follow-on — config keys already exist in `AdmissionControlConfig`).

---

## Decision 4 — Config Toggle Wiring

**Decision**: Pass `&HooksConfig` (from `AppConfig`) into both `HttpPackServer` and the gRPC `receive_pack` handler. Each phase call site checks the relevant `HookToggle::enabled` before invoking the phase function.

**Rationale**: `HooksConfig` and all five `HookToggle` fields already exist in `config.rs` (all disabled by default). No new config keys are needed (assumption confirmed). Passing config by reference keeps `HttpPackServer` lightweight; the gRPC handler already has `AppConfig` accessible via a shared `Arc<AppConfig>` pattern to be established in this feature.

**Alternatives considered**: Global config singleton (not idiomatic Rust); per-toggle method on `HttpPackServer` (too granular); reading config inside each hook function (breaks single-responsibility).

---

## Decision 5 — Structured Phase Logging

**Decision**: Emit a `tracing::info!` span per phase execution with fields: `phase`, `ref_name` (if per-ref), `duration_ms`, `outcome` ("accepted" | "rejected"), and `reason` (if rejected).

**Rationale**: The codebase already uses `tracing` for all observability (FR-004 via `emit_span` in `pack_server.rs`). Adding per-phase events follows the same pattern. Using `tracing::info!` (not `debug!`) for accepted phases and `tracing::warn!` for rejections ensures operators see hook activity in production `level = "info"` logs without noise.

**Alternatives considered**: Metrics-only (no log lines for accepted phases — insufficient for debugging rejection chains); `tracing::Span` with enter/exit (heavier, no benefit here); separate audit log (out of scope for alpha).

---

## Decision 6 — proc-receive Placement and Semantics

**Decision**: Execute proc-receive after pre-receive and before update, per Git documentation. The stub implementation passes all ref updates through unmodified (no rewriting), providing the invocation point required by the spec without implementing ref rewriting logic (deferred to #105/#106).

**Rationale**: The FR-007 requirement is for the invocation point with ability to rewrite ref targets, not that rewriting is exercised now. The admission handler trait covers the "routing contract" for proc-receive. The order pre-receive → proc-receive → update matches the Git source and the spec FR-001 ordering.

**Alternatives considered**: proc-receive after update (incorrect per Git spec); proc-receive as part of update (conflates per-push and per-ref semantics).

---

## Decision 7 — HTTP vs gRPC call site duplication

**Decision**: Both `HttpPackServer::handle_receive_pack` (sync, called from blocking context) and `grpc/server.rs::receive_pack` (async) share the same synchronous phase functions for pre-receive, update, proc-receive, post-receive. The reference-transaction phase and admission handler calls are async and live only in the gRPC path for now; the HTTP path calls a blocking wrapper.

**Rationale**: `handle_receive_pack` runs inside `spawn_blocking` (see gRPC server usage). Adding async admission calls there would require a `block_on` trampoline, which is anti-pattern with tokio. For this feature, the HTTP path's admission handler is a no-op `NoopAdmissionHandler`; the real async handler is only wired in the gRPC path. The HTTP path is a secondary code path (Smart HTTP proxied via gitstore-api which calls gRPC).

**Alternatives considered**: Fully async `HttpPackServer` (large refactor, out of scope); duplicating all logic (maintenance burden).
