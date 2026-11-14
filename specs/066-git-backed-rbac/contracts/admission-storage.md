# Admission, Projection and Lifecycle Contract

## Validation-start authorization

For every RBAC candidate:

1. Authenticate the external actor or enrolled service identity through
   existing trusted principal construction and signed RPCs.
2. Capture required cluster/namespace tier snapshots from authoritative
   state, with current canonical Git tips and matching complete projections.
   A mismatch/timeout fails retryably; no watch-cache fallback.
3. Pin the revision vector and actor for all validation of this candidate.
   Resolve all candidate role/binding relationships from its final tree,
   but derive the actor's authority only from pre-change captured state.
   A candidate cannot authorize itself.
4. Check operation permission and conservative grant coverage, or the
   explicit bind/escalate alternative. Check built-in/managed protections,
   scope, UID/lifecycle and quotas independently.
5. Accept the whole candidate or reject it before publication. Use expected
   old OID; a new candidate after ref conflict requires fresh capture.

The vector is not globally atomic. A revocation committing in another
repository after capture does not cancel the accepted write. A test must
exercise and document this case; it is not a stale-cache regression.
Validation has a 30 s maximum deadline; canceled/expired validation cannot
be reused indefinitely as a bearer permission.

Bindings to absent roles require explicit bind. Existing resolved
references pin target UID; candidate delete/recreate of a role cannot
reattach old bindings automatically.

## Verified common-admission boundary

The fixed OPA module now prepares a second, named coverage query. Proof covers
every expanded finite action and requested name, and accepts only unconditional
caller grants at sufficient scope. Unrestricted candidate names require an
unrestricted caller grant; enumerating current resources is never proof.
Candidate conditions may narrow otherwise covered authority. Owner/attribute
predicates and missing proof capability instead require explicit bind/escalate.

Each validation pins the selected provider, actor and authoritative tier vector
for at most 30 seconds, further bounded by caller cancellation and principal
expiry. Ordinary serving caches are not consulted. The whole changed-manifest
candidate is checked against pre-change authority; final-candidate relationships
cannot self-authorize. Updates preserve owned lifecycle/status metadata, and
deletion requires explicit intent: an empty YAML file cannot become a delete.
Author termination is published only with the committed projection, preserving
UID, generation, finalizers and quota until completion.

Focused race tests prove an admitted namespace write can finish after revocation
in the independent root repository, while a new candidate observes that
revocation. Expired captures cannot authorize, prove coverage or be restarted
with the expired context/identity. A target terminating after capture leaves the
binding unresolved without acquiring a claim; old resolved UIDs cannot become
proof for a recreated same-name role. The termination race uses the bounded
production committed-projection helper to rebase the tier's owned metadata,
without reauthorizing or altering the caller's admitted candidate.

Validation/protection denials retain bounded structured admission reasons;
incomplete authoritative state and failed evaluation
remain retryable unavailability, never a successful coverage result. The
race-enabled auth, RBAC, common-admission and local datastore contracts passed,
as did repository lint and the complete native Scylla datastore gate after
adding explicit committed termination/replay.

These are constructed common-admission and native datastore contracts. Public
Git/GraphQL admission, authoritative production origin lookup,
serving publication and two-replica convergence are subsequent
gates; this evidence does not enable dynamic production writes.

Ordinary manifest subjects are structurally validated, not looked up in
UserDir; an absent subject is admitted and grants nothing until a matching
trusted identity authenticates. Managed namespace-owner provisioning and
access review retain their separate trusted-directory requirements.

## Native Git and GraphQL parity

Use blocking pre-receive for candidate checks and the shared committed
admission pipeline for projection. Extend existing protobufs only for
missing candidate/tree/actor/revision metadata; preserve generated Go/Rust
stubs and receiver-bound signature verification. An external push author
field is not the authenticated security principal.

Post-receive is not a veto. If post-receive projection fails, the committed
OID remains authoritative and gets retried by bounded repair/reconciliation.
Ordinary decisions remain within their freshness contract or deny.
GraphQL returns partial-outcome commit diagnostics rather than pretending
the committed write never happened.

Admission checks the final candidate tree once, including removals and
renames. Multi-ref RBAC-changing pushes require atomic transport semantics
or are rejected as unsupported before any refs change. This restriction
must be explicit in operator documentation and protocol tests.

Only the canonical branch affects policy; accepting a branch for review
does not grant access. Merging/reverting into the canonical branch invokes
the current admission checks again.

### Native bring-up findings and remaining gate

The owned multi-process fixture uses two APIs, two independent controller
checkpoints, one native Git process and a separate single-node Scylla keyspace.
It exposed two additional regressions with failing-then-passing focused tests:
idle work-only cleanup queues must not require a resource snapshot when no
fan-out exists, and receive-pack must release its repository write lock before
validation calls back into canonical-origin reads. Publication reacquires the
lock and retains exact old-ref CAS; the concurrent-tip regression verifies
that releasing the lock does not authorize replacing an intervening commit.

