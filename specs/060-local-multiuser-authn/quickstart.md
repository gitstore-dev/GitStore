# Quickstart: Local Multi-User AuthN + UserDir Provider (`static-users`)

## Test-first implementation order

1. **`staticusers.loadUsers`/`Reload` + UserDir read methods** (`gitstore-api/internal/auth/provider/staticusers`): add failing tests for missing file, malformed YAML, wrong `version`, duplicate username, missing/empty `password_hash`, and for `GetBySubject`/`ListGroups`/`SearchUsers` (known user, unknown user → `ErrUserNotFound`, case-insensitive search). Implement `users.go` and the UserDir side of `provider.go` until green.
2. **`StaticUsersProvider.Authenticate`/session lifecycle**: add failing tests for Basic Auth success/unknown-user/wrong-password, Bearer verify success/expired/foreign-signature, `RevokeSession`, `RefreshSession` grace-window behavior, `IssueSession` — mirroring `staticadmin_test.go`'s former coverage shape (that file is deleted as part of this same change). Implement the AuthN side of `provider.go` until green.
3. **`RBACLocalProvider.HasAnyRoleBindingFor`**: add a failing test asserting it returns `true` when any given username is a `role_bindings` key and `false` otherwise, with zero change to `Authorize`'s existing test coverage. Implement it in `rbaclocal/provider.go` until green.
4. **`validateAuthChainConfig`**: add failing tests covering (a) `static-users` in chain + empty `auth.jwt.secret` → error; (b) `static-users` absent + empty `auth.jwt.secret` → no error; (c) empty `auth.grpc.hmac_secret` → error regardless of chain contents. Remove `JWTConfig.Secret`'s `validate:"required"` tag and implement the function until green.
5. **Migration-safety check**: add a failing test for `buildProviderRegistry` asserting construction fails when `static-users` + `rbac-local` are both configured and no configured username has a `role_bindings` entry, succeeds when `authz.provider` is `allow-all` under the same user/policy configuration, and succeeds when at least one configured username has an entry. Implement the check until green.
6. **Config/wiring cutover**: remove `AuthConfig.Admin`/`UserConfig`; add `AuthConfig.StaticUsers`; update `auth.authn.chain`'s default in both `config.go` and `server.go`; replace `buildProviderRegistry`'s `"static-admin"` case with `"static-users"` (including UserDir wiring); update `MarshalLogObject`.
7. **Delete `staticadmin`**: remove the package; fix the resulting compile errors in the three test files that directly construct it (`cmd/server/main_test.go`, `middleware/security/secure_test.go`, `graph/resolver/auth_resolvers_test.go`) by switching to `staticusers.New(...)`; relabel the cosmetic `"static-admin"` string literals in the four label-only test files.
8. **Harness migration** (User Story 4): per `contracts/backdoor-retirement.md`, remove `testUserAuthN`, migrate the harness's own bootstrap identity from `staticadmin.New` to `staticusers.New`, fix `namespaceOwnerAuthZ`'s `principal.IsAdmin()` check, and migrate `TestRepositoryAuthorization_TwoUserNamespaceIsolation` and its helpers to real logins. Confirm `grep -rn "test-user:\|staticadmin\|static-admin" tests/integration/` returns zero matches.

## Manual verification — fresh (non-migration) setup

