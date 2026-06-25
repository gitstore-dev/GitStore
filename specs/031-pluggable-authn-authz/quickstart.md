# Quickstart: Pluggable AuthN/AuthZ Phase 1

## Prerequisites

- Existing local dev setup working (`make dev` starts without errors).
- `gitstore-api/.env` has `GITSTORE_AUTH__ADMIN__PASSWORD_HASH` set (run
  `make gen-admin-password ADMIN_PASSWORD=<pw>` if not).

## Local-fast profile (zero new config required)

Add the following to `gitstore-api/.env` (only the three new keys):

```bash
GITSTORE_AUTH__AUTHN__CHAIN=static-admin
GITSTORE_AUTH__AUTHZ__PROVIDER=allow-all
GITSTORE_AUTH__USERDIR__PROVIDER=none
```

Start the stack:

```bash
make dev
```

Verify the `allow-all` security warning appears in API logs:

```
WARN  SECURITY: authz provider is allow-all — ALL authorization checks are disabled. DO NOT use in production.
```

Run the bootstrap to confirm end-to-end auth still works:

```bash
make bootstrap ADMIN_PASSWORD=<your-password>
```

## Local-secure profile (RBAC enforcement)

Create a `policy.yaml` (or use the bundled default from `specs/031-pluggable-authn-authz/`):

```yaml
version: v1
roles:
  admin:
    allow: ["*"]
    deny: []
  developer:
    allow: ["namespace.read", "repository.read", "repository.write"]
    deny: []
default_deny: true
role_bindings:
  "admin":
    - admin
```

Update `gitstore-api/.env`:

```bash
GITSTORE_AUTH__AUTHN__CHAIN=static-admin
GITSTORE_AUTH__AUTHZ__PROVIDER=rbac-local
GITSTORE_AUTH__RBAC__POLICY_FILE=/path/to/policy.yaml
GITSTORE_AUTH__USERDIR__PROVIDER=none
```

Start:

```bash
make dev
```

## Running tests

```bash
cd gitstore-api
go test ./internal/auth/...
go test ./internal/auth/provider/...
go test ./internal/graph/resolver/...
```

All existing integration tests must still pass:

```bash
make test
```

## Verifying the fix for callerUsernameOrAnon

After implementing Phase 1, create a namespace while authenticated and check the
`createdBy` field:

```graphql
mutation {
  createNamespace(input: { name: "test-ns", displayName: "Test", tier: USER }) {
    namespace {
      name
      createdBy
    }
  }
}
```

`createdBy` must equal `"admin"` (not `"anon"`).

## Provider selection reference

| Env var                            | Phase 1 options                           |
|------------------------------------|-------------------------------------------|
| `GITSTORE_AUTH__AUTHN__CHAIN`      | `"static-admin"` (only option in Phase 1) |
| `GITSTORE_AUTH__AUTHZ__PROVIDER`   | `"allow-all"` or `"rbac-local"`           |
| `GITSTORE_AUTH__USERDIR__PROVIDER` | `"none"` (only option in Phase 1)         |
