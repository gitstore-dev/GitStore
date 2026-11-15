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

The local namespace/watch foundation now provides exact scheduled-offer
accounting, bounded pending/drain scheduling and an independent committed-vector
oracle. It requires predeclared expected decisions, distinguishes infrastructure
failures, and retains intermediate grant/revoke transitions. Local topology,
private-file, epoch-counter and fault-window regressions do not replace live
native counts or ownership probes. `CAPACITY_RBAC_WORKLOAD=1` currently fails
explicitly rather than silently dispatching health-only evidence; it is not a
supported deployed workload selector yet.

Independently verified source variants of the same admitted cluster/Namespace
Git vector retain the original admission time and expected cases. They cannot
restart propagation clocks or reinterpret expected decisions. Late registration
still checks every later admitted transition, including intermediate revocations;
matching Git revisions alone never authorize an unknown content digest.

New native seed receipts retain each of the six bounded import commits and its
client-observed successful-push acknowledgement time before remote-ref or
per-replica readback. Invalid, repeated or reordered records cannot rewrite
that history. Historical receipts without these clocks cannot supply
propagation measurements. This is clock plumbing, not the live mutation driver
or a propagation-latency result. Local topology validation also accepts a shared
multi-peer Scylla cluster while retaining exact API/controller counts, singleton
Git, ownership and independent-checkpoint requirements.

Bounded native seeding now passes a diagnostic with two Namespaces, twenty Roles
and one hundred bindings, including the personal-owner template. Both APIs
verify exact authored policies, unique UIDs and resolved Role links. Four real
Staff principals exercise ordinary Product list requests for unconditional,
group-conditioned and built-in grants, plus unresolved-role and unbound-subject
denials. Shared per-replica readback pacing respects the existing administrator
complexity budget rather than disabling admission. Transient authorization
unavailability occurred during seeding; the successful convergence checks are
not propagation-latency or availability acceptance. Full native scale and the
deployed committed-vector oracle remain incomplete.

The owned native fixture can now capture canonical source candidates before
provider preparation, without a public endpoint or a production configuration
switch. Private, exclusive, bounded record streams are independently hashed;
the existing namespace/watch harness checks complete models against authored
policies, native counts and immutable UIDs. The two-Namespace diagnostic passes
these checks on both APIs and the five ordinary-principal probes. Actual ordinary
Product decision vectors also match captured source models. Authoritative
write-validation captures are separate and must not be mistaken for serving
snapshots. This diagnostic observation path is not a completed deployed
committed-vector oracle or uninstrumented performance evidence.

Source verification also checks canonical nested role-reference generations and
immutable Namespace Role UIDs, not just the GraphQL readback shape. The diagnostic
harness can predeclare ordinary request IDs and join exact subjects, actions,
Namespaces and expected outcomes to both captured policy tiers. Regressions reject
missing/duplicate receipts, changed identities or verdicts, unknown digests and
cross-tier substitutions. Both sealed replay and fresh native execution pass:
the one-writer two-Namespace fixture and a four-actually-concurrent-writer
four-Namespace fixture verify five predeclared client traces on each API.
A further fresh native diagnostic pins the root Namespace and bootstrap binding
incarnations independently through both APIs. Captured root models must match
the authored initial-administrator subject, exact grant and canonical policy-tree
digest; additional root policy or replaced incarnations are refused. Uninitialized
or unresolved root candidates cannot supply a probe's policy vector. This proves
the fixed bootstrap fixture, not general root-policy mutation or the dynamic
committed-policy history still required for T090-T092.

Four concurrent seed writers still exceed the strict per-replica readback bound,
including with production-default resync/checkpoint settings. A full-size
two-writer attempt stopped after twelve complete Namespace receipts and one
partial receipt. The one-writer full-size attempt also failed, after fifty-one
complete Namespace receipts and one partial receipt. Both encountered pending
binding reconciliation and authorization unavailability. Backend evidence
includes tier revision conflicts and projection-not-ready errors, but does not
establish a single underlying cause. These remain failures, not native-scale
acceptance. Neither admission limits nor the readback deadline were raised.

