# Tasks: Translation Frontmatter Support

**Input**: `/Users/julius.krah/Documents/GitHub/GitStore/.gitstore/worktrees/translation-contract-spec/specs/071-translation-contract/`
**Prerequisites**: [plan.md](plan.md), [spec.md](spec.md),
[research.md](research.md), [data-model.md](data-model.md),
[authored contract](contracts/authored-resource.md),
[lifecycle contract](contracts/lifecycle-and-storage.md),
[capacity contract](contracts/capacity-and-rollout.md), [quickstart.md](quickstart.md).
**Delivery state**: Implementation in progress; unchecked tasks and deployed acceptance remain incomplete.
**Tests**: Required by the specification and constitution. Write each slice's
tests first, observe the relevant failure, then implement and run the smallest
covering suite. Capacity and physical-fault acceptance are separate opt-in work.

**Current checked count**: 78/83 complete, 5 open: T077-T080 and T083.

**Interrupted migration correctness**: T080's isolated Scylla regression
reproduced a real restart failure when an additive column committed before its
ledger progress update. Recovery now runs under the normal migration lock,
checks the unchanged migration prefix/checksum and exact existing column
type/kind, and advances only unfinished progress with CAS. Changed checksums,
incompatible columns and inconsistent history are rejected without advancing
progress; completed history and the existing Product UID/body/revision/version/
generation remain intact. The final native race run passes this regression
and three existing interrupted target-audit/backfill/failure-watermark cases.
The earlier four-test run also passes read-only validation, migration
idempotence and refusal of old migration bundles. These use isolated RF1
test-owned keyspaces on the owned Scylla cluster, not a full deployed
mixed-version/drain/capable-rollback rehearsal. T080 therefore remains open.
No applied migration file, retained corpus or performance setting changed.
The full default `GOWORK=off GOFLAGS=-p=1 CARGO_BUILD_JOBS=2 make pr-ready`
rerun passes after this production correctness fix, including lint/build/test/
race/license and credential-leakage checks. Whitespace and user-facing
reference checks pass. The refreshed AST graph has 30,369 nodes, 77,215 edges
and 1,460 communities with the existing optional-parser/zero-node warnings.

**Current priority**: Correctness only for this delivery scope; capacity failures
are acceptable and do not trigger premature performance tuning. Finish safe
retained-data recovery and correctness/rollout verification; preserve bounded
work, ownership and failed measurements. T077-T079 remain deferred qualification
gates, not waived or claimed passes. ProductVariant lifecycle planning in #482
needs the established
source/status/watch and fenced-writer contracts, not a claim that Translation
has passed production capacity certification. Its implementation must preserve
Translation lifetime fences and invalidation; it is not implemented here.

**Verifier and documentation completion**: T073 and T076 are complete.
The public oracle now verifies API and controller replacement resource epochs
and bounded independently declared gaps, retaining per-process CPU/GC/heap/
goroutine baselines and unchanged peer joins. Controller captures independently
join writable retained checkpoint identities to preflight and reject equal,
nested or volume-aliased storage. The existing physical runner now preserves
those captures and rejects changed target as well as peer checkpoints.
Shared API/controller fixtures retain the seventeen common negative cases;
eight additional controller checkpoint negatives and two runner restoration
negatives pass. The earlier API/journal source/restoration negatives remain
passing. No synthetic result is classified as deployed acceptance.
The final path-alias regression exposed double-root (`//`) paths that could
evade raw prefix overlap checks; both the verifier and runner now reject them,
with matching original-source captures retained in the negative fixture.

The capacity, chaos and script guides describe implemented selectors,
ownership, exact diagnostic windows and explicit acceptance limitations.
Previously successful real-ingestion guide/quickstart evidence still matches
the unchanged native test source; its SHA-256 remains
`50a1af19289e0b102e22bbb1f4ee2a3b06ec0bd7307511212fac00acc41c004f`.
It includes the eight lifecycle pushes, six guide rejections and three deferred/
ordering cases with the common #40 status and #549 enum regression assertions.
Generation, dispatcher, shell syntax, whitespace and user-facing reference
checks pass. The final full default
`GOWORK=off GOFLAGS=-p=1 CARGO_BUILD_JOBS=2 make pr-ready` passes, retaining
lint/build/test/race/license/credential checks. The final focused dispatcher
rerun also passes against the checkpoint path-alias fix.
T081 and T082 are complete: generation and standard checks pass; AST-only
refresh reports 30,367 nodes, 77,197 edges and 1,438 communities with the
existing zero-node/optional-HCL-parser warnings. The root instruction link
already points to this plan, whose navigation and diagnostic support boundaries
are reconciled without unrelated instruction edits. Deployed acceptance
and rollout gates remain open. The automatic companion still selects simulated
faults only and does not assemble combined physical/resource receipts.

**Prior deployed blocker**: Read-only checks could not connect to either the
Docker Desktop or default daemon context, and neither retained Admin API was
reachable. Ownership and retained storage could not then be reverified.
No daemon startup, process recreation, fault, load or storage cleanup was
attempted in the shared environment. Deployed acceptance/rollout and final
requirements closure remain open; local checks cannot substitute for them.

**Verified retained restoration**: After Docker was made available, the eight
original owned processes were restored in place after exact identity, image,
configuration and storage checks. Only absent/empty default DNS arrays were
normalized. Git remained singleton-only and controller checkpoints independent.
Finite verification through both APIs passed for all 1,009 original documents;
corpus, cases, baselines, targets and original Git refs matched the retained
authority, with all eight process epochs/images/mounts unchanged during the
verification. This proves retained correctness after restoration, not a
predeclared fault, capacity result or deployed rollout qualification.

**Recovery-source correctness progress**: T073's local oracle now requires
an acknowledged measured commit, an observed restoration time and two exact
current API recovery observations before sustained traffic. Ten test-first
negative cases exposed previously accepted missing/stale/aliased recovery
claims. Replacement epochs must begin by restoration, not merely before traffic.
The public Python verifier independently requires
`trafficProof.recoveryReads`; six additional dispatcher tampering cases reject
missing reads, duplicate APIs, pre-restoration reads, changed UID/body and
historical Ready. Native recovery collectors take two fresh authorized snapshot
observations for the private source fingerprint rather than copying earlier
push/watch evidence. Ordinary load reads retain their existing cheaper path.
Focused race tests pass in 11.529 seconds, integration vet and the public
dispatcher/shell checks pass. AST-only refresh reports 30,364 nodes, 77,189 edges
and 1,439 communities, with the existing optional-parser/zero-node warnings.
The first subsequent full default PR suite was interrupted. Its complete
rerun now passes `GOWORK=off GOFLAGS=-p=1 CARGO_BUILD_JOBS=2 make pr-ready`,
including the new source/restoration evidence regressions, races, license and
credential-leakage checks. Generation also passed before the interrupted run;
no generated contract changed afterward. No native fault, sustained capacity
or runtime replacement was executed. Physical resource/epoch/gap joins and the
formal T073 completion gate remain open.

**Physical journal oracle progress**: The public diagnostic verifier now
accepts an independently declared physical journal pause inside simulated
callback-loss recovery only with complete owned Scylla before/during/after
captures, observed pause/unpause, immutable image/container identity, unchanged
process epoch, bounded runner timing, full-window API/controller resource
checks, ordinary acknowledged-source replay, fresh both-API observations and
five seconds of correct traffic. A combined local fixture passes; thirteen
tampering cases reject absent pause/captures/resources, wrong ownership or
source, image/epoch changes, OOM/unrestored state, late restoration, failed
injection, health-only proof and missing declaration. Fixture preparation also
exposed and corrected mismatched test-source provenance; the oracle's source
checks were preserved.

The existing native recovery fixture now embeds the chaos runner's sanitized
target captures in its protected receipt. The automatically coordinated
connected companion still schedules simulated transport faults only; it does
not assemble a combined physical/resource receipt. At this slice's completion,
API/controller replacements and declared scrape gaps were still rejected by
the public verifier. Combined
local Translation race tests and vet pass (13.139 seconds), dispatcher and
shell checks pass. The latest AST refresh is 30,366 nodes, 77,193 edges and 1,457
communities with the existing warnings. Full default
`GOWORK=off GOFLAGS=-p=1 CARGO_BUILD_JOBS=2 make pr-ready` passes against this
slice, including race, shell, license and credential-leakage checks.
No physical fault, capacity run, runtime replacement
or new deployment qualification was performed; T073 and all nine formal
remaining tasks stay open.

**API replacement resource-oracle progress**: The public verifier segments
original resource scrapes by native process start and UUID, retaining separate
CPU, GC/heap and goroutine baselines for each epoch. An independently hashed
`services.translationResourceGaps` declaration identifies the API role, owned
container, physical fault and bounded maximum duration. The scrape transition
must bracket observed injection/restoration; target container/image/start
captures and an unchanged owned API peer join preflight. Resets inside an
epoch, missing or duplicate declarations, oversized gaps, stale/restoration
clocks and overlapping/aliased identities fail closed. One combined synthetic
API replacement fixture and seventeen negative cases extend the existing
dispatcher. Controller replacement and automatic combined physical/resource
collection remain unimplemented. The existing opt-in native recovery collector
now retains sanitized surviving-peer captures for replica restarts alongside
the target captures. The dispatcher, shell syntax, whitespace and user-facing
reference scans pass; local Translation race tests pass in 13.121 seconds and
integration vet passes. Full default
`GOWORK=off GOFLAGS=-p=1 CARGO_BUILD_JOBS=2 make pr-ready` passes, retaining all
default lint/build/test/race/license/credential checks. A final focused
dispatcher rerun also passes after strengthening the reused/overlapping
identity negatives to preserve matching original-scrape provenance and making
the GC-reset case use two positive cycle counts. The final AST-only refresh
reports 30,366 nodes, 77,194 edges and 1,437 communities; existing optional HCL
parser and zero-node warnings remain. No deployed fault, replacement or
capacity acceptance was performed. T073 and all nine formal remaining tasks
remain open.

**Standard-check progress**: `make generate TARGET=all` passed. The default
integration selector now includes local Translation authoring, read/watch and
evidence-oracle regressions, retaining race checks and all previous selectors
without enabling native deployments. That combined local race suite passed in
41.335 seconds. Test-first dispatch checks exposed the missing selectors; the
corrected dispatcher passes. Full-suite execution also exposed stale shared CI
setup counts and an unsupported-kind test that still listed now-supported
Collection/ProductVariant watches. The fixed API watch-contract suite passes
with races in 2.019 seconds.

`GOWORK=off GOFLAGS=-p=1 CARGO_BUILD_JOBS=2 make pr-ready` passes again against
the final quickstart/lifecycle fixture and deterministic cross-read regression,
after another successful `make generate TARGET=all`. It retains the full
default lint/build/test/license/credential suite, including Rust forwarding,
all Go modules with races and coverage, and the expanded local integration
selector. No checks were removed and no deployed capacity run substituted for
them. Final whitespace and shell syntax checks also pass. AST-only refresh
completed after the final code edit: 30,359 nodes, 77,172 edges and 1,455
communities. Existing zero-node and missing optional HCL-parser warnings remain;
this is not semantic documentation extraction.

**Executable quickstart/lifecycle progress**: The opt-in
`TestTranslationNativeQuickstartLifecycle` passes with races in 34.07 seconds
against the unchanged owned deployment. Eight acknowledged lifecycle pushes
verify named-selector reorder, genuine selector removal/restoration, target
delete/recreate, sticky binding after an unrelated source edit and explicit UID
retargeting through both APIs. Six exact guide candidates reject the whole
receive, including a valid companion, without advancing refs. Three deferred
target/candidate-order cases also pass. Source UID/generation/revision and target
material assertions retain the common status and enum contracts.

Independent post-pass verification compares all six original authority fields
(`corpusSha256`, `verifiedDocuments`, `references`, `cases`, `baselines`,
`targets`) across all 1,009 retained sources; every field, original ref and all
eight process epochs remain unchanged. Test provenance is the current dirty
worktree against the existing runtime, not immutable runtime qualification or
capacity/rollout acceptance. Failed attempts and successful receipts are retained
privately. The fixture and quickstart now distinguish an absent selector from
an omitted optional Product option title, which canonicalizes to empty text.
Bounded polling waits for joined public/controller identity, generation and
revision, retries transient binding unavailability, and never treats an
unclassified retrieval failure as proof of target absence. A deterministic
local regression covers a target update between the two reads. The final
combined authoring/integration race suite and integration vet also pass.

One full rerun exposed three permission-fixture failures caused by a private-log
`umask` leaking into the test process. All three focused regressions pass with
the normal fixture `umask`; the subsequent full default rerun passes while its
log remains private. No production code or security checks were weakened.

T081/T082's current-source check and navigation work is verified, but their
final checkboxes retain the declared story-completion prerequisites: remaining
implementation changes must rerun standard checks and refresh the graph before
an implementation PR. T073/T076-T080 and final T083 acceptance remain open.
Capacity workloads and runtime replacements are deferred, not certified.

T064 is complete. The opt-in native fixture now covers both configured Smart
HTTP ingress URLs, exact source/body/patch reads and durable watch joins through
both API endpoints, all five target kinds, equivalent-locale identities,
deferred target arrival, both candidate orderings, target material/resolved
invariance and whole-receive negative cases. Independent SHA-1/SHA-256 Git
round trips, normalization/path-escape rejection, source/freshness and watch
decoder regressions pass with races and vet. Its exact query documents validate
against the generated API schema with clean covering API static checks.
The owned native fixture passes end to end in 42.91 seconds. Real
receives accepted all five target kinds and independently named Translations.
Execution exposed fixture assumptions about empty Relay progress, target
condition generations, public/private IDs and canonical-locale casing; the
covering local regressions now enforce the existing contracts. It also exposed
static-provider controller enrollment coupling and a CDC repository-pointer
comparison that falsely stopped the shared journal. Both runtime fixes pass
covering race/static checks; corrected APIs replay retained CDC into current
Ready Translation observations through both endpoints without reseeding.
Further covering race/static checks enforce retryable fail-closed ownership
lookups and certified, authorized missing-target observations without expanding
the positive thin-proof budget. Durable watch recovery resumes only from an
observed cursor within fixed bounds. Actual deferred arrival, both candidate
orderings and all 26 whole-receive rejections pass, including unchanged canonical
refs and absent rejected/sentinel resources through both APIs. The rejection
oracle follows the existing common `file: message` Git format; structured
reason/field parity remains covered by the standalone/preflight matrix.
Contemporaneous host/container clock diagnostics observed no suspension during
the passing run. Earlier intermittent signed-RPC transport failures remain
retained failures, not evidence of physical-fault recovery or sustained capacity.
API and controller
startup now support explicit matching capability pins for owned acceptance;
missing enabled-component pins and any configured mismatching pins fail closed.
Normal API/resolver/all-role constructors and configured production controller
registration are exercised without private activation overrides, with covering
race suites, vet and staticcheck passing. All production defaults stay disabled;
a matching local pin does not certify peer roles, target writers or deployment.
Two Smart HTTP endpoints do not by themselves prove callback distribution:
the singleton Git process uses its configured catalog URI. Connected topology,
capability and recovery evidence therefore remains required.

T067 now reuses the finite offered push scheduler and adds independent expected
source/body/patch joins, both-replica current-status/watch checks, target/baseline
invariance, full-window/rate/dataset gates and accurately classified
fault/restoration evidence. Missing, reused, partial, helper-only and health-only
proofs fail local contracts. Covering races and vet pass; broader integration
staticcheck still reports five unrelated existing diagnostics outside these files.
The receipt checks are not yet a connected collector or deployed certification.

T068 retains 500 files, all 50 Namespace manifests, two helper processes,
10 requests/s, 20 workers and the full 30-minute gates. All valid Translation
fixtures, locale/field negatives and exact 1-MiB sources now share that budget.
Standalone/preflight parity and actual signed RPCs through both helpers pass.
The helper is same-package to exercise dormant validation without exporting an
activation bypass; absent-target facts require an actual completed coverage
audit. Helper-only preflight receipts bind corpus/harness hashes, and the existing
dispatcher regression passes. Complete cataloggrpc races, vet/staticcheck pass.
RPC correctness and scheduling are tested separately so race instrumentation
does not masquerade as an unprofiled latency/throughput acceptance run.

T066 is complete: independent handler/replacement tests verify concurrent
duplicate creation/update, bounded CAS-loser retries, delayed signed callbacks,
stable UID/generation/source/body and unchanged targets. They pass with races,
and normal-constructor component tests reject disabled/staged replay consumers
and missing/mismatched activation pins while permitting capable replacement.
Full native ingestion also passes with the singleton Git process replaced
without overlap on retained storage and Catalog callbacks directed to the
second API (46.72 seconds). This exercises both Catalog owners sequentially,
not simultaneous callback distribution. Two independently issued enrolled-key
credentials now exercise both actual APIs: idempotent binding does not advance
versions/epochs, stale source/binding/epoch and retarget attempts fail, and
exactly one distinct current observation wins source-version status CAS.
The winning mutation retains guarded current Ready proof; source identity,
generation/revision/body/patches, sticky binding and target material remain
unchanged. The strengthened native race passes in 0.84 seconds.
The new native callback fixture creates and updates one independently named
source through both actual ingress URLs, then concurrently delivers signed old
and current callbacks to both live Catalog APIs. Both public reads retain the
exact latest source/status/UID/version/generation, full Product hashes stay
unchanged and canonical refs/reachable history are identical around replay.
The protected native receipt passes in a 9.377-second race package; the earlier
failed attempt exposed GraphQL-global versus storage Repository IDs, now
decoded and checked before signed RPCs. Accepted additions and failed logs
remain retained. All Translation Catalog/resolver/provider races pass
(50.816/3.899-second covering packages), as do vet and explicit rolling-reader
regressions. Normal constructed disabled/staged readers refuse read/list/Node/
watch access to an already accepted source instead of silently hiding it;
capable pinned replacement preserves the exact guarded response and stored
source. Component writer tests likewise refuse incapable replay after
acceptance. This completes the replica/capability test slice, not a forced
initially-unbound native race, physical-fault recovery or the full deployed
drain/activation/rollback rehearsal retained by T072/T078/T080.

T069 is complete. The bounded dataset builder has 1,009 independently named
documents in ten namespaces, including 1,000 same-Product dependents, all five
target kinds, two locales, all three channels and an exact 1-MiB source.
Independent source/body/patch fingerprints preserve authored body bytes.
An opt-in connected seeder now reuses bounded Git commits and durable watches
with per-document two-API read/current-status checks and target invariance.
Connected attempts reached hundreds of accepted current sources but exposed
fixture defects: serial replica readers, discarded historical delivery after
resume, unhandled graceful WebSocket closure and a shared one-MiB response cap
that truncates a valid one-MiB source's larger JSON envelope. Focused failing
regressions now enforce concurrent consumers, exact historical source/version
joins to independently guarded current Ready, bounded actual-cursor recovery
and an eight-MiB Translation-only response allowance. Shared Secret/RBAC
defaults remain unchanged; their covering races and vet pass.
The nonfatal bounded reader additionally joins public bytes/status to the
authorized current controller snapshot's source fingerprint. Bounded Relay
baseline collection hashes full Product identity/spec/body/resolved material,
rejects duplicate identities and invalid cursor progress, and does not assume
ascending name order. Independent Git reference hashing protects canonical
refs and reachable history. Local SHA-1/SHA-256 and generated-query checks pass.
The actual two-API replica probe verifies source fingerprints and unchanged
full hashes for four retained Products plus canonical Git refs before/after
status/binding races (15.717-second race package). This is not the required
production-scale baseline. Full connected dataset verification subsequently
passes as recorded below.
The next complete dataset attempt exposed a separate 128-KiB Git-output ceiling
in exact-blob verification before the near-limit batch was pushed. Local failing
SHA-1/SHA-256 regressions now enforce a bounded one-MiB-plus-one blob allowance
without changing ordinary Git command limits. All accepted data from prior
attempts is retained.

