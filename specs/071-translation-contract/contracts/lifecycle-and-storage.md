# Translation Lifecycle, API and Storage Contract

This defines implementation obligations, not existing capabilities.

## Interfaces and Authority

| Boundary | Required extension |
| --- | --- |
| CatalogService validation/admission | Existing raw-file RPC, typed ParsedResource/comparable/admission dispatch, same-candidate target lookup; no new per-kind protobuf |
| Typed datastore | Translation named/UID lookup, namespace-scoped paged list, create/update/delete with existing expected-resourceVersion CAS; binding CAS, paged target dependents and per-target evidence fence |
| Admin GraphQL | Typed `Translation` Node, `translation(by: ...)` and cursor-paged `translations` following existing selector/connection conventions; common metadata, authored spec/body, source and guarded status |
| Generic resources/status | Register Translation kind/converters/listwatch and status routing explicitly; existing registration does not discover it automatically |
| Controller client | Existing Admin HTTP/WebSocket client and controller status mutation, no datastore access from controllers |
| Watch | Durable shared watch journal and source attribution; never process-local fan-out |
| Storefront | No localized fields, Translation mutation, fallback or publication reader added |

Translation fields are `id`, common metadata, `spec { locale targetRef patches }`,
`body`, source attribution and the guarded status envelope in data-model.md.
Reuse common reference/metadata/condition types; preserve patch order and exact
body string bytes. Raw blob remains recoverable through accepted Git source.
Named and Node reads must agree; list cursors are opaque and bound to namespace,
filter, schema version and stable ordering. First-page defaults to 100,
maximum 256; generic controller pages follow the same maximum.

Preserve #549's enum naming boundary. Canonical authored enum values use
UpperCamelCase; GraphQL enum values use SCREAMING_SNAKE_CASE through explicit
converters, not arbitrary case folding. Freshness checks are service behavior,
not additional authored or public status fields. Reuse common condition
converters for True/False/Unknown versus GraphQL TRUE/FALSE/UNKNOWN. This adds no Translation
authored enum or legacy spelling aliases; locale canonicalization follows its
separate BCP 47 contract. Never case-convert reference identifiers, field paths,
patch text or body bytes. Require schema/converter tests across typed and
generic status, Node/list/watch and controller-client boundaries so internal
spellings cannot leak into GraphQL or feed back into authored content.

Do not introduce a second authoring mutation merely to bypass Git. The
supported authoring route is the normal Git candidate/receive path through
either API. Status mutation remains system-only and returns guarded current
status, not the unqualified persisted evidence. A deferred target is a typed
unsupported outcome, not a new dynamically registered API.

## Security

Add the generated `translation` resource action family through the existing
canonical action source/generator: use its existing read/list/watch/write
conventions and `translation.status.write`, never a new `updateStatus`
vocabulary. API policy tests must enumerate every actual generated route/action.
Controller credentials grant only the target reads and status scope required.

Before any target lookup returning details, authorize the corresponding target
kind's read action in the effective namespace/name context. Candidate authoring
also requires owning repository/namespace permissions; Translation write is
not target write. Missing permitted targets may be accepted only after the
same existence-independent authorization decision, without probing another
tenant. Provider failure is unavailable, not allow.

Reading Translation source does not automatically grant target read. Public
status must redact resolved target UID/revisions/paths and return opaque
not-current status when that read is forbidden. The authored targetRef is
already Translation source, not a license to enrich it. Test all pluggable
provider integrations applicable to catalog reads, denied actors, controller
authority, stale pins and cross-namespace paths.

The shared manifest preflight now authorizes all source operations before
validating changed Translations against the complete effective candidate.
Proposed creates/updates/deletes, renames and unchanged candidate targets
override stored fields independently of file order. Thin serialized Namespace
and target facts supply target-read authorization before any target content
fetch. Stored content is fetched by proved UID, checked against its exact
material digest and rechecked for ownership, material and Namespace lifetime
changes. Status-only churn is permitted without a version pin.
Only proposed updates or unchanged candidate entries retain a proved existing
UID; a proposed create cannot borrow it. Future versions remain unallocated.
An unchanged candidate's version is usable only if its exact material matches
the current evidence. Authoritative absence requires current Namespace-bound
coverage and unchanged serialized missing/vacant head observations.
Missing permitted targets and unallocated pins remain unresolved; unavailable
proof and storage/provider errors stay operational failures. Declared catalog
JSON/YAML field tags map keys without case-folding values or identities, while
retaining null and absent leaves. Ambiguous aliases and excessive
byte/node/depth work fail closed; source/body and target content are unchanged.
Domain failures use the common bounded diagnostic envelope. Unchanged
Translations are left to reconciliation when targets change, rather than
retroactively rejecting unrelated target writes. T048's component and dormant
shared preflight integration are complete, not connected/deployed acceptance
or post-commit report integration. Startup still forbids activation.

## Observed Evidence and Current Freshness

The validation-only reconciler's controller-only snapshot now distinguishes
current certified absence from unavailable proof. It serially checks Namespace
name/UID/owner/lifecycle facts, uses the same scoped target-read authorization
for positive and negative outcomes, and rechecks source/binding/Namespace
versions before returning. Negative snapshots contain no target content or
historical target evidence. Positive snapshots verify exact material and
preserve null/absence; status-only churn is harmless without a version pin.
First observed eligible target UID binding survives field-invalid observations,
so replacement cannot silently cure an invalid Translation. Operational or
work-budget failures remain retryable without publishing partial validation.
The existing source UID/version status CAS carries generation and source
revision together. T050's component is complete, including standalone module/
container dependencies. All-five durable watch handoffs now use fresh,
controller-only authorized thin proof, persisted coalesced sweeps and lock-free
cold-relist publication barriers. Matching private bindings carry Namespace/raw
sticky UID identity; conservative namespace/name aliases retain invalidation
when proof is unavailable. Durable hint records never replace read-time proof.
The gated runtime factory reuses Product/CategoryTaxonomy runners and adds
metadata-only observers without duplicate resource work. Production startup
still forbids activation. Component recovery tests cover independent replica
directories, retained retries/quarantine and changed-source cold-relist
recovery; deployed process/physical-fault acceptance and public routes remain
separate work.

The persisted observation contains exactly the Translation generation/digest
and target UID/authored-generation/content-digest/descriptor-digest validated
by the controller. Preserve the common `conditions`, `observedGeneration`,
`lastAppliedRevision` and kind-specific `resolved` status fields from #40.
Write `lastAppliedRevision` as the Translation source revision successfully
reconciled into the observation, atomically with matching observed generation
and evidence under the existing status CAS. A successfully persisted unresolved
or invalid target outcome can advance that marker; a failed write or stale
worker cannot. It is neither the target revision nor proof of target application,
and must not substitute for accepted source attribution or the freshness guard.
Reuse this field in typed/generic status, Node/list/watch serializers and
controller clients; do not add `observedRevision` as a parallel replacement.

Status-only target changes do not change authored material
evidence; an explicit target resourceVersion pin still matches the full opaque
version and can be invalidated by such changes.

**Mandatory service guard**, shared by named/Node/list/generic-status reads,
status mutation responses and live watch delivery:

1. Read the authoritative Translation and its evidence at resourceVersion V.
   Reject evidence for another generation, content digest or descriptor.
2. Authorize the target read, then fetch only authoritative current target
   identity/material metadata. Require the exact recorded UID, non-terminating
   lifecycle, generation/content digest and any explicit resourceVersion pin.
