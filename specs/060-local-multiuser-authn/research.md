# Research: Local Multi-User AuthN Provider (`static-users`)

## 1. Provider name

**Decision**: `static-users` (plural), config keys under `auth.staticusers.*`.

**Rationale**: The existing provider naming convention is a short, kebab-case adjective/noun pair describing the identity source: `static-admin` (AuthN), `oidc-jwt` (AuthN), `rbac-local` (AuthZ), `allow-all` (AuthZ), `anonymous` (AuthN), `none` (UserDir). `static-admin`'s defining structural property is "exactly one static credential"; this new provider's defining structural property is "more than one static credential, still config/file-driven, still no external identity service." `static-users` communicates that relationship directly — same family (`static-*`), pluralized to signal the one-vs-many distinction — without inventing a new naming axis. `local-users`, `multi-admin`, and `file-users` were considered and rejected: `local-users` loses the explicit `static-*` family relationship to `static-admin` that this spec's entire narrative depends on; `multi-admin` wrongly implies every configured user is an admin (they are not — FR-005); `file-users` describes the storage mechanism rather than the identity model, inconsistent with how `static-admin` is named for its credential model, not its config source.

**Alternatives considered**:
- `local-users`. Rejected — obscures the direct `static-admin` sibling relationship.
- `multi-admin`. Rejected — implies every listed user has admin privileges, which FR-005 explicitly forbids.
- `file-users`. Rejected — names the storage mechanism (a file), inconsistent with the existing naming convention which names the credential/identity model, not the config source (`rbac-local` is the one partial exception, and it names the *policy* model, not the file format).

## 2. Config/file-loading convention

**Decision**: `static-users` loads a YAML file (default `users.yaml`, configurable via `auth.staticusers.users_file` / `GITSTORE_AUTH__STATICUSERS__USERS_FILE`), structurally mirroring `rbaclocal/policy.go`'s `loadPolicy` function: read file → `yaml.Unmarshal` → `validatePolicy`-equivalent validation (here: non-empty username, non-empty bcrypt hash, no duplicate usernames, `version: "v1"` required) → fail fast on any error, both at initial construction and on reload.

**Rationale**: `rbaclocal/policy.go` (read in full) already establishes exactly this pattern for a different multi-subject-list use case (`role_bindings`, `roles`). Reusing the same shape — a small, versioned, YAML file with strict validation and atomic-replace-safe reload — is the smallest possible design distance from working, tested code (Principle VII, Simplicity & YAGNI), and gives operators one mental model ("local policy/identity files live next to `policy.yaml`, same load/validate/reload discipline") instead of two.

**Alternatives considered**:
- *Extend the existing Viper `AuthConfig` struct directly* (e.g. `auth.staticusers.users: [{username, password_hash}, ...]` as a structured Viper list, no separate file). Rejected — bcrypt hashes are long opaque strings; embedding a growing user list directly in `.env`/environment variables (Viper's env-var binding path) is unwieldy compared to a dedicated file, and every other multi-entry identity/policy list in this codebase (`rbac-local`'s roles and `role_bindings`) already uses a dedicated file for exactly this reason. Diverging from that precedent for no benefit would itself be inconsistent with Principle VII.
- *Reuse `policy.yaml` itself*, adding a `users:` top-level key alongside `roles`/`role_bindings`. Rejected — conflates two different config planes (AuthN provider config vs. AuthZ provider config) into one file and one package (`rbaclocal`), which would force `static-users` (an AuthN provider) to depend on the `rbaclocal` package (an AuthZ provider) or duplicate its YAML-loading code either way. A separate file with an analogous loader function in the `staticusers` package keeps the AuthN/AuthZ plane separation the existing architecture already establishes (`internal/auth/provider/<name>/` per provider, per plane).

## 3. Password hashing

**Decision**: bcrypt, via the same `golang.org/x/crypto/bcrypt` dependency `static-admin` already uses, verified with `bcrypt.CompareHashAndPassword` exactly as `staticadmin/provider.go`'s `authenticateBasic` does today. Hash generation reuses the existing `gitctl hash-password` subcommand (`gitstore-api/cmd/gitctl/main.go`) — no new hashing tool.

**Rationale**: No new dependency, no new hashing tool, and the exact same cost/verification semantics operators already rely on for `static-admin`. `gitctl hash-password` already accepts a password via argv or stdin and prints a bcrypt hash with `bcrypt.DefaultCost` — sufficient for generating N user entries by invoking it N times, one per configured user.

