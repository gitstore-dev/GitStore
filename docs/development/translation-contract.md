# Translation implementation boundaries

Translation provides validation and resolution evidence, not localized
application. It never writes translated text to a target or builds a localized
catalogue. API and controller activation default to disabled. The components
below do not constitute a certified enabled deployment.

Scylla migration restart can recover a committed simple `ALTER TABLE ... ADD`
whose progress write was interrupted. The normal migration lock protects
recovery; the embedded checksum and the existing column's exact type and kind
must match before a conditional progress update. Completed migration history
and existing resource data are preserved. Changed migration bytes,
inconsistent history and incompatible columns fail closed; do not edit the
ledger or applied migration files to bypass them. Read-only schema validation
does not perform recovery.

## Owned acceptance configuration

The normal API constructor accepts `api.translation.enabled=true` only with
`api.translation.descriptor_digest` equal to the build's capability digest.
Controller startup uses the same rule for `controller.translation.enabled` and
`controller.translation.descriptor_digest`; its configured gate registers the
normal disk-backed controller with independent per-replica checkpoints.
The environment equivalents use `GITSTORE_API__TRANSLATION__` and
`GITSTORE_CONTROLLER__TRANSLATION__` prefixes with `ENABLED` and
`DESCRIPTOR_DIGEST` suffixes. A configured mismatching pin is rejected even
when disabled; an unpinned disabled component retains the default behavior.

This path exists for explicitly owned acceptance deployments without private
activation overrides. It does not certify peer roles, target-writer coverage,
native ingestion, sustained load, rolling upgrades or rollback. Provision real
authentication and controller enrollment, verify every participating role and
writer, and retain all source, journal and independent checkpoint storage.
Production configuration stays disabled until connected acceptance and rollout
requirements pass. Never use a matching descriptor as a substitute for them.

The opt-in native ingestion fixture uses `TRANSLATION_TEST_RUN=1` and
`TRANSLATION_TEST_OWNED_DEPLOYMENT=1`. Supply distinct base URLs with
`TRANSLATION_TEST_API_A`, `TRANSLATION_TEST_API_B`,
`TRANSLATION_TEST_GIT_URL_A` and `TRANSLATION_TEST_GIT_URL_B`, an explicitly
owned `translation-` namespace through `TRANSLATION_TEST_NAMESPACE`, and private
credential files through `TRANSLATION_TEST_TOKEN_FILE` and
`TRANSLATION_TEST_CONTROLLER_TOKEN_FILE`. Prepare a fresh namespace, its Ready
system repository and certified target coverage before selecting
`TestTranslationNativeGitIngestion`. It retains accepted data and requires real
authentication, enrolled controller authority and durable watches.

`TRANSLATION_TEST_QUICKSTART_LIFECYCLE=1` selects
`TestTranslationNativeQuickstartLifecycle` with the same owned fixture settings.
Use a separate fresh namespace with an empty system repository, not a retained
baseline namespace. The fixture checks named-selector reordering,
missing-selector invalidation and restoration, target removal/recreation,
sticky binding across unrelated source edits and explicit UID retargeting
through both APIs. It also rejects six guide candidates without publishing
their valid companion documents or advancing refs, and checks deferred target
arrival and both candidate orderings. Removing an optional Product option title
alone is not a missing-field test: the canonical Product model retains it as
empty text. Remove the selected option member instead.
Accepted resources are retained; optional `TRANSLATION_TEST_EVIDENCE_FILE`
receipts must use a fresh protected path. These are ordinary ingestion and
lifetime correctness checks, not capacity or rollout certification.

`TestTranslationNativeReplicaStatusAndBinding` consumes that accepted namespace,
not a fresh one. Its second private credential file,
`TRANSLATION_TEST_CONTROLLER_TOKEN_FILE_B`, must contain an independently
issued enrolled-controller token. It checks cross-API status CAS, idempotent
binding, stale preconditions and unchanged target material. These correctness
fixtures do not certify sustained load or rolling capability. Two Smart HTTP
URLs also do not prove both Catalog owners: callbacks follow the singleton
Git process's configured Catalog destination.

