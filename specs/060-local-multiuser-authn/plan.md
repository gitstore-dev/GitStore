# Implementation Plan: Local Multi-User AuthN + UserDir Provider (`static-users`)

**Branch**: `060-local-multiuser-authn` | **Date**: 2026-09-01 (revised 2026-09-01) | **Spec**: [spec.md](spec.md)
**Input**: Feature specification from `/specs/060-local-multiuser-authn/spec.md`

## Summary

Remove `static-admin` entirely and replace it with `static-users`, a new `AuthNProvider` **and** `UserDirProvider` in `gitstore-api` that authenticates HTTP Basic Auth credentials against a config-driven `users.yaml` file listing `{username, bcrypt password_hash, display_name?, email?}` entries. "Admin" stops being a distinct code concept — it becomes purely an `rbac-local` role name, granted through the existing, unmodified `role_bindings` mechanism. Because `static-admin`'s hardcoded role assignment disappears with it, this plan adds a startup safety check that fails fast if `static-users` + `rbac-local` are both active but no configured username has any `role_bindings` entry, preventing a silent, migration-induced lockout. The migration itself is a documented, operator-run, breaking change (justified against this project's pre-stable alpha-phase precedent), not a runtime env-var fallback. A previously-planned registry-level `IssueSessionFor` addition is dropped as no longer necessary once `static-admin` is gone. Along the way, this plan fixes a confirmed, pre-existing config-validation bug: `auth.jwt.secret` was unconditionally required regardless of whether any local credential provider was even chained in; it becomes conditional on `static-users`' presence in `auth.authn.chain`, while `auth.grpc.hmac_secret` (unrelated to AuthN provider choice) is confirmed correctly unconditional and left untouched.

## Technical Context

The current `main` baseline exposes an explicit `auth.userdir.provider`
selector, explicit config-file loading for the local Compose profile, and the
durable Namespace watch contract. These are deployment and authorization
inputs for this feature, not additional 060 implementation scope.

**Language/Version**: Go 1.25 (`gitstore-api`). No Rust (`gitstore-git-service`) or `gitstore-controller-manager` change — the latter is deliberate, not an oversight: the controller-manager's post-`static-admin` credential path is owned by spec 061 (`061-controller-serviceaccount-auth`, PR #409), per spec.md's Dependencies section (DEP-001/003).
**Primary Dependencies**: `golang.org/x/crypto/bcrypt`, `github.com/golang-jwt/jwt/v5`, `gopkg.in/yaml.v3`, `go.uber.org/zap`, `github.com/spf13/viper` — all already in `go.mod`. No new dependency.
**Storage**: File-backed `users.yaml` and `policy.yaml`; shared ScyllaDB `auth_session_revocations` rows with per-JTI TTL in production and an in-process equivalent for memdb development. Migration 007 adds the revocation table.
**Testing**: Go unit tests for the new `staticusers` package (AuthN: load/validate/reload, Basic Auth, Bearer, revoke/refresh/issue; UserDir: get/list/search, not-found sentinel, unsupported writes); a unit test for the new `RBACLocalProvider.HasAnyRoleBindingFor` helper; a unit test for the new `validateAuthChainConfig` function covering both the conditional-JWT-secret case and the unconditional-HMAC-secret case; migrated `tests/integration/authz_repository_contract_test.go` and `tests/integration/namespace_contract_test.go` per `contracts/backdoor-retirement.md`; root `make test`/`make build`/`make pr-ready`.
**Target Platform**: Linux server and Darwin development hosts already supported by `gitstore-api`.
**Project Type**: Single-service, replacement-in-place feature within `gitstore-api`'s existing pluggable AuthN/AuthZ architecture.
**Performance Goals**: Not on any hot path beyond what `static-admin` already cost per request (one bcrypt compare on Basic Auth, one HMAC verify on Bearer) — no new performance target.
**Constraints**: MUST NOT modify `rbac-local`'s `Authorize`/`Policy` decision semantics. MUST NOT modify `oidc-jwt`/spec 059. Operator CLI helpers may perform explicit, validated, atomic config-file edits, but no runtime mutation API is introduced. MUST NOT auto-migrate legacy `GITSTORE_AUTH__ADMIN__*` env vars at runtime. MUST NOT document creating a `static-users` (human-shaped) credential for `gitstore-controller-manager` — that path belongs to spec 061's `serviceaccount-jwt` provider (spec.md DEP-002).
**Scale/Scope**: Config-file-driven user counts (tens, not thousands) — the lightweight/testing/small-deployment path, not a scaled multi-tenant identity store.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-checked after Phase 1 design.*