3. Re-read the authoritative Translation version and require V unchanged.
   If it changed, restart, for at most two attempts in the request deadline.
4. Only a matching observation may be represented as current Ready=True.
   A known mismatch masks Ready to False/StaleEvidence;
   failed/forbidden lookup or exhausted budget masks it to Unknown with a safe
   Unavailable/Forbidden reason. Schedule durable reconciliation as appropriate.

This is a bounded **observational freshness** contract: a successful response
has a matching validation snapshot at the target metadata read within its
operation interval. It is not a lease, a cross-resource CAS, or a claim that the
target cannot change immediately afterward. Observed versions make that boundary
explicit; condition transition times do not establish freshness. Non-reused
monotonic/CAS resource versions prevent an ABA Translation update from passing
step 3.

The target metadata operation must check the authoritative current incarnation,
not an eventually repaired name/UID/list projection or a cached old object.
Implement a typed internal `ReadTargetEvidence` service operation for the five
target kinds with a resource-sized durable fence keyed by namespace UID,
target GVK and name:

- The fence stores a non-reused CAS token, STABLE/UPDATING phase, current UID,
  material generation/digest and a bounded recoverable write intent.
- Every create, material source/body change, termination, deletion and name
  replacement first CAS-marks UPDATING, then commits the existing authoritative
  resource CAS, then marks STABLE with the resulting evidence. Writers serialize
  only on that target identity. UID tombstones and non-reused tokens prevent ABA.
  Status-only changes need not invalidate material evidence; the read still
  checks the authoritative row's resourceVersion for explicit pins.
- Read the STABLE fence, read that UID's authoritative metadata, then re-read
  the fence and require its token/phase/evidence unchanged and the row matching.
  Missing, UPDATING, lagging or contradictory evidence yields unavailable,
  never a positive result from a repaired lookup index.
- An interrupted write resumes from its durable intended source/CAS outcome.
  A stale writer cannot finalize another token. Replay/recovery resolves
  UPDATING; it is not cleared by a timeout that guesses the write did not happen.
  Keep the intent small by referencing durable candidate/source attribution,
  not copying a target blob into the fence.

The shared read-protocol component now bounds this sequence to three callbacks
without retries: stable fence, authoritative source metadata, unchanged fence.
It validates private source namespace UID/token/digest and immutable creation
time, requires explicitly proved active lifecycle and allows a later source
resourceVersion only when the material evidence is identical. Missing proof
is repair-required, not authoritative absence. Returned evidence and frozen
fence snapshots are detached from callback-owned values. Memdb and native
metadata adapters dispatch all five kinds to authoritative rows, not name/UID
projections. Native metadata uses a single SERIAL point query without spec/body;
instrumentation forwards the same raw metadata operation. Both backends now
compose the protocol in `ReadTargetEvidence`, preserved by instrumentation.
Raw/decorated five-kind contracts cover missing/pending/deleted proof, replacement,
status versions, cancellation and namespace-incarnation isolation. This internal
operation does not certify deployment capability: mutation-watermarked backfill
and the public read guard remain unimplemented, and admission stays disabled.

The internal write-fence metadata store bounds the complete serialized intent
at 16 KiB, retains the exact authoritative creation timestamp and hashes the
physical name key while checking the full logical identity. Times are frozen
at the native timestamp's UTC millisecond precision. A same-ID material retry
may present a later resourceVersion expectation after status-only activity;
it retains the original frozen intent and write token. Completion permits a
strictly later resourceVersion rather than requiring exactly one increment.
This does not waive the actual resource writer's own expected-version CAS.
Older expectations cannot reserve a new token against accepted evidence.
These metadata operations are not sufficient for a positive target read:
every-writer integration, authoritative row proof, UID retirement and
watermarked backfill remain separate prerequisites.

Memdb canonical creates and advancing-version material updates now atomically
commit private source namespace UID/token/digest and stable name evidence with
the actual source row. Ordinary DTO clones and JSON/YAML output omit private
proof. Status-only updates preserve the material token; a different pending
owner blocks material publication without changing source. Projection failure
rolls back both source and head. Final deletion vacates the name and permanently
retires the UID in that transaction. Existing name movements vacate the old
name but do not yet establish positive evidence at the new name. Legacy
envelopes and non-advancing File material updates preserve CRUD behavior while
invalidating positive source proof; deletion and name replacement still work.
These cases need supported upgrade/backfill semantics before activation.
The shared material fingerprint frames exact GVK/raw-spec/body components,
checks cancellation between 32 KiB chunks and bounds combined spec/body bytes
at 100 MiB. It neither validates nor canonicalizes target content.
Its string-backed spec entry point streams native stored strings without an
additional whole-spec byte-slice allocation.

Native target source inserts and material updates now persist a bounded private
namespace/digest record in the actual source CAS. An update also conditions on
the exact previous nullable record, preventing a paused writer from replacing
an intervening private ownership marker with an unchanged public version.
Unproved writes carry a fresh private mutation ID, preventing equal-version
content ABA from restoring an older paused writer's exact CAS preimage.
Ordinary mutations retain existing namespace ownership and never fill missing
legacy ownership from the current namespace. Corrupt or oversized records fail
repair-required before source publication. Creation without a Namespace row
retains legacy CRUD behavior without inventing namespace ownership. Creates
freeze that ownership before any projection writes and compare the incarnation
again before source publication. A Namespace created during a paused legacy
create cannot supply the source's birth authority.

For already-proved native sources, all five normal material-update paths now
reserve UPDATING, commit the originating token and digest in the conditional
source write, and complete matching stable evidence. Invalid/unsupported
transitions lose proof instead of inheriting it. A committed source whose
fence publication fails returns repair-required with its committed version.
Bounded update recovery uses a frozen namespace/key/previous nullable source
identity, never a body read or a lookup projection. It either publishes a
matching committed token or first CAS-installs a durable canceled-intent
marker before restoring old material under the new token. The marker
distinguishes cancellation from a later status-only version advance with
identical content. A timeout or unchanged row is not cancellation proof.
Replacement-store tests cover both outcomes and a publication interruption;
all five actual update paths cover committed-before-publication interruption.
The legacy-source tests seed proved initial source/head state explicitly; they
do not establish backfill correctness. File's status-only CAS preserves private proof
and does not rewrite, hash or fence its unchanged body.

Canonical native births now reserve UPDATING, commit the originating token in
the authoritative insert and publish stable evidence. Unsupported or incomplete
legacy births remain unproved. Bounded creation recovery completes a matching
committed source, or CAS-inserts an exact-key negative source marker before
permanent UID retirement and stable absence. This fences a paused source INSERT;
neither an absent-row read nor elapsed time is negative outcome proof. A late
replay cannot complete the canceled head or make its permanently retired UID
visible through ordinary point reads. Creation rollback uses the same retained
retirement/name-vacancy protocol and checks the exact original private identity.
Unproved rollback also publishes permanent UID retirement before source
compaction. Independent-store replay cannot reuse that UID; a new UID may
reuse its released name.
Committed-before-fence publication can recover independently of missing lookup
hints; ordinary projection repair restores those hints from source authority.
Canonical five-kind tests exercise birth, material update, final deletion and
replacement name ownership. Native Product and category foreground termination
also advance the material fence.

