# Quickstart: Local Multi-User AuthN Provider (`static-users`)

## Test-first implementation order

1. **`staticusers.loadUsers`/`Reload`** (`gitstore-api/internal/auth/provider/staticusers`): add failing tests for missing file, malformed YAML, wrong `version`, duplicate username, and missing/empty `password_hash` — mirroring `rbaclocal_test.go`'s coverage shape. Implement `users.go` until green.
2. **`StaticUsersProvider.Authenticate`/session lifecycle**: add failing tests for Basic Auth success, unknown username, wrong password, Bearer verify success, Bearer verify against an expired token, Bearer verify against a foreign-signature token, `RevokeSession`, `RefreshSession` grace-window behavior, `IssueSession` — mirroring `staticadmin_test.go`'s coverage shape. Implement `provider.go` until green.
3. **Cross-provider token safety** (User Story 3): add a failing test asserting a `static-users`-issued token is rejected by a real `StaticAdminProvider.Authenticate` call, and a `static-admin`-issued token is rejected by a real `StaticUsersProvider.Authenticate` call. Confirm green once both providers are configured with distinct secrets/issuers.
4. **`ChainedAuthN.IssueSessionFor`** (`gitstore-api/internal/auth/registry.go`): add a failing test proving it routes to the provider whose `Name()` matches `principal.AuthMethod`, not the first provider in the chain that merely supports `IssueSession`. Add a failing `Login` resolver test (`auth_service_test.go` or equivalent) proving a `static-users` login issues a token signed by `static-users`, even with `static-admin` listed first in the chain. Implement `IssueSessionFor` and update the `Login` call site until green.
5. **Config wiring** (`gitstore-api/internal/config/config.go`, `gitstore-api/internal/app/server.go`): add a failing test asserting server construction fails when `static-users` is listed in `auth.authn.chain` but `auth.staticusers.jwt.secret` is empty. Add the config struct, defaults, known-keys entries, and the `buildProviderRegistry` `case "static-users":`. Extend the existing SIGHUP handler to also reload any active `static-users` provider.
6. **`rbac-local` role_bindings end-to-end** (User Story 2): add a failing integration/unit test configuring two `static-users` identities bound to two different roles via `policy.yaml`'s existing `role_bindings`, asserting different authorization outcomes for a role-differentiated action — with zero changes to `rbaclocal/provider.go` or `rbaclocal/policy.go`.
7. **Backdoor retirement** (User Story 4): per `contracts/backdoor-retirement.md`, remove `testUserAuthN` (both the standalone type and its embedded-source duplicate) from `tests/integration/namespace_contract_test.go`; wire a real `staticusers.New(...)` (with test-fixture `alice`/`bob` credentials) into the synthetic helper server's provider chain; migrate `TestRepositoryAuthorization_TwoUserNamespaceIsolation` and its helpers in `tests/integration/authz_repository_contract_test.go` to authenticate via real `login` calls. Confirm `grep -rn "test-user:" tests/integration/` returns zero matches.

## Manual verification

```bash
# 1. Generate bcrypt hashes for two local users (reuses the existing gitctl subcommand)
cd gitstore-api
go run ./cmd/gitctl hash-password 'alice-password'
go run ./cmd/gitctl hash-password 'bob-password'

# 2. Write gitstore-api/users.yaml
cat > users.yaml <<'EOF'
version: v1
users:
  - username: alice
    password_hash: "<hash from step 1>"
  - username: bob
    password_hash: "<hash from step 1>"
EOF

# 3. Add role_bindings for alice/bob to gitstore-api/policy.yaml (existing file)
#    role_bindings:
#      "admin": [admin]
#      "alice": [namespace-owner]
#      "bob": [developer]

# 4. Configure the chain (gitstore-api/.env, additive to the existing local-fast profile)
cat >> .env <<'EOF'
GITSTORE_AUTH__AUTHN__CHAIN=static-admin,static-users,anonymous
GITSTORE_AUTH__AUTHZ__PROVIDER=rbac-local
GITSTORE_AUTH__STATICUSERS__USERS_FILE=users.yaml
GITSTORE_AUTH__STATICUSERS__JWT__SECRET=dev-static-users-secret-change-me
GITSTORE_AUTH__STATICUSERS__JWT__ISSUER=gitstore-static-users
EOF

# 5. Start the stack
cd ..
make dev   # or: make compose

# 6. Log in as alice and bob independently
curl -s -X POST http://localhost:4000/graphql \
  -H 'Content-Type: application/json' \
  --data '{"query":"mutation($u:String!,$p:String!){ login(input:{username:$u,password:$p}) { token { accessToken } } }","variables":{"u":"alice","p":"alice-password"}}'

curl -s -X POST http://localhost:4000/graphql \
  -H 'Content-Type: application/json' \
  --data '{"query":"mutation($u:String!,$p:String!){ login(input:{username:$u,password:$p}) { token { accessToken } } }","variables":{"u":"bob","p":"bob-password"}}'

# 7. Confirm alice's token is rejected as an admin token and vice versa, e.g. by decoding
#    each JWT's header/payload and confirming distinct issuers, or by presenting alice's
#    token to any admin-only mutation and confirming it is denied unless role_bindings
#    explicitly grants alice the admin role.
```
