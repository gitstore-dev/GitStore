# Implementation Plan: Canonical Authorization Vocabulary and rbac-local v1beta1

**Approved concurrency extension (2026-10-11):** Product, CategoryTaxonomy, Collection and ProductVariant updates require a caller-supplied expected resource version, checked atomically by both datastores. Successful updates advance the version. A stale authorization snapshot must never overwrite a concurrent owner change. Update all callers and cover stale and competing writes in the shared backend contract suite (T079–T081); do not add a compatibility overload.

**Branch**: `064-authz-action-vocabulary` | **Date**: 2026-10-10 | **Spec**: [spec.md](spec.md)
**Input**: `specs/064-authz-action-vocabulary/spec.md`
**Status**: Core implementation and non-capacity verification complete; capacity/deployed acceptance deferred to #451.

**Capacity follow-up (2026-10-11):** At the user's direction, remaining capacity
harness work, load/soak, deployed replica/replacement and chaos acceptance move
to [#451](https://github.com/gitstore-dev/GitStore/issues/451#issuecomment-6107662533).
Local tests and diagnostic timings do not pass those gates. The original
thresholds and singleton Git restriction remain binding; see tasks.md for IDs.

## Summary

Replace the flat v1 policy with a strict, allow-only v1beta1 reference provider
and one finite action catalog consumed by Go, Rust and operator tools. Move
ownership/transfer decisions into the authorizer; canonicalize enforcement
contexts and signed actions; make ServiceAccounts name-only identities with
replica-safe uniqueness; preserve default-deny in the replacement alpha baseline.

Reuse existing middleware, policy loader/editor, Git client, admission pipeline,
controller credentials, datastore backends and capacity runners. Production
policy engines, group sourcing, authorization directives, endpoint splitting,
visibility fields and legacy compatibility are outside this feature.

Research found that main already uses all four purge actions, but still has
inert partial wildcard grants, internal Git authorization bypasses, namespaced
SA persistence, missing rejection-driven controller credential refresh and Git
admission gaps for USER namespace invariants. These are explicit work items.
See [research.md](research.md) for evidence and resolutions of stale spec prose.

## Technical Context

**Language/Version**: Go 1.26.0 (current API/controller go.mod), Rust 1.x; existing shared/secretmaterial remains Go 1.25.0.
**Primary Dependencies**: Existing gqlgen v0.17.90, yaml.v3, jwt/v5, zap, Prometheus, memdb, gocqlx/gocql, Rust tonic/serde. The explicit 2026-10-11 replay-protection decision adds direct Rust HMAC/SHA-256/randomness dependencies; the subsequently approved dependency compatibility work retains `hmac 0.13`, `sha2 0.11`, `getrandom 0.4`, `gix 0.89` and `gix-pack 0.76`. Go uses its standard library. No new Go module or policy service.
**Storage**: YAML policy compiled into immutable bounded snapshots; existing memdb/Scylla with replacement cluster-ServiceAccount baseline tables; existing Git storage and durable watch journal.
**Testing**: Go unit/contract/integration and race tests, Rust cargo tests, shared JSON decision fixtures and generated-table parity, existing shell regressions and deployed make capacity/make chaos gates.
**Target Platform**: Linux service containers and existing native developer workflows.
**Project Type**: Three-service backend plus gitctl/operator configuration and shared schemas.
**Performance Goals**: Authorization p99 <1ms at 200 roles/1,000 bindings/2,000 rules/50 groups; no evaluator I/O; unchanged readiness/lifecycle gates; 30s same-policy API restart recovery.
**Constraints**: Only v1beta1; no deny rules/legacy actions or SA subjects; preserve AuthZProvider/ResourceContext signatures; absent facts, invalid policy and incompatible peers fail closed.
**Scale/Scope**: Eight core kinds plus explicit system/reserved domain vocabulary; API/Git/admission authorization surfaces; two APIs/two controllers and one Git service; 5,000,000-product production envelope remains a separate evidence gate.
**Replica/Scaling Model**: Identical policy digest/principal/context yields identical decisions. Conditional authoritative SA writes protect names/UIDs across APIs. Controllers coalesce credential renewal and retain durable list/watch recovery.

**Topology constraint**: Git is singleton-only with retained storage and
non-overlapping replacement. No Git replication, sharding or HA requirement.
API/controller replica safety is tested per changed path, not asserted globally.

**Authentication/Authorization**: Static users, OIDC, verified SA assertions/JWTs and anonymous principals normalize at the trusted boundary; no Principal roles. Scoped bindings/conditions determine access, including explicit internal system:api calls.
**Load/Backpressure Model**: Bounded policy bytes/counts/conditions/groups; indexed candidates; serialized reload builders and immutable reader snapshots; existing admission concurrency limits; bounded singleflight renewal without ambiguous mutation replay.
**Capacity Profile**: Extend tests/capacity/profiles/api-readiness.js and existing repository lifecycle integration/harness with semantic probes and sampled latency; contracts/validation.md declares load, duration, payload/evidence and production limits.
**Fault Profile**: Existing tests/chaos/profiles/api-restart.json; malformed reload under load; two-API SA write/projection faults; rejected-token races and legacy-input rejection tests; singleton Git retained-storage replacement.

Go versions above follow module manifests rather than the older 1.25 baseline
prose in AGENTS.md; this plan does not propose a toolchain upgrade.

## Constitution Check

Updated scope (2026-10-11): the user explicitly confirmed that the namespaced
ServiceAccount baseline has no released deployments. Replace its definitions
directly in migration 009, without legacy tables or migration/rollback machinery.
The same-version replica, isolation and capacity gates remain mandatory; this
does not change the prohibition on rewriting deployed migration history.

| Gate                    | Pre-design finding / resolution                                                                  | Post-design result                                                                                                                              |
|-------------------------|--------------------------------------------------------------------------------------------------|-------------------------------------------------------------------------------------------------------------------------------------------------|
| I Test-first            | Failing tests precede each story, identity and persistence change                                | PASS: validation matrix covers failure, concurrency and recovery                                                                                |
| II Contract-first       | Freeze provider signature; define policy, signed/proto, identity and CLI changes                 | PASS: reviewed contracts and machine-readable seeds                                                                                             |
| III Versioning | No released ServiceAccount data contract to migrate; direct alpha baseline replacement explicitly authorized | Document the schema/API break and fresh-schema workflow; no release-version or legacy rollback blocker for this planning scope |
| IV Observability        | Bounded reasons, policy/reload/renewal diagnostics                                               | PASS: digest, latency and sanitized evidence contract                                                                                           |
| V User stories          | Four stories with independent fixture/flow acceptance                                            | PASS: Phase 2 preserves story labels and dependencies                                                                                           |
| VI Independent delivery | No deployed old/new fleet needs a compatibility window | Explicit alpha scope: test same-version API/controller rolling replacement and reject legacy inputs; no old-fleet cutover requirement |
| VII Simplicity          | Reuse services/runners; no new evaluator framework                                               | PASS: generated registry/fixtures and SA tables justified                                                                                       |
| VIII Replica safety     | Same-document decisions; SA LWT/UID checks; controller refresh races                             | PASS design, runtime UNVERIFIED: two-API/two-controller tests required; Git singleton                                                           |
| IX Multi-user security  | Touched routes, providers, internal/callback boundaries                                          | PASS design: default deny, identity trust, scopes and I/O-free conditions                                                                       |
| X Capacity              | Bounded catalog-independent evaluation and sustained affected-path load                          | PASS design, runtime UNVERIFIED: p99 distribution, alpha gates and separate 5M envelope                                                         |
| Repeatable evidence     | Existing make capacity/make chaos and domain verifiers                                           | PASS: no authz-specific public capacity target or Compose topology                                                                              |

Post-design: no unresolved technical research questions or additional approval
requirement for baseline replacement. The user's 2026-10-11 clarification
supersedes the earlier legacy-cutover/release-readiness issue. This is not a
claim of cross-version compatibility. PASS describes design obligations, not
executed tests; production readiness for rbac-local is not claimed and
non-negotiable principles remain in force.

## Project Structure

### Documentation (this feature)

```text
specs/064-authz-action-vocabulary/
  spec.md                         # existing, unchanged by planning
  plan.md
  research.md
  data-model.md
  quickstart.md
  contracts/
    action-vocabulary.json        # machine-readable catalog seed
    decision-table.json           # provider-neutral conformance seed
    rbac-policy.md                # grammar, evaluation, limits, tools
    service-boundaries.md         # signed context, storage and rollout
    validation.md                 # tests and deployed evidence
  tasks.md                        # generated later, not by this command
```

### Source Code (implementation destinations)

```text
shared/
  schemas/authorization-actions.json             # new authoritative catalog
  schemas/{auth,serviceaccount,namespace}.graphqls
  proto/gitstore/{git,catalog}/v1/*.proto
gitstore-api/
  generate.go, cmd/gitctl/
  internal/auth/{types,registry,logging}.go
  internal/auth/provider/{rbaclocal,serviceaccountjwt,serviceaccountassertion}/
  internal/middleware/security/
  internal/gitclient/, internal/cataloggrpc/
  internal/graph/resolver/, internal/namespace/
  internal/datastore/{memdb,scylla}/
  internal/datastore/scylla/migrations/009_service_account.cql
  internal/app/{server,bootstrap}.go, internal/config/
gitstore-controller-manager/internal/{config,graphqlclient,listwatch,status}/
gitstore-git-service/src/{auth,grpc,git/hooks}/
tests/{fixtures,integration,capacity,chaos}/
scripts/{run-make-workflow,run-capacity,repository-capacity-stack}.sh
config/{policy.yaml,config.toml}, compose*.yml, Makefile
docs/{ADRs,implementation,runbooks}/
AGENTS.md, CLAUDE.md -> AGENTS.md
```

**Structure Decision**: Extend existing files/packages first. New files are
justified for the shared vocabulary, generated language tables, neutral
fixtures; replace the existing ServiceAccount baseline migration in place.
No authz Go module, additional SA migration or policy service.

## Phase 0 — Research completed

Resolved catalog ownership/generation, grammar/indexes/bounds, transfer and
denial semantics, identity trust/storage, controller recovery, admission and
evidence. [research.md](research.md) records alternatives. Reject
defaultDeny:false; FR-016 governs concealment; when is exclusively a list. Empty
policy is valid deny-all, but configured active local users require satisfiable
grants.

## Phase 1 — Design and contracts completed

The [data model](data-model.md), [policy](contracts/rbac-policy.md),
[boundaries/rollout](contracts/service-boundaries.md),
[validation](contracts/validation.md), catalog and decision fixtures define
implementation inputs. [quickstart.md](quickstart.md) distinguishes planned
commands from current behavior. Update agent context to this plan without
accepting ADR 0010 before implementation.

## Phase 2 — Sequence for task generation

Each slice starts with demonstrated failing tests, implements the minimum
change and reruns focused tests. Wire/policy/SA changes use one coherent alpha
baseline after required slices pass; no existing deployment migration is needed.

1. **Foundational contracts/baseline:** Promote catalog/neutral fixtures; record
   actual admin/controller/internal/anonymous outcomes; inventory protected
   fields, Git methods and catalog transitions. Add generation/drift and failing
   strict-policy/action-recorder/signed-wire tests. Extend root commands with
   generic `make generate TARGET=authz` (and all selector), documented in
   AGENTS.md, reusing gitctl/generate.go.
2. **US1 reference provider:** Replace v1 parsing, compile bounded indexes,
   scoped allow-union, conditions, transfer semantics and diagnostics; atomic
   reload and active-local-user effective-permission validation. Run neutral
   table, malformed/oversized inputs and concurrent reload/race tests.
3. **Identity/storage foundation for US2/US3:** Normalize AuthN; remove roles;
   migrate SA stores/token/GraphQL/enrollment and controller config to name-only.
   Replace namespaced table definitions in Scylla migration 009, update memdb
   uniqueness and add fresh-schema/two-API repair/fault tests.
   Add controller token-generation invalidation/renewal using existing
   singleflight/backoff. Regenerate schema/protobuf clients; no legacy fallback.
4. **US2 canonical enforcement:** Migrate middleware, typed/generic watches,
   status and Git HTTP; preserve existing purges. Add missing checks on touched
   namespace and node/nodes routes. Inject live registry into Git client/catalog
   server, reorder bootstrap and explicitly authorize internal backing work.
   Migrate Rust method checks and carry trusted actor facts through hooks and
   reverse callbacks.
5. **US3 domain/condition behavior:** Remove caller ownership decisions, retain
   concurrency guards; normalize target facts and map concealment from resource
   facts. Enforce USER owner/name/immutability at GraphQL, Git preflight and CAS
   admission; initialize the fresh default bootstrap as ORGANIZATION. Verify all owner kinds,
   unconditional transfer, management reads and missing-visibility/no-grant
   cases through real boundaries on both APIs.
6. **US4 tools/config:** Extend YAML-aware role/binding helpers and Make selectors;
   reject DENY/removed SA namespace config; exact action projection without
   overgrant. Ship built-ins and narrow controller/internal policy. Migrate
   config/Compose/bootstrap/secret/capacity fixtures and docs together; verify
   baseline flows plus explicitly new self-service grants.
7. **Operational verification:** Run p99/soak, existing readiness/lifecycle alpha
   gates with semantic probes, malformed reload, replica replacement and
   legacy-input rejection. Document production limits and the fresh-schema
   development workflow; no old-schema upgrade or rollback harness.
   Run make pr-ready before an implementation PR; graphify update . after code.
8. **ADR/spec closure in implementing PR:** Amend ADR-0010 sections 6, 8–14 and
   examples for allow-only semantics, condition lists, canonical pairs, trusted
   identities, cluster SAs, no compatibility shim, built-ins and completed
   authorizer ownership. Flip Status to `Accepted (YYYY-MM-DD)` using
   docs/ADRs/README.md's dating rule and update its index in the same PR.
   Update docs/ and close the spec only when implementation/evidence is complete.
   Flag gitstore-dev/quickstart follow-up for policy/config/Compose/enrollment
   drift.

Tasks must retain FR/PR/SC and story traceability, including purge regressions,
internal authorization, neutral conformance, identity/namespace rules, atomic
reload and PR-001–006 evidence.

## Complexity Tracking

| Constraint / tradeoff                                                             | Reason and risk                                                                                                                                                      | Rejected alternative / resolution                                                                                                                                                                                                                  |
|-----------------------------------------------------------------------------------|----------------------------------------------------------------------------------------------------------------------------------------------------------------------|----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| Direct replacement of the undeployed alpha baseline | No released deployments or ServiceAccount data contract to preserve, per 2026-10-11 clarification | Replace migration 009 definitions in place; no legacy tables, compatibility layer, backfill, old-fleet cutover or rollback artifact |
| Cluster-SA authority and projections | Global name/UID uniqueness and bounded lookup/listing are required | Replace namespaced definitions and paths; retain LWT/UID guards and bounded repair, not legacy storage |
| Shared catalog and neutral fixtures                                               | Cross-language/provider parity must be reviewable                                                                                                                    | Independent tables drift; new service/module adds no needed capability                                                                                                                                                                             |

No constitution amendment is performed. Non-negotiable runtime gates remain
mandatory before their associated readiness claims.

### Planning artifact validation

On 2026-10-10: parsed both JSON contracts; checked all 82 unique catalog entries
against their resource/verb projections; checked fixture IDs, referenced policies
and mutation pointers; walked all 55 decision cases against the documented
semantics with no mismatches. The 28 policy-validation fixtures are design
inputs, not passing tests against the still-v1 implementation. Checked links in
all seven new Markdown documents and ran git diff --check. The Spec Kit
prerequisite script recognizes research, data-model, contracts and quickstart.
Agent context points to this plan through the existing CLAUDE.md → AGENTS.md
symlink. No application tests, capacity runs or feature implementation were
performed during planning.

### Scope clarification applied 2026-10-11

The earlier ALPHA-064-001 cutover/release issue is superseded by the user's
explicit confirmation: there are no deployments of the namespaced ServiceAccount
baseline to preserve. Task generation must implement direct replacement in 009,
fresh-schema initialization and name-only enrollment, without restoring legacy
table retention, backfill or compatibility tasks. Existing developer databases
are not silently reset; same-version recovery and legacy-input rejection remain
test obligations.