Internal GraphQL backing writes now evaluate against the admitted request's
captured policy, rather than vetoing that capture when the serving cache becomes
unavailable. The override is restricted to that repository's internal content
write and retains the validation deadline. Other actors, repositories and
operations still use the live provider. Focused race-enabled tests cover those
boundaries; no global authorization fence or policy-free internal grant is added.

`tests/integration/rbac_git_graphql_test.go` now passes the complete four-kind
native authoring, wrong reserved-directory placement, whole-receive rejection,
protected roleRef and revert join. The expanded restricted-actor matrix also
passes create/update/delete, covered versus excessive grants, conditional and
resource-name-constrained delegation, absent-role binding, cluster containment,
and subsequent explicit bind/escalate through both transports. Known
resourceVersion conflicts refresh only the version of the same UID; unavailable
and unknown-outcome responses are not counted as denials. Temporary actor grants
are revoked with spec/UID/CAS checks, including after lifecycle-wait failures.

The real callback-loss scenario forwards signed native validation but suppresses
committed admission callbacks with gRPC unavailable. It verifies fault counters,
unchanged committed canonical OID, recovered Role/RoleBinding projections on both
APIs, exact source revisions and successful delivery after restoring callbacks.
This proves application-transport callback failure, not a physical network fault.

Snapshot capture scans the pinned immutable tree once, then rechecks live
Namespace/repository/ref identity rather than reading the same blobs again.
Changed or unavailable identities still prevent publication within the existing
three-attempt/30-second budget. Race-enabled snapshot, catalog and app tests cover
the unchanged and concurrently changed-tip paths. No authorization cache or
staleness extension is introduced.

Transient RBAC status authorization failures retain a redacted retryable error.
Controllers classify these failures and HTTP 503 as shared dependency outages,
apply their existing cooldown and durably defer unfinished work without consuming
the poison budget. Mutations are not replayed by the HTTP client; subsequent
reconciliation recomputes under UID/CAS. Ordinary forbidden and non-retryable
errors retain their existing handling.

Native stack diagnostics exposed hundreds of per-source stream readers waiting
behind serial checkpoint writes. Checkpoints now share bounded same-partition
conditional batches, retaining lease-holder/token/expiry fencing and existing
TTLs. Actual Scylla tests verify multi-batch persistence and zero checkpoint
changes from stale leases. The optional completed-query callback also advances
quiet streams in mixed query batches, only after successful consumption and
query closure; legacy consumer notification semantics remain unchanged.

Progress-only idle checkpoints now coalesce for 500 ms while resource events
retain their 25 ms flush cadence. A fixed 128-entry immutable tree-digest cache
removes repeated signed manifest reads only after complete verification; every
hit checks current live Namespace/repository/ref identity. Source freshness,
request deadlines, replay durability and payload bounds are unchanged.

`tests/integration/rbac_access_review_test.go` passes with a real restricted
UserDir identity on both APIs. It joins ordinary enforcement decision logs to
review results by subject, action, resource, exact tier/Git revisions and release
digests; covers scoped/self review permission, explicit hypothetical inputs and
cluster review separation; and verifies grant removal against actual reads.
Reviews do not advance the policy revisions.

These are functional transport proofs, not sustained freshness or capacity
acceptance. The fixture still permits bounded pre-execution unavailability
retries, and transient readiness failures must not be counted as policy
rejections or as proof of the five-second propagation SLO. Production activation
remains gated on the remaining runtime, recovery and rollout requirements.

`tests/integration/rbac_runtime_test.go` previously passed the initialized native
grant/revoke scenario. A real Kratos Staff directory supplies trusted group
membership; static-users authentication remains unchanged. Both APIs admit
Product writes through the namespaced `edit` binding, reject cross-namespace
reads/writes and deliver two native Product pushes on the original subscriptions.
After binding removal, both APIs deny reads/writes and the subscriptions terminate
without delivering a separately committed post-denial canary. Already-buffered
pre-revocation frames are not mistaken for new post-revocation delivery.

The watch fixture first joins real enforcement decisions to the post-setup
canonical Git tips on both APIs. Acquiring a subscription may retry explicit
pre-execution unavailability; established subscriptions are never reconnected
to manufacture stable-delivery evidence. This proof stays within the configured
bounded directory lease and does not claim renewal across directory-lease expiry.

