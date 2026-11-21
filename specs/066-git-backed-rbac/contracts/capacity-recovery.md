# Validation, Capacity and Recovery Contract

## Delivery scope (owner decision 2026-11-21)

Feature delivery is correctness-first. The unchanged sustained performance
and production-capacity requirements below move to #451; they are not
passed, waived or deleted. This scope decision supersedes earlier statements
that every pending capacity gate blocks feature closure, including the
2026-11-20 decision's broader closure wording. Historical measurements and
failed attempts keep their original classifications.

| Acceptance | Feature closure | Production acceptance in #451 |
|---|---|---|
| Authorization, isolation, privilege containment, exact revision/UID truth | Required; no wrong grants or false denial-as-recovery claims | Still required at declared load |
| Native store, two API/controller replicas, rolling upgrades | Required on the actual implemented contracts | Still required during sustained load |
| Freshness, revocation and bounded work | Configured security/resource bounds remain enforced; no stale grants, unbounded queues or product scans | Measure the full declared envelope without relaxing caps |
| Functional faults and recovery | T097: actual domain recovery, surviving-peer correctness and <=30 s recovery after source restoration | Original thirty-minute fault-load/rate/percentile workload remains pending |
| Healthy sustained capacity | T096 deferred, not passed | 1,800-second mutation / 1,860-second ordinary window, 1,000 requests/s, 50 RBAC writes/min and original gates |
| Percentile optimization/acceptance | Not a feature blocker; configured revocation bound is not a percentile waiver | Decision p99 <=2 ms and propagation p99 <=5 s; p95 <=2 s remains a reported non-blocking goal |
| >=5,000,000-Product peak-push interference | T098 deferred, not passed | Eligible retained catalog plus unchanged peak-push and RBAC gates |

T103 can close the feature only after all non-deferred correctness proofs,
including T097, pass. It must explicitly leave production acceptance and
activation unclaimed. T093 injector/stand-in receipts are not native domain
recovery. The native functional matrix below completes T097. Final source
validation passes, completing the explicitly correctness-only T103 closeout
on the implementation branch. All 100 non-deferred tasks are complete;
T096/T098 remain deferred and T102 remains retired.

The full default `make pr-ready`, separately serialized `make check TARGET=all`,
focused API auth/OPA and integration proxy race suites, generated-drift checks
and AST refresh pass with implementation source `e93069b1`. Native evidence
retains its exact sources below. Closing this implementation branch does not
silently check unrelated unpublished tasks on main: the independent native
recovery PR carries its own main-based proofs and leaves main's remaining
integration closeout pending. No production activation, readiness, sustained
capacity or unrelated ADR/issue completion is claimed.

## Main-reconciled executable verification (2026-11-21)

Executable source `74862cb6` preserves main's composable runtime roles and
identity-query conventions alongside the completed RBAC implementation.
Fresh native builds record clean source, binary hashes and independent
API/controller baseline identities; earlier receipts retain their original
source attribution. Full default `make pr-ready`, separately serialized
`make check TARGET=all` and the AST refresh pass after the acceptance-helper
repair. Subsequent evidence-only documentation does not change those
executables.

`TestRBACNativeJournalRecovery` passes in 9.19 seconds, with 561.014 ms domain
recovery after physical CQL restoration. It exercises below/above-bound
interruptions, cursor expiry, complete-source resync, surviving-peer
revocation and a physical revoked-grant API restart under the unchanged
five-second freshness and thirty-second recovery bounds.

The diagnostic rolling/cutover matrix passes in 51.50 seconds from implemented
baseline `67a9220d` to `74862cb6`, using distinct API and controller binaries.
It verifies seeded canonical parity, write freeze/drain, snapshot-ready
traffic admission, owner catch-up, compatible physical replacements and
off-traffic refusal of stale static-policy rollback. Owner authorization
recovers on both APIs in 25.062 seconds after controller replacement.
Both successful matrices stop normally, close every owned listener and
preserve the database process, foundation services and retained datasets.

This is implemented-version diagnostic compatibility, not certification of
an OPA upgrade from production main. A separate attempt from gated main
`57459dc5` fails the healthy-peer requirement with HTTP 503 during replacement;
its failure evidence is retained and is not counted as a pass. Production
OPA remains disabled, and the historical feature-disabled release/migration
proof is separate from this native diagnostic matrix.

The rolling rerun also verifies a repaired acceptance helper: canonical
author YAML intentionally has no datastore-owned UID, so pending-creation
readback is joined to its exact committed Git revision instead. No
freshness, recovery, parity or healthy-peer assertion was relaxed.
T096/T098 and sustained fault-load acceptance remain pending in #451.
Publishing these layers is not equivalent to their integration into main.

## Native directory-lease boundary acceptance (2026-11-21)

The remaining native proof for #588 passes on its independent exact source
`279c7fd3`, with API, controller and Git binaries rebuilt from that source.
Two APIs, two native controllers, singleton Git, Scylla and real Kratos
exercise the unchanged five-minute directory lease for 312.071 seconds.
The 29,016 primary authorization probes and additional replacement-permission
checks verify both APIs discard expired old-group grants, admit the current
replacement group's permission, explicitly reject suspended profiles, fail
closed during a targeted backing-directory outage and recover the same
session tokens after restoration. There are zero wrong decisions and zero
healthy-directory unavailability outcomes.

Backing-directory traffic independently confirms the actual refreshes and
outage. Created identities are removed, processes stop without forced
termination and the database process, foundation services and retained
datasets remain unchanged. Auth/Kratos/security race regressions also pass;
the existing exact-head full default readiness pass and twenty green CI
checks remain attributable to #588. Its enrichment and regression-test
sources match the implementation branch. The cluster-scoped probe avoids
depending on #594's separate cold-Namespace fix.

This closes the directory-freshness correctness gap, not sustained-load
acceptance. T096/T098 and the original sustained fault-load gates remain
pending in #451; no throughput, percentile or production-readiness pass
is inferred.

## Native functional fault/recovery acceptance (2026-11-21)

T097 passes on owned native infrastructure, not HTTP stand-ins. On source
`b392787a`, `TestRBACNativeJournalRecovery` passes in 9.37 seconds with
806.658 ms domain recovery after connectivity restoration. The independent
main-based source `0fdd4893` also passes in 9.63 seconds with 774.441 ms recovery.
Both use two healthy APIs, two native controllers, one Git process and an
additional isolated API behind a bounded loopback CQL proxy.

The fresh disposable RF1 keyspace uses four-second journal retention and the
unchanged five-second OPA freshness bound. A 250 ms interruption closes real
connections; a second interruption exceeds the freshness bound with an
independent twenty-second safety restoration. Healthy peers preserve grants
and enforce revocation. The isolated API becomes unready and returns
unavailability even for the trusted administrator. Restoration requires a real
`WATCH_EXPIRED` / `RETENTION_EXPIRED` cursor rejection, complete sealed root
and Namespace source models captured after restoration but before recovery,
and a correct deny at independently read canonical Git revisions. The revoked
binding is absent or terminating in those models. A physical API restart
reconstructs the same denial rather than restoring the grant.

The proof exposed a real cold-load defect: request-scoped serving captures
did not request missing Namespace snapshots. A failing-first provider
regression and the native test pass after routing capture failures through
the same bounded loader as ordinary requests; missing state still fails closed.

Controller recovery is measured separately through actual owner authorization
on both APIs, not a responsive PID. On `e93069b1`, the existing GraphQL,
signed-Git and Namespace-recreation controller-replacement scenarios recover
in 22.208, 13.320 and 15.169 seconds after fault restoration. The independent
main-based source `eaa01fa6` passes the same scenarios in 22.298, 14.468 and
15.638 seconds. Each enforces <=30 seconds through retained-checkpoint
replacement and actual owner decisions, with existing UID/isolation and
duplicate-binding assertions preserved.

Every successful run closes its owned listeners without forced stops and
preserves the database process, foundation services and retained datasets.
These bounded, source-attributed functional proofs do not pass T096/T098,
thirty-minute fault-load acceptance, throughput or percentile gates, and do
not enable production OPA or establish production readiness.

The latest full healthy attempt on source `9c368084` passed the root preamble
and acknowledged 67 Namespace writes before one ordinary k6 pool exhausted
its configured 200 VUs at 500 iterations/s. The run has no final k6 summary:
it does not establish exact final HTTP drops or a provider/propagation
percentile failure. Scheduled companion writes abandoned after cancellation
are not HTTP failures. A later bounded trigger-based diagnostic failed in
the root preamble and captured no traces. Neither proves a hardware limit
or a causal bottleneck, and neither earns a capacity pass.

## Runner integration status

Runner integration (T092) is complete. Both existing public selector families
dispatch to the bounded RBAC driver with owned preflight, exact workload
accounting, independent source/decision joins and mandatory sealed alpha
postflight. Capacity-dispatch regressions and all `TestRBACCapacity` race
contracts pass on source `463149d4`; the Go suite completes in 125.380 seconds.
Production selection remains refused.

These checks verify runner behavior, not a thirty-minute healthy or fault
workload or large-catalog interference. Those capacity requirements remain
open in #451, while T097 retains native functional recovery. The separate
quickstart-validation task T102 is retired, not passed. Neither runner
integration nor scope deferral closes the feature or establishes production
readiness.

Root preamble readbacks group at most four typed GraphQL fields per request
on each API. Every resource still requires its own identity, generation, full
specification or owned-termination result; errors remain scoped to their
individual aliases. Requests share the original preamble context, and batching
does not change admission clocks, source joins or acceptance thresholds.

## Earlier acceptance evidence (before the 2026-11-21 scope decision)

**Owner decision (2026-11-20):** the 2-second propagation p95 is a reported
optimization goal, not a delivery blocker for this feature. The measured
3.25/3.12-second maximum-root result is accepted on that basis, not relabeled
as meeting 2 seconds. The 5-second p99 and per-observation/revocation bounds
remain enforced, as do decision latency, zero-error/drop accounting, resource
limits, full workload durations and fault recovery. This feature-specific
decision does not change the independent contracts or complete the outstanding
production-capacity work tracked in #451. Earlier failed evidence below
retains its original classification and thresholds.