`TestTranslationNativeDuplicateAndOutOfOrderCallbacks` additionally accepts
`TRANSLATION_TEST_CALLBACK_DRIVER`, the Catalog package's compiled Go test
binary. Supply the two actual Catalog addresses in
`TRANSLATION_TEST_CATALOG_A`/`TRANSLATION_TEST_CATALOG_B` and the protected shared
RPC secret file in `TRANSLATION_TEST_RPC_SECRET_FILE`. The owned fixture uses
its authenticated static-user JWT actor and normal temporary controller
authority. It creates and updates one independently named source through the
two ingress URLs, then invokes `TestTranslationNativeCallbackDelivery` to
concurrently deliver signed old/current callbacks to both Catalog owners.
Exact current source/status, Product material and canonical refs/history must
remain unchanged around replay. `TRANSLATION_TEST_EVIDENCE_FILE` optionally
selects a fresh protected receipt; existing receipts are never overwritten.
This tests actual callback routing, not process replacement or fault recovery.

`TestTranslationNativeCallbackTransportProxy` in the same Catalog test binary
is a bounded, loopback-only transport injector, selected with
`TRANSLATION_TEST_CALLBACK_PROXY=1`. It additionally requires explicit owned
deployment confirmation, a fresh `TRANSLATION_TEST_CALLBACK_RUN_ID`, a new
absolute `TRANSLATION_TEST_CALLBACK_EVIDENCE_DIR`, and loopback addresses in
`TRANSLATION_TEST_CALLBACK_LISTEN`/`TRANSLATION_TEST_CALLBACK_UPSTREAM`.
The helper creates a private directory and publishes `ready.json`. It preserves
signed requests, deadlines and all three pre-receive validation methods.
Only admission delivery is intercepted: `callback-loss` prevents forwarding,
whereas `reply-loss` follows successful upstream admission. Protected,
commit-attributed records distinguish simulated losses from successful forwarded
retries. Invalid controls fail closed; receipts cannot be overwritten.

After confirming sole ownership and retaining storage, an owned coordinator
can sequentially replace the singleton Git process with its Catalog destination
pointing at this proxy. The Git configuration requires a `dns:///host:port` URI,
not an HTTP URL. Select `TestTranslationNativeCallbackTransportRecovery` with
the native fixture variables and matching proxy directory, run ID and upstream
address. It exclusively claims that run, atomically selects/restores faults,
pushes through both ingress URLs, joins interceptions and subsequent successful
ordinary retries to the accepted commits, and checks exact current source/status
on both APIs, unchanged Product material and unchanged refs during recovery.
It does not resubmit accepted sources.

Protected recovery receipts include two fresh current source observations,
one per API, after transport restoration and before sustained recovery traffic.
These bounded proof reads use authorized snapshots for the private source
fingerprint and retain UID, version, generation, revision and content digests.
Earlier push or watch observations cannot substitute for them. Ordinary load
reads still do not request a private snapshot for every operation.

`TRANSLATION_TEST_POST_ACK_RESTART=1` additionally requires
`TRANSLATION_TEST_GIT_PID`. After a successful push and observed callback loss,
the fixture publishes `restart-request.json`. The owned coordinator has a
bounded window to interrupt that exact Git writer, confirm its exit, and start
one replacement on the same retained storage. It atomically publishes a private
`git-restored.json` containing matching `runId`/`newCommit`, `kind: "physical"`,
distinct `beforePID`/`afterPID`, and restoration time `at`. The fixture verifies
the old process is gone and the replacement is a live Git process, then requires
another matching delivery while loss is still active before restoring transport.
Physical replacement and simulated transport faults remain separately labeled.
The protected `recovery.json` is diagnostic evidence, not sustained acceptance.

Alternatively, `TRANSLATION_TEST_CHAOS_PROFILE` selects one of the existing
`api-restart`, `controller-restart` or `journal-interruption` profiles while
callback loss holds the acknowledged source. Supply the profile's explicit
`CHAOS_CONFIRM=1`, owner, target and required peer/checkpoint settings. The
fixture invokes the existing `make chaos` interface with `CHAOS_TRANSLATION=1`
and a bounded same-run window, joins the physical restoration receipt, then
requires accepted-source retry and exact both-API convergence separately.
Do not combine this option with post-acknowledgement Git replacement.

Physical recovery captures retain the chaos runner's sanitized target
before/after observations and, for journal interruption, the observed paused
target. Replica restarts also retain the surviving peer's before/after captures.
The diagnostic verifier can join a declared shared-journal pause to
the owned Scylla lifetime, unchanged process epoch, full-window API/controller
resources and exact source recovery. It does not infer a pause from a health
probe or certify replica replacement from this unchanged-epoch path.
The public verifier also supports separately declared API/controller replacement evidence:
original scrapes must join distinct native process identities and starts to the
physical before/after captures, per-process resource baselines, a bounded
declared gap and an unchanged owned same-role peer. Controller captures must
also retain each replica's independently writable checkpoint source, volume and
destination across replacement, matching preflight without overlapping storage.
The automatic connected companion does not yet collect a combined
physical/resource receipt; synthetic verifier regressions are not deployed
recovery or capacity certification.