The existing continuously consumed two-API watcher and bounded asynchronous
oracle now support Translation source versions, namespaces, generations,
body/patch fingerprints and separate historical-delivery provenance. Initial
subscriptions use an actually observed global bookmark, not an unsupported
Translation sentinel or a fabricated initial event; recovery retains the
actual last cursor. Exact watch evidence joins independently guarded reads
without treating historical status as Ready authority.
The finite push schedule now reuses owned clones, actual commits and bounded
rebase/retry; full offered read accounting records errors, scheduler drops and
queue-inclusive latency without reducing the offered denominator. Public load
reads remain separate from private source-fingerprint oracle requests.
A native two-worker, three-push/two-ingress diagnostic joins both actual
watch streams to both APIs and preserves Product hashes. Its finite 20-reads/s
probe passes (23.540-second race package). Normal 60-second enrolled-controller
issuance and a refreshable credential source additionally pass local contracts.
The strengthened native probe naturally renews its short-lived credential
before authenticating another actual guarded source snapshot; three pushes,
20 offered reads and unchanged Product hashes pass in 50.924 seconds.
Broader Translation/RBAC/Secret and shared scheduler/watch races pass in
33.759 seconds, with integration vet passing. Push/read schedulers can share
one exact offered-window anchor rather than approximate caller timestamps.
The short connected driver now pre-verifies stable read sources and offers
reads concurrently with pushes using that shared anchor. Its explicit ten-name
selection routes clones and watches across all ten namespaces with two global
push workers, ten offered pushes and 200 offered reads over ten seconds.
Every target kind is independently captured before seeding and compared again
after load, alongside full Product material/aggregate hashes. A zero-result
canceled read window can no longer return success. All local capacity contract
races pass in 4.307 seconds, and integration vet passes. The strengthened native
ten-scope probe passes in a 103.693-second race package: ten pushes and 200
offered reads share a ten-second window, with both-replica source/watch joins,
natural short-lived credential renewal, all 50 target samples and unchanged
full Product hashes.
The bounded coordinator now routes explicitly selected namespaces to separate
clone pools and continuous watches under the same global concurrency bound.
An actual two-namespace/three-push diagnostic with normal credential renewal
and 20 offered reads passes in 49.782 seconds; it does not certify ten-namespace
sustained orchestration.

A subsequent full dataset attempt exposed a runtime defect, not merely a
fixture limit: an admitted one-MiB source exceeded the checkpoint's one-MiB
serialized projection allowance and blocked both Translation recovery loops.
The store now supports a bounded persisted projection allowance; Translation
opts into eight-MiB projections/32-MiB pages, while other kinds retain their
one-/four-MiB defaults. Maximum-body envelope/HTML-escaping and reopening tests,
all controller package races and vet pass. Sequential owned controller
replacement preserves independent checkpoints and resumes accepted sources.
A native read-only check independently joins the original near-limit UID,
generation/revision, complete authored bytes and private source fingerprint
on both APIs to retained Git history without resubmission (4.427 seconds).
This is not measured <=30-second sustained physical-fault recovery.
The next fresh full dataset attempt failed at a smaller batch. An ambiguous
Scylla reservation CAS timeout correctly retained a namespace lifecycle fence,
blocking accepted-source projection retries. Quiescing only owned writers and
using the existing confirmation-gated offline repair CLI cleared exactly one
verified fence with zero projection actions and clean post-repair audits.
Accepted delivery then resumed without source resubmission. Investigation also
found that the controller misclassified explicitly retryable
TRANSLATION_UNAVAILABLE responses, consuming its retry budget and quarantining
six sources. A focused regression reproduces that defect; the shared client
now defers that code only with retryable=true, without replaying an uncertain
mutation. GraphQL client, Translation and manager contract race suites and vet
pass. Sequential corrected-controller replacement retains both original
checkpoints; supported management requeue of the six owned A items restores
20/20 current Ready reads on both APIs with unchanged source UID, generation
and revision. B consumes the corresponding newer events and clears its old
quarantine normally. Both controllers are credential-ready with zero poison;
one older unrelated deferred obligation remains on each. This repair/recovery
diagnostic does not turn the failed full dataset attempt into a pass.

The subsequent fresh full dataset passes in 926.517 seconds: all 1,009 sources
across ten namespaces, including the 1,000-dependent Product and exact one-MiB
source, join actual accepted Git bytes to both APIs' durable watches and guarded
current Ready observations. Every namespace subtest passes without skips.
Together with the independent ten-scope simultaneous push/read probe, this
completes the bounded driver and oracle implementation. Earlier failed attempts
remain failures; their accepted sources, original history and private evidence
are preserved.

These probes are not full sustained windows. Protected complete receipts,
workload-wide baseline/target joins,
public wiring and the required full acceptance windows remain open.

T070 is in progress. The existing public `api/readiness` selector now accepts
`CAPACITY_TRANSLATION_READINESS=1` for diagnostic-only authenticated exact-source
reads, with ten distinct owned namespace cases, protected fixture ownership,
fresh run IDs and source/fixture-attributed non-certifying receipts. It rejects
identity-mode combinations and unrelated profiles instead of falling back to
health traffic. Actual public execution completes 200 reads (100 per API)
over ten seconds with no dropped/incorrect requests. It exposed k6's optional
end-boundary iteration and legacy rate-summary shape; finite planned domain
slots now exclude/account for the boundary without dropping offered work, and
both summary representations have passing regressions. Selector, protected
fixture, exact-body/current-status and malformed/incomplete receipt contracts
pass with the existing dispatcher suite. Automatic connected push orchestration
now uses `CAPACITY_TRANSLATION_PUSHES=1`, a bounded native companion and one
shared future timestamp for actual pushes and public reads. Its protected
receipt joins scheduled offers, commits, independently expected source/body/
patch fingerprints and both APIs' continuous watches/current status, with
unchanged full target/Product hashes and retained canonical history.
Actual public execution passes in 49.16 seconds: ten pushes and 200 reads
over one ten-second window, zero drops, 1,009 independently prepared retained
sources and unchanged ten-Product baseline. Initial setup failures exposed
an oversized child deadline, relative fixture working-directory assumptions
and unpaced oversized baseline pages; those failures remain retained, not
reclassified as passes. Small bounded baseline pages and paced control
sampling preserve the complete inventory without raising deployment limits.
Dispatcher regressions additionally reject push-mode misuse, missing source
fingerprints, material changes, drops and a mismatched load barrier. Covering
capacity races and integration vet pass; the final scoped race package is
6.417 seconds. Alpha/production still fail closed: complete configuration/live-
topology/resource attribution and acceptance verification remain unfinished.
Resource export now precedes the Translation domain verifier, scopes raw API/
controller histories to two explicit existing scrape targets per role, requires
process identity/CPU/RSS/goroutine samples and binds the captured queries to the
run/source/configuration/environment, explicit range and raw-series digests.
Diagnostic verification consumes those bindings and rejects changed raw series,
missing epochs and multiple endpoint aliases of one process without claiming
full-window resource coverage. Shared target validation and missing/
duplicate/unsafe-target, missing-process/resource and nonfinite-sample
regressions pass, preserving legacy profile exports. An owned, finite native
Prometheus capture independently observes six samples for each of the four
live API/controller process identities. It is idle diagnostic capture, not
live container/source attribution, full-window resource verification or
sustained acceptance. Connected diagnostics now cap the offered window at
90 minutes, reserving setup/recovery margin under the unchanged two-hour child
deadline; read-only diagnostics retain their two-hour maximum.
The retained-corpus fixture collector now independently joins all 1,009 original
sources to their last authored Git commits and exact blobs, both APIs' current
observations and normally renewed controller-source snapshots. Canonical refs
remain unchanged; the ten protected public read cases agree across replicas.
Actual retained capture passes in a 319.641-second race package without source
resubmission, followed by another successful public 200-read diagnostic.
SHA-1/SHA-256 near-limit history/body/scope regressions, all local capacity races
and integration vet pass (6.393-second race package). Temporary controller-key
issuance/removal is shared with the existing native probe rather than duplicated.

T070/T073 now also have locally verified immutable-image/PID/endpoint/ownership
topology wiring with configured CPU/memory ceilings, separate controller
checkpoint mounts, singleton Docker Git storage consumption and live Scylla
membership checks. The optional diagnostic manifest block remains fail-closed
for dirty verifier sources and mismatched images, hosts, namespaces, mounts or
endpoints. Raw resource export additionally captures independently hashed
original-scrape timestamps; an owned four-process idle capture verifies these
against five or six real samples per process. Both controllers and both APIs
have since been replaced sequentially with immutable Linux release artifacts,
with the unchanged peer retained at each step. The singleton Git process has
also been replaced without writer overlap on the retained storage.
Complete native captures verify
all 1,009 retained sources, refs/history, ten exhausted Product baseline scans
and 50 seeded targets unchanged through both APIs after each verified transition.
Actual controller and API namespace probes verify PID-1 executables, local/public
endpoint epochs and completed-GC signal presence. A real Linux ptrace-read
permission failure was reproduced and corrected: only executable inspection
receives `SYS_PTRACE`; HTTP probes remain capability-free. All three core-service
release images have clean-snapshot attribution. Three owned Scylla nodes now
agree on live membership; a complete post-bootstrap capture also preserves
the retained corpus, history, Product baselines and seeded targets.
The owned keyspace has now been backfilled to RF3 with APIs/controllers paused,
full sequential repairs on all three nodes and verified restoration. A complete
post-RF3 native capture preserves the same retained source/material evidence.
Both controllers now consume their original independent checkpoint sources at
the canonical destination, with complete retained captures after each change.
A newer clean-source-qualified rollout also passes full captures after both
controller and both API replacements. Its Go binaries are freshly compiled;
the Rust artifact is explicitly reused after exact compiler-input/content/mode
and executable-byte verification, not represented as a fresh compilation.
The final singleton retained capture times out at twelve minutes; the four
preceding RF3 captures take 656-700 seconds. A fresh fifteen-minute preparation
retry fails after its retained administrative token expires, including temporary
reader-key cleanup. The same existing issuer then renews that token without
changing identity, audience, authorization or lifetime; expired-token rejection
and authenticated both-API reads are independently verified. Exactly the two
failed readers' temporary keys are retired, preserving both permanent controller
keys and the account UID. Fresh retained verification then passes against all
six prior corpus fields, including 1,009 original sources and retained
history, baselines and targets. The public connected/resource run reaches
preflight but offers no load: the original Scylla node was created using a
mutable tag despite running the same immutable image as its two peers.
An owned image-reference correction first refuses Docker's unset-to-false OOM
setting normalization before starting the replacement; the original node and
both APIs/controllers are restored and independently regain readiness.
A fresh correction then passes with unchanged image bytes, storage, node
address/UUID and original cluster membership. It permits only that equivalent
OOM-setting representation, verifies membership before restarting applications,
and independently confirms both APIs and both controllers regain readiness.
Fresh post-maintenance verification then passes for all 1,009 original sources
and all six retained-corpus fields. The public diagnostic passes live-topology
preflight and all 199 executed exact-source reads, but fails the unchanged
no-drop gate after one of 200 offered reads is dropped. Its ten normal pushes
all complete, preserving target/baseline digests and original reachable history.
Separate execution of its existing resource checks also rejects one API's
137-to-155 goroutine variation; this is not represented as a growing leak or
full resource acceptance. A fresh capture independently joins the ten exact
acknowledged main-reference advances. The retry with 100 preallocated read
clients inside the existing 100-client maximum completes all 200 offered reads
and all ten pushes correctly, with zero drops or HTTP errors, but still fails
the unchanged goroutine-drift gate. Both diagnostic collectors are stopped.
The connected harness now implements separately accounted thirty-second read
preparation, twenty verified preparatory pushes and fifteen-second read cooldown,
without changing measured traffic or resource gates. Watch ownership is retained
through the final resource bracket. Focused race tests, dispatcher/verifier
regressions, vet and syntax checks pass. Clean-source qualification and all five
non-overlapping owned replacements then pass, followed by complete retained
verification against all six fields and the original 1,009 sources. The prepared
public run verifies all 200 measured and 900 preparatory reads with zero drops or
HTTP errors, but fails all twenty preparatory push/watch joins and emits no
complete connected receipt. Its collector is stopped. The shared watch was
still filtering only measured-run names with a measured-only cardinality bound.
A real WebSocket regression now verifies explicitly scoped preparation and
measured events on the same bounded watch, while retaining the default filter
and excluding unrelated runs. Race, dispatcher, vet and syntax checks pass.
The twenty acknowledged preparation advances must be independently joined
before a fresh retained capture. That audit then verifies exactly twenty
preparation-only additions through both APIs, preserving all original main
roots. Fresh clean-source qualification, five owned replacements and complete
retained verification pass. The next public run verifies all twenty preparatory
and ten measured push/source/watch joins plus 200 measured and 900 preparatory
reads, preserving baseline/target material and original history. It still fails:
whole-second metadata truncates the precise cooldown bracket by 227 milliseconds.
A read-only replay of the remaining original assertions passes without changing
that failed verdict; it is diagnostic analysis, not acceptance. Load timestamps
now preserve microseconds in Translation mode, with unchanged default-mode
formatting. Fractional-boundary and truncated-cooldown regressions, dispatcher
and syntax checks pass. Fresh clean-source qualification, five non-overlapping
owned replacements and full retained verification then pass. The new public
diagnostic passes every original source/read/watch/material/history/resource
assertion: 200 measured and 900 preparatory reads, twenty preparatory and ten
measured pushes, zero drops/errors and a complete precise cooldown bracket.
The bounded collector is stopped. T070 is complete; sustained/fault/production
acceptance remains open. One API nevertheless has an asymmetric high idle
baseline after this run: 7,827 goroutines and roughly 1.4 CPU cores, versus 85
goroutines on its peer. This is not diagnosed as a growing leak or hidden by a
restart; bounded reader/lease activity needs investigation before sustained
qualification.
The subsequent read-only investigation verifies 768 version-1 CDC streams and
768 vnode groups in the live generation. The existing reader starts one
long-lived query worker per group/table, consistent with the asymmetric stable
idle count; no stack profile or growing leak is claimed. Original source-qualified
diagnostic receipts also show measured visibility p95 about 10.08 seconds and
convergence p95 about 10.66 seconds, so they do not meet the sustained gates.
T073 now rejects aliased independent-source UIDs, receipt windows outside the
actual read command and unjoined fault claims. Its measured schedule includes
exact-one-MiB sources and separately reports normal/near-limit push latencies,
excluding preparation. Local red/green corpus, receipt, dispatcher, race and
vet checks pass. An opt-in bounded CDC batching patch reuses the existing
multi-partition query path with at most 32 streams per API batch, retaining
every consumer/progress frontier and unchanged confidence/query/failure rules.
Shared reader and API journal/catch-up/shutdown race regressions and vet pass.
No restart masks the original baseline; new runtime qualification, retained
source verification and deployed measurements remain necessary. T070 remains
complete; T073 remains open for complete negative-traffic and fault/restoration
joins, with all sustained/fault/rollout/production tasks still open.
The bounded reader build is now source-qualified, and all five exact owned
non-overlapping replacements and the complete original 1,009-source/six-field
retained capture pass. Active-capture samples show about 385-388 leading-API
goroutines; these are not idle or acceptance measurements. A first public attempt
stops before workload submission because private topology manifests were absent;
its failed evidence and stopped collector are retained. After supplying
byte-identical unchanged topology/environment declarations, the public run passes
all 200 measured/900 preparation reads and all twenty preparation/ten measured
source/watch/current-status joins, including the measured exact-one-MiB source.
Original targets/Product material and history remain unchanged. The public
verifier nevertheless fails the original retained-memory growth gate; no pass
is claimed. Separately retained diagnostic analysis reports nine normal pushes,
one near-limit push, normal visibility/convergence p95 about 7.47/7.79 seconds,
stable API goroutines and substantially lower CPU consumption. These remain
diagnostic measurements at the one-push/second offered rate, not the slower
alpha-rate latency result. Preparation previously omitted the newly measured
near-limit content class. It now reuses the same bounded corpus selector for
twenty preparatory pushes, preserving original source bytes and all unchanged
resource gates; focused race/vet checks pass. That preparation build is now
source-qualified, all five owned non-overlapping replacements pass, and fresh
original-corpus verification joins all preceding acknowledged reference advances.
Its public run again passes the native source/watch/current-status joins and all
reads, but fails the unchanged retained-memory growth gate. Representative
preparation alone is not a demonstrated resource fix. Original failed evidence
and its stopped collector are preserved; T073 and all sustained gates remain open.
T073's public verifier additionally has a red/green expected-rejection matrix
covering false acceptance, partial sentinel publication, changed canonical refs,
missing/aliased replicas, stale phase receipts, incomplete counts, missing source
fingerprints, reused accepted commits and unavailable outcomes mislabeled as
rejections. The existing real-Git negative runner now records candidate/parent
commits, source/sentinel fingerprints, unchanged reference digests and both-API
absence; connected preparation invokes the canonical invalid-locale/sentinel case.
Those preparatory rejection counts and response percentiles cannot dilute any
measured accepted-push distribution. Local race/vet/dispatcher/syntax checks
pass, including a real rejected local Git candidate and both-API absence queries.
The shared rejection helper also avoids empty-token over-redaction in that
unauthenticated fixture.
The rejection build is now source-qualified, all five owned replacements and
pre-run original-corpus/reference verification pass, and its actual native
companion verifies the independent invalid-locale/sentinel candidate and all
twenty preparation/ten measured accepted pushes. Both APIs show no invalid or
sentinel publication; canonical refs and target/baseline/history checks pass.
The original public verdict still fails retained-memory growth and is unchanged.
A separately labeled read-only replay of copied actual receipts passes the
domain/rejection verifier, reporting a rejection response of about 216 ms;
resource acceptance and new workload are explicitly excluded. The collector is
stopped. Full post-load verification passes for every original source against
the latest acknowledged references, preserving all six retained capture fields.
The existing callback/reply-loss fixture is being strengthened to require five
seconds of correctly scheduled both-API domain reads inside the original
30-second fault-recovery deadline, rather than one converged observation.
Local race/vet checks pass; fresh deployed fault qualification remains pending.
T073 remains open for complete
fault/restoration/epoch/gap joins; all sustained/rollout/production gates remain
open, and the checked total remains 74 of 83.