Proved native deletion reserves an intent before retiring source, guards both
resourceVersion and private identity, then publishes permanent UID retirement
and stable absence before compaction. Recovery cancels a paused live deletion
with a source CAS or completes its persisted retirement. Generic retirement
repair settles the owned head before compacting source proof. These are isolated
component/interruption tests, not supported legacy upgrade,
mutation-watermarked backfill or deployed physical-fault acceptance.
A positive target-read capability remains absent.

Non-advancing or unsupported mutations of a proved native source reserve an
explicit invalidating intent. The actual source CAS carries its token and an
invalidated marker before completion revokes positive evidence. Recovery uses
that marker rather than requiring a resourceVersion advance or confusing the
outcome with cancellation. The live UID is not retired. Subsequent unproved
legacy mutations retain CRUD without reacquiring proof; deletion and fresh-UID
name reuse still work. An evidence-less stable head alone must be treated as
unavailable evidence, never as an authoritative claim of target absence.

Native pending-target discovery now uses the existing segmented allocator and
UID-ordered pager with private pending and namespace layouts. Before the head
CAS, a retained resource-sized locator freezes the full pending fence, intent
and expected old token, then publishes namespace and intent discovery cells.
The intent ID and actual random fence token are distinct; pre-head recovery
reuses the frozen token rather than minting another. Namespace discovery is
retained independently of Namespace existence. A different head token excludes
the old expected-token CAS forever, including after another writer cancels.
Completion first closes the locator, then retires and compacts its derived
private cell. Unlike reusable Translation placements, a unique pending ID has
its own permanent closed authority. An exact delayed cell replay only triggers
cleanup of that closed record and never changes the target head.
API lifecycle workers execute separate durable discovery/recovery phases,
at most 16 head/segment probes and 256 physical cells per turn. Persisted
per-namespace cursors bound in-memory state; at most four consecutive recovery
turns precede yielding to other namespaces. Incomplete work does not advance
its namespace cursor, while errors permit other namespaces to receive turns.
Workers use deadlines, retry safely across replicas and stop before datastore
closure. Isolated tests cover deleted Namespace discovery, pre-head/source and
post-source interruptions, paused original Begin, competing workers, saved
cursors, closed-cell replay, updates, invalidation, deletion and foreign
replacement ownership. API lifecycle start/retry/shutdown and instrumentation
forwarding also pass. This does not establish deployed latency or fault gates,
legacy ownership proof, backfill watermarks or a positive target reader.

The UID-retirement component retains exact kind/UID claims. Native deletion
freezes the authoritative key in a pending claim, records a private retirement
marker in the source expected-version CAS, publishes permanent UID retirement,
then physically compacts the source. A failed CAS does not retire the live
resource; pending claims remain retained and forbid UID reallocation.
The same UUID bytes in another kind are independent. Native point reads and
projection repair consult SERIAL source/UID proof rather than reviving a delayed
row. Material/status/termination updates and creation compensation cannot
clear source retirement. The shared CDC decoder publishes deletion at the
source transition and suppresses physical compaction.
Cleanup of name/UID reservations requires freshly proven retirement; an
ordinary absent authoritative row remains insufficient. Audit/repair may recover
interrupted publication from the exact source retirement marker, but must
publish permanent UID retirement before deleting that proof. Supported legacy
upgrade, complete writer verification and mutation-watermarked audit remain
required before activation. Native raw pages
fail closed on a retained retired row rather than silently truncating keyset
paging. All five target kinds use the existing 250-item ceiling in each
direction. Audit/repair may remove an exact retired authoritative key only
after a fresh SERIAL retirement read; it retains the retirement fact itself
and rechecks authority when applying saved plans.

Use the backend's serialized CAS/LWT consistency path for fence and resource
version checks; a generic QUORUM read alone is not a linearizability proof.
Integrate every existing writer of the five enabled target kinds, not only
Translation code. Backfill/audit initial stable fences under a mutation
watermark before activation. A missing fence fails closed. This is a new,
explicit implementation prerequisite, not a capability claimed of existing
target stores. It is not a lock spanning a target and its Translations:
dependent evidence may remain historical, and the public guard detects it.

The bounded target-source discovery component now scans all five authoritative
layouts independently of name/UID lookups and name fences, using metadata-only
native server pages and namespace/backend/layout-scoped cursors. Foreign records
count against each page budget. Raw/decorated memdb and native recovery recheck
source lifetime/version before the existing evidence protocol. Native pending
write recovery returns Conflict so the observation must be reloaded. Missing
legacy birth proof is retained, never inferred from today's Namespace name.
This is not mutation-watermarked namespace completeness or a deployment
capability.

Native source operations now use a separate namespace-name-scoped single-CAS
clock (`translation_target_mutations_by_namespace` in the consolidated additive
migration), including repository-less and unowned legacy CRUD. Arbitration by
name also covers a source operation that began before Namespace birth or
recreation. This is not birth authority: the frozen source keeps its original
namespace UID or absence, while idle epoch observations bind the clock to the
live namespace UID. A fresh UUID epoch grants exactly
one frozen conditional source write. The owner is bounded to 4 KiB and carries
the exact source key and prior/next private identities. Recovery first durably
revokes it, conditionally cancels the frozen source operation or recognizes
committed material, then releases only that owner. Waiting can initiate this
protocol; elapsed time alone cannot release authority. Compensation after a
completed birth obtains a new source epoch. Memdb commits its source epoch and
resource changes in one transaction, including known-birth incomplete envelopes.
Raw and decorated backends expose the same idle mutation observation; native
pending or malformed grants never yield an accepted epoch. Bounded page recovery
compares this observation before discovery and after its source rechecks.
Unchanged page members do not hide another source's birth or retirement:
changed/unavailable epochs return no accepted continuation.

Known-birth source backfill now initializes proof for canonical sources of all
five kinds, including those restored after invalidation. It never adopts a
source without original namespace birth authority. Shared raw/decorated
contracts compare complete public envelopes and idempotent subsequent recovery.
Memdb constructs the initial source/head proof in one atomic transaction.
Native backfill enrolls the retained exact-key UID locator and publishes an
explicit `Backfilling` intent with the existing UID as both previous and next
UID, then conditionally updates only private source identity under the name
clock. Unlike provisional creation, completion preserves the public source
version and cancellation restores an unproved head without retiring the source.
Replacement-store audit recovery fences paused operations or settles matching
committed tokens. All-five-kind regressions cover intervening material changes,
delete/recreate under a fresh UID, exact body preservation and continued
backfill after cancellation. Missing birth authority still fails closed.

An independent server-owned target audit now traverses Repairing, Verifying and
Verified with bounded physical pages and full-preimage progress CAS. Memdb and
native backends, including the decorator, persist the same namespace-bound
state. The verification pass spans all five layouts under one coverage epoch
and descriptor digest. An invalidating epoch change restarts repair; failed pages
never advance their cursor. Native replica races cannot overwrite a winning
page, and an uncertain post-CAS response requires reloading stored progress.
Coverage readers recheck the live coverage epoch, rejecting even an unproved
mutation between the last verification read and the progress CAS. A separate
ordinary mutation epoch continues to observe every source CAS. Coverage survives
proved material changes, canonical fenced births and retirement, including the
first fenced birth after empty-namespace verification without initializing a
clock on read. Per-source coverage checks require original birth authority and a
stable or recoverable pending head, not an unchanged material version. They do
not cancel proved writers or establish positive material or target absence.
Native coverage reads may accept a held proved grant after checking its frozen
identities and live Namespace lifetime; unproved or malformed grants fail closed.
Backfill cancellation invalidates coverage because its prior source was unproved.
Confirmed native repair fences a blocking unproved source grant and returns
Conflict without publishing progress; subsequent turns reload and resume.
Classification and revocation compare the same frozen owner, preventing an
intervening proved grant from inheriting cancellation. Read-only inspection
never performs this recovery.
All-five-kind raw/decorated contracts mutate material inside every discovery
page; native independent-store checks complete verification while a proved source
grant remains held and retain unavailable positive reads for pending heads.
These are deterministic sustained-writer interleavings, not deployed load gates.
Permanently retired UID rows, including negative cancellation markers
and late physical replay, count against scan budgets but not active coverage.
Retirement markers lacking permanent UID proof block the page.