The unprofiled bounded diagnostic on `e64b3050`, including revision
acknowledgements and batched root readbacks, completes all eight root phases,
twenty Namespace admissions and four forward restorations. Its 84-second
ordinary window completes 84,000 HTTP requests with zero wrong, unavailable,
dropped or failed requests. The Namespace mutation window is only 24 seconds,
and maximum-root creation takes approximately 3.25/3.12 seconds across the
APIs with a 200 ms maximum journal poll interval. This is not a thirty-minute
acceptance pass and does not meet the non-blocking root propagation p95 goal.
Independent cold verification confirms root withdrawal and carried Namespace
denials without further policy mutations; retained failed evidence is unchanged.

An alpha attempt on `2a424c91` passes live preflight, complete independent source
readiness and all eight root phases, but stops after 180 admitted Namespace
writes when a 2.57-second Git admission fills the serialized writer's bounded
queue and an original offer is missed. Four forward restorations and independent
cold withdrawal/denial verification pass. The shortened ordinary window has no
complete k6 summary and earns no healthy-acceptance credit. The client fixture
still forced `noop` fetch negotiation despite the common-history ACK correction
already merged in #544. It now uses consecutive negotiation; a packet-trace
regression proves the working clone advertises its known parent while retaining
the exact acknowledged-parent checks. This correction does not change write
rates, pending capacity, admission serialization or propagation clocks.

An unprofiled diagnostic on `c0037bb0` subsequently completes all eight root
phases, 250 Namespace writes and 360,000 ordinary HTTP requests, with zero
wrong/unavailable/dropped outcomes or HTTP failures. Independent cold
withdrawal/denial verification passes. Its final replay still fails because
the oracle retains only 64 admissions per Namespace, fewer than the declared
workload requires. Replay now shares the existing 1,508-admission envelope
(1,500 writes plus four baselines and four restorations), preserves original
alias clocks at the bound and rejects overflow. A red/green regression and all
local RBAC race tests cover this correction. The diagnostic remains failed
evidence, not a thirty-minute acceptance pass; its final SDK capture does not
establish snapshot-budget exhaustion as the cause of earlier unavailable
admissions. No memory cap or workload gate is relaxed.

The unprofiled comparison on `9fb617b5` completes all eight root mutation
phases and their independent source pairs, followed by all twenty bounded
Namespace admissions and four forward restorations. It still fails healthy
acceptance: k6 drops 93 iterations and maximum-root propagation reaches
approximately 4.27/4.17 seconds across the APIs. No wrong ordinary decisions,
authorization-unavailable responses or HTTP failures are observed in that run.
Withdrawals now consume change slots but not the proposed-manifest byte budget;
the 256-change limit, individual validation and 4 MiB proposed-content bound
remain intact.

On `44988e92`, initial Product listing windows use one bounded cross-shard CQL
request instead of 64 separate requests. A read-only before/after regression
confirms that request count; live Scylla pagination race contracts and the full
default PR suite pass. This does not establish sustainable throughput.
The fresh unprofiled comparison completes all eight root phases but reaches
approximately 3.44/3.58 seconds at maximum-root creation. Ordinary requests then
receive HTTP 503 during physical Role deletion after the final withdrawal;
cluster-origin expiry, revision conflicts and projection-not-ready errors are
observed in that window. The writer barrier refuses all Namespace admissions.
The early abort leaves no complete k6 summary, so zero drops cannot be claimed.

A final-withdrawal trace diagnostic on the same core source completes all
84,000 ordinary HTTP requests with the exact 60/30/10 outcome split and zero
wrong, unavailable, dropped or failed requests. All twenty Namespace admissions
and four forward restorations complete. Maximum-root propagation still reaches
approximately 4.38/4.40 seconds, and late static-probe replay fails because it
mistakes later dynamic root sources for bootstrap sources. The verifier now
pins that replay to the original static phase's source-capture cutoff; the
regression reproduces the original error and passes with phase isolation.
Dynamic history replay and strict source validation remain independent.
This instrumented diagnostic does not establish sustained acceptance or erase
the preceding unprofiled availability failure.

A subsequent unprofiled run with phase-isolated replay completes all eight root
phases, but fails earlier during Namespace observation: four offers complete,
sixteen drop and one is unavailable. The ordinary process is aborted without a
complete summary. Maximum-root propagation reaches approximately 4.55/4.69
seconds. This does not establish deployed success for the verifier correction.

On `d64baf96`, finalized, already-terminating Role purges preserve prepared
authority only when complete consecutive headers, unchanged projection and
exact deletion-repair evidence prove policy equivalence. Relevant race suites,
tagged live-Scylla RBAC contracts and the full default PR suite pass.
The unprofiled comparison completes all eight root phases, with maximum-root
propagation approximately 3.16/3.60 seconds. Sixteen Namespace writes are
acknowledged before an original scheduled offer is missed; accounting records
seventeen completed offers, three dropped and one unavailable. The missed offer
cancels a pending observer, so this is not proof of an ordinary HTTP 503.
The aborted ordinary process has no complete summary.

The serialized companion previously fetched objects and merged its working
clone after every GraphQL acknowledgement, before starting its observer.
Canonical readback now reads only the remote ref within the original deadline.
Subsequent Git authoring synchronizes to its exact acknowledged parent;
restoration fetches that parent before creating an isolated worktree.
Regressions cover remotely authored objects absent locally, forward-only
admission/restoration, malformed refs and changed-parent refusal.
Rates, pending capacity, admission serialization and propagation clocks remain
unchanged. A subsequent instrumented run exercises this correction through
seventeen acknowledged Namespace admissions, but fails before completing the
schedule; deployed healthy acceptance remains unverified.

Subsequent native comparisons fail before Namespace admissions: an unprofiled
run observes real authorization-unavailable responses during root transitions,
and correctly attributed maximum-root diagnostics exceed the original
five-second propagation bound. Live CQL/origin-fact waits are substantial;
aggregate blocking totals are not request latencies or sole-cause proof.
The new bounded live-fact reader replaces up to 160 CQL reads per 32-Namespace
check with four bounded queries against the same authoritative and mapping
tables. Forward/reverse consistency, lifecycle visibility, exact identity
association, uncached Git proofs and freshness/publication fences remain
required. Focused race, isolated live-Scylla contracts and the full default PR
suite pass. A fresh unprofiled comparison confirms the bulk path is active with
no recorded bulk-read errors, but maximum-root creation still exceeds the
original five-second bound before any Namespace admission. A separately
instrumented, canonical-revision-joined maximum-root trace reproduces that
failure with lower live-origin waiting; it does not establish a sole remaining
cause. Independent cold observation proves safe withdrawal without new Git
operations. Sustained acceptance remains unverified.

The maximum-root worker timeline also shows synchronous source-evidence writes
on the pre-publication path throughout the failed window. Source-frame export
now coalesces writes through a bounded 64 KiB buffer, flushing the complete
begin/resource/end frame before returning. It does not defer evidence past
publication, omit records, change capture clocks or weaken sticky failure,
quota and sealing checks. Focused race regressions verify full-frame readback
before close, fewer underlying writes and final-flush/cancellation failures;
the full default PR suite passes. A fresh unprofiled comparison observes
maximum-root creation in 4.750/4.603 seconds across the two APIs, rather than a
five-second timeout, but this is one observation per API, not a percentile or
sole-cause result. The ordinary stream exhausts its unchanged 200 VUs per API
during that transition, so the healthy-stream gate correctly refuses the next
admission. Initial root grant also takes 2.011/2.014 seconds. No Namespace
admissions or sustained acceptance are established. Independent cold observation
certifies automatic withdrawal without new Git operations; the failed workload
remains failed.

An instrumented comparison on the same core source completes all eight root
phases and seventeen scheduled Namespace admissions. Maximum-root creation
takes 3.666/3.577 seconds. The next GraphQL offer is unacknowledged, concurrent
observations are cancelled and accounting records twenty offers, eighteen
completions, two drops, one unavailable and zero wrong decisions. The original
failure remains intact. Independent canonical-ref, exact-identity, source and
cold-denial checks on all four Namespaces and both APIs prove the uncertain
grant did not commit and verify the acknowledged forward restoration. This is
cleanup certainty, not a successful schedule or ordinary-availability result.

A supported journal-poll maximum override of 250 ms in a subsequent unprofiled
comparison reduces the initial grant observation to 1.704/1.757 seconds, but
maximum-root creation still takes 4.746/4.574 seconds and ordinary k6 again
exhausts its unchanged 200 VUs per API. No Namespace admissions occur.
Independent cold observation certifies withdrawal. The override is not adopted
as a default or established as a solution.

Single-attempt GraphQL refusal receipts now retain the HTTP status and an
allowlisted error-code classification, including mixed or unclassified errors.
They never copy response messages, extensions or unknown codes into the receipt.
Focused scheduler regressions verify redaction, unchanged failed-admission
clocks and exactly one HTTP submission. Historical receipts without that
classification cannot establish the precise admission error retrospectively.
A subsequent instrumented run again completes all eight root phases but fails
after only one acknowledged Namespace admission. The failed GraphQL offer
records HTTP 200 and an unclassified error, not a transport failure or a proven
CAS conflict. The classifier now recognizes the API's actual
`AUTHORIZATION_UNAVAILABLE` code. Focused race regressions pass after that
correction; the original unclassified failure is not rewritten or relabeled.
Independent cold canonical, exact-source and denial checks on both APIs
subsequently verify all four restored Namespace baselines and prove that the
uncertain grant did not commit, without another policy write.

Failed workloads remain failures. Observation-only runtime replacements
allow already-admitted lifecycle deletions to drain without new Git policy
writes. Independent terminal canonical/source/denial checks then verify the
bootstrap-only root and restored Namespace baselines. Prepared sources may
retain already-denying terminating rows after physical purges; an exact
bootstrap-only terminal proof must still refuse those sources. A fresh cold
capture after draining passes without new Git policy writes. Root propagation,
post-withdrawal availability, the sustained healthy/fault families and the
large-catalog interference gate remain open; alpha/production activation and
feature closure remain refused.

## Existing command interface

Use existing `make test`, `make lint`, `make check TARGET=all`,
`make generate TARGET=all`, `make pr-ready`, `make capacity` and
`make chaos` families. Do not replace full default PR validation with a
capacity run. Datastore contracts run with `make test TARGET=datastore`;
native Scylla uses its existing DATASTORE/address selectors and an owned
isolated keyspace.