```bash
# 1. Generate bcrypt hashes for two local users (unchanged tool)
cd gitstore-api
go run ./cmd/gitctl hash-password 'alice-password'
go run ./cmd/gitctl hash-password 'bob-password'

# 2. Write gitstore-api/users.yaml
cat > users.yaml <<'EOF'
version: v1
users:
  - username: alice
    password_hash: "<hash from step 1>"
    display_name: "Alice Doe"
    email: "alice@example.com"
  - username: bob
    password_hash: "<hash from step 1>"
EOF

# 3. Add role_bindings for alice/bob to gitstore-api/policy.yaml (existing file)
#    role_bindings:
#      "alice": [admin]
#      "bob": [developer]

# 4. Configure the chain (gitstore-api/.env) — static-users is now the default local provider
cat >> .env <<'EOF'
GITSTORE_AUTH__AUTHN__CHAIN=static-users,anonymous
GITSTORE_AUTH__AUTHZ__PROVIDER=rbac-local
GITSTORE_AUTH__USERDIR__PROVIDER=static-users
GITSTORE_AUTH__STATICUSERS__USERS_FILE=users.yaml
EOF
# Note: GITSTORE_AUTH__JWT__SECRET is still required here — static-users is in the chain.
# It would NOT be required if the chain were, e.g., ["anonymous"] alone.

# 5. Start the stack
cd ..
make dev   # or: make compose

# 6. Log in as alice and bob independently
curl -s -X POST http://localhost:4000/graphql \
  -H 'Content-Type: application/json' \
  --data '{"query":"mutation($u:String!,$p:String!){ login(input:{username:$u,password:$p}) { token { accessToken } } }","variables":{"u":"alice","p":"alice-password"}}'
```

## Manual verification — migrating an existing `static-admin` deployment

```bash
# 1. Note the existing admin credential (before upgrading past this spec).
#    Old: GITSTORE_AUTH__ADMIN__USERNAME, GITSTORE_AUTH__ADMIN__PASSWORD_HASH (already a bcrypt hash).

# 2. Reuse the existing hash directly in gitstore-api/users.yaml (no need to regenerate
#    if you don't want to change the password — bcrypt hashes are provider-agnostic):
cat > gitstore-api/users.yaml <<'EOF'
version: v1
users:
  - username: admin          # or whatever GITSTORE_AUTH__ADMIN__USERNAME was
    password_hash: "<the existing GITSTORE_AUTH__ADMIN__PASSWORD_HASH value>"
EOF

# 3. THIS STEP IS NOT OPTIONAL — add the role_bindings entry, or the server will refuse
#    to start once static-users + rbac-local are both configured (FR-013):
#    policy.yaml:
#      role_bindings:
#        "admin": [admin]

# 4. Update the chain and remove the legacy keys from gitstore-api/.env:
#    - GITSTORE_AUTH__AUTHN__CHAIN=static-users,anonymous   (was: static-admin,anonymous)
#    - remove GITSTORE_AUTH__ADMIN__USERNAME / GITSTORE_AUTH__ADMIN__PASSWORD_HASH (now unused)
#    - GITSTORE_AUTH__JWT__SECRET stays as-is — static-users reuses it unchanged
#    - GITSTORE_AUTH__USERDIR__PROVIDER=static-users (explicitly selects the same users file for UserDir)
#    - GITSTORE_AUTH__STATICUSERS__USERS_FILE=users.yaml

# 5. Start the stack. If step 3 was skipped, the server fails fast with an error naming
#    the missing role_bindings entry, instead of starting and silently denying every
#    request from the "admin" identity.
make dev
```

> **Authenticating `gitstore-controller-manager` after this migration** — do **not** create a
> `users.yaml` entry for the controller. `users.yaml` identities are human-shaped
> (username + bcrypt password, HTTP Basic Auth) and a controller must never be
> indistinguishable from a human operator in the audit trail. The controller's supported
> credential path is spec 061's (`061-controller-serviceaccount-auth`, PR #409)
> `serviceaccount-jwt` provider: register a `ServiceAccount`, enroll its public key, and
> exchange a signed assertion via `issueServiceAccountToken` for a short-lived token to
> place in `GITSTORE_CONTROLLER__API_TOKEN`. See spec.md's Dependencies section
> (DEP-001/002). If spec 061 has not yet landed in your deployment's release, this is a
> known gap tracked by that spec — not a reason to mint a human credential for a machine.

## Regenerating a hash for the Makefile-based bootstrap flow

```bash
# Replaces the removed `make gen-admin-password`:
make hash-static-user-password PASSWORD='a-new-password'
# Prints the bcrypt hash, plus a reminder to add it to users.yaml and add/confirm the
# matching role_bindings entry in policy.yaml. `make bootstrap-token`/`bootstrap-namespace`/
# `bootstrap-repository` keep using the ADMIN_USERNAME/ADMIN_PASSWORD Makefile variables
# unchanged — they now describe whichever static-users identity is used to bootstrap.
```
