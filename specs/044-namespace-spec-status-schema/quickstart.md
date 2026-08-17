# Quickstart: Namespace Resource Contract

## Goal

Implement the declarative Namespace read contract without adding persistent write semantics or datastore migrations.

## 1. Write failing contract tests

Add tests before editing the SDL or converter.

Required assertions:

- `Namespace` exposes `id`, `apiVersion`, `kind`, `metadata`, `spec`, and `status`.
- `NamespaceMetadata` has no `namespace` field.
- shared `ObjectMeta.namespace` remains non-null for namespace-scoped resources.
- `NamespaceMetadata` includes labels, annotations, UID, resource version, generation, creation timestamp, revision, owner references, and finalizers.
- policy defaults are typed and byte limits use `Long`.
- status is non-null and has no `phase` or `resolved`.
- the flat Namespace output fields remain present with deprecation reasons.

Run the targeted tests and confirm they fail:

```bash
cd gitstore-api
go test ./internal/graph/resolver ./internal/graph/scalar
```

## 2. Add failing projection tests

Cover conversion of an existing flat datastore row:

```text
ID           -> id and metadata.uid
Identifier   -> metadata.name
DisplayName  -> spec.title
Tier         -> spec.tier
CreatedAt    -> metadata.creationTimestamp
N/A          -> metadata.resourceVersion "1"
```

Assert empty maps/lists, generation `1`, null revision/default groups, and initial status `{observedGeneration: 0, lastAppliedRevision: null, conditions: []}`.

Also cover the deprecated flat fields mapping to their existing datastore values.

## 3. Update the authoritative SDL

Apply the reviewed contract from [contracts/namespace.graphqls](contracts/namespace.graphqls) to the shared schema source. Keep existing create/delete/query operations and add the declarative fields to their Namespace output type.

Do not:

- make shared `ObjectMeta.namespace` nullable;
- remove the deprecated flat Namespace fields;
- change mutation write behavior;
- edit generated gqlgen files manually.

## 4. Add the `Long` scalar

Map GraphQL `Long` to Go `int64` in `gitstore-api/gqlgen.yml` and implement explicit marshal/unmarshal behavior in the existing scalar package.

Scalar tests must cover:

- minimum and maximum `int64`;
- zero and normal byte sizes;
- overflow/underflow;
- fractional and string inputs;
- explicit GraphQL errors for invalid values.

## 5. Generate and implement

```bash
cd gitstore-api
go generate ./...
```

Implement the Namespace converter and resolver wiring after generation. Return both the preferred declarative fields and deprecated flat projections. Keep the datastore entity and both backends unchanged.

The admin GraphQL client does not currently consume Namespace. Its regeneration
is deferred until the stalled admin Phase 1 (Alpha) lands; this does not block
the API contract or integration consumers.

## 6. Verify the API shape

Example query:

```graphql
query NamespaceContract($by: NamespaceBy!) {
  namespace(by: $by) {
    id
    apiVersion
    kind
    metadata {
      name
      uid
      resourceVersion
      generation
      creationTimestamp
      revision
      finalizers
    }
    spec {
      title
      tier
      repositoryDefaults {
        visibility
        defaultBranch
      }
      pushPolicyDefaults {
        maxPackSizeBytes
        maxFileSizeBytes
      }
    }
    status {
      observedGeneration
      lastAppliedRevision
      conditions {
        type
        status
      }
    }
  }
}
```

Expected for a legacy row:

- constants `gitstore.dev/v1beta1` and `Namespace`;
- UID from the existing ID;
- resource version `"1"`;
- generation `1`;
- null revision and policy defaults;
- observed generation `0`;
- empty conditions.

## 7. Validate

Run the smallest covering commands first:

```bash
cd gitstore-api
go test ./internal/graph/resolver ./internal/graph/scalar
go test ./...
go build ./...
```

Then run integration and repository gates:

```bash
cd tests/integration
go test ./... -v

cd ../..
make pr-ready
```

## 8. Document

Create `docs/namespace/namespace-spec.md` from the manifest contract. Include:

- field ownership/mutability matrix;
- create and update manifests;
- hydrated API example;
- deprecation and future-major-removal note;
- links to GH#172, GH#173, GH#174, and GH#249 for deferred behavior.

Add an automated documentation-contract test that extracts and validates both example manifests against the schema-only Namespace field contract.
The same test must assert that the documentation defines `Ready`, `AdmissionAccepted`, and `DeletionBlocked` as the initial open-ended condition vocabulary.