**Alternatives considered**:
- *Argon2id*. Rejected — stronger in isolation, but introduces a second password-hashing primitive alongside bcrypt for no functional benefit at this scale, and would require a new `gitctl` subcommand and a new dependency; inconsistent with reusing `static-admin`'s exact, already-audited verification path.

## 4. JWT signing secret/issuer — the cross-provider privilege-escalation risk

**Decision**: `static-users` MUST use its own dedicated JWT signing secret and issuer (`auth.staticusers.jwt.secret` / `auth.staticusers.jwt.issuer`, both required, structurally mirroring `auth.jwt.secret`/`auth.jwt.issuer`), never `static-admin`'s `auth.jwt.*`. `static-users`' own bearer-verification path assigns **no** hardcoded role to any principal it authenticates — role assignment is left entirely to the active AuthZ provider's own subject-keyed mechanism (`rbac-local`'s `role_bindings`, for example).

**Rationale — traced directly from `staticadmin/provider.go`'s `authenticateBearer`** (read in full):

```go
principal := &auth.Principal{
    Subject:    claims.Subject,
    Issuer:     claims.Issuer,
    Roles:      []string{"admin"},   // ← hardcoded for ANY token that verifies
    AuthMethod: "static-admin",
    TokenID:    claims.ID,
}
```

`static-admin`'s bearer-verification path grants the hardcoded `admin` role to **any** token that verifies against its own HMAC secret and issuer — it never checks `claims.Subject` against `p.username`. This is safe *today* only because `static-admin` is the sole provider that ever signs a token with that secret, and its own `authenticateBasic` only ever authenticates its one configured username before minting one. The moment a second provider is introduced, this blanket assumption becomes a live risk in two independent ways, both of which this spec closes by construction rather than by operator discipline:

1. **Shared-secret risk**: if `static-users` signed its tokens with the *same* secret/issuer as `static-admin`, any `static-users` identity's token would also verify successfully against `static-admin`'s own `authenticateBearer` — which would then unconditionally grant it the `admin` role, regardless of who the subject actually is or what `rbac-local`'s `role_bindings` say. A distinct secret/issuer makes `static-admin`'s parser fail signature verification for a `static-users`-signed token (wrong key), returning `Challenge` and correctly falling through to `static-users`' own verifier instead. **This closes the risk without touching `static-admin`'s source at all** — the fix is entirely in the new provider's own configuration.
2. **Chain-order `IssueSession` risk** (independent of the secret being shared or not): `ChainedAuthN.IssueSession(ctx, subject)` (`registry.go`, read in full) asks each configured provider in chain order and returns the **first** one that does not return `ErrNotSupported` — it has no way to know which provider actually authenticated `subject` moments earlier in the `Login` resolver (`auth_service.go`, read in full: `principal, decision, err := r.registry.AuthN().Authenticate(...)` immediately followed by `r.registry.AuthN().IssueSession(ctx, principal.Subject)`, with no provider-identity threading between the two calls). `static-admin.IssueSession` (via its `issueToken` helper) mints a token for **any** subject string handed to it, with no check that the subject is its own configured admin username. If `static-admin` happens to be earlier in `auth.authn.chain` than `static-users`, a `static-users` login (e.g. `alice`) would still route `IssueSession(ctx, "alice")` to `static-admin` first, which would happily mint an `alice`-subject token signed with `static-admin`'s own secret — and that token would then verify successfully against `static-admin`'s own `authenticateBearer`, which (per risk 1's exact same code) blindly grants it the `admin` role.

**Resolution requires two coordinated, additive pieces — neither one touches `static-admin`'s source**:
- A distinct `static-users` signing secret/issuer (closes risk 1, as shown above).
- A small, additive change to how the `Login` resolver requests session issuance: instead of the generic `ChainedAuthN.IssueSession(ctx, subject)` (first-provider-that-supports-it semantics), route issuance to the specific provider identified by `decision.Provider` (already returned by `Authenticate`, and already mirrored onto `Principal.AuthMethod`) — e.g. a new `ProviderRegistry`/`ChainedAuthN` method such as `IssueSessionFor(ctx, principal *Principal)` that looks up the provider by name instead of iterating for "first supporter." This is a change to the shared chain/registry plumbing (`gitstore-api/internal/auth/registry.go`) and to the `Login` resolver's call site (`gitstore-api/internal/graph/resolver/auth_service.go`) — **not** to `static-admin`'s or `static-users`' own provider code, and not to the `AuthNProvider` interface contract itself. `RefreshSession` has an analogous, lower-severity version of the same ordering question (a provider could refresh a token it did not issue if it doesn't itself verify the token first) — `static-admin`'s `RefreshSession` already re-verifies the full token (signature, issuer, claims) before acting, and `static-users`' `RefreshSession` implementation MUST do the same, which makes routing-by-provider-identity a defense-in-depth improvement for `RefreshSession` rather than a strict correctness requirement the way it is for `IssueSession`.

