# Implementation Plan: Local Multi-User AuthN Provider (`static-users`)

**Branch**: `060-local-multiuser-authn` | **Date**: 2026-08-29 | **Spec**: [spec.md](spec.md)
**Input**: Feature specification from `/specs/060-local-multiuser-authn/spec.md`

## Summary

Add `static-users`, a new, additional, opt-in `AuthNProvider` implementation in `gitstore-api` that authenticates HTTP Basic Auth credentials against a config-driven `users.yaml` file listing `{username, bcrypt password_hash}` pairs — structurally a hybrid of `static-admin`'s Basic Auth/JWT session-lifecycle code and `rbac-local`'s file-load/validate/reload convention. `static-admin` is left completely unchanged. `static-users` uses its own dedicated JWT signing secret/issuer to prevent a traced cross-provider privilege-escalation path (research.md Decision 4), and never hardcodes a role, deferring entirely to `rbac-local`'s existing, unmodified `role_bindings` mechanism. One small, additive registry-level change (`ChainedAuthN.IssueSessionFor`) routes session issuance to the provider that actually authenticated the principal, closing a chain-ordering hazard without touching either provider's own code. The test-only `test-user:` bearer-token backdoor in `tests/integration/namespace_contract_test.go` is retired in favor of real `static-users` logins in `TestRepositoryAuthorization_TwoUserNamespaceIsolation`.

## Technical Context

**Language/Version**: Go 1.25 (`gitstore-api`). No Rust (`gitstore-git-service`) or `gitstore-controller-manager` change — this feature is entirely within `gitstore-api`'s AuthN plane and its own integration test harness.
**Primary Dependencies**: `golang.org/x/crypto/bcrypt` (already in `go.mod`, used identically to `static-admin`), `github.com/golang-jwt/jwt/v5` (already in `go.mod`), `gopkg.in/yaml.v3` (already in `go.mod`, used by `rbac-local`), `go.uber.org/zap`, `github.com/spf13/viper`. No new dependency in any `go.mod`/`Cargo.toml`.
**Storage**: None. No `datastore.Datastore` interface or schema change. The new user list is a config file (`users.yaml`), not a datastore-backed entity — mirroring `rbac-local`'s `policy.yaml`, which is also not datastore-backed.
**Testing**: Go unit tests for the new `staticusers` package (mirroring `staticadmin_test.go`'s and `rbaclocal_test.go`'s coverage shapes: load/validate/reload, Basic Auth success/failure, Bearer verify success/expired/foreign-token, revoke, refresh, issue); a unit test proving `static-users`-signed tokens fail `static-admin.authenticateBearer` and vice versa (User Story 3); a `registry_test.go` addition for the new `IssueSessionFor` method; migrated `tests/integration/authz_repository_contract_test.go` and `tests/integration/namespace_contract_test.go` coverage per `contracts/backdoor-retirement.md`; root `make test`/`make build`/`make pr-ready`.
**Target Platform**: Linux server and Darwin development hosts already supported by `gitstore-api`.
**Project Type**: Single-service, additive feature within `gitstore-api`'s existing pluggable AuthN/AuthZ architecture (`docs/implementation/020-pluggable_auth_architecture.md`).
**Performance Goals**: Not on any hot path beyond what `static-admin` already costs per request (one bcrypt compare on Basic Auth, one HMAC verify on Bearer) — no new performance target.
**Constraints**: MUST NOT modify `static-admin`'s source, config keys, or behavior (FR-003). MUST NOT modify `rbac-local`'s source (research.md Decision 5 confirms it needs none). MUST NOT modify `oidc-jwt`/spec 059. MUST NOT introduce a new UserDir provider (research.md Decision 6). The one shared-code change (`ChainedAuthN.IssueSessionFor`) MUST be additive — no existing `AuthNProvider`/`ChainedAuthN`/`ProviderRegistry` method signature changes.
**Scale/Scope**: Config-file-driven user counts (tens, not thousands) — this is explicitly the lightweight/testing/small-deployment path, not a scaled multi-tenant identity store (spec.md Assumptions).

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-checked after Phase 1 design.*

