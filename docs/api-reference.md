# GitStore GraphQL API Reference

This reference documents the current GraphQL contract exposed by `gitstore-api`.

Catalogue reads are GraphQL-first. Catalogue writes are Git-driven today: author Markdown/frontmatter resources, commit them, and push through Git Smart HTTP. Category GraphQL mutations (`createCategory`, `updateCategory`, `deleteCategory`) commit to Git on the caller's behalf. Product, collection, and variant GraphQL write operations are intentionally not documented as supported catalogue write APIs while Git-backed CRUD over GraphQL is being finalized.

## Endpoint

| Item         | Value                              |
|--------------|------------------------------------|
| GraphQL URL  | `http://localhost:4000/graphql`    |
| Playground   | `http://localhost:4000/playground` |
| Method       | `POST`                             |
| Content type | `application/json`                 |

## Authentication

Public read access depends on resolver and deployment policy. Protected mutations require a JWT bearer token:

```http
Authorization: Bearer <token>
```

GitStore delegates OAuth2/OIDC federation to external identity providers. The GraphQL `login`
mutation is a local-provider convenience (for example `static-users`) and returns an OIDC-style
token payload. External providers such as `oidc-jwt` are expected to authenticate out-of-band and
present a token to GitStore for verification.

Login:

```graphql
mutation Login {
  login(input: { username: "admin", password: "<password>" }) {
    token {
      accessToken
      tokenType
      expiresIn
      refreshToken
      scope
      idToken
    }
  }
}
```

Refresh:

```graphql
mutation RefreshToken {
  refreshToken(input: { refreshToken: "<refresh-token>" }) {
    token {
      accessToken
      tokenType
      expiresIn
      refreshToken
      scope
      idToken
    }
  }
}
```

Logout:

```graphql
mutation Logout {
  logout {
    success
  }
}
```

`scope` requests are currently unsupported for local providers; send no scope to avoid a validation error.

## Operation Summary

### Queries

| Operation                                  | Purpose                                 |
|--------------------------------------------|-----------------------------------------|
| `node(id: ID!)`                            | Fetch one Relay node by global ID       |
| `nodes(ids: [ID!]!)`                       | Fetch multiple Relay nodes by global ID |
| `namespace(by: NamespaceBy!)`              | Fetch one namespace                     |
| `namespaces(...)`                          | List namespaces                         |
| `repository(by: RepositoryBy!)`            | Fetch one repository                    |
| `repositories(namespace: String!, ...)`    | List repositories in a namespace        |
| `product(by: ProductBy!)`                  | Fetch one product resource              |
| `products(namespace: String!, ...)`        | List products in a namespace            |
| `productVariant(by: ProductVariantBy!)`    | Fetch one product variant resource      |
| `productVariants(namespace: String!, ...)` | List product variants in a namespace    |
| `category(by: CategoryBy!)`                | Fetch one category resource             |
| `categories(namespace: String!, filter, ...)` | List categories in a namespace, or the subtree below one category |
| `collection(by: CollectionBy!)`            | Fetch one collection resource           |
| `collections(namespace: String!, ...)`     | List collections in a namespace         |

### Mutations