**Alternatives considered**:
- *Share `static-admin`'s secret/issuer, add a subject-allowlist check inside `static-admin.authenticateBearer` itself*. Rejected outright — this requires changing `static-admin`'s source, which the project brief and this spec's own FR-003 explicitly forbid.
- *Leave `ChainedAuthN.IssueSession` as "first provider that supports it," and instead document a required chain ordering (e.g. "list more-specific providers before `static-admin`") as an operator responsibility*. Rejected — this is exactly the kind of "correct only by operator discipline, not by construction" outcome Principle IV (Observability & Debuggability) and general security practice reject; a misconfigured chain order would silently reintroduce the exact privilege-escalation path this spec exists to prevent, with no error or warning at startup.

## 5. `rbac-local` sufficiency (no AuthZ code change required)

**Decision**: `rbac-local` requires zero source changes. `Policy.RoleBindings map[string][]string` and `RBACLocalProvider.Authorize`'s `if bound, ok := policy.RoleBindings[principal.Subject]; ok { ... }` merge step (`provider.go`, read in full) already operate on an arbitrary subject string with no built-in assumption limiting it to one or to any particular AuthN provider's naming scheme.

**Rationale**: Read directly, not inferred. `provider.go`'s `Authorize` builds `effectiveRoles` from `principal.Roles` (whatever the AuthN provider set, which for `static-users` is empty per FR-005) plus whatever `policy.RoleBindings[principal.Subject]` contains, with no reference anywhere to `principal.AuthMethod` or any provider-specific field. Any subject string — `"admin"`, `"alice"`, `"test-user:alice"`, or a future OIDC `sub` claim — is handled identically. This has simply never been exercised with more than one *real* subject because no real AuthN provider before `static-users` ever produced one.

**Alternatives considered**: None — this is a verification finding, not a design choice with real alternatives.

## 6. UserDir sufficiency (no new UserDir provider required)

**Decision**: `none` (`userdirnone.NoneProvider`) remains the active UserDir implementation. No new UserDir provider is introduced by this spec.

**Rationale**: An exhaustive search (`grep -rn "UserDir()\|registry.UserDir\|UserDirProvider\|userdir" gitstore-api/internal/graph --include="*.go"`, excluding test files) returns zero matches — no resolver, service, or mutation anywhere in the GraphQL layer calls `registry.UserDir()` today. The `UserDirProvider` plane is fully wired into `ProviderRegistry` (`buildProviderRegistry` in `server.go` always constructs a `userdirnone.New()`) but has no live consumer. `rbac-local`'s `role_bindings` — the only mechanism this spec needs to make `static-users` identities meaningful for authorization — keys off the bare `Principal.Subject` string alone, which `static-users` already supplies without needing `UserDirProvider.GetBySubject`/`ListGroups`/etc. Building a richer UserDir implementation now would be speculative work with no consumer to validate it against (Principle VII, Simplicity & YAGNI).