Staged seeding now imports ten manifests per admitted Git commit and verifies
both APIs before the next batch. It preserves the exact final geometry, authored
policies, protected owner slot and immutable UIDs across commits. The one-writer
and four-writer diagnostics pass with the unchanged thirty-second per-API bound
for every batch. This changes import shape, not the failed bulk-commit results
or the healthy workload gates. The larger four-writer staged diagnostic failed
after forty-seven complete Namespace receipts and one partial receipt, again at
the unchanged per-API bound with authorization unavailable. Its source captures
were well below their byte limit. Offline preparation of its complete source
models does not reproduce live allocation exhaustion; no single underlying cause
is established. Full staged scale remains unverified. Six paced
readbacks per Namespace also require a whole-fixture lifetime above the former
six-hour choice; that lifetime is separate from every convergence and acceptance
deadline.

A one-writer medium diagnostic subsequently passes all sixty-four Namespaces,
640 Roles and 3,200 bindings in 3,214 seconds, including both-API policy/UID
readback, independently authored root verification and ordinary client traces.
It used the pre-rule-sharing provider. A fresh four-writer diagnostic against
the merged shared-rule provider passes four Namespaces, forty Roles and two
hundred bindings in 100.27 seconds with the same source/root/trace checks.
Both have complete terminal receipts, no forced stops, closed owned listeners
and an unchanged foundation. These results establish their respective scopes,
not the earlier failure's cause, one-thousand-Namespace execution, sustained
four-writer capacity or healthy/fault acceptance.

A subsequent full-count attempt with one writer and 128 real Staff principals
failed in its first Namespace after 41.45 seconds: authorization remained
unavailable beyond the unchanged per-API readback bound. A bounded two-Namespace
control with that subject pool and source observation disabled passes, so those
settings alone do not reproduce the failure. The failed fixture is preserved;
all owned listeners closed without forced stops and the foundation is unchanged.
No single underlying availability cause or full-scale pass is established.

## Current verification boundary

T061 passes on the owned native Scylla/Kratos deployment: both APIs deliver
ordinary Product pushes on their original subscriptions and enforce revocation;
isolated real API processes verify cold failure, reconstruction, source outage,
same-process recovery and denial after removal. Both GraphQL and native Git USER
creation verify AuthN-selected ownership, admitted owner status, both-API
namespace isolation and exact incarnation finalization. The fresh native
T064/T073 functional owner matrix now passes, including prior-state collisions,
GraphQL/signed-Git catch-up, Namespace recreation and distinct authenticated
subject/directory username coverage. This does not establish sustained-load or
rolling-upgrade acceptance.

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

A private launcher's fixed disk-space threshold is not a repository or user
gate. Use canonical validation commands and diagnose measured resource failures
without broad cache/container pruning or retained-data deletion.

The reusable catalog has passed authoritative native counting above five
million Products and cold recovery with its schema-compatible baseline images:
three Scylla peers, two genuinely ready APIs, two credential-ready controllers
with separate retained checkpoints and one Git process. Original signing
material and migration history were preserved. Private recovery wiring and
receipts retain the operational details. Final post-recovery backup and full
restore proofs passed before the nine verified historical source volumes were
retired. This does not classify unrelated development volumes as duplicates.

This storage-recovery evidence does not establish a current RBAC image/schema
upgrade or T098 peak acceptance. T098 remains pending on the feature runtime,
native RBAC dataset and unchanged concurrent peak workload. Preserve the
original thresholds, storage and history and recheck actual launch prerequisites
before starting a workload. No full healthy/fault/peak workload is claimed, and
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

A local `darwin/arm64` benchmark on Apple M3 Pro now uses the seeded conditional,
multi-resource policy mix with group, built-in and unresolved bindings rather
than the earlier simplified four-action fixture. Sharing each authored rule
across action indexes measured 526,912 reserved bytes and 124,812 sampled retained
heap bytes per tier, with approximately 7.16 MB cumulative allocations per rebuild.
Reservations are not heap or RSS measurements. The eightfold structural-estimate
multiplier and logical expansion bound are unchanged. The representative
regression checks that 1,000 reservations, four replacement tiers and two cluster
publications fit the unchanged 512 MiB budget; it failed before rule sharing.
See [research](../research.md#representative-translation-reservation-probe) for
the before/after methodology and measurement limits.

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
