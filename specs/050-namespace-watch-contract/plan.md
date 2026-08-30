# Implementation Plan: Namespace Watch Contract and Durable Journal

**Branch**: `050-namespace-watch-contract` | **Date**: 2026-08-30 | **Spec**: [spec.md](spec.md)
**Input**: Feature specification from `/specs/050-namespace-watch-contract/spec.md`

## Summary

Expose the canonical cluster-scoped
`watchNamespaces(selector, resourceVersion): NamespaceWatchEvent!` GraphQL
subscription and replace Namespace's process-local watch history with a durable,
replica-safe journal. Scylla CDC captures the committed effects of shipped
spec 047 synchronously with the authoritative Namespace row; a fenced embedded
materializer normalizes those changes into one bounded global Namespace stream.
Every API replica replays/tails that journal with the same opaque cursor,
explicit expiry/backpressure errors, durable idle bookmarks, authorization,
readiness, metrics, and rolling-upgrade contract. The generic
`watchResources(kind: "Namespace")` path remains compatible and reads the same
journal. Spec-047 admission, deletion, repository fencing, status ownership,
and mutation outcomes are inputs and are not reopened.

## Technical Context

**Language/Version**: Go 1.25 (`gitstore-api`); gqlgen v0.17.90 generated GraphQL contracts  
**Primary Dependencies**: Existing gocqlx/gocql Scylla stack, go-memdb, gqlgen WebSocket transport, zap, Prometheus client; new `github.com/scylladb/scylla-cdc-go v1.2.1` for production CDC stream/topology/progress handling  
**Storage**: Migration 006 enables full-preimage/postimage CDC with 14-day TTL on `namespaces_by_uid`, adds an internal projection-commit marker, and adds bounded journal events plus a partition-local clock/lease/progress table. Memdb uses an in-process implementation of the same journal contract for development. No public spec-047 Namespace contract fields change.
**Testing**: Go unit, resolver, middleware, transport, backend-neutral journal contracts, tagged memdb/Scylla integration, two-API-replica rolling-replacement tests, and a 60-minute threshold-enforcing capacity soak  
**Target Platform**: Linux server; Darwin/Linux development environments  
**Project Type**: GraphQL API subscription plus embedded Scylla CDC materializer/journal infrastructure  
**Performance Goals**: 10 sustained Namespace transitions/s with 100/s bursts and 1,000 concurrent subscriptions across two API replicas; event visibility p95 ≤1 s and p99 ≤3 s; 10,000-event replay p95 ≤5 s; recovery after replica replacement ≤30 s; internal errors <0.1%; zero missing acknowledged transitions  
**Constraints**: Additive GraphQL evolution; preserve generic path; seven-day bounded journal; 14-day CDC TTL; at-least-once/idempotent delivery; one per-kind order; no cross-kind order; explicit `WATCH_EXPIRED` on continuity loss; `FORBIDDEN` before cursor disclosure; no change to spec-047 mutation behavior  
**Scale/Scope**: Cluster-scoped Namespace stream only. Product/category/file watches retain their current backends pending their own migration specs. Namespace volume is low relative to the 5,000,000-product catalogue, but infrastructure is tested under sustained push/admission traffic and 1,000 subscribers.  
**Replica/Scaling Model**: Every API replica reads one shared journal epoch/cursor space. A Scylla-LWT lease/fencing token bounds CDC materialization to one active writer; overlap may duplicate but cannot lose events or permit stale progress. Any replica can register/replay a subscription.  
**Authentication/Authorization**: Both typed and generic Namespace watches require cluster-scoped `namespace.watch` before cursor parsing, journal reads, or replay. Existing pluggable AuthN/AuthZ providers and decision logging remain the enforcement boundary.  
**Load/Backpressure Model**: Journal buckets 4,096 events; read batches 256; subscriber channel 64; maximum resume 100,000 events; bounded 30 s delivery backpressure; 100 ms journal poll with capped 2 s idle backoff; 30 s durable bookmark; 30 s materializer lease renewed every 10 s; sustained overflow/retention/discontinuity fail closed.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle | Gate evaluation |
|---|---|
| I. Test-First Development | PASS — schema, AuthZ, journal, CDC, materializer, resolver, replica, restart, overflow, and capacity tests precede implementation. |
| II. API-First Design | PASS — typed GraphQL, cursor/error, journal, rollout, and operational contracts are defined in `contracts/` first. |
| III. Clear Contracts & Versioning | PASS — GraphQL is additive; generic compatibility, cursor version/epoch, migration retention, mixed-version deny, and rollback order are explicit. |
| IV. Production Observability & Debuggability | PASS — CDC/materializer lag, lease, journal bounds, replay, subscribers, expiry, overflow, bookmarks, readiness, and end-to-end latency have required signals. |
| V. User Story Driven Development | PASS — bootstrap/live view, resume/expiry, and documented consumer behavior remain independently testable. |
| VI. Independently Deployable Delivery | PASS — migration-first/server-first rollout denies Namespace watches during mixed semantics; spec-047 mutations remain available and unchanged. |
| VII. Simplicity with Proven Scale | PASS — Scylla's synchronous CDC avoids a new broker/service and is justified by the replica-safe atomic change requirement. |
| VIII. Horizontally Replicable Core Services | PASS — no watch correctness depends on mutation and subscription sharing a process; cursors/history survive replacement. |
| IX. Multi-User Authentication, Authorization & Isolation | PASS — both entry points enforce `namespace.watch` before any journal disclosure. |
| X. Production Capacity, Backpressure & Load Validation | PASS — every queue/read/replay/retention/retry bound and a 60-minute two-replica envelope are specified. |

