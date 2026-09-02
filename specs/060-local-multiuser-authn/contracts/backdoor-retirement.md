# Contract: Retiring the `test-user:` Backdoor and the Harness's `static-admin` Bootstrap

## What exists today (confirmed by direct code reading)

`tests/integration/namespace_contract_test.go` defines, and shares via `newNamespaceContractHarness`, a synthetic `gitstore-api` server with **two** separate identity-related mechanisms this spec must migrate — not just the one the first draft scoped:

1. **The `test-user:` bypass**: an embedded Go source string, compiled into a throwaway subprocess binary, whose `main()` wires `authpkg.NewChainedAuthN(&testUserAuthN{}, staticAdmin, anonymous.New())`. `testUserAuthN.Authenticate` recognizes any `Authorization: Bearer test-user:<subject>` header and unconditionally authenticates it with `Roles: []string{"developer"}` — no credential check at all.
2. **The harness's own admin bootstrap**, in the *same* embedded source, immediately before that `ChainedAuthN` is constructed:
   ```go
   hash, err := bcrypt.GenerateFromPassword([]byte("admin123"), bcrypt.MinCost)
   // ...
   cfg := config.AuthConfig{
       Admin: config.UserConfig{Username: "admin", Password: string(hash)},
       JWT:   config.JWTConfig{Secret: "namespace-contract-secret", Issuer: "gitstore", Duration: "2h"},
   }
   staticAdmin, err := staticadmin.New(cfg, zap.NewNop())
   ```
   This is not optional to migrate: once `gitstore-api/internal/auth/provider/staticadmin/` is deleted (FR-001), this embedded source string — and the standalone Go file at the top of `namespace_contract_test.go` that also imports `staticadmin` directly for the harness's own setup — will not compile. Migrating it is a mechanical consequence of the removal, not a discretionary hygiene improvement.
3. `namespaceOwnerAuthZ` (also defined in `namespace_contract_test.go`), the harness's bespoke, non-`rbac-local` `AuthZProvider` test double:
   ```go
   func (*namespaceOwnerAuthZ) Authorize(_ context.Context, principal *authpkg.Principal, _ string, resource authpkg.ResourceContext) (authpkg.Decision, error) {
       if principal.IsAdmin() {
           return authpkg.Allow("namespace-owner-test", "administrator"), nil
       }
       if resource.OwnerSub != "" && resource.OwnerSub != principal.Subject {
           return authpkg.Deny("namespace-owner-test", "resource belongs to another user"), nil
       }
       return authpkg.Allow("namespace-owner-test", "owner or unowned resource"), nil
   }
   ```
   This checks `principal.IsAdmin()` — which relies on `Principal.Roles` containing `"admin"`. `static-admin`'s former `authenticateBearer`/`authenticateBasic` hardcoded exactly that. `static-users` never does (FR-004). Left unmodified, this test double would silently stop recognizing the harness's bootstrap identity as an administrator after migration — not a compile failure, a silent behavior change in test assertions. FR-019 requires fixing this in the same change.
4. The unrelated `/__test/resource-body` route (also in the same embedded source) is untouched (FR-020) — it is a resource-body/status inspection fixture with no identity dependency.

## Target state after migration

1. `testUserAuthN` (both the standalone type at lines 194/215-240 and its embedded-source duplicate) is deleted.
2. `namespace_contract_test.go`'s own import of `gitstore-api/internal/auth/provider/staticadmin` is replaced with an import of `gitstore-api/internal/auth/provider/staticusers`; its `staticAdmin, err := staticadmin.New(cfg, ...)` bootstrap becomes:
   ```go
   // Write a test-fixture users.yaml with the harness's bootstrap identity.
   staticUsers, err := staticusers.New(config.AuthConfig{
       JWT: config.JWTConfig{Secret: "namespace-contract-secret", Issuer: "gitstore", Duration: "2h"},
       StaticUsers: config.StaticUsersConfig{UsersFile: "<generated-fixture-path>/users.yaml"},
   }, zap.NewNop())
   ```
   with the fixture `users.yaml` containing the harness's bootstrap username (e.g. `admin`, hash generated inline exactly as the removed code did for `"admin123"`), plus `alice`/`bob` for the two-user isolation test.
3. The harness's `ProviderRegistry` wiring becomes:
   ```go
   registry := authpkg.NewProviderRegistry(
       authpkg.NewChainedAuthN(staticUsers, anonymous.New()),
       &namespaceOwnerAuthZ{},
       staticUsers, // static-users also serves as UserDir here, matching production wiring
   )
   ```
4. A companion **test-fixture `policy.yaml`-equivalent** is not required here specifically *because* the harness's own `AuthZProvider` is `namespaceOwnerAuthZ`, not `rbac-local` — FR-013's startup safety check only fires when `auth.authz.provider == "rbac-local"` (research.md Decision 5's explicit scope note), so this harness is unaffected by that check. This is worth stating explicitly so the migration doesn't over-apply FR-013 where it doesn't belong.
5. `namespaceOwnerAuthZ.Authorize`'s `principal.IsAdmin()` check is replaced with a check against the harness's own known bootstrap subject (e.g. `principal.Subject == "admin"`), since that is the only signal available once no provider hardcodes `Roles`. This is a one-line change to an already-being-touched test double, not a new dependency on `rbac-local` or any production role-binding mechanism — it is explicitly a test-double concern, not a `static-users` provider concern (FR-004 is unaffected: `static-users` itself still never hardcodes a role; this fixes the *test double's* own admin-recognition logic to work given that fact).
6. `TestRepositoryAuthorization_TwoUserNamespaceIsolation` and its helpers (`createNamespaceAsUser`, `createRepositoryAsUser`, `repositoriesAsUser`) in `tests/integration/authz_repository_contract_test.go` call a new harness helper (e.g. `h.gqlAsRealUser(username, password, query, vars)`) that performs a real `login` mutation and uses the returned access token, instead of constructing a `"test-user:<subject>"` string.

## Verification contract

- `grep -rn "test-user:" tests/integration/` returns zero matches after migration (SC-005).
- `grep -rn "staticadmin\|static-admin" tests/integration/` returns zero matches after migration (SC-005).
- `TestRepositoryAuthorization_TwoUserNamespaceIsolation` passes with identical assertions using real logins.
- Every other test in `namespace_contract_test.go` that relied on the harness's `admin`-bootstrap identity's administrative access continues to pass, confirming `namespaceOwnerAuthZ`'s updated admin-recognition logic is behaviorally equivalent to the removed `principal.IsAdmin()` check for that one known bootstrap subject.
- The unrelated `/__test/resource-body` route remains present and untouched (FR-020).