The stronger recovery fixture now has fresh deployed diagnostic evidence.
Clean source-v9 qualification, race/vet/dispatcher checks and all five owned
non-overlapping replacements pass, followed by complete verification of the
original 1,009 sources. An initial finite recovery run passed callback-loss
but exposed the fixture's incorrect assumption that successful upstream
admission guarantees immediate visibility on the other API. That failed run
and its two acknowledged commits are retained; direct singleton restoration,
proxy termination and a full original-source/reference verification pass.

The test-only source-v10 correction waits for exact authored replica visibility
inside the existing 30-second recovery deadline, reserving five seconds for
correct traffic. It also persists each successful fault separately so a later
failure cannot discard an earlier typed recovery receipt. All production
compiler inputs remain identical to the qualified source-v9 deployment.
Worktree and exact clean-source race/vet checks, dispatcher/syntax/whitespace
checks and the AST refresh pass. Actual callback-loss and reply-loss diagnostics
each complete five seconds of 100 correctly scheduled both-API reads, finishing
8.29635 and 6.972026 seconds after their respective observed faults. No
resubmission is needed; the direct singleton and peers are restored and the
proxy is stopped. Full post-run verification again preserves all 1,009 original
sources and material fields against the exact additional acknowledged commits.

A fresh canonical physical controller-restart diagnostic also passes, with a
distinct process epoch, unchanged surviving peer and independent retained
checkpoint mounts, accepted callback replay and five seconds of correct domain
traffic. Its callback-loss and reply-loss phases each complete 100 reads, and
the singleton is restored without overlap before the proxy stops. Full
post-controller original-source verification and the subsequent owned API
restart diagnostic are running sequentially. These finite diagnostics are not
sustained acceptance, do not establish full-window resource/fault-gap coverage,
and do not change the original public retained-memory failure. All nine open
tasks remain open.

Full post-controller retained verification passes for all original 1,009
sources. The initial owned API-restart diagnostic then fails its strict traffic
gate with 100 offered but only 99 completed reads; that original failure is
preserved, and singleton/peer restoration and proxy termination pass. Another
full retained verification joins its single acknowledged commit and preserves
every original source and material field.

The test-only source-v11 fixture prepares read workers before a future 500-ms
barrier, reserving both that lead and the complete five-second read window
inside the unchanged 30-second recovery budget. It persists actual traffic
counts/times before assertions, including failed windows. Worktree and clean
source race/vet checks, dispatcher/syntax checks and AST refresh pass; production
compiler inputs remain unchanged. Fresh physical API-restart diagnostics now
pass with a changed target process epoch and unchanged peer: callback-loss and
reply-loss each complete 100 reads over five seconds with zero drops/errors,
finishing 10.216856 and 8.627612 seconds after their observed faults. No
resubmission or overlapping singleton writer is required; restoration and
proxy termination pass. Full post-API retained verification is running before
the canonical pause of one owned Scylla journal source and its final retained
capture. These are finite diagnostics, not selective receiver interruption,
full-window fault/resource joins or sustained acceptance; nine tasks remain.

Full post-API retained verification passes for all original 1,009 sources.
The six-second owned journal-source pause is physically observed and restored,
with its process epoch unchanged. Its callback-loss phase completes five
seconds of 100 correct reads, but the subsequent reply-loss window fails with
97 completions and three dropped offers. Protected traffic receipts retain
that failure and its latency distribution (read p50 about 113 ms, p99 about
503 ms); the direct singleton is restored and the proxy stops.

The recovery fixture's read concurrency was tied to the two push workers,
although the contract limits push concurrency separately and the existing
reader permits up to 32 bounded read workers. Test-only source-v12 uses four
bounded read workers, retaining 20 reads/s, the complete five-second window,
the original 30-second deadline and zero-drop/error gates. Push concurrency
and service resource ceilings are unchanged. Worktree and clean-source
race/vet checks and AST refresh pass; production compiler inputs are unchanged.
Full original-source verification of the failed journal diagnostic is running
before the fresh source-v12 journal retry. The failed verdict is not overwritten,
and all nine task checkboxes remain open.

Full retained verification of the failed source-v11 journal diagnostic passes.
The source-v12 retry retains another strict failure: 100 offers, 99 completed
reads and one drop, with maximum scheduled-read latency about 1.087 seconds.
Its original receipt remains unchanged; singleton restoration and proxy
termination pass. No journal acceptance or identified root cause is claimed
from increasing read concurrency alone.

Source-v13 preserves per-offer completion identities and dispatch delays, plus
the exact namespace/name/commit/source hash in failure traffic receipts.
A focused overload regression verifies that every dropped offer remains
identifiable and dispatch delay is included in end-to-end latency. Worktree
and clean-source race/vet checks, dispatcher/syntax checks and AST refresh pass;
production inputs remain unchanged. Full post-source-v12 retained verification
also passes against its single additional acknowledged commit.
With the same four-worker/rate/deadline plan, the instrumented source-v13
journal diagnostic passes: callback-loss and reply-loss each complete five
seconds of 100 correct reads, finishing 16.219773 and 8.409593 seconds after
their observed faults. Maximum dispatch delay is about 2.2 and 431.4 ms,
respectively; all scheduled offer identities are present and drops/errors
are zero. The six-second physical source pause is observed/restored with no
epoch change, and the direct singleton is restored before the proxy stops.
Full final original-source/reference verification passes all 1,009 documents
and all six original capture fields against the two exact acknowledged
commits, with all ten original history roots retained. This finite
pass neither overwrites either failed run nor establishes sustained/resource
acceptance; all nine task checkboxes remain open.

Expected-rejection evidence now requires both APIs to independently confirm
that the invalid document and valid sentinel remain absent after the entire
offered window and cooldown, not only immediately after admission rejects
them. Final observations join the connected companion's actual completion;
the runner records that clock separately from the read command's completion.
Focused regressions reject delayed sentinel publication, missing/aliased
replicas, stale/future observations, incomplete cooldown and unobserved
companion completion. Worktree and qualified test-only source-v14 race/vet
and dispatcher checks pass. The actual owned finite public diagnostic passes
with 20 preparation pushes, ten measured pushes and one rejected candidate
whose invalid source and valid sentinel remain absent on both APIs after
cooldown. Before/after verification preserves all 1,009 original documents
and their six capture fields with the exact 30 acknowledged reference
advances; all owned process epochs stay unchanged. Its normal visibility/
convergence p99 is approximately 4.12/4.65 seconds, so this correctness
diagnostic does not satisfy the unchanged sustained latency gates.
This is boundary absence proof, not continuous absence
monitoring or sustained/resource/fault acceptance; no task checkbox closes.

The public verifier also rejects truthy string Ready values, numeric history
flags, boolean generations and numeric source identities instead of letting
malformed receipt types masquerade as current success. Focused dispatcher
regressions pass. Independent T076 preparation adds exact quickstart
Product/File extraction and an opt-in normal-Git ingestion test with
both-API source/current-status checks, exact accepted blob bytes and unchanged
target material. Only owned namespace/name substitution is allowed; no
retained target is changed. Qualified test-only source-v15 race/vet and
dispatcher checks pass, followed by actual normal-Git ingestion of both
published sources on the unchanged source-v9 services. Exact accepted bytes,
both APIs' current controller/source observations and unchanged target
snapshots pass. Full post-quickstart verification preserves all 1,009
original sources and six capture fields, joining its single acknowledged
commit; all owned process epochs stay unchanged. This verifies the two
authoring blocks, not the entire lifecycle/negative/documentation support
matrix, so T076 remains open.

Qualified test-only source-v16 binds ready/final connected receipts to the
explicit verifier Git revision as well as the source-state digest. Its native
companion passes the 20 preparation pushes, ten measured pushes and final
both-API rejection checks, but the public run fails two exact read checks
(one warmup, one measured). The failed verdict is preserved; post-run original
source verification is tracked separately and cannot turn that run into a pass.
Source/status response details were not recorded, so the original two failures
cannot be retroactively classified.

A deterministic profile regression reproduces a separate timing defect:
waiting until the barrier inside setup leaves executor initialization outside
the absolute schedule and can move final phase offers across their boundaries.
Setup now leaves bounded initialization time, requests wait for their absolute
offers, and teardown brackets the full cooldown. Lower/upper phase bounds and
all offered counts remain strict. Bounded content-free counters distinguish
scheduling failures from transport/response/source/status failures. Worktree
dispatcher checks and qualified test-only source-v17 race/vet/dispatcher checks
pass; no production compiler input changes and no acceptance gate changes.
The quickstart prerequisite/final target reads also refresh the existing
short-lived controller credential. No additional task checkbox closes.

The source-v16 post-failure retained verification passes independently:
all 1,009 original documents and six capture fields are preserved, with the
exact 30 acknowledged reference advances and unchanged owned process epochs.
Its public diagnostic remains failed. The canonical source-v17 finite
before/load/after scheduling diagnostic starts from that verified retained
state; no fault, collector or application replacement is involved.
Independent T076 review confirms #40's four-field status baseline and #549's
authored/GraphQL enum boundary requirements. The quickstart now distinguishes
the unreleased, activation-gated implementation from existing deployments,
and reserves optional destructive lifecycle checks for separately named
disposable targets, never the retained original targets. The two published
authoring blocks are unchanged; complete lifecycle/negative/support acceptance
remains open.

Source-v17's actual finite public diagnostic now passes every offered read:
200 measured and 900 preparation reads, with no classified failures. Its
20 preparation pushes, ten measured pushes, rejected-candidate final absence
checks, explicit verifier revision and full before/after 1,009-source/six-field
retention joins pass; all owned process epochs remain unchanged. Normal
combined visibility/convergence p99 is approximately 5.87/6.07 seconds
(near-limit 5.99/6.60 seconds), so sustained latency acceptance still fails.
The original source-v16 failure remains preserved and unclassified.

Read-only tracing confirms the existing watch join intentionally raises
combined visibility/convergence to include actual watch delivery. It cannot
establish whether the observed delay is source ingestion, guarded readiness
or watch delivery because the separate clocks were not retained. New
focused evidence checks retain the first source-read clock, first successful
guarded-ready read for the observed UID/version, and actual watch-arrival clock
without changing the existing combined boundaries or any acceptance gate.
Public checks reject incomplete, early, late or hidden component joins;
preparation and measured cases both require complete clocks when supplied.
Focused race/vet and dispatcher checks pass. No production code is changed
and no root cause or performance improvement is claimed from these local
checks; T073 and all nine remaining tasks stay open.

Qualified test-only source-v18 passes clean-source race/vet/dispatcher checks
with unchanged source-v9 production compiler inputs. Its actual owned
before/load/after diagnostic passes every exact read and all 60 independent
component-clock joins across preparation and measured observations. Full
retained verification preserves all 1,009 original sources and six capture
fields, joining exactly 30 acknowledged commits; all eight owned process
epochs remain unchanged. Rejected source/sentinel absence is confirmed on
both APIs after cooldown. Normal combined visibility/convergence p99 is
approximately 8.23/8.84 seconds; near-limit is 7.96/8.49 seconds. The
unchanged sustained latency gates remain unsatisfied. Paired observations
also show delays before guarded readiness and after it before watch arrival;
these client-observed clocks do not isolate server-stage latency or prove
that watch delivery is the sole cause. No acceptance task checkbox closes.

Test-first exporter/dispatcher regressions reproduce acceptance of missing
native resource epochs. Canonical API/controller queries now include
`process_start_time_seconds` with original-scrape provenance. Fault-free
full-window checks require every native epoch to match live preflight and
reject missing, changed or mismatched starts despite continuous counters.
Both covering shell suites, syntax checks and whitespace checks pass.
This strengthens fault-free evidence, not legitimate physical-fault/restoration
joins or sustained acceptance; all nine task checkboxes remain open.

Qualified test-only source-v19 passes race/vet, both covering shell suites and
fresh VCS-attributed driver qualification with all 1,976 inventoried files and
unchanged source-v9 production compiler inputs. No new application image or
process replacement is involved. Additional test-first local receipt checks
reproduce acceptance of missing, mixed, early, late and rewritten independent
clocks; the local oracle now enforces the same complete component-clock joins
as the public verifier while retaining legacy clock-free evidence. Covering
race/vet and public shell checks pass without changing combined latency gates.
These are local correctness checks, not an additional deployed acceptance pass.

T073 now admits independently joined simulated callback/reply recovery rather
than rejecting every fault claim. Test-first dispatcher evidence reproduces
rejection of a complete valid recovery record; covering checks now admit both
simulated boundaries and reject missing declarations/windows, interception,
successful replay, source identity, complete native request sequences/timings
and five-second traffic, along with stale/late/physical claims. The connected
companion can declare and observe one boundary during its actual measured
window without changing ordinary scheduling or the two push-worker limit.
Native recovery evidence includes source UID/version and the full existing
traffic proof. Cancellation clears the owned selector; another proxy/run
cannot be adopted. Worktree covering race/vet and shell checks pass. Actual
connected fault execution and the remaining physical resource/restoration/gap
joins are still required; no task checkbox closes from these local checks.

The cancellation test initially rejected its test-owned directory because
`t.TempDir()` did not satisfy the protected-root mode. Both observer tests
now explicitly protect their directories; the foreign-proxy case also verifies
the intended run-mismatch error, rather than passing through the earlier
directory guard. Covering race/vet and public shell checks pass. Test-only
source-v20 qualifies all 1,976 inventoried files against unchanged source-v9
production compiler inputs, with fresh integration and proxy binaries.
Its owned connected callback-loss diagnostic fails before the load barrier:
the proxy's internal five-minute lifetime expires during original-source
preparation despite a longer outer test deadline. The genuine failure and
unavailability rejection remain preserved, not classified as an admission
rejection or callback recovery. Direct singleton restoration, stopped
temporary routing/proxy and all eight healthy owned processes are independently
verified. Independent original-source/reference re-verification then passes
all 1,009 documents and six capture fields with zero canonical reference
advances and stable restored-runtime epochs. The original diagnostic remains
failed; the separate retained proof does not promote it to recovery or
acceptance.

Further test-first recovery-read checks reproduce acceptance of a recreated
UID, advanced source version and incomplete identity/version pair despite
matching authored bytes. The existing bounded reader now verifies the pinned
UID/version on every recovery request; both connected and standalone recovery
producers supply the complete pair. Covering race/vet checks pass without
changing ordinary unpinned reads, scheduling or gates. These changes postdate
source-v20 and cannot be attributed to its earlier binary.

Test-only source-v21 qualifies those per-request identity/version checks and
private failed-traffic preservation against unchanged production compiler
inputs. The proxy lifetime defect is reproduced by focused tests and now has
an explicit positive duration bounded at seventy minutes, retaining the
five-minute default and unchanged fault/recovery gates. The complete Catalog
Translation race suite and vet pass. Private coordination leaves time for
before/load/after verification and preserves failed proxy exits separately
from a successful coordinated stop receipt; the corrected runner has not
yet produced a successful connected fault result.

Corrected test-only source-v22 passes clean integration and Catalog Translation
race suites, vet, both covering shell suites and fresh driver qualification.
All 1,976 inventoried files retain unchanged source-v9 production compiler
inputs. The new actual connected callback-loss before/load/after diagnostic
is running against the independently verified post-failure retained authority.
Its proxy and routed singleton are independently responsive; the original
writer is stopped without overlap. Terminal domain, original-source/reference
and direct-route restoration proofs are still required before any result
claim or another owned workload.

That source-v22 connected callback-loss diagnostic subsequently passes:
20 preparation and 30 measured acknowledged pushes, 600 measured and 900
preparation exact reads, and 100 pinned recovery reads over five seconds within
12.43 seconds of interception. The rejected candidate and valid sentinel stay
absent on both APIs after cooldown. Independent retained verification preserves
all 1,009 original documents and six capture fields against exactly 50
acknowledged reference advances; all eight running epochs remain unchanged
during the workload. Direct singleton routing is restored without overlap and
the proxy exits successfully with its coordinated stop receipt.
The public verdict remains diagnostic, with `passed:false` and
`domainChecksPassed:true`; normal visibility/convergence p99 is 6.80/7.15
seconds and near-limit is 6.65/6.94 seconds, so sustained latency gates still
fail. The distinct fresh connected reply-loss run subsequently passes with
the same 20 preparation/30 measured pushes, 600 measured/900 preparation reads
and 100 pinned recovery reads over five seconds, recovering in 14.05 seconds.
All 1,009 original documents and six capture fields remain joined to exactly
50 acknowledged reference advances; rejection absence and unchanged workload
epochs pass, and direct singleton restoration and coordinated proxy shutdown
are verified independently. Its public verdict remains diagnostic, with
`passed:false` and `domainChecksPassed:true`; normal visibility/convergence p99
is 14.12/14.83 seconds and near-limit is 11.12/11.56 seconds.
Neither finite run certifies physical-fault or sustained acceptance.

Additional test-first public evidence reproduces acceptance of matching opaque
reference fingerprints without the actual acknowledged head. Recovery now
includes complete bounded canonical before/after reference snapshots. Public
checks join all sorted refs and their fingerprints to the acknowledged main
commit, rejecting missing, reordered, duplicate, wrong-head and mismatched-digest
claims while allowing retained non-main refs. The existing native reference
reader and both recovery producers reuse the same canonicalization; covering
race/vet and shell regressions pass. These checks postdate source-v22 and
cannot be attributed to either of its immutable run binaries.

Public topology regression tests also reproduce acceptance of aliased Scylla
container IDs. Live preflight now retains journal-source names and startup
epochs; the verifier rejects duplicate core/journal identities, absent or
post-load journal epochs, disagreeing membership and mismatched ceilings.
The covering dispatcher, syntax and whitespace checks pass. These attribution
checks do not establish physical interruption/restoration or close T073.

Owned immutable snapshot deduplication preserves every inventoried byte, path
and Git history, leaving all 22 snapshots clean and reclaiming approximately
610 MiB of duplicate files. Qualified source-v23 runtime images then build
offline with fresh, clean-source Go binaries and explicitly verified
identical-input Rust artifact reuse; no fresh Rust compilation or deployment
is claimed. Independent pre-replacement verification subsequently preserves
all 1,009 originals and six capture fields with zero reference advances and
unchanged eight-process epochs against the successful reply-loss authority.
Strict tested-source/image attribution remains enabled.

Idle observations also show stable CDC-worker fan-out and intermittent readiness
failure on one API; neither a growing leak nor resource acceptance is claimed.
The eight-container/RF3 topology is independently verified, while
completed-GC/full-window native resource acceptance remains unverified.
The shared SDK now exposes last-completed-GC live heap and cumulative GC cycles
without forcing GC during collection. Test-first regressions verify genuine
allocation/release and exclude allocations made after GC. The local Go receipt
model checks full-window resources and declared physical epoch/gap joins; the
Python verifier independently derives fault-free CPU/RSS/retained-heap/goroutine
checks from original scrapes and rejects lookback-filled gaps, counter resets,
missing GC provenance and configured-limit violations. Canonical raw and
metric-preserving timestamp query selectors are independently bound to the
declared roles and targets; test-first tampering regressions pass. Native companion
scheduling now has local diagnostic/alpha contracts with exact offered counts
and a six-second alpha interval. Near-limit push rendering now preserves the
exact document budget for short and maximum-length run IDs by resizing only
explicit synthetic filler, never retained or unmarked authored body bytes.
Focused corpus/race checks and integration vet pass; this does not yet certify
near-limit traffic during a sustained window. Public acceptance remains disabled pending
complete traffic, fault/restoration and deployment joins. Covering dispatcher,
exporter, secret fail-closed and scoped Go race/vet tests pass. Neither T070 nor
T073 is complete.

