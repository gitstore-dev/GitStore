# Implementation Plan: Deployment Discovery and Self Access

**Branch**: `codex/072-capabilities-discovery` | **Date**: 2026-11-20 | **Spec**: [spec.md](spec.md)

**Input**: `specs/072-capabilities-discovery/spec.md`; issues #307 and #472.

## Summary

Add public `GET /.well-known/gitstore` on the existing Admin listener and additive authenticated Admin GraphQL `me`, self-access reviews, deployment diagnostics and configuration inspection. Public discovery is independent of authentication/authorization readiness. Do not construct the Admin schema on the Storefront listener.

Build one typed public snapshot from explicit published URLs, a configured fleet capability floor, validated provider metadata and component-owned registrations. Configure interactive client profiles separately from the OIDC verifier's audience fallback. Pre-registration is the baseline; optional RFC 7591 and Client ID Metadata Document descriptors require verified support. RFC 8707 identifies a separate protected-resource target. GitStore executes no client registration or delegated login exchange.

Reuse canonical authorization actions, authoritative validation captures, existing decision evidence under self-review status.resolved, error diagnostic extensions and extensions.admission. All new top-level GraphQL objects carry apiVersion/kind/metadata. Body fields are either spec/status or direct fields when neither is defined; new status types follow #40 shared Condition/resolved conventions. Preserve arbitrary-subject accessReview unchanged. No runtime code or deployed qualification is delivered by this planning change.

## Technical Context

**Language/Version**: Go 1.27.2 (`gitstore-api/go.mod`); existing JavaScript k6/shell tooling; no Rust changes.

**Primary Dependencies**: Existing gqlgen 0.17.90, Gin 1.12.0, go-oidc 3.21.0, oauth2 0.37.0, Viper 1.21.0, validator 10.30.3, OPA 1.22.2, gqlparser, Zap and shared observability; no new runtime dependency.

**Storage**: Operator publication config and bounded replaceable metadata cache; existing memdb/Scylla resources/RBAC capture stores. No new datastore or secret cache.

**Testing**: Go contracts/unit tests, gqlparser SDL validation, backend-neutral integration, owned Scylla two-replica acceptance and existing endpoint-split connected oracle.

**Target Platform**: Existing Linux API containers and local Go runtime.

**Project Type**: Existing API service/shared schemas.

**Performance Goals**: Discovery p95 <=250 ms/p99 <=1 s; review p95 <=1 s/p99 <=2 s; unexpected errors <1% outside injected faults, preserving stricter existing gates.

**Constraints**: <=16 providers, <=16 endpoints, <=32 client profiles total, <=32 checks per one-scope batch; <=256 KiB public document/provider fetch; no request-time remote metadata fetch or catalogue scan.

**Scale/Scope**: Retained >=5,000,000 products and >=10,000 categories; 100 discovery reads/s and 20 review requests/s for 30 minutes with existing connected authoring/push/watch load; five minutes at double new rates, 30 seconds overload recovery.

**Replica/Scaling Model**: Two APIs/two existing controllers in connected evidence; common publication floor and staged operator activation. Provider caches are observations, not policy/publication authority. Git remains singleton-only, retained storage and non-overlapping replacement; no HA/autoscaling scope.

**Authentication/Authorization**: Anonymous public discovery without implicit credential processing; normal Admin authentication for `me`. An optional self-review provider interface promises authoritative evidence or unsupported. RBAC uses trusted enriched identity and validation-start `Authorize`. Add cluster actions `deployment.diagnostics.read` and `deployment.configuration.read` with explicit grants, never implicit `iam.manage` reuse.

**Load/Backpressure Model**: Existing Admin admission budgets; separate public discovery budget (128 in flight, 200 requests/s, burst 400 per replica, existing bounded limiter-key policy). Sequential <=32-check batches share a ten-second deadline. Metadata: two workers total, one queued/in-flight item/provider, three-second fetch, zero redirects, refresh every five minutes with <=30-second jitter, validity 15 minutes, bounded retry delays 5/15/30/60 seconds then normal refresh.

**Capacity Profile**: Extend `api/endpoint-split` through the existing Make interface; new probes are implementation work. See [operations contract](contracts/operations.md).

**Fault Profile**: Existing owned `api-restart` and `identity-restart`, retaining their stricter 30-second recovery gates; controlled metadata timeout/expiry fixture with <=60-second recovery after dependency readiness.

## Constitution Check

Pre-research: **PASS for planning**. Post-design: **PASS for design**. Neither is an implementation or production-evidence pass; no waiver is requested.

