# Translation Acceptance, Capacity and Rollout

Everything below is required future evidence. No Translation capacity or
production pass is claimed by this planning change.

## Runner Reuse and Dataset

Extend existing selectors, not public feature-specific Make targets:

```sh
make capacity TARGET=namespace PROFILE=validation MODE=alpha
make capacity TARGET=api PROFILE=readiness MODE=alpha
```

The first is currently helper-process validation, not deployed ingestion proof.
The second is currently health-only unless an existing domain mode is selected;
implementation must add the connected Translation corpus/driver and document
its actual selector before using it as evidence. These commands do not already
exercise Translations. Keep existing workload modes independently usable.

Minimum Translation corpus: 1,000 named documents across ten owned namespaces,
at least two locales, all five delivered target kinds, field-only/body-only/
combined channels where eligible, near-limit documents and multiple named
documents with equivalent locales/targets. Include a target with >=1,000
dependents and concurrently changed targets, not only evenly distributed edges.
Keep separate valid, negative and deferred-target corpora.

Production-mode evidence additionally retains the 5,000,000-Product baseline
and all existing profile dataset/rate/full-duration gates. Seed only
explicitly owned additions, preserve baseline UIDs/Git refs/history, and
independently prove baseline identity/count/digest before and after the run
using bounded pages. The 1,000-Translation minimum is not a substitute for
catalog-scale background data.

## Measured Gates

| Boundary | Required envelope |
| --- | --- |
| Existing `namespace/validation` alpha | Unchanged: two helper processes, 10 requests/s, 20 workers, 500 files/request, <=50 Namespace manifests, >=30 minutes; p95 <=100ms/p99 <=250ms, internal errors <0.1%, zero incorrect decisions, recovery <=30s, CPU <80%, retained-memory growth <10%, goroutine drift <=5% |
| Translation validation extension | Add Translation cases within the same 500-file request budget, not instead of its original Namespace cases; preserve its complete window/gates and compare standalone/ingestion decisions |
| Connected Translation alpha | Two APIs/two controllers/one Git; >=30-minute offered window; 10 real candidate pushes/minute, <=20 changed Translation documents/push, <=2 concurrent push workers; 20 authenticated read/list requests/s total split across APIs; all five targets and both content channels exercised |
| Connected Translation production | Retain existing selected production profile duration/rate/dataset gates, with at least a 60-minute Translation window, 50 real candidate pushes/minute and 100 read/list requests/s total; at least five concurrent bounded push workers and unchanged owned deployment resource ceilings |
| Connected correctness/latency | Zero dropped scheduled operations, accepted-source loss, partial documents, unauthorized disclosure, false current Ready or target mutation; unexpected request errors <0.1%; read p95 <1s/p99 <3s; acknowledgement-to-visible and valid-to-Ready convergence p95 <=1s/p99 <=3s; acknowledged batch push p99 <=30s |
| Connected resources/recovery | CPU <80%, retained-memory growth <10%, goroutine drift <=5% per API/controller; remain under all configured CPU/memory/storage ceilings; fault recovery <=30s with >=5 seconds of sustained correct traffic |

These are Translation extensions/minima, not replacements for stricter existing
gates. Expected negative-corpus rejections are tagged separately, never
reclassified unexpected errors. Near-limit cases retain the same correctness
and bounded-work guarantees and receive separately reported latency, without
silently diluting normal-workload percentiles.

Retain the original watch profile windows too if watch-scale acceptance is
claimed: alpha is 10 minutes, 100 clients, 1,000 namespaces, five writers,
20 watchers, page 256/batch 20, visibility p95 <=2s/p99 <=3s and its existing
resource gates; production is 60 minutes, 1,000 clients, 10,000 namespaces,
20 writers, 50 watchers, page 1,000/batch 100, p95 <=1s/p99 <=3s. Translation
pages remain capped at 256 even when a generic profile requests a larger page.
A smaller connected Translation corpus is not this independent watch-scale
proof. Never replace the separate spec-066 RBAC gates with these envelopes.

