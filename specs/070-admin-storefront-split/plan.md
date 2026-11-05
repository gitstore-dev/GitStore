# Implementation Plan: Admin and Storefront Endpoint Split

**Branch**: `070-admin-storefront-split` | **Date**: 2026-11-04 | **Spec**: [spec.md](spec.md)\
**Input**: Feature specification from `specs/070-admin-storefront-split/spec.md`

## Summary

Split the existing API's GraphQL contract into independently generated Admin and Storefront schemas in the same process. Preserve Admin authoring, admission, authorization and durable watches. Use exactly two named GraphQL listeners: `api.admin_port` serves `/admin/graphql`; `api.storefront_port` serves `/storefront/graphql`. Remove generic `api.port` and migrate clients in one coordinated pre-GA release, without routing modes, compatibility aliases or a Storefront enablement flag. Add an endpoint switcher to the existing playground UI.

Publication readers and trusted target binding are not implemented in this checkout. Deliver an identity-only Storefront schema and a fixed public unavailable handler (503, `STOREFRONT_UNAVAILABLE`, no data, no-store). Do not invent commerce fields, projections or production fixtures to enable it. Add minimal endpoint-specific bounded ingress/operation controls using the existing security middleware and pinned gqlgen hooks. Successful published-catalog capacity and the inherited WebSocket frame-allocation bound remain outside current readiness claims.

## Technical Context

**Language/Version**: Go 1.27.2 (API, controller and workspace); no Rust runtime change.\
**Primary Dependencies**: Existing gqlgen 0.17.90, gqlparser 2.5.33, Gin 1.12.0, gin-contrib/cors 1.7.7, Viper 1.21.0, JWT 5.3.1, x/time/rate and OpenTelemetry Metrics through `shared/observability`; no new dependency.\
**Storage**: Existing memdb development/contracts and Scylla production datastore, durable watch journal and singleton Git storage. No new tables, indexes or migrations.\
**Testing**: Existing Go app/config/security/resolver suites, schema validation/generation checks, HTTP/WebSocket integration fixtures, race tests, shell dispatcher tests and owned k6/Go capacity runners.\
**Target Platform**: Existing native development and Linux service/container deployments; Docker Desktop remains supported for owned tests.\
**Project Type**: Multi-service repository; API wiring and consumers change, not service topology.\
**Performance Goals**: Declared 30-minute workload: 100 public requests/s, 10 Admin operations/s, 20 watches, one bounded manifest push/s. Successful real public reads p95 ≤500 ms/p99 ≤1 s; Admin p95 ≤2 s; unexpected errors <0.1%; acknowledged management convergence ≤60 s. Boundary 503 latency is separate.\
**Constraints**: No alias, enabled flag, current-row Storefront fallback, request-time related-resource aggregate queries, extra service, full doc 038 redesign or copied WebSocket stack.\
**Scale/Scope**: Production envelope: five million Products, 10,000 Categories, two namespaces/targets, two APIs and two controllers. Small publication fixtures cannot satisfy real-public-read production gates.\
**Replica/Scaling Model**: Existing API/controller replicas share authoritative datastore/auth/journal contracts. Endpoint configuration is identical per replica. Bounded caches and traffic counters are protection/efficiency state, not correctness or global quotas. Initial cutover is coordinated; new-contract rolling replacement remains required.

**Topology constraint**: Exactly one active Git process, retained repository storage and non-overlapping replacement. No sharding, replication, autoscaling or Git failover work.

**Authentication/Authorization**: One pluggable registry and independent UserDir plane. Preserve Admin middleware/service authorization and WS auth/revocation. Storefront cannot import Admin models/field authorizers or datastore/Git dependencies. Separate global-ID allow-lists and authenticated Admin introspection are explicit changes.\
**Load/Backpressure Model**: [Security contract](contracts/security-and-limits.md): HTTP body 1 MiB, document 64 KiB, parser 15,000 tokens, depth 15, aliases 30, roots 20, fragments 1,000 visits, finite errors, pages 250, IDs 50 and cost ceiling 1,000. HTTP admission 128 Admin/32 Storefront per replica, no waiting queue, 10-second non-subscription deadline. Extend the IP limiter with finite key count/owned cleanup; implement the absent bounded Admin principal budget. WS connection/operation counts are bounded; pre-decode message allocation remains an inherited gap.\
**Capacity Profile**: Proposed `tests/capacity/profiles/api-endpoint-split.js` / `api/endpoint-split`, reusing `_capacity-k6`, repository push/watch drivers and owned stack/verifier. `api/readiness` is smoke only. [Capacity contract](contracts/capacity-and-rollout.md) defines provenance and separate evidence outcomes.\
**Fault Profile**: Existing `tests/chaos/profiles/api-restart.json`, one owned API, 30-second readiness/60-second observer recovery; publication dependency faults use contract fixtures until real support exists.

