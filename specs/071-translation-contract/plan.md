# Implementation Plan: Translation Frontmatter Support

**Branch**: `071-translation-contract` | **Date**: 2026-11-18 | **Spec**: [spec.md](spec.md)
**Input**: Reviewed specification covering #78 and #188-#191, including #492's related-resource design.
**Delivery state**: Alpha implementation in final PR checks; admission remains disabled by default and production qualification is deferred.

## Summary

Add a namespace-scoped, Git-authored Translation resource to the existing Go
validation/admission, typed datastore, Admin GraphQL and controller lifecycle.
Select `i18n.gitstore.dev/v1beta1` with `locale`, `targetRef`, ordered
`patches[{fieldPath, translation}]` and a byte-preserved Markdown body after
evaluating, rather than assuming, #78's proposal. No legacy format is retained.

Translations validate independently and never modify their targets. A small
versioned target descriptor declares eligible text and stable list identities.
Initially support Product, ProductVariant, CategoryTaxonomy, Collection and
File; explicitly defer AttributeDefinition and custom kinds until their own
contracts ship. Durable reverse dependencies and reconciliation materialize
validation evidence. A bounded service-layer metadata guard prevents stale
evidence from being advertised as current Ready; a Translation-only CAS is
not misrepresented as a cross-resource transaction.

Retain #40's common Resource status baseline: `conditions`,
`observedGeneration`, `lastAppliedRevision` and kind-specific `resolved`.
`lastAppliedRevision` tracks the Translation's own reconciled source revision,
not application of localized content to its target. No replacement
`observedRevision` field is introduced.

Preserve #549's enum boundaries: authored enums use UpperCamelCase and GraphQL
enums use explicit SCREAMING_SNAKE_CASE projection. Reuse existing condition
converters and verify typed/generic reads and watch delivery without case-folding
locales, identifiers or translated content. Expose only the four common status
fields; freshness checks mask conditions rather than adding public status fields
or a duplicate historical observation envelope.

## Technical Context

**Language/Version**: Go 1.27.2; Rust edition 2021, minimum 1.99.1.
**Primary Dependencies**: Existing gqlgen v0.17.90, YAML v3.0.1, validator v10.30.3, frontmatter v0.2.0, gocql v1.17.0, gocqlx v3.0.4, Scylla CDC v1.2.1 with repository replacement, shared observability, generic controller/listwatch/checkpoint packages. The repository-local `shared/translation` module directly pins existing x/text v0.43.0 for pure locale validation.
**Storage**: Authoritative bare Git; memdb contract/development backend; Scylla authoritative resource and query-first indexes; independent disk-v2 controller checkpoints.
**Testing**: Go contract/race/integration suites, Rust hook forwarding tests, generated schema/catalog drift checks, existing capacity and chaos runners.
**Target Platform**: Existing Linux native/container deployments; local development follows existing macOS tooling.
**Project Type**: Cross-service catalog resource in the existing monorepo; no new service, broker or UI.
**Performance Goals**: Preserve existing profile gates; validation p95 <=100ms/p99 <=250ms at 10 requests/s for 30 minutes; deployed reads p95 <1s/p99 <3s, visibility p95 <=1s/p99 <=3s and recovery <=30s for the Translation acceptance extension.
**Constraints**: Exact valid UTF-8 body bytes; no target mutation, live resolver aggregates, implicit reference traversal, positional paths or silent UID rebinding. All work and physical partitions bounded as specified in the contracts.
**Scale/Scope**: At least 1,000 Translations, ten owned namespaces, two locales, all five delivered targets and both content channels; production evidence retains the constitutional 5,000,000-Product baseline.
**Replica/Scaling Model**: Two APIs and two controllers must be tested. Authoritative CAS, durable watch journal, persisted target bindings and recoverable index maintenance are shared. Each controller has its own checkpoint directory; duplicate work is safe. Git has exactly one active process, retained storage and non-overlapping replacement; no sharding, autoscaling or Git HA is introduced.
**Authentication/Authorization**: Existing pluggable providers; generated `translation.*` action family, namespace/repository write ownership, target-kind read authorization, controller-only status authority and read-side redaction.
**Load/Backpressure Model**: Existing candidate/request limits, worker pools, deadlines and retry policy remain authoritative. Additional Translation ceilings are 1 MiB/document, 256 patches, 16 path segments, eight selectors and bounded diagnostics. Point reads, indexed fan-out pages and durable continuations replace catalog scans.
**Capacity Profile**: The Translation corpus extends existing `namespace/validation`; `api/readiness` has an opt-in connected Translation diagnostic driver. The latter explicitly rejects alpha/production selection. Exact gates, source-attributed evidence and uncompleted sustained/production acceptance are in [capacity-and-rollout.md](contracts/capacity-and-rollout.md).
**Fault Profile**: Reuse `tests/chaos/profiles/api-restart.json`, `controller-restart.json` and `journal-interruption.json`; add callback-loss and post-acknowledgement interruption assertions to existing owned drivers, not a feature-specific stack.