The harness must preflight available disk/resources and explicit ownership,
not infer a user-imposed free-disk threshold. Only task-owned reproducible
caches/artifacts may be cleaned up under the applicable cleanup rules;
datasets, credentials, history and failed evidence are retained.

## Correctness Joins and Test-First Coverage

Persist protected source-attributed receipts for offered/completed push counts,
Git acknowledgements, file/blob digests, Translation UID/generation/revision,
canonical locale, patch/body digest, target evidence and both replicas'
observations. Verify exact UTF-8 body bytes including CRLF/blank lines,
delimiter-adjacent content and embedded delimiters. Independently compare
target authored/resolved digests; a successful HTTP status is not a verifier.

Each fault receipt must identify an already acknowledged measured commit and
an independently observed restoration time. Both API recovery reads must join
that commit's source UID, version, generation, revision and exact content
digests, report current Ready, and occur after restoration but before sustained
recovery traffic begins. A restoration boolean or correct-read count alone
cannot establish those joins. The protected native `trafficProof.recoveryReads`
array carries those two actual current observations, not copies of earlier
push or watch observations. Replacement resource epochs must have started
by the observed restoration time; later process evidence cannot retroactively
authorize earlier recovery reads.

Before implementation, add failing cases for every spec negative-matrix row
and these connected boundaries:

- Every delivered example; File rejects `spec.title`; current media/reference/
  selection strings remain ineligible. Deferred keyed/AttributeDefinition/
  custom-resource cases return explicit unsupported outcomes.
- Selector reorder, zero/multiple matches, duplicate/overlapping leaf paths,
  nested proposed key fixtures and extraction/retargeting walkthrough.
- Known/unknown well-formed BCP 47 tags, aliases/case comparison, private use,
  grandfathered/extension forms and malformed/duplicate subtag cases.
- Same-candidate target create/update/delete independent of file order;
  unchanged target source/resolved content across every Translation operation.
- Missing-to-resolved, invalid-to-valid, target mutation/deletion/replacement,
  stale UID/version pins, sticky binding through restart and pin toggling.
- Deterministic target-read/status-CAS race and every guard interleaving in
  the lifecycle contract, including all read/status/watch representations.
- Two APIs concurrently ingest duplicate/out-of-order callbacks and two
  controllers race status/binding CAS; derived-index repair cannot resurrect
  older content or lose accepted work.
- Authorized/denied human/service actors, namespace isolation, forbidden target
  existence-safe responses and no leaked target evidence through another route.
- Exact source/path/YAML/page/partition/history limits; cancellation, saturated
  queues, retry exhaustion, segment rollover/interrupted reservations and bounded
  high fan-out; interrupted per-target fence publication/recovery, including
  target writes from every existing ingress route.

## Fault and Rolling Matrix

Use existing owned `api-restart.json`, `controller-restart.json` and
`journal-interruption.json` runners. Physical process faults and simulated
callback transport failures require separate accurately labeled receipts.

| Scenario | Required assertion |
| --- | --- |
| Replace one API under sustained push/read load | Other API remains correct; source/UID/status converge on replacement; no lost accepted callback |
| Replace one controller, separate checkpoints | Other remains duplicate-safe; replacement resumes durable pending/fan-out work without overlapping checkpoint writers |
| Lose admission callback/reply after Git acknowledgement | Replay accepted source without author resubmission; duplicate delivery cannot advance an old revision |
| Interrupt after source CAS / before index or status completion | Durable intent repairs source/index/binding consistency; current guard fails closed until trustworthy |
| Interrupt shared journal consumption | Recovery resumes/re-lists under existing cursor rules; no process-local substitute or permanent missed invalidation |
| Target changes during validation/status publication | No stale current Ready; convergence meets normal/recovery gates |
| Rolling API/controller binaries before activation | Old binaries never receive an unsupported kind; incumbent behavior stays correct under overlap |
| Rolling new-capability replicas after activation | Duplicate-safe CAS and descriptor compatibility verified under sustained work |

Fault-free traffic and exercised-fault windows are reported separately without
weakening their original thresholds. Maintain at least five seconds of
successful domain traffic within recovery deadline; one health check is not
recovery. If a Git upgrade is exercised, stop the old process before starting
one replacement on retained storage, report downtime honestly and never claim
Git HA. A Git fault is not implied by API/controller fault coverage.