The proxy terminates after five minutes, or when the coordinator atomically
publishes a protected `stop.json` with the matching `runId`. On success or
failure, restore direct Catalog routing through a non-overlapping singleton
replacement **before** stopping the proxy. Its `stopped.json` proves injector
termination only, not domain recovery. Never leave a live Git process depending
on an expiring test proxy.

Two additional opt-in fixtures exercise capacity-harness correctness, not
capacity acceptance. `TRANSLATION_TEST_CAPACITY_DATASET=1` selects
`TestTranslationNativeCapacityDataset`; provide ten fresh, prepared owned
namespaces as a comma-separated `TRANSLATION_TEST_CAPACITY_NAMESPACES` value,
starting with `TRANSLATION_TEST_NAMESPACE`. It seeds 1,009 documents, including
1,000 dependents of one Product and an exact one-MiB source, then checks every
source through both APIs and durable watches. Failed attempts retain accepted
data; a subsequent fresh run must not overwrite those namespaces.

`TRANSLATION_TEST_CAPACITY_DRIVER=1` selects
`TestTranslationNativeCapacityPushSchedule` in a separate fresh, prepared
namespace. It exercises bounded scheduled pushes through both ingress
endpoints, continuously consumed durable watches, independently guarded current
reads and a simultaneous finite 20-reads/s probe sharing the push schedule's
exact offered-window anchor. Stable accepted sources are verified before
offering read traffic. Historical delivery supplies source evidence
only when its exact identity/version/body/patches join a separate current Ready
read; it never supplies Ready authority. Ordinary load reads do not manufacture
the private source fingerprint or issue additional controller snapshot requests.
The fixture enrolls and removes only its own temporary key on
`TRANSLATION_TEST_CONTROLLER_SERVICEACCOUNT` (default
`gitstore-controller-manager`) and exercises normal short-lived token renewal.
It preserves Product material and aggregates. Neither short probe supplies a
full-window receipt, a retained production-scale baseline or physical-fault
acceptance; the public capacity selectors are not extended by these test flags.
`TRANSLATION_TEST_CAPACITY_SECOND_NAMESPACE` optionally selects a second fresh,
prepared owned namespace for clone/watch routing checks. Alternatively, the
ten-name `TRANSLATION_TEST_CAPACITY_NAMESPACES` list selects a ten-second,
ten-namespace diagnostic with two globally bounded push workers and 200 offered
reads; do not combine it with the second-namespace flag. The retained-source
fixture, `TestTranslationNativeCapacityRetainedSource`, uses
`TRANSLATION_TEST_CAPACITY_RETAINED_SOURCE=1` with the original ten namespace
names and privately recorded `TRANSLATION_TEST_CAPACITY_SOURCE_UID` and
`TRANSLATION_TEST_CAPACITY_SOURCE_REVISION`. It compares the full near-limit
source to retained Git bytes and guarded observations on both APIs without
resubmitting it.

The public `api/readiness` selector also supports
`CAPACITY_TRANSLATION_READINESS=1` for authenticated read-only diagnostics.
It consumes a protected ten-namespace expected-source fixture, checks exact
source/current-status responses on both APIs and records source-attributed,
explicitly non-certifying receipts. See the capacity guide for its private
fixture requirements. Adding `CAPACITY_TRANSLATION_PUSHES=1` starts the native
Git companion and releases push/read workloads at one shared future timestamp.
The diagnostic verifies exact accepted source/body/patches, both APIs' watch
and current-status observations, unchanged target/Product hashes and retained
canonical history. It requires the independently verified retained-corpus
fixture and both owned Git ingress URLs; refresh source preparation after a
push changes canonical refs. Alpha and production modes still fail closed
until complete topology/resource attribution and acceptance verification are
available. Neither diagnostic mode certifies sustained or physical-fault gates.
Read-only diagnostics allow up to two hours. Connected windows are capped at
90 minutes so setup and recovery retain time under the bounded two-hour child
deadline; this does not shorten either required acceptance minimum.
`TRANSLATION_TEST_CAPACITY_READ_FIXTURE=1` selects
`TestTranslationNativeCapacityReadFixture` to prepare those expected read cases
from the original retained ten-namespace dataset. Supply a fresh private
`CAPACITY_TRANSLATION_FIXTURE_FILE` and matching `CAPACITY_RUN_ID`, alongside
the native fixture variables. It joins every retained document to independently
read Git bytes and its last authored commit, both APIs' current observations
and normally renewed controller-source snapshots, without resubmitting sources.
Canonical refs must remain unchanged, and the final public cases must match
both replicas. Source preparation alone is not sustained or push evidence.

