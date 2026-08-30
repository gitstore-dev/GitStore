# Implementation Plan: Controller-Manager Service-Account Authentication (Phase 1)

**Branch**: `061-controller-serviceaccount-auth` | **Date**: 2026-08-29 | **Spec**: [spec.md](spec.md)
**Input**: Feature specification from `/specs/061-controller-serviceaccount-auth/spec.md`

## Summary

Formalize `docs/implementation/021-controller_service_account_auth.md`'s already-decided design (Option B — a GitStore-issued service-account identity plane, chosen over Kubernetes-issued tokens, an external OAuth2/OIDC AS, SPIFFE/SPIRE, mTLS, and the status quo) into Spec Kit artifacts and an implementation sequence. Add a persistent `ServiceAccount` datastore record (namespace, name, UID, `disabled`, enrolled public keys) backed by both `go-memdb` and ScyllaDB; two new opt-in `gitstore-api` AuthN providers — `serviceaccount-assertion` (proof-of-possession, gates only `issueServiceAccountToken`) and `serviceaccount-jwt` (verifies access tokens on ordinary requests); four new GraphQL mutations (`issueServiceAccountToken`, `createServiceAccount`, `rotateServiceAccountKey`, `deleteServiceAccount`); a `gitstore-controller-manager`-side `CredentialSource` abstraction replacing `graphqlclient.Client`'s immutable token string, shared across its three existing client-construction call sites; a `gitctl` enrollment subcommand; and a `transport.Websocket.InitFunc` binding subscription lifetime to token expiry and account revocation. This spec is scoped so that its P1 work alone (persistent identity + both providers + admin CRUD + least-privilege `role_bindings`) is sufficient to give `gitstore-controller-manager` a working, non-human, least-privilege credential without `static-admin` or spec 060's `static-users` existing — see spec.md's "Relationship to Specs 059, 060, and Doc 021" for the exact, code-verified nature of that dependency (architectural, not a hard compile/runtime break).

## Technical Context

