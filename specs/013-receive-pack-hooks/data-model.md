# Data Model: In-Process git-receive-pack Hook Pipeline

**Branch**: `013-receive-pack-hooks` | **Date**: 2026-06-01

## Entities

### RefUpdate

Represents a single ref being created, updated, or deleted as part of a push event.

| Field    | Type   | Constraints                                      |
|----------|--------|--------------------------------------------------|
| ref_name | String | Non-empty; valid Git ref name (`refs/heads/…`)   |
| old_oid  | String | 40-hex SHA-1; all-zeros = ref does not exist yet |
| new_oid  | String | 40-hex SHA-1; all-zeros = deletion               |

Already exists in `git/hooks.rs`. No new fields needed.

---

### HookDecision

The outcome returned by each hook phase.

```
HookDecision
  ├── Accept
  └── Reject(reason: String)   // reason surfaces verbatim to Git client
```

New type to be added to `git/hooks.rs`. Replaces the current `Result<()>` / `Vec<usize>` ad-hoc return convention.

---

### AdmissionDecision

Returned by an `AdmissionHandler` implementation.

```
AdmissionDecision
  ├── Accept
  └── Reject(reason: String)
```

New type in `git/hooks.rs`. Semantically identical to `HookDecision`; kept separate so the admission contract is explicit and independently evolvable.

---

### AdmissionHandler (trait)

An async, dyn-compatible trait that external admission and validation services (future: #105, #106) implement.

```rust
#[async_trait]
pub trait AdmissionHandler: Send + Sync {
    async fn admit(
        &self,
        phase: &str,           // "pre-receive" | "update" | "proc-receive" | "reference-transaction"
        updates: &[RefUpdate],
    ) -> anyhow::Result<AdmissionDecision>;
}
```

Stored as `Arc<dyn AdmissionHandler + Send + Sync>` in `HttpPackServer` and passed into the gRPC `receive_pack` handler.

**Built-in implementations:**
- `NoopAdmissionHandler` — always returns `Accept`; used when no admission service is configured (default).

---

### HookPipeline

Encapsulates phase execution, config-toggle enforcement, and per-phase logging. New type in `git/hooks.rs`.

| Field              | Type                               | Description                                    |
|--------------------|------------------------------------|------------------------------------------------|
| config             | `HooksConfig` (copy/clone)         | Phase enabled/disabled toggles                 |
| admission_phase    | `String`                           | Which phase routes to the admission handler    |
| admission_handler  | `Arc<dyn AdmissionHandler>`        | The admission/validation service callout       |

---

### PhaseLog (structured log event — not a persisted struct)

Emitted via `tracing::info!` / `tracing::warn!` for each phase execution.

| Field       | Type | Notes                                         |
|-------------|------|-----------------------------------------------|
| phase       | &str | "pre-receive", "update", "proc-receive", etc. |
| ref_name    | &str | Present only for per-ref phases (update)      |
| duration_ms | u64  | Elapsed milliseconds for this phase call      |
| outcome     | &str | "accepted" or "rejected"                      |
| reason      | &str | Present only when outcome = "rejected"        |

---

## State Transitions

### Push Event Lifecycle

```
push received
    │
    ▼
[pack staged in quarantine]
    │
    ▼
pre-receive phase ──(Reject)──► abort; return ng to client; drop quarantine
    │ Accept
    ▼
proc-receive phase ──(Reject)──► abort; return ng; drop quarantine
    │ Accept
    ▼
update phase (per ref) ──(Reject)──► mark ref as ng; continue other refs
    │ at least one Accept
    ▼
reference-transaction: prepared
    │
    ├──(Reject)──► rollback all lock files; return ng to all refs; drop quarantine
    │
    │ Accept
    ▼
[promote quarantine pack → ODB]
    │
    ▼
reference-transaction: committed  (observation only)
    │
    ▼
post-receive phase  (fire-and-forget; failures logged at ERROR, not returned)
    │
    ▼
return report-status to client
```

### reference-transaction Sub-states

```
prepared  ──Accept──►  committed  (refs written)
          ──Reject──►  aborted    (lock files dropped)
          ──error ──►  aborted    (Drop impl releases locks)
```

---

## Validation Rules

- `ref_name` must start with `refs/` (enforced upstream by gix parse, not re-validated in hook layer).
- `old_oid` and `new_oid` must be exactly 40 hex characters or the all-zeros string.
- A `HookDecision::Reject` reason string MUST be non-empty; the pipeline substitutes `"rejected by hook"` if empty.
- Post-receive is only invoked with the accepted ref updates subset (those that passed update + reference-transaction).

---

## Config Entities (existing, no changes)

`HooksConfig` / `GitReceivePackHooks` / `HookToggle` — all exist in `config.rs`. The pipeline reads these by reference; no new config keys are introduced in this feature.

`AdmissionControlConfig.validating_admission_policy.phase` — the string that `HookPipeline.admission_phase` is initialised from at startup.