The current `namespace/watch` and `api/readiness` drivers support bounded
owned RBAC diagnostics, not the complete sustained feature envelope. A health-only readiness smoke run
is not an RBAC acceptance result. Document supported selectors with the
implementation rather than publish invented flags here.

The local namespace/watch foundation now provides exact scheduled-offer
accounting, bounded pending/drain scheduling and an independent committed-vector
oracle. It requires predeclared expected decisions, distinguishes infrastructure
failures, and retains intermediate grant/revoke transitions. Local topology,
private-file, epoch-counter and fault-window regressions do not replace live
native counts or ownership probes. `CAPACITY_RBAC_WORKLOAD=1` selects the
connected native driver only in diagnostic mode; alpha/production remain
explicitly refused rather than silently dispatching health-only evidence.

The existing k6 readiness profile now has an ordinary RBAC request path and
the native namespace/watch fixture can execute it with an independently
authored private working set. Two direct API scenarios preserve exact declared
HTTP offers even when k6 emits an extra boundary iteration. A connected
six-second proof passes 180 ordinary requests per API: 108 allowed, 54 denied
and 18 owner-isolation denials, with zero wrong, unavailable or dropped
decisions. Every receipt joins a completely verified root/Namespace source
vector captured before the decision, within the actual k6 process window.
The independent readiness verifier recomputes the result from sealed counts,
authored cases, source streams and actual k6 counters, rather than accepting
an aggregate helper success flag. Later Namespace/root mutation sources cannot
rewrite the earlier load phase. Native companion requests use a separate
correlation namespace. This remains static diagnostic evidence; the public
sustained write/propagation companion and full
gates remain pending.

The public diagnostic now runs through the existing capacity dispatcher,
readiness preflight and namespace/watch Make runner. Its protected deployment
declaration is checked against live process epochs, executable hashes,
direct API/controller health, native Git listening, owned Scylla and independent
controller checkpoints. Configured geometry is not treated as verified native
counts. A fresh public `api/readiness` run passes in 52.87 seconds with two
complete imports, twenty Roles, one hundred bindings, all 360 actual k6
outcomes and all eight Namespace/root mutation phases; sealed independent
replay and unchanged post-workload process checks also pass. An independently
fresh public namespace/watch run passes the same complete diagnostic in
52.20 seconds, including sealed replay. An earlier
preflight failure confused the API smart-HTTP endpoint with native Git health;
the corrected declaration separates the smart-HTTP URL from native gRPC
listening. Failures remain preserved, and storage/process health is unchanged.
This does not close native scale, mutable-policy or sustained
acceptance requirements.

The existing source recorder is now shared runtime code, selected by the
optional private `api.auth.opa.evidence_directory`. It exclusively creates
bounded canonical-source and unsampled decision streams, preserves capture
clocks and sticky failures, serializes concurrent writes and seals after
runtime shutdown. Normal operational-log sampling is unchanged. Capture
selection changes require a controlled restart, and default/production
startup refusal is unchanged. A fresh public diagnostic using only the
configured runtime producer, with no test snapshot injection or sampling
override, passes all native/k6/mutation/source phases in 57.84 seconds.
Independent replay also passes after every owned process has stopped.
Race tests cover concurrent receipt completeness, privacy/exclusive creation,
failed-stream preservation, constructor/reload behavior and the unchanged
production gate. This establishes portable evidence production, not the
remaining mutable-history or sustained acceptance envelope.

The root diagnostic now additionally authors a custom unconditional ClusterRole
and its binding, revokes/restores its list rule, and removes both through signed
Git admission. Source verification independently canonicalizes the changed
role tree, pins Role generations one/two/three and retains binding generation
one and its originally resolved reference generation. Built-ins remain unchanged.
A later newly unresolved root grant can retain its previously authored deny
truth only when no earlier live authored reference is reinterpreted; removed
incarnations must remain unchanged. Test-first tree identity and generation/
reference regressions pass under race, as do the original pending-source guards.
A fresh public diagnostic passes in 67.96 seconds with all twelve Namespace,
root-binding and root-rule phases plus every native/k6 source join.
Its first attempt passed the native mutations but failed the verifier's obsolete
first-root-creation-only assumption; that failure is preserved. Owned shutdown
and datastore health/storage remain unchanged. This is bounded unconditional
rule-history evidence, not conditional root-policy or sustained acceptance.

The public verifier now seals and independently replays all twelve dynamic
Namespace/root phases after shutdown, rather than accepting in-process
aggregate success. Named private receipts retain authored Namespace history,
individual HTTP probe clocks and two separately bounded root probe stages.
The verifier reconstructs exact mutation declarations from the seed plan and
requires complete phase/replica evidence, original admission clocks and
source/decision joins. Later root captures are excluded from the preceding
fixed-root Namespace phase using their original capture clocks. A corrected
fresh public diagnostic passes in 58.54 seconds; post-shutdown replay and
persisted-evidence missing-probe, altered-generation and omitted-stage refusal
checks pass. The earlier capture-scope failure remains preserved. Sustained
mutation rate, conditional/general histories and percentile gates remain open.

The short concurrent companion now has a native pass on final source. A
separately enrolled holdout prevents changes to ordinary 60/30/10 intent;
one serialized writer rotates total50/minute binding-subject mutations across
four owned organization tiers. Original admission clocks, per-tier generations
and immutable references survive independent source/decision replay. The
88.30-second public run admitted all10 writes and completed1,440 ordinary
requests without wrong/unavailable/dropped outcomes, restored two outstanding
grants and passed replay after shutdown. Failed-k6/evidence/assertion cleanup
is bounded and idempotent; redacted failed-response capture cannot change gate
accounting. Earlier failed runs and their unproven HTTP-error cause remain
preserved. Exact recovery of earlier fixture grants advanced only owned
revisions/generations and independently verified denial. This is not proof of
a single hot tier, four concurrent writers, general rule/root mutations, full
native scale or the sustained acceptance envelope.

The explicit isolation-principal cohort now also passes a fresh public
`api/readiness MODE=diagnostic` run with eight enrolled readers, eight separately
enrolled groupless visitors and a disjoint mutation holdout. Five Namespaces,
fifty Roles and 250 bindings pass both independent source joins. During the
36-second ordinary window, both APIs complete 1,080 offers each with the exact
648/324/108 allow/deny/isolation mix and zero wrong, unavailable or dropped
decisions. Twenty conditional-Role admissions run at 50/minute across four
organization tiers; four forward-only restorations recover the original rules.
Independent sealed replay passes after all owned processes stop. The original
database process and retained data are unchanged. This validates real visitor
enrollment and credential rotation without a quota exemption, not sustainable
full-rate capacity or completion of the remaining acceptance gates.

The mixed-transport companion also passes a fresh public diagnostic on source
`1d93a7bf`: twenty conditional-Role admissions comprise ten native Git pushes
and ten CAS-protected GraphQL updates, rotating both transports through all
four tiers and both APIs. The 36-second ordinary window completes all 2,160
offers with the exact 60/30/10 mix and zero wrong, unavailable or dropped
decisions. Original GraphQL response clocks precede canonical-ref fetches and
propagation checks. All four forward-only restorations and independent sealed
source/decision replay pass after shutdown. The complete default PR-readiness
suite, capacity race contracts, vet and canonical lint pass; singleton Git,
independent controller checkpoints and the original database process are
preserved. This closes the missing mixed admission-path prerequisite, not the
thirty-minute or maximum-batch acceptance gates.

On source `efc5348f`, complete revision-pinned propagation replay now covers all
twenty mixed admissions independently on both APIs, including replay after
shutdown. The five-Namespace diagnostic completes all 2,160 ordinary offers
with the exact 60/30/10 mix and zero wrong, unavailable or dropped decisions;
all four forward-only restorations pass. Original-ACK-to-matching-HTTP
propagation p95/p99 is 2.166366/2.189851 seconds on API A and
2.519819/3.095379 seconds on API B, including adaptive sampling delay. Both
p95 results exceed the unchanged two-second target: this is complete
diagnostic coverage, not healthy capacity acceptance. Exact-vector attribution
also identifies delayed publication after earlier source captures, so sampling
alone does not explain the gap. Prior failed attempts remain preserved.
Independent replay also joins the original integer-nanosecond provider-call
measurement for every ordinary decision, not a sampled subset. On that same
source and diagnostic window, the 1,080 observations per API yield nearest-rank
p99 of 839.833 microseconds on A and 865.458 microseconds on B, with maxima
of 1.372250 and 1.426042 milliseconds. These are server-side provider calls,
not HTTP latencies; they do not establish full-scale or sustained p99.

Independently verified source variants of the same admitted cluster/Namespace
Git vector retain each tier's original admission time and expected cases. Each
tier also retains its first independently acknowledged supersession deadline:
a newer companion tier or late source alias cannot refresh an old component,
including when its expected verdict is unchanged. Future, reordered or
reinterpreted component admissions are rejected atomically; matching Git revisions
alone never authorize an unknown content digest. Mixed-tier regressions fail on
the former composite clock and pass with independent clocks under race detection.
The native diagnostic independently declares root binding grant/revoke/restore
and removal histories as well as Namespace histories. The extended hook also
verifies custom unconditional ClusterRole rule histories; mutable conditional
root policies and sustained workloads remain unverified.

Native seed receipts retain each declared bounded import commit and its
client-observed successful-push acknowledgement time before remote-ref or
per-replica readback. Invalid, repeated or reordered records cannot rewrite
that history. Ten-manifest diagnostics retain six imports; complete fixture
imports retain one acknowledgement for 59 or 60 changed manifests, with the
protected USER-owner binding unchanged. Historical receipts without these
clocks cannot supply
propagation measurements. This is clock plumbing, not the live mutation driver
or a propagation-latency result. Local topology validation also accepts a shared
multi-peer Scylla cluster while retaining exact API/controller counts, singleton
Git, ownership and independent-checkpoint requirements.