The existing projection maintenance commands select this separate traversal
with `--kind Translation --audit-scope targets`; default `indexes` retains the
source/index traversal. Read-only inspection neither backfills nor initializes
progress or an absent clock. Confirmed repair performs one current turn.
Verified covers participating target writers only, not old replicas,
Translation reverse-index completeness or deployment activation. Deployed
writer-role certification, old-replica drain and sustained-load acceptance remain
separate activation prerequisites.
These mechanisms are also separate
from whole-callback lifecycle ownership: `catalogMutation` still bypasses that
reservation for an empty repository ID, and source recovery never clears its
`pending` slot. Those are not interchangeable mutation or completeness proofs.

At most one direct `ReadTargetEvidence` operation per Translation/attempt;
its normal path is three bounded metadata reads. Together with the two
Translation reads, the guard uses at most ten metadata reads over two attempts,
before page-level deduplication. Batch
identical target identities within a list page. All initial source rows precede
the page's target observations; a source-version retry must bypass the shared
observation because its new source can postdate that proof. Use a request-local
eight-worker pool, following the API's existing bounded hydration pattern;
never one unbounded goroutine per list item. No positive
cross-request freshness cache, target-content fetch, field revalidation,
reference traversal or related-resource aggregate occurs in a resolver.
The service guard compares precomputed metadata; controllers own all computed
path/dependency summaries.

The private service core now shares the binding boundary's single authorized
fence/metadata/fence observation. It compares source UID, namespace incarnation
and immutable creation time as well as resourceVersion on the final read.
A changed version retries once; a changed lifetime or exhausted retry budget
cannot advertise current success. Retry results pair the returned source with
its own observation. Masking is detached from stored status, and target denial
also masks stored negative observations. Deterministic tests exercise actual
status-CAS/target mutations and stale name/UID lookup responses. Page guards
now deduplicate initial observations, isolate denied/unavailable targets and
stop their bounded workers on cancellation. Retry proofs are never served
from that page's earlier observation.

Masked stale/forbidden/unavailable reads request a bounded durable revalidation
hint. The backend fences the exact source lifetime/version and coalesces one
request per version without changing public metadata, authored bytes, stored
status or binding history. Scylla stores only a private source-row version
marker; full-image CDC carries the unchanged source through the existing
shared journal. Memdb preserves the same committed event contract. This is
one conditional metadata write, not additional target metadata/content reads
or a process-local queue. A superseded source needs no old-version hint;
other persistence failures fail the read closed.

The Translation disk runner alone opts into atomic same-version hint
requeue/cursor commits, so generic version deduplication cannot swallow this
work. Request replay, independent/replacement APIs, real native CDC delivery
and controller checkpoint replacement have component coverage. Named, Node,
list and typed/generic status responses now use the same guard. Source
authorization precedes target work; target denial, stale material and unavailable
proof mask success and redact other conditions that may disclose target details.
Stored observations remain unchanged.

Journal payloads record historical observed state and versions. Generic
resource/watch serializers must not expose a stored historical Ready=True as
an unqualified current assertion. On live delivery, guard the current resource
or label the payload explicitly historical and omit current Ready. Never pair
a new resource payload with an old event version. Clients relist on expiration
or a recovery gap; the absence of another watch event does not turn an earlier
Ready observation into a freshness lease. Keep the four-field common status
shape and label historical evidence on the watch event, not in a second status
envelope. Validation fingerprints remain private on every serialization route.
The subscriber exposes its actual registration-time replay high-water boundary,
not a second guessed journal bounds read. Initial replay is always historical.
A matching live source uses guarded conversion; a source-version/lifetime change
before or during that guard falls back to the original historical event and
omits status. Component tests cover typed/generic parity, post-CAS target edits,
denial, unavailable proof, source retries and replay on an independent resolver.
The actual controller WebSocket client accepts uppercase GraphQL condition enums
and optional historical status while rejecting YAML aliases. Production
activation remains disabled pending the remaining acceptance gates.

**Required deterministic interleavings**: target changes after controller
read/before status CAS; target changes after CAS/before public read; Translation
changes between guard reads; deletion/name reuse with a lagging derived index;
status-only target update; stale explicit resourceVersion; lookup I/O error;
unavailable authoritative identity fence; historical watch replay. All must
avoid false current Ready and must converge after durable revalidation.

## Binding, Reconciliation and Propagation

First resolution CAS-binds the target UID to the Translation reference epoch
before a successful observation is written. Binding cannot be cleared by a
status patch or process restart. A same-name new UID requires an authored pin
to that UID; ordinary locale/patch edits and toggling a pin do not retarget.
Source and binding writes use version checks so an old worker cannot bind a
new reference epoch. Keep required binding history until the Translation is
finally deleted; this state is authoritative, not reconstructed from targets.

The controller transport uses `translationBinding`, `bindTranslationTarget`
and `scanTranslationDependents` over the existing authenticated Admin connection.
The binding mutation accepts `BindTranslationTargetInput!` and returns
`BindTranslationTargetPayload!` with a non-null `binding: TranslationBinding!`
field, following the shared mutation input/payload convention.
All require the live enrolled controller plus scoped source permissions.
Binding disclosure/mutation also requires target-read authorization from the
stable fence/metadata/fence ownership observation. Unbound state reveals no
target evidence. First binding compares source UID/public version, binding
version and reference epoch, verifies the exact target UID/version observation,
and preserves history on the server. It never writes target content or public
source/status fields. Missing or contradictory ownership proof fails closed.
Dependent discovery instead requires scoped source-list/status authority.
`scanTranslationDependents(target:, limit:, continuation:)` returns
`TranslationDependentScan { items, continuation }`, deliberately distinguishing
a bounded controller scan from Relay resource pagination. The default and
maximum row budget is 256, with a minimum of 1; retired cells consume that
budget and index probes are independently bounded. Empty items with a non-null
continuation are valid progress, not exhaustion. Consumers resume with the
continuation for the same target index and Namespace lifetime until it is null.
These are scan positions, not edge cursors or snapshot guarantees. The
`translations` resource list remains a Relay connection. Discovery does not
read or claim target existence, allowing deletion invalidation to continue.
Consumers reload each source; hints are not source or target authority.
Authored revisions and source CAS versions remain separate from reconciled
revisions and journal cursors. Condition decoding accepts only GraphQL enum
spellings.

