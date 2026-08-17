# Quickstart: Repository Resource Contract

## Generate GraphQL code

```bash
cd gitstore-api
go generate ./...
```

## Run focused tests

```bash
cd gitstore-api
go test ./internal/graph/resolver ./internal/datastore/memdb
```

With a configured Scylla test environment:

```bash
cd gitstore-api
go test ./internal/datastore/scylla
```

## Verify the additive read contract

Query an existing repository:

```graphql
query RepositoryContract($namespace: String!, $name: String!) {
  repository(by: { namespacePath: { namespace: $namespace, name: $name } }) {
    id
    apiVersion
    kind
    name
    defaultBranch
    storageClass
    storagePath
    createdAt
    createdBy
    updatedAt
    updatedBy
    metadata {
      name
      namespace
      uid
      resourceVersion
      generation
      creationTimestamp
    }
    spec {
      defaultBranch
      visibility
      pushPolicy {
        maxPackSizeBytes
        maxFileSizeBytes
        receivePackHooks {
          preReceive {
            enabled
          }
        }
        schemaValidation {
          phase
          timeoutSeconds
        }
        admissionControl {
          phase
          branchPattern
        }
      }
    }
    status {
      observedGeneration
      lastAppliedRevision
      conditions {
        type
        status
        observedGeneration
        lastTransitionTime
        reason
        message
      }
      resolved {
        storagePath
        storageClass
      }
    }
  }
}
```

Expected invariants:

- All legacy flat fields are still present and unchanged.
- Duplicate legacy output fields carry `@deprecated` guidance to their declarative replacements.
- `apiVersion` is `gitstore.dev/v1beta1`; `kind` is `Repository`.
- `metadata`, `spec`, `status`, `status.conditions`, and `status.resolved` are non-null.
- A repository with no stored contract fields reads as generation `1`, resourceVersion `"1"`, visibility `PRIVATE`, observedGeneration `0`, and an empty condition list.
- Rename preserves `id`, advances generation and resourceVersion, and updates legacy `name` plus `metadata.name`.
- Transfer preserves `id` and generation, advances resourceVersion, and updates `metadata.namespace`.
- Existing maximum pack/file sizes project through `spec.pushPolicy`; visibility is the reserved `PRIVATE` default and extended policy groups are null until a future write/persistence feature.
- This feature emits no Repository-specific condition types; the valid initial condition vocabulary is the empty set.

## Full API validation

```bash
cd gitstore-api
go test ./...
```