T072 is complete as fault-coordination implementation, not sustained acceptance.
A bounded loopback-only Catalog transport proxy preserves
signed requests, deadlines and all pre-receive validation methods while
independently dropping admission callbacks or successful upstream replies.
The test-first regression initially fails because the forwarding stub does not
drop callbacks; it now verifies both boundaries, ordinary retry, invalid control,
receipt failure and preservation of upstream rejection. Native execution then
passes both loss cases with actual ingress pushes and unchanged full Product
hashes/history. A strengthened native run passes in 20.86 seconds: after an
acknowledged push whose callback has not projected its source, an abrupt kill
and verified non-overlapping replacement of the singleton on unchanged storage
replays the accepted commit without author resubmission. Both APIs converge to
the exact current source/status, and successful post-restoration transport
receipts independently join each accepted commit. Simulated transport and the
physical Git interruption are labeled separately. Direct Catalog routing is
restored and the temporary injector stops; accepted sources and every failed/
successful receipt are retained. The complete Catalog Translation races pass
in 20.218 seconds with vet. The existing owned chaos runner now accepts the
exclusive Translation selector for API/controller restart and journal-source
pause, reusing its bounded windows, ownership, immutable-target restoration
and independent-checkpoint assertions. Translation API restart additionally
requires an unchanged owned API peer. The native fixture invokes those same
public selectors while an acknowledged source awaits callback delivery, joins
same-run physical restoration, and independently verifies ordinary retry and
exact both-API convergence. Actual journal pause/recovery passes in 16.38
seconds; actual API restart/recovery passes in 11.82 seconds with the other API
unchanged. Controller restart/checkpoint and peer-preservation wiring passes
the shared dispatcher regressions; this segment does not claim an actual
container-controller fault. The dispatcher additionally rejects mixed/invalid
selectors, unowned/missing peers, shared checkpoints, Git targets and a changed
surviving API epoch. Full dispatcher/secret-capacity regressions, local
Translation integration races (10.721 seconds), vet and whitespace checks pass.
Full sustained faults, topology/resource receipts and rollout rehearsal remain
separate open gates.

T074 is complete. The delivered authoring guide and canonical Product/File
examples now document the exact selected envelope, both content channels,
all five target kinds, eligible fields, locales, sticky reference behavior,
limits and common status semantics. Push validation, API reference, storage
overview and documentation navigation agree and explicitly retain disabled
activation, pending acceptance and no localized application. Admin documentation
preserves input/payload writes, resource Relay connections and the separate
non-Relay dependent scan. A focused regression checks every Translation example
in both authoring/storage guides byte-for-byte against the corpus already
exercised by actual ingestion; it first caught an extra blank body byte in the
new field-only example. Full local Translation fixture/source/capacity races
pass in 10.815 seconds with vet, whitespace and internal-reference checks.
This publication does not certify sustained acceptance or future extensions.

T071 is complete:
SDK-backed source/target validation, admission, single/page guarded reads,
live/historical watch conversion, controller attempts/lookups/status CAS and
durable target handoff metrics now exercise actual paths with bounded labels.
Complete API catalog/resolver and controller Translation/listwatch/status race
suites plus static checks pass. Durable queue age, reconciliation completion
lag and readiness gates are covered. These metrics are not deployed acceptance
or certification.

The oldest-pending slice now persists original queue timestamps through retry,
quarantine, source coalescing, acknowledgement and generation replacement.
Translation explicitly opts into age-index-capable checkpoint writing, without
changing the contract of existing kinds. Bounded ordered-index statistics and
SDK-derived health metrics distinguish known origins from legacy unknown ages;
per-kind readiness reflects the existing sync/recovery/stall/poison gates, not
activation or target readiness. Focused persistence, HTTP/SDK and independent
runtime-replica tests pass. The complete standalone controller race suite,
covering vet/staticcheck and complete API datastore/resolver/cataloggrpc races
pass. Completion lag counts only successful current-lease durable
acknowledgements; superseded work and unknown ages cannot fabricate samples.
Actual source-audit/target-repair SDK paths and cancellation pass with bounded
labels. An active-recovery regression verifies the operational readiness gate.

The binding mutation now follows the common input/payload convention:
`BindTranslationTargetInput!` returns `BindTranslationTargetPayload!` with
`binding: TranslationBinding!`. Generated schema/models, real resolver,
controller transport and replica/recovery fixtures agree; malformed or old
unwrapped acknowledgements fail closed.

T075 is complete:
the contributor guide documents descriptors, binding, authorized snapshots,
bounded guard/repair and additional-kind handoffs. The operator runbook covers
disabled activation, diagnosis, bounded maintenance and data-preserving
rollback without an unsupported enablement recipe. Both are linked from the
documentation indexes. Documented audit flags, whitespace and internal-reference
checks pass. Production activation and connected acceptance remain incomplete.

T062/T063 are complete:
all 50 negative corpus rows now run through standalone validation, full shared
preflight and single-manifest validation with identical bounded diagnostics.
Signed pre-receive tests additionally cover structural, target-field and
invalid-UTF-8 rejection without changing the previously accepted source,
generation, revision, controller observation or target authored/resolved image.
Source-authorized target denial is identical for present and absent targets.
All five non-Translation fixtures retain their enum/source/body behavior with
the feature disabled and privately enabled in tests. API generation, complete
cataloggrpc/parser/admission/report race suites and static checks pass.
These are dormant handler/component checks; connected pushes and deployed
acceptance remain T064 onward, and production activation remains disabled.

T059/T061 are complete:
exact YAML node/depth ceilings, source/path/patch/target/diagnostic budgets,
cancellation and expired deadlines pass. Saturation is exercised beside the
existing private Catalog interceptor in `internal/app/role_runtime_test.go`,
not through a second Translation queue or an activation bypass. It rejects
overload without entering validation, keeps health and Smart HTTP capacity
independent and releases its finite slot after completion.
An actual full preflight regression exposed continued parsing and accepted
responses after cancellation in later shared phases. Candidate, immutable,
deletion and namespace-policy parsing now stop at every parse boundary and
return no partial result. Full cataloggrpc/parser/app/admission/report races,
vet and staticcheck pass. Activation stays disabled.

T057 is complete:
all raw/decorated backend-neutral and isolated native Translation subcontracts
pass with races, including binding budgets, index/source audit recovery,
retirement, serialized fences and the evidence protocol. Native Translation/
retirement recovery tests, native production staticcheck and contract vet pass.
Full candidate/parser/guard/memdb/journal, standalone shared semantics and
controller/listwatch/status/checkpoint/contract races pass, with a standalone
controller build. This is component/backend acceptance, not connected pushes,
physical faults, mixed-deployment certification or sustained capacity.
Connected acceptance remains separate; activation stays disabled.

T056 is complete: real
configured RBAC-local and published-tier OPA providers exercise named/Node/list
and live-watch reads, denied human/service actors and typed/generic mutations.
The tests reproduced humans with an explicit status grant bypassing controller
enrollment; both write paths now reuse the existing live controller-identity
boundary. Stale controller UIDs are rejected without source changes, and
unavailable namespace authority fails closed. Shipped local policies and
release built-ins now grant Translation author/viewer source actions and
controller read/list/watch/status, including read/list/watch for all five target
kinds without adding target material writes. Full resolver/security/provider/
RBAC races and scoped static checks pass. Later connected acceptance remains;
activation remains disabled.

T040/T054/T055 are complete:
named/Node/list and typed/generic status-mutation responses use the shared
guard. Actual handler tests cover current, stale, denied and unavailable proof,
post-CAS target mutation, enum boundaries, private-field redaction and unchanged
stored observations. Live watches guard only a matching source event; a source
retry cannot substitute a newer body/version under that event. Initial replay
uses the subscriber's exact registration boundary and remains historical with
no status. Independent resolver replay and the controller WebSocket client
cover uppercase GraphQL enums, absent historical status and separate source/
journal versions; YAML input aliases are rejected. Complete API resolver/
security/journal races, generation, vet/staticcheck and controller transport/
Translation/status races/static checks pass. Subsequent backend and deployed
acceptance remain open; activation remains disabled.

T053's bounded guard core now
has eight-worker page deduplication after initial source reads, independent
source reauthorization, fresh proof on source-version retries, denial/storage
isolation and cancellation. Stale/forbidden/unavailable masks request durable,
lifetime/version-fenced work without changing public metadata, authored bytes,
stored status or binding history. Coalescing survives concurrent and replacement
APIs; native CDC delivers unchanged-source hints through the existing journal.
The Translation runner atomically requeues same-version hints with their cursor.
Complete API resolver/security/backend/journal and controller recovery race
suites, static checks and standalone controller build pass; raw/decorated
memdb/native source CAS and native journal/replacement regressions pass.
Public route integration is now complete in the dormant runtime.

T051's durable all-five-kind
target handoff is complete. Coalesced, round-robin 256-item sweeps retain
progress and repeat dirty prefixes; lock-free cold-relist handoffs complete
before snapshot/cursor publication. Source indexes use matching controller-only
Namespace/binding identity, with conservative namespace/name aliases when
proof is unavailable. Raw sticky UIDs survive certified target deletion and
never come from masked public status or Relay target IDs. An enrolled,
target-authorized thin endpoint reads no target content; fresh material proof
classifies version-only events, while missing/denied proof, termination,
replacement and Namespace changes invalidate conservatively. Bounded durable
hint records are invalidation state, never current-Ready authority.
Product/CategoryTaxonomy runners are reused; the other three use metadata-only
observer runners without duplicate reconciliation work. Source/target runners
are wired in a gated runtime factory; default production startup remains
disabled. Full API resolver/security and standalone controller/manager/
checkpoint/contract races, builds and static checks pass. Both new native CDC
sources and the consolidated migration passed isolated native tests; canonical
datastore checks pass after updating their authoritative-source assertion.
T052's dormant runtime wiring and component recovery acceptance are complete:
independent replica directories reject overlapping writers, retry deadlines
and fan-out survive replacement, and an actual denied API response quarantines
work. Replacement and an unchanged cold relist retain that obligation; a
changed source clears the old poison and converges through the real HTTP
client/manager. These are local component tests, not deployed provider or
physical-fault acceptance. Public guard integration and later acceptance remain open.

T050's validation-only
reconciler component is complete. Controller snapshots now distinguish
certified, authorized absence from missing/unavailable proof for all five
kinds. Serialized Namespace metadata and final source/binding/lifetime fences
apply to positive and negative snapshots; no target evidence/content escapes
denied reads. Actual GraphQL handler and client tests preserve nullable targets.
The first observed eligible UID is durably bound even for invalid fields, so a
same-name replacement cannot silently cure the source. Budget/transport errors
publish no partial status; generation/revision writes retain source UID/version
CAS. Full resolver/security races/static checks and standalone controller
translation races/static checks/build pass, including the existing independent
disk-v2 replica/conflict/restart contracts. Module and container dependencies
were already wired and are verified with GOWORK=off. T053 public guard
integration remains open;
activation stays forbidden.

T048 now connects authorized
effective-candidate lookup to dormant shared push/manifest preflight. All source
operations are authorized before target semantics. Serialized Namespace/target
facts precede target-read authorization and content; proved UID reads check exact
material and recheck ownership/lifetimes. Namespace-bound coverage plus unchanged
missing/vacant heads proves absence. Pending/unavailable proof is not absence.
Complete proposed trees, including unchanged targets, override stale stored
fields independently of order; unchanged Translations are not retroactively
rejected on target edits. Creates never borrow an old UID, updates never invent
a future version, and unchanged candidates use current versions only for exact
proved material. Status-only churn is harmless unless explicitly version-pinned.
Connected preflight tests reproduced and fixed invalid target fields being
accepted by batch and single-manifest validation. Full cataloggrpc/parser/
admission/report races and static checks pass; raw/decorated memdb and native
absence/Namespace contracts pass, as do both backend static variants. These
are component/dormant-handler tests, not deployed ingestion or post-commit
report acceptance. Public activation remains forbidden.

T047's component
implementation is complete: strict serialized positive target evidence,
known-birth backfill, durable all-five-layout coverage traversal and operator
integration work on raw/decorated memdb and native stores. Independent coverage
epochs preserve verification during proved writes while unproved writes and
backfill cancellation invalidate it. Deterministic all-five-kind tests mutate
material inside every discovery page and complete native verification during a
held proved source grant, without canceling that writer or exposing pending
material as positive evidence. Confirmed native repair fences blocking unproved
grants, returns Conflict without progress, and resumes on retry. Original birth
authority and permanent UID retirement remain mandatory.
Full scoped memdb Translation races pass4.329s, native writer/retirement/audit
races139.212s, native shared audit/backfill23.376s, gitctl races3.634s, both
backend static variants and canonical datastore tests pass. These are component
and simulated-interruption checks, not deployed sustained-load or physical-fault
acceptance. T051 and later integration remain open; T077-T080 retain deployment,
role/drain and activation gates. Admission remains disabled.

Additional native creation regressions first reproduced all five kinds
borrowing a Namespace born during a paused legacy create, and unproved
rollback deleting source without permanently retiring its UID. Creates now
freeze birth authority before projection writes and reject a changed
incarnation before source publication. All rollbacks reuse conditional
retirement, including unproved envelopes; replacement-store replay cannot
resurrect their UIDs, while fresh UIDs can reuse names. The complete native
Translation/retirement race selector and tagged vet/staticcheck pass.
These fixes do not supply recoverable namespace callback ownership or complete
T047's all-writer mutation-watermarked backfill.
An independent-store legacy File regression also reproduced an equal-version
material ABA restoring a paused writer's private CAS authority. Unproved source
writes now carry a fresh private mutation ID, without adding public metadata or
changing legacy version rules. The paused write conflicts and preserves the
intervening material. Native Translation/retirement race tests and tagged
vet/staticcheck pass after this additional fix.

Native source writes now have a separate durable single-source-CAS
epoch, including repository-less and unowned CRUD. Arbitration follows the
namespace name rather than UID so a newly born/recreated Namespace observes
older source grants; epoch observations bind that clock to its live UID without
adopting the source's missing or original birth authority. All five late
unowned source/Namespace-birth regressions first failed, then passed with the
name-scoped clock. A bounded frozen owner is revoked and
its exact source operation canceled or recognized as committed before release;
elapsed time alone never clears authority. Later compensation acquires a fresh
epoch. Recovery leaves the separate lifecycle pending reservation untouched.
All-five-kind native ownership/cancellation/committed interruption, legacy ABA,
compensation, malformed-owner and concurrent unproved-birth regressions pass.
The concurrency case isolates source grants from the separate target-discovery
allocator's retryable conflicts. Fixture-only DDL setup/cleanup queries now
have bounded 30-second deadlines; runtime query limits are unchanged.

Raw/decorated memdb and native backends now share idle mutation-epoch
observations. Memdb publishes its epoch in the same source-write/retirement
transaction and captures incomplete new envelopes' namespace birth before
eligibility checks. New regressions first failed for absent epoch support,
untracked incomplete births and accepting a page after namespace mutation.
Bounded source-page recovery now compares epochs around discovery/rechecks and
returns no continuation for changed, pending, malformed or unavailable proof,
including an actual new source outside the observed page. These are per-turn
watermarks, not durable all-layout completeness.
T047 remains unchecked and activation remains disabled.

Known-birth target backfill now works for all five kinds on raw/decorated
memdb and native backends, preserving the complete public envelope and repeated
recovery's epoch. It also restores proof after unsupported transitions return
to canonical metadata. Missing birth authority remains repair-required.
Native durable backfill intents distinguish existing-source proof construction
from provisional creation, enroll exact-key retained UID authority and update
private identity under the source clock. All-five-kind independent-store audit
recovery tests cover paused and committed writes, intervening material changes
and delete/recreate under a fresh UID. Cancellation cannot retire the live source
or invent previous evidence. Full scoped memdb Translation race tests, native
Translation/retirement race tests, shared native source audit/backfill contracts
and both tag variants' production staticcheck/contract vet pass. These are
component and simulated-interruption checks, not deployed fault acceptance.
Durable all-five-layout target traversal now has independent Repairing,
Verifying and Verified progress on raw/decorated memdb and native backends.
Verification binds its complete pass to an idle namespace mutation epoch and
descriptor digest; changed epochs restart it and coverage readers recheck that
epoch. Native full-preimage CAS, replacement-store reload after uncertain
responses, late-mutation rejection and permanently retired source exclusions
pass. The existing projection commands expose `--audit-scope targets`, retaining
`indexes` as their default. Preview remains read-only, including an absent clock;
a regression first reproduced lazy clock initialization by preview before the
epoch reader was made non-mutating. Complete native writer/retirement/audit
race tests pass229.636s, all gitctl race tests4.242s, native shared source
audit/backfill21.939s and both tags' vet/production staticcheck pass.
These earlier strict-epoch observations are superseded by the coverage-epoch
implementation summarized above; neither establishes old-replica/deployment
certification.

T058 is complete: one shared test runner exercises 50 negative fixtures through
standalone validation and the internal manifest-snapshot stage. It pins stable
reason/location and common diagnostic mapping, checks exact source/target
preservation, and covers structural/null/unknown/duplicate members, aliases/
merges, system fields, mixed patches, target selectors, pins and all delivered
kinds. The matrix exposed ancestor-overlap classification behind descriptor
rejection; shared validation preflights path grammar/overlap and applies those
findings in authored patch order before that patch's field eligibility.
Earlier text errors are not displaced by later path errors. Source and target
adapters use the same ordering. Complete API parser,
cataloggrpc, admission/report, resolver/security race suites and static checks
pass, as do the shared standalone race suite and controller race/standalone/
static checks. Public ingestion, overload and full limits remain T059-T063;
this matrix does not activate the dormant runtime.

T060 is complete: typed safe diagnostic conversion and common admission-reason
mapping retain document/patch ordering, 32 findings, 256-byte UTF-8 messages and
explicit truncation. Structural server-path tests verify the same bounds and
ordering, and the 50-case shared matrix pins all delivered domain reasons and
locations. The existing common admission/report envelope is retained.
T059/T061 have additional verified components but remain unchecked. Source
tests cover exact/over 1-MiB and 256-patch limits, YAML nodes/depth and path byte/
segment/selector boundaries; supplied targets exercise node/depth/logical-byte
exhaustion without partial success. Cancellation regressions first returned
ordinary validation errors or inactive-feature errors; validation now stops
between blobs and preserves canceled/deadline gRPC results before source work.
Raw target preflight counts exact value nodes/depth before full decoding and
rejects duplicate JSON members, including opaque nested maps. Regressions prove
the exact node ceiling remains accepted and over-budget malformed tails are
never decoded. Complete parser/cataloggrpc/resolver race suites and static
checks pass. Actual queue saturation and complete candidate/report integration
remain unfinished; no runtime activation or deployed acceptance is claimed.