A further diagnostic-only extension connects the existing bounded scheduler to
ordinary authenticated Product/Role list requests. It predeclares each request,
rotates the owned working set and preserves a 60/30/10 allow/deny/owner-isolation
mix separately on each API. Denial principals exclude all independently declared
Group memberships as well as direct and built-in grants; an unresolved binding
alone does not prove a subject has no other permission. HTTP regressions separate
authorization unavailability and transport failures from actual denials, reject
partial-data leakage and bound response reads. Source joins now require the
declared action and resource kind for both request families.
The static native hook requires captured source/decision evidence and is bounded
to 240 predeclared requests; it is not the sustained or dynamic propagation driver.
Local race/HTTP contracts pass, but fresh native execution of this extension
now also passes its static slice: two Namespaces, twenty Roles, one hundred
bindings and eight real Staff principals complete all 120 scheduled ordinary
requests with 72 allows, 36 denials, twelve owner-isolation denials and zero
wrong, dropped or unavailable outcomes. Both APIs' exact action, source and
fixed-root joins pass. This six-second diagnostic is not sustained acceptance.

The diagnostic hook can additionally revoke and restore one independently
selected organization User grant by editing its Role's list permission and then
its binding's subjects through four real signed Git pushes. It excludes
personal owners and principals with overlapping Group grants, observes both APIs
concurrently from each successful-push acknowledgement, and preserves the
five-second bound without substituting a later ref/readback clock. Unavailability,
malformed responses and late successes fail rather than count as revocation.
Exclusive private receipts retain all four admissions and incomplete failures.
The hook also verifies the five independently authored Namespace states
against complete captured source models, immutable UIDs, exact mutation
generations, pinned reference-resolution generations and fixed root intent,
and joins each HTTP observation through its actual decision vector to the
committed oracle. Unresolved references cannot become ready source aliases;
verified aliases retain their original admission clocks and intermediate
revocations. Local race and sealed-replay contracts pass.

An earlier binding-subject mutation exposed immediate tier invalidation:
ordinary decisions became unavailable about 750 ms after acknowledgement.
Replacement work now retains only an already complete publication's existing
origin proof for stable-identity revisions, never renewing its clock. Expiry,
source gaps, lifecycle changes, repository replacement and overload remain
fail-closed; race tests cover these boundaries. A native binding-edit follow-up
then observed revocation in about 744 ms and restoration in about 2.84-2.95 s
without unavailable HTTP results, but still failed the strict source join.
That verifier incorrectly required the immutable reference's resolution
generation to advance with subject-only edits to the binding.
That failure is preserved, not reclassified as a complete propagation pass.
An earlier Role-rule-only diagnostic leaves binding generations unchanged.
Its native run passes its full diagnostic: the 120-request static phase,
both APIs' source/root joins, revocation in about 1.30-1.41 s, restoration in
about 1.15 s, exact Role/binding incarnation and restored-spec checks, and every
observed dynamic decision's committed-history join. These are two observations
per mutation phase, not propagation percentiles or a sustained workload.
Captured-source diagnostics now
disable decision-log sampling in their test process so missing receipts cannot
silently disappear. This is diagnostic overhead, not production logging behavior
or unprofiled acceptance. All completed fixtures close owned listeners without
forced stops and preserve the foundation. That earlier diagnostic did not prove
dynamic root history, propagation distributions or sustained acceptance.

The expanded native diagnostic passes in 71.10 seconds with two APIs, two
controllers, singleton Git and all 120 expected static outcomes. Both APIs join
Role-rule and binding-subject revocation/restoration to the five authored source
states, preserving immutable UIDs. Binding generations advance from one to two
and three while their originally resolved reference remains pinned to generation
one. Regressions reject both a self-consistent older binding generation and a
forged replacement reference generation. Controller `observedGeneration` is not
authorization readiness: a current subject-only edit with the independently
pinned immutable reference is already effective. A missing reference to an
existing Role remains ineligible as a ready source. Binding revocation takes
about 2.47-2.57 seconds and restoration about 2.63-2.74 seconds. These pass five
seconds, not the separate two-second p95 gate. The failed first expanded fixture
remains preserved; its source join conflated the binding and reference
generations. The correction changes only verification, not runtime status
semantics, propagation deadlines or outcome handling.

The bounded root hook now also passes its complete native history proof in
66.49 seconds: all 120 ordinary outcomes, four Namespace mutation phases and four
root grant/revoke/restore/removal phases, with every observed decision joined to
its actual source vector. Root phase observations are 0.53–2.03 seconds across
the two APIs. These are individual diagnostic observations, not percentiles or
full-scale acceptance.

Static measurement begins only after a bounded source-backed HTTP readiness
check; this warm-up does not reset any mutation acknowledgement clock. Initial
root creation independently declares an inactive unresolved state: its exact
new binding UID, generation, manifest and original acknowledgement are verified,
it contributes no grant, and it is accepted only within the same five-second
acknowledgement window. It never counts as a ready alias; every admission still
requires a complete reference-verified source. Later loss of an already resolved
reference, forged links and future or expired initial states are rejected.
Removal may retain only its independently pinned terminating row, or its later
physical absence. A live retained row cannot certify removal. The initial
administrator remains pinned throughout. Runtime publication and controller
authorization behavior are unchanged by the diagnostic verifier.

The native import hook also verifies an explicitly declared complete fixture
batch: two Namespaces, twenty Roles and one hundred bindings, one admission per
Namespace, all 120 ordinary outcomes and the complete eight-mutation source
history pass in 53.68 seconds. The single candidate remains within existing
256-manifest/4-MiB limits; live source validation, UID/reference checks,
four-second shared readback pacing and 30-second readback deadlines are
unchanged. This removes the six-import pacing multiplier, not proof of native
thousand-Namespace counts, sustainable throughput or full-scale percentiles.

The review-fixed runtime also passes a fresh complete Role-rule diagnostic:
all 120 ordinary outcomes and static/dynamic source joins pass, with revocation
about 2.47 seconds on each API and restoration about 1.26 seconds. Retry-backoff,
actual replacement-memory recovery and renewed-proof regressions pass under race
detection, as do the coupled application readiness cases and final isolated
default checks. The five-second diagnostic bound passes; two observations per
phase do not establish the separate two-second propagation p95 acceptance gate.

Bounded native seeding now passes a diagnostic with two Namespaces, twenty Roles
and one hundred bindings, including the personal-owner template. Both APIs
verify exact authored policies, unique UIDs and resolved Role links. Four real
Staff principals exercise ordinary Product list requests for unconditional,
group-conditioned and built-in grants, plus unresolved-role and unbound-subject
denials. Shared per-replica readback pacing respects the existing administrator
complexity budget rather than disabling admission. Transient authorization
unavailability occurred during seeding; the successful convergence checks are
not propagation-latency or availability acceptance. Full native scale and the
deployed mutable-policy oracle remain incomplete. The later full-scale static
verification below supersedes this diagnostic's count limitation, not its
original result.

The complete native fixture/static oracle prerequisite now passes on source
`7c814edeb29b2bef80b0c576909b312ad5e2a5ca`: 1,000 Namespaces, 10,000 Roles,
50,000 bindings and 61,000 distinct Namespace/policy UIDs. Both directly probed
APIs independently join every Namespace's authored policies, immutable UIDs and
ordinary decisions to captured source vectors within the unchanged shared
30-second gate (9.75 and 19.43 seconds). All five original policy cases pass.
The 500-second, 40-request/s rotation completes 20,000 of 20,000 offers with
12,000 allows, 6,000 denials and 2,000 isolation denials; wrong, dropped and
unavailable counts are zero. The deployment has two independent-checkpoint
controllers and one Git process. All owned listeners close normally without
forced stops; the isolated database process, original failed result and all
1,000 import-receipt hashes are unchanged.

Scheduled Namespace-origin proof batching uses the existing 32-ref hard bound,
three local workers plus root reservation, fresh live metadata per identity,
five-second freshness and pre-I/O proof clocks. No acceptance gate or resource
limit changes. This instrumented, read-only retained replay closes T090's
native fixture/static oracle prerequisite only. It does not convert the failed
original seed run into a pass or prove admitted-write propagation, full-rate
thirty-minute healthy/fault workloads or large-catalog interference.

A full-geometry public `api/readiness MODE=diagnostic` run passes on source
`466f99a28fbcd6376870f35922abe84ee0a1d0cd`. Both APIs independently join all
1,000 Namespaces within 12.57 seconds cumulative. During the 36-second window,
each API completes all 18,000 offers at 500 requests/s with exact
10,800/5,400/1,800 allow/deny/isolation counts and zero wrong, unavailable or
dropped outcomes. Twenty general Role resource/verb/condition admissions run
at 50/minute across four tiers: ten signed Git pushes and ten GraphQL updates.
Original-acknowledgement propagation p95/p99 is 1.542/1.545 seconds on A and
1.540/1.542 seconds on B; provider-call p99 is 562.750/558.458 microseconds.
All four forward restorations and independent sealed replay pass. Two
independent-checkpoint controllers and singleton Git are preserved; all owned
listeners stop normally, with the original import receipts, failed results and
database process unchanged. Canonical mutation refs advance through the
recorded forward restorations rather than a reset.

The run declares finite ingress 600/s with burst 1,200 and the profile's existing
100/200 VU defaults per API. It also uses the existing bounded directory defaults
of 4,096 cache entries and 16 concurrent lookups, retaining the five-minute TTL
and two-second timeout. Each API records 258 successful directory GETs and no
unavailable GETs or saturation, unlike the earlier undersized 128-entry/eight-
lookup fixture. The three earlier redacted response hashes exactly identify
`DIRECTORY_UNAVAILABLE` before authorization; both request drivers now classify
that as unavailability, never a policy denial or success. Earlier failures are
not reinterpreted. This closes T091's public selector/verifier prerequisite and
proves short full-rate general-Role coverage, not sustained healthy/fault,
maximum-batch, root-under-load or large-catalog acceptance.

A subsequent explicit 1,800-second mutation / 1,812-second ordinary diagnostic
on the same source fails early, rather than completing thirty-minute acceptance.
The complete 1,000-Namespace source joins and five ordinary sanity cases pass;
about eleven seconds into ordinary traffic, an API returns
`AUTHORIZATION_UNAVAILABLE` for an unchanged Namespace. Its correlated provider
receipt and SDK counters identify a Namespace-origin failure, not the earlier
directory outage. The existing failure guard stops further admissions and the
bounded cleanup verifies all four original policy specifications, UIDs,
generations and refs on both APIs. Eight admissions had completed, ending in
revocations, so cleanup needs no additional restoration write. All owned
listeners close normally and original receipts, failed results and database
process remain unchanged. Preserve this failure independently of the earlier
short pass; installed-tier absence versus proof expiry still needs attribution.
No freshness, latency or availability requirement is relaxed.