## Constitution Check

| Gate | Before research | After design / implementation obligation |
| --- | --- | --- |
| Test-first | Pass: five stories and negative matrix identify observable outcomes | Write failing parser, store, guard, replica and recovery tests before their implementations |
| API/contract-first | Pass: explicit design phase before code | [Authored contract](contracts/authored-resource.md) and [lifecycle contract](contracts/lifecycle-and-storage.md) precede schemas/handlers |
| Core-service boundary | Pass: Go semantics, Rust byte forwarding, controller resolution | No independent Rust Translation parser or new per-kind protobuf |
| Replica safety | Pass as a requirement, not a current capability claim | CAS, per-target evidence fences, durable bindings/index repair, separate checkpoints, mixed-version activation gate and race/recovery matrix |
| Multi-user security | Pass: existing provider boundary retained | Generated actions, target-read checks and existence-safe errors; every new route covered |
| Production capacity | Pass: fixed minimum corpus and existing envelope retained | Full production baseline and sustained push evidence required before production-ready claims |
| Repeatable evidence | Pass: canonical capacity/chaos families selected | Offered/completed joins, body/target digests, physical fault receipts and attribution required |
| Bounded work | Pass: no global target scan | Document/path/page/partition/fan-out/read-guard ceilings are normative contracts |
| Observability | Pass: reuse shared SDK instrumentation | Bounded reason labels; queues, retries, conflicts, guard failures, lag and recovery exposed |
| Incremental delivery | Pass: activation separates deployment from new-kind admission | Old unsupported binaries drained before activation; post-activation rollback retains capable readers/writers |
| Simplicity | Pass: typed resource and small descriptors | One dependency edge per Translation today; no generic dependency engine or localized projection |

No constitution exception is requested. These checks approve the design, not
unexecuted production acceptance.

## Current Completion Priority

The current delivery scope prioritizes correctness, executable documentation,
generation, the full default `make pr-ready` suite and safe retained-data
recovery. Capacity failures are acceptable for this scope: record them honestly,
do not tune throughput or resource settings to make them pass, and do not make
production capacity certification a blocker for dependent ProductVariant work.
The original performance requirements remain future qualification gates, not
waived or passing results. Correctness failures, unsafe rollouts and source or
UID loss remain blockers; bounded work and ownership safeguards still apply.
Deferred capacity and full deployed qualification stay explicitly incomplete,
but do not block alpha implementation/PR readiness under constitution 4.0.0.
Focused migration, replay, fence, binding and concurrent-replica correctness
tests remain required; known unsafe rollouts or data loss are not waived.