Ordinary catalog mutation fields now retain their first complete serving
publication for at most 30 seconds. Domain decisions and internal backing work
continue using that field-scoped publication after its own commit invalidates
the live cache. The capture cannot cross namespaces or selected providers;
new operations still require current readiness, and closing/canceling the
operation releases retired publication memory. Red/green provider tests and
race-enabled auth, middleware, catalog and app suites verify these boundaries.
This is not the authoritative RBAC admission capture or a global commit fence.

A separate real cold API process attempts the unavailable canonical Git source
and remains unable to serve ready traffic during the observed outage. With the
same isolated configuration and the source restored, a fresh process becomes
ready, serves the retained administrator and preserves the actor's revoked
access. The fixture verifies process/listener cleanup. It does not yet establish
native cursor-loss recovery, mixed-version replacement or sustained load.

The app runtime now also exercises an expired journal cursor with the actual
manager, snapshot reader and OPA provider. Restoring journal reads cannot restore
readiness while complete origin scans fail. Trusted `system:masters` traffic
remains unavailable, and a failed namespace neither opens itself nor invalidates
an independently proven cluster or namespace tier. These cursor and namespace
faults use the app's memdb journal adapter, not a deployed Scylla epoch reset.

The native cold-process scenario additionally reconstructs a real group grant,
admits a Product write, loses canonical Git RPC access through its private HTTP/2
proxy, denies the retained administrator while unready and recovers in that same
process. The unaffected APIs remain ready. After revocation, another fresh
process reconstructs denial from the retained canonical state. Healthy child
processes shut down gracefully and release their source leases; cleanup is
bounded and verifies that the process listener closes. Git is never replaced.
This proves same-version process reconstruction, not mixed-version upgrades.

Healthy rebuilds now read the complete current snapshot before attempting
projection repair. A matching canonical snapshot needs no duplicate repair scan;
failed reads still invoke bounded repair and acquire a fresh complete snapshot.
Red/green app coverage and race-enabled app, manager and provider suites verify
that lost callbacks, cursor recovery and freshness checks remain enforced.

Fresh native runtime and cold-process runs now pass after the bounded postcommit
capture fixes. Original subscriptions on both APIs deliver the ordinary Product
pushes, then terminate after actual revocation without accepting the post-denial
canary. No established subscription is reconnected and freshness limits remain
unchanged. Actual isolated API processes reject unavailable-source cold startup,
reconstruct grants, fail closed during their own source interruption, recover in
the same process and reconstruct denial after grant removal. T061 is complete
for this functional initialized fixture, not mixed-version or sustained-load
acceptance. For committed deletion readback failures, the test helper requires
the exact canonical commit and eventual UID-checked lifecycle state without
repeating the deletion mutation.

## Store boundary

Define a single backend-neutral `RBACStore` with focused operations for:

- get by kind/scope/name or UID, and bounded paged list;
- read current tier revision/counts;
- apply complete admitted changes with expected old Git tip/tier revision;
- conditional lifecycle/status patches and final removal;
- list bindings by resolved role UID;
- acquire a complete immutable tier snapshot;
- report projection completeness and repair committed-but-unprojected tips.

All mutation methods include expected resourceVersion and UID where
existing identity is involved. No process-local authorization lock replaces
the datastore condition. Namespace identity is the UID.

The authoritative Scylla collection is `rbac_resources_by_scope` with a
same-partition revision/count sentinel. A bounded single-partition
conditional batch changes resources, tombstones, counts, Git projection
receipt and revision together. Expected-tip/CAS failures publish nothing.
Lifecycle updates advance the same tier revision. Memdb provides identical
observable atomicity.

Stable reads use sentinel-before, paged full-envelope reads, then
sentinel-after at consistency sufficient to observe completed conditional
writes. Retry a changing tier at most three times within the request
deadline, otherwise return unavailable. The page source is authoritative,
not an eventually repaired lookup index.

Verify native conditional-batch restrictions, response-loss behavior, full
CDC images and repair under real Scylla before implementing dependent
consumers. A per-row loop with partial visibility is not an acceptable
substitute. External admissions retain candidate row/byte bounds. Committed
catch-up can combine several already-authorized receives in one transaction
within the complete tier's resource and payload quotas.

Apply new tables in the next unused incremental migration (014 at planning
time). Never rewrite 001-013. Enable full-image CDC/14-day retention only
on the authoritative watched collection. Extend `RunCatalogCDC` with
discriminated row kinds and shared journal payloads; do not create per-kind
journals or extra CDC daemons.

## Verified store boundary

The backend-neutral contracts run through the existing datastore contract
factories, rather than a second backend-specific assertion suite. The optional
`RBACCapable` interface preserves existing datastore test doubles; the
instrumented wrapper preserves the capability and records each operation
through its existing SDK instruments. Storage envelopes live in
`internal/datastore/rbac.go` to avoid an auth-registry import cycle; domain
parsing and shared mutation planning remain in `internal/rbac/`.