T065 is complete: the actual native validation handler forwards all seven
valid documents, all 50 negative fixtures, three deferred fixtures and invalid
UTF-8 over its existing gRPC client byte-for-byte, including CRLF/Unicode bodies,
paths and blob IDs. A mock acceptance intentionally proves the native hook
does not add semantic parsing. An extended shared rejection golden fixture
passes both Go and Rust; actual native handler rejection uses exactly that text.
All 21 validation-handler tests, strict all-target Clippy and full formatting
checks pass. No Rust production logic, dependency, protobuf or Git subprocess
was added. This is native transport/parity evidence, not connected Go validation
or deployed ingestion acceptance.

T038's deterministic freshness tests and the private service core now reuse the same authorized
thin target observation as controller binding, with at most two attempts and
ten metadata reads including the initial source read. Deterministic tests cover
target mutations before/after status CAS, delete/recreate with stale lookup
projections, full-version pins versus status-only updates, source/name and
namespace-incarnation ABA, source retries/exhaustion, unavailable proof/I/O,
target denial including stored negative status, cancellation and ownership.
It returns status and source from the same retry, masks without modifying
stored observations, and never fetches target content. The complete resolver/
security race suites and static checks pass. T038 is complete; T053 remains
unchecked: complete controller convergence, page-level deduplication/concurrency, durable
revalidation scheduling and all public routes remain unfinished. Public
observations remain hidden and activation stays disabled.

Status writes now also require the source lifetime UID: the typed input makes
it mandatory, the common input requires it for Translation, and the controller
transmits the existing `StatusPatch.UID`. A missing-UID regression failed before
the fix. Actual typed/generic GraphQL tests delete/recreate the source, reproduce
identical versions/generation/revision and verify that the old UID conflicts
without changing the replacement, while its own UID succeeds. The complete API
resolver/security and controller Translation/listwatch/status race suites,
generation, vet and staticcheck pass. Public status shape and enum boundaries
are unchanged.

Latest broader native verification is not a pass: full canonical reruns hit
intermittent schema-DDL timeouts and existing ServiceAccount identity/category
membership assertions. The owned fixture was restored without removing its
volume; focused raw/decorated target evidence/audit contracts pass28.137s.
These failures do not justify enabling admission or marking deployed acceptance
complete.

The unregistered T039/T050 reconciler core now imports shared validation through
the controller's standalone module/container dependency path. Its authorized
snapshot interface requires matching source UID/version, source digest,
namespace incarnation and binding epoch. It validates all five descriptors,
preserves authored bytes/casing, binds only before valid success and writes
UID/version/generation/revision-fenced observations without target mutation.
Negative outcomes use bounded fallback retries; I/O, missing proof and target
work-budget exhaustion never become invalid-domain observations. Concurrent
workers respect status CAS. Source-version-fenced private observations, rather
than absent/masked public status, supply complete fingerprint no-op comparisons
and transition times; missing private evidence is repaired. Scoped race/static checks and the
standalone module pass. The positive-target API snapshot adapter and actual
controller client now cover all five kinds, authorize before full content reads,
compare exact material evidence around normalization and fence source/namespace
lifetimes and binding versions. Admission and snapshots share one bounded raw
normalizer preserving absent/null fields. Actual GraphQL transport, denied/
unproved reads, target mutation, same-version source recreation, schema matching
and contradictory client response contracts pass. Complete scoped API/controller
race suites, generation, standalone controller tests, vet and staticcheck pass.
T039 is complete: real GraphQL transport, independent disk-v2 replicas and the
existing manager retain conflicts beyond the retry budget, preserve deadlines
across replacement, settle after watch-projection refresh without duplicate
status writes, and resume persisted fan-out work. Common revision, sticky
binding and exact source body are retained; target mutation is forbidden.
The complete Translation race suite, standalone tests, vet and staticcheck pass.
T050 remains unchecked: authoritative target absence and effective-candidate
integration are still required; runtime registration stays unwired.

T047's whole-page target-source recovery now validates every hint before
mutating any entry and rechecks every observation after page recovery. Invalid
scope/lifetime/UID, duplicate identities, oversized pages, stuck cursors,
cancellation and repair/recheck failures return no accepted continuation.
Raw/decorated memdb and native shared contracts pass, as do unit race tests,
vet and staticcheck. This is not a namespace-wide mutation certificate.

T049 is complete. Schema-validated named/ID reads, typed status CAS, paged lists,
watches, current binding observations, first-bind CAS and bounded dependent
pages reuse the existing HTTP/WebSocket client. Private binding/edge routes
require the live enrolled controller and scoped source permissions. Binding
disclosure/mutation also checks target-read permission against the same thin
serialized target ownership observation; a target-version race cannot bind.
The API alone preserves binding history, source UID/version and reference
epochs. Dependent discovery returns source hints, not target authority, and
continues after target deletion. Public status remains the four-field baseline.
Tests cover pre-list bookmarks, backpressure, sparse/stuck continuations,
source/journal version separation, exact bytes/casing, separate authored and
reconciled revisions, strict condition enums, CAS/provider errors, enrollment,
target ownership/version races, bind replay and unchanged source/status.
Complete API resolver/security/datastore and controller transport/status race
suites, vet/staticcheck and generation pass. Five-kind ownership metadata passes
raw/decorated native and memdb contracts. Runtime registration and deployed
provider/replica acceptance remain later tasks; activation stays disabled.

The application now validates configured Translation activation and the optional
expected descriptor digest before datastore/provider/listener construction,
including Catalog, Admin, SmartHTTP and standalone Storefront roles, and passes
the same value to catalog admission. Defaults remain disabled and backward
compatible; enabled=true is rejected even with a matching digest, and mismatched
digests fail startup. File/environment precedence, all-role rejection and
matching-disabled compatibility tests pass, together with all existing runtime
role/layout startup combinations and complete config/app/catalog race suites,
vet and staticcheck. This completes T011's disabled startup/admission boundary,
not deployment certification, fence backfill or permission to activate.

The dormant API dispatcher now has real signed callback coverage for all five
target kinds and exact authored spec/body/source, unchanged target envelopes,
lost create/update/delete responses, delayed delivery and delete/recreate ABA.
A malformed Translation snapshot now fails before any projection instead of
silently becoming a deletion or allowing a valid sibling write. Constructor
validation still rejects every enabled configuration; only internal-package
fixtures exercise the private dormant state. The complete Catalog RPC race
suite, vet, staticcheck and formatting pass. Receipt-enabled streaming
cancellation now also passes for successful and failed publication, with the
repository guard held through the durable transaction. Native replacement
replay and signed source-order contracts establish the internal ingestion
slice; deployed process-replacement and connected acceptance remain US5 work.

T014/T015/T022/T023 are complete. Dormant named/Node/list reads agree on exact
source/body, accepted revision, authored pins and opaque namespace-bound
forward/reverse continuations. Missing/denied/provider-failed reads fail closed,
and stored status is hidden until the bounded freshness guard is connected.
Committed removal requires the exact source UID/provenance, confirmed manifest
absence and current-tip proof; a source-version preimage prevents deletion of a
replacement lifetime. Complete Catalog RPC, resolver, security and app race
suites, vet and staticcheck pass. Public activation and T024's generic/status/
watch wiring remain incomplete.

T024 is now complete. Typed and generic status mutations share source-version,
generation and accepted-revision CAS, preserve omitted status fields and convert
condition enums explicitly. Their responses still hide unguarded observations.
Typed/generic watches share the existing journal/subscriber and CDC source,
replay through independent/replacement resolvers and preserve tombstones and
explicit historical payloads. Watch permission cannot expose a read-denied
source. Generic middleware now uses the same canonical actions. Complete
resolver/security race suites, vet and staticcheck pass; T025 canonical
default/native verification passes under the race detector, completing T025.
Focused parser, signed ingestion and source/status/watch contracts also pass.
Enabled construction is still rejected.

Native replay now retains the original catalog-routing subset alongside the
complete accepted-ref authorization. The changed-pattern replacement regression
failed first by acknowledging two undelivered callbacks; replay now neither
drops formerly managed refs nor newly admits an excluded tag. Real signed
ReceivePack verifies routing capture before success. The private checksummed
record uses GSTRCV02; old records without routing evidence fail closed and remain
intact, so prior-format queues must be drained by their originating binary
before upgrading. Full native tests (207 library, two binary, eight integration;
one intentional benchmark ignored), strict Clippy, formatting and binary build
pass. This is component/actual-handler evidence, not deployed fault acceptance.

T043's backend dependency/repair and durable handoff core is also verified:
name and bound-UID edges follow source/status/binding changes on both backends;
persisted native retarget/delete boundaries recover through independent stores
without reviving retired placements. The shared source-index, binding,
maintenance and journal contracts pass in both canonical selectors. Disk-v2
pending invalidation requests, bounded fan-out pages and pre-cursor handoff
pass the Translation/listwatch/checkpoint race suites. Implementations reuse
the existing source mutation/publication/maintenance paths rather than adding
a second native dependency writer. Live target-watch/controller registration
remains T049-T052; complete audit/repair watermarks remain T044.

Bounded source-recovery turns and private per-source repair watermarks now
pass unit/raw/decorated memdb/native race contracts. Recovery invalidates old
audit authority before attempting repair and publishes only against an
unchanged coordinator version and final lifecycle. Source/status/binding/death
progress invalidates it. Failed re-audit cannot retain a previous successful
marker. Canonical default/native datastore suites pass under race detection;
native backend191.211s and shared contracts151.576s. These are source-scoped
observations, not namespace completeness or activation certificates.

The separate bounded reservation-audit component also passes raw/decorated
memdb/native race contracts. Distinct cursor scopes prevent cross-use with
source scans. It repairs accepted placements and retires obsolete Reserved and
Committed allocations for live or permanently retired sources, fencing paused
publishers without changing source versions or binding history. Unknown source
absence and lost live UID authority fail closed. Native source-audit regression
tests, vet and staticcheck pass. Independent physical-cell discovery now covers namespace/name/UID layouts,
including missing reservation authority. Unowned Preparing records fail closed
without cancellation guesses. Server-owned Sources/Reservations/Cells/
VerifySources progress survives replacement and advances only with a
full-preimage LWT. The existing operator service/CLI previews bounded discovery
without mutation and confirms exactly one server-resumed repair turn.
Canonical default/native datastore race suites pass (native181.557s,
shared164.153s), as do actual operator replacement/resume tests, the complete
CLI race suite, vet and staticcheck. T044 is complete. Scanned is traversal
history, not mutation-fenced completeness or an activation capability.

**Latest verified internal components**: Disk-v2 scoped name/UID and pin-only
fan-out requests now support partial-write replay, 256-item restartable pages
and pre-cursor handoff, including reused source versions. The target-read
protocol core validates stable fence/source-metadata/fence agreement in at most
three callbacks, including private ownership/material/lifetime proof, explicit
active lifecycle, status-only version advancement and mid-read changes.
Raw memdb/native metadata adapters and instrumentation now cover all five
kinds without target content reads. Internal bounded source-audit discovery now
finds live/retired sources independently of namespace anchors and native UID
locators, including sparse empty turns; scope-bound cursors do not certify
completeness or repair lost authority. None is wired as a complete
runtime feature. T043/T044's backend repair components are complete;
T047 and controller/public integration remain unchecked. Concrete raw/decorated `ReadTargetEvidence` dispatch now uses the
bounded protocol on both backends; mutation-watermarked audit/backfill,
deployment-capability certification and public read-guard wiring remain
unimplemented, so admission remains disabled.

The internal effective-candidate manifest stage now performs real target-read
authorization and five-kind lookup through existing operation identities.
Tests cover create/update/delete/rename order independence, namespace defaults
and isolation, exact pins, unavailable future versions, provider/storage
failures, source/body preservation and all-or-nothing field validation.
Declared field-name conversion preserves missing/null leaves and exact
identities; ambiguous aliases and excessive work fail closed. These contracts
complete T036. Public batch dispatch and committed projection/replay remain
unwired, so T048 and public acceptance remain unchecked.

The internal parsed-entry projector now persists exact Translation source/body
and Git attribution, preserves immutable identity and controller observations,
and propagates source-write failures. Contracts verify duplicate/lost-reply
replay, formatting-only generations, source/status CAS, owner transfer and
current-tip/raw-source/UID-version-fenced removal. Committed-result proof
requires exact raw source, not only semantic spec/body equality. Public entry
points remain disabled; durable callback recovery and complete source-order
acceptance are not established. T014/T022 and T048 remain unchecked.

The common Rust post-receive callback now makes at most three RPC attempts for
transient failures, with 30-second per-attempt deadlines and fresh signed
requests over an unchanged accepted snapshot. Actual-handler attribution/
nonce tests, retry exhaustion, terminal denials and a nonresponding receiver
pass; full Rust unit/integration tests, Clippy and formatting also pass.
Retries remain process-local. Persisted receipt/accepted-ref coordination,
replacement replay and the post-ref-commit scheduling gap remain unresolved,
so T014/T022 are still unchecked and public Translation activation is disabled.

Accepted native ref commits now retain their repository write guard inside
the blocking transaction and schedule successful callbacks there, independently
of client cancellation. The signed streaming-RPC regression failed first on
premature unlock; successful and failed transaction cases now pass, preserving
actor/receive/ref attribution and suppressing callbacks on mutation failure.
This closes request cancellation, not process-loss recovery or the remaining
durable accepted-source delivery work.

The remaining native file-write/tag mutation paths now use owned write guards
inside blocking work, not borrowed async guards that cancellation can release
early. Signed legacy/guarded file-write cancellation tests verify exact content
and guarded receipt recovery after the blocked mutation completes. Repository
creation/deletion and file deletion already follow the same ownership pattern.
This ref-writer audit does not add a persisted callback queue.

API accepted-source callbacks now return retryable `Unavailable` for namespace/
transient ref lookup failures and `Aborted` when bounded convergence is exhausted, rather
than acknowledging incomplete work. Ref lookup's captured-authorization retry
loop is bounded to three attempts. Signed callback regressions failed first,
then passed with unchanged-request recovery and deliberate stale/deleted-ref
skips. Permanent ref-read denials and caller cancellation keep distinct error
codes; proven stale policy denials cannot change newer status or force retries.
This prevents false acknowledgements; persisted delivery and replacement
replay remain unimplemented, so T014/T022 stay unchecked.

The internal native accepted-receipt storage core now persists immutable
repository/receive/actor/full-ref snapshots in versioned, checksum-addressed
protobuf files. It enforces 128 retained receipts, 1 MiB/file, 256 refs/receipt,
one temporary file, 32-entry metadata pages, exclusive OS ownership and fsynced
prepare/commit/acknowledgement transitions. Actual killed-writer replacement,
phase guards, full-queue interrupted-publication recovery and corrupt/unknown
record preservation pass alongside the complete native suite, Clippy and fmt.
No production ref transaction or replay worker uses this core yet; ref-outcome/
lock recovery and callback delivery integration remain required. This is not
T014/T022 completion or an activation capability.

The internal native committed-receipt replay entry point now awaits the
existing signed/retrying callback transport for each applicable ref and removes
the receipt only after every acknowledgement. Tests cover prepared work,
unavailable repository/retained object evidence, partial multi-ref delivery,
retry exhaustion and replacement-handler/store replay with original actor,
receive, refs and changed paths. The current callback protocol still signs an
exact singleton ref envelope per RPC; the retained receipt keeps the complete
accepted vector. This is not a production replay worker or complete repository
lifetime/startup coordination; T014/T022 remain open.

Internal fenced preparation/ref recovery now verifies canonical repository
identity, every original ref preimage and absent native locks before receipt
publication. Unfenced preparation is test-only. Recovery requires uninterrupted
repository exclusion or pre-serving replacement ownership, preflights all
ref/object outcomes, finishes only recorded old/new states, fsyncs published
refs and marks the receipt committed. Divergence preserves refs/lock evidence;
committed receipts cannot undo a later reset. Real killed-child tests cover
prepared, partial lock-held native publication and all-refs-published windows.
Complete native tests, Clippy and formatting pass. Production writers and
startup recovery still do not call these APIs; repository lifecycle fencing
and complete delivery scheduling remain missing, so T014/T022 remain open.

The internal recovery core now retains every distinct nonzero accepted old/new
object with existing hidden/protected Git refs before publishing public refs.
Production commit/discard/acknowledgement bypasses are removed; raw storage
transitions remain test-only. Fenced acknowledgement adds a durable cleanup
phase before deleting exact owned pins and the receipt, rejecting preexisting
native locks and preserving interrupted cleanup authority. Replay acquires the
caller's repository write guard only for blocking cleanup after network
acknowledgements. Actual ref packing/pruning verifies retained commits survive
removal of all public branches and become collectible after cleanup; a fourth
killed-child window verifies native cleanup-lock recovery. These internal
components still need production writer/startup and all-writer lifecycle wiring;
T014/T022 and public activation remain incomplete.

Production native ReceivePack now persists its accepted actor/full-ref vector
before success, after durable object promotion and before ref publication.
A bounded validated metadata index fences every native mutation path during
Prepared/Acknowledged recovery, prevents old cleanup crossing newer pending
work and blocks repository deletion/recreation around retained delivery.
The binary recovers/validates retained lifetimes before binding RPC, then runs
one notified, bounded-page replay worker with a one-second fallback. Failed
delivery is retained without preventing unrelated cleanup. Signed native
multi-ref RPC, corrupt-record mutation gates, startup/next-write recovery,
storage-loss recreation fencing and replacement snapshot contracts pass.
The actual replacement replay loop also completes partial delivery with
unchanged singleton envelopes. Durable promotion preserves bounded small-pack
loose-object publication, including already-packed objects. Full native
unit/integration suites, strict Clippy, formatting and production binary build
pass; one intentionally ignored timing benchmark remains opt-in.
Full source-order, actual-handler cancellation/replacement and connected
fault acceptance remain incomplete; T014/T022 remain unchecked.

Source-audit recovery now checks discovered scope, lifetime, private/source
versions and phase before invoking existing serialized source recovery.
Raw/decorated memdb and native Scylla contracts pass; missing namespace cells
reuse accepted allocations. Lost live UID locators remain repair-required rather
than inventing frozen name-vacancy authority. Native retained Retired sources
can conditionally restore their retired UID locator without reviving the UID,
overwriting contradictory lifetimes or changing replacement name ownership.
Native audit also requires retained source authority through its final read:
an absence-only hint cannot invoke provisional-source retirement or release
remaining UID/name claims. The regression failed first, then passed alongside
ordinary creation recovery and raw/decorated audit/maintenance contracts.
These are isolated metadata/state-injection tests, not full audit completion,
server-owned progress, mutation-watermark certification or physical-fault proof.
T044 and T047 remain unchecked.

The shared raw/decorated datastore suite now covers T004's source/status CAS,
lookups, bounded pages, binding budgets/history, segmented allocation and
publication, maintenance/discovery/recovery, target fences/evidence and shared
journal. Canonical default and sequential native Scylla suites pass under the
race detector. This completes the contract entry point and T007's
backend-neutral entity/capability interfaces, not runtime activation or later
full repair/backfill acceptance. Interfaces live beside their Translation
contracts in `internal/datastore/translation.go`, `translation_audit.go` and
`translation_targets.go`; optional capabilities preserve compatibility with
disabled deployments and existing resource-only datastore test doubles.