| Gate | Design and required evidence |
| --- | --- |
| Test-first | Contract, security, concurrency and fault tests fail before each runtime slice. |
| API/contract-first | Public HTTP, additive SDL, error/configuration, rollout and load contracts in `contracts/`; no new RPC/persisted contract. |
| Core-service boundary | API changes only; controllers and singleton Git participate in connected evidence. Optional clients use API/front door. |
| Replica safety | Fleet floor, staged activation, readiness, mixed binary/configuration cases and two direct API probes. Runtime fleet membership is not inferred. |
| Multi-user security | Trusted identity, one-scope capture, selector-only context, explicit insufficient-context/provider states, redaction/no-store. |
| Production capacity | Full retained baseline and sustained connected workload; no catalogue scan/live related-resource aggregates. |
| Repeatable evidence | Existing Make selectors and source/config/topology/dataset receipts plus domain verifier. Diagnostics are not acceptance. |
| Bounded work | Numeric bounds and overload/resource gates in operations contract. |
| Observability | Shared SDK instruments: request class/outcome, refresh/cache state, active work, floor mismatch and recovery; finite labels and no subject/URL/token labels. |
| Incremental delivery | Additive schema; code rollout before advertisements; independent clients; no Git replacement required. |
| Simplicity | One public transport, no DCR executor/distributed discovery store/new service. Optional AuthZ interface avoids falsely claiming every provider supports reviews. |

Unverified baseline paths remain explicit: Storefront is a placeholder, the reference OIDC profile is confidential authorization-code only, generic AuthZ lacks authoritative self-review, and no membership coordinator exists. New features stay unavailable until their contracts/tests are implemented.

## Project Structure

### Documentation (this feature)

```text
specs/072-capabilities-discovery/
  spec.md, plan.md, research.md, data-model.md, quickstart.md
  checklists/requirements.md
  contracts/discovery.md
  contracts/admin.graphqls
  contracts/authorization.md
  contracts/operations.md
```

### Source Code (repository root)

```text
gitstore-api/internal/
  config/                  # explicit publication and interactive profiles
  discovery/               # proposed typed snapshot/registry/fetcher/HTTP handler
  auth/                    # optional review interface, adapters and generated actions
  admin/graph/resolver/     # me, self-review and protected views
  app/                     # independent public route, lifecycle and readiness
  health/                  # reuse bounded health functions, no HTTP-to-self
shared/schemas/
  admin/                   # additive authenticated SDL
  authorization-actions.json
tests/integration/         # provider/backend/replica contracts
tests/capacity/            # extend endpoint-split family
docs/                      # update operator/client/auth docs with implementation
```

**Structure Decision**: A cohesive API-internal discovery package; existing owners retain config/auth/health/telemetry/test responsibilities. No new service, shared runtime module, database or migration.

## Phase 0 — Research

Complete in [research.md](research.md): bootstrap/cache/proxy authority, client alternatives, device/resource prerequisites, provider lifecycle, fleet rollout, self-review/provider gaps and diagnostics authorization. No unresolved clarification remains.

## Phase 1 — Design and Contracts

Complete in [data-model.md](data-model.md) and `contracts/`. [Quickstart](quickstart.md) distinguishes proposed post-implementation procedures from current checks. Agent context references this plan through canonical `AGENTS.md`, preserving `CLAUDE.md`/`GEMINI.md` symlinks. For older Spec Kit scripts rejecting `codex/`, use `SPECIFY_FEATURE=072-capabilities-discovery`; do not rename the branch or change tooling here.

Design validation must cover actual query variables, shared envelopes/metadata, direct query returns with descriptive arguments, three @oneOf selector modes, conditions/resolved, and no batch scope/per-kind union/resource-specific create input. Runtime scope/partial-context and endpoint credential compatibility remain implementation gates.

| Spec coverage | Owning design contract |
| --- | --- |
| FR-001–012, FR-020–021 | `contracts/discovery.md`, metadata/data model, fleet/cache operations |
| FR-013–017, FR-019 | `contracts/admin.graphqls`, `contracts/authorization.md` |
| FR-018, FR-022 | Protected views, implementation documentation/release sequence |
| FR-023–024 | Research client alternatives; discriminated bootstrap and independent resource target |
| FR-025, SC-008 | Initial contract classification, versioned transient inputs, no-effect rejection and normal-path drift gates |
| FR-026 | Fixed action-based target modes, derived single-scope capture, mixed/indeterminate-scope rejection |
| FR-027, SC-009 | Endpoint credential bindings linked to shared login profiles; actual handler/scheme/audience compatibility |
| PR-001–007, SC-001–008 | `contracts/operations.md`, source/flow/review/replica/convention correctness oracles |

## Phase 2 — Implementation Sequence for Task Generation