The local gate and the complete native gate passed with:

```sh
make test TARGET=datastore
GOFLAGS=-p=1 make test TARGET=datastore DATASTORE=scylla SCYLLA_TEST_ADDR=<owned-address>
```

The native fixture used Scylla 2026.1 on Linux/arm64, one shard, one node and
replication factor one, with test-owned keyspaces. Serial package execution
avoids competing migration/CDC schema churn on that small fixture; the
contracts still run simultaneous quota, status and claim/termination writes,
including two independently constructed Scylla stores sharing a keyspace.
This is functional transaction evidence, not replicated availability or
production throughput evidence.

The tests verify whole-candidate CAS/count/receipt publication, replay after
discarding a successful reply, stable bounded snapshots, full CDC preimages
and postimages, and recovery after removing and restoring a test-owned
lookup table. Reply discard models an uncertain client outcome; it does not
establish physical network-fault recovery. A pending secondary-index repair
does not hide the complete authoritative snapshot, but blocks subsequent
mutations until repair completes. Terminal UID-only lookup tombstones prevent
delayed repair from resurrecting a removed lookup.

Race-enabled model, existing static-provider and datastore suites, and
repository lint also passed. The original owned container was removed afterward.
The foundation join reran the complete native gate on a new independently
owned fixture after adding bounded receipt decoding and committed projection
repair. Durable RBAC journal delivery is verified separately in the
[snapshot/provider contract](snapshot-provider.md); serving OPA activation,
production transport activation and deployed fault/load acceptance remain
unfinished.

## Verified foundation join

The committed-candidate helper preserves Git identity after response loss,
caller cancellation and projection failure. It uses no post-commit
reauthorization or rollback: at most three apply attempts and independent,
bounded repair recover exact receipts without replacing a newer Git tip.

Model-v1 capture verifies trusted live origin before and after the authoritative
scan, matching namespace UID/lifecycle, repository/ref/commit/tree, catalog and
release digests. Source changes and page changes share three total attempts,
not nested retry budgets. The concrete production origin adapter is still
required before transports can use this capture.

Release built-ins have stable incarnation UIDs and deterministic content
digests; only a private release set establishes trusted provenance. Resolution
acquires a durable role claim before publishing a binding link. Pending-claim
recovery fences unresolved publishers by binding resourceVersion before release
after acquisition closes; uncertain reads retain blocking claims.

The Admin schema now declares four Node envelopes, scoped one-of selectors,
connections, expected-version writes/purges and typed journal watch payloads.
It reuses the generic JSON-boxed watch/status contract rather than inventing a
generic typed union absent from the existing API. Conditions use a typed one-of
token/attribute adapter because the existing JSON scalar accepts objects, not
the manifest's mixed string/object array. Admin reads, authoring, status/finalization and durable watch adapters are now
implemented. Dynamic authoring/status remain unavailable without the explicitly
injected validator; production app activation is not enabled. Lifecycle finalizer
input remains rejected for other kinds.

`ResourceValidationTree`, `ResourceBlob`, `ReceiveRef` and
`CallbackAuthorization` bind changed trees, paths/content/blob OIDs,
old/new commit OIDs, trusted actor and the whole receive. Two additive RPCs
close missing protocol guarantees: `CommitFileAtRevision` publishes a GraphQL
candidate with an expected-old-OID CAS, and `ValidateRBACReceive` requires a
complete proposed RBAC subtree, including unchanged review-branch resources.
Older binaries return UNIMPLEMENTED before writing; neither call falls back
to its legacy counterpart. Empty optional fields preserve existing wire
goldens. RBAC-changing multi-ref receives are rejected before refs move.

The full-tree callback retains the 256-change limit and common admission's
4 MiB proposed-change limit (native packaging independently caps 8 MiB)
independently of the 5,500-resource/64 MiB final-tree limit. A 96 MiB RPC receive
bound includes old/new changed blobs and path/protobuf overhead. Native tests
exercise payloads above gRPC's default limit. Final-tree reference proof uses
the proposed role, but actor authority always comes from the captured serving
tiers. Canonical final-tree digests must match the captured projection plus
the exact candidate overlay.

Admin versions bind native resourceVersion to resource UID, preventing a
same-name recreated resource or Namespace from reusing a coincident numeric
version. Tests cover all four CRUD/Node/list families, actual Git actor and
receipt attribution, immutable roleRef, conflict rebuilding against a
concurrent push, revocation on recapture, unknown outcomes without reauthoring
and known-commit projection failure diagnostics. Status/purge have independent
permissions, preserve spec ownership and require UID/version CAS; a live role
claim blocks purge until its terminating binding releases the claim.

