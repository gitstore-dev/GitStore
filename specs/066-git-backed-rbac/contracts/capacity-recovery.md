# Validation, Capacity and Recovery Contract

## Existing command interface

Use existing `make test`, `make lint`, `make check TARGET=all`,
`make generate TARGET=all`, `make pr-ready`, `make capacity` and
`make chaos` families. Do not replace full default PR validation with a
capacity run. Datastore contracts run with `make test TARGET=datastore`;
native Scylla uses its existing DATASTORE/address selectors and an owned
isolated keyspace.

The current `namespace/watch` and `api/readiness` drivers require extensions
before they can exercise this feature. A health-only readiness smoke run
is not an RBAC acceptance result. Document supported selectors with the
implementation rather than publish invented flags here.

## Current verification boundary

T061 passes on the owned native Scylla/Kratos deployment: both APIs deliver
ordinary Product pushes on their original subscriptions and enforce revocation;
isolated real API processes verify cold failure, reconstruction, source outage,
same-process recovery and denial after removal. Both GraphQL and native Git USER
creation verify AuthN-selected ownership, admitted owner status, both-API
namespace isolation and exact incarnation finalization. The complete
T064/T073 migration, condition/fault and stale-UID matrix remains pending.

Fresh native bootstrap and real controller enrollment pass with isolated
Scylla keyspaces, singleton Git and two APIs. A separate same-contract upgrade
uses distinct binaries with a private previous-release view-rule fixture and
unmodified current definitions. It verifies changed content/resource versions,
stable UIDs, fresh controller assertions and continued administrator revocation
through the mixed-version window; an unchanged-content control fails.
T080's functional matrix is complete. These are release-fixture process tests,
not shipped-image, production-cutover or stale-rollback acceptance.

Earlier focused cross-layer race suites, the full default `make pr-ready` and
`make check TARGET=all` passed. Final-source aggregate validation remains
required by T101. Aggregate verification caught a workspace-only
test dependency and a CDC test issuing its single-use close signal twice during
retry; both were fixed without adding a dependency or changing CDC runtime
semantics. The completed-window/shutdown fixture also passes 100 race-enabled
repetitions. These checks do not establish mixed-version or production capacity
acceptance.

Storage reclamation cleared the previously observed disk-space blocker against
the retained peak profile's preflight requirement above 46 GiB. The reusable
catalog's stopped container identities and retained mounts have been reverified,
but its missing private runtime wiring and terminal restoration receipts must
be recovered before reuse. Its native five-million-product count has not been
reverified. T098 remains pending on runtime/asset eligibility; preserve the
original thresholds, storage and history and recheck all launch gates before
starting a workload. No full healthy/fault/peak workload is claimed, and
production activation remains gated.

## Test-first matrix

| Area | Failures to demonstrate before implementing |
|---|---|
| Schemas/catalog | Unknown/legacy/deny fields; wrong scope/path; unrestricted unknown actions; generated Go/Rust drift |
| Store/projection | Partial batch; concurrent quota overflow; stale CAS; lost acknowledgement; status writes racing snapshots; CDC row reordering |
| Push/GraphQL | Same invalid candidate denied on both paths; actor spoof; multi-manifest self-grant; atomic multi-ref support or whole-push rejection |
| Delegation | Conditional caller grant is not universal proof; resourceNames narrowing; absent target needs bind; cluster authority cannot be inferred from current tenants |
| Snapshot consistency | Current Git tip with lagging projection cannot authorize validation; ordinary cache deliberately bypassed for review/write |
| Chosen race | Pause after validation capture, commit revocation in another repository, finish authorized write; a newly started validation sees revocation |
| Readiness | Cold boot, stalled CDC, cursor loss, overflow, partial list, source silence vs unhealthy heartbeat, concurrent snapshot replacement |
| Lifecycle | Termination revokes before final removal; role UID recreation; cross-tier reference claim vs role completion; leaked-claim recovery; errored lookup is not absence |
| Owner access | Staff-only creation; denied Customer/ServiceAccount; fixed template; ordinary collision; owner/admin tampering; duplicate reconcilers; delayed old-UID work |
| Bootstrap | No Git access means unready; crash after initial-admin commit; removal survives restart; immutable virtual built-ins |
| Review/watch | SubjectAccessReview version/kind validation, spec echo and status envelope; no persisted review or Node/list/watch lifecycle; reviewer permission and hypothetical groups (including empty vs omitted); namespace isolation; exact decision revisions; subscription revocation and membership changes |
| Compatibility | Old/current feature-disabled overlap; controlled provider switch; same-contract rolling replacement; safe refusal of obsolete static rollback |

Use real memdb and native Scylla contracts, fixed Rego decision fixtures,
race tests and bounded integration fixtures. In-process providers do not
prove deployed AuthN/AuthZ or process replacement.

## Healthy workload

### Local memory-accounting evidence

The provider reserves expanded role/action and subject indexes before allocation,
retains old-publication reservations until their pinned readers close, and
reclaims failed/discarded builds. The manager reserves 64 MiB of scratch before
source scanning, with at most four workers. Race suites cover aggregate pressure,
replacement/pin reclamation, cancellation and scratch-pressure readiness.

