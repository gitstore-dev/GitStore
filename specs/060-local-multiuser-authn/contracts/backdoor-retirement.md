# Contract: Retiring the `test-user:` Integration-Test Backdoor

## What exists today (confirmed by direct code reading, not inferred)

`tests/integration/namespace_contract_test.go` defines, and shares via `newNamespaceContractHarness`, a synthetic `gitstore-api` server built as follows:

1. A **Go source string literal** (`source := \`package main ...\``) embedded directly inside the test file, written to `gitstore-api/namespace_contract_server_helper.go` at test-run time.
2. That file is compiled into a standalone throwaway subprocess binary (`namespace_contract_server_helper_bin`), launched and torn down per the harness's `refs`-counted lifecycle (`acquireNamespaceContractAPI`/`releaseNamespaceContractAPI`).
3. Its embedded `main()` wires:
   ```go
   registry := authpkg.NewProviderRegistry(
       authpkg.NewChainedAuthN(&testUserAuthN{}, staticAdmin, anonymous.New()),
       &namespaceOwnerAuthZ{},
       nil,
   )
   ```
   where `testUserAuthN.Authenticate` (duplicated as both a real Go type in the test file, lines 194/215-240, and inside the embedded source string) recognizes any `Authorization: Bearer test-user:<subject>` header and unconditionally authenticates it as `Principal{Subject: <subject>, Roles: []string{"developer"}, AuthMethod: "integration-test"}` — no credential of any kind is checked.
4. `namespaceOwnerAuthZ` is also a bespoke, test-only `AuthZProvider` (not `rbac-local`) that grants everything to an `IsAdmin()` principal and otherwise checks `resource.OwnerSub == principal.Subject`. It is unrelated to the AuthN backdoor and is **not** in scope for this migration — it is a legitimate, purpose-built test double for ownership-based authorization and is unaffected by replacing `testUserAuthN`.
5. The same embedded source registers `/__test/resource-body`, an unrelated test-only HTTP route for direct resource-body/status inspection (FR-015: out of scope for this migration).

This mechanism is confirmed to be **entirely test-scoped** — it is never linked into the real `cmd/server` binary. It is nonetheless the only place in the codebase that has ever stood in for "two real, distinct users," and its continued presence (an unauthenticated-identity bypass pattern, even if unshipped) is what this spec's User Story 4 retires.

## Exact migration targets (confirmed via `grep -rn "test-user:" tests/integration/`)

| File | What references the backdoor |
|---|---|
| `tests/integration/authz_repository_contract_test.go` | `TestRepositoryAuthorization_TwoUserNamespaceIsolation` (uses `"test-user:alice"`/`"test-user:bob"` directly at lines 21-23, 32-33, 41-42, and strips the `"test-user:"` prefix to derive expected `createdBy` at lines 97, 134); helpers `createNamespaceAsUser`, `createRepositoryAsUser`, `repositoriesAsUser` (each takes a raw token string and passes it straight through to `h.gqlWithToken`). |
| `tests/integration/namespace_contract_test.go` | Defines the mechanism itself: the standalone `testUserAuthN` type (lines 194, 215-240) and the duplicate embedded-source-text copy of the same type and its `"Bearer test-user:"` prefix check (line 218) inside the compiled helper binary's source. |

No other file under `tests/integration/` matches the string `test-user:`.

## Target state after migration

1. `testUserAuthN` (both the standalone type and its embedded-source duplicate) is deleted.
2. The embedded helper source's `ProviderRegistry` wiring becomes:
   ```go
   staticUsers, err := staticusers.New(config.StaticUsersConfig{
       UsersFile: "<test-fixture-path>/users.yaml",
       JWT: config.JWTConfig{Secret: "namespace-contract-static-users-secret", Issuer: "gitstore-static-users", Duration: "2h"},
   }, zap.NewNop())
   // ...
   registry := authpkg.NewProviderRegistry(
       authpkg.NewChainedAuthN(staticUsers, staticAdmin, anonymous.New()),
       &namespaceOwnerAuthZ{},
       nil,
   )
   ```
   with a test-fixture `users.yaml` (committed under `tests/integration/testdata/` or generated inline at harness startup, mirroring how the harness already generates its bcrypt admin hash inline via `bcrypt.GenerateFromPassword`) containing `alice` and `bob` with throwaway bcrypt hashes.
3. `TestRepositoryAuthorization_TwoUserNamespaceIsolation` and its three helpers call a new harness method (e.g. `h.gqlAsRealUser(username, password, query, vars)`) that performs a real `login` mutation for `alice`/`bob` and uses the returned access token as the bearer credential, instead of constructing a `"test-user:<subject>"` string directly.
4. `namespaceOwnerAuthZ`'s ownership-check logic is unaffected — it already compares `resource.OwnerSub` to `principal.Subject`, and `alice`/`bob` remain the subjects; only how they get authenticated changes, not what the test asserts about namespace isolation.

## Verification contract

- `grep -rn "test-user:" tests/integration/` returns zero matches after migration (SC-005).
- `TestRepositoryAuthorization_TwoUserNamespaceIsolation` passes with identical assertions (permission-denied on cross-user delete, correct per-namespace repository listing, correct `createdBy` attribution) using real logins.
- The unrelated `/__test/resource-body` route and `namespaceOwnerAuthZ` type remain present and untouched (FR-015).