## Shared semantics and descriptors

`shared/translation` is the pure Go semantic implementation used by the API and
controller. Callers own authorization, I/O, persistence and checkpointing. The
Git service forwards complete authored bytes to Go without a second semantic
parser or a per-kind protobuf.

Descriptors identify an exact API version and kind, eligible text leaves,
stable list selectors, reference boundaries, body support and work ceilings.
The delivered descriptors cover Product, ProductVariant, CategoryTaxonomy,
Collection and File. File permits the body channel, not an invented title.
Selectors use declared identities, never array positions or implicit traversal
through another resource reference.

A descriptor digest includes its resolver version and field/list/work policy.
The capability digest additionally covers the authored API version, locale
policy and document/path ceilings. These digests identify compatible semantics;
they are not deployment-capability or repair certificates.

Authored locale spelling and source/body bytes remain unchanged. A canonical
locale is derived evidence, not a rewrite or a uniqueness key. YAML enum names
and GraphQL enum names retain their separate established representations.

## Admission and authorized snapshots

Pre-receive and single-manifest validation share the same preflight. All source
operations are authorized before target semantics. A complete candidate tree
takes precedence over a stale stored target, independently of file order.
Unchanged Translations are not retroactively rejected when a target changes.

Target-read authorization uses serialized Namespace and target ownership
metadata before fetching content by its proved UID. A final observation checks
material identity and lifetimes. Missing targets are unresolved only when
current coverage and matching missing/vacant heads prove absence; unavailable,
pending or denied proof is not absence.

Rejection is whole-document: no validated subset, partial body or target write
escapes an error. Existing admission diagnostics retain safe reasons and field
locations. Documents are limited to 1 MiB, 65,536 YAML nodes, depth 64 and 256
patches. Path and target-work limits apply independently. Diagnostics are
bounded to 32 entries with 256-byte messages and explicit truncation.
Human Git rejections retain the common `file: message` format; structured
reason and field locations belong to the admission diagnostic envelope.
The shared Catalog in-flight gate rejects overload without a waiting queue;
cancellation prevents later parse phases from accepting the request.

The controller-only snapshot additionally fences the source lifetime/version,
Namespace lifetime and stored observation. It preserves missing/null target
fields rather than manufacturing empty text. Ordinary public reads use thin
proof, not full target content.

## Binding and reconciliation

A server-owned binding records the first observed eligible target UID, even
when the selected text is invalid. The same authored reference cannot silently
adopt a same-name replacement. Unrelated source edits and public status writes
do not reset binding history. A recognized authored reference change advances
the reference epoch; source and binding CAS fences arbitrate concurrent workers.

The controller-only binding mutation follows the common input/payload contract:
`bindTranslationTarget(input: BindTranslationTargetInput!): BindTranslationTargetPayload!`.
Clients select the acknowledged state under `binding`, not directly on the
mutation result.

Dependent discovery is an explicit controller scan, not a Relay resource list:
`scanTranslationDependents(target:, limit:, continuation:)` returns
`TranslationDependentScan { items, continuation }`. The row budget defaults to
256 and accepts 1 through 256; independent probe limits also bound each turn.
Retired cells consume the row budget, so a turn can return no hints and still
have a continuation. Consumers resume even after empty batches and stop only
when the continuation is null. Progress is scoped to the same target index and
Namespace lifetime; it is not an edge cursor or a snapshot of dependent
resources. Each hint must be reloaded before reconciliation.
The `translations` resource list retains its Relay connection contract.

The validation-only reconciler materializes the common status shape:
`conditions`, `observedGeneration`, `lastAppliedRevision` and `resolved`.
`lastAppliedRevision` identifies the Translation source that was reconciled;
it does not mean translated content was applied. Kind-specific public results
remain under `resolved`. There is no public freshness enum, check timestamp,
second historical status envelope or connection resource version.

Source UID/version, generation and revision preconditions protect status
writes. Both typed and generic writes require the currently enrolled controller
ServiceAccount in addition to policy permission. Enrollment uses the configured
Admin controller subject independently of the selected authorization provider;
static-policy controller access does not enable dynamic RBAC authoring.
Operational failures retry
without publishing partial validation results.

## Freshness, replay and bounded repair