### Pre-design gate

| Principle | Result | Plan evidence |
|---|---|---|
| I. Test-First Development | PASS | Failing unit tests for `staticusers` (AuthN + UserDir), `HasAnyRoleBindingFor`, and `validateAuthChainConfig` are written before their implementations (Phase 2/3 task ordering). |
| II. API-First Design | PASS | The `users.yaml` schema (`data-model.md`) and the `AuthNProvider`/`UserDirProvider`/config-validation contracts (`contracts/static-users-provider.md`) are defined before any provider code is written. Zero GraphQL schema changes. |
| III. Clear Contracts & Versioning | PASS (with a documented, precedent-backed exception) | The removal of `static-admin`'s config keys is a breaking change to environment-variable-level configuration, justified per Constitution Principle III's own precedent: spec 030 and spec 046 both already established that removing something never shipped in a stable release is not a breaking change under semver — this project is at `0.1.0-alpha.2`, no stable release exists. See Complexity Tracking. |
| IV. Observability & Debuggability | PASS | `static-users` logs load/reload/auth-decision events with the same `zap` conventions `static-admin`/`rbac-local` used; the migration-safety startup failure (FR-013) is a clear, actionable error, not a silent misconfiguration. |
| V. User Story Driven Development | PASS | Work maps to US1 (multi-user AuthN), US2 (`role_bindings` end-to-end and now load-bearing), US3 (migration safety net), US4 (backdoor + harness-bootstrap retirement). |
| VI. Incremental Delivery | PASS | US1-US3 (the provider itself, fully correct and safe) are independently shippable and testable before US4's test-harness migration begins. |
| VII. Simplicity & YAGNI | PASS | Every structural choice is copied from `static-admin`'s or `rbac-local`'s existing, already-tested code. The previously-planned `IssueSessionFor` addition is explicitly *removed* in this revision once its motivating problem (two coexisting local providers) no longer exists — the simpler outcome, not a more complex one, is the correct one here. |