**Verified component progress**: Setup, the strict source parser, shared
locale/path components, authored/GraphQL schemas and common condition enum
conversion are implemented and tested. Memdb source/status CAS, name uniqueness,
bounded bidirectional paging and shared-journal events pass backend-neutral
contracts, including race detection and the instrumented-store path.
Typed read/list/watch/status and Node authorization use canonical actions and
field-scoped source snapshots. Real strict-RBAC contracts now separate source
read/update grants from list/watch and controller-ServiceAccount status grants;
source reads cannot authorize any of the five target kinds' Node reads.
Named/Node/batch denials conceal present-versus-missing source existence, and
provider-unavailable errors retain retryable envelopes without private causes.
Generic status/watch backends remain unregistered. Focused race tests, vet and
staticcheck pass; canonical `make generate TARGET=authz` verification also
passes. Git Smart HTTP contracts also verify that source read/update grants do
not permit repository writes, ownership-conditional author grants reject other
owners, and allowed pushes retain the original actor and canonical action.
This completes T005's foundational isolation contracts and T010's generated
action family. Complete public read-guard/redaction and configured-route
acceptance remain T053-T056 work; no public backend is enabled by these tests.
The additive Scylla schema passes live migration, partition-key and canonical
envelope contracts in an isolated test-owned keyspace. Scylla source creation,
UID/name reads and creation recovery also pass isolated cross-client tests;
the complete source CRUD/list/journal, ownership and per-source maintenance
contracts now pass against both backends. Full audit/backfill and target
integration remain unfinished.
Shared target descriptors now hash their actual field, identity, reference and
work-limit policies. Bounded cancellable snapshot validation, explicit option
and media identities, text constraints and deferred extraction boundaries pass
standalone and API-consumer race tests.
The internal manifest stage now validates supplied authorized target snapshots,
including effective namespace, exact pins, unavailable pin allocation and
all-or-none field outcomes; it does not perform target lookup or authorization.
Memdb source/status transitions atomically preserve sticky binding history,
with source-fenced first-bind CAS, pin-toggle/name-return protection and
separate epoch/byte budgets. Lost history fails closed rather than being reset.
The same binding contracts pass through instrumented access.
The common segment-allocation contract passes on memdb and live Scylla.
Scylla uses SERIAL point reads and SERIAL/QUORUM LWT, durable Preparing/Reserved
records, non-reused retry tokens and pending head ownership. Cross-store tests
cover byte rollover, persisted-state interruption recovery, stale-plan
replanning, all three layouts and timestamp precision. These are isolated
single-node database/state-injection tests, not deployed physical-fault proof.
The single unreleased `015_translation.cql` includes the constant head token
and reservation recovery metadata.
Index publication/removal now passes shared memdb/Scylla contracts and live
cross-store tests. Retained retired cells and non-reused slot ownership prevent
paused publishers/removers from resurrecting or changing newer placements.
Persisted cell-before-acknowledgement state injection and concurrent publication
versus removal cover all three layouts, not physical-fault acceptance.
The same development migration includes retirement metadata;
existing migration upgrade tests verify retained data and unchanged old ledger
entries through the expanded bundle. Name-index physical partitions use an
exact-identity digest to bound key bytes without case-folding authored names.
Reverse-index paging now passes shared memdb/Scylla contracts, including
retired-row filtering, segment rollover, sparse empty continuations and exact
target/cross-namespace cursor isolation. Each turn scans at most 256 cells,
including retired cells, and spends at most 16 head/segment probes.
Pure-driver tests cover cancellation, no partial success on I/O failure and
resuming a saved head across empty segments. Live tests resume cursors through
independent/replacement stores and exercise a logical target name exceeding
the native composite-key size. These are isolated store tests, not connected
controller/deployed acceptance.
The same development migration includes private source coordination,
a co-located binding snapshot and the frozen name-vacancy token. The previously
split development migrations have been consolidated with explicit approval;
published migrations `001`–`014` remain unchanged.
Full-envelope creation, concurrent name ownership and persisted Creating-state
recovery pass isolated live tests. Source/status/binding update methods and
recoverable Updating intents pass independent-client live tests. Concurrent
recovery resumes the same frozen intent after persistence, reservation,
publication and old-placement removal; source/first-binding races preserve
the winning incarnation history. These are isolated persisted-state injections,
not physical-fault or deployed-replica acceptance. Durable intents preserve raw
spec/status/owner-reference bytes, including nil status, without JSON compaction.
Local envelope/binding-evidence, generation/identity and corrupt-intent preflight
tests also pass in the canonical datastore suite.
The mutation path validates old placement identity before any new index writes.
Fresh-schema and upgrade tests pass through the consolidated fifteen-file
bundle, preserving existing identities/data and the checksums/write timestamps
of previously applied migrations.
Recoverable final deletion now passes independent-client live tests, including
source/delete races, five injected persisted boundaries and replacement name
ownership before old cleanup. Retained retired UID/source records and frozen
vacancy tokens prevent delayed resurrection; payload compaction precedes final
namespace-anchor retirement. Missing provisional sources are fenced at the
exact authoritative key before discovery is removed.
Both backends now use bounded directional namespace paging with immutable
lifetime anchors, shard/segment-major ordering, layout-v2 namespace-bound
cursors, per-item cursors and advancing empty turns. Memdb source/status/binding
and deletion update reverse projections atomically. Read-only authoritative
maintenance headers and bounded namespace discovery include pending/unpublished
sources. Stable audit restores missing thin cells using existing allocations,
with serialized source-header checks and retained retirement fences; missing
heads, retired accepted placements and contradictions fail closed. Live
cross-client and memdb tests verify allocation/version/history preservation
and preflight behavior. These are isolated component/corruption-state tests.
Translation now registers its custom decoder in the shared catalog CDC runner.
Unit phase/identity/version/frame contracts and an isolated live native-reader
test pass under race detection: provisional and binding-only transitions remain
silent, full source/status/final deletion publish, and a replacement reader
replays committed writes while it was down without duplicating events.
Ownership/repository membership now participates in source acceptance, mutation,
deletion and stable repair. Scylla retains co-partitioned private retry markers
and changes each live projection in the same conditional batch, fencing delayed
creation, retarget, ABA, repair and deletion workers. Existence queries seek
past marker rows; repository membership reuses its existing 32 shards.
Generic repair excludes Translation-owned rows instead of bypassing these
fences. Live tests verify missing hints, source compaction and corrupt markers;
the shared ownership contract verifies stale source writes cannot change
winning owner edges or repository blockers. Malformed/oversized owner metadata
is rejected before reserving source identity.
Source, binding and index/maintenance capabilities now survive instrumentation.
All sixteen shared Translation entries pass against native Scylla under race,
including the native shared CDC source/status/delete journal contract and a
decorated index lifecycle. Target write-fence metadata now has memdb and native
Scylla implementations, including instrumentation, all five GVKs, exact-name
hashing, 16-KiB serialized intent budgets, non-reused write tokens and durable
STABLE/UPDATING transitions. Replacement-store tests resume the original frozen
intent and authoritative creation timestamp. Status-only resourceVersion
advances can retry the same material intent without changing its token or
snapshot; completion requires a later version, while stale expectations,
generation/namespace regression and malformed evidence fail closed.
Missing memdb fence payloads return repair-required without panicking or
resetting retained state. These are isolated metadata/state-injection tests;
material writers, complete UID-retirement recovery, authoritative fence/row/fence evidence,
full audit/backfill, repair watermarks and public route integration remain
pending. In particular, low-level fence operations alone do not complete
T004/T024/T044/T046/T047. The complete default and native datastore suites pass,
alongside focused race-enabled target-fence, replacement-store and corruption
tests, vet/staticcheck and the standalone API build. Namespace-only ownership
also passes shared backend contracts: an empty repository ID is an exact owner
scope, not a missing index key or a wildcard.
The low-level failure-injection tests, sticky binding implementation and
per-target fence metadata operations complete T037/T042/T045, not material-writer
or current-Ready integration. Isolated store tests are not deployed
physical-fault proof.
Completed target deletion now retains kind-scoped UID retirement on both
backends. Shared five-kind contracts cover deletion/name replacement, rejection
of a retired UID at another authoritative key, wrong-version deletion without
retiring the live resource and cross-kind UUID isolation. Memdb also preserves
inactive-parent error precedence. Native point reads check private retirement
authority after reading the row; lookup projections and a delayed physical row
cannot revive a retired UID through these reads. Isolated native Product
state injection verifies replacement-store reads and generic projection repair.
Five-kind native state injection verifies both UID/name point reads. The four
raw namespace-page paths fail closed on retired rows; the existing Product
hydration path already excludes them. Both backends now reuse the 250-item
catalog ceiling for all five target kinds and reject negative direction sizes.
Shared negative-page contracts and actual tagged memdb Translation contracts
pass under race detection. Native repair also removes exact physical retired
target keys under a fresh permanent-retirement proof, restoring raw pages;
retirement facts remain retained. Isolated native tests verify all-kind page
recovery and that a serialized plan cannot substitute for missing authority.
Retired targets are excluded from repair authority, and saved reservation-row
cleanup plans recheck permanent retirement rather than treating an ordinary
missing row as sufficient proof. Native final deletion now prepares a retained
UID claim with the frozen source key, records a private retired marker in the
source expected-version CAS, publishes permanent UID retirement and only then
compacts the source. All five point readers use SERIAL source reads; pending
claims consult the original source proof and do not hide a still-live row.
Normal/status/termination updates and creation compensation cannot clear the
proof. File creation now reserves UID identity before source insertion.
The shared catalog CDC decoder treats retirement as deletion and suppresses
later physical compaction. Five-kind persisted-state tests verify live pending
claims, unpublished source retirement and repair promotion before compaction.
Real native deletion calls also pass one-shot publication/compaction interruption
tests under race detection: replacement stores reject delayed alternate-key
rows through the pending claim's original source proof, and confirmed repair
removes both physical keys after permanent publication. Shared five-kind
contracts also reject live-UID creation at another key without moving the
original resource or publishing an alias. These are isolated failure-injection
and state-replay tests, not deployed physical-fault acceptance.
The shared exact-material digest now bounds combined raw spec/body bytes at
100 MiB and checks cancellation every 32 KiB. Memdb canonical creates and
advancing-version material updates for all five target kinds atomically commit
namespace UID/token/digest with source and stable name evidence. Ordinary DTO
clones and JSON/YAML omit private proof; status-only changes preserve it.
Material publication conflicts with a pending foreign owner. Lifecycle source
updates use the same path; final deletion and old-name vacancy commit atomically.
Projection errors roll back both source and head. Tests cover all five kinds,
source privacy, caller-proof rejection, pending ownership, projection rollback,
legacy rename/name reuse and legacy File equal-version deletion/replacement.
Name movement and non-advancing File material updates deliberately lose positive
proof without breaking existing CRUD; they still need supported proof/backfill
semantics. No positive read capability has been added.
Native target source creates and updates now persist namespace ownership and
exact-material digest in the same authoritative CAS. Updates also condition
on the exact previous nullable private identity: five-kind live tests interpose
an ownership-marker change without a public-version advance and verify that
the paused writer conflicts without changing source. Tests cover digest changes,
status-only content, rejected versions, retained old namespace ownership,
empty/NULL legacy metadata without epoch adoption and malformed/oversized
metadata rejection. Native hashing streams string-backed specs without another
whole-spec allocation. Full native testing exposed namespace-less legacy
creation compatibility, now preserved without namespace proof; the full native
and default canonical suites passed after the fix.
For explicitly seeded proved native sources, actual material-update paths for
all five kinds now reserve a new fence, commit its token in the guarded source
CAS and publish matching evidence. Committed-before-publication interruption
returns repair-required and recovers from the stored token. Bounded update
recovery freezes namespace/key/previous nullable source identity, CAS-cancels
paused writers before restoring old material, retains the new token and
distinguishes canceled markers from same-content status advances. A
replacement-store persisted-boundary test verifies both cancellation and
committed outcomes. The pure abort planner rejects changed material, stale
tokens/versions and invented absence, and never resets an old token. File
status uses a source status-only CAS, preserving its material token and body.
These are isolated component and seeded-proof tests, not backfill, automatic
pending discovery or deployed physical-fault acceptance.
Canonical native births now reserve a name fence, commit their originating token
with the actual source insert and publish stable evidence. Five-kind native
tests cover ordinary birth/update/delete/name replacement without seeded proof.
Incomplete legacy envelopes retain CRUD without acquiring proof.
Bounded birth recovery proves a committed token or CAS-inserts a negative source
marker before permanent UID retirement and stable absence; a missing-row read
alone never cancels a paused insert. Persisted negative-marker/absence-publication
interruption and committed-before-publication recovery pass through independent
stores. Source creation compensation now guards the original private identity
and uses retained UID retirement/name vacancy. Product and File projection-error
tests verify that rollback does not leave positive evidence over a missing UID.
Native final deletion reserves a proved intent, guards source version/private
identity, then publishes permanent UID retirement and stable name absence before
source compaction. An actual paused-delete test proves that cancellation fences
the old source CAS even without advancing public resourceVersion. Generic
retirement repair now settles the owned pending head before removing its source
proof; its regression failed first and then passed. Product and category
foreground termination advance the material fence; the category bypass failed
first and now reuses the fenced authoritative writer.
These are isolated component and persisted-boundary tests, not deployed
physical-fault or legacy backfill evidence. No positive
target-read capability has been added.
An actual paused source INSERT now resumes after negative recovery and cannot
complete its canceled head. Retained UID retirement hides the late physical
row even after restoring its lookup hint; repair removes it without changing
replacement evidence. Legacy equal-version File material updates first failed
the token-invalidation assertion and now reserve recoverable invalidating
intents. A source invalidated marker distinguishes committed unprovable
material from cancellation; stable completion revokes evidence without retiring
the live UID. Repeated legacy changes and final deletion/name replacement pass.
Stable evidence absence is not, on its own, authoritative target absence.
Native pending-target discovery now freezes the intent, expected old head and
actual new token before publishing segmented discovery and reserving the head.
Retained namespace anchors include deleted namespaces; replacement stores resume
pre-head orphans with the same token. Head advancement safely obsoletes a stale
locator without touching replacement ownership. Completion closes the retained
locator before compacting its derived pending cell; an exact delayed cell replay
cannot restart the old head CAS. Discovery/recovery use the existing allocator
and pager, separate durable phases, <=16 head/segment probes and <=256 cells per
turn. Per-namespace cursors survive process replacement, yield after four turns,
and do not advance past failed work. The API lifecycle starts the bounded worker,
retries and joins it before datastore closure. Instrumented stores retain the
capability. Race tests cover birth/update/invalidation/delete, pre-head and
pre-source failure, committed source, interrupted locator retirement, paused
Begin, closed-cell replay, deleted Namespace, foreign owner, alternating stores
and concurrent workers. API lifecycle start/retry/shutdown and cursor isolation
tests pass. These are isolated component/state-replay tests, not deployed
physical-fault or recovery-time evidence.
This does not complete T046: supported legacy ownership/upgrade semantics,
complete every-writer verification, authoritative evidence and mutation-
watermarked backfill remain unfinished.
Public activation remains disabled.
Typed Translation diagnostic conversion, explicit common-reason mappings,
32-finding/256-byte diagnostic budgets and an explicit truncation message are
implemented. Structural ingestion now preserves typed locations/reasons instead
of splitting error text. YAML duplicate-key locations retain their field path,
and malformed frontmatter with a recognizable Translation header stays on the
strict, safe diagnostic/activation path. Shared negative-corpus parity,
malformed-YAML and diagnostic-budget tests pass, as do the complete parser,
cataloggrpc, admission and admission-report suites under the race detector.
Vet/staticcheck and the standalone API build pass. Full candidate/provider,
overload, public-ingestion and report integration remain incomplete; this does
not complete T058-T063.
Foundation remains in progress: full binding/index/fence contracts and activation
wiring, full audit/target-fence integration, reconciliation, freshness guards and
connected/deployed evidence are not done. Internal components do not unblock
public story acceptance.
Activation is hard-disabled, including constructor attempts to enable it;
callback gates also precede mixed RBAC projection.

## Format and execution rules

- Checklists use `Tnnn`, optional `[P]`, and `[USn]` on story tasks.
- `[P]` means disjoint-file work can run concurrently once its stated
  prerequisites are complete, not that it can bypass preceding test/contracts.
- All implementation paths below are relative to the worktree root
  `/Users/julius.krah/Documents/GitHub/GitStore/.gitstore/worktrees/translation-contract-spec/`.
  New Translation-specific files are planned paths; reuse an existing helper
  before introducing another one. Read scoped instructions before editing.
- Keep public admission and accepted-callback writers consistently disabled
  until the complete readiness/activation prerequisites pass. US1's initial
  source-preservation slice is an internal MVP, not permission to accept
  incompletely validated public documents.
- Preserve #40's `conditions`, `observedGeneration`, `lastAppliedRevision` and
  kind-specific `resolved`. The revision belongs to the Translation observation,
  including successfully reconciled unresolved/invalid outcomes, not target
  application. Accepted source attribution remains independent; no replacement
  `observedRevision`, `resolved.appliedFieldPaths` or `ProjectionUpdated`.
- Preserve #549: explicit UpperCamelCase authored-enum to SCREAMING_SNAKE_CASE
  GraphQL conversion; reuse condition converters. Keep the four common status
  fields, with kind-specific results under `resolved` and validation fingerprints
  private. Freshness is a service guard, not an extra public status field,
  timestamp or duplicate observation envelope. Locale canonicalization is a
  separate BCP 47 rule; never case-fold names, paths, patches or body content.
- No new service, per-kind RPC/journal, localized materializer, implicit
  reference traversal, custom-kind activation, Git replication or public
  feature-specific Make target.
- Approved placement refinement: pure locale/path/descriptor/field semantics
  live in `shared/translation`, imported by API and controller. Service-owned
  authorization, metadata, I/O, persistence and checkpoints stay local. Keep
  module workspace/aggregate/standalone-CI and consuming-builder wiring in
  the same implementing slices; do not add a validation RPC.

## Phase 1: Setup

**Purpose**: Establish reusable acceptance inputs without adding tooling.

- [X] T001 Create valid, negative and deferred Translation fixtures in `gitstore-api/internal/cataloggrpc/testdata/translation/`, with matching targets for all five delivered GVKs, field/body/combined examples, exact-byte body cases and proposal-only keyed/shared-definition cases distinguished from supported input.
- [X] T002 [P] Add failing disabled-by-default activation contracts in `gitstore-api/internal/cataloggrpc/translation_activation_test.go` for validation, admission and accepted-callback replay writers, including inconsistent capability/descriptor configuration and old/new replica overlap.

## Phase 2: Foundational contracts

**Purpose**: Shared contracts and safe deployment gate; blocks story implementation.

### Tests first