The public diagnostic verifier may join a journal-source pause nested inside a
declared simulated callback-loss window only when configuration independently
declares `services.translationPhysicalFaults` as
`{"journal-interruption":"physical"}`. The existing chaos runner's metadata
and sanitized before/during/after target captures must join the owned topology's
Scylla container ID, immutable image and unchanged process start. The during
capture must show a real pause; the after capture must show a live unpaused,
non-OOM process. Bounded injection/restoration times must precede forwarded
source replay, fresh recovery reads and five seconds of correct domain traffic.
Require the unchanged full-window API/controller resource gates. This does not
authorize resource gaps, API/controller replacement, or production acceptance.

Replacement-resource diagnostics require a separate physical API/controller
declaration and a bounded `services.translationResourceGaps` entry identifying
the role, owned container, fault and maximum gap seconds (at most 30).
Original scrapes independently identify each native process instance/start and
segment CPU counters, completed-GC heap and goroutine baselines per epoch.
Before/after target and surviving-peer captures join immutable images,
container lifetimes and runner timestamps; controller captures additionally
retain non-overlapping checkpoint identities. Never normalize a counter reset
inside an epoch or join a new process to the old process's baseline.

## Activation and Rollback

Focused migration, fence, binding and replay tests establish alpha PR
correctness. The complete deployed sequence below is production qualification,
not a prerequisite to merging every alpha implementation. Deferred qualification
does not authorize production activation or waive unsafe compatibility.

1. Ship disabled new-kind admission plus additive schemas/catalogs, store
   contracts and migration. New tables are additive; existing applied
   migrations and records remain untouched.
2. Migrate Scylla, verify authoritative CDC and name/UID identity behavior,
   backfill/audit thin indexes/bindings from accepted rows if any, and verify
   repair watermarks. Build/audit initial target fences under a mutation
   watermark and ensure every enabled target writer participates before Ready
   activation. Rehearse interrupted migration/repair/fence recovery.
3. Deploy capable API readers/writers and controllers with compatible
   descriptor digests; overlap with old binaries only while no Translation
   content can be admitted. Gate every writer, including callback replay, not
   merely the user-facing parser.
4. Drain old API/controller binaries from all relevant traffic/watch/replay
   roles. Prove topology/capability rather than relying on one healthy pod.
   A singleton Git upgrade, if needed, is non-overlapping.
5. After connected acceptance, enable Translation admission consistently on
   all writers. A mismatched activation/descriptor fails closed. Keep
   revalidation/index repair observable; do not announce production support
   until production-mode evidence passes.

Before activation, rollback may return to old binaries with new admission
still disabled only if those binaries accept the migrated ledger. A `database
is ahead` refusal requires retaining a migration-compatible disabled build,
not resetting schema/history. After any Translation is acknowledged, do not roll readers or
replay consumers back to binaries that reject/skip the kind: disable new
authoring if needed and retain the last capable build while forward-fixing.
Never delete accepted documents, reset the keyspace or edit Git history to
make rollback appear compatible. No legacy wire-format shim is required.

## Evidence and Documentation Completion

Retain private evidence through existing protected runner paths, including
failed and recovery bundles. Publish only redacted workload/topology/source/
configuration digests, actual counts, percentiles, resource maxima and faults
actually exercised. Debug/partial/helper-only runs cannot certify native
deployed or production behavior.

Before #191/parent completion, update canonical resource authoring examples,
validation reference and localization overview to the same selected contract.
Explicitly say "validation/resolution only; no localized application".
Contributor docs explain descriptors, binding, repair and guard semantics;
operator docs explain activation, missing/stale outcomes and safe rollback,
without experiment diaries or private runtime identifiers. Verify every
published example through the actual ingestion path and all documentation
support claims against code.

Run focused tests/generation/lint for each implementation slice and the full
default `make pr-ready` before creating an implementation PR. Capacity/chaos
evidence supplements, never replaces, that suite. This documentation-only
planning change requires artifact consistency checks, not service builds.