Two later full-intent attempts on source `47e26366` also fail and remain failed.
The first reaches 291 admissions before two ordinary HTTP 429 responses.
Their exact response digest and endpoint SDK counters identify HTTP concurrency
capacity, not the configured 600/s IP quota. Provisioning 256 finite HTTP slots
per API for the 200 ordinary clients plus bounded companion/controller traffic
does not establish sustained availability: the next attempt reaches 100
admissions before both APIs report `namespace origin expired` on unrelated,
unchanged Namespace tiers, approximately 121 seconds into ordinary traffic.
Its full source joins and five sanity cases pass beforehand.

Both attempts' bounded cleanup acknowledges its first forward restoration but
fails observation when a Namespace-origin proof expires. Separate exact-owned
recovery verifies every current Role UID, generation, specification and
canonical ref before writing. It restores only the remaining two or three
grants, then verifies original policy state and eight holdout denials across both
APIs, with independently captured exact-ref source models. Original failed
journals and seed receipts remain unchanged; all owned listeners close normally
and the retained database process is unchanged. Recovery is a safety result,
not a propagation or availability pass for either failed workload.

The follow-up input reader removes serial live-identity head-of-line delay
within signed-proof batches. At most four identities, each with three live
datastore reads, run concurrently per batch; proof input/result order and
invalid-identity isolation remain independently tested. The 32-ref ceiling,
root worker reservation, five-second freshness and pre-I/O proof clocks are
unchanged. A regression fails against serial collection and passes with race
detection after the change. These local checks do not prove the observed native
outages fixed or close any thirty-minute acceptance gate.

Its native comparison reaches 186 admissions before Namespace-origin expiry
on unchanged tiers of both APIs, approximately 224 seconds into ordinary
traffic. Cleanup completes with two forward restorations, no pending admission
or recovery pin and verified original state on both replicas. Input parallelism
alone is therefore insufficient; this attempt remains failed.

A separate receiver regression demonstrates that a write-locked repository
blocks unrelated canonical-ref proofs in the same response. The receiver now
uses nonblocking read-lock admission for this proof RPC only: a busy repository
returns no proof while unlocked repositories retain ordered, authorized
responses. No lock is bypassed and a missing proof cannot refresh freshness.
The regression fails with the original waiting lock and passes with independent
busy/unlocked results after the change. Signed authorization, replay, identity
binding and the 32-ref limit remain covered. A native full-window comparison is
still required before claiming that this isolation repairs the observed outage.

The first native comparison stops after 17 admissions when the next GraphQL
write is acknowledged but its immediate canonical-ref fetch encounters a
temporarily uninstalled Namespace origin. The original journal retains that
acknowledgement with an unknown ref; cleanup refuses its newer generation
rather than overwriting it. Independent recovery pins the exact committed ref,
UIDs, generations and specifications on both APIs before restoring the one
remaining grant. Eight final HTTP denials and matching exact-ref source models
verify safety; failed journals and original seed receipts remain unchanged.
This short failed attempt proves neither sustained availability nor repair of
the earlier expiry.

The companion now retries only the post-acknowledgement read-only canonical-ref
capture within the original five-second deadline, recording attempts separately.
GraphQL admissions remain single-attempt, malformed or late refs cannot become
proof, and neither the acknowledgement nor propagation clock is reset. Recovery
pins distinguish an acknowledged write with an unverified ref from an
unacknowledged admission. Capacity-contract race regressions cover these bounds;
the next comparison exercises seven read-only attempts for one acknowledged
write within its original deadline, but stops after four admissions on ordinary
Namespace-origin expiry. Cleanup verifies four forward restorations without
pending writes or recovery pins. It remains a failed workload.

Nonblocking canonical-ref responses now identify native write-lock contention
separately from absent refs. Contention never carries a commit proof. Only an
already installed, complete publication with identical freshly read Namespace,
mapping and repository identity and healthy journal progress may retain its
original unexpired proof while verification retries after 100 ms. Neither its
proof timestamp nor expiry advances. Changed identity, missing refs, malformed
busy-plus-commit responses, canceled verification and expired or future proofs
retain fail-closed behavior. Focused receiver, batch/single identity and manager
regressions demonstrate the distinction; no sustained availability gate is
closed by these local tests.

A subsequent uninstrumented full-rate comparison stops after 42 admissions on
ordinary origin expiry in an unchanged Namespace. Cleanup verifies the final
denials and two forward restorations without pending writes or recovery pins.
The contention distinction therefore does not establish sustained availability.
A bounded two-minute comparison with existing native SDK export enabled completes
all 100 admitted writes, with no dropped, wrong or unavailable companion
decisions, and verifies four forward restorations. In its 100-second interior
window, signed-verification stages remain below 100 ms and canonical-ref RPCs
below 250 ms. This instrumented diagnostic is neither a thirty-minute healthy
run nor proof of the earlier failure's cause.

A separate scheduler regression demonstrates that the previous rebuild limit
could consume every Namespace-eligible worker while the remaining worker was
reserved exclusively for root authority. Installed Namespace proofs could then
expire behind blocked scans even though root checks remained healthy. Once
publications are installed, the default four-worker manager now admits at most
two Namespace rebuilds, preserving independent capacity for Namespace proofs
and root recovery. Regressions cover three- and four-worker dispatch and actual
proof renewal during blocked scans. The total worker and ref bounds, startup
behavior, root priority and original freshness clocks are unchanged; sustained
acceptance remains open.

The following full-rate attempt stops after 68 admissions on a mutated Namespace
reporting that its origin is not installed. No ordinary-stream provider errors
are logged before cancellation, but this failed interval cannot establish
sustained availability. Four forward restorations complete and independent
post-shutdown canonical-ref reads match all four recorded restoration commits.
An unacknowledged next-offer marker remains preserved, so continuation requires
separate safety validation; neither the failed result nor the marker is erased.

A deterministic scheduling regression also exposes unused batch capacity while
Namespace workers are occupied by rebuilds or another proof batch. The last
available Namespace proof worker previously retained only its fixed per-worker
share. It now uses the remaining shared bounded capacity, counting all running
Namespace work and retaining the separate root/recovery share. Concurrent batch
and blocked-worker regressions pass under race detection. This does not renew
proofs without verification, change expiry or establish the latest failure's
cause.

An unprofiled two-minute comparison then completes all 100 admitted mutations
and four forward restorations, without wrong or unavailable companion decisions.
Its ordinary stream drops 44 scheduled iterations, so the comparison fails;
successful mutations do not override the ordinary accounting gate. The following
thirty-minute attempt stops after three admissions on Namespace-origin expiry.
Three forward restorations complete, with no pending writes, cleanup errors or
recovery pins. Independent terminal validation preserves the failed results and
the advanced baseline; subsequent runs must revalidate live identities, specs,
generations, canonical refs and holdout denial before writing.

A separate SDK-export-instrumented diagnostic on source `44036f05` completes
20 mutations over 24 seconds and 36,000 ordinary requests over 36 seconds.
Both APIs independently source-join every ordinary decision with the exact
60/30/10 mix and zero wrong, unavailable or dropped decisions. All mutation
revisions and original acknowledgement clocks join their propagation probes,
and four forward restorations complete. This short instrumented comparison
does not reproduce the preceding expiry and is not sustained acceptance.
Finite rebuild-failure stages now distinguish source capture, translation,
canonical verification and installation without renewing proofs or relaxing
the workload; superseded scans are not classified as source failures.
Both thirty-minute healthy families, the fault workload, and retained-catalog
push interference therefore remain unaccepted.

A subsequent root-under-load diagnostic on source `90568908` passes the
ordinary-start barrier on both APIs and verifies four root binding transitions.
It then fails an empty mutation-generation-pin precondition during dynamic
source alias verification, before the maximum batch or Namespace mutation
schedule begins. This is a harness-precondition failure, not maximum-batch or
sustained acceptance. Terminal verification independently confirms that the
root Git tree is byte-identical to its pre-run tree, temporary grants are
withdrawn, and both APIs produce source-joined holdout denials after withdrawal.
All four Namespace refs, original specs and immutable identities remain
unchanged. Continuation requires this independent root clearance as well as
fresh live checks; a Namespace journal alone cannot clear root mutations.

A maximum-root-batch diagnostic on source `ecc7578b` subsequently reaches the
256-resource batch under the unchanged ordinary load, but fails the original
five-second successful-push propagation bound. Both APIs capture complete
candidate models; later controller resolution and repeated root rebuilds do
not retroactively satisfy that bound. The automatic thirty-second cleanup also
fails, and its pending state and error remain in the original failed receipt.
Separate, UID-fenced forward recovery withdraws the temporary binding first,
then removes the unbound Roles in bounded batches. Continuation checks recorded
Git acknowledgements against the actual canonical ref, and waits for normal
holdout denials at the exact revision on both APIs rather than accepting
authorization unavailability as denial. Independent terminal verification
confirms all temporary resources are gone, the original complete root Git tree
and bootstrap identity are unchanged, and both denials join complete source
models after the final withdrawal acknowledgement. Controller deletion settles
before clearance; all four Namespace refs, generations, specs and immutable
identities remain unchanged. The diagnostic remains failed, not root-batch or
sustained acceptance.

A follow-up on `2110dbdb`, including covered-progress publication and root clone
preparation before ordinary traffic, still fails at the maximum root batch with
actual cluster-origin expiry and the original cleanup bound. Complete candidate
models first appear 4.40 and 6.04 seconds after acknowledgement on the two APIs;
the binding initially reports `RoleNotFound` and resolves after 16.94 seconds.
These observations do not establish a sole cause. Audited forward withdrawal and
independent terminal source/denial verification again restore the original tree,
bootstrap identity and four unchanged Namespace baselines without rewriting the
failed workload result.

A finite controller-request-budget comparison then stops before any policy
admission because startup polling samples its upper clock before a concurrent
decision-file append. The guard now samples that clock after reading the matching
decision and rechecks the original startup deadline after verification. Race
regressions retain pre-start, future-clock, wrong-provider/verdict and missing-
source rejection. A native follow-up on `d69010bc` passes startup and the four
binding transitions, then fails maximum-root availability and automatic cleanup.
Six ordinary decisions on one API report expired Namespace proofs; complete
candidate models first appear 5.95 and 5.65 seconds after acknowledgement.
The finite controller-budget increase therefore establishes no repair.
Audited forward withdrawal and independent terminal verification restore the
original complete root tree, bootstrap identity and four unchanged Namespace
baselines; the original failed receipt remains failed.