### Pre-design gate

| Principle | Result | Plan evidence |
|---|---|---|
| I. Test-First Development | PASS | Failing unit tests for `staticusers` (load/validate/reload, auth, session lifecycle, cross-provider token-rejection) and for `ChainedAuthN.IssueSessionFor` are written before their implementations (Phase 2/3 task ordering). |
| II. API-First Design | PASS | The `users.yaml` schema (`data-model.md`) and the `AuthNProvider`/registry contracts (`contracts/static-users-provider.md`) are defined before any provider code is written. No GraphQL schema change at all — `login`/`logout`/`refreshToken` mutations are untouched (their resolvers already delegate generically to the registry). |
| III. Clear Contracts & Versioning | PASS | Purely additive: one new provider name, one new config section, one new registry method. No existing `AuthNProvider`/`AuthZProvider`/`UserDirProvider` interface signature changes. `static-admin`'s contract is explicitly unchanged (FR-003). |
| IV. Observability & Debuggability | PASS | `static-users` logs load/reload/auth-decision events with the same `zap` structured-logging conventions `static-admin`/`rbac-local` already use; the `DecisionLogger` AuthZ middleware (unchanged) continues to log every `rbac-local` decision for `static-users` subjects exactly as it does for any other subject. |
| V. User Story Driven Development | PASS | Work maps to US1 (multi-user authn), US2 (`rbac-local` role_bindings exercised end-to-end), US3 (cross-provider token-safety), US4 (backdoor retirement). |
| VI. Incremental Delivery | PASS | US1-US3 (the provider itself, fully correct and safe) are independently shippable and testable before US4's test-harness migration begins; US4 depends on US1 existing but not vice versa. |
| VII. Simplicity & YAGNI | PASS | Every structural choice (file format, load/reload discipline, hashing, session-lifecycle shape) is copied verbatim from one of two already-existing, already-tested providers (`static-admin`, `rbac-local`) rather than inventing new patterns. The one genuinely new piece of shared code (`IssueSessionFor`) is the minimum necessary to close a traced, real security gap (research.md Decision 4) — not speculative. |

**Gate result**: PASS. No complexity exceptions required.

### Post-design gate

Phase 1 design preserves the pre-design result:

- `staticusers.UserList`/`UserEntry` mirror `rbaclocal.Policy`/`RolePolicy`'s exact struct-tag/versioning shape (data-model.md);
- `StaticUsersProvider`'s session-lifecycle fields (`blacklist`, `jwtSecret`, `jwtDuration`, `refreshGrace`) mirror `StaticAdminProvider`'s exact fields and behavior (contracts/static-users-provider.md);
- `buildProviderRegistry`'s new `case "static-users":` reuses the existing `switch name { ... }` dispatch shape verbatim (contracts/static-users-provider.md);
- the backdoor-retirement migration (contracts/backdoor-retirement.md) changes only test-scoped code and leaves the unrelated `namespaceOwnerAuthZ` and `/__test/resource-body` fixtures untouched (FR-015).

**Post-design result**: PASS.

## Project Structure

### Documentation (this feature)

```text
specs/060-local-multiuser-authn/
├── plan.md
├── research.md
├── data-model.md
├── quickstart.md
├── contracts/
│   ├── static-users-provider.md      # AuthNProvider contract, registry IssueSessionFor addition, wiring
│   └── backdoor-retirement.md        # exact test-user: mechanism trace + migration contract
├── checklists/
│   └── requirements.md
└── tasks.md                           # created later by /speckit-tasks
```

### Source Code (repository root)

