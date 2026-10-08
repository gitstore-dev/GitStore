# Contract: GraphQL schema delta (`shared/schemas/category.graphqls`)

Docstrings in the real schema must not cite specs, ADRs or requirement IDs (`.claude/rules/no-internal-refs-in-user-facing-docs.md`). After editing, run `go generate ./...` in `gitstore-api`.

```graphql
extend type Query {
  """
  List categories with Relay cursor-based pagination. Without `filter`, ordering is
  newest first. With `filter`, results are the subtree below `filter.descendantOf`,
  ordered by relative depth and then name. Cursors from one mode are rejected by the other.
  """
  categories(
    namespace: String!
    filter: CategoryFilterInput
    first: Int
    after: String
    last: Int
    before: String
  ): CategoryConnection!
}

"""
Subtree filter. Hierarchy comes from each category's `status.resolved.path`:
categories that have not yet been reconciled are not matched, and results are
eventually consistent after an ancestor moves (see the `AncestorPathReady` condition).
"""
input CategoryFilterInput {
  """Name of the category whose descendants are returned. Unknown names return an empty connection."""
  descendantOf: String!
  """Include `descendantOf` itself as the first result."""
  includeSelf: Boolean = false
  """Maximum levels below `descendantOf` (1–128). Omit for the whole subtree."""
  maxDepth: Int
}

extend type Mutation {
  """Create a category by committing its manifest to the namespace's gitstore-system repository and admitting it."""
  createCategory(input: CreateCategoryInput!): CreateCategoryPayload!

  """Update a category by committing its manifest to the repository and path it was admitted from."""
  updateCategory(input: UpdateCategoryInput!): UpdateCategoryPayload!

  """
  Start foreground deletion by removing the category's manifest from Git. Blocked while
  child categories exist. Assigned products are decoupled asynchronously.
  """
  deleteCategory(input: DeleteCategoryInput!): DeleteCategoryPayload!
}

input CategorySpecInput {
  title: String!
  parentRef: CatalogObjectReferenceInput
  media: [MediaDefinitionInput!]
}

input CreateCategoryInput {
  apiVersion: String! = "catalog.gitstore.dev/v1beta1"
  kind: String! = "CategoryTaxonomy"
  metadata: ObjectMetaInput!
  spec: CategorySpecInput!
  """Markdown body. Omit for an empty body."""
  body: String
}

input UpdateCategoryInput {
  apiVersion: String! = "catalog.gitstore.dev/v1beta1"
  kind: String! = "CategoryTaxonomy"
  """`name` and `namespace` identify the category and cannot change."""
  metadata: ObjectMetaInput!
  spec: CategorySpecInput!
  """Markdown body. Omit to keep the current body."""
  body: String
}

type CreateCategoryPayload { category: Category }
type UpdateCategoryPayload { category: Category }

# DeleteCategoryInput { id: ID! } is unchanged.
type DeleteCategoryPayload {
  category: Category
  outcome: ResourceDeletionOutcome!
}

type Category implements Node {
  # path: [String!]!   REMOVED. Use status.resolved.path.
  # depth: Int!        REMOVED. Use status.resolved.depth.
  """Resolved parent; null for roots, unresolved parents, or before first reconcile."""
  parent: Category          # now resolved (gqlgen resolver: true)
  """Direct children (at most 100), ordered by name. Empty until children are reconciled."""
  children: [Category!]!    # now resolved (gqlgen resolver: true)
}
```

## Breaking changes (pre-1.0, release notes)

- `Category.path`, `Category.depth` removed.
- `DeleteCategoryPayload.deletedCategoryId`, `.orphanedProductIds` removed. Use `category`/`outcome`.
- Namespace mutation errors adopt the shared envelope: `NAMESPACE_*` codes are replaced by kind-neutral codes, and `phase: STRUCTURAL|POLICY`, `reason` and `reasons` are replaced by `diagnostics[].reason`. Reason values are unchanged.

## Authorization

| Field                 | Action                              | Scope source                                        |
|-----------------------|-------------------------------------|-----------------------------------------------------|
| `createCategory`      | `categoryTaxonomy.create`           | `input.metadata.{namespace,name}`                   |
| `updateCategory`      | `categoryTaxonomy.update`           | stored record: namespace, name, owner, repositoryID |
| `deleteCategory`      | `categoryTaxonomy.delete`           | stored record (replaces `category.delete`)          |
| `categories(filter:)` | `categoryTaxonomy.list` (unchanged) | namespace                                           |

All three mutations require an authenticated principal and are checked before any Git or datastore work.

## Error codes

All mutation errors use the four-key envelope in `admission-diagnostics.md`: `code`, then `diagnostics`, `phase` and `commit` where applicable. `NOT_FOUND`, `BAD_USER_INPUT` and `FORBIDDEN` already exist. `ADMISSION_REJECTED`, `ALREADY_EXISTS`, `CONFLICT` and `FAILED_PRECONDITION` are new kind-neutral codes. Namespace's `NAMESPACE_*` codes migrate onto them.

| Code                  | When (Category)                                                                             | `diagnostics[].reason`                                                       |
|-----------------------|---------------------------------------------------------------------------------------------|------------------------------------------------------------------------------|
| `ADMISSION_REJECTED`  | pre-receive or post-receive manifest check failed (`phase`, plus `commit` for post-receive) | see the reason table                                                         |
| `ALREADY_EXISTS`      | create of an existing name                                                                  | `CATEGORY_ALREADY_EXISTS`                                                    |
| `NOT_FOUND`           | update/delete target missing                                                                | `CATEGORY_NOT_FOUND`                                                         |
| `CONFLICT`            | superseded by another commit to the same file                                               | `SUPERSEDED`                                                                 |
| `FAILED_PRECONDITION` | target terminating (update), child categories present (delete), provenance unavailable      | `CATEGORY_TERMINATING`, `CHILD_CATEGORIES_PRESENT`, `PROVENANCE_UNAVAILABLE` |
| `BAD_USER_INPUT`      | `maxDepth` out of range; cursor from the other list mode                                    | `INVALID_ARGUMENT`                                                           |
| `FORBIDDEN`           | authorization denied                                                                        | none                                                                         |