The interrupted additive-migration rehearsal found and fixed the DDL-before-
ledger crash boundary. Normal locked migration startup now conditionally
recovers unfinished simple column additions only after unchanged-history,
checksum and exact schema checks; read-only validation stays non-mutating.
Isolated native Scylla tests cover this boundary and interrupted target repair.
The finite owned RF3 diagnostic also verifies old-controller drain, saved
between-turn repair recovery after an API restart, consistent diagnostic
enablement, all-five-target ingestion and capable rollback. The reusable
`TestTranslationNativeCapableRollback` checks live source/snapshot/binding/
target/ref equality and retained process/storage lifetimes independently of
operator success flags. This is not a complete live mixed-API or populated RF3
interrupted-backfill rehearsal; those remain deployed qualification, not alpha
merge prerequisites.

The public diagnostic oracle now supports independently declared API/controller
replacement epochs and bounded original-scrape gaps, with per-process resource
baselines, immutable target/unchanged peer joins and retained independent
controller checkpoints. Local synthetic tests are not deployed acceptance.
The automatic connected companion still schedules simulated transport faults
only and does not assemble a combined physical/resource receipt.

## Project Structure

### Documentation (this feature)

```text
specs/071-translation-contract/
  spec.md
  plan.md
  research.md
  data-model.md
  quickstart.md
  checklists/requirements.md
  contracts/
    authored-resource.md
    lifecycle-and-storage.md
    capacity-and-rollout.md
```

`tasks.md` tracks implementation and acceptance separately.
The [implementation boundaries](../../docs/development/translation-contract.md)
and [operator safety guide](../../docs/runbooks/translations.md) describe the
delivered components and disabled-runtime restrictions; neither certifies an
enabled deployment.

### Source Code (repository root; planned changes)

```text
gitstore-api/internal/
  catalog/                   Translation envelope and observed-status model
  validate/                  strict Translation decoding and shared validation adapter
  cataloggrpc/               shared validation/admission dispatch and replay
  datastore/                 typed store contracts and entities
    memdb/                   equivalent CAS/index/watch behavior
    scylla/                  append-only migration, indexes, CDC and repair
  admin/graph/resolver/       thin Translation routes into resource services
gitstore-controller-manager/internal/
  translation/               new resource-specific reconciler and target watches
  checkpoint/                reuse disk-v2; extend only for proven missing primitives
shared/schemas/admin/        Translation, Node, list and generic-status registration
shared/translation/          pure locale/path/descriptor/field validation used by both Go services
gitstore-git-service/src/git/hooks/
                             existing raw-file forwarding, parity fixtures
tests/integration/           connected ingestion, replicas, recovery and domain driver
tests/capacity/               existing profile/corpus/verifier extensions
docs/                        resource guide, validation and localization support matrix
```

**Structure Decision**: Follow the existing typed File/category resource
pattern, but do not copy incomplete File-reference resolution or process-local
category reverse maps. Use the Product query-first storage pattern for large
resource populations. Retain the generic raw-blob RPC.

**Approved implementation refinement**: Put canonical pure validation in the
repository-local `shared/translation` Go module, not an API `internal` package
or a new authenticated validation operation. API and controller import the
same locale, path and descriptor implementation; callers retain authorization,
bounded target snapshots, I/O, persistence and checkpoint ownership. Wire the
module into workspace, aggregate checks, independent `GOWORK=off` CI and each
consuming service's local replacements and container builder.

## Phase 0: Research Decisions

[research.md](research.md) records the chosen envelope, strict parsing/body
boundary, locale policy, explicit descriptors, observed-versus-current status,
durable dependencies and related-resource handoffs. No unresolved design
placeholder remains. Implementation measurements may tune bounded defaults
without weakening acceptance or expanding target eligibility.

## Phase 1: Design

- [data-model.md](data-model.md): authored resource, persisted binding,
  observations, indexes and transitions.
- [contracts/authored-resource.md](contracts/authored-resource.md): selected
  envelope, grammar, eligible fields, limits and diagnostics.
- [contracts/lifecycle-and-storage.md](contracts/lifecycle-and-storage.md):
  API/store/watch/status interfaces, authorization and freshness.