- [X] T003 Add failing model/schema contracts in `gitstore-api/internal/catalog/translation_test.go` and `gitstore-api/internal/admin/graph/resolver/translation_schema_contract_test.go` for identity, the four-field common status baseline, kind-specific resolved results, private validation fingerprints, independent accepted source attribution, common enum conversion and absence of target-application claims.
- [X] T004 [P] Extend `gitstore-api/tests/contract/datastore/contract_test.go` with a Translation contract entry point covering source/status CAS, name/UID lookup, opaque bounded paging, durable binding, segmented thin indexes, repair intents, target fences and the shared journal; reuse the suite for both backends.
- [X] T005 [P] Add failing route/action isolation contracts in `gitstore-api/internal/admin/graph/resolver/translation_authorization_test.go` for typed/generic/Node/list/watch/status routes, repository ownership, target-kind reads, controller-only status, provider failure and existence-safe redaction.

### Shared implementation

- [X] T006 Define Translation envelope/observed status/resolved evidence in `gitstore-api/internal/catalog/translation.go`, pure source/spec/patch/descriptor types in `shared/translation/`, and service-owned reference epoch/binding types; preserve ordered patches, raw locale/body/source and #40's `lastAppliedRevision` without a parallel revision field (after T003).
- [X] T007 Define backend-neutral Translation entity/store, binding, maintenance-intent, dependent-page and target-evidence fence interfaces in `gitstore-api/internal/datastore/entities.go` and the adjacent Translation contract files, using existing resourceVersion CAS and bounded cursors rather than new datastore abstractions (after T004, T006).
- [X] T008 Add the strict authored JSON Schema in `schemas/i18n.gitstore.dev/v1beta1/translation.schema.json`, matching the selected envelope, writable metadata/reference subset, patch limits and forbidden system fields without pretending schema alone validates target content (after T006).
- [X] T009 [P] Add additive Translation Node/connection/selector, four-field common status and kind-specific resolved GraphQL contracts in `shared/schemas/admin/translation.graphqls` and `shared/schemas/admin/schema.graphqls`; expose Git authoring only, controller status through existing conventions, and common GraphQL enum names (after T003, T006).
- [X] T010 [P] Extend `shared/schemas/authorization-actions.json` with the actual Translation route action family, including `translation.status.write`, and regenerate via `make generate TARGET=authz`; preserve the existing provider boundary and avoid inventing an updateStatus action (after T005).
- [X] T011 Implement consistent disabled admission/callback activation and descriptor-capability checks in `gitstore-api/internal/cataloggrpc/translation_activation.go` and its wiring in `gitstore-api/internal/app/`, so unsupported replicas cannot process Translation while pre-activation traffic remains compatible (after T002, T006).
- [X] T012 Run `make generate TARGET=all`, update generated bindings in `gitstore-api/internal/admin/graph/model/` and `gitstore-api/internal/admin/graph/generated/`, and satisfy T003's schema checks without hand-editing generated sources (after T008-T011).

**Checkpoint**: Reviewed types, API/action/store contracts and disabled deployment
gate exist. No supported-runtime claim follows from generated schemas.

The canonical full generation command and generated Translation/common-status
schema contracts pass after T008-T011. All foundational tasks are complete;
runtime source recovery, public backends and deployment acceptance remain
unchecked below.

## Phase 3: US1 - Submit a valid Translation (P1; internal MVP)

**Goal**: Preserve complete authored Translation source through the normal
ingestion/projection and read paths without mutating its target.

**Independent test**: For matching owned targets, ingest field-only, body-only
and combined fixtures for all five delivered GVKs; compare namespace/name,
UID/generation, raw locale/reference/patch order/body bytes and accepted
commit/blob attribution on named/Node/list reads, with unchanged target digests.
Run the full public-ingestion version after US2-US4 integration; do not bypass
their checks or enable admission to demonstrate the early MVP.

### Tests first

- [X] T013 [US1] Add parser tests in `gitstore-api/internal/validate/translation_test.go` for the selected envelope, explicit empty versus missing/null text, writable metadata, both content channels and exact UTF-8 suffix preservation across LF/CRLF, opening/closing delimiter edges, blank lines, embedded delimiters and invalid UTF-8 (uses T001).
- [X] T014 [P] [US1] Add source-ingestion/replay tests in `gitstore-api/internal/cataloggrpc/translation_admission_test.go` for atomic whole-document projection, source ordering, duplicate/lost/delayed callbacks, stable UID/generation and unchanged target source/resolved content.
- [X] T015 [P] [US1] Add read/source contracts in `gitstore-api/internal/admin/graph/resolver/translation_contract_test.go` for named/Node/list agreement, opaque namespace-bound pagination, raw content preservation, independent accepted revision visibility and no unguarded current Ready before controller evidence exists.

### Implementation

- [X] T016 [US1] Implement strict Translation-only YAML node validation and byte-offset body extraction in `gitstore-api/internal/validate/translation.go`, then register it in `gitstore-api/internal/validate/validator.go` and `ParsedResource`; preserve existing other-kind framing/behavior (after T013).
- [X] T017 [P] [US1] Implement Translation source/status CAS, name reservations, thin bounded lists and shared memdb journal behavior in `gitstore-api/internal/datastore/memdb/translation.go`, reusing the existing backend and T004's contracts rather than process-local production correctness state.
- [X] T018 [P] [US1] Add failing migration/table/CDC-shape tests in `gitstore-api/internal/datastore/scylla/migration_schema_test.go` and the existing `gitstore-api/internal/datastore/scylla/migration_test.go` for resource-sized authoritative keys, thin segmented projections, name reservations and append-only migration history.
- [X] T019 [US1] Append `gitstore-api/internal/datastore/scylla/migrations/015_translation.cql` only after confirming `014_rbac.cql` remains the current lexical maximum; renumber upward if it has advanced. Include authoritative source, names, segmented lists/dependencies, bindings, maintenance allocation/intent state and target fences; CDC only on the authoritative row with full images and 14-day TTL (after T018).
- [X] T020 [US1] Implement resource-sized authoritative Translation CRUD/CAS, reservation-safe identity lookup and thin list pagination in `gitstore-api/internal/datastore/scylla/translation.go`, using the existing serialized consistency path and complete source envelope (after T019).
- [X] T021 [US1] Implement durable source-to-index maintenance intents and idempotent segment/slot reservations in `gitstore-api/internal/datastore/scylla/translation_indexes.go` and memdb parity in `gitstore-api/internal/datastore/memdb/translation_indexes.go`; enforce 64 shards, 16,384-row/64-MiB segment ceilings, page 256 and at most 16 probes/turn with resumable empty continuations (after T017, T020).
- [X] T022 [US1] Extend comparable identity/body, candidate dispatch, upsert/delete and callback replay in `gitstore-api/internal/cataloggrpc/admission_operations.go`, `gitstore-api/internal/cataloggrpc/manifest_validator.go` and `gitstore-api/internal/cataloggrpc/committed_manifest.go`; preserve accepted source order and recover interrupted projection without author resubmission (after T014, T016, T021).
- [X] T023 [US1] Implement thin typed Translation reads through `gitstore-api/internal/admin/graph/resolver/translation_service.go` and `gitstore-api/internal/admin/graph/resolver/translation.resolvers.go`, including common authorization, raw source/body, namespace-safe cursors and fail-closed status until US3's guard is connected (after T015, T022).
- [X] T024 [US1] Register Translation Node/global-ID, generic resource/list/status and shared watch serialization in `gitstore-api/internal/admin/graph/resolver/schema.resolvers.go`, `gitstore-api/internal/admin/graph/resolver/status_generic.go` and `gitstore-api/internal/admin/graph/resolver/watch.go`; register authoritative CDC in `gitstore-api/internal/datastore/scylla/catalog_cdc.go`, never a per-kind journal (after T023).
- [X] T025 [US1] Run focused parser/ingestion/read tests and the source/CAS/list/journal subset of `gitstore-api/tests/contract/datastore/contract_test.go` through `make test TARGET=datastore`, then the same tagged Scylla subset against an isolated owned keyspace via `make test TARGET=datastore DATASTORE=scylla`; retain failures/skips separately and verify T013-T015 source/target digests. Full binding/fence contracts complete at T057 (after T024).

**Checkpoint**: Internal source-preserving vertical slice. Public US1 acceptance
also requires US2, US3 and US4; all production activation remains blocked.

## Phase 4: US2 - Address nested text predictably (P1)

**Goal**: Resolve only explicitly eligible text using stable selectors, with
deterministic conflicts and hard reference boundaries.

**Independent test**: Permute target list order and run the path corpus; each
supported address resolves to one identical eligible leaf or the documented
failure. Body roundtrips remain byte-exact. Deferred keyed/shared cases must
not become runtime support.

### Tests first

- [X] T026 [US2] Add grammar/limit tests in `shared/translation/paths_test.go` for dot/brace nesting, exact case-sensitive selectors, positional/wildcard/executable/escape rejection, 1,024-byte paths, 16 segments, eight selectors and duplicate/overlapping/equivalent-leaf rejection.
- [X] T027 [P] [US2] Add descriptor tests in `shared/translation/paths_test.go` for all five GVKs, reorder invariance, zero/multiple selector matches, existing title limits/optional empty titles, File body-only support and all operational/reference/non-text exclusions.
- [X] T028 [P] [US2] Add inline/shared extraction design fixtures in `gitstore-api/internal/catalog/translation_related_test.go` covering current option name identity, deferred nested key identities, forbidden definitionRef traversal, unchanged variant selections, explicit defining-UID retargeting and unsupported AttributeDefinition/custom kinds without implementing them.

### Implementation

- [X] T029 [US2] Implement the bounded parsed path grammar and deterministic source-level duplicate/ancestor checks in `shared/translation/paths.go`, rejecting `{body}` as an authored path and preserving authored order (after T026).
- [X] T030 [P] [US2] Implement immutable per-GVK/version text/list/body descriptors and stable digests in `shared/translation/targets.go`; use Product option name and explicit media fileRef.name identities, never name/key guessing or generic all-string eligibility (after T027, T028).
- [X] T031 [US2] Implement bounded target-owned selector indexes, leaf eligibility, equivalent-address conflict checks and existing field text constraints in `shared/translation/targets.go`; stop at reference boundaries and never mutate target values (after T029, T030).
- [X] T032 [US2] Connect source/descriptor-decidable path checks and available-target field validation through `gitstore-api/internal/validate/translation.go`, `gitstore-api/internal/validate/translation_target.go` and the internal stage in `gitstore-api/internal/cataloggrpc/manifest_validator.go`, retaining unresolved field outcomes when permitted targets are absent; use supplied authorized target snapshots at this stage, with real effective-candidate lookup added later by T048 (after T031).
- [X] T033 [US2] Extend `gitstore-api/internal/cataloggrpc/translation_admission_test.go` with reorder, overlap, mixed-valid/invalid, text-limit and body-byte contracts using supplied authorized target snapshots, proving zero partial acceptance and unchanged supplied source/target content; real effective-candidate lookup, persisted target digests and connected ingestion are verified in US3/US5 (after T032).
- [X] T034 [US2] Run the focused shared-module path/descriptor and API parser/internal-manifest cases in `shared/translation/`, `gitstore-api/internal/validate/`, `gitstore-api/internal/catalog/` and `gitstore-api/internal/cataloggrpc/`, retaining separate unsupported outcomes for every proposal-only fixture; public ingestion remains activation-blocked (after T033).

**Checkpoint**: Supported paths are exact, bounded and stable under reorder;
no translated identity, implicit traversal or localized target application.

## Phase 5: US3 - Understand locale and target validity (P1)

**Goal**: Distinguish invalid source, permitted unresolved targets and observed
validity; keep Ready truthful during concurrent target changes and recovery.

**Independent test**: Exercise BCP 47 equivalence/invalidity, same-candidate
ordering, missing-to-resolved, invalid-to-valid, pin mismatch, deletion/name
reuse and explicit retargeting. Interleave target writes/status CAS/read guards
and verify zero false current Ready on every representation and both backends.

### Tests first

- [X] T035 [US3] Add locale tests in `shared/translation/locale_test.go` and raw-source assertions in `gitstore-api/internal/validate/translation_test.go` for scripts/regions/variants, extensions, private-use/grandfathered and unregistered well-formed subtags, aliases/case equivalence, malformed/duplicate subtags, whitespace/underscore rejection and lossless raw locale preservation.
- [X] T036 [P] [US3] Add candidate/reference tests in `gitstore-api/internal/cataloggrpc/translation_targets_test.go` for create/update/delete/rename independent of file order, namespace defaults/isolation, effective same-candidate targets, missing permitted targets, exact UID/resourceVersion pins and provider/I/O failure versus absence.
- [X] T037 [P] [US3] Add binding/index/fence failure-injection contracts in `gitstore-api/tests/contract/datastore/translation_contract_test.go` for CAS races, sticky UID history through pin toggling/restarts, 256-epoch exhaustion, reservation interruption/rollover, retarget/delete repair and interrupted target STABLE/UPDATING publication.
- [X] T038 [P] [US3] Add deterministic guard tests in `gitstore-api/internal/admin/graph/resolver/translation_freshness_test.go` for target-read/status-CAS races, post-CAS mutation, Translation ABA/version recheck, stale derived identity indexes, delete/name reuse, pins/status-only updates, unavailable fences/I/O and bounded retry exhaustion.
- [X] T039 [P] [US3] Add reconciliation/checkpoint tests in `gitstore-controller-manager/internal/translation/reconciler_test.go` and the external-package `reconciler_checkpoint_test.go` for common conditions/revision, missing/replaced/invalid targets, duplicate workers, lookup errors, durable pending retries/fan-out cursor recovery and no target mutation.
- [X] T040 [P] [US3] Add freshness/enum/redaction route contracts in `gitstore-api/internal/admin/graph/resolver/translation_status_test.go` for typed/Node/list/generic/status-mutation/live-watch and historical replay, ensuring stored True/internal enum spellings cannot escape as current GraphQL success.

### Locale and reference implementation

- [X] T041 [US3] Implement bounded RFC 5646 grammar and pinned x/text canonical comparison in `shared/translation/locale.go`; preserve unknown well-formed subtags rather than replacing them with und, keep raw source and require the existing x/text version directly in `shared/translation/go.mod` (after T035).
- [X] T042 [P] [US3] Implement reference epochs and sticky binding/history CAS in `gitstore-api/internal/datastore/scylla/translation_mutation.go` and `gitstore-api/internal/datastore/memdb/translation_bindings.go`; preserve old-incarnation history, reject a 257th distinct epoch and require explicit new-UID retargeting after name reuse (after T021, T037).
- [X] T043 [US3] Implement name/UID reverse edges, durable pending invalidation and interrupted retarget/delete repair in `gitstore-api/internal/datastore/scylla/translation_dependencies.go` with equivalent `gitstore-api/internal/datastore/memdb/translation_dependencies.go`, sharing segmented allocation and maintenance-intent rules (after T042).
- [X] T044 [US3] Extend projection audit/repair in `gitstore-api/internal/datastore/scylla/repair.go` and `gitstore-api/internal/datastore/scylla/translation_repair_test.go` for source/binding/index agreement, stale/deleted edges, interrupted reservations and repair watermarks, retaining source and binding authority during incomplete repair (after T043).
- [X] T045 [US3] Implement per-target serialized fence/intent operations in `gitstore-api/internal/datastore/scylla/translation_target_fences.go` and `gitstore-api/internal/datastore/memdb/translation_target_fences.go`, with non-reused CAS tokens, durable recovery and STABLE/UPDATING metadata checks; do not claim a cross-resource transaction (after T037, T020).
- [X] T046 [US3] Integrate fences into every Product, ProductVariant, CategoryTaxonomy, Collection and File material writer in `gitstore-api/internal/datastore/scylla/backend.go`, `gitstore-api/internal/datastore/scylla/file.go` and matching `gitstore-api/internal/datastore/memdb/` writers; cover create/body/source/termination/delete/name replacement and prevent stale writers finalizing another token (after T045).
- [X] T047 [US3] Add authoritative `ReadTargetEvidence` dispatch and mutation-watermarked fence backfill/audit in `gitstore-api/internal/datastore/translation_targets.go` and `gitstore-api/internal/datastore/scylla/repair.go`; require stable fence/row/fence evidence, fail closed for absent/lagging fences and use serialized consistency rather than assuming generic QUORUM proves linearizability (after T044, T046).
- [X] T048 [US3] Implement authorized effective-candidate target lookup and pin enforcement in `gitstore-api/internal/cataloggrpc/translation_targets.go` and connect locale/reference/field checks in `gitstore-api/internal/cataloggrpc/manifest_validator.go`; perform decidable checks for absent targets, distinguish transport failure and never query stale stored content over candidate content (after T036, T041, T031, T047).

### Controller and current-status implementation

- [X] T049 [US3] Implement Translation `PagedListWatcher`, typed status/binding/dependent client operations and existing HTTP/WebSocket client reuse in `gitstore-controller-manager/internal/translation/graphql_client.go` and `gitstore-controller-manager/internal/listwatch/graphql_listwatcher.go`, preserving authored revision separately from lastAppliedRevision (after T024, T042, T043).
- [X] T050 [US3] Implement validation-only reconciliation in `gitstore-controller-manager/internal/translation/reconciler.go` importing T031/T041's pure `shared/translation` semantics, with authorized API target snapshots, existing retry/worker controls and matching-generation status CAS; wire the consumer's module/container dependencies and materialize canonical locale, descriptor/target/validated-path evidence and common conditions/revision, never translated target content (after T039, T048, T049).
- [X] T051 [US3] Add durable name/UID target-change fan-out for all five target watches in `gitstore-controller-manager/internal/translation/dependencies.go`, persisting pending IDs before journal cursor advancement, paging at 256, yielding under backpressure and ignoring material no-ops except explicit version pins (after T043, T050).
- [X] T052 [US3] Wire the reconciler and independent `Translation.disk-v2` state in `gitstore-controller-manager/cmd/controller/main.go` and `gitstore-controller-manager/internal/translation/checkpoint_test.go`; verify duplicate controllers, cold relist, quarantined/retry work and fan-out recovery without shared writable checkpoints (after T051).
- [X] T053 [US3] Implement the shared bounded read-time guard in `gitstore-api/internal/admin/graph/resolver/translation_service.go`, comparing Translation V, authorized target material evidence and unchanged V, with at most two attempts/ten metadata reads, page deduplication and bounded concurrency; no positive cross-request cache or target-content resolution in resolvers (after T038, T047, T050).
- [X] T054 [US3] Route typed/Node/list/generic status reads and status-mutation responses through T053 in `gitstore-api/internal/admin/graph/resolver/translation.resolvers.go`, `gitstore-api/internal/admin/graph/resolver/schema.resolvers.go` and `gitstore-api/internal/admin/graph/resolver/status_generic.go`; mask stale/forbidden/unavailable success through common conditions, redact target evidence and private validation fingerprints, and keep historical watch payloads explicit without a second status envelope (after T040, T053).
- [X] T055 [US3] Apply guarded live versus explicitly historical serialization in `gitstore-api/internal/admin/graph/resolver/watch.go` and common condition conversion in `gitstore-api/internal/admin/graph/resolver/converters.go`; retain matching event/resource versions and test #549 through controller-client decoding without introducing YAML enum aliases (after T054).
- [X] T056 [US3] Complete route/provider enforcement and controller permissions in `gitstore-api/internal/admin/graph/resolver/translation_authorization_test.go`, `gitstore-api/internal/auth/provider/rbaclocal/` and `gitstore-api/internal/auth/provider/opa/`, exercising actual configured provider integrations, denied human/service actors and controller-only status without granting target write (after T005, T048, T055).
- [X] T057 [US3] Run race-enabled locale/candidate/controller/guard tests and backend-neutral plus isolated tagged Scylla contracts in `gitstore-api/tests/contract/datastore/translation_contract_test.go` and `gitstore-controller-manager/internal/translation/`, verifying T037-T040 interleavings and binding/fence recovery before claiming current Ready (after T052, T055, T056).