The private validation-only reconciler expects an authorized API snapshot tied
to the requested source global ID and version. The snapshot carries the exact
accepted source digest, namespace UID, matching binding observation and the
target's proved material evidence plus normalized spec fields. Its private
stored observation includes validation fingerprints, uses the common GraphQL
condition enum conversion and is fenced by that same source version. It is not
a currentness claim and is never substituted for fresh target validation.
The controller uses it for transition times and complete no-op comparisons,
not the absent/masked public status. Missing or changed private fingerprints
must be repaired even if public resolved fields match. Target fields
omit an outer `spec` envelope. A nil target is an authorized missing result;
unavailable or unproved reads must return an operational error instead.
The enrolled controller-only `translationValidationSnapshot` operation and
controller client implement the positive-target adapter for all five kinds.
Source UID/version, namespace incarnation and binding version are checked
around the observation. Target authorization precedes the full content read;
the exact raw spec/body digest, identity, generation, revision and version must
match serialized thin evidence before and after normalization. Admission and
controller snapshots share the same bounded authored-name normalizer, preserving
absent/null leaves without typed DTO coercion. No target content is mutated.
This explicit controller operation is separate from ordinary Translation reads
and their metadata-only guard. Unproved/missing target fences remain operational
errors; authoritative absence handling and runtime registration are still
incomplete. Component contracts use real GraphQL client calls and independent
disk-v2 managers to retain conflict retries beyond attempt limits and across
replacement, settle with refreshed masked public status without duplicate
writes, and resume pending invalidation pages. Target-watch projections never
establish snapshot authority.

Typed status CAS requires the public source UID alongside version, generation
and revision. The common status input enforces the same UID precondition for
Translation; other kinds retain their existing contract. The controller sends
the existing `StatusPatch.UID`. After scoped authorization, the server compares
that UID with the authoritative named source before its UID-addressed CAS.
Deletion/name reuse cannot accept a delayed observation even when versions,
generation and revision are identical across the two source lifetimes.

Use `PagedListWatcher`, generic reconciler/status client, error-returning
lookups and independent per-replica `Translation.disk-v2`. Controller retries,
pending IDs, quarantine and fan-out cursors are durable. Duplicate controllers
may compute the same observation; only matching-generation CAS succeeds.
Conflicts requeue under existing retry/deadline controls.

Maintain name-based target edges for unresolved wakeup and UID edges for bound
invalidation. Subscribe to all five target kinds, ignore status-only material
no-ops except pinned versions, and resume target changes through journal
tokens. Page dependents at <=256 rows, persist pending IDs before advancing the
cursor, yield after each page and retain the cursor when queues are full.
Do not synchronously drain an entire target's dependents in its write path.

The internal controller handoff reuses disk-v2 relation indexes and durable
`RequestFanout`/`ProcessFanout` requests, not another queue. Index identities
include namespace UID and exact GVK, with separate name and bound-UID groups.
Version-pinned dependents also join pin-only groups: a proved material no-op
with a new resourceVersion wakes only those groups. Unknown material identity
must invalidate conservatively. Partial request persistence is replayable;
fan-out pages and their enqueued work commit atomically, at 256 items per turn.
An optional disk-runner pre-commit callback propagates handoff failures before
changing the target projection or its journal cursor, including hints with
reused source versions. Projection deduplication must not hide those hints.
This component is tested, but target-watch registration, bootstrap/cursor-loss
integration, API dependent
clients and full reconciliation remain required.

Cold start/cursor loss uses bounded authoritative Translation pages to rebuild
indexes/checkpoints and revalidate evidence. Do not trust an incomplete
derived-index rebuild for positive readiness. Read-guard correctness does not
depend on fan-out speed; readiness convergence and recovery deadlines still do.

## Query-First Storage and Bounds

Append an incremental migration after the actual current lexical maximum;
never edit applied checksums or reset/reseed a keyspace.
During development, keep all Translation schema changes in the single
unreleased `015_translation.cql`, as explicitly approved for this feature.
Migrations `001`–`014` remain unchanged. Disposable development databases that
recorded the superseded four-file bundle must be recreated explicitly; this is
not an upgrade path for that unreleased bundle.

| Table family / key | Responsibility |
| --- | --- |
| `translations_by_key` / complete per-resource namespace/creation/UID key | Single authoritative full envelope and co-located binding snapshot, coordinated source/status/binding CAS; full-image CDC |
| `translations_by_name` / namespace UID + name | Resource-sized name reservation and current UID/version lookup |
| `translations_by_uid` / immutable Translation UID | Retained physical-key locator and retired-identity fence |
| `translations_by_namespace` / namespace UID + shard + segment, ordered identity | Thin immutable discovery anchor and list projection, never current source-version evidence |
| `translation_bindings_by_uid` / Translation UID | Reserved by the initial additive schema; not a second authority or a fallback for missing co-located binding history |
| `translation_dependents_by_target_name` / namespace UID + target GVK/name + shard | Thin unresolved/resolved wakeup edges |
| `translation_dependents_by_target_uid` / namespace UID + target UID + shard | Thin incarnation invalidation edges |
| `translation_target_fences_by_name` / namespace UID + target GVK/name | Resource-sized serialized current-target evidence and recoverable material-write intent; not a dependent aggregate |

Reuse Product's resource-sized authoritative keys and thin sharded projection
mechanism rather than placing all full Translation blobs in one namespace
partition. No secondary indexes, materialized views or unbounded global list.
Derive shard from Translation UID; cursors carry shard and last identity.

The namespace anchor is immutable for a source's lifetime: its creation
resourceVersion and first reference epoch remain `1` and the initial epoch,
even when source/status/binding updates replace the reverse edges. Loading an
anchor always reads the current authoritative source at its complete physical
key. Never use the anchor's old source version as current validation evidence.
Canonical namespace connection order is shard ascending, segment ascending,
then creation timestamp descending and UID descending. A backward page scans
in the reverse direction but returns that same canonical connection order.
Layout-v2 opaque cursors bind the namespace UID, traversal direction, exact
shard/segment, saved head bound and tuple position; each returned item also has
a cursor suitable for direction reversal. Removed cursor rows do not invalidate
their tuple positions. Empty advancing pages are normal bounded work turns,
not proof that no later item exists. Pending sources fail closed rather than
being silently omitted from a controller cold relist.

Derived namespace range hints use QUORUM; authoritative source/identity,
allocation and retirement-fence point reads remain SERIAL. An index hint never
establishes current identity or positive freshness on its own.

Initial explicit index limits: 64 namespace-list shards and 64 reverse edge
shards per target key. Include a segment number in each physical partition key;
seal/roll over a segment before 16,384 thin rows or 64 MiB, whichever comes
first. A resource-sized shard head CAS-allocates segment/slot capacity and the
maintenance intent retains each edge's allocated location for idempotent retry
and removal. Crash-created holes are safe; reservations are recovered, never
reused speculatively while an old writer can still commit.

An allocation attempt has a stable retry token retained by its durable source
intent. Persist its Preparing record, including the expected head version and
proposed segment/slot, before CASing the head. The head retains that token while
the outcome is uncertain; no later allocator may skip it. A matching serialized
head read proves allocation ownership after interruption. Durably acknowledge
the reservation as Reserved before CAS-clearing the pending head token.
If another token advanced the expected head first, CAS-replan the still-Preparing
record; never change an already Reserved location. Record and head versions
advance independently. Removal does not refund reserved capacity.

Publishing a reservation uses its persisted immutable placement, never a
caller-reassigned source, epoch, segment or slot. In a physical cell, larger
non-reused slots supersede smaller ones; equal slots must have identical token
and entry identity. Both publication and removal CAS the observed cell owner.
Removal retains a retired cell before acknowledging Removed on its reservation.
Never physically delete that cell while a publisher may still be in flight:
otherwise a delayed INSERT IF NOT EXISTS could resurrect the removed edge.
Within one slot, retirement is irreversible. A later slot may replace it,
and older publish/remove retries cannot change the later cell. A cell write
that precedes its reservation acknowledgement is recoverable by the same token.
These derived-cell fences supplement, rather than replace, source-authoritative
maintenance fencing and stale-source checks.