- [contracts/capacity-and-rollout.md](contracts/capacity-and-rollout.md):
  acceptance gates, fault coverage, evidence and activation/rollback.
- [quickstart.md](quickstart.md): prospective authoring and verification walkthrough.

The root agent-context marker points here through canonical `AGENTS.md`;
`CLAUDE.md` and `GEMINI.md` remain symlinks.

## Phase 2: Implementation Sequencing

These are delivery slices, not generated tasks or claims of completion.

1. **Contract baseline (#188)**: Commit failing authored-format, target-matrix,
   authorization and status/read-guard contracts. Add typed models, strict
   decoding and generated GraphQL/action definitions behind disabled admission.
2. **Content addressing (#189)**: Implement bounded grammar/descriptors and
   exact body extraction with invalid/ambiguous/overlapping-path tests.
   Work can proceed alongside slice 3 after the shared contract baseline.
3. **Locale and lifecycle (#190)**: Implement locale comparison, candidate
   target checks, typed stores/migration/CDC, sticky identity bindings,
   freshness guard and per-target write fences for all five target kinds,
   durable fan-out/repair and controllers. Storage and
   read-guard prerequisites must land before current Ready can be exposed.
4. **Connected support (#191)**: Extend both API paths and Rust forwarding
   fixtures; test replay/lost acknowledgement, concurrency, all target kinds,
   every published example and unchanged targets. Update user/operator/
   contributor documentation together with the actual support boundary.
5. **Acceptance and activation**: Extend existing capacity/fault drivers;
   satisfy unchanged gates, upgrade and repair stores, upgrade/drain replicas,
   then activate admission. Do not close parent/children on schema-only delivery.

ADRs 0014 and 0016 remain Proposed: their release/custom-resource cores are
not implemented here. Neither Translation validation nor duplicate-safe CAS
implements ADR 0018's platform-wide leases/fencing. Body preservation does not
implement ADR 0013's IR, and existing Collection/File gaps do not implement
ADRs 0007/0008. No ADR acceptance flip is justified by this plan; if later tasks
actually implement a proposed core decision, that implementing slice must
explicitly update its status and the ADR index under the dating rules.

## Requirement Traceability

| Requirements | Design / verification owner |
| --- | --- |
| FR-001, FR-002, FR-003, FR-004, FR-005 | Authored contract: envelope, metadata, required values, strict decoding; parser/admission parity |
| FR-006, FR-007, FR-008, FR-009 | Authored contract: grammar, descriptors, conflict rules and body byte fixtures |
| FR-010, FR-011, FR-012, FR-013 | Research locale decision and authored target/reference matrix |
| FR-014, FR-015, FR-016, FR-017 | Lifecycle contract: candidate view, pins/binding, evidence/current guard and independent documents |
| FR-018, FR-019, FR-020, FR-021 | Authored diagnostics; lifecycle replay; capacity correctness joins and documentation/example checks |
| FR-022, FR-023 | Research related-resource decisions and data-model ownership/extraction walkthrough |
| PR-001, PR-002 | Lifecycle auth/CAS and capacity replica/rolling matrix |
| PR-003, PR-004, PR-005 | Capacity corpus/gates and authored/storage/work bounds |
| PR-006, PR-007 | Capacity physical-fault, recovery and retained-data contracts |
| SC-001, SC-002, SC-003 | All delivered examples, negative matrix, stable-selector permutations and target digests |
| SC-004, SC-005 | Deterministic guard interleavings and connected two-replica/recovery assertions |
| SC-006, SC-007, SC-008 | Full evidence verifier, documentation parity and related-resource design fixtures |

## Complexity Tracking

No gate violation. Durable reverse indexes are needed to wake unresolved
references without scans; sticky bindings prevent UID replacement from silently
rebinding; per-target write fences establish authoritative incarnation evidence;
the bounded metadata guard closes the specific read-then-CAS freshness gap.
Segmented thin indexes keep physical partitions bounded without capping the
whole catalog. None is a new generic framework or proof of cross-resource
transactionality.