**Pre-design gate result**: PASS. No non-negotiable violation remains.

## Project Structure

### Documentation (this feature)

```text
specs/050-namespace-watch-contract/
├── plan.md
├── research.md
├── data-model.md
├── quickstart.md
├── contracts/
│   ├── namespace-watch.graphqls
│   └── namespace-watch-journal.md
└── tasks.md                    # generated by /speckit-tasks, not this command
```

### Source Code (repository root)

```text
shared/
└── schemas/
    └── namespace.graphqls

gitstore-api/
├── go.mod
├── internal/
│   ├── app/
│   │   └── server.go                  # backend/materializer wiring and readiness
│   ├── config/
│   │   └── config.go                  # bounded watch configuration
│   ├── datastore/
│   │   ├── datastore.go               # Namespace watch journal capability
│   │   ├── memdb/
│   │   │   └── namespace_watch.go
│   │   └── scylla/
│   │       ├── namespace_watch.go
│   │       └── migrations/
│   │           └── 006_namespace_watch_cdc.cql
│   ├── eventbus/
│   │   └── eventbus.go                # retained for non-Namespace kinds
│   ├── graph/
│   │   ├── generated/                 # gqlgen regenerated output
│   │   ├── model/
│   │   │   └── models_gen.go
│   │   └── resolver/
│   │       ├── namespace.resolvers.go # typed subscription
│   │       ├── schema.resolvers.go    # generic Namespace routing
│   │       └── watch.go               # event conversion/errors
│   ├── middleware/security/
│   │   └── graphql.go                 # namespace.watch enforcement
│   └── watchjournal/
│       ├── journal.go                 # interfaces, cursor, expiry reasons
│       ├── materializer.go            # CDC normalization and sequencing
│       ├── lease.go                   # LWT lease/fencing
│       ├── subscriber.go              # bounded replay/tail
│       ├── metrics.go
│       └── readiness.go
└── tests/
    └── contract/
        └── namespace_watch_test.go

tests/
└── integration/
    └── namespace_watch_test.go        # two API replicas + Scylla

docs/
├── configuration.md
├── namespace/
│   └── namespace-watch.md
└── runbooks/
    └── controller-watch-status.md

compose.scylla.yml                     # CDC/journal integration configuration
Makefile                               # focused/capacity targets if new commands are required
```

**Structure Decision**: Add one internal Namespace journal/materializer package
to `gitstore-api`, extend existing datastore backends, GraphQL, security, and
server wiring in place, and keep every other kind on the existing eventbus.
No fourth service or external broker is introduced.

## Phase 0: Research Outcomes

Research is captured in [research.md](research.md). Decisions:

- Preserve spec 047 as the sole mutation/policy authority.
- Use synchronous Scylla CDC to derive events from committed Namespace writes.
- Use official `scylla-cdc-go v1.2.1` with durable progress.
- Materialize CDC into a seven-day bucketed journal with one global versioned
  cursor and deterministic watermark sequencing.
- Gate ADDED publication on visibility of the shipped Namespace list
  projection, without changing spec-047 mutation outcomes.
- Derive and monotonically persist the actual first retained journal sequence
  as TTL rows expire.
- Append journal rows before saving CDC progress; crash recovery is
  at-least-once and may duplicate, never skip.
- Tail the same journal from every API replica and both GraphQL entry points.
- Retain the bootstrap-bookmark/list/drain sequence with a durable high water.
- Append real journal BOOKMARK records every 30 seconds while idle.
- Fail closed with bounded `WATCH_EXPIRED` reasons on retention, overflow,
  invalid epoch/cursor, replay limit, or discontinuity.
- Require `namespace.watch` on typed and generic Namespace subscriptions.
- Roll out migration/schema/materializer/readers server-first under a fleet-wide
  Namespace-watch deny.
- Enforce explicit retention, batching, buffers, retry, load, recovery, and
  observability thresholds.

No `NEEDS CLARIFICATION` remains.

## Phase 1: Design and Contracts

### Committed change capture

Migration 006 alters the authoritative `namespaces_by_uid` table to enable CDC
with full preimages/postimages and 14-day TTL and adds an internal
`watch_committed` marker. Namespace creation sets the marker only after its
listing projection is durable, allowing CDC to distinguish a committed create
from rollback cleanup. Scylla persists CDC with the base write and consistency
level. Spec-047 service/resolver/catalog gRPC code continues returning the same
public outcomes.

The materializer consumes one logical CDC mutation, coalesces its
preimage/delta/postimage rows, and classifies:

- absent preimage + committed postimage -> ADDED;
- existing preimage + committed postimage -> MODIFIED;
- final row delete + preimage -> DELETED;
- no base write -> no event.

An ADDED candidate crosses the journal boundary only after its matching
`namespaces_by_bucket` row is readable. A present authoritative row with a
missing projection causes a retry without progress advancement; an
authoritative row already removed by create rollback suppresses the staged
addition.

Namespace deletion commits the authoritative `namespaces_by_uid` removal
before changing its list and name projections. A rejected conditional delete
therefore leaves the bootstrap list untouched. The materializer withholds the
resulting DELETED event until `namespaces_by_bucket` no longer exposes the row,
so a successful delete is always repaired by an event after any earlier
bootstrap bookmark.

Deletion marking/finalizer changes remain MODIFIED; only permanent row removal
is DELETED.

### Durable journal and ordering

A fenced LWT allocates `nwv1:<epoch>:<sequence>`. Events are stored in
4,096-sequence buckets with seven-day TTL and read in ascending sequence. CDC
records are normalized through one sequencer and deterministically ordered by
write time with stream/arrival tie-breakers. Publication waits for the common
active-stream watermark, and a newly registered stream behind the published
frontier fails closed. The published frontier is persisted before per-stream
progress and restored after restart. The journal append is the public per-kind ordering
linearization point. Migration 006 follows the repository's existing Scylla
conventions: timestamp columns use the `_timestamp` suffix and event labels use
native `map<text, text>` storage rather than JSON-encoded text. Bounds checks advance the stored `oldest` value from the
first live TTL row before validating a new subscription cursor, scanning at
most 32 buckets and checkpointing once per call. Incomplete reconciliation
fails registration unavailable; a TTL race after registration terminates with
`RETENTION_EXPIRED`. Concurrent replicas use a monotonic CAS for the checkpoint;
a CAS loser rereads and accepts the winner's already-advanced lower bound
rather than reporting a healthy journal unavailable.

The materializer writes the event before saving per-stream CDC progress. A
failure between those steps causes a duplicate after recovery. The controller
contract is level-triggered/idempotent, so no exactly-once machinery is added.

A single fenced materializer appends an actual BOOKMARK after 30 seconds without
a data event. This advances the shared cursor and proves idle health.

### Subscription lifecycle