**Alternatives considered**:
- *Feed `users.yaml` into a new UserDir implementation preemptively, in case a future spec needs it*. Rejected — no current consumer exists to define the right shape for such a provider (e.g., what `DisplayName`/`Email` fields would even be populated from, since `users.yaml`'s minimal schema per Decision 2 carries only `username`/`password_hash`). Building it now would be guessing at a future requirement rather than responding to a real one.

## 7. Retiring the `test-user:` backdoor — exact mechanism traced

**Decision**: Remove the `testUserAuthN` type and its duplicated embedded-source-text form from `tests/integration/namespace_contract_test.go`; replace both with a `static-users` provider configured with test fixture credentials in the same synthetic helper server's `AuthNProvider` chain.

**Rationale — exact mechanism confirmed by reading the harness in full, not assumed**:

`tests/integration/namespace_contract_test.go` is a normal `package integration` test file (no build tag gating anything in the *real* `gitstore-api` binary). Its harness (`startNamespaceContractAPIServer`, called via `acquireNamespaceContractAPI` → `newNamespaceContractHarness`) does the following at test run time:

1. It holds a **Go source file as a string literal** inside the test file itself (`source := \`package main ...\``), which it writes out to `gitstore-api/namespace_contract_server_helper.go`.
2. That embedded source is compiled into a standalone throwaway binary, `namespace_contract_server_helper_bin` (a `go build`/`go run`-style step, via `exec.Cmd`), which is launched as a **subprocess** and torn down (`cmd.Process.Kill()` / `cmd.Wait()`) once the last referencing test finishes (ref-counted via `namespaceContractServer.refs`).
3. That embedded `package main`'s own `func main()` constructs a real `internal/app.NewGraphQLHandler` using the real `gitstore-api` packages (`internal/app`, `internal/auth`, `internal/auth/provider/staticadmin`, `internal/auth/provider/anonymous`, `internal/datastore/memdb`), but wires its own bespoke `ProviderRegistry`:
   ```go
   registry := authpkg.NewProviderRegistry(
       authpkg.NewChainedAuthN(&testUserAuthN{}, staticAdmin, anonymous.New()),
       &namespaceOwnerAuthZ{},
       nil,
   )
   ```
   `testUserAuthN` (defined in the same embedded source, and also as a real Go type earlier in `namespace_contract_test.go` for direct reference) recognizes any `Authorization: Bearer test-user:<subject>` header and unconditionally returns `Allow` with `Principal{Subject: <subject>, Roles: []string{"developer"}, AuthMethod: "integration-test"}` — no password, no file, no real credential check at all.
4. The same embedded source also registers a **separate, unrelated** test-only route, `mux.HandleFunc("/__test/resource-body", ...)`, which lets test helpers directly read/inspect a resource's stored body/status via the datastore — this is a test-fixture convenience for a different concern (resource-body inspection) and has nothing to do with identity. FR-015 preserves it.

**Conclusion**: this is **not** a build-tag-gated bypass compiled into the production binary — it is a wholly separate, throwaway `package main` that exists only inside the test module's own generated/compiled helper, sharing import paths with `gitstore-api`'s real packages but never linked into `cmd/server`'s actual binary. It is nonetheless a legitimate security smell worth closing (per the project brief): it is source code, in this repository, that implements an unauthenticated-identity bypass pattern, and its continued presence is the reason no test in this codebase has ever exercised two *real*, credential-authenticated distinct principals. Replacing it with a real `static-users` provider (configured with test-only fixture credentials, e.g. `alice`/`bob` with throwaway bcrypt hashes committed only to a test fixture file) closes that gap using exactly the production code path this spec ships, rather than a parallel fake one.

**Exact migration targets** (confirmed via `grep -rn "test-user:" tests/integration/`):
- `tests/integration/authz_repository_contract_test.go`: `TestRepositoryAuthorization_TwoUserNamespaceIsolation` (lines 21-23, 32-33, 41-42, 97, 134) and its helpers `createNamespaceAsUser`, `createRepositoryAsUser`, `repositoriesAsUser`.
- `tests/integration/namespace_contract_test.go`: line 218, the `const prefix = "Bearer test-user:"` inside the embedded helper source string, and the standalone `testUserAuthN` type definition (lines 194, 215-240) referenced by that same embedded source's `main()`.

No other file under `tests/integration/` matches `test-user:`.

## 8. Relationship to spec 059 (optional OIDC provider)

**Decision**: Purely complementary — no shared code, no shared config surface, no design dependency in either direction.

**Rationale**: Spec 059 ships an *optional*, separately-deployable Hydra+Kratos stack as one possible `issuer_url` for the already-designed (not-yet-implemented) `oidc-jwt` Relying Party provider — it targets operators who want a real, standards-compliant, externally-federatable IdP. This spec ships an always-available, zero-optional-dependency AuthN provider living entirely inside `gitstore-api`'s own binary, targeting (a) the mandatory core components' own test suites, which must be able to prove namespace-isolation/security-boundary behavior without depending on any optional component, and (b) small deployments that want more than one named local account without standing up a full IdP. Neither spec's `research.md`, `data-model.md`, or `contracts/` reference the other's provider implementation; both explicitly document the relationship as complementary in their own Clarifications/Assumptions sections.

**Alternatives considered**: None — this is a scope-boundary clarification, not a design decision with real alternatives.