## Constitution Check

### Pre-design gate

| Principle | Assessment and concrete design obligation |
| --- | --- |
| Test-first | PASS for planning: write and observe failing route/schema/ID/listener/failure/replay tests before implementation. No runtime pass is claimed now. |
| API/contract-first | PASS: `contracts/` defines the new boundary; existing management storage/resource contracts remain unchanged. |
| Core-service boundary | PASS: API owns execution; controller/client source sets and URLs migrate; Git protocols/storage unchanged. |
| Replica safety | PASS for planned boundary: two API/two controller concurrency/replacement tests and no process-local correctness authority. Initial version cutover is the explicit exception below. |
| Multi-user security | PASS for design: provider matrix, denied/cross-scope probes, disjoint schemas, no public data fallback. Publication authorization is not claimed implemented. |
| Production capacity | CONDITIONAL readiness: retain the five-million-Product envelope and real-data gates. Public read capacity stays blocked on publication. |
| Repeatable evidence | PASS for mechanisms: existing Make families, owned topology and domain receipts; smoke/503 observations cannot substitute for reads. |
| Bounded work | PASS for planned HTTP boundary. WS pre-decode allocation remains open; no full production bounded-memory claim before supported upstream integration/adversarial tests. |
| Observability | PASS: OpenTelemetry SDK/shared lifecycle with bounded attributes, existing auth/admission/watch signals and exact source/config identity; no new direct Prometheus collectors. |
| Incremental delivery | EXCEPTION: explicit user-approved pre-GA coordinated breaking cutover, not overlapping pre-split/post-split routing. Same-contract rolling replacement remains required. |
| Simplicity | PASS: one binary, existing runners/storage/middleware, no migration modes, registry framework or transport fork. |

The cutover exception is justified below. Non-negotiable security, replica and resource obligations are not waived: delivering the boundary is not production certification. Publication and WS readiness gaps must remain visible in implementation evidence. No additional user choice is needed to design this boundary.

## Project Structure

### Documentation (this feature)

```text
specs/070-admin-storefront-split/
├── spec.md
├── plan.md
├── research.md
├── data-model.md
├── quickstart.md
├── checklists/requirements.md
└── contracts/
    ├── storefront.graphqls
    ├── endpoints.md
    ├── security-and-limits.md
    └── capacity-and-rollout.md
```

`tasks.md` is generated later by `/speckit-tasks`; this command does not create it.

### Source Code (repository root; planned changes)

```text
shared/schemas/
  admin/                          # existing management SDL moved here
  common/                         # actually used shared value/interface types
  storefront/                     # identity-only public contract
gitstore-api/
  gqlgen.admin.yml                # rename existing config; Admin + common
  gqlgen.storefront.yml            # new isolated generator configuration
  generate.go                     # explicit generation for both schemas
  internal/app/                   # executors, routes and owned listeners
  internal/config/                # endpoint ports/profiles and validation
  internal/middleware/security/   # extend existing auth/limit/CORS mechanisms
  internal/admin/graph/            # move Admin generated/model/resolver/scalar
  internal/storefront/graph/       # new separate generated/model/resolver
  internal/wsregistry/             # preserve auth/revocation/lifecycle
  cmd/gitctl/                     # complete Admin URL defaults
gitstore-controller-manager/
  internal/config/                # Admin URL defaults
  internal/graphqlclient/         # URL/WS regression coverage
  internal/schemavalidate/         # Admin + common source set
tests/integration/                # existing replica/push/watch fixtures
tests/capacity/{profiles,verifiers}/
scripts/                          # existing dispatcher/stack/schema helpers
config/                           # canonical configuration examples
Makefile                          # API URL and independent health origin
compose.local.yml
compose.capacity.yml
docs/                             # ADR and endpoint/operator/developer updates
```

