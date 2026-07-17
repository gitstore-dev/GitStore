# Research: Git Smart-HTTP Authentication (035)

**Date**: 2026-07-12  
**Branch**: `035-git-http-auth`

## Decision 1 — Prometheus counter shape for auth outcomes

**Decision**: Single `CounterVec` named `gitstore_git_http_auth_requests_total` with labels `outcome` (`allow`/`deny`/`error`) and `service` (`upload_pack`/`receive_pack`). Registered on an injected `prometheus.Registerer` (not hardcoded `DefaultRegisterer`) so tests can use isolated registries.

**Rationale**: Matches the existing `gitstore_datastore_operation_errors_total` pattern (`Namespace: "gitstore"`, `Subsystem: <component>`, `Name: <verb_noun>_total`). A single `CounterVec` with labels is idiomatic Prometheus — avoids proliferating separate counters for each outcome/service combination and keeps the metric cardinality bounded.

**Alternatives considered**:
- Separate counters per outcome (`gitstore_git_http_auth_allow_total`, etc.) — rejected; SC-007 names these but the `CounterVec` approach subsumes them with the same queryability.
- Hardcoding `prometheus.DefaultRegisterer` — rejected; the datastore instrumentation already demonstrates the injected-registerer pattern for testability (`NewInstrumentedDatastoreWithRegistry`).

---

## Decision 2 — Middleware value propagation pattern

**Decision**: Use `c.Set("repoID", repoID)` / `c.Get("repoID")` (gin-native) for `repoID`. Use `context.WithValue` with a typed private key (the existing `principalContextKey` pattern) only for `Principal`, which must escape gin into downstream gRPC and datastore calls.

**Rationale**: The gin docs recommend using `c.Set` / `c.Get` for request-scoped values shared within the handler chain, and reserving `context.WithValue` for values that need to propagate beyond gin (e.g., into `store.*` or gRPC calls via `c.Request.Context()`). `repoID` is consumed only by `GitHttpAuthorizer`, `PushContextInserter`, and route handlers — all of which hold `*gin.Context` — so `c.Set` is the correct mechanism. `Principal` correctly uses `context.WithValue` because it flows into datastore and GraphQL resolver calls that receive only a `context.Context`.

**Alternatives considered**:
- `context.WithValue` for `repoID` — rejected; unnecessary for a value that never leaves the gin chain; goes against gin's own best-practice guidance on when to use each mechanism.
- Passing resolver func as a closure into each middleware — rejected; introduces duplicate datastore lookups.

---

## Decision 3 — Proto field number for `push_context`

**Decision**: Field number **4** for `push_context PushContext` in `ReceivePackRequest`.

**Rationale**: Fields 1–3 are the core streaming fields (`ref_commands`, `pack_data`, `is_last`). Field 4 is the next open slot and groups `push_context` with the first-chunk metadata. Fields 1–15 encode as 1 byte in proto3 wire format — preserving all hot fields (including `pack_data`) in the 1-byte range. The `repository_id = 15` convention (used across all request messages in this proto) reserves high single-byte slots for cross-cutting routing keys; new content fields use low numbers.

**Alternatives considered**:
- Field 16+ — rejected; 2-byte encoding for a field sent on every push first-chunk adds unnecessary wire overhead.
- Field 14 (adjacent to `repository_id = 15`) — rejected; no semantic reason to group it with the routing key.

---

## Decision 4 — Where `PushPolicy` fields live in the Go datastore

**Decision**: New fields on `datastore.Repository`: `MaxPackSizeBytes int64` and `MaxFileSizeBytes int64`. Zero value = no limit (FR-015 sentinel). Resolved via `store.GetRepository(ctx, repoID)` after `LookupRepository` returns the `repoID`.

**Rationale**: The spec requires policy to come from "operator-controlled repository control-plane state" (FR-013). `Repository` is the existing control-plane record for a repository. Adding fields directly avoids a new join, a new table/index, and a new datastore interface method. `GetRepository` already exists in the `Datastore` interface.

**Alternatives considered**:
- Separate `RepositoryPolicy` record — rejected; unnecessary complexity for two scalar fields.
- Operator config file — rejected; would make policy static/global rather than per-repository.

---

## Decision 5 — 503 vs 401 split in `basicAuth`

**Decision**: `err != nil` (provider error) → 503 + structured zap error log. `OutcomeDeny` with `err == nil` (credential rejection) → 401 + `WWW-Authenticate: Basic realm="GitStore"`. No new error types or `Decision` outcome values needed.

**Rationale**: The `ChainedAuthN.Authenticate` contract already separates transport/provider faults (`error` return) from policy decisions (`Decision.Outcome`). The split is unambiguous at the call site and requires only a reordering of the existing conditional in `basicAuth`. Introducing a sentinel error type or new `OutcomeError` variant would add complexity with no benefit.

**Alternatives considered**:
- `auth.TransientError` sentinel type — rejected; unnecessary; `err != nil` is already the transient signal.
- New `OutcomeError` Decision outcome — rejected; conflates the error channel with the policy channel, complicating callers that only care about one of the two.