Typed/generic watches share the durable journal, opaque cursors, replay and
selector transitions for all four kinds. Internal tier progress becomes only
a payload-free bookmark, never a public sentinel resource. Constructed tests
and native hook tests are not deployed replica-convergence evidence.

The gated native post-receive adapter reads canonical Namespace/repository
facts and the complete immutable Git tree, then checks the identity again
after scanning. It projects accepted content without reauthorizing the
historical actor, preserves UID/status/finalizers, terminates removed resources
and retains terminating rows until guarded purge. Unrelated canonical commits
advance the tier identity without changing resource versions. Delayed callbacks
cannot roll back a newer tip or attribute its content to an older actor.
Ordinary catalog callbacks retain live ServiceAccount and policy checks.

Signed callback tests cover invalid partial trees, lost projection acknowledgments,
known-commit failures and fresh signed repair replay. A catch-up exceeding one
external candidate is one publication, not visible per-candidate batches.
The backend-neutral native Scylla contracts verify this larger committed
transaction and index repair; full-image CDC remains bounded by complete-tier
quotas. Native receive and recursive canonical `rbac` listing share the same
bounded regular-file/tree validator; canonical reads do not open unrelated catalog
subtrees. Retained storage and older Git binaries still require the planned
compatibility/cutover validation before activation.

An internal canonical-tier repair entry point reuses this planner after an
entirely lost callback. It records `system:rbac-projection-repair` as the repair
writer and retains the immutable Git revision for source authorship; it does not
fabricate an authenticated historical actor or reauthorize committed content.
Focused tests cover lost callbacks, effect-free replay and deletion suppression.
Periodic origin checks and repair run through private opt-in application
composition with the selected provider, readiness gates and purpose-bound signed
Git transport. Composed-server tests cover cold Namespace repository provisioning
without requiring its absent policy tier, while checking the live enrolled
controller and ordinary global provisioning permission. Production activation
and deployed automatic recovery remain part of the resync/bootstrap gate.

Foundation verification:

```sh
make generate TARGET=all
make _check-generated
go -C gitstore-api test -race ./internal/auth/... ./internal/rbac ./internal/admission ./internal/admin/graph/resolver ./internal/cataloggrpc ./internal/datastore/... ./internal/watchjournal ./internal/app ./tests/contract/datastore -count=1
GOFLAGS=-p=1 make test TARGET=datastore DATASTORE=scylla SCYLLA_TEST_ADDR=<owned-address>
make lint
git diff --check
```

## Durable delivery and snapshot installation

CDC rows belonging to one candidate can be delivered separately.
Consumers coalesce them into a dirty-tier signal and acquire a complete
stable snapshot; they never publish a partially observed grant set.
Ignore superseded/duplicate dirty notifications only after checking tier
identity/revision. Bound queue and rebuild concurrency; overflow invalidates
readiness and forces resync.

Capture a shared journal cursor before scanning. Keep a watch subscribed
through scan/installation, and retain any newer dirty revision. Install
snapshot and cursor state atomically only when source progress is fresh
and continuity is verified. A full tier snapshot can subsume earlier
events, but must not discard newer revisions hidden in a buffer.

The shared reader must demonstrate contiguous history or report cursor
loss; event timestamps alone are not evidence of source freshness.
Namespace lifecycle invalidation is part of the consumed stream.

## Primary lifecycle group

The private opt-in controller constructor now registers four kind-specific
disk-v2 runners sharing one provider-neutral reconciler. Component tests verify
separate replica checkpoint roots, persisted pending work and nonoverlapping
reopen after replacement. These are storage/composition proofs, not evidence of
two live deployed controllers. Production registration remains disabled.

Typed clients use bounded pages with visitor backpressure, UID-bound status and
completion, durable journal cursors, scope-checked images and cancellation-safe
subscriptions. Role completion closes acquisition and scans pending claims in
1,000-row pages within its deadline before authoritative guard finalization.
Unknown/live references remain blocking; finalized guards survive a failed
resource-removal attempt without preventing retry. Binding changes enqueue their
referenced role so release can unblock finalization. Namespace cascade and its
old-incarnation cleanup use the durable handoff described below.

The shared status encoder also supplies RBAC condition timestamps at the
GraphQL scalar's required millisecond precision. Regression tests exercise
unresolved, resolved and terminating binding requests; existing resolved UIDs
are not resubmitted for attachment when only conditions change. Concurrent
controller tests force the same old observation: one status CAS wins, the
other retries from the new observation, and delayed old-UID completion cannot
remove a recreated binding. Replacement tests also recover unresolved-to-resolved
transitions and committed status/finalizer/deletion replies lost to the caller.
Completion waits for the new UID-bound version instead of reusing the
pre-finalizer observation. Authoritative reference tests separately reproduce a
crash after claim acquisition before publication and a lost release reply after
grant suppression; fresh coordinators retry without duplicating claims or
reviving the binding.