**Gate result**: PASS. One documented, precedent-backed exception (breaking config-key removal, justified under Principle III's own established pre-stable-release precedent) — recorded in Complexity Tracking, not treated as a silent violation.

### Post-design gate

Phase 1 design preserves the pre-design result:

- `staticusers.UserList`/`UserEntry` mirror `rbaclocal.Policy`/`RolePolicy`'s exact struct-tag/versioning shape;
- `StaticUsersProvider`'s AuthN fields mirror `StaticAdminProvider`'s former exact fields and behavior; its new UserDir methods reuse the same loaded state, no second load path;
- `buildProviderRegistry`'s `case "static-users":` reuses the existing `switch name { ... }` dispatch shape verbatim, simply replacing the removed `"static-admin"` case;
- `RBACLocalProvider.HasAnyRoleBindingFor` is additive and read-only — `Authorize`'s decision logic is untouched, preserving the first draft's (and this revision's re-confirmed) finding that `rbac-local` needs no semantic changes;
- `validateAuthChainConfig` reuses the existing two-phase "generic struct validation, then explicit semantic checks" pattern already present in `validateConfig` (`validateDatastoreConfig`/`validateLogFormat`) rather than inventing a new validation mechanism.

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
│   ├── static-users-provider.md      # AuthN + UserDir contract, registry/config wiring, migration-safety check placement
│   └── backdoor-retirement.md        # test-user: mechanism + harness static-admin bootstrap + namespaceOwnerAuthZ fix
├── checklists/
│   └── requirements.md
└── tasks.md
```

### Source Code (repository root)

```text
gitstore-api/
├── internal/
│   ├── auth/
│   │   ├── types.go                           # Principal.IsAdmin() doc-comment update only (FR-022) — no behavior change
│   │   └── provider/
│   │       ├── staticadmin/                    # DELETED entirely (provider.go, jti.go, staticadmin_test.go)
│   │       ├── staticusers/                    # NEW package, replaces staticadmin as sibling to rbaclocal/
│   │       │   ├── provider.go                   # StaticUsersProvider: AuthNProvider methods (mirrors former staticadmin) + UserDirProvider methods (new)
│   │       │   ├── users.go                      # UserList/UserEntry + loadUsers/validateUsers (mirrors rbaclocal/policy.go)
│   │       │   ├── errors.go                     # ErrUserNotFound sentinel
│   │       │   ├── jti.go                        # generateJTI (moved from staticadmin/jti.go, unchanged)
│   │       │   └── staticusers_test.go
│   │       └── rbaclocal/
│   │           └── provider.go                  # additive: HasAnyRoleBindingFor([]string) bool (read-only; Authorize untouched)
│   ├── config/
│   │   └── config.go                          # Admin/UserConfig fields+type deleted; JWTConfig.Secret loses `validate:"required"`; new StaticUsersConfig; new validateAuthChainConfig; defaults/known-keys/log-marshaling updated
│   └── app/
│       └── server.go                          # buildProviderRegistry: "static-admin" case removed, "static-users" case added; explicit auth.userdir.provider="static-users" selects the same instance for UserDir; default chain literal updated; migration-safety check (FR-013) added; SIGHUP reload extended
├── users.yaml.example                         # NEW, documents the schema from data-model.md
└── .env.example                               # ADMIN__USERNAME/PASSWORD_HASH lines removed; chain example updated

tests/integration/
├── namespace_contract_test.go                 # testUserAuthN removed; embedded helper source's staticadmin import/bootstrap replaced with staticusers; namespaceOwnerAuthZ's IsAdmin() check replaced
└── authz_repository_contract_test.go          # TestRepositoryAuthorization_TwoUserNamespaceIsolation + helpers migrated to real static-users logins

gitstore-api/cmd/server/main_test.go                              # staticadmin.New(...) call migrated to staticusers.New(...)
gitstore-api/internal/middleware/security/secure_test.go          # same
gitstore-api/internal/graph/resolver/auth_resolvers_test.go       # same
gitstore-api/internal/middleware/security/graphql_test.go         # "static-admin" AuthMethod string literals relabeled to "static-users" (cosmetic, no import to migrate)
gitstore-api/internal/middleware/security/graphql_file_status_test.go  # same
gitstore-api/internal/auth/provider/rbaclocal/rbaclocal_test.go   # same
gitstore-api/internal/auth/provider/allowall/allowall_test.go     # same

Makefile                                       # add-user/add-role/assign-role and hash-user-password operator helpers; bootstrap hints updated

docs/
├── implementation/020-pluggable_auth_architecture.md   # §2a rewritten (static-users, not static-admin); §5a config keys updated; §7 Phase 1 language updated
├── user-guide.md                                       # line 380's static-admin example updated
└── api-reference.md                                    # line 25's static-admin example updated
```

**Explicitly out of scope for this plan** (research.md Decision 7): `gitstore-api/gen/gitstore/git/v1/*` (generated code — never hand-edited); `docs/implementation/021-controller_service_account_auth.md` (a separate, unimplemented spec's own premise — flagged, not edited); `docs/implementation/019-pluggable_auth_design.md` (superseded historical record).

**Structure Decision**: A direct in-place replacement of one provider package by another, sibling to `rbac-local`; one additive read-only method on `rbac-local`; config-struct removal plus one conditional-validation fix on the same already-being-touched config surface; a test-harness migration confined to `tests/integration/` plus the handful of unit-test files that directly imported the removed package. No new service, no new datastore backend, no `AuthNProvider`/`UserDirProvider`/`AuthZProvider` interface change.

## Phase 0: Research Outcomes

Research decisions are recorded in [research.md](research.md):

1. Full replacement, not a sibling — `static-admin` deleted entirely; default chain becomes `["static-users","anonymous"]`.
2. "Admin" is no longer a code concept — purely an `rbac-local` role name via `role_bindings`.
3. `Principal.IsAdmin()` is no longer reliable for `static-users` principals; confirmed dead on every live (non-test) code path already; doc-comment updated; the one real test-double caller (`namespaceOwnerAuthZ`) is fixed as part of the harness migration.
4. Migration path: clean breaking change, no runtime env-var fallback — justified by this project's own pre-stable-alpha precedent (spec 030, spec 046, `cmd/gitctl` replacing `cmd/hashpw`) and by a structural reason (an auto-migrated credential would still need a matching `role_bindings` entry in a separately-owned config file, which a runtime auto-write would have to reach into).
5. The role_bindings safety net: a fail-fast startup check when `static-users` + `rbac-local` are active but no configured username has any `role_bindings` entry — closes the exact "migrated and silently lost admin access" hazard.
6. Migration tooling: `gitctl hash-password` remains the hashing primitive; explicit `users add`, `rbac role add`, and `rbac binding add` commands perform validated atomic file updates without cross-file side effects.
7. Removal scope enumerated exhaustively by grep and categorized (provider package, production wiring, tests importing the package, tests using it only as a string label, generated code, in-scope docs, flagged-but-out-of-scope docs).
8. `ChainedAuthN.IssueSessionFor` (planned in the first draft) is dropped — its motivating problem (two coexisting local, token-minting providers) no longer exists once `static-admin` is removed.
9. `rbac-local` sufficiency re-confirmed unchanged.
10. UserDir: implement the read half now, backed by two new optional `users.yaml` fields — reverses the first draft's "no change needed" conclusion because the premise (no real multi-identity data worth serving) changed.
11. Nothing consumes UserDir yet; `createdBy`/`updatedBy` are the natural future candidates but changing them is a separate, additive GraphQL design decision deferred to whichever future spec needs it.
12. Config-validation bug found and fixed: `auth.jwt.secret` was unconditionally required regardless of chain contents; made conditional. `auth.grpc.hmac_secret` (the suspected "third env var") confirmed correctly unconditional and explicitly ruled out of the fix.
13. Relationship to spec 059 unchanged.

All technical unknowns are resolved; no `NEEDS CLARIFICATION` remains.

## Phase 1: Design and Contracts

### Data model

[data-model.md](data-model.md) defines the `users.yaml` schema (including the new optional `display_name`/`email` fields), the in-memory `UserList`/`UserEntry`/`StaticUsersProvider` shapes, the config-struct removal and repurposing (`AuthConfig.Admin` deleted, `AuthConfig.JWT` repurposed to `static-users`, new `AuthConfig.StaticUsers`), the `validateAuthChainConfig` fix, the `Principal`/`UserProfile` shapes `static-users` produces, and the (unmodified) `policy.yaml` `role_bindings` usage pattern that is now load-bearing for any administrative access.

### Interface contracts

- [contracts/static-users-provider.md](contracts/static-users-provider.md): the `AuthNProvider` + `UserDirProvider` method-by-method contract, the `buildProviderRegistry` wiring change, the `HasAnyRoleBindingFor` helper and the migration-safety check's exact placement, the `validateAuthChainConfig` fix, and the `Principal.IsAdmin()` doc-comment update.
- [contracts/backdoor-retirement.md](contracts/backdoor-retirement.md): the exact, traced mechanism behind both the `test-user:` bypass *and* the harness's own `static-admin`-based bootstrap (a mechanical consequence of removal, not optional), the `namespaceOwnerAuthZ` fix, and the target post-migration state.
- [quickstart.md](quickstart.md): test-first implementation order, plus manual verification and migration steps.

### Implementation sequence

1. Add failing unit tests for `staticusers.loadUsers`/`Reload` (missing file, malformed YAML, wrong `version`, duplicate username, empty `password_hash`) and for the new UserDir read methods (`GetBySubject`/`ListGroups`/`SearchUsers`, including the not-found sentinel). Implement `users.go`/`provider.go`'s UserDir side until green.
2. Add failing unit tests for `StaticUsersProvider.Authenticate`/session lifecycle (mirroring `staticadmin_test.go`'s former shape, minus anything cross-provider-specific — there is no second local provider to test against anymore). Implement `provider.go`'s AuthN side until green.
3. Add a failing unit test for `RBACLocalProvider.HasAnyRoleBindingFor`. Implement it as a pure, additive, read-only method until green — confirm `Authorize`'s existing tests are unaffected.
4. Add a failing unit test for `validateAuthChainConfig` (both directions: `static-users` in chain + empty secret fails; `static-users` absent + empty secret succeeds; `auth.grpc.hmac_secret` empty always fails regardless of chain). Implement it, and remove `JWTConfig.Secret`'s struct tag, until green.
5. Add a failing test for `buildProviderRegistry`'s migration-safety check (FR-013): `static-users` + `rbac-local` + zero matching `role_bindings` fails with an actionable error; the same with `allow-all` succeeds; the same with at least one matching `role_bindings` entry succeeds. Implement the check until green.
6. Remove `AuthConfig.Admin`/`UserConfig`, add `AuthConfig.StaticUsers`, update defaults/known-keys/`MarshalLogObject`, replace `buildProviderRegistry`'s `"static-admin"` case with `"static-users"`, add `"static-users"` as a valid explicit `auth.userdir.provider` value, and pass the same instance to UserDir only when that selector is chosen. Update the default AuthN chain literal in both `config.go` and `server.go`.
7. Delete `gitstore-api/internal/auth/provider/staticadmin/` entirely. Fix the resulting compile errors in `gitstore-api/cmd/server/main_test.go`, `gitstore-api/internal/middleware/security/secure_test.go`, `gitstore-api/internal/graph/resolver/auth_resolvers_test.go` by switching their `staticadmin.New(...)` calls to `staticusers.New(...)`.
8. Relabel the cosmetic `"static-admin"` `AuthMethod` string literals in `gitstore-api/internal/middleware/security/{graphql_test.go,graphql_file_status_test.go}`, `gitstore-api/internal/auth/provider/rbaclocal/rbaclocal_test.go`, `gitstore-api/internal/auth/provider/allowall/allowall_test.go` to `"static-users"`.
9. Migrate `tests/integration/namespace_contract_test.go` (backdoor removal, `staticadmin`→`staticusers` bootstrap migration, `namespaceOwnerAuthZ` fix) and `tests/integration/authz_repository_contract_test.go` per `contracts/backdoor-retirement.md`. Confirm `grep -rn "test-user:\|staticadmin\|static-admin" tests/integration/` returns zero matches.
10. Update `Makefile` operator helpers and bootstrap hints, `gitstore-api/.env.example`, `docs/implementation/020-pluggable_auth_architecture.md` (§2a/§5a/§7), `docs/user-guide.md`, `docs/api-reference.md`. Note `docs/implementation/021-controller_service_account_auth.md`'s stale premise as a flagged, not-performed-here follow-up (mirroring how the first draft flagged `docs/runbooks/production-readiness-testing.md`). Run targeted tests, `make build`, `make test`, `make pr-ready`.

## Complexity Tracking

| Violation | Why Needed | Simpler Alternative Rejected Because |
|---|---|---|
| Breaking removal of `auth.admin.*` configuration keys and the `static-admin` provider, with no runtime backward-compatibility shim | This project has no stable release yet (`0.1.0-alpha.2`); Constitution Principle III's own precedent (already invoked verbatim by spec 030 and spec 046) treats removing something never shipped in a stable release as not a breaking change under semver. A runtime auto-migration shim was seriously considered and rejected on structural grounds (research.md Decision 4): it would still need to reach into `rbac-local`'s separately-owned `policy.yaml` to synthesize a usable role binding, reproducing in a new form the exact "administrative access granted by something invisible in the policy file" problem this spec removes from `static-admin`'s hardcoded role. | A runtime fallback that migrates the credential but not its role binding was rejected — it is precisely the silent-lockout hazard Decision 5's safety net exists to prevent, so choosing it would mean solving that hazard with one hand while reintroducing a variant of it with the other. |
| One additive method on `rbac-local` (`HasAnyRoleBindingFor`), touching code outside the new provider package | The migration-safety check (FR-013) needs to query `rbac-local`'s loaded `role_bindings` from `buildProviderRegistry`, and no existing read-only accessor exposes that map | Duplicating `role_bindings`' loading/parsing inside `staticusers` or `server.go` instead of adding one accessor method to the package that already owns the data was rejected as strictly worse: it would create a second, independent parser for the same file format, risking drift from `rbaclocal/policy.go`'s own validation rules |
