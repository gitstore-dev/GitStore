# Quickstart: Namespace Types — Remove Enterprise

**Branch**: `030-remove-enterprise-namespace` | **Date**: 2026-06-19

This guide shows the namespace API surface after the `enterprise` tier is removed.

## Valid namespace tiers

After this change, exactly two namespace tiers are supported:

| Tier           | Who can create         | Owns repositories? |
|----------------|------------------------|--------------------|
| `USER`         | Any authenticated user | Yes                |
| `ORGANIZATION` | Any authenticated user | Yes                |

## Create a user namespace

```graphql
mutation {
  createNamespace(input: {
    identifier: "alice"
    tier: USER
  }) {
    namespace {
      id
      identifier
      tier
      createdAt
    }
  }
}
```

Expected response:

```json
{
  "data": {
    "createNamespace": {
      "namespace": {
        "id": "<global-node-id>",
        "identifier": "alice",
        "tier": "USER",
        "createdAt": "2026-06-19T10:00:00Z"
      }
    }
  }
}
```

## Create an organization namespace

```graphql
mutation {
  createNamespace(input: {
    identifier: "acme-corp"
    displayName: "Acme Corporation"
    tier: ORGANIZATION
  }) {
    namespace {
      id
      identifier
      tier
    }
  }
}
```

## Rejected: enterprise namespace type

Attempting to create a namespace with `tier: ENTERPRISE` returns a validation error regardless of caller permissions:

```graphql
mutation {
  createNamespace(input: {
    identifier: "acme-enterprise"
    tier: ENTERPRISE   # ← schema error: ENTERPRISE is not a valid NamespaceTier value
  }) {
    namespace { id }
  }
}
```

Expected response (schema validation error, before the mutation handler is invoked):

```json
{
  "errors": [
    {
      "message": "Argument 'input' has invalid value ...",
      "locations": [...],
      "extensions": { "code": "GRAPHQL_VALIDATION_FAILED" }
    }
  ]
}
```

## List namespaces

```graphql
query {
  namespaces(first: 10) {
    edges {
      node {
        id
        identifier
        tier
        createdAt
      }
    }
    pageInfo {
      hasNextPage
      endCursor
    }
    totalCount
  }
}
```

## Notes for existing integrations

- The `parentEnterpriseId` field has been removed from the `Namespace` type. Clients querying this field will receive a schema validation error.
- The `parentEnterpriseIdentifier` input field has been removed from `CreateNamespaceInput`. Clients sending this field will receive a schema validation error.
- Both fields were never part of a stable public release, so this is not treated as a breaking change.