Existing USER Namespace catch-up reuses the enrolled Namespace paged-list
and disk runner rather than a second scanner. Tests stream separate pages
into each replica's private disk-v2 snapshot, retain pending provisioning work
and immutable owner subjects, stop before reopening the same volume, and
resume after a watch bookmark without an unnecessary complete relist.
Owner-client tests recover a committed create after response loss by reading
the deterministic binding before retrying; another replica does not issue
another create. Common-admission and Admin wire tests reject ordinary
owner/administrator/controller deletion or replacement of the managed binding,
verify trusted Staff subjects distinct from usernames, and retain initiating
owner annotations. After resolving the binding so its owner actually has
namespace-full-control, Admin deletion and signed ValidateRBACReceive callbacks
still reject owner, administrator and controller removal. Both an empty
pre-template revert tree and a removal accompanied by unrelated content veto
the whole receive. These are local contract/composition checks with constructed
Git trees; native Git transport parity and deployed two-controller gates
remain outstanding.

One `rbac` controller group owns the RBAC finalizer, resolved role links
and status. Reuse PagedListWatcher, disk-v2 work/checkpoint/indexes,
StatusClient, enrolled GraphQL credentials, bounded workers and backoff.
Replicas do not share checkpoint directories. Duplicate work is safe by
UID/resourceVersion CAS, recomputation and deterministic desired output.

Use authoritative reference guards at completion; relation projections
accelerate discovery but cannot prove absence when stale or errored.
A concurrently admitted binding cannot be attached to a terminating role.
An explicit-bind unresolved reference to an already terminating target
stays unresolved and has no blocking resolved UID link.

For cross-tier ClusterRole references, use a durable role-UID guard with
same-partition lifecycle/version sentinel and idempotent binding-UID claims.
Acquire a claim conditionally while the role is live before publishing a
resolved binding. Role termination closes new claims; final removal is
conditional on no claims remaining. Release only after the binding no
longer contributes permissions. Crash recovery reconciles pending claims
from authoritative binding UID/state and retains uncertainty as blocking.
This is per-role lifecycle coordination, not a global authorization fence.
A stale validation can still commit an unresolved binding if its referenced
role terminated after capture; it cannot force a resolved link to that role.

Role finalization waits for resolved bindings; binding finalization releases
its reference. Namespace cascade removes children without blocking the
Namespace, and a namespace tombstone suppresses grants independently of
cleanup completion.

After proving Namespace manifest removal, deletion persists an immutable
UID/name handoff before removing the parent. Backend-neutral contracts cover
idempotent scheduling/completion, name conflicts and paging after completed
jobs disappear. The consolidated migration 014 stores the handoff in 64 deterministic partitions
and adds the retained per-Namespace tier marker; idle discovery is bounded by
those partitions and never scans catalog resources.

One cleanup invocation retires the old UID's tier and processes at most 100
bindings or roles within 30 seconds. Retirement rejects delayed projection,
including exact receipt replay, and authoritative snapshots reject retired
tiers even if an origin adapter incorrectly reports the old parent live.
Binding suppression precedes claim release; final role removal still requires
closed acquisition, pending-claim recovery and an authoritative zero-claim
guard. Foreign finalizers and uncertain parent reads retain the job. A
same-name replacement's actual resources remain untouched.

The private controller constructor registers an internal disk-v2 poll schedule
in the primary group. Manager dispatch/requeue and nonoverlapping disk reopen
are tested without requiring a resource snapshot for this internal work.
Only the current enrolled controller with global Namespace purge permission
can call the maintenance operation. Per-job failures retain work but advance
the discovery cursor so another parent can progress.

Native Scylla contracts pass for the handoff, retired tier, child finalization,
claim release and migration compatibility. Race tests additionally cover
duplicate cleaners, lost apply/completion replies, page bounds, foreign
finalizers and Namespace replacement. These are component/storage proofs,
not two-controller deployed acceptance or throughput measurements.

## Managed personal-owner binding

The 2026-11-11 clarification preserves the winning AuthN provider's subject
through namespace admission, ownership and User binding subjects. Both OPA
and rbac-local now compare `subjectUsername` against that authenticated subject,
while retaining the trusted-human-profile requirement. Race-enabled provider,
enrichment and namespace tests pass, including directory usernames distinct
from the authenticated name; mixed OIDC/static-user chain tests verify that
the first successful provider retains its subject, issuer and authentication
method. Downstream resolver, catalog callback and app suites also pass.