**Language/Version**: Go 1.25 (`gitstore-api`, `gitstore-controller-manager`). No Rust (`gitstore-git-service`) change — this spec is entirely an AuthN/identity-plane and controller-client concern, orthogonal to git validation/admission.
**Primary Dependencies**: `github.com/golang-jwt/jwt/v5 v5.3.1` (already in `gitstore-api/go.mod`; supports `EdDSA`/`ES256` signing methods needed for FR-012 — no version bump required), `go.uber.org/zap`, `github.com/spf13/viper`, `github.com/google/uuid` (ServiceAccount UID generation, already used elsewhere for UIDs), `gocqlx/v3` + `gocql` (ScyllaDB), `go-memdb` (dev datastore) — all already present. `gitstore-controller-manager`'s `graphqlclient` package and `gorilla/websocket` are already present and require no new dependency. No new external dependency in either service.
**Storage**: New `ServiceAccount` datastore entity and its memdb table + Scylla migration (`006_service_account.cql`, numbered after `005_namespace_repository_fence.cql`, which spec 047 / PR #370 added), added to `datastore.Datastore`'s interface (`CreateServiceAccount`/`GetServiceAccountBySubject`/`ListServiceAccountKeys`/`UpdateServiceAccountKeys`/`DisableServiceAccount`/`DeleteServiceAccount`), mirroring the `File` entity's existing `entities.go` struct + `memdb/backend.go` + `scylla/file.go` + `scylla/migrations/004_file_resource.cql` pattern (spec 051) as its template. An in-memory-only assertion-`jti` replay cache and WebSocket live-connection registry are explicitly *not* datastore-backed (single-instance-scoped by design, per doc 021 §8c/§8d and this spec's own Assumptions on multi-replica deferral).
**Testing**: Go unit tests for `serviceaccountjwt`/`serviceaccountassertion` providers (mirroring `staticadmin_test.go`'s/spec 060's `staticusers_test.go`'s shape: load/validate, sign/verify, clock-skew, replay, revocation); datastore contract tests against both memdb and Scylla backends (mirroring `repository_contract_test.go`/`file_test.go`); `gitstore-controller-manager` unit tests for `CredentialSource`/`ServiceAccountSource` (sign, exchange, cache, proactive renewal, backoff-on-failure); integration tests for the full assertion→token→authenticated-request flow and for WebSocket `InitFunc` accept/reject/revoke; root `make test`/`make build`/`make pr-ready`.
**Target Platform**: Linux server and Darwin development hosts already supported by all services.
**Project Type**: Multi-service feature spanning `gitstore-api` (identity plane) and `gitstore-controller-manager` (credential consumer); `gitstore-git-service` is untouched.
**Performance Goals**: Not on the git push/admission hot path (constitution's <5ms/500-files budget is unaffected). One additional asymmetric-signature verification per GraphQL request when `serviceaccount-jwt` is chained in — the same per-request cost class as `static-admin`'s existing HS256 verify, not a new performance tier. Assertion exchange (`issueServiceAccountToken`) is a low-frequency operation (once per access-token lifetime, default every ~8 minutes per controller instance), not a per-request cost.
**Constraints**: MUST NOT modify `rbac-local`'s `Authorize`/`Policy` decision semantics (FR-021). MUST NOT remove or break `GITSTORE_CONTROLLER__API_TOKEN` (FR-014). MUST NOT embed roles/scopes in any token (FR-011). MUST use asymmetric signing only for access tokens (FR-012). MUST NOT introduce a mandatory external dependency (no Kubernetes, no external OAuth2 AS, no SPIRE) — this spec works identically on bare process, Docker Compose, and CI, per doc 021 §4c/§5's already-decided constraint.
**Scale/Scope**: Single-`gitstore-api`-instance profile for replay protection and the WebSocket connection registry (see spec.md Assumptions); ServiceAccount count scales with controller-class count (currently one: CategoryTaxonomy), not with catalog scale.
**Replica/Scaling Model**: The persistent `ServiceAccount` registry (FR-001) is replica-safe from the start (datastore-backed like every other entity). The assertion-replay cache and WebSocket connection registry are explicitly single-instance-scoped for this spec (doc 021 §15's flagged risk) — a follow-on spec is required before enabling this feature across multiple `gitstore-api` replicas, exactly as spec.md's Assumptions state.
**Authentication/Authorization**: Two new AuthN providers (`serviceaccount-assertion`, `serviceaccount-jwt`), strictly opt-in via `auth.authn.chain` (FR-009); zero `rbac-local`/`AuthZProvider` interface change; the four new mutations are authorized via existing `rbac-local` action-string checks (`serviceaccount.create`/`serviceaccount.key.rotate`/`serviceaccount.delete`, new action strings, gated the same way `category.status.write` already is).
**Load/Backpressure Model**: No new unbounded queue; assertion exchange and access-token verification reuse the existing per-request GraphQL processing path with no new goroutine pool. Controller-side renewal is a single scheduled goroutine per `CredentialSource`, not per-request.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-checked after Phase 1 design.*

### Pre-design gate

| Principle | Result | Plan evidence |
|---|---|---|
| I. Test-First Development | PASS | Failing provider, datastore-contract, mutation, and controller-side `CredentialSource` tests are written before each corresponding implementation change (Phase 2+ task ordering), mirroring spec 060's proven `staticusers` test-first sequence. |
| II. API-First Design | PASS | `issueServiceAccountToken`/`createServiceAccount`/`rotateServiceAccountKey`/`deleteServiceAccount`'s GraphQL shapes and the access-token/assertion claim contracts are fully defined in doc 021 §8/§9 and this spec's `contracts/` before any resolver or provider code is written. |
| III. Clear Contracts & Versioning | PASS | All four mutations are net-new/additive; no existing mutation's shape changes. `GITSTORE_CONTROLLER__API_TOKEN` keeps working unmodified (FR-014) — no breaking change, unlike spec 060's justified `static-admin` removal. |
| IV. Production Observability & Debuggability | PASS | New `gitstore_api_authn_requests_total{provider,outcome}` metric (doc 021 §13); structured `DecisionLogger` fields already cover `subject`, which naturally renders as `serviceaccount:<namespace>:<name>`; controller-manager `/health` reports not-ready until a credential is obtained (FR-016); grep-based CI check for credential leakage in logs (FR-020, SC-007). |
| V. User Story Driven Development | PASS | Work maps to US1 (mint a credential without static-admin/static-users), US2 (ServiceAccount CRUD), US3 (least privilege via unmodified `rbac-local`), US4 (automatic renewal), US5 (enrollment tooling), US6 (WebSocket lifecycle). |
| VI. Independently Deployable Delivery | PASS | US1–US3 (P1) are independently shippable and testable using only `gitstore-api` and a hand-signed assertion — no `gitstore-controller-manager` change required to satisfy them, exactly matching this spec's own "unblock spec 060" scoping. US4–US6 layer on afterward without re-opening US1–US3's contracts. |
| VII. Simplicity with Proven Scale | JUSTIFIED COMPLEXITY | See Complexity Tracking — introducing two new AuthN providers, a new persistent entity, and a controller-side credential abstraction in one spec is inherently larger than a typical incremental spec, though every individual piece reuses an existing pattern (provider shape from `staticadmin`/`staticusers`, entity/datastore shape from `File`, controller registration shape from `internal/namespace`/`internal/categorytaxonomy`) rather than inventing a new one. |
| VIII. Horizontally Replicable Core Services | PASS (with a documented, scoped exception) | The persistent `ServiceAccount` registry is replica-safe from day one. The assertion-replay cache and WebSocket connection registry are explicitly single-instance-scoped, recorded as a flagged, deferred risk (doc 021 §15) rather than a silent gap — see Complexity Tracking. |
| IX. Multi-User Authentication, Authorization & Isolation | PASS | This spec adds a *fourth* principal type (non-human) alongside human-local (spec 060), human-external (spec 059), and anonymous — cleanly isolated by subject-namespace convention (`serviceaccount:<namespace>:<name>`) and by `AuthMethod` value, with zero cross-principal-type code sharing beyond the already-generic `AuthNProvider`/`Principal`/`ChainedAuthN` interfaces. |
| X. Production Capacity, Backpressure & Load Validation | PASS | No new unbounded queue, scan, or goroutine per request; assertion exchange is a low-frequency operation bounded by access-token TTL, not by request volume. |

**Gate result**: PASS (with one justified complexity exception and one documented, scoped multi-replica limitation — both recorded, not silent).

### Post-design gate

Phase 1 design preserves the pre-design result:

- `ServiceAccount`'s datastore methods reuse `File`'s exact interface shape (`Create*`/`Get*ByX`/`List*`/`Update*WithResourceVersion`/`Delete*`) rather than inventing a new persistence pattern;
- `serviceaccount-assertion`/`serviceaccount-jwt` reuse the existing `AuthNProvider` interface verbatim — `Authenticate`/`RevokeSession`/`RefreshSession`/`IssueSession` — exactly as `staticadmin`/`staticusers` already do, with `RefreshSession`/`IssueSession` returning `ErrNotSupported` where doc 021 §8b specifies (service accounts renew via proof-of-possession, not the session-refresh flow);
- `buildProviderRegistry`'s `switch name { ... }` dispatch gains two new `case` arms, matching the exact shape spec 060 already used to swap `"static-admin"` for `"static-users"`;
- the controller-manager's `CredentialSource` interface is additive — `graphqlclient.Client.token string` becomes `Client.credentials CredentialSource`, and `StaticToken` (wrapping the unmodified `GITSTORE_CONTROLLER__API_TOKEN` path) is one trivial implementation alongside the new `ServiceAccountSource`, so FR-014's backward-compatibility requirement is structurally guaranteed, not merely tested for;
- no existing `AuthNProvider`/`AuthZProvider`/`UserDirProvider`/`Datastore` interface signature changes — every addition is either a new interface implementation or new, additive interface methods (`Datastore.CreateServiceAccount` etc.).

**Post-design result**: PASS.

## Project Structure

### Documentation (this feature)

```text
specs/061-controller-serviceaccount-auth/
├── plan.md
├── research.md
├── data-model.md
├── quickstart.md
├── contracts/
│   ├── serviceaccount-provider.md      # serviceaccount-assertion + serviceaccount-jwt AuthN contract, claim tables, buildProviderRegistry wiring
│   ├── serviceaccount-mutations.md     # issueServiceAccountToken/createServiceAccount/rotateServiceAccountKey/deleteServiceAccount GraphQL contract
│   └── controller-credential-source.md # graphqlclient.CredentialSource, ServiceAccountSource, StaticToken, the 3-call-site sharing fix, WebSocket InitFunc contract
├── checklists/
│   └── requirements.md
└── tasks.md
```

### Source Code (repository root)

```text
gitstore-api/
├── internal/
│   ├── auth/
│   │   ├── types.go                              # unchanged — AuthNProvider/Principal/Capability already sufficient (doc 021 §2)
│   │   └── provider/
│   │       ├── serviceaccountassertion/           # NEW package
│   │       │   ├── provider.go                      # verifies client assertions; typ/aud/iat/exp/jti checks; gates issueServiceAccountToken only
│   │       │   ├── replay.go                        # in-memory jti replay cache (single-instance scope)
│   │       │   └── provider_test.go
│   │       └── serviceaccountjwt/                  # NEW package
│   │           ├── provider.go                      # verifies access tokens; iss/aud/exp/sa_uid checks; empty Roles
│   │           ├── keys.go                           # signing-key set with kid-based lookup, overlap-window support
│   │           └── provider_test.go
│   ├── config/
│   │   └── config.go                              # new AuthConfig.ServiceAccount (issuer/audience/assertion_audience/signing_key/default_ttl/max_ttl/clock_skew); new ControllerServiceAccount-adjacent keys live in gitstore-controller-manager's own config, not here
│   ├── datastore/
│   │   ├── entities.go                            # new ServiceAccount struct (mirrors File's shape: UID/Namespace/Name/Generation/ResourceVersion/CreationTimestamp/...)
│   │   ├── datastore.go                           # new Datastore interface methods: CreateServiceAccount/GetServiceAccountBySubject/ListServiceAccountKeys/UpdateServiceAccountKeys/DisableServiceAccount/DeleteServiceAccount
│   │   ├── memdb/backend.go                       # memdb table + method implementations (mirrors File's methods in the same file)
│   │   └── scylla/
│   │       ├── serviceaccount.go                    # Scylla implementation (mirrors scylla/file.go)
│   │       └── migrations/006_service_account.cql   # new table, numbered after 005_namespace_repository_fence.cql
│   ├── graph/resolver/
│   │   ├── serviceaccount.resolvers.go            # NEW: issueServiceAccountToken/createServiceAccount/rotateServiceAccountKey/deleteServiceAccount resolvers
│   │   └── serviceaccount_service_test.go
│   ├── middleware/security/
│   │   └── graphql.go                             # AroundFields gains an issueServiceAccountToken-specific subject/UID match check (mirrors category.status.write's existing per-field gate)
│   └── app/
│       └── server.go                              # buildProviderRegistry: two new case arms; transport.Websocket gains InitFunc + CloseFunc; new live-connection registry wiring
├── shared/schemas/
│   └── serviceaccount.graphqls                    # NEW: ServiceAccount type, IssueServiceAccountTokenInput/Payload, CreateServiceAccountInput/Payload, RotateServiceAccountKeyInput, DeleteServiceAccountInput/Payload
└── policy.yaml.example                            # gitstore-controller-manager role + role_bindings entry added as a documented example (not a default — FR-009's opt-in posture)

gitstore-api/cmd/gitctl/
└── main.go                                        # new `enroll-serviceaccount` subcommand (US5): generate/accept key pair, call createServiceAccount/rotateServiceAccountKey using an already-authenticated session, write only the private key locally

gitstore-controller-manager/
├── internal/
│   ├── config/config.go                          # new ServiceAccountNamespace/ServiceAccountName/ServiceAccountKeyID/ServiceAccountKeyRef (an ADR 0001 SecretRef, not a path)/SecretProviderBootstrap config keys; ApiToken's doc comment updated to "deprecated dev/CI fallback"
│   ├── secret/                                   # NEW (ADR 0009 §3): bootstrap-tier SecretResolver — Ref/BootstrapProviderConfig types, file + env providers, ADR 0001 error classes, fail-closed. Prerequisite for the credential source below
│   └── graphqlclient/
│       ├── client.go                               # Client.token string → Client.credentials CredentialSource; do()/Subscribe() call credentials.Current(ctx)
│       ├── credential.go                           # NEW: CredentialSource interface, Credential struct, StaticToken, ServiceAccountSource
│       └── credential_test.go
└── cmd/controller/main.go                          # single graphqlclient.Client (and single CredentialSource) constructed once, passed into registerNamespace/registerCategoryTaxonomy/registerProductWatch instead of each constructing its own client (fixes the 3-call-site drift recorded in research.md)

tests/integration/
└── serviceaccount_auth_test.go                     # end-to-end: create SA → enroll key → sign assertion → issue token → authenticated request → rotate → disable/delete → WebSocket InitFunc accept/reject/revoke
```

**Structure Decision**: Extend the existing pluggable-auth pattern in place — two new sibling packages under `gitstore-api/internal/auth/provider/`, one new datastore entity following the `File` template exactly, four new resolvers following the existing mutation-resolver shape, and a controller-side `CredentialSource` seam that is additive to (not a rewrite of) `graphqlclient.Client`. No new service, no new datastore backend, no new external dependency in either module.

## Phase 0: Research Outcomes

Research decisions are recorded in [research.md](research.md):

1. Adopt doc 021's Option B (GitStore-issued service accounts) verbatim — already decided, not re-litigated; this spec's own research is confined to codebase verification and scope/phasing decisions doc 021 left to a future spec (see below).
2. Doc 021's source citations (`gitstore-api`/`gitstore-controller-manager` line numbers, interface shapes) were re-verified against the current codebase (2026-08-29) and found substantially accurate, with one drift: three `graphqlclient.Client` construction call sites in `main.go`, not one — this spec's `CredentialSource` MUST be shared across all three, not installed at a single cited call site.
3. Confirmed, by tracing `Makefile`'s `bootstrap-token` target and `gitctl`'s actual subcommand list, that spec 060 introduces no hard compile/runtime dependency on this spec — the coupling this spec closes is architectural (avoiding a human-shaped credential for a machine caller), not a hard block. Recorded precisely in spec.md's "Relationship to Specs 059, 060, and Doc 021" section rather than overstated.
4. This spec's own phase mapping reorders doc 021 §14's Phase 2/3 by actual urgency to spec 060 (persistent identity + providers + CRUD first, since that alone unblocks 060; controller-side renewal and enrollment tooling both follow, in either order, since both are production-usability improvements over the already-sufficient manual-token path).
5. `ServiceAccount` persistence follows the `File` entity's exact datastore-interface and migration-numbering template (spec 051) rather than inventing a new persistence pattern.
6. The `docs/implementation/020-pluggable_auth_architecture.md` cross-reference follows spec 059's own precedent exactly (an "Addendum" paragraph appended to the superseded Phase 7 section, not a rewrite of that section).

All technical unknowns doc 021 already resolved are treated as resolved; the only items this spec's own research newly settles are codebase-verification findings and phase-sequencing choices. No `NEEDS CLARIFICATION` remains.

## Phase 1: Design and Contracts

### Data model

[data-model.md](data-model.md) defines the persistent `ServiceAccount` entity and its enrolled-key sub-structure, the access-token and client-assertion claim tables (doc 021 §9a/§9b, carried forward verbatim), the `gitstore-controller-manager`-side `Credential`/`CredentialSource` shapes, and the config keys on both sides (doc 021 §8c, re-verified against `config.go`'s current struct shape on both services).

### Interface contracts

- [contracts/serviceaccount-provider.md](contracts/serviceaccount-provider.md): `serviceaccount-assertion`/`serviceaccount-jwt` method-by-method `AuthNProvider` contract, `OutcomeChallenge` vs. `OutcomeDeny` decision table, `buildProviderRegistry` wiring.
- [contracts/serviceaccount-mutations.md](contracts/serviceaccount-mutations.md): the four new GraphQL mutations' input/payload shapes (doc 021 §8b, carried forward), authorization requirements per mutation, and the `issueServiceAccountToken` subject/UID-match field-level gate.
- [contracts/controller-credential-source.md](contracts/controller-credential-source.md): `CredentialSource`/`Credential`/`StaticToken`/`ServiceAccountSource`, the shared-construction fix across `main.go`'s three call sites, and the `transport.Websocket.InitFunc`/live-connection-registry contract.
- [quickstart.md](quickstart.md): test-first implementation order across both services, plus manual verification steps (enroll a key, exchange an assertion by hand, authenticate a query, rotate a key, disable an account, observe WebSocket revocation).

### Implementation sequence

1. Add the `ServiceAccount` datastore entity, `Datastore` interface methods, memdb implementation, and Scylla migration/implementation. Add failing contract tests against both backends first (mirroring `file_test.go`/`scylla/file.go`'s contract-test shape).
2. Add failing unit tests for `serviceaccountassertion`/`serviceaccountjwt` (claim validation, signature verification, replay rejection, clock skew, `OutcomeChallenge` vs. `OutcomeDeny`). Implement both providers until green.
3. Add failing resolver tests for `createServiceAccount`/`rotateServiceAccountKey`/`deleteServiceAccount`/`issueServiceAccountToken` (authorization gating, key overlap, UID-mismatch rejection, disabled/deleted denial). Implement resolvers, the new `shared/schemas/serviceaccount.graphqls` schema, and the `issueServiceAccountToken`-specific field authorizer until green.
4. Wire both providers into `buildProviderRegistry`'s `switch` (opt-in only; default chain unchanged) and add the metrics/logging doc 021 §13 specifies.
5. Add `contracts/serviceaccount-provider.md`'s `policy.yaml` example role/binding (`gitstore-controller-manager`) as documented example config, not a shipped default.
6. Add failing unit tests for `gitstore-controller-manager`'s `CredentialSource`/`StaticToken`/`ServiceAccountSource` (sign, exchange, cache, proactive renew, backoff-on-failure, precedence over `GITSTORE_CONTROLLER__API_TOKEN`). Implement `credential.go`, rewire `client.go`'s `do()`/`Subscribe()`, and consolidate `main.go`'s three call sites into one shared client/`CredentialSource` construction.
7. Add the `gitctl enroll-serviceaccount` subcommand with a failing idempotency test first.
8. Add `transport.Websocket.InitFunc`/`CloseFunc`, the live-connection registry, and revocation-on-disable/delete, with failing tests first (accept/reject/revoke/expiry-deadline).
9. Add end-to-end integration coverage (`tests/integration/serviceaccount_auth_test.go`) and update `docs/implementation/020-pluggable_auth_architecture.md` (Phase 7 addendum), `docs/configuration.md`/equivalent (new config keys, `GITSTORE_CONTROLLER__API_TOKEN` deprecation note), and `docs/runbooks/controller-auth.md` (doc 021 §13's runbook, new). Run targeted tests, `make build`, `make test`, `make pr-ready`.

## Complexity Tracking

| Violation | Why Needed | Simpler Alternative Rejected Because |
|---|---|---|
| Two new AuthN providers plus a new persistent entity plus a controller-side credential abstraction, shipped as one spec rather than split further | Doc 021 already scoped Phase 1 as one inseparable deliverable (persistent identity + both providers + issuance + CRUD) because issuance without CRUD is unadministrable and CRUD without issuance has no consumer; splitting Phase 1 itself further would leave an unusable intermediate state with no independent value, unlike the P2/P3 stories (US4–US6) which genuinely do stand alone as production-usability improvements over an already-working P1 | A narrower "just add `serviceaccount-jwt`, defer CRUD to a follow-on spec" alternative was considered and rejected: without `createServiceAccount`, there would be no way to register the very identity `serviceaccount-jwt` verifies against, making the provider untestable and unshippable in isolation |
| Single-instance-scoped assertion-replay cache and WebSocket connection registry, despite the codebase's general "horizontally replicable core services" constitution principle | Doc 021 §15 already identifies this as a known, explicitly-flagged risk requiring a shared replay store and revocation-broadcast mechanism before multi-replica enablement; building that shared mechanism now, with no current multi-replica `gitstore-api` deployment exercising it, would be speculative infrastructure for a scaling tier this codebase has not yet reached for *any* AuthN provider (not even `static-admin`'s existing in-memory session blacklist is multi-replica-safe today) | Deferring the entire feature until multi-replica-safe replay/revocation exists was rejected — it would block spec 060's unblocking need (this spec's whole reason for urgency) on a scaling concern no current deployment profile actually requires yet; the single-instance limitation is explicitly documented (spec.md Assumptions, this table) rather than silently accepted |