| Operation                                                     | Purpose                                                                                                                                                                              |
|---------------------------------------------------------------|--------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `login(input: LoginInput!)`                                   | Create an OIDC-style token response for local providers                                                                                                                              |
| `logout`                                                      | End the current session                                                                                                                                                              |
| `refreshToken(input: RefreshTokenInput!)`                     | Exchange a refresh token for a new OIDC-style token response                                                                                                                         |
| `createNamespace(input: CreateNamespaceInput!)`               | Create a namespace                                                                                                                                                                   |
| `deleteNamespace(input: DeleteNamespaceInput!)`               | Delete an empty namespace                                                                                                                                                            |
| `createRepository(input: CreateRepositoryInput!)`             | Create a repository in a namespace                                                                                                                                                   |
| `deleteRepository(input: DeleteRepositoryInput!)`             | Delete a repository and its storage                                                                                                                                                  |
| `createCategory(input: CreateCategoryInput!)`                 | Create a category by committing its manifest to the namespace's `gitstore-system` repository                                                                                         |
| `updateCategory(input: UpdateCategoryInput!)`                 | Update a category by committing to the repository and path it was admitted from                                                                                                      |
| `deleteCategory(input: DeleteCategoryInput!)`                 | Start foreground deletion of a category by removing its manifest from Git                                                                                                            |
| `completeCategoryDeletion(input: CompleteCategoryDeletionInput!)` | Controller-only completion of a terminating category's deletion                                                                                                                  |
| `updateCategoryStatus(input: UpdateCategoryStatusInput!)`     | Controller-only partial-merge write to a CategoryTaxonomy's `.status` sub-resource                                                                                                   |
| `updateNamespaceStatus(input: UpdateNamespaceStatusInput!)`   | Controller-only partial-merge write to a Namespace's `.status` sub-resource                                                                                                          |
| `updateProductStatus(input: UpdateProductStatusInput!)`       | Controller-only partial-merge write to a Product's `.status` sub-resource, including `resolved.category` (`{name, uid}`) and its declarative `CategoryTaxonomy` owner-reference sync |
| `updateRepositoryStatus(input: UpdateRepositoryStatusInput!)` | Controller-only partial-merge write to a Repository's `.status` sub-resource                                                                                                         |
| `updateResourceStatus(input: UpdateResourceStatusInput!)`     | Generic, kind-parameterized counterpart of the per-kind status mutations for CRD-defined kinds                                                                                       |

### Subscriptions

| Operation                                                                                                 | Purpose                                                                            |
|-----------------------------------------------------------------------------------------------------------|------------------------------------------------------------------------------------|
| `watchCategories(namespace: String, selector: LabelSelectorInput, resourceVersion: String)`               | List-then-watch stream of `CategoryTaxonomy` changes                               |
| `watchResources(kind: String!, namespace: String, selector: LabelSelectorInput, resourceVersion: String)` | Generic, kind-parameterized counterpart of `watchCategories` for CRD-defined kinds |

## Relay IDs

Types that implement `Node` expose opaque global IDs. Treat IDs as opaque strings and pass them back unchanged to `node`, `nodes`, selectors, filters, and mutation inputs typed as `ID`.

Human-readable selectors use namespace paths:

```graphql
query {
  product(
    by: {
      namespacePath: {
        namespace: "gitstore-test"
        name: "macbook-pro"
      }
    }
  ) {
    id
  }
}
```

## Query Operations

### node

```graphql
query GetNode($id: ID!) {
  node(id: $id) {
    id
    ... on Product {
      metadata {
        name
      }
      spec {
        title
      }
    }
  }
}
```

### nodes

File IDs returned by `file` also resolve through `node` and `nodes`, with the
same `file.read` authorization. Batch results retain input order, including
duplicate IDs and null entries for missing resources. File datastore failures
are reported as GraphQL errors, not successful missing-resource responses.

```graphql
query GetNodes($ids: [ID!]!) {
  nodes(ids: $ids) {
    id
    ... on Namespace {
      metadata {
        name
      }
      spec {
        title
        tier
      }
    }
    ... on Repository {
      metadata {
        name
      }
      spec {
        defaultBranch
      }
    }
  }
}
```

### namespace

