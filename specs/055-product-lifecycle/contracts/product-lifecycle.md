# Contract: Private Product GraphQL Lifecycle

This contract is additive. Product GraphQL is a private current-catalogue API;
it does not expose a public snapshot-serving surface.

```graphql
input ProductLifecycleSpecInput {
  state: ProductLifecycleState = ACTIVE
}

enum ProductLifecycleState { ACTIVE RETIRED }
enum ProductDeletionOutcome { TERMINATION_STARTED ALREADY_TERMINATING }

input ProductSpecInput {
  title: String
  categoryRef: CatalogObjectReferenceInput
  tags: [String!]
  media: [MediaDefinitionInput!]
  options: [ProductOptionDefinitionInput!]
  lifecycle: ProductLifecycleSpecInput
}

input CreateProductInput {
  apiVersion: String! = "catalog.gitstore.dev/v1beta1"
  kind: String! = "Product"
  metadata: MetadataInput!
  spec: ProductSpecInput!
  body: String
}

input UpdateProductInput {
  apiVersion: String! = "catalog.gitstore.dev/v1beta1"
  kind: String! = "Product"
  metadata: MetadataInput!
  spec: ProductSpecInput!
  body: String
}

input DeleteProductInput {
  id: ID
}

type CreateProductPayload { product: Product }
type UpdateProductPayload { product: Product }

type DeleteProductPayload {
  "The current terminating Product envelope."
  product: Product

  "Whether the request began termination or observed an existing workflow."
  outcome: ProductDeletionOutcome!
}

extend type Mutation {
  createProduct(input: CreateProductInput!): CreateProductPayload!
  updateProduct(input: UpdateProductInput!): UpdateProductPayload!
  deleteProduct(input: DeleteProductInput!): DeleteProductPayload!
  updateProductStatus(input: UpdateProductStatusInput!): UpdateProductStatusPayload!
}
```

## Mutation invariants

- No Product mutation input includes a repository or path field. Create authors
  the manifest in the namespace's `gitstore-system` repository. Update resolves
  the admitted Product's persisted repository and source path and writes the
  original manifest there, including when it was Git-pushed to a non-system
  repository; each waits for the shared admission result.
- `apiVersion` defaults to `catalog.gitstore.dev/v1beta1`; `kind` defaults to
  `Product`. Explicit mismatches are rejected.
- `DeleteProductInput` uses the standard `id: ID` resource-delete shape. The
  server resolves namespace, name, repository, and source path from the
  admitted Product before authorisation-sensitive lifecycle work begins.
- `DeleteProductPayload` follows Namespace's asynchronous-deletion shape: it
  returns the terminating Product envelope and a mandatory outcome of
  `TERMINATION_STARTED` or `ALREADY_TERMINATING`. It introduces no deprecated
  `deletedIdentifier` field because Product deletion is new API surface.
- `metadata.name` and `metadata.namespace` are immutable after creation. A
  rename is an explicit delete and create, never an in-place move.
- Inputs expose no UID, generation, resource version, status, owner reference,
  finalizer, deletion timestamp, provenance, or actor/timestamp fields.
- `deleteProduct` with blocking variants is rejected without a Git deletion or
  lifecycle write. An eligible delete removes the canonical manifest through
  admission, marks the hydrated Product terminating, and returns that Product.
  A repeated request returns the same terminating lifecycle view.
- Resolver errors preserve existing GraphQL error conventions and must not
  return a partial Product on failed Git admission.

## Private read and authorisation contract

`product`, `products`, node lookup, Product relationships/counts, and both
watch surfaces require Product read/watch authorisation in namespace scope
before a lookup, cursor parse, replay, or subscription registration. Mutations
require the matching Product author/update/delete capability. Status and
deletion completion are controller-only capabilities. Unauthorised callers get
no Product object, existence signal, cursor, or replay information.

Every returned Product retains the full existing resource envelope, including
`metadata.ownerReferences`, `metadata.finalizers`, and
`metadata.deletionTimestamp`. A Product `RETIRED` state remains readable to
authorised management callers and does not make it public.
