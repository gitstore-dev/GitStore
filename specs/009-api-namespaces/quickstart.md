# Quickstart: API-Driven Namespace Lifecycle Management

**Feature**: `009-api-namespaces` | **Date**: 2026-05-15

This guide shows how to run the namespace API locally and exercise the key operations.

---

## Prerequisites

- Go 1.25 installed
- `gitstore-git-service` running (or `GITSTORE_GIT_GRPC_URI` pointing to a reachable instance; namespace operations do not require git-service, but the server startup will fail if it cannot dial)
- No ScyllaDB required for local development — the default backend is `memdb`

---

## Start the API Server

```bash
cd gitstore-api

# Copy example config (if not already present)
cp config.example.toml config.toml

# Generate a bcrypt password hash for the admin user
go run ./cmd/hashpw admin123

# Set required env vars (or add to config.toml)
export GITSTORE__AUTH__JWT__SECRET="dev-secret-change-me"
export GITSTORE__AUTH__ADMIN__USERNAME="admin"
export GITSTORE__AUTH__ADMIN__PASSWORD_HASH="<bcrypt hash from above>"

# Run the server (memdb backend by default)
go run ./cmd/server
```

The server starts at `http://localhost:4000`. The GraphQL Playground is at `http://localhost:4000/playground`.

---

## Authenticate

Namespace mutations require authentication. Obtain a JWT token:

```bash
curl -s -X POST http://localhost:4000/graphql \
  -H "Content-Type: application/json" \
  -d '{
    "query": "mutation { login(input: { username: \"admin\", password: \"<your-password>\" }) { session { token user { username isAdmin } } } }"
  }' \
  | jq -r '.data.login.session.token'
```

Export the token:

```bash
export TOKEN="<token from above>"
```

---

## Create a User-Space Namespace

```bash
curl -s -X POST http://localhost:4000/graphql \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{
    "query": "mutation { createNamespace(input: { identifier: \"alice\", tier: USER }) { namespace { id identifier tier createdAt createdBy } } }"
  }' | jq .
```

Expected response:
```json
{
  "data": {
    "createNamespace": {
      "namespace": {
        "id": "<uuid>",
        "identifier": "alice",
        "tier": "USER",
        "createdAt": "2026-05-15T12:00:00Z",
        "createdBy": "admin"
      }
    }
  }
}
```

---

## Create an Organisation Namespace with a Parent Enterprise

```bash
# 1. Create the enterprise namespace first (requires isAdmin)
curl -s -X POST http://localhost:4000/graphql \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{
    "query": "mutation { createNamespace(input: { identifier: \"acme-enterprise\", tier: ENTERPRISE, displayName: \"Acme Enterprise\" }) { namespace { id identifier tier } } }"
  }' | jq .

# 2. Create the org namespace with the parent enterprise
curl -s -X POST http://localhost:4000/graphql \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{
    "query": "mutation { createNamespace(input: { identifier: \"acme-engineering\", tier: ORGANISATION, parentEnterpriseIdentifier: \"acme-enterprise\" }) { namespace { id identifier tier parentEnterpriseId } } }"
  }' | jq .
```

---

## List All Namespaces

```bash
curl -s -X POST http://localhost:4000/graphql \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{
    "query": "query { namespaces { id identifier displayName tier createdAt createdBy } }"
  }' | jq .
```

---

## Get a Namespace by Identifier

```bash
curl -s -X POST http://localhost:4000/graphql \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{
    "query": "query { namespace(identifier: \"alice\") { id identifier tier createdAt createdBy updatedAt updatedBy } }"
  }' | jq .
```

---

## Get a Namespace by ID

Use the `id` returned by `createNamespace`, `namespaces`, or `namespace(identifier:)`.

```bash
curl -s -X POST http://localhost:4000/graphql \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{
    "query": "query { namespaceById(id: \"<namespace-id>\") { id identifier tier createdAt createdBy updatedAt updatedBy } }"
  }' | jq .
```

---

## Delete a Namespace

```bash
curl -s -X POST http://localhost:4000/graphql \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{
    "query": "mutation { deleteNamespace(input: { identifier: \"alice\" }) { deletedIdentifier } }"
  }' | jq .
```

Expected:
```json
{
  "data": {
    "deleteNamespace": {
      "deletedIdentifier": "alice"
    }
  }
}
```

---

## Error Cases

### Duplicate identifier (conflict)

```bash
# Create "alice" twice — second request returns a conflict error
curl -s -X POST http://localhost:4000/graphql \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"query": "mutation { createNamespace(input: { identifier: \"alice\", tier: USER }) { namespace { id } } }"}' | jq .
# Expected: errors[0].message contains "already exists"
```

### Invalid identifier

```bash
# Identifier with spaces or uppercase
curl -s -X POST http://localhost:4000/graphql \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"query": "mutation { createNamespace(input: { identifier: \"Invalid Name!\", tier: USER }) { namespace { id } } }"}' | jq .
# Expected: errors[0].message contains "invalid identifier"
```

### Enterprise namespace without admin role

Any authenticated user attempting to create an `ENTERPRISE` tier namespace when `isAdmin == false` receives a permission-denied error.

### Delete namespace not found

```bash
curl -s -X POST http://localhost:4000/graphql \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"query": "mutation { deleteNamespace(input: { identifier: \"unknown-ns\" }) { deletedIdentifier } }"}' | jq .
# Expected: errors[0].message contains "not found"
```

---

## Run Integration Tests

```bash
cd gitstore-api
go test -v -run TestNamespace ./tests/contract/...
```

The integration tests use the `memdb` backend by default and do not require Docker or ScyllaDB.

---

## Using ScyllaDB Backend (optional)

```bash
export GITSTORE__DATASTORE__BACKEND="scylla"
export GITSTORE__DATASTORE__SCYLLA__HOSTS="localhost:9042"
export GITSTORE__DATASTORE__SCYLLA__KEYSPACE="gitstore"

# Start ScyllaDB
docker run -d -p 9042:9042 scylladb/scylla:5.4

# Create keyspace (one-time)
docker exec -it <container> cqlsh -e "CREATE KEYSPACE IF NOT EXISTS gitstore WITH replication = {'class': 'SimpleStrategy', 'replication_factor': 1};"

# The server applies migrations automatically on startup
go run ./cmd/server
```