```graphql
query GetNamespace {
  namespace(by: { name: "gitstore-test" }) {
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

### namespaces

```graphql
query ListNamespaces {
  namespaces(first: 20) {
    edges {
      cursor
      node {
        id
        metadata {
          name
        }
        spec {
          title
          tier
        }
        status {
          observedGeneration
        }
      }
    }
    pageInfo {
      hasNextPage
      endCursor
    }
  }
}
```

### repository

```graphql
query GetRepository {
  repository(
    by: {
      namespacePath: {
        namespace: "gitstore-test"
        name: "catalog"
      }
    }
  ) {
    id
    metadata {
      name
      namespace
    }
    spec {
      defaultBranch
    }
    status {
      resolved {
        storagePath
        storageClass
      }
    }
  }
}
```

### repositories

```graphql
query ListRepositories($namespace: String!) {
  repositories(namespace: $namespace, first: 20) {
    edges {
      cursor
      node {
        id
        metadata {
          name
        }
        spec {
          defaultBranch
        }
      }
    }
  }
}
```

### product

`Product` is the non-sellable parent descriptor. SKU, pricing, and inventory are on `ProductVariant`.

```graphql
query GetProduct {
  product(
    by: {
      namespacePath: {
        namespace: "gitstore-test"
        name: "macbook-pro"
      }
    }
  ) {
    id
    apiVersion
    kind
    metadata {
      name
      namespace
      uid
      resourceVersion
      generation
      creationTimestamp
      labels
      annotations
    }
    spec {
      title
      tags
      categoryRef {
        name
        kind
      }
      options {
        name
        title
        values
      }
    }
    status {
      observedGeneration
      conditions {
        type
        status
        reason
        message
      }
    }
  }
}
```

### products

```graphql
query ListProducts {
  products(namespace: "gitstore-test", first: 10) {
    edges {
      cursor
      node {
        id
        metadata {
          name
        }
        spec {
          title
          tags
        }
      }
    }
    pageInfo {
      hasNextPage
      hasPreviousPage
      startCursor
      endCursor
    }
  }
}
```

### productVariant

```graphql
query GetProductVariant {
  productVariant(
    by: {
      namespacePath: {
        namespace: "gitstore-test"
        name: "macbook-pro-16-m4-64gb-1tb"
      }
    }
  ) {
    id
    apiVersion
    kind
    metadata {
      name
      namespace
      uid
      resourceVersion
      generation
    }
    spec {
      sku
      title
      productRef {
        name
      }
      selectedOptions {
        name
        value
      }
      pricing {
        priceSet {
          name
          prices {
            name
            currencyCode
            amount
            priority
            strategy {
              type
            }
          }
        }
      }
      inventory {
        managed
        policy
      }
    }
    status {
      conditions {
        type
        status
        reason
      }
      resolved {
        selectedOptionsHash
        priceSet {
          name
          priceCount
          currencies
          strategies
        }
      }
    }
  }
}
```

### productVariants

```graphql
query ListProductVariants {
  productVariants(namespace: "gitstore-test", first: 20) {
    edges {
      cursor
      node {
        id
        metadata {
          name
        }
        spec {
          sku
          title
        }
      }
    }
  }
}
```

### category

Requires the `categoryTaxonomy.read` permission on the category's namespace.

```graphql
query GetCategory {
  category(
    by: {
      namespacePath: {
        namespace: "gitstore-test"
        name: "laptops"
      }
    }
  ) {
    id
    apiVersion
    kind
    metadata {
      name
      namespace
    }
    spec {
      title
      parentRef {
        name
      }
    }
    status {
      resolved {
        path
        depth
      }
    }
    parent {
      metadata {
        name
      }
    }
    children {
      metadata {
        name
      }
    }
  }
}
```

### categories

```graphql
query ListCategories {
  categories(namespace: "gitstore-test", first: 20) {
    edges {
      cursor
      node {
        id
        metadata {
          name
        }
        spec {
          title
        }
        status {
          resolved {
            path
            depth
          }
        }
      }
    }
  }
}
```

Without `filter`, categories are listed newest first.

#### Subtree filter

```graphql
query Laptops {
  categories(
    namespace: "gitstore-test"
    filter: { descendantOf: "electronics", includeSelf: true, maxDepth: 2 }
    first: 50
  ) {
    edges { node { metadata { name } status { resolved { depth path } } } }
    pageInfo { hasNextPage endCursor }
  }
}
```

| Field          | Meaning                                                                          |
|----------------|----------------------------------------------------------------------------------|
| `descendantOf` | Name of the category whose descendants are returned. An unknown name returns an empty connection. |
| `includeSelf`  | Include `descendantOf` itself as the first result. Default `false`.              |
| `maxDepth`     | Levels below `descendantOf` to return, 1 to 128. Omit for the whole subtree. Out of range fails with `BAD_USER_INPUT`. |

- Results are ordered by relative depth, then name.
- Hierarchy comes from each category's `status.resolved.path`. Categories that have not been reconciled yet (`status.resolved` is `null`) are not matched, and results are eventually consistent after an ancestor moves.
- Filtered and unfiltered listings use different cursors. A cursor from one mode passed to the other fails with `BAD_USER_INPUT`.
- Listing categories, with or without the filter, requires the `categoryTaxonomy.list` permission on the namespace.

### collection

```graphql
query GetCollection {
  collection(
    by: {
      namespacePath: {
        namespace: "gitstore-test"
        name: "featured-laptops"
      }
    }
  ) {
    id
    apiVersion
    kind
    metadata {
      name
      namespace
    }
    spec {
      title
      selector {
        matchLabels {
          key
          value
        }
      }
    }
    products(first: 10) {
      edges {
        node {
          metadata {
            name
          }
        }
      }
    }
  }
}
```

### collections

```graphql
query ListCollections {
  collections(namespace: "gitstore-test", first: 20) {
    edges {
      cursor
      node {
        id
        metadata {
          name
        }
        spec {
          title
        }
        status {
          resolved {
            memberCount
          }
        }
      }
    }
  }
}
```

## Mutation Operations

### login

See [Authentication](#authentication).

### logout

See [Authentication](#authentication).

### refreshToken

See [Authentication](#authentication).

### createNamespace

Creates a namespace.

```graphql
mutation CreateNamespace {
  createNamespace(
    input: {
      apiVersion: "gitstore.dev/v1beta1"
      kind: "Namespace"
      metadata: { name: "gitstore-test" }
      spec: { title: "GitStore Test", tier: USER }
    }
  ) {
    namespace {
      id
      metadata {
        name
      }
      spec {
        title
        tier
      }
      status {
        observedGeneration
      }
    }
  }
}
```

Input fields:

| Field                | Required | Notes                                          |
|----------------------|----------|-------------------------------------------------|
| `metadata.name`      | yes      | Globally unique DNS-label namespace identifier |
| `spec.title`         | no       | Human-friendly display title                   |
| `spec.tier`          | yes      | `USER` or `ORGANIZATION`                       |

### deleteNamespace

Starts foreground deletion by removing the Namespace manifest from
`gitstore-system/gitstore-system`. Ordinary repositories, including terminating
ones, and catalog resources in the namespace's system repository block deletion.
An empty system repository alone does not block it; controller completion removes
that repository before releasing the Namespace name.

```graphql
mutation DeleteNamespace($id: ID!) {
  deleteNamespace(
    input: {
            id: $id
    }
  ) {
        namespace { id metadata { name } status { conditions { type } } }
        outcome
  }
}
```

### createRepository

Creates a repository in a namespace.

```graphql
mutation CreateRepository($namespace: String!) {
  createRepository(
    input: {
      apiVersion: "gitstore.dev/v1beta1"
      kind: "Repository"
      metadata: { namespace: $namespace, name: "catalog" }
      spec: { defaultBranch: "main", visibility: PRIVATE }
    }
  ) {
    repository {
      id
      metadata {
        name
        namespace
      }
      spec {
        defaultBranch
      }
      status {
        resolved {
          storagePath
        }
      }
    }
  }
}
```

### deleteRepository

Starts foreground deletion by removing `repositories/<name>.md` from the
namespace's `gitstore-system` repository. Catalog resources block deletion.
Backing Git storage and the name reservation remain until controller completion.
Direct deletion of the system repository is rejected.

```graphql
mutation DeleteRepository($id: ID!) {
  deleteRepository(
    input: {
            id: $id
    }
  ) {
        repository { id metadata { name } status { conditions { type } } }
        outcome
  }
}
```

### createCategory

Creates a category by committing a `CategoryTaxonomy` manifest to the namespace's `gitstore-system` repository and admitting it. The same checks as a Git push apply. Requires `categoryTaxonomy.create`.

```graphql
mutation CreateCategory($input: CreateCategoryInput!) {
  createCategory(input: $input) {
    category { id metadata { name resourceVersion } spec { title } }
  }
}
```

```json
{
  "input": {
    "metadata": { "name": "laptops", "namespace": "gitstore-test" },
    "spec": { "title": "Laptops", "parentRef": { "name": "electronics" } },
    "body": "Category copy for laptops."
  }
}
```

`apiVersion` and `kind` default to `catalog.gitstore.dev/v1beta1` and `CategoryTaxonomy`. Omit `body` for an empty body. Fails with `ALREADY_EXISTS` if the name is taken.

### updateCategory

Updates a category by committing to the repository and path it was admitted from. Requires `categoryTaxonomy.update`. `metadata.name` and `metadata.namespace` identify the category and cannot change. Omit `body` to keep the current body. Fails with `NOT_FOUND` if the category does not exist, `FAILED_PRECONDITION` (`CATEGORY_TERMINATING`) if it is being deleted, and `CONFLICT` (`SUPERSEDED`) if another commit to the same file won.

```graphql
mutation UpdateCategory($input: UpdateCategoryInput!) {
  updateCategory(input: $input) {
    category { id metadata { generation resourceVersion } spec { title } }
  }
}
```

Changing `spec.parentRef` moves the category; its descendants' `status.resolved.path` converges as the controller reconciles them.

### deleteCategory

Starts foreground deletion by removing the category's manifest from Git. Requires `categoryTaxonomy.delete`. The request is blocked with `FAILED_PRECONDITION` (`CHILD_CATEGORIES_PRESENT`) while child categories exist. Assigned products never block it: the controller decouples them asynchronously and then finishes removal.

```graphql
mutation DeleteCategory($input: DeleteCategoryInput!) {
  deleteCategory(input: $input) {
    category { id metadata { deletionTimestamp } }
    outcome
  }
}
```

`outcome` is `TERMINATION_STARTED`, or `ALREADY_TERMINATING` for a repeat request, which creates no commit. See the [deletion runbook](runbooks/categorytaxonomy-deletion.md).

### completeCategoryDeletion

Controller-only. Finishes foreground deletion of a terminating category once its children are gone and its products are decoupled. Requires `categoryTaxonomy.purge`. `resourceVersion` must equal the category's current value, otherwise the request fails with `CONFLICT` (`RESOURCE_VERSION_CONFLICT`). Fails with `FAILED_PRECONDITION` (`CATEGORY_NOT_TERMINATING`, `CHILD_CATEGORIES_PRESENT` or `PRODUCT_DECOUPLING_INCOMPLETE`) when the category is not ready to be removed. Returns `{ id }` of the removed category.

### Completion identity

`completeNamespaceDeletion`, `completeRepositoryDeletion`, `completeProductDeletion`,
and `completeCategoryDeletion` all require `input.id: ID!`: the resource's opaque
Relay Node `id`, also exposed as `metadata.uid`. Raw datastore UIDs and IDs of a
different Node type are rejected. CategoryTaxonomy uses the GraphQL `Category`
Node type. The input field is `id`, not `uid`; output `metadata.uid` is unchanged.

Supply the observed `name` and `resourceVersion`, plus `namespace` for namespaced
resources. Completion checks that the ID, route, and version identify the same
incarnation before finalizing it. A stale request cannot finalize a same-name
replacement, even if its version matches. Controller authorization, dependent
checks, and finalizer safeguards still apply. Other resource kinds do not expose
a separate deletion-completion mutation.

### updateCategoryStatus

Controller-only, partial-merge write to a `CategoryTaxonomy`'s `.status` sub-resource. Only non-null input fields are changed; existing status fields not mentioned in the input are left unchanged. Requires `resourceVersion` to match the resource's current value, or the request fails with a `CONFLICT` error whose diagnostic reason is `RESOURCE_VERSION_CONFLICT` and whose message includes the current version (`current resourceVersion is N`). Requires controller-level authorization (`categoryTaxonomy.status.write`), independent of whether `resourceVersion` matches. Never alters `.spec` or author-controlled `.metadata` — the input type has no such fields.

```graphql
mutation UpdateCategoryStatus($input: UpdateCategoryStatusInput!) {
  updateCategoryStatus(input: $input) {
    category {
      metadata {
        resourceVersion
      }
      status {
        observedGeneration
        conditions {
          type
          status
        }
      }
    }
    hasMoreProductDependents
  }
}
```

A resource that no longer exists returns a GraphQL error with `extensions.code == "NOT_FOUND"`, distinct from `CONFLICT` ("someone else changed it first").

### updateResourceStatus

Generic, kind-parameterized counterpart of `updateCategoryStatus` for CRD-defined kinds that have no compile-time-known `resolved` shape. Same partial-merge/precondition/authorization semantics; the resource payload and `resolved` field are JSON-boxed instead of strongly typed.

```graphql
mutation UpdateResourceStatus($input: UpdateResourceStatusInput!) {
  updateResourceStatus(input: $input) {
    object
    conflict {
      currentResourceVersion
    }
  }
}
```

### watchCategories

Subscription: an ordered stream of `CategoryTaxonomy` changes. Pass no `resourceVersion` to start receiving only future changes; pass a previously-observed `resourceVersion` to resume. A cursor that predates the server's retained event window terminates the subscription with a GraphQL error carrying `extensions.code == "WATCH_EXPIRED"` — the caller must re-list (via `categories(namespace: ...)`) and resume from a fresh cursor rather than assume it is caught up.

```graphql
subscription WatchCategories($namespace: String, $resourceVersion: String) {
  watchCategories(namespace: $namespace, resourceVersion: $resourceVersion) {
    type
    name
    resourceVersion
    category {
      metadata {
        name
        resourceVersion
      }
      status {
        conditions {
          type
          status
        }
      }
    }
  }
}
```

### watchResources

Generic, kind-parameterized counterpart of `watchCategories` for CRD-defined kinds. Same list-then-watch/resume/expiry semantics; the resource payload is JSON-boxed via `object`.

```graphql
subscription WatchResources($kind: String!, $resourceVersion: String) {
  watchResources(kind: $kind, resourceVersion: $resourceVersion) {
    type
    kind
    name
    resourceVersion
    object
  }
}
```

## Catalogue Writes

Use Git for catalogue writes:

```bash
git add products variants categories collections
git commit -m "Update catalogue"
git push origin main
```

The API reference intentionally omits catalogue CRUD mutation docs. Some schema fields may remain for compatibility or transitional UI work, but they are not the supported write path for catalogue resources.

Git-backed catalogue resources are projected into GraphQL after post-receive admission. Resource identity is `apiVersion`, `kind`, namespace, and `metadata.name`; the source file path is provenance only. File moves preserve `metadata.uid`. Spec or Markdown body edits increment `metadata.generation` and `metadata.resourceVersion`; path-only moves and metadata-only edits preserve `generation` and increment `resourceVersion`. Admission deletes Product, Collection, ProductVariant, and File records only when the persisted `resourceVersion` still matches the version observed during ownership validation; a concurrent update rejects the push instead of deleting the newer record. Deleted files disappear from GraphQL reads after admission deletes the stored identity. A later delete/re-add receives a new UID and starts again at `generation=1`.

Post-receive admission is asynchronous and cannot reject an already accepted Git push. DB-backed conflicts such as duplicate variant SKUs leave the existing stored resource unchanged and skip the conflicting incoming resource.

## Types

### Namespace

```graphql
type Namespace implements Node {
  id: ID!
  apiVersion: String!
  kind: String!
  metadata: NamespaceMetadata!
  spec: NamespaceSpec!
  status: NamespaceStatus!
}
```

`NamespaceMetadata` intentionally omits `namespace` because Namespace is a
top-level resource. Existing datastore rows hydrate with
`resourceVersion: "1"`, `generation: 1`, and an initial non-null status of
`observedGeneration: 0`, `lastAppliedRevision: null`, and `conditions: []`.
See [Namespace Resource Contract](namespace/namespace-spec.md).

### Repository

```graphql
type Repository implements Node {
  id: ID!
  apiVersion: String!
  kind: String!
  metadata: ObjectMeta!
  spec: RepositorySpec!
  status: RepositoryStatus!
}