The same run records a three-to-five-second journal append. The 32 KiB event
batch budget fits only one padded root resource at a time, serializing the
256-resource admission into approximately one conditional append/publication
pair per resource. An isolated four-MiB manifest verification diagnostic takes
about 250 ms on the same host; this distinguishes that CPU work from the observed
journal tail but does not establish a sole cause. Event batches now retain the
32-statement and bucket boundaries with an initial 128 KiB encoded-byte budget;
source-progress checkpoint batches keep their original 32 KiB budget.
Regression coverage rejects the former per-resource degeneration while retaining
byte, statement, oversized-event and bucket-boundary checks. Full default
readiness and tagged Scylla race contracts pass. Native follow-ups reduce the
journal append tail to at most one second and do not repeat the Namespace-proof
expiry, but still fail maximum-root ordinary availability: complete candidate
sources first appear about 4.5–5.9 seconds after acknowledgement, and cluster
proofs expire. A bounded instrumented follow-up does not establish a single CPU
cause. Both workloads fail automatic cleanup; separate audited forward
withdrawals and independent terminal verification restore the original complete
root tree, bootstrap identity and four unchanged Namespace baselines. Their
original failed receipts remain failed. No acceptance limit is changed.

Those native traces also record 1.4–2.8 seconds per API in index repair, which
previously serialized independent UID conditional writes. Repair now overlaps
at most four resources per call, preserving each resource's lookup/link ordering,
UID tombstones and the final revision CAS. Failure cancels and joins all workers
without clearing pending repair. Focused race, concurrent native Scylla repair/
retry and independent-writer contracts, and full default readiness pass.
An unprofiled comparison reduces observed repair time to about one second but
still fails root availability. A subsequent bounded diagnostic captures both
APIs' worker stacks 0.3–4.3 seconds after maximum-root acknowledgement: complete
candidate sources arrive by 3.3 seconds, but journal publication remains active
at 4.3 seconds and ordinary decisions report generic tier unavailability near
five seconds. These timings do not establish sustained acceptance or a single
remaining cause. Every failed workload is separately forward-withdrawn and
independently checked against the original root and Namespace baselines.

The 128 KiB event bound still serializes these padded frames into roughly
37 conditional append/publication pairs. The finite event-byte budget is now
512 KiB, retaining the original 32-statement limit, bucket boundaries, isolated
oversized-event handling and separate 32 KiB progress checkpoint bound.
A focused regression rejects the former seven-padded-events-per-batch limit;
native contiguous readback coverage crosses both byte and bucket boundaries.
This is bounded journal work amortization, not a change to any freshness,
propagation, ordinary-availability, snapshot, worker or acceptance-duration gate.
The clean-source native comparison still fails the original five-second
maximum-root bound. Journal appends stay below 500 ms, and both APIs capture
the new root candidate after approximately 2.3 seconds. Its binding remains
`RoleNotFound` in captured controller status until approximately 21 seconds:
independent Role and binding watches have not caught up together. Generic
unavailability begins with the diagnostic's five-second cancellation, so it
does not establish a stale published frontier; subsequent root-origin failures
remain separate evidence. Audited forward withdrawal and independent clearance
restore the original root and unchanged Namespace baselines.

The controller now confirms a missing cached role with one scoped authenticated
API lookup before declaring the reference absent. Checkpoint errors are not
hidden by this fallback, and authority errors remain retryable. Duplicate
controllers converge on the same UID-bound status; replacement Roles cannot
inherit an existing binding. Focused race regressions pass. Native validation
of this controller correction and all sustained gates remain required.

The next native comparison stops before maximum-root admission: one Product
probe denies ordinary list access, then allows management-list access after a
new binding-status publication within the same request. The final HTTP outcome
therefore differs from the ordinary decision joined by replay. Automatic root
withdrawal and independent clearance restore the baseline without Namespace
mutations. Product and ProductVariant read/list alternatives now reuse one
captured serving model for the authorization operation, retaining any enclosing
mutation capture. Failing-first regressions reproduce the mid-check publication
for both kinds and verbs, verify identical evaluated revisions and require a
fresh request to observe the new grant. Native comparison and the unchanged
sustained gates still remain required; no trace or propagation gate is relaxed.

With the alternate-check correction, the native comparison passes the four
simple root transitions and reaches maximum-root admission. The binding is
resolved at about 2.8 seconds and appears in both APIs' captured candidates by
4.3 seconds, but neither serves the new grant before the original five-second
deadline. Concurrent Role status writes continue advancing tier revisions;
captured candidates are not evidence of installed publications. Automatic
restoration fails, then audited UID/generation-pinned forward withdrawal and
independent clearance restore the root and unchanged Namespace baselines.
The manager's newer-revision publication fence remains intact.

An offline maximum-root translation benchmark identifies repeated parsing of
non-semantic annotation padding. Translation now counts complete canonical
manifests against the unchanged byte limits, validates annotation keys and UTF-8
values, then reparses only the remaining manifest. Source annotations and
permission digests are unchanged. The bounded benchmark improves from about
128 to 63 ms and from 108 to 71 MB allocated per operation on this development
host; these are offline diagnostic totals, not peak memory or sustained-load
acceptance. Annotation rejection/preservation regressions and provider/manager
race suites pass. Native validation and all sustained gates remain outstanding.

The subsequent native comparison still fails maximum-root propagation and
requires audited forward withdrawal; independent clearance again preserves the
root, Namespace refs/generations and original failed evidence. Repeated staged
candidates are superseded while Role status writes advance revisions.
The provider now reuses successful individual manifest validation for unchanged
annotated root resources, keyed by complete author-controlled content. Current
UIDs, lifecycle, status, binding resolution, source vectors and the manager's
publication fence are still evaluated on every rebuild. The reuse cache is
limited to 256 entries and 512 KiB, charged inside the original snapshot budget;
it yields its reservations before a valid publication can fail for memory.
Concurrent preparations, changed/malformed author content, unresolved or stale
UID bindings, terminating Roles, rule revocation, eviction and shutdown have
focused coverage. An offline repeated maximum-root preparation benchmark is
about 8 ms and 12 MB allocated per operation, compared with about 67 ms and
71 MB for uncached translation. These are diagnostic totals, not peak memory or
native acceptance; the fresh comparison and sustained gates remain required.

That fresh comparison also fails the unchanged maximum-root deadline: the first
complete capture begins at about 5.9 seconds on one API; the other captures an
unresolved binding at 3.3 seconds and resolved status at 4.8 seconds. Automatic
restoration fails, then nine audited forward-withdrawal operations and independent
clearance restore the original root and unchanged Namespace refs/generations.
The existing source reader still performs complete canonical YAML validation
before provider preparation, so provider-only validation reuse does not remove
that repeated work. Its offline maximum-root benchmark measures about 145 ms
and 128 MB allocated per scan. Snapshot capture and provider preparation now
share the same exact-author-content validator and existing budgeted cache;
neither keeps an authoritative source model or decision in that cache. Complete
source envelopes and content digests still include current status, immutable
identity and annotations. Focused source-bound/identity regressions and full
reader/provider/runtime/security race suites pass. Warm snapshot scans measure
about 24 ms and 32 MB allocated versus 82 ms and 91 MB without reuse on this host.
These are offline diagnostic totals, not peak memory or native acceptance.
The fresh native comparison and all sustained gates remain required.

The instrumented comparison still fails maximum-root propagation. Both APIs
are in committed-tree projection or UID-index repair during the early transition;
their first complete captures arrive at about 6.7 seconds. This does not isolate
one database or Git-service bottleneck. Automatic cleanup fails; audited
forward withdrawal and independent clearance again restore the original root
and Namespace baselines without adopting the failed workload. Scheduling review
also finds that an unsuccessful complete scan can assign its one-second retry
delay to a newer generation already queued during that scan. A failing-first
regression reproduces the delay. Newer queued scans now proceed without that
older attempt's delay; unchanged failed scans and origin-proof failures retain
their backoff. Publication fences, freshness, worker and queue bounds are
unchanged. Reader/provider/runtime race suites pass; a fresh native comparison
and all sustained gates remain required.

The unprofiled comparison with the scheduling correction still misses the
maximum-root bound: first complete candidates arrive at about five seconds on
both APIs. Audited withdrawal and independent clearance preserve the original
root, Namespace baselines and failed evidence. A competing multi-resource
projection attempt spends about 980 ms in `ApplyRBAC`; the transition stacks
include its sequential per-resource reads. Independent candidate lookups now
reuse the existing four-worker, cancel-and-join step runner before planning and
the unchanged atomic tier CAS. Absent resources still represent creates; any
other read failure prevents planning or returning a partial candidate.
Focused concurrency/failure race regressions and actual Scylla replica
status/conflict contracts pass. No schema, write bound, revision fence or repair
ordering changes. Native impact and all sustained gates remain unverified.

The fresh unprofiled comparison reaches resolved maximum-root candidates in
about 3.0 and 3.5 seconds, but publication still misses the original five-second
bound. The subsequent bounded, instrumented comparison also fails. Its stacks
include committed-tree projection, UID repair and signed ref proofs; they do not
isolate a sole bottleneck. Both failed candidates are withdrawn through audited
forward operations and independently cleared, preserving the original root,
Namespace refs/generations and failed evidence. A further failing-first scheduling
regression shows that a newer durable revision arriving after a failed scan
still inherits that older scan's retry delay. Queuing a strictly newer revision
now clears the old delay, including when it arrives after failure. The original
revision/publication fences and backoff for unchanged failures remain intact.
Reader/provider/runtime race suites pass. Neither correction nor the diagnostic
captures establish maximum-root or sustained acceptance.