Name-index partitions store the bounded SHA-256 digest of the exact logical
target identity in their physical name component, matching the allocator head
identity. Reservation metadata retains the exact logical target. This does not
normalize or case-fold an authored name, or impose a new authored name limit.

Page <=256 rows and probe <=16 segments/shards per request/work turn, returning
a continuation even if that bounded turn is empty. Cursors carry layout
version, shard, segment and last identity; no silent truncation or restart from
zero. Shard-head rows remain constant-sized; segment enumeration is numeric
and bounded per turn, not an unbounded directory row. Rollover is part of
ordinary index maintenance, not a reason to reject already acknowledged source
or require operator repartitioning. Existing lower datastore limits win.
Storage/queue exhaustion applies bounded backpressure with durable pending
work; never drop accepted data. Layout changes require versioned cursor/index
migration and measured evidence.

The reverse-index reader counts both head reads and segment reads against the
16-probe budget. Retired cells count against the 256-row scan budget even though
they are not returned. An opaque continuation retains its exact logical index
scope, shard, segment, known head segment and last row identity. Empty turns
advance that continuation rather than restarting or implying exhaustion.
The selected name or UID index is paged independently; a target fan-out worker
persists progress for both required index scopes. Readers return thin hints,
not proof that source version/reference epoch is still authoritative. Source
lookups must reject stale hints before scheduling or publishing observations.

Binding history must have a finite limit too: at most 256 distinct authored
target identities/epochs per Translation; a further identity-changing update
is rejected explicitly. Same-identity no-op/status updates do not consume it.
Keep historic bindings needed to prevent pin toggling from silently rebinding.
This is a resource budget, not automatic compatibility/migration behavior.

Cross-table writes are not atomic by assertion. Persist maintenance intent
with the authoritative CAS, then idempotently update projections/edges using
source resourceVersion and retain work until acknowledged. Reserve index
segment capacity before its thin-row write; interrupted reservations are
recovered, not counted as accepted content. A source acknowledgement
is not a successful partial patch set. Existing accepted-Git replay must
recover interrupted source projection without requiring another push.

### Source mutation coordination

Source/status writes and private binding CAS must share one durable per-source
coordination CAS at the authoritative physical key. Checking the source and
then separately CASing a binding is insufficient: a reference update can commit
between those operations. A non-reused private coordination version and an
explicit pending phase fence every participating writer, including binding-only
writes whose public source resourceVersion does not change. Memdb obtains the
same invariant through its shared source/binding/index transaction.

In Scylla, `binding_snapshot` in `translations_by_key` is the authoritative
bounded reference epoch and sticky UID/history. Final source and binding
publication replaces the snapshot in the same row CAS. The separately created
binding table is unused by this implementation; reading or writing it as an
independent authority would reintroduce the source/binding race.

Freeze the desired source/binding transition, previous/current placements,
new reservation tokens and progress under that coordination CAS. Workers resume
the same intent; they cannot allocate fresh tokens for a completed earlier step.
Retain the complete intended accepted source at its resource-sized authoritative
key, not in name/list/dependent indexes. Keep public source fields unchanged
while an update is pending, then publish the desired full envelope and final
placement state atomically after required private writes are acknowledged.
A recovered intent must validate all previous placements against the frozen
source identity/version/epoch before performing index writes.
A pending read fails closed; it cannot expose old Ready as current. Binding-only
progress must not manufacture a public source revision or watch event. CDC
serialization distinguishes pending/private changes from a final source commit.
Never clear an uncertain intent by timeout or compensate a committed public
source back to an older version.

Creation must establish a durable bounded namespace-index discovery anchor
before persisting a provisional full source. Reserve its immutable UID locator,
retain that namespace placement, persist the full creation intent at the
authoritative source key, and only then claim the name and complete the remaining
projections/binding. The name claim is the public identity assignment point.
Another callback finding that claim recovers its original UID/timestamp and
frozen source intent rather than assigning a replacement identity.
An interrupted pre-source anchor is not proof of accepted content. Cleanup must
first fence the absent source key with a retained retired record, so a delayed
provisional INSERT cannot create an undiscoverable source after anchor removal.
If the provisional source won that CAS instead, resume it rather than discard it.

Maintenance discovery is a read-only metadata view of source-authoritative
work, not an independently writable queue. Bounded namespace anchors discover
provisional, Updating, Deleting and accepted sources; an absent full source is
reported as Unpublished until its exact key is fenced or recovered. Co-located
source coordination supplies the work ID/private version, including private
binding changes whose public version is unchanged. The reserved initial
maintenance table is not another authority.

The internal source-audit pager enumerates authoritative source headers
independently of namespace anchors and UID locators, including retained retired
sources. Native paging reads one metadata-only server page and SERIAL source
headers; memdb visits live and retired UID tables in separate phases. Every turn
counts all scanned records, including other namespaces, against the 256-record
ceiling. Sparse empty pages retain advancing, namespace/backend-bound
continuations. Missing anchors and native UID locators do not hide retained
source authority. Recovery checks the hint's namespace/UID, creation lifetime,
private/source versions and phase against current authoritative metadata before
reusing the existing serialized per-source recovery protocol. Missing namespace
cells can be restored using their original accepted allocations. A native live
UID locator cannot be reconstructed from a discovery hint: it contains frozen
name-vacancy and global lifetime authority that discovery alone does not prove.
Missing live locators fail closed. A retained Retired source is permanent proof
that permits conditional restoration of its retired-only UID locator; a
contradictory lifetime is never overwritten, and replacement name owners remain
unchanged. Native source-audit recovery requires the retained full-source key
at both discovery validation and recovery; an absence-only Unpublished hint
never invokes provisional-creation retirement or releases remaining UID/name
authority. Raw and instrumented backends preserve this recovery operation.
Neither discovery nor per-source recovery certifies a complete namespace scan.
Fenced recovery first invalidates the previous private per-source repair
watermark, then publishes the verified coordinator version only after successful
recovery against that unchanged version and final lifecycle. Source, status,
binding and retirement transitions invalidate it. Bounded recovery turns
validate the complete discovery page before work and recheck all published
versions before accepting a continuation. A failed or raced turn returns no
accepted cursor. These source-level markers do not replace server-owned
namespace progress, all-index coverage or mutation-watermarked deployment
certification.

A separate bounded reservation audit visits source-index allocation records,
including reservations no longer listed by the current source. Source and
reservation cursors have distinct scopes in addition to namespace/backend
binding. Accepted placements reuse existing stable repair; obsolete Reserved
and Committed allocations are retired through their original slot/token
protocol, so a delayed publisher cannot revive them. Native decisions read
reservation authority before current serialized source/binding authority;
new intents cannot adopt a previously allocated token. Missing source is not
retirement proof: preserve records unless current source authority or a matching
permanent retirement permits cleanup. Metadata-only scans count foreign and
unrelated maintenance records against each page budget. Unowned Preparing
records are preserved with RepairRequired because cancellation authority is
missing. Pending-source recovery can replan allocation coordinates; return
Conflict and reload reservation authority before source authority on retry.

Independent physical-cell discovery scans namespace, target-name and target-UID
layouts with a separately scoped cursor. Missing reservation authority remains
detectable without reservation enumeration and fails closed. Native target-name
keys are irreversible digests, so their full address requires matching retained
reservation authority. Current serialized cell images must match observations
before invoking existing fenced reservation recovery.