**Checkpoint**: Status is observed validation, not application. Every public
current-Ready representation is guarded; missing, stale, replaced, forbidden
and unavailable are distinct safe outcomes. Persistent evidence alone is not
a freshness lease or distributed transaction.

## Phase 6: US4 - Correct invalid documents using useful diagnostics (P1)

**Goal**: Give bounded, consistent, existence-safe document/field diagnostics,
without coercion, partial acceptance or overload disguised as success.

**Independent test**: Submit every negative-matrix category through standalone
validation and ingestion, compare classifications/locations, and confirm no
partial source/status advancement or unauthorized content disclosure.

### Tests first

- [X] T058 [US4] Add a shared negative-matrix runner in `gitstore-api/internal/validate/translation_diagnostics_test.go` and `gitstore-api/internal/cataloggrpc/translation_diagnostics_test.go`, reusing `gitstore-api/internal/testutil/translation.go` and the shared corpus to assert stable reason/location parity for every spec matrix row, unknown/null/duplicate members, aliases/merges, authored system fields and mixed valid/invalid patches.
- [X] T059 [P] [US4] Add limit/cancellation/overload tests in `gitstore-api/internal/cataloggrpc/translation_limits_test.go` for 1-MiB blobs, 65,536 YAML nodes/depth 64, 256 patches, path ceilings, bounded target work, diagnostics 32/messages 256 bytes with truncation, saturated queues and request deadlines without partial publication.

### Implementation

- [X] T060 [US4] Implement deterministic safe diagnostic ordering/location/reasons in `gitstore-api/internal/validate/translation_diagnostics.go`, reusing `gitstore-api/internal/admission/` RejectedError and `gitstore-api/internal/admissionreport/` rather than another error envelope; never echo translated values or unauthorized target details (after T058).
- [X] T061 [US4] Enforce remaining source/YAML/diagnostic budgets before expensive allocation in `gitstore-api/internal/validate/translation.go` and bounded target/candidate work in `gitstore-api/internal/cataloggrpc/translation_targets.go`; reuse existing lower request/push-policy/worker/retry/deadline limits and distinguish unavailable from missing (after T059).
- [X] T062 [US4] Connect consistent whole-document rejection and admission-report output in `gitstore-api/internal/cataloggrpc/manifest_validator.go` and `gitstore-api/internal/cataloggrpc/server.go`, ensuring rejected revisions cannot advance accepted status and no typed/generic route discloses forbidden target existence (after T060, T061).
- [X] T063 [US4] Run standalone-versus-ingestion negative parity and schema fixture checks in `gitstore-api/internal/validate/translation_diagnostics_test.go` and `gitstore-api/internal/cataloggrpc/translation_diagnostics_test.go`, including invalid UTF-8 and unchanged non-Translation enum/source behavior (after T062).

**Checkpoint**: All required negative categories have consistent safe outcomes;
US1-US4 can now be assessed together behind the activation gate.

## Phase 7: US5 - Rely on tested support and accurate documentation (P2)

**Goal**: Prove connected ingestion, replicas, sustained bounds and recovery,
then document only the behavior actually delivered.

**Independent test**: Run all published examples/negative fixtures through real
Git ingestion against owned two-API/two-controller/one-Git deployments; join
accepted source, exact body/patch digests, observed/current status and unchanged
targets before/during/after declared faults within unchanged acceptance gates.

### Tests first

- [X] T064 [US5] Add connected ingestion tests in `tests/integration/translation_test.go` for all examples/five target kinds, field/body/combined channels, equivalent-locale independent documents, negative/deferred fixtures, same-candidate ordering and unchanged target authored/resolved digests; use real normal Git pushes through both APIs.
- [X] T065 [P] [US5] Extend raw-byte forwarding/parity tests in `gitstore-git-service/src/git/hooks/validation_handler.rs` for Translation fixtures, Go diagnostic forwarding and exact body/source bytes; add no Rust semantic parser, per-kind protobuf or production Git subprocess.
- [X] T066 [P] [US5] Add two-API/two-controller duplicate/out-of-order admission, status/binding conflicts and rolling-capability tests in `gitstore-api/internal/cataloggrpc/translation_replica_test.go`, `gitstore-api/internal/cataloggrpc/translation_rolling_upgrade_test.go` and `tests/integration/translation_recovery_test.go`, including post-activation refusal to roll back to incapable readers.
- [X] T067 [P] [US5] Add failing scheduler/oracle/evidence contracts in `tests/integration/translation_capacity_contract_test.go` for finite pending work, offered-versus-completed joins, source/config/topology attribution, target invariance, rate/full-window enforcement, physical versus simulated faults and rejection of health-only/helper-only evidence.

### Connected workload and operational implementation

- [X] T068 [US5] Extend `gitstore-api/internal/cataloggrpc/namespace_capacity_test.go` and `tests/capacity/preflight/namespace-admission.sh` with Translation valid/negative/near-limit corpora inside the unchanged 500-file request budget, preserving original Namespace cases, two helpers, 10 requests/s, 20 workers and the complete 30-minute validation gates (after T063, T067).
- [X] T069 [P] [US5] Implement a bounded real-push/read/watch Translation driver and domain oracle in `tests/integration/translation_capacity_test.go`, reusing existing owned stack/push helpers; generate at least 1,000 Translations/ten namespaces/two locales/five targets, both channels and one target with at least 1,000 dependents, with independent target/retained-baseline digest checks (after T064, T067).
- [X] T070 [US5] Extend `tests/capacity/profiles/api-readiness.js`, `tests/capacity/preflight/api-readiness.sh`, `tests/capacity/verifiers/api-readiness.sh` and `scripts/run-capacity-target.sh` with an explicit connected Translation domain mode and protected source-attributed receipts; preserve existing health/identity modes and existing selector families, and add dispatcher regressions in `scripts/test-capacity-dispatch.sh` (after T069).
- [X] T071 [P] [US5] Implement shared-SDK bounded-label validation/admission/guard/reconciliation/fan-out/repair/replay telemetry in `gitstore-api/internal/cataloggrpc/translation_metrics.go`, `gitstore-api/internal/admin/graph/resolver/translation_metrics.go` and `gitstore-controller-manager/internal/translation/metrics.go`; expose queue/oldest-pending/conflict/lookup/repair lag and readiness gates without names/locales/content in metric labels (after T057, T063).
- [x] T072 [US5] Implement owned restart/journal/callback-loss/post-acknowledgement fault coordination and restoration assertions in `tests/integration/translation_recovery_test.go`, reusing `tests/chaos/profiles/api-restart.json`, `tests/chaos/profiles/controller-restart.json` and `tests/chaos/profiles/journal-interruption.json`; require ownership, independent checkpoints, durable accepted-source replay and separately labeled physical/simulated receipts (after T066, T069).
- [X] T073 [US5] Extend `tests/capacity/verifiers/api-readiness.sh` and local `tests/integration/translation_capacity_contract_test.go` checks to reject source loss, partial bodies, false current Ready, stale/reused receipts, missing fault/restoration joins, undeclared gaps and modified baseline/targets; count near-limit and expected-rejection traffic separately (after T070, T071, T072).

### Documentation and acceptance

- [X] T074 [US5] Publish the delivered authoring guide and executable examples in `docs/storage-groups/translations.md`, and update `docs/products/push-validation.md`, `docs/api-reference.md`, `docs/storage-groups/git-backed.md` and `docs/README.md` to the same envelope/locale/path/target/status contract; state validation/resolution only, no localized application, and retain deferred examples as proposals (after T064-T066 pass).
- [X] T075 [P] [US5] Document descriptors, binding, bounded repair/guard and snapshot/custom-kind handoffs in `docs/development/translation-contract.md`, and activation/diagnosis/safe rollback in `docs/runbooks/translations.md`; follow documentation/runbook rules, publish no private experiment data and do not claim proposed ADR cores are implemented (after T057, T063).
- [X] T076 [US5] Document the implemented domain-mode selector, exact dataset/rates/windows/gates, evidence limits and ownership preflight in `tests/capacity/README.md`, `tests/chaos/README.md` and `scripts/README.md`; verify all guide examples and `specs/071-translation-contract/quickstart.md` through T064's actual ingestion path, including #40/#549 regressions (after T073-T075).
- [ ] T077 [US5] Run `make capacity TARGET=namespace PROFILE=validation MODE=alpha` with the implemented Translation extension and `make capacity TARGET=api PROFILE=readiness MODE=alpha` with the connected domain selector documented in `tests/capacity/README.md`; retain protected receipts meeting the complete 30-minute windows, 10 pushes/minute, at most 20 documents/push/two workers, 20 reads/s, read p95 <1s/p99 <3s, visibility/convergence p95 <=1s/p99 <=3s and all stricter existing gates (after T076).
- [ ] T078 [US5] Run the owned API/controller restart, journal interruption, callback-loss and post-acknowledgement scenarios from `tests/integration/translation_recovery_test.go` during sustained connected load via existing `make chaos` selectors; verify recovery <=30s with at least five seconds of correct domain traffic, replay without resubmission, retained UIDs/history/targets and no overlapping Git replacement, recording unexercised faults explicitly (after T077).
- [ ] T079 [US5] Run `make capacity TARGET=api PROFILE=readiness MODE=production` with the connected mode documented in `tests/capacity/README.md` against an exclusively owned retained baseline of at least 5,000,000 Products, at least a 60-minute Translation window, 50 pushes/minute, at least five bounded push workers and 100 reads/s; preserve all stricter profile/resource gates and baseline identities/history, and withhold production claims if unavailable or failing (after T078).
- [ ] T080 [US5] Rehearse additive migration, interrupted repair/fence backfill, pre-activation mixed versions, old-replica drain and capable-only post-activation rollback in `tests/integration/translation_recovery_test.go`; prove every enabled target writer is fenced and all read/replay roles are capable before enabling admission consistently, using T075's runbook and protected topology receipts (correctness rehearsal may proceed after T072/T075 without capacity qualification; full sustained acceptance still joins T078, and production claims additionally require T079).

**Checkpoint**: Connected correctness, security, replica/recovery and documentation
support boundaries are verified. Alpha/helper evidence never substitutes for
production acceptance. Failed or unavailable owned-deployment gates stay open.

## Phase 8: Polish and cross-cutting completion

- [X] T081 Run `make generate TARGET=all`, scoped formatter/lint/test commands and the full default `make pr-ready` before creating an implementation PR, including schema/action drift, Rust forwarding and Go race regressions; use `specs/071-translation-contract/contracts/capacity-and-rollout.md` to keep deployed evidence separate rather than substituting it for the default suite (after all story implementation).
- [X] T082 Refresh the AST-only graph with `graphify update .` after code changes and reconcile contributor navigation in `AGENTS.md` and `specs/071-translation-contract/plan.md` without modifying unrelated instructions or presenting generated planning as runtime support (after T081).
- [ ] T083 Reconcile final FR/PR/SC and #188-#191 coverage in `specs/071-translation-contract/tasks.md` and `specs/071-translation-contract/spec.md`; link only redacted result summaries, preserve private failed/success evidence, and close the implemented spec/issues only when their required gates pass. Keep ADRs 0007/0008/0013/0014/0016/0018 Proposed as specified by `docs/ADRs/README.md`; if scope actually implements a proposed ADR core, add and execute its explicit status/date/index task in that implementing PR instead of silently claiming acceptance (after T079-T082).

## Dependencies and execution order

### Phase and story graph

```text
Setup T001-T002
  |
Foundation T003-T012
  |
US1 source/storage/API core T013-T024
  |                            |
  |                            +--> US3 locale/reference/storage/fence track
  +--> US2 path/descriptor core T026-T032 ----> US3 candidate integration T048
                                                 |
                                    US3 controller/guard/auth T049-T057
                                                 |
                                US4 diagnostics T058-T063
                                                 |
                      US1 complete public acceptance + US5 T064-T080
                                                 |
                                  Final checks/roll-up T081-T083
```

T025 can verify the internal US1 source slice after T024; repeat its
public-ingestion assertions after T057/T063. Story independence means each
acceptance corpus has its own observable assertions, not that an unsafe subset
can ship without shared validation, authorization or freshness prerequisites.

| Work | Actual prerequisites |
| --- | --- |
| T001 / T002 | Independent setup work |
| T003-T005 | Setup; tests are disjoint and can be authored together |
| T006 / T007 | T003 / T004 plus T006 respectively |
| T008-T011 | Their explicit type/test prerequisites; T012 waits for all |
| US1 | Foundation; T017/T018 can run alongside T016 after their tests |
| US2 core | Foundation, fixture/model types; integrate after US1 parser/candidate seams exist |
| US3 locale and tests | Foundation; T035-T040 are independent test files |
| T042-T047 | US1 stores/index allocator and T037; T043->T044, T045->T046, both join at T047 |
| T048 | T036, T041, T031, T047 and US1 candidate dispatch |
| T049-T052 | US1 routes/CDC and binding/dependency APIs; T050 waits for candidate semantics |
| T053-T057 | T038/T040, authoritative fences, reconciler evidence and all route integration |
| US2 T033-T034 | T032 and supplied authorized snapshots; US3/US5 verify effective-candidate/connected lookup |
| US4 | US1 parser and US2/US3 diagnostics; tests may be authored earlier |
| US5 connected implementation | US1-US4; T064-T067 are disjoint test lanes |
| US5 harness branches | T068 and T069 after their contracts; T070 joins driver, T073 joins faults/metrics/wiring |
| US5 docs | T074/T075 independent files after specified correctness tests; T076 joins them |
| US5 acceptance | T080 correctness rehearsal may run after T072/T075 independently of capacity; full qualification retains T077->T078 and T079 for production |
| Final | All story implementation; T083 waits for production/rollout/default-suite evidence |

Within each lane, tests precede implementations. Edits to shared
`manifest_validator.go`, `schema.resolvers.go`, `status_generic.go`,
`watch.go`, datastore interfaces and `repair.go` must be sequenced, not blindly
parallelized. Generated output is regenerated after merging source contracts.

The current user-directed priority is correctness without premature
optimization. T076/T081/T082 are complete; rerun affected standard checks when
new correctness changes require them, not capacity work to postpone closure.
T077-T079 are deferred qualification, while T080's correctness work can
proceed independently. Keep unmet rollout and final-closure gates explicit.

The current scope does not wait for capacity qualification to begin T080's
correctness rehearsal. Full sustained acceptance retains T073->T076->T077->T078,
with T079 retained as the final production-capacity gate. Independent verifier
tests, SDK evidence support, documentation/example checks and rollout-test
preparation may overlap implementation, but their final completion still joins
the stated prerequisites. Owned acceptance/fault workloads on the same
deployment run sequentially; preparation and partial diagnostics never close
their acceptance tasks. Final source checks/navigation and requirement
reconciliation retain T081-T083's existing completion gates.

### Parallel execution examples

| Story | Independent work that may run together | Join / constraint |
| --- | --- | --- |
| US1 | T013 parser tests, T014 source/replay tests, T015 read tests; then T016 parser, T017 memdb, T018 migration tests | T019 before T020; T021 before source dispatch/read integration |
| US2 | T026 grammar tests, T027 descriptor tests, T028 related-resource tests; then T029 grammar and T030 descriptors | T031 joins grammar/descriptors; shared validator wiring is serialized |
| US3 | T035-T040 test files; T041 locale and T042 bindings after their prerequisites | Fence and index tracks join at T047; do not concurrently edit repair.go |
| US4 | T058 diagnostic parity tests and T059 budget/cancellation tests | Implement source-budget changes and common errors before T062 report wiring |
| US5 | T064 connected fixtures, T065 Rust forwarding, T066 replica tests, T067 oracle tests; T074 author docs and T075 operator/contributor docs | Harness/verifier integration and acceptance wait for all required branches |

These are execution examples, not authorization to spawn agents or run owned
deployments concurrently with another owner.

## Requirement and issue coverage

| Requirements / delivery | Tasks |
| --- | --- |
| #188; FR-001-FR-005, common metadata/status and #40 | T001-T016, T023-T024, T050, T058-T063 |
| #189; FR-006-FR-009 | T013, T016, T026-T034, T065 |
| #190; FR-010-FR-017 | T027, T030-T032, T035-T057 |
| FR-018; deterministic errors and limits | T005, T013, T026, T036, T056, T058-T063 |
| FR-019; accepted source/replay and target invariance | T014, T017-T025, T037, T042-T057, T064-T073 |
| #191; FR-020-FR-021 | T025, T033-T040, T057-T080 |
| FR-022-FR-023; SC-008; #492/ADR handoffs | T028, T030-T031, T036-T038, T042-T048, T074-T075, T083 |
| PR-001; SC-004-SC-005 | T002, T004, T014, T037-T040, T045-T057, T066, T072, T078, T080 |
| PR-002; no unauthorized disclosure | T005, T010, T036, T040, T048, T054-T056, T058-T064 |
| PR-003-PR-005; SC-006 | T007, T018-T021, T026, T037, T043-T047, T051-T053, T059-T061, T067-T079 |
| PR-006-PR-007; durable recovery/integrity | T014, T021-T022, T037-T038, T042-T057, T066-T073, T078-T080 |
| SC-001-SC-003; examples/negative matrix/reorder | T001, T013-T016, T025-T034, T035-T040, T058-T065, T076 |
| SC-007; consistent docs/issue completion | T074-T076, T081-T083 |
| #549; enum-boundary regressions | T003, T009, T012, T040, T049, T054-T055, T063, T076, T081 |

## Implementation strategy

1. Establish the reviewed contracts and disabled activation gate. Keep existing
   resources, providers and generated catalogs compatible.
2. Build the US1 internal MVP: exact authored source through typed storage,
   replay and read surfaces, tested without claiming current readiness or
   opening public admission prematurely.
3. Develop US2 paths/descriptors and US3 locale/storage/fences in their
   independent lanes. Join at effective-candidate validation, then finish the
   durable reconciler and shared current-status guard. Complete US4 safe
   diagnostics and run the US1-US4 acceptance corpora together.
4. Prove real Rust-to-Go ingestion, two-replica behavior, provider isolation,
   sustained bounds and physical recovery through US5. Reuse existing runners;
   preserve the expensive baseline and all private evidence.
5. Upgrade/migrate/audit/drain before controlled activation. Roll back only to
   capable readers after acknowledged source exists. Production claims require
   production evidence, not alpha/helper/default-unit passes.
6. Run the default PR suite, refresh the graph and close only genuinely
   implemented/verified scope. No proposed ADR is accepted by task generation.

**MVP scope**: Foundation plus US1 is a useful internal demonstration of source
preservation. The smallest releasable authoring increment requires US1-US4,
connected US5 correctness/security/recovery and the activation prerequisites.
The complete feature additionally retains the specified production gates.