1. Foundations: failing contract/security/convention tests, validated publication config, finite action catalog, optional provider capabilities and component-owned registration. Classify every new root operation/input family using existing API-reference conventions; assert versioned transient review requests/results and discriminator validation before effects. Generate catalogs when their source changes.
2. US1: canonical public snapshots/ETags, independent route/admission, proxy/CORS/redaction/placeholder cases.
3. US2: bounded metadata validation/refresh, shared login profiles and endpoint-specific credential delivery/provider bindings; baseline pre-registration and gated DCR/CIMD/device/resource support. Test Git header delivery versus Basic password prompting and GraphQL HTTP/WebSocket channels; no inferred protocol compatibility.
4. US3: align with #589 descriptive query arguments/direct returns; implement three selector modes and one derived capture scope. Reuse owning contexts, report REVIEW_CONTEXT_REQUIRED when missing proposed facts prevent a definitive answer, and retain arbitrary-subject authorization semantics.
5. US4: protected health/provider/config projections behind distinct deployment actions; preserve errors/admission extensions.
6. Qualification: replica/mixed-version and owned deployed load/fault coverage in existing runners; update operator/client docs, run generation checks and `make pr-ready` before creating a PR. Live tests require a verified owned deployment.

Every slice includes security/bounds tests. Run `graphify update .` after runtime code changes. Generate dependency-ordered `tasks.md` later with `/speckit-tasks`; no implementation tasks are executed by this command.

### Prevent Convention and Behavioral Drift in the Initial Feature

Remove batch scope and Boolean sentinels. Target has only id, namespacePath and inNamespace context; no name/create branches or tier/visibility inputs. Action definitions determine kind/scope. Omitted target is allowed only for registered cluster actions requiring no individual resource. For creation/context checks lacking relevant proposal facts, return explicit context-required rather than fabricate a definitive decision. Existing-resource resolution delegates to owning helpers.

Resolve all check scopes as CLUSTER or one authoritative Namespace incarnation before one capture. Reject mixed/unknown scopes wholesale without leaking inaccessible facts. Recheck namespace identity/version after capture. Test same-scope cross-kind checks, ID/path parity, cluster-only actions, mixed namespaces, unresolved IDs, context-required cases and recreation races.

Use shared ObjectMeta/ObjectMetaInput with no parallel metadata contract; computed metadata may be null without changing persisted contracts. Follow #589: selfAccessReviews(reviews:...) returns an ordered direct list, no input-named query argument/batch wrapper/Payload. Integrate against #589 before runtime implementation; do not preserve its removed accessReview wrapper. AST gates enforce these conventions.

FR-025/SC-008 are release obligations, not deferred cleanup. Extend existing schema contract suites so every new operation/input family has a reviewed classification; enforce every top-level apiVersion/kind/metadata envelope, exclusive spec/status versus direct-body placement, #40 Condition/resolved status shape, defaults/nullability, shared type references, omitted/valid/invalid envelopes, direct Go callers and no effects on rejection. A projection label cannot exempt a top-level object. Add deliberate negative mutations proving missing metadata/discriminators/defaults, sibling body fields outside spec/status, incompatible status shapes, ignored validation or changed operation/review parity fail the relevant check. Generated-source freshness alone cannot catch semantically inconsistent SDL.

Share executable context builders between operations and reviews and typed descriptors between feature owners and discovery; do not copy switch tables, action strings, query-term lists, flow prerequisites or schema snapshots. Public addresses/client registration policy remain explicit operator configuration, but unsupported/unknown references fail validation. Promote planned SDL to `shared/schemas/admin` once during implementation and replace the planning copy with a pointer, avoiding two maintained schemas. Validate first-party consumers/examples with the owning schema. All applicable gates run through existing default tests and `make pr-ready`, and changes to contracts, producers and local consumers ship together. This is scoped to preventing drift introduced by this feature, not a repository-wide cleanup project.

### ADR Lifecycle and Completion

ADR 0012 is already Accepted; preserve its listener/schema isolation. ADR 0019's App installation/grant system remains Proposed and out of scope: static discovery registration does not implement it. No Proposed ADR is accepted here. If a dedicated discovery ADR is introduced, keep it Proposed until its core decision is in force and include dated acceptance/index updates in the implementing PR. Final implementation must close this spec lifecycle and reconcile #307/#472 only after evidence exists.

## Complexity Tracking

No constitution violations. Operator-controlled capability-floor rollout is an explicit deployment prerequisite, not process-local consensus. Optional OAuth descriptors preserve interoperability without credential or registration lifecycle management inside GitStore.

### Review Result Identity and Correlation

Use reviews: [SelfSubjectAccessReviewInput!]! directly. Remove the unreturned key and check wrapper; preserve input order and list length, including null failures with indexed GraphQL errors. Keep namespacePath for the established resource selector and rename namespace-only context to inNamespace. The latter selects a Namespace context rather than a child resource.

Normalize resolved targets into spec.targetRef using the existing CatalogObjectReference type; no separate namespace/resourceId/resourceName output fields or new reference schema. Use actual source UID/version, not Relay input IDs. A namespace context reference identifies that Namespace; omitted cluster context has null reference. Captured scope remains internal/evidenced and still enforces one Namespace incarnation or cluster per batch. Contract gates assert type reuse, reference normalization, no check/key wrappers and selector-mode applicability.