Server-owned namespace progress traverses Sources, Reservations, Cells,
VerifySources and Scanned. Each bounded turn uses persisted cursors and a
full-preimage LWT progress CAS; caller cursors are not accepted. A failed page
does not advance progress; an uncertain database response requires rereading the
stored row. Replacement stores and the existing projection-repair operator
service resume the same traversal. Operator audit/dry-run only previews the
next discovery page without initializing progress or claiming consistency;
confirmed repair performs one fresh, authoritative turn. Scanned records
traversal, never mutation-fenced completeness or deployment certification.
A subsequent turn starts a fresh run. T047 still owns global backfill and
activation capability.

Stable per-source audit preflights all two or three accepted placements against
the current binding, persisted reservations, allocation heads and cells before
writing any repair. Re-publish missing or older thin cells through the existing
slot/token CAS using the accepted allocation, never a new token or capacity
reservation. Recheck the serialized source coordination header around repair;
a concurrent source transition returns conflict. Its retained per-cell
retirement fences protect against delayed publication after deletion.
Healthy audit and cell-only repair do not advance source/private versions,
allocate capacity, rewrite binding history or produce resource events.
Missing allocation heads, retired accepted placements or contradictory
identities require explicit repair, not speculative head reconstruction or
resurrection. Memdb preflights and commits the same repairs transactionally.

Retain name-vacancy generations and small retired UID/source records. A creation
intent freezes the exact name-vacancy token it may claim; retries never adopt a
later vacancy token. Final deletion changes that token instead of physically
removing the name fence. Otherwise an old delayed INSERT IF NOT EXISTS could
reclaim a name after deletion/replacement. Name and target identity components
must use bounded exact-identity physical keys, with authoritative identity
verification, rather than imposing new authored length restrictions.

Deletion first CAS-marks Deleting with the frozen exact placements and a unique
name-vacancy token. Retire reverse edges, conditionally release only that UID's
original name claim, and then CAS-mark Retired. Fence the UID locator and compact
the source payload to its small retained physical-key/version tombstone before
retiring namespace discovery last. The creation namespace reservation remains
derivable after compaction, so replacement workers can finish cleanup without
the old body or intent. A newer vacancy or name owner proves old release already
completed; delayed cleanup must never adopt that newer token.

Translation registers a custom frame decoder in the existing catalog CDC
runner and uses the shared journal/sequencer and durable source checkpoints.
Creating-to-Stable emits Added, Updating-to-Stable emits Modified only when the
public source resourceVersion advances, and Deleting-to-Retired emits Deleted
with the final accepted source snapshot. Provisional insertion/retirement,
pending intents, binding-only completion and retired-row compaction are
progress-only. Reject incomplete full images, regressing versions, immutable-key
changes, duplicate images, impossible phase transitions and unbounded frames
before publishing any subset or checkpointing them. Private retired images
may lack cleared public name/body fields; their physical identity is retained.
Event identities are deterministic across frame ordering and replay.

These are source-store/recovery implementation obligations. The allocator,
cell publication and reverse-index pager alone do not implement this protocol.

### Ownership and repository membership

Accepted Translations participate in the existing repository membership and
blocking-owner projections. In Scylla, retain one empty-kind marker beside
each live Translation projection in the same partition. The marker stores its
private source coordination version and retired state; a single-partition
conditional batch advances that marker and inserts/deletes the live row.
Older workers cannot change a newer marker, including owner ABA, deletion and
repair. Equal-version retries may restore the same projection but cannot change
its retired state. Missing or contradictory marker metadata fails closed.
Repository membership retains the existing shared 32-shard layout, not the
Translation-specific 64-shard dependency layout.

Existence queries seek past empty kinds so retained markers are not permanent
parent-deletion blockers or an unbounded scan. Blocking-owner paging includes
Translation among the existing bounded kind queries. Stable source maintenance
reuses its serialized source snapshot to repair current ownership/membership
without changing source/private versions, allocations, bindings or events.
Memdb performs equivalent projection repair in its source transaction.
The generic offline projection snapshot excludes Translation-owned live rows
and validated private membership markers; generic unfenced repair must not
delete or recreate them. This isolation is not the full bounded Translation
audit/backfill or repair watermark described below.

Audit/repair checks authoritative source/binding versus all indexes, including
stale edges, deleted resources and interrupted retargeting. Publish an indexed
repair watermark before enabling dependent processing. Preserve originals,
failed evidence and existing datasets. Both backends implement the same
contract; memdb success alone does not certify Scylla CAS/recovery.

Enable CDC only on the authoritative Translation table with full preimage,
postimage and 14-day TTL. Register it in `catalog_cdc.go`; reuse
`resource_watch_clock`/`resource_watch_events`. Do not add a per-kind journal.

## Ingestion and Replay

API startup parses `api.translation.enabled` (default false) and the optional
expected `api.translation.descriptor_digest`, including standard environment
overrides. The application validates the shared `TranslationActivation` before
constructing datastore/providers/listeners, including standalone SmartHTTP and
Storefront roles, and passes the same value to the catalog admission runtime.
This build always rejects enabled=true; a nonempty mismatched descriptor is
also rejected. Disabled deployments with no expected digest remain compatible,
and matching digests do not certify fences/backfill or enable any operation.
Actual mixed-version deployment and activation certification remain separate
acceptance obligations, not consequences of these startup checks.

The internal accepted-source projector now preserves exact authored bytes and
body, accepted Git blob identity/source digest, UID, creation metadata and
controller observations. Identity hashing retains byte-distinct documents.
Formatting-only changes advance resourceVersion without changing generation.
Projection failures propagate; duplicate/lost-create-reply replay is idempotent.
Removal checks repository/ref/path, exact accepted source and current commit
before UID/version CAS. Committed-result proof also requires raw-source equality.
Focused memdb/persisted-state-interleaving contracts cover source/status races
and removal versus newer projection without writing target source or resolved
content. These are internal component tests, not durable callback delivery,
connected replica, physical-fault or public ingestion acceptance.
Public activation remains disabled. Signed source-order/replay and exact
committed-removal contracts now complete T014/T022 internally; effective-candidate
public dispatch (T048) and deployment certification remain required.

The existing Rust post-receive callback now retries transient transport,
deadline, overload, conflict and internal RPC failures in at most three
attempts. Each attempt is limited to 30 seconds; retries wait 100/200 ms,
retain the original accepted snapshot and generate a fresh receiver-bound
signed transport request. Policy/authentication/input/precondition failures
are terminal for this retry loop. Tests exercise actual handler wiring,
unchanged actor/ref/path attribution, distinct RPC nonces, retry exhaustion
and a nonresponding receiver. The complete native unit/integration suites,
Clippy and formatting checks pass. Production delivery now uses the durable
receive/worker path below; the legacy no-receipt fixture pipeline retains its
in-process behavior. Internal source-order/replay contracts complete T014/T022;
connected deployed replacement acceptance remains US5 work.