```text
gitstore-api/
├── internal/
│   ├── auth/
│   │   ├── registry.go                       # additive: ChainedAuthN.IssueSessionFor(ctx, *Principal)
│   │   ├── registry_test.go                   # new tests for IssueSessionFor routing
│   │   └── provider/
│   │       └── staticusers/                   # NEW package, sibling to staticadmin/ and rbaclocal/
│   │           ├── provider.go                  # StaticUsersProvider: Authenticate/RevokeSession/RefreshSession/IssueSession/Reload
│   │           ├── users.go                     # UserList/UserEntry + loadUsers (mirrors rbaclocal/policy.go)
│   │           ├── jti.go                       # generateJTI (mirrors staticadmin/jti.go, or shared helper extracted — decided in Phase 0 research if warranted)
│   │           └── staticusers_test.go
│   ├── config/
│   │   └── config.go                          # AuthConfig gains StaticUsers StaticUsersConfig `mapstructure:"staticusers"`; new defaults + known-keys entries
│   ├── app/
│   │   └── server.go                          # buildProviderRegistry gains `case "static-users":`; SIGHUP reload extended to any active static-users provider
│   └── graph/resolver/
│       └── auth_service.go                     # Login calls registry.AuthN().IssueSessionFor(ctx, principal) instead of IssueSession(ctx, principal.Subject)
└── users.yaml.example                         # NEW, mirrors an existing policy.yaml.example convention if present, else a new documented example file

tests/integration/
├── namespace_contract_test.go                 # testUserAuthN removed; embedded helper source wires staticusers.New(...) with test-fixture users.yaml instead
└── authz_repository_contract_test.go          # TestRepositoryAuthorization_TwoUserNamespaceIsolation + helpers migrated to real static-users logins

docs/
├── implementation/020-pluggable_auth_architecture.md   # §2 gains a new static-users subsection (sibling to static-admin's); §5a gains the new Viper keys; §7 Rollout Phases gains a new phase entry
└── runbooks/production-readiness-testing.md            # Pattern 4's worked-example citation flagged for a follow-up update (not performed by this spec — see spec.md Assumptions)
```

**Structure Decision**: A new provider package (`staticusers/`) sibling to the two existing providers it draws from, one additive registry method, one additive config section, one additive `buildProviderRegistry` case, and a test-harness migration confined to `tests/integration/`. No new service, no new datastore backend, no existing interface signature change, no change to `static-admin`, `rbac-local`, or `oidc-jwt`.

## Phase 0: Research Outcomes

Research decisions are recorded in [research.md](research.md):