type RepositorySpec {
  defaultBranch: String!
  visibility: RepositoryVisibility!
  pushPolicy: RepositoryPushPolicy!
}

type RepositoryPushPolicy {
  maxPackSizeBytes: Long!
  maxFileSizeBytes: Long!
  receivePackHooks: ReceivePackHookDefaults
  schemaValidation: SchemaValidationDefaults
  admissionControl: AdmissionControlDefaults
}

type RepositoryStatus {
  observedGeneration: Int!
  lastAppliedRevision: String
  conditions: [Condition!]!
  resolved: ResolvedRepositoryDefinition!
}

type ResolvedRepositoryDefinition {
  storagePath: String!
  storageClass: String!
}
```

`apiVersion` is always `gitstore.dev/v1beta1`; `kind` is always `Repository`.
Legacy rows normalize to generation/resourceVersion `1` and an initial non-null
status. `spec.visibility` currently projects `PRIVATE`; extended push-policy
groups project null until Repository write/persistence semantics are added.
Existing maximum-size fields, including explicit zero values, project through
`spec.pushPolicy`. See
[Repository Resource Contract](repository/repository-spec.md).

### Product

```graphql
type Product implements Node {
  id: ID!
  apiVersion: String!
  kind: String!
  metadata: ObjectMeta!
  spec: ProductSpec!
  status: ProductStatus
}
```

### ProductVariant

```graphql
type ProductVariant implements Node {
  id: ID!
  apiVersion: String!
  kind: String!
  metadata: ObjectMeta!
  spec: ProductVariantSpec!
  status: ProductVariantStatus
  body: String
}
```

### Category

```graphql
type Category implements Node {
  id: ID!
  apiVersion: String
  kind: String
  metadata: ObjectMeta!
  spec: CategorySpec!
  status: CategoryTaxonomyStatus
  body: String
  parent: Category
  children: [Category!]!
  products(first: Int, after: String, last: Int, before: String): ProductConnection!
}
```

A category's position in the tree is `status.resolved { path depth childCount productCount }`. It is computed by the controller manager, is `null` until the first reconcile, and is eventually consistent after an ancestor moves. `Category.path` and `Category.depth` no longer exist.

`Category.products` is a paginated projection of Products assigned to that
category or any resolved descendant. It is eventually consistent while a
category move is being reconciled; callers should retry after the category's
resolved path changes rather than treating the connection as a live aggregate.

### Collection

```graphql
type Collection implements Node {
  id: ID!
  apiVersion: String
  kind: String
  metadata: ObjectMeta!
  spec: CollectionSpec!
  status: CollectionStatus
  body: String
  products(first: Int, after: String, last: Int, before: String): ProductConnection!
}
```

## Scalars

| Scalar | Meaning |
|---|---|
| `DateTime` | ISO 8601 timestamp |
| `Decimal` | String-backed decimal for exact monetary values |
| `JSON` | Arbitrary JSON value |

## Pagination

Connection fields use Relay-style cursor pagination:

```graphql
query PageProducts($after: String) {
  products(namespace: "gitstore-test", first: 10, after: $after) {
    edges {
      cursor
      node {
        id
      }
    }
    pageInfo {
      hasNextPage
      endCursor
    }
  }
}
```

Use `first` + `after` for forward pagination and `last` + `before` for backward pagination.

## Error Handling

GraphQL errors use the standard response shape:

```json
{
  "errors": [
    {
      "message": "repository not found",
      "path": ["repository"],
      "extensions": {
        "code": "NOT_FOUND"
      }
    }
  ],
  "data": {
    "repository": null
  }
}
```

Single-resource queries return `null` when the resource is not found.

### Mutation error envelope

Git-backed mutations (`createCategory`, `updateCategory`, `deleteCategory`, `completeCategoryDeletion`, and the Namespace mutations) and status writes return errors with at most four `extensions` keys:

| Key           | Present when                                                          | Value |
|---------------|-----------------------------------------------------------------------|-------|
| `code`        | always                                                                | One of the codes below |
| `diagnostics` | the error has detail (always for `ADMISSION_REJECTED`, `FAILED_PRECONDITION`, `CONFLICT`); omitted for `FORBIDDEN` | One or more entries, see below |
| `phase`       | only `ADMISSION_REJECTED`                                             | `PRE_RECEIVE`: nothing was committed. `POST_RECEIVE`: the commit exists, the last accepted generation is kept and `AdmissionAccepted` becomes `False` |
| `commit`      | only `phase = POST_RECEIVE`                                           | Commit SHA |

A diagnostic is `{ reason, message, level, file?, field? }`. `reason` is a stable SCREAMING_SNAKE string clients should branch on; `message` is for humans; `level` is `FAILURE`, `WARNING` or `NOTICE`; `file` and `field` are set for manifest problems. Clients must ignore unknown keys.

```json
{
  "message": "categories/laptops.md: spec.title is required; categories/laptops.md: spec.parentRef.name must not equal metadata.name",
  "path": ["createCategory"],
  "extensions": {
    "code": "ADMISSION_REJECTED",
    "phase": "PRE_RECEIVE",
    "diagnostics": [
      { "reason": "REQUIRED_FIELD", "message": "spec.title is required", "level": "FAILURE",
        "file": "categories/laptops.md", "field": "spec.title" },
      { "reason": "SELF_PARENT", "message": "spec.parentRef.name must not equal metadata.name", "level": "FAILURE",
        "file": "categories/laptops.md", "field": "spec.parentRef.name" }
    ]
  }
}
```

For `ADMISSION_REJECTED`, `message` is the `file: message` entries joined by `"; "`, byte-identical to the text a Git push prints for the same manifest. For other codes, `message` is free text.

| Code                  | Meaning |
|-----------------------|---------|
| `ADMISSION_REJECTED`  | The manifest failed schema, structural, immutability or policy checks |
| `ALREADY_EXISTS`      | The name is already taken |
| `NOT_FOUND`           | The target or a referenced namespace does not exist |
| `CONFLICT`            | Superseded by a concurrent commit or a resource-version change |
| `FAILED_PRECONDITION` | The target's state forbids the operation (terminating, blocking dependents, missing provenance, bootstrap resource) |
| `BAD_USER_INPUT`      | Malformed arguments, such as `maxDepth` out of range or a cursor from the other list mode |
| `FORBIDDEN`           | Authorization denied; nothing about the resource is disclosed |

Errors raised by other operations continue to use the codes in the table at the end of this section.

#### Category reasons

| `diagnostics[].reason` | `code` |
|------------------------|--------|
| `INVALID_ENVELOPE`, `REQUIRED_FIELD`, `INVALID_FIELD`, `SELF_PARENT`, `PARENT_TERMINATING`, `CROSS_NAMESPACE_REFERENCE`, `IMMUTABLE_NAME`, `IMMUTABLE_NAMESPACE`, `POLICY_DENIED`, `VALIDATION_FAILED` (fallback for unmapped checks) | `ADMISSION_REJECTED` |
| `CATEGORY_TERMINATING`, `CHILD_CATEGORIES_PRESENT`, `PROVENANCE_UNAVAILABLE`, `CATEGORY_NOT_TERMINATING`, `PRODUCT_DECOUPLING_INCOMPLETE` | `FAILED_PRECONDITION` |
| `CATEGORY_ALREADY_EXISTS` | `ALREADY_EXISTS` |
| `CATEGORY_NOT_FOUND` | `NOT_FOUND` |
| `SUPERSEDED`, `RESOURCE_VERSION_CONFLICT` | `CONFLICT` |
| `INVALID_ARGUMENT` | `BAD_USER_INPUT` |

`PROVENANCE_UNAVAILABLE` means `updateCategory` and `deleteCategory` cannot target the category: it has no recorded repository and path, or it was admitted from a ref other than the default branch (API mutations commit only to the default branch).

#### Warnings on success: `extensions.admission`

Non-fatal findings from a successful commit are returned at the top level of the response, never inside `data`:

```json
{
  "data": { "a": { "category": { "id": "..." } }, "b": { "category": { "id": "..." } } },
  "extensions": {
    "admission": [
      { "path": ["b"], "commit": "9f2c...",
        "diagnostics": [{ "reason": "MEDIA_UNRESOLVED", "message": "...", "level": "WARNING",
                          "file": "categories/tv.md", "field": "spec.media" }] }
    ]
  }
}
```

`path` is the response path of the mutation field, so it reflects aliases. The key is omitted when there is nothing to report and never overwrites other top-level extension keys.

### Other error codes

| Code               | Meaning                                       |
|--------------------|-----------------------------------------------|
| `NOT_FOUND`        | Requested resource does not exist             |
| `VALIDATION_ERROR` | Input validation failed                       |
| `CONFLICT`         | Requested change conflicts with current state |
| `INTERNAL_ERROR`   | Server error                                  |

## Related Docs

- [User Guide](user-guide.md)
- [Developer Guide](developer-guide.md)
- [Product Spec](products/product-spec.md)
- [ProductVariant Spec](products/product-variant-spec.md)
- [CategoryTaxonomy Spec](categories/category-taxonomy-spec.md)
- [Collection Spec](collections/collection-spec.md)
- [GraphQL schema files](../shared/schemas/)