A local `darwin/arm64` benchmark on Apple M3 Pro uses ten roles (four Product
actions each) and fifty resolved one-subject bindings in one Namespace.
`BenchmarkProviderDeclaredNamespaceTranslation -benchmem -count=3` measured
459,648 reserved bytes and 132,169-147,820 retained heap bytes per tier, with
approximately 7 MB cumulative allocations per rebuild. Reservations are not
heap or RSS measurements. The earlier 32x allocation multiplier reserved about
1.84 MB for that tier and could not fit even this ordinary declared-count
fixture; the calibrated 8x structural-estimate multiplier retains more than
threefold headroom over the measured retained heap. A regression checks that
1,000 such reservations, four replacement tiers and two cluster publications
fit the 512 MiB budget.

This is bounded component accounting evidence, not the native 1,000-Namespace
dataset, maximum-manifest heap bound, process RSS, latency or sustained deployed
acceptance. Those gates remain pending.

Topology: two independently addressable API processes, two controller
processes with separate checkpoint volumes, shared Scylla and exactly one
Git process with retained storage. Verify topology and native source
revisions before collecting acceptance.

Dataset: 1,000 namespaces, 10,000 roles and 50,000 bindings, including
Staff personal-owner templates, namespace/group bindings and cluster-role
references. Maintain an independent expected-policy oracle and committed
revision map. Exercise both unconditional and conditional rule fixtures,
unresolved roles, built-ins and managed bindings.

Run for at least 30 minutes at 1,000 offered ordinary authorization
requests/s total (500 per API) while sustaining 50 admitted RBAC writes/min.
Rotate the working set across all namespaces, both granted and denied
identities and representative actions; report working-set distribution
rather than imply uniform coverage from a small repeated fixture.

Use 60% expected allows, 30% expected denials and 10% invalid/isolation
probes; infrastructure unavailability is a separate outcome, not a denial
count. Sustain canonical Git commits and GraphQL writes with at most four
concurrent writers. A push may change up to 256 manifests/4 MiB RBAC
bytes; exercise both small writes and maximum accepted batches. Rejected
admission is not counted as successful throughput.

Required gates:

- OPA provider-call p99 <=2 ms at declared dataset, without profiler overhead.
- Propagation to each API p95 <=2 s, p99 <=5 s from successful admission;
  no allowed decision beyond the configured 5 s revocation bound.
- Zero wrong policy decisions, zero privilege escalations and zero dropped
  scheduled iterations in healthy acceptance.
- No request-path full product scan; no unbounded queue or goroutine growth.
- Translated snapshots remain within 512 MiB budget per API; bounded
  64 MiB rebuild buffers and 1,024 dirty-tier queue; no silent allocation
  beyond caps. Report RSS/heap separately from budget accounting.
- Exact per-process SDK counters and epoch segmentation for restart runs.

Extend `make capacity TARGET=namespace PROFILE=watch` to verify RBAC
propagation, and `make capacity TARGET=api PROFILE=readiness MODE=alpha`
with explicit RBAC workload selection implemented in the existing driver.
Do not conflate these selectors with their present health-only behavior.

## Interference and larger-catalog gate

Reuse an eligible owned existing catalog-capacity fixture, with at least
5,000,000 products for a production-scale claim, without reseeding or
altering retained assets. Run the existing declared catalog peak push
profile concurrently with the RBAC workload, keeping that profile's native
payload/concurrency/throughput thresholds unchanged. Record the exact
profile configuration with the evidence; a tiny new RBAC fixture does
not prove sustained catalog capacity.

If no eligible asset/runtime is available, mark production-scale
interference acceptance pending; do not reduce it to alpha or claim this
feature resolves #451. This gate is implementation validation, not a
planning-time authorization to launch or mutate retained deployments.

## Fault and rolling workload

Repeat the declared 30-minute request/write workload with predeclared,
bounded fault windows and ownership verification:

1. Replace one API process; surviving replica remains correct and restarted
   process cannot serve authorization before snapshot/watch readiness.
2. Interrupt the target's journal delivery for less than, then beyond, the
   staleness bound. Verify fail-closed behavior, cursor-loss full resync,
   and recovery <=30 s after the source becomes available.
3. Replace one controller; retain its checkpoint volume without overlap,
   reconcile queued work and verify no duplicate managed bindings.
4. Roll compatible APIs/controllers one at a time; separately verify the
   provider-switch write freeze/drain and unchanged grants after cutover.
5. If changing Git hooks, stop the singleton Git process before starting its
   replacement against retained storage. Account for downtime; no Git HA claim.

Fault receipts prove injection/restoration only. The request oracle must
prove recovery and no revoked grants on either API. Account for every
unavailable request within declared windows; wrong decisions and dropped
iterations still fail. Never reuse an earlier source revision's results as
acceptance for untested code.

## Evidence and operator boundaries

Record source/build/config identity, topology, hardware, duration, actual
throughput, errors, propagation distributions, per-replica latency,
snapshot memory, queue/retry saturation and fault epochs. Retain private
credentials, runtime identifiers and raw profiles outside versioned docs.
Publish validated measurements, not experiment diaries.

Before any destructive fault/cleanup, verify explicit test ownership and
current terminal restoration receipts. Preserve unrelated deployments,
identities, key material, storage and retained capacity datasets. No
profiling, capacity deployment or fault execution is required by planning.