**Structure Decision**: Move Admin packages to `internal/admin/graph/` and rename the generator config to `gqlgen.admin.yml`, symmetrically with Storefront. About 80 files outside generated outputs reference the old `internal/graph/` path in this checkout; the move creates real one-time import/generator/test churn. Perform it mechanically, update module-qualified imports and scalar/model bindings, regenerate both schemas using explicit config flags, then compile and run existing tests before introducing routing/security changes. Keep package names stable and add no alias/shim packages. Explicit ownership and enforceable endpoint import boundaries justify that pre-GA cost. Extract genuinely shared pure helpers only when needed. No endpoint plugin framework, new module or second service. The referenced local `gitstore-admin` consumer is absent here; coordinate its actual repository separately.

## Phase 0 — Research decisions

[research.md](research.md) records source evidence, decisions, rationale and alternatives. Two read-only research agents checked schema/security and deployment/clients; a focused pinned-source check verified the WS limit-hook gap.

Resolved choices: separate generation, unavailable public route, deterministic listener matrix, no storage change, contextual migration, minimum bounded middleware, no transport fork, separate evidence levels and explicit pre-GA cutover exception. Real publication activation is a named dependency, not unresolved design work to hide behind a flag.

## Phase 1 — Design and contracts

1. **Schema/IDs**: First mechanically move Admin Go sources to `internal/admin/graph/` and update imports/generator bindings, with no behavior change. Rename config to `gqlgen.admin.yml`; generation directives use `go tool gqlgen generate --config gqlgen.admin.yml` and `--config gqlgen.storefront.yml`. Move SDL and extract Node, then generate Storefront with no mutation/subscription root. Validate ID contracts and enforce imports: Storefront cannot depend on Admin models/services/current-row datastore/Git writers.
2. **App construction**: Separate executor creation from route mounting in existing `internal/app`. An internal/test Admin constructor may remain; it must not retain an old public path. Give each endpoint separate APQ/query cache instances and a validated security profile.
3. **Routing/lifecycle**: Validate the four distinct named ports, reject removed generic `api.port`, pre-bind Admin/Storefront/Git HTTP/catalog, then serve under one shutdown owner. Test fixed prefixed paths, custom ports, collisions, OS bind failures and rollback. Operational routes stay on Admin. Preserve Admin WS auth/registry; enforce caller deadline for catalog graceful stop. No shared-listener branch or path relocation logic.
4. **Storefront boundary**: Register its fixed route and unavailable JSON handler with ingress size/CORS/rate/concurrency checks. It does not read the body to determine availability, execute an authorizer, touch datastore or expose a production activation switch. Direct generated-executor tests do not bypass the production guard.
5. **Minimum security gaps**: Use pinned parser/validation/complexity hooks and existing limiter primitives. Add concrete caps, bounded limiter storage/cleanup, list cardinality cost, authenticated Admin introspection and principal cost exemption policy. Do not skip auth/validation on cache hits. Cap WS connections/operations and explicitly document the missing pre-decode frame bound.
6. **Clients/config/health/UI**: Update complete Admin URLs to `/admin/graphql` in controller, `gitctl`, Makefile, Compose, drivers and tests; list/watch uses the same URL without layout detection. Update schema loading and named-port settings. Configure the Admin root health URL explicitly rather than derive `/admin/health`. Add configurable endpoint CORS and the playground selector with independent sessions, explicit peer URL input and no credential transfer. Coordinate external Admin/quickstart consumers separately.
7. **Observability**: Create new instruments through the OpenTelemetry Metrics SDK and existing shared provider/export lifecycle. Use bounded endpoint/outcome/reason attributes; no direct Prometheus collectors or new registry. Existing legacy instrumentation migration is outside this feature. Test SDK collection/export and resource cleanup; no sensitive/high-cardinality attributes.

Contracts: [data-model.md](data-model.md), [endpoints.md](contracts/endpoints.md), [security-and-limits.md](contracts/security-and-limits.md), [storefront.graphqls](contracts/storefront.graphqls).

### Post-design gate re-evaluation

Design gates hold with the declared user-authorized pre-GA exception. There is no new durable correctness state, migration, service or unsupported Git topology. Previously absent HTTP/introspection/cost safeguards are explicit implementation work. Production published-read and adversarial WS-memory gates remain blocked/unverified, not passed by 503 throughput. ADR status stays Proposed during planning.

Update only the plan reference inside root `AGENTS.md` SPECKIT markers; `CLAUDE.md`/`GEMINI.md` remain symlinks. Detailed technologies/history stay in feature artifacts per repository instructions rather than expanding root agent context.

## Phase 2 — Implementation and verification handoff