The public guard checks authorized material proof and the source lifetime,
retrying a changed source once within ten metadata reads including the original
source read. List pages share target proof only within the page; a source retry
obtains fresh proof. Failure masks current conditions and resolved evidence
without rewriting stored observations. Durable coalesced hints request
revalidation without changing the authored resource.
Stored negative observations remain visible for an absent target only after
current certified absence and normal target-read authorization succeed.
Missing coverage and denied proof cannot masquerade as `TargetNotFound`.

Live watches may carry guarded status only for the matching current source
event. A changed source cannot replace an older event's body/version. Initial
replay and mismatched events remain historical and omit status. The controller
retains source CAS versions separately from journal cursors.

All five target kinds feed bounded durable invalidation handoffs. Independent
disk checkpoints retain fan-out, retry and replay obligations across controller
replacement. Invalidations and saved repair progress are hints, not positive
material authority.

Retryable `TRANSLATION_UNAVAILABLE` responses defer controller work without
consuming its poison retry budget, just like retryable authorization outages.
They do not authorize a validation observation or automatically replay an
uncertain mutation. Errors without an explicit true `retryable` flag retain
normal error handling. Previously quarantined items remain durable; requeue
them through the controller management API after resolving the underlying
failure.

The Translation checkpoint opts into durable pending-age accounting. Original
enqueue timestamps survive deferred retries, quarantine, coalesced changes and
replacement snapshots; acknowledged work starts a new age. An ordered index
and atomic committed statistics keep health and metrics independent of queue
size and checkpoint merge locks. Existing resource kinds retain their current
checkpoint-writing contract; all supported writers of an opted-in checkpoint
must maintain its age index.
Its persisted projection allowance is eight MiB so JSON envelope and escaping
expansion cannot reject an admitted one-MiB document. Translation checkpoint
read/write pages remain bounded to 32 MiB and 256 items; other kinds retain their
one-MiB projection and four-MiB page defaults. Reopening retains the checkpoint's
declared allowance rather than silently reverting to a smaller reader limit.

`gitstore_controller_oldest_pending_timestamp_seconds` is the oldest recorded
unfinished obligation, including delayed and quarantined work. It is zero when
no timestamp is known. Interpret it with
`gitstore_controller_pending_without_timestamp`: legacy obligations with missing
timestamps remain unknown through retries and relists, not artificially young.
`gitstore_controller_kind_ready` reflects sync, recovery, stall and poison
gates only; it does not certify credentials, feature activation, target validity
or deployment capability. All three use only the bounded resource-kind label.

`gitstore_controller_work_completion_lag_seconds` measures original enqueue to
durable acknowledgement, including queueing and retries, for timestamped
reconciliation work. `gitstore_controller_work_completions_total` separates
known and unknown ages. A successful attempt with a superseded lease, failed
checkpoint write or unavailable age observation cannot fabricate a completed
lag sample. This is asynchronous reconciliation/repair lag, not a claim that a
target is Ready or an index audit is certified. The existing datastore SDK
metrics separately measure bounded audit/repair operations and errors.

Translation maintenance reuses the bounded projection audit/repair commands.
Source/index traversal and target-proof verification have distinct progress
and coverage semantics. Preview is read-only; confirmed repair resumes one
bounded turn. Original Namespace birth authority and permanent UID retirement
must survive recovery. See the [operator guide](../runbooks/translations.md).

## Extending supported target lifecycles

ProductVariant is already a supported Translation target. Lifecycle work can
reuse its existing descriptor while the eligible owned text and reference
boundaries remain unchanged; it does not require localized application or a
new target kind.

New authoring, termination and final-removal paths must preserve the existing
fenced target-writer contract in both datastores, durable invalidation, immutable
UIDs and delete/recreate lifetime boundaries. Do not bypass those paths with
direct row writes or use controller status as target-source authority.
Exercise edit, termination and same-name replacement with existing Translation
bindings and guarded reads, including concurrent and stale writes. Preserve the
common four-field status envelope and materialized related-resource summaries.

These implementation dependencies are separate from deployed Translation
capacity certification. Standard correctness checks do not enable admission or
satisfy the pending activation, recovery and rollout gates.

## Handoff for additional target kinds

AttributeDefinition and custom kinds remain unsupported. Adding a descriptor
alone is insufficient: a kind needs its own accepted authored/schema contract,
authoritative source lifetime and ownership metadata, fenced material writers,
durable watch integration, bounded authorized snapshots, invalidation and
repair coverage, and replica/recovery tests.

Reuse the shared validator and existing snapshot/controller interfaces when
that kind ships. Do not introduce a generic dependency engine, implicit
reference traversal, localized projection or dynamic kind registration as a
side effect of Translation support.