Common and native namespace admission now retain the authenticated owner's
method and issuer in protected metadata annotations alongside the existing
owner annotation, without new datastore columns or ownerReferences. Omission
preserves established facts; explicit forgery/change is rejected. Only the
authenticated owner can establish missing legacy provenance. Qualified
directories resolve this original principal through `ResolvePrincipal`;
legacy directories retain `GetBySubject`. Neither path substitutes a directory
UID/username for the owner, and both require a current active Staff profile.
Missing qualified provenance fails unavailable rather than guessing.

Signed Git callbacks now preserve issuer alongside subject/authentication
method, reject signed-issuer tampering, and use bounded live directory enrichment
without changing the AuthN-selected identity. Committed RBAC projection retains
its historical-actor reauthentication exception. Race-enabled auth, namespace,
admission, callback, resolver, app and RBAC suites pass, as do all 189 native Git
library tests. These are component results, not T064/T073 deployed acceptance.

T094 must still prove the reserved-annotation cutover: older writers do not
protect these keys, so preexisting authored values are not proof of verified
provenance. Freeze/drain incompatible writers and establish trusted legacy
facts before enabling qualified owner catch-up. Do not claim automatic legacy
adoption, mixed-version protection or safe provider/issuer retirement from
new-binary tests alone. Production activation remains gated.

`TestRBACNativeGraphQLOwnerAuthentication` and
`TestRBACNativeGitOwnerAuthentication` now pass against the owned
Scylla/Kratos deployment with two APIs, two independent controller checkpoints
and singleton native Git. Each creates a fresh USER namespace,
checks protected source facts and the exact User subject, observes usable owner
access on both APIs, rejects access to the other namespace, and waits for exact
incarnation deletion. The preceding failed-response namespace was also recovered
through an ordinary update and controller replacement, with both-API access and
isolation checked before guarded completion. The native four-kind Git/GraphQL
parity suite also passed with issuer-carrying callbacks.

This verification exposed and fixed coupled integration faults: committed
readback must include server-preserved owner annotations; Namespace status UID
preconditions must accept the API's typed Relay representation without accepting
another kind or incarnation; managed templates must use the decoded canonical
UUID; and provisioned system repositories must retain their self storage ID,
including guarded recovery of incomplete deterministic rows. Namespace storage
finalization uses current cluster policy only for API-owned deletion of verified
terminating system storage. Target namespace facts remain in policy input, local
grants stay retired, other actors/actions retain normal tier checks, and missing
cluster proof still fails unavailable.

Native verification additionally exposed postcommit reads losing authorization
while their own complete RBAC projection republished. Ordinary callbacks now
retain their first complete serving publication for the same bounded operation
as GraphQL writes. Initial unavailable ref reads retry within that operation's
30-second deadline; denied reads never retry. A failed initial capture returns
unavailability rather than closing a typed-nil evaluation and crashing the API.
Component race tests cover recovery, denial, cancellation and later successful
capture. Signed actor verification, live directory checks and tier/scope bounds
remain enforced.

T064/T073 remain open for the complete migration, conditions, deliberate
collision/failure and stale-UID grant matrix. Both creation transports and
component race suites do not close that broader gate. Old/new compatibility
remains part of T094.

The owned native catch-up/replacement slice additionally passes injected
provisioning/admission faults, Pending/Failed/BindingAdmitted transitions,
retained Namespace snapshot generation, both-API access/isolation and Namespace
finalization. Checkpoint proof uses post-shutdown read-only metadata receipts:
HTTP list queries cannot identify the Namespace runner because RBAC enumeration
reuses its watcher.

The full unprofiled same-name Namespace-recreation scenario now passes on both
APIs, including a new managed-binding UID and denial of the stale ordinary
subject against a same-name replacement Role. The catch-up/replacement scenario
also passes again. Cleanup proves the old UID absent from both APIs rather than
using name-index disappearance as finalization evidence.

Native failure diagnosis found that a losing Namespace completion CAS restored
indexes after a peer deleted the authoritative UID. Completion now commits that
UID CAS before projection cleanup and never restores a loser's indexes.
Full-image committed CDC deletions confirm authoritative absence and resume
exact UID-bound cleanup, preserving same-name replacements. Fresh-keyspace
orphan/replacement regressions fail before the fix and pass afterward under
race. Retained-fixture restart repaired the previously orphaned projections
without storage or journal reset. This does not close the complete collision,
migration, mixed-version or sustained-load gates.

The expanded owned recovery tests reject administrator-authored ordinary
`namespace-owner-access` collisions on both APIs before provisioning resumes.
No binding or implicit owner grant appears. After controller admission, denied
subject replacement and deletion preserve the managed binding UID/spec.
The expanded unprofiled recreation run passes; a preceding cleanup attempt
hit authorization unavailability and required exact guarded recovery. Neither
that failure nor the finite functional pass establishes sustained availability.