1. Provider name: `static-users` (plural), config keys under `auth.staticusers.*` — chosen to preserve the `static-*` family relationship to `static-admin` while signaling the one-vs-many distinction.
2. File-loading convention: a dedicated `users.yaml` file, structurally mirroring `rbaclocal/policy.go`'s exact load/validate/reload shape — not embedded in `policy.yaml`, not a raw Viper env-var list.
3. Password hashing: bcrypt via the existing `gitctl hash-password` subcommand — no new hashing tool or dependency.
4. **Critical, traced finding**: `static-admin.authenticateBearer` grants its hardcoded `admin` role to *any* bearer token verifying against its own secret/issuer, regardless of subject. `static-users` MUST use a dedicated signing secret/issuer (closes the shared-secret risk with zero `static-admin` change) AND session issuance MUST be routed to the provider that actually authenticated the principal via a new, additive `ChainedAuthN.IssueSessionFor` method (closes the chain-ordering risk, also with zero `static-admin`/`static-users`-internal change — the fix lives in shared registry plumbing and the `Login` resolver's call site).
5. `rbac-local` requires zero source changes — confirmed by reading `Authorize`/`Policy` in full; `role_bindings` already operates on an arbitrary subject string with no provider-specific assumption.
6. UserDir (`none`) requires no change — confirmed by an exhaustive grep showing zero live consumers of `registry.UserDir()` anywhere in the GraphQL layer today.
7. The `test-user:` backdoor is a wholly test-scoped mechanism (an embedded-source-string-compiled throwaway subprocess binary), never linked into the real `gitstore-api` binary — but its continued presence is a lingering unauthenticated-identity-bypass pattern in the codebase, and its exact two migration targets are `tests/integration/authz_repository_contract_test.go` and `tests/integration/namespace_contract_test.go` (no other file matches `test-user:`).
8. Relationship to spec 059: purely complementary, zero shared code or design dependency in either direction.

All technical unknowns are resolved; no `NEEDS CLARIFICATION` remains.

## Phase 1: Design and Contracts

### Data model

[data-model.md](data-model.md) defines the `users.yaml` file schema, the in-memory `UserList`/`UserEntry`/`StaticUsersProvider` shapes, the new Viper config keys, the `Principal` shape `static-users` produces (no hardcoded roles), and the (unmodified) `policy.yaml` `role_bindings` usage pattern operators will use to make `static-users` identities meaningful for authorization.

### Interface contracts

- [contracts/static-users-provider.md](contracts/static-users-provider.md): the `AuthNProvider` method-by-method contract, the additive `ChainedAuthN.IssueSessionFor` method and its call site change in the `Login` resolver, the `buildProviderRegistry` wiring addition, and non-normative chain-ordering guidance.
- [contracts/backdoor-retirement.md](contracts/backdoor-retirement.md): the exact, traced mechanism behind the `test-user:` bypass, its precise migration targets, and the target post-migration state.
- [quickstart.md](quickstart.md): test-first implementation order, plus manual verification steps.

### Implementation sequence

1. Add failing unit tests for `staticusers.loadUsers`/`Reload` (missing file, malformed YAML, duplicate username, missing hash) mirroring `rbaclocal_test.go`'s shape. Implement `users.go` until green.
2. Add failing unit tests for `StaticUsersProvider.Authenticate` (Basic Auth success/unknown-user/wrong-password; Bearer success/expired/foreign-signature) and session lifecycle (`IssueSession`/`RevokeSession`/`RefreshSession`) mirroring `staticadmin_test.go`'s shape. Implement `provider.go` until green.
3. Add a failing unit test proving a `static-users`-signed token is rejected by `static-admin.authenticateBearer` and a `static-admin`-signed token is rejected by `static-users`' verifier (User Story 3 / SC-002). Confirm green with distinct secrets/issuers — no implementation change needed beyond Steps 1-2 if secrets are correctly distinct.
4. Add a failing test for `ChainedAuthN.IssueSessionFor` routing to the provider named by `principal.AuthMethod`, plus a failing `Login` resolver test proving a `static-users` login issues a `static-users`-signed token even when `static-admin` is earlier in the chain (User Story 3 / SC-003). Implement `IssueSessionFor` and the `Login` call-site change until green.
5. Add the `auth.staticusers.*` Viper config keys, defaults, and known-keys entries in `config.go`; add the `case "static-users":` in `buildProviderRegistry`; extend SIGHUP reload. Add a failing config-validation test first (missing `staticusers.jwt.secret` when `static-users` is chained in).
6. Add failing `rbac-local` `role_bindings` integration coverage proving two `static-users` subjects bound to two different roles produce different authorization outcomes, with zero `rbac-local` source changes (User Story 2 / SC-004).
7. Migrate `tests/integration/namespace_contract_test.go` and `tests/integration/authz_repository_contract_test.go` per `contracts/backdoor-retirement.md`; confirm `grep -rn "test-user:" tests/integration/` returns zero matches (User Story 4 / SC-005).
8. Update `docs/implementation/020-pluggable_auth_architecture.md` (§2, §5a, §7) documenting the new provider and rollout phase; note the `docs/runbooks/production-readiness-testing.md` Pattern 4 citation as a flagged follow-up (not performed here). Run targeted tests, `make build`, `make test`, `make pr-ready`.

## Complexity Tracking

| Violation | Why Needed | Simpler Alternative Rejected Because |
|---|---|---|
| One additive change to shared registry plumbing (`ChainedAuthN.IssueSessionFor`) touching code outside the new provider package | Closing the traced `IssueSession` chain-ordering privilege-escalation hazard (research.md Decision 4) cannot be done inside `static-users`' own code alone — the ambiguity is inherent to `ChainedAuthN`'s existing "first provider that supports it" resolution strategy, which has no way to know which provider authenticated a given subject | Leaving it as an operator-chain-ordering documentation concern was considered and rejected — it is exactly the kind of "correct only by convention, not by construction" outcome that would silently reintroduce a real privilege-escalation path on a misconfigured chain, with no startup warning; a small, additive, well-tested registry method is a strictly smaller risk than that alternative |