The `WatchJournal` interface atomically captures high water, validates a cursor,
replays bounded rows, and tails later rows. The bootstrap sentinel returns a
BOOKMARK at captured high water before the client lists. The client holds the
subscription open, pages Namespaces, replaces its local snapshot, then drains
queued rows after the cursor.

A subscriber reads at most 256 rows per query, buffers 64 events with bounded
delivery backpressure, and replays at most 100,000. Any condition that prevents continuity returns `WATCH_EXPIRED`
with a bounded reason; infrastructure unavailable before registration returns
`WATCH_UNAVAILABLE`.

### GraphQL and compatibility

[contracts/namespace-watch.graphqls](contracts/namespace-watch.graphqls) adds
`watchNamespaces` and `NamespaceWatchEvent`. There is no Namespace filter
argument because Namespace is cluster-scoped. `name` is empty only for
BOOKMARK to retain the established non-null event convention.

The generic `watchResources(kind: "Namespace")` resolver routes to the same
journal and cursor converter. Product, CategoryTaxonomy, and File routing is
unchanged.

### Authorization

`authorizeSubscription` recognizes both Namespace forms and calls the active
provider with `namespace.watch` and cluster-scoped resource context before
cursor parsing, validity/retention checks, or journal reads. Tests prove denied
callers cannot distinguish valid, invalid, or expired cursors.

### Rollout and rollback

1. Install a fleet-wide deny for both Namespace watch forms.
2. Apply migration 006 and verify CDC/log/journal schema.
3. Deploy every new API replica with schema support and explicitly override the
   alpha-default-on durable watch gates to disabled.
4. Enable the fenced materializer and require healthy persisted CDC query
   progress; bookmarks do not certify CDC health.
5. Enable journal readers everywhere and run cross-replica probes.
6. Remove the deny; clients may then select `watchNamespaces`.

Rollback restores the deny first, disables readers/materializer, and uses an
artifact retaining migration 006. Spec-047 mutation traffic does not require a
rollback or behavioral change.

### Test-first implementation order

1. Failing GraphQL schema/generated contract tests.
2. Failing typed/generic `namespace.watch` authorization/non-disclosure tests.
3. Failing cursor/journal/replay/expiry/overflow backend-neutral contracts.
4. Failing migration 006 and Scylla CDC atomicity/classification tests.
5. Failing materializer progress, duplicate recovery, fencing, and bookmark
   tests.
6. Implement migration, journal backends, materializer, and metrics/readiness.
7. Implement typed resolver and generic Namespace routing; regenerate gqlgen.
8. Add end-to-end event-shape and bootstrap/resume/expiry tests.
9. Add two-replica rolling replacement and mixed-version rollout tests.
10. Add docs/runbook and threshold-enforcing 60-minute soak.
11. Run focused checks, GraphQL generation, Scylla suites, graph update, and
    `make pr-ready`.

### Post-design Constitution Check

| Principle | Result |
|---|---|
| Test-First | PASS — each contract/infrastructure slice starts with observable failing tests. |
| API-First | PASS — GraphQL and journal/error contracts precede generated/resolver code. |
| Clear Contracts | PASS — cursor version, expiry, payload, rollout, rollback, and compatibility are explicit. |
| Observability | PASS — every durable boundary, lag, bound, failure, and recovery state has a signal. |
| User Story Driven | PASS — bootstrap/live, resume/expiry, and documentation remain independently verifiable. |
| Independent Delivery | PASS — fleet-wide deny and migration-first rollout prevent mixed cursor semantics. |
| Simplicity | PASS — Scylla CDC and one embedded package avoid a new broker/service while meeting atomicity. |
| Replica Safety | PASS — shared durable cursor/history and cross-replica tests remove process affinity. |
| Multi-User Security | PASS — `namespace.watch` precedes all journal disclosure. |
| Production Capacity | PASS — storage, replay, buffers, retries, load, soak, and recovery are bounded and measured. |

**Post-design gate result**: PASS. No complexity exception remains.

## Complexity Tracking

No constitution violation requires an exception. The new CDC journal is
contractually required infrastructure, not optional abstraction: the previous
local ring cannot satisfy feature 050 or the current production constitution.