The fixed name is `namespace-owner-access`; identity includes Namespace UID.
The primary RBAC controller watches admitted USER namespaces and derives
the User subject from the trusted Staff owner mapping. It cannot accept
subject/role claims from a request, Git annotation or checkpoint alone.

The enrolled controller receives named, cataloged write/bind/status/purge
capabilities for its legitimate lifecycle operations. Admission always
enforces the managed template: correct namespace UID, live USER tier,
verified Staff owner, fixed ClusterRole `namespace-full-control`, correct
ownerReferences and managed provenance. A colliding ordinary manifest
fails closed; no automatic adoption. External administrators cannot use
the template merely by naming it or copying annotations.

Status on the existing Namespace reports owner-access Pending/Ready/Failed,
bounded reason and admitted binding revision. Ready there means binding
admitted, not that every API has consumed it. Retries are durable and
backed off. Same-name namespace recreation has a different UID and stale
work must fail before changing either identity's binding.

Common-admission and GraphQL tests now prove the deterministic template, trusted
Staff subject lookup, external administrator rejection and controller-only
managed status. The Namespace hook records Pending/Failed/BindingAdmitted,
preserves unrelated conditions and reports the admitted binding revision without
claiming per-API readiness. Its status writes carry the observed Namespace UID;
an old incarnation cannot update a replacement at a coincident numeric version.

## Bootstrap

Load release built-ins before constructing dynamic snapshots. A narrow
in-process bootstrap path may seed only the configured initial-admin
binding into an otherwise uninitialized root scope under common admission.
Persist its commit and once-only receipt in existing root Namespace
bootstrap status using CAS. Crash after commit is reconciled from that
commit, not by inserting another binding.

After initialization, deleting the ordinary initial-admin binding is
allowed and persists across restart; config does not silently re-create it.
System bindings remain immutable virtual resources. `system:masters`
has no default members.

If Git/bootstrap/projection is unavailable, expose retryable readiness
failure and continue only bounded internal recovery. Never open external
authorization with just a temporary process-local admin grant.

The internal initializer now persists a root-UID-bound intent before writing.
It constructs only `ClusterRoleBinding/initial-admin` for the persisted User
subject and fixed `cluster-admin`; no request input selects this capability.
Common admission still checks the complete empty authoritative root snapshot,
canonical repository/ref, namespace version, protection and limits. It does
not add `system:masters` membership or replace the serving authorizer.

The guarded Git write optionally retains a private receipt keyed by the exact
repository/ref/parent/path/content/author/message. Receipt-only probes cannot
publish or resume a prepared write. Lost acknowledgments are reconciled from
proven publication in bounded branch history, even if the seed was subsequently
removed. A published write cannot be repeated after resetting to its parent.
The existing prepared-object sync and consumed recovery marker protocol also
covers these retained writes. Ordinary writes keep their original conflict
semantics when receipt retention is not requested.

Root Namespace receipt identity is immutable, commit completion is monotonic,
and controller status patches cannot create, replace or erase it. Both datastore
backends use the same merge rules under UID/resourceVersion CAS. The initializer
uses the existing role-UID claim/resolution protocol only for the exact seed,
so the first administrator can enroll the lifecycle controller; it never
reconstructs a missing, terminating or subsequently edited administrator.

Component evidence covers concurrent initializers, unavailable dependencies,
commit acknowledgment loss, removal before receipt completion and restart.
Native tests cover retained-write recovery, prepared-content validation and
branch-reset refusal. The receipt CAS contract also passes on owned native
Scylla. These component fixtures alone do not establish deployed bootstrap
acceptance or production activation.

The initialized canonical runtime now runs one serial, deadline-bounded
initialization retry loop sharing the existing scratch budget. Failed attempts
release reservations. Both readiness and ordinary Provider decisions (including
masters/internal identities) stay unavailable until the resolved seed's exact
revision/content digest is actually published. Journal shutdown also closes
serving availability. The owned native application path supplies purpose-bound
internal Git recovery transport, not ordinary cached authorization for the reads
needed to establish that same cache. Production activation remains disabled;
there is no general internal fast path.

`TestRBACNativeBootstrapRevocationSurvivesProcessReplacement` now passes against
a fresh owned native Scylla keyspace, isolated singleton Git storage and two
real API processes using static-users AuthN with qualified Kratos enrichment.
Both APIs expose the same ordinary initial-admin binding; attempts to recreate
or delete the controller built-in role and binding leave them unchanged.
Deleting the ordinary administrator revokes privileged reads on both APIs
with `FORBIDDEN`, not merely temporary unavailability. Replacing each API in
turn without overlapping its predecessor restores readiness without restoring
the removed grant. Both replacements use the same source binary and contract:
this is not evidence of an old/new built-in release upgrade or controller
enrollment. Those remaining T080 scenarios and the T094 rollout matrix are
still pending.