This sequence is input to `/speckit-tasks`, not runtime implementation or a generated `tasks.md`.

| Slice | Test-first proof and scope | Requirements |
| --- | --- | --- |
| Foundation | Mechanical Admin package/config rename, update imports/bindings and regenerate; compile/regression gate before behavior changes; split schemas and ID boundaries | FR-001, FR-007–FR-010, FR-013 |
| US1 management | Preserve authoring/admission/status/completion; WS authentication/revocation/replay and introspection-denial tests | FR-002, FR-009–FR-010 |
| US2 routing/UI | Fixed two-listener paths, custom named ports, reject old generic config/collisions; bind rollback/shutdown; playground switching with no credential transfer | FR-003–FR-005, FR-015 |
| US3 unavailable | Exact 503/no-data/no-store under credentials, upgrade, persisted-doc and draft probes; fixture-only isolation without runtime toggles | FR-006–FR-008, FR-013 |
| US4 resource controls | Exact/one-over-limit payload/parser/structure/cost/page/ID cases; bounded key maps/in-flight counters, cancellation, principal exemptions and independent origins | FR-010–FR-012, PR-002–PR-004 |
| Connected evidence | Two APIs/two controllers + singleton Git, acknowledged push/replay, new dispatcher profile and overload/restart verifier with separate evidence levels | PR-001–PR-007, SC-001–SC-006 |
| Release/docs | Local/external client inventory, cutover/rollback rehearsal, generated drift checks, ADR/doc alignment and full checks | FR-005, FR-014 |

Test seams: API app route/lifecycle tests, `server_websocket_test.go`, `namespace_watch_shutdown_test.go`, config tables, security `secure_test.go`/subscription transport tests, global-ID tests; controller config/HTTP-to-WS/schema-validation tests; integration push/watch fixtures and existing dispatcher/stack shell tests. Prove exclusion, absence of unauthorized effects, cleanup and durable recovery rather than mirroring constants.

### ADR and documentation completion

The implementing PR must explicitly:

- Change ADR-0012 **Proposed → Accepted (YYYY-MM-DD)** under `docs/ADRs/README.md` dating rules, update that index/open publication/readiness items and close this spec's implemented boundary scope. Planning does not accept the ADR.
- Align ADR-0010/0011 and implementation docs 022/037/038 plus publication-lifecycle wording with endpoint-fixed scope and one cutover. Remove conflicting staged-migration/enablement claims, without implementing unrelated deferred authorization directives or buyer flows.
- Update API/controller docs, config/Compose examples, Make reference, capacity README/scoped instructions and relevant runbooks. User-facing docs must not include internal spec/task/evidence references.
- Use a breaking Conventional Commit/PR title. Document coordinated API/client/routing cutover and rollback with no storage migration. Check actual external Admin/quickstart consumers before release.

### Checks and readiness limits

Run `make generate TARGET=all`, focused/race and shell tests, `make check TARGET=all`, then full default `make pr-ready` before an implementation PR. Extend aggregate generation checks to both schema sets. Refresh `graphify update .` after code changes; this planning change is documentation only.

Use existing Make capacity/chaos families and owned fixtures as defined in [capacity-and-rollout.md](contracts/capacity-and-rollout.md). Real-public production acceptance is blocked by publication/target support; unavailable-boundary results are separate. Supported upstream frame-size integration plus adversarial oversized/fragmented-message regressions are required before full WS bounded-memory readiness. Do not expand this split into a copied transport implementation or claim those unrun gates passed.

## Complexity Tracking

| Violation / tradeoff | Why needed | Resolution / alternative |
| --- | --- | --- |
| Constitution III/VI preference for additive deprecation/compatible overlap | User explicitly directs the least-complex pre-GA cutover for #495 | Coordinated matching API/client/routing upgrade, breaking release note and rehearsed rollback. Temporary aliases are rejected. Same-contract replica safety and durable correctness remain mandatory. |
| Inherited WS pre-decode allocation bound absent | Pinned transport requires roughly 1,100 lines of private protocol/helpers to fork for one setting | Preserve pinned transport and declare production-hardening gap; retain connection/operation/parser caps. Full adversarial memory readiness requires upstream-supported integration and tests. |
| No real published-catalog capacity yet | Publication readers/target binding are separate unimplemented dependencies | Deliver safe unavailable boundary and generated schema tests. Real-public-read acceptance stays blocked; fake readers/current-row fallback are rejected. |