The internal native `AdmissionReceiptStore` persists the existing
`CallbackAuthorization` snapshot, including repository/receive identity,
original actor attributes and the complete ordered accepted-ref vector.
The private GSTRCV02 wrapper also retains the exact catalog-routing subset.
Production preparation captures it from the accepting handler before ref
publication. Replacement replay uses that subset instead of reclassifying the
receive with its current branch pattern; RBAC-only head routing still derives
from the pinned original trees. Routing must be a duplicate-free subset of the
accepted refs and shares the existing file/ref bounds. Prior records without
routing authority are preserved and rejected, not silently migrated or replayed
under a guessed configuration. Drain such queues using their originating
binary before upgrading.
Versioned, checksum-addressed protobuf files distinguish Prepared, Committed and
Acknowledged;
file and directory fsync cover publication, phase transition and acknowledgement.
Bounds are 128 retained receipts, 1 MiB per file, 256 refs per receipt, one
in-flight temporary file and 32-entry metadata pages. Saturation does not evict
older work. An OS advisory ownership lock permits only non-overlapping users
of the retained directory; a failed/terminated owner releases it automatically.
Replacement validates records before use and can reclaim an interrupted
temporary hard link even when all 128 receipt slots are occupied.
Tests verify immutable context after reopen and actual killed-writer replacement,
phase/removal guards, bounds, pagination, corrupt/unknown-file preservation and
ambiguous or unattributed context rejection. Full native tests, Clippy and
formatting pass.

The internal production-facing preparation API now verifies the retained
repository's canonical identity, all exact old-ref preimages and absence of
native loose/packed ref locks before publishing a receipt. Unfenced preparation
is test-only. Its ref recovery core requires uninterrupted repository exclusion,
or replacement ownership before serving any new mutation. It preflights every
ref/object before changing refs or reclaiming this receive's locks, permits only
the recorded old/new outcomes, conditionally finishes pending create/update/delete
edits, fsyncs refs/directories and then commits the receipt. Before public ref
publication, recovery pins every distinct nonzero old/new object under the
existing hidden, client-write-protected internal namespace, with at most 512
pins per receipt. Pins and their directory chains are fsynced. Divergent refs,
symbolic replacements and missing objects retain evidence instead of overwriting
newer state. A previously committed receipt never reapplies refs after a reset.
Actual killed-child tests cover prepared, partial native lock-held publication
and completed refs with an uncommitted receipt; these are owned component
process failures, not deployed physical-fault acceptance.

The internal delivery entry point loads only committed work, validates retained
commit/root-tree objects and awaits all applicable ref callbacks. Each RPC
retains the protocol's exact singleton envelope and original actor/receive; the
receipt retains the full ordered vector. Failure after one acknowledged ref
keeps the complete receipt, so replacement may redeliver that ref before
acknowledging the remaining refs. Cleanup takes the caller's repository write
guard only after the network requests finish and moves that guard into blocking
work. Under that exclusion it verifies native cleanup locks absent, durably
marks Acknowledged, removes only exact receipt-owned pins and finally removes
the receipt. Interrupted Acknowledged work resumes cleanup without requiring
objects or replaying delivery and must fence later mutations until completion.
Ref packing/actual Git pruning tests verify old/new commits remain reachable
after all public branches disappear and become collectible only after cleanup.
Owned killed-child tests also cover lock-held native pin cleanup.
Production ReceivePack now durably promotes accepted objects before
fenced preparation, then uses native recovery/publication under its owned
guard. The durable path preserves the bounded small-pack loose-object
optimization, syncing exact loose files and directory chains, including when
objects were already packed. Large packs retain synced pack/index publication.
A bounded derived receipt metadata index is rebuilt from fully validated
files and updated before publication/acknowledgement fsync can fail. Every
mutation entry point settles Prepared/Acknowledged work under its owned guard.
Committed work permits later writes but blocks repository deletion; missing
or tombstoned retained storage cannot be recreated around a receipt. Older
delivery cleanup also refuses to cross a newer pending receive.

The production binary opens the exclusive store and validates retained
repository lifetimes/recovers pending work before binding its RPC listener.
Only after binding/starting RPC does it run one delivery worker, preserving
API readback availability. The worker consumes 32-item pages, wakes on accepted
publication and has a one-second retry/recovery fallback. Delivery failure
retains the record and does not prevent other receipts from being processed.
Shutdown cancels pending delivery without acknowledgement; in-progress
blocking recovery/cleanup retains its guard and store ownership.
Signed native RPC tests verify actual multi-ref acceptance retains the
original actor/vector through reopen, pending/corrupt work gates all unary
writers, pre-serving and next-write recovery agree, and deletion waits for
acknowledgement. They also verify missing storage cannot be recreated and a
failed callback does not block unrelated no-callback cleanup. These tests are
not full deployed callback-loss/process-replacement acceptance.
T014/T022 remain unchecked and public activation stays disabled.

ReceivePack's blocking ref transaction now owns its repository write guard and
schedules post-receive admission after successful commit inside the same
blocking task, using the existing context-propagating blocking helper. Actual
signed streaming-RPC tests cancel the request while a native ref lock is held:
the repository lock remains fenced, completed commits deliver the captured
actor/receive/ref snapshot, and failed ref transactions deliver no callback.
This closes the request-cancellation scheduling gap, not the process-crash
gap between ref commit and non-durable callback scheduling.

The same native lock-lifetime audit covers legacy `CommitFile`, guarded
`CommitFileAtRevision` and `CreateTag`: each moves an owned repository guard
into context-propagating blocking work, matching existing create/delete
repository and file-deletion behavior. Actual signed file-write tests cancel
while the repository marker lock blocks mutation, then verify completion with
byte-exact content and retained guarded receipts. This is in-process
cancellation correctness, not a replication or durable callback guarantee.

The API callback receiver distinguishes failed Git ref reads from deliberate
stale-ref skips. Transient namespace/ref lookup failures return `Unavailable`, and
exhausted projection convergence returns `Aborted`, allowing the common native
retry loop to retain the accepted request snapshot. Captured-authorization ref
read retries are limited to three attempts rather than waiting indefinitely
for the caller deadline. Signed callback tests verify failure-without-source-
mutation, successful retry after recovery, preserved deleted-ref skips and
convergence recovery. Terminal ref-read denials return `PermissionDenied`;
authentication, cancellation and deadline codes retain their distinct meanings.
A proven stale policy denial skips status writes without requesting retries,
while an unavailable denial fence keeps projection retryable. This
acknowledgement contract is not a durable queue.

Extend parser, comparable identity/body, candidate validation, identity lookup,
upsert/delete and callback dispatch together. Rust forwards raw files through
the existing RPC; extend parity tests, not semantic duplication.

Duplicate/delayed callbacks compare accepted repository/ref source order and
resourceVersion under the existing projection guards. Older callbacks cannot
replace newer content or observations. Re-evaluate current target evidence at
projection/reconciliation; pre-receive validation is not a perpetual lock on a
mutable target. Post-acknowledgement interruption and lost replies must resume
from durable accepted source, not a client retry assumption.

Store complete Translation source or none for that revision; no valid-patch
subset is ever admitted. Verify target source and resolved digests before and
after every mutation, invalidation, retarget and Translation deletion.

## Observability

Use shared SDK metrics/traces/logs for validation/admission/guard latency,
outcome, CAS conflicts, target lookup errors, queue depth, oldest pending age,
fan-out pages, repair lag, replay and convergence. Labels are bounded enums:
kind, stage, outcome, reason and replica role, never paths, locales, names,
UIDs, translated text or credentials. Correlation/source identifiers belong in
access-controlled bounded logs, not metric labels.

Readiness must expose unavailable stores/journal and incomplete required
repairs/unsupported descriptor activation. Per-resource invalidity does not
make the whole API healthy/unhealthy by accident. Saturation is observable
bounded backpressure, never a dropped accepted resource.