The next unprofiled comparison still misses maximum-root publication: resolved
candidates captured at about 4.0 and 4.6 seconds are never served; later resolved
revisions first serve at about 5.7 and 5.9 seconds. Both actual probe windows
last the original five seconds, so the failure is not preamble truncation.
A separate bounded execution-trace comparison also fails. Its root workers
accumulate about 2.2--2.4 seconds of synchronization wait, predominantly in
snapshot reads, including canonical Git reads and datastore snapshots; final
origin and journal checks account for much less synchronization wait.
These aggregate waits are neither exact end-to-end durations nor proof of a
sole bottleneck. Traces have instrumentation overhead and are decoded with
their embedded symbols, without executable re-symbolization. Both failed
candidates require audited forward withdrawal and independent clearance;
the original root, Namespace baselines and failed evidence are retained.

Runtime recovery previously used complete committed-tree reconciliation even
when a successful canonical snapshot already proved a matching projection and
only its UID-index repair remained pending. That path now calls the existing
revision-checked index repair directly, then repeats the complete authoritative
snapshot read. Actual projection mismatches retain committed-tree recovery.
A failing-first runtime regression proves the redundant recovery scan; focused
retry coverage verifies that index-repair failure cannot publish the pending
source or fall back to another complete Git-tree repair. Reader, provider,
runtime and catalog recovery race suites pass. Native impact and all sustained
acceptance gates remain unverified.

The fresh unprofiled comparison with direct index repair fails on unplanned
unavailability at about 4.4 seconds after maximum-root acknowledgement, rather
than exhausting its five-second observation deadline. The first resolved
candidates are captured at about 4.0 and 3.5 seconds; one is first served at
4.7 seconds, while the other is superseded and its replacement first serves at
5.1 seconds. These joins do not establish a performance improvement. Audited
forward withdrawal and independent clearance preserve the original root and
unchanged Namespace refs/generations.

Cold canonical revisions also caused an entire immutable-tree scan before the
runtime discovered a stale projection and delegated another entire scan to
committed-tree recovery. Runtime origin reads now compare live canonical
repository/ref/commit facts with the projected identity on digest-cache misses.
A mismatch routes directly to existing recovery; matching metadata still
requires independent tree verification. Unconditional origin reads, complete
datastore snapshots, revision fences and final live proofs are unchanged.
A failing-first regression verifies no immutable reads before mismatch recovery,
independent verification afterward and refusal to substitute a cached digest
for current canonical facts. Reader/provider/runtime/catalog recovery race
suites pass, including bootstrap and lost-callback recovery. Fresh native impact
and all sustained gates remain unverified.

The fresh comparison still reaches resolved maximum-root candidates at about
3.1 seconds on both APIs without installing them. Replacement candidates first
serve at about 5.5 seconds; both original five-second probes time out. Audited
forward withdrawal and independent clearance again preserve the root,
Namespace baselines and failed receipts. No native acceptance is claimed.

A separate failing-first regression finds the complementary retry problem in
origin proofs: a newer durable revision queued while a failed proof runs can
still inherit that proof's retry delay, for both the shared root worker and
batched Namespace proofs. Work now records the generation of its last strictly
newer durable revision. Such queued revisions bypass obsolete proof backoff;
unchanged failures, same-revision requests and a failed proof's own replacement
retain their delay. Failed proofs cannot renew publication freshness, and
revision, lifecycle and final-publication fences are unchanged. Root/batch
regressions and reader/provider/runtime race suites pass. Native impact and all
sustained gates remain unverified.

The subsequent unprofiled maximum-root comparison still fails on unplanned
unavailability at about 4.2 seconds after acknowledgement. A bounded instrumented
comparison also times out both original five-second probes. During that capture,
native `ResolveRefs` reaches about 1.50 seconds, replay verification about
1.29 seconds and replay-lock acquisition about 1.05 seconds. These are SDK
histogram maxima, not joined timings from one RPC, propagation percentiles or
proof of a sole bottleneck. A preceding instrumented attempt stops before the
maximum-root phase because the ordinary stream drops offers. All remain failures;
independent canonical/source/denial clearance follows automatic restoration or
audited forward withdrawal, preserving Namespace baselines and original receipts.

Cold origin reads also repeat the live canonical proof obtained for the
digest-cache/projected-identity lookup immediately before scanning immutable
files. They now reuse that initial proof to pin the scan, retaining the complete
immutable verification and the live proof afterward. Direct tree reads still
obtain both proofs themselves. A failing-first regression covers unconditional
and projected cold reads, including rejection when the canonical ref advances
during the scan. Native impact and sustained acceptance remain unverified.

Native replay expiry reclamation still performed its clock fsync, directory
fence and bounded unlinks while holding the shared nonce-reservation mutex.
It now claims at most one bounded cleanup batch, releases that mutex for
durable filesystem work and accounts for completed deletions afterward.
Spent records remain reserved until deletion; the clock fence still precedes
every deletion, pending writes are excluded and capacity remains fail-closed.
The cleanup owner rechecks the clock and envelope expiry before reserving its
own nonce. A failing-first concurrency regression blocks cleanup while another
signed request completes, rejects that request's replay and verifies rollback
protection after replacement. Existing durability/capacity/pending-write tests
and partial-cleanup retry coverage pass. Fresh native performance and all
sustained acceptance gates remain unverified.

The next unprofiled comparison serves resolved maximum-root authority on one API
at about 4.19 seconds, but the other retains an unresolved binding and does not
capture its resolved source until about 5.86 seconds. The combined five-second
gate still fails; no ordinary decision errors precede the deadline. Audited
forward withdrawal and independent source-joined denial clearance pass.
The delayed API records repeated stable-page revision conflicts during Role
metadata writes. Scylla now fences bounded raw-row I/O with identical complete
tier states before decoding; shared snapshot validation, the three-attempt
budget and all publication fences remain. Native impact is not yet verified.

On the next unprofiled comparison both APIs serve the resolved maximum-root
source within five seconds (about 4.32/4.35 seconds; HTTP probes about 4.60/4.54).
The workload still fails because source eligibility is checked only after
sequential UID readbacks. Offline replay through the unchanged strict oracle
proves complete independently eligible captures at about 3.24/3.23 seconds.
The source wait now checks sealed capture observation clocks against the
original ACK plus five seconds, rejecting missing/future clocks and later
captures; verification time cannot relabel a source. The HTTP bound,
independent UID/generation/reference checks, writer barrier and fixed
48-second preamble remain unchanged. The failed workload is not retroactively
accepted. Forward withdrawal and independent clearance pass; a fresh comparison
is still required.

That fresh comparison passes the first four root phases and all four independent
source pairs. Maximum-root creation still fails the original HTTP propagation
bound: one API succeeds at about 4.30 seconds, while the other cannot capture
resolved authority until about 6.34 seconds. Its three consecutive raw reads
conflict with controller Role metadata writes; the next bounded rebuild also
conflicts. The failed workload remains failed. Forward withdrawal and independent
clearance pass, preserving the four Namespace refs and generations.
Row retries now use the existing parallel live-origin checker before reusing
verified immutable tree proof, instead of repeating sequential origin discovery.
All live identity/ref facts are reread; a failed check consumes an existing
attempt and forces rediscovery. Source rows still require identical complete
tier headers, exact projection matching and unchanged final publication guards.
The three-attempt budget and failure cooldown are not increased or removed.
Race regressions pass; a fresh deployed comparison remains required.

Subsequent retained diagnostics still fail ordinary authorization availability.
One stops before maximum root with expired Namespace origins; two reach maximum
root at approximately 4.53/4.59 and 4.62/4.62 seconds across the APIs but encounter
expired cluster origins. Those are actual unavailable decisions, not dropped
offers. Every failed run is independently cleared after automatic restoration
or audited forward withdrawal, preserving the original receipts and Namespace
baselines. Root status convergence advances tier revisions while the canonical
Git identity remains unchanged; this observation does not establish a controller
no-op feedback loop.

A protected maximum-root trace records a longest repair wait of approximately
1.82 seconds and aggregate repair waits of 1.83 seconds; overlapping aggregate
waits are not propagation latency. Inspection also identifies a completion race:
after a peer clears an identical projection's repair marker, a status write can
advance its revision before the other replica clears that marker. A failed
conditional clear now performs one fresh read and acknowledges completion only
for a newer revision of the exact same projection and tier, with unchanged
Namespace retirement and no pending repair. Entry revision checks, bounded
workers, lookup tombstones and all authority/publication fences remain intact.
The failing-first confirmation regression and isolated Scylla concurrent-status
tests pass under race detection. This does not establish that the completion
race is the sole native bottleneck; fresh performance and sustained acceptance
remain unverified.

The subsequent unprofiled comparison reaches maximum root at approximately
4.48 seconds on each API but still fails the root availability probe. One
ordinary request reports capture unavailable approximately 4.17 seconds after
acknowledgement; this is not an explicitly classified origin-expiry response.
Automatic withdrawal times out, followed by successful audited forward recovery
and independent canonical/source/denial clearance with unchanged Namespace
baselines. The repair acknowledgement correction is insufficient for acceptance.

Recovery and serving-source construction also independently scan the same
canonical manifests. They now share the existing bounded immutable-tree digest
cache, populated only after complete pinned-file verification and matching live
proofs. No resource, authority, Namespace fact or freshness proof is cached by
this change. Serving construction still obtains live canonical facts, checks
the complete datastore projection, validates resources and retains every
publication fence. A failing-first regression covers receive and lost-callback
projection followed by a serving read without a second immutable scan, while
rejecting changed or unavailable canonical refs. Native impact remains
unverified.

The next unprofiled comparison reaches maximum root at approximately
4.30 seconds per API but still encounters capture unavailability and one
explicit cluster-origin expiry immediately before publication. Audited forward
withdrawal and independent clearance again preserve the Namespace baselines.
Neither correction is sufficient for acceptance.

The shared root previously used the same one-second proof interval as local
tiers, consuming up to a second of the original five-second freshness budget
before a canonical change. Its healthy checks now run every two watch polls
(200 milliseconds), still capped at half the configured freshness bound.
Namespace cadence, proof expiry, uncertain-proof handling, generation fences,
worker limits and publication barriers are unchanged. This adds only bounded
shared-root checks, not more frequent checks across every Namespace. A
failing-first deterministic-clock regression verifies earlier root renewal and
the unchanged local interval; the existing slow, contended and superseded-proof
race regressions pass. Native impact remains unverified.

The following unprofiled comparison reaches the original maximum-root deadline
without ordinary provider-unavailable decisions before abort, but still fails
grant propagation. Both APIs serve the initial unresolved-binding revision;
resolved candidates arrive at about 3.03 and 2.89 seconds, yet are not installed
within five seconds while initial Role status updates advance tier revisions.
Post-abort cancellations are not classified as origin-expiry responses.
Audited forward withdrawal and independent clearance preserve the original root
and unchanged Namespace baselines. Candidate capture is not publication proof.

Publication candidate construction now delegates its final live-origin recheck
to the manager's existing mandatory post-preparation proof, rather than doing
both serially. Only runtimes with that publication proof select the candidate
path. Initial live canonical identity, exact complete datastore projection,
resource validation, byte/retry bounds, newer-revision rejection and all
publication/lifecycle/continuity fences remain intact. Authoritative validation
and runtimes without a publication proof retain the reader's own final check.
A failing-first candidate regression and runtime readiness tests cover deferred
proofs, refusal to publish on failed final proof and unchanged authoritative
validation. Reader/provider/runtime race suites pass. Native impact and all
sustained acceptance gates remain unverified.

The candidate-composition native comparison still fails maximum-root propagation:
resolved candidates arrive at about 3.95 and 3.83 seconds, while both APIs serve
the initial unresolved-binding revision through the original five-second
window. Only post-abort cancellations are observed, not earlier ordinary
provider-unavailable decisions. Nine audited forward withdrawals and independent
clearance restore the root and unchanged Namespace refs/generations.

Healthy Namespace origin renewal now uses half the configured freshness bound
(2.5 seconds at defaults), instead of the earlier one-second cap across every
resident tier. Shared-root checks remain at 200 milliseconds; durable watch
processing and dirty-tier rebuild scheduling remain immediate. This reduces
scheduled unchanged-Namespace proof work while retaining half the freshness
budget as renewal headroom. It does not extend proof expiry, renew uncertain
proofs or change live canonical checks, publication fences, worker/batch bounds
or propagation gates. A failing-first deterministic-clock regression covers
the changed local cadence and unchanged prompt root renewal. Native impact
and sustained acceptance remain unverified.

The following unprofiled comparison on `2d1cb8a2` still fails maximum-root
propagation. Resolved candidates appear at about 2.66 and 2.85 seconds after
acknowledgement, but neither API serves that root vector before failure.
One API explicitly reports cluster-origin expiry at about 4.85 seconds;
capture-unavailable decisions also occur. Automatic withdrawal fails, including
a timed-out cleanup push. Nine audited forward withdrawals and independent
clearance restore the root and unchanged Namespace refs/generations. Earlier
candidate arrival is not successful publication or acceptance.

Publication catch-up now permits at most three complete source captures in one
bounded worker attempt, retaining the existing deadline and resource bounds.
A newer source must have the exact same canonical/lifecycle origin and
projection, a strictly newer revision, the same journal epoch and no pending
repair or retirement. OPA can retain the prepared immutable policy only after
translating the complete newer source with its existing memory budget and
verifying an identical normalized authorization digest. This advances source
metadata, not policy or the original live-origin proof timestamp. Changed
authorization data instead requires a fresh preparation and a new mandatory
post-preparation live-origin proof. Both paths repeat the final durable
revision, lifecycle, epoch, progress and proof-age fences; exhausted catch-up
releases the preparation rather than publishing an older revision.
Provider, manager and runtime race suites cover policy changes, consumed or
closed preparations, temporary-budget exhaustion, retirement, later revisions,
lifecycle/epoch changes, original-proof expiry and the capture limit. These
regressions do not establish native improvement or sustained acceptance.

The unprofiled comparison on `9dc7cf8e` still fails maximum-root propagation.
Resolved sources appear at about 3.16 seconds on both APIs, but ordinary
decisions retain the unresolved-binding revision through five seconds.
Only post-abort cancellation errors are observed. Automatic cleanup again
times out; nine audited forward withdrawals and independent clearance restore
the root and unchanged Namespace refs/generations. Offline replay through the
actual OPA translator confirms that the first two resolved complete sources
have identical normalized policy digests and can rebase successfully. Repeated
Role metadata transactions can nevertheless keep superseding publication.

Role diagnostics, audit metadata, owner-link and finalizer-only changes now
retain their normal durable resource events and monotonically advancing CAS
revisions without emitting a synthetic authorization-tier progress event.
The shared classifier requires unchanged desired policy, identity, generation,
labels, annotations, canonical revision and termination state. Bindings are
never classified this way. Scylla additionally requires complete pre/post
sentinels at consecutive revisions, an identical valid projection, unchanged
counts, no retirement and no pending repair; uncertainty retains tier progress.
Canonical imports, role creation/removal, permission changes and binding
resolution/revocation therefore retain the existing newer-revision publication
fence without changing manager scheduling or relabeling an older source.
Local journal/CDC and publication race regressions pass. Native impact and all
sustained acceptance gates remain unverified.

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

A subsequent source-observation-disabled full-count attempt reached sixty-eight
complete Namespace receipts, 680 Roles and 3,400 bindings before failing after
4,187.04 seconds. One further Namespace remained partial, last recording ten
Roles and forty bindings. Replica zero remained authorization-unavailable beyond
the unchanged thirty-second readback bound. Both supported binary failure
profiles decode; they show RBAC origin/watch work waiting in CQL calls, not a
proven underlying cause. The owned backend did not restart, all fixture listeners
closed without forced stops, and the foundation identities remained unchanged.
These frozen-build receipts predate the new acknowledgement-clock fields and
cannot supply propagation or source-vector acceptance. Preserve the partial
fixture and failure; no full-scale pass or automatic full retry is implied.

A fresh one-writer control subsequently completes eighty Namespaces, 800 Roles
and 4,000 bindings with the unchanged thirty-second per-API readback bound.
Its terminal receipt verifies normal shutdown, closed owned listeners and an
unchanged foundation. It uses the pre-review bounded-replacement runtime with
source/profile captures disabled. No image or full-suite build overlaps its
workload; bounded single-host-core Go regressions do run alongside it. This
surpasses the earlier diagnostic's cardinality but does not isolate its cause,
verify the subsequent retry/memory fixes at that scale, or establish full-scale
or sustained acceptance.

A reviewed-runtime eighty-Namespace control fails after 3,053.80 seconds at
sixty-seven complete receipts, 670 Roles and 3,350 bindings, with one partial
Namespace. After its fourth successful import acknowledgement, canonical
`git ls-remote` exhausts the existing thirty-second retry window on
authorization-unavailable responses. This is not a per-API readback timeout.
Both API failure profiles show all four workers in full Git-backed origin
rebuilds rather than the earlier CQL waits. No local builds or tests overlap the
workload; cleanup closes all owned listeners without forced stops and preserves
the foundation and failure evidence. No native cause is established.

A separate manager regression proves long Namespace scans can occupy every
worker until healthy root origin proofs expire. With installed publications and
at least two workers, scheduling now leaves one slot for origin checks or shared
root replacement and prioritizes root rebuilds over Namespace rebuilds.
The regression fails before the correction and passes under race detection,
including real origin-failure invalidation and eventual queued-scan recovery.
The configured worker count and all freshness, retry, timeout and memory bounds
are unchanged. The local regression alone does not establish native recovery.

The corrected reviewed-source runtime subsequently completes eighty Namespaces,
800 Roles and 4,000 bindings in 3,220.25 seconds, with all six acknowledged
canonical imports complete per Namespace. The one-writer, eight-principal
control disables source/request/propagation/startup-profile captures and has no
overlapping local builds or tests. Normal cleanup closes all owned listeners
without forced stops and preserves the foundation. This is current-source
medium-scale evidence, not one-thousand-Namespace, sustained-load or fault
acceptance; the earlier failed control remains preserved.

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

T094 now has two independently passing native matrices. The feature-disabled
production case passes in 53.21 seconds against the exact pre-RBAC baseline,
with additive migration/read-only startup refusal, mixed-version caller parity,
authoritative ServiceAccount UID/resourceVersion preservation and unchanged
singleton Git history. Its Git commit is separately seeded; ServiceAccount
admission is not claimed to have produced that history.
The forward diagnostic cutover case passes in 50.88 seconds with genuine
source-distinct compatible API/controller binaries, seeded canonical parity,
real admitted-request drain and write freeze, source-ready traffic admission,
USER-owner catch-up, retained independent controller checkpoints and healthy
peer requests during both API replacements. An obsolete static candidate starts
off traffic but fails the independent frontend parity/admission oracle after
dynamic revocation. This is not intrinsic static-provider rollback refusal,
hot provider reload or production OPA activation.

The compatible diagnostic baseline includes the payload-free deletion repair.
Valid local Namespace/Repository tombstones previously invalidated every RBAC
tier; actual materializer regressions reproduce that failure. Matching tiers
now require fresh authoritative UID-bound scans without invalidating unrelated
root authority or permanently blocking a same-name replacement. Missing
identities and malformed non-deletion payloads remain fail-closed. Earlier
buggy-baseline forward failures remain failures; a reverse-release control
does not replace forward evidence. Owned helpers stop normally and preserve
both retained database processes/storage. These bounded matrices complete T094,
not the full T096/T097/T098 workload or T103 closure.

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

The current-source full `make pr-ready` and `make check TARGET=all` pass,
including the default build, lint, test, license and credential suites. An
earlier run failed with actual linker `ENOSPC` errors; removing reproducible
task-owned compiler caches allowed full validation without deleting retained
datasets, immutable binaries or failed evidence. No fixed free-space threshold
is an acceptance gate. These aggregate checks do not complete the six pending
native acceptance tasks.

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

## Healthy workload (production acceptance in #451)

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
- Propagation to each API p99 <=5 s from successful admission; report p95
  against the non-blocking <=2 s optimization goal. No allowed decision may
  exceed the configured 5 s revocation bound.
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

## Interference and larger-catalog gate (production acceptance in #451)

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

For T097's feature proof, exercise the native domain scenarios below in
bounded owned windows with an independent decision/revision oracle. Require
zero wrong or expired grants, ready-only traffic admission, correct
surviving peers, UID-safe controller recovery and recovery <=30 s after the
source becomes available. Do not substitute responsive HTTP stand-ins,
injector success or a restarted PID for authorization recovery. Record
unavailability distinctly from a correct policy denial.

The following sustained workload remains required by #451, not by
correctness-only feature closure. Its rates, duration, oracle and gates
are unchanged.

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
