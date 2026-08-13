# Quickstart: Verifying Namespace/Repository Deletion Ordering and System Repository Bootstrap

This walks through manually verifying all three user stories against a running
`gitstore-api` (`make api`), using GraphQL requests. Each step calls out which
functional requirement it exercises.

## 1. Verify system repository bootstrap (User Story 3, FR-007, FR-008)

```graphql
mutation {
  createNamespace(input: { identifier: "quickstart-041", tier: USER }) {
    namespace { id identifier }
  }
}
```

Immediately after, list its repositories:

```graphql
query {
  repositories(namespaceId: "<id from previous response>") {
    edges { node { name } }
    totalCount
  }
}
```

**Expected**: `totalCount` is `1`, and the single repository's `name` is the
well-known system repository name (`gitstore-system`). Before this feature ships,
`totalCount` is `0` — this is the regression test for FR-007.

Re-run the same `createNamespace` mutation with the same identifier a second time
(simulating a client retry). **Expected**: the mutation fails on the existing
identifier-uniqueness check (unchanged behavior), and a subsequent `repositories`
query still shows exactly `1` repository, not `2` — this is the regression test for
FR-008 (no duplicate system repository from a retried creation attempt, verified via
research.md Decision 4's create-and-treat-`ErrAlreadyExists`-as-success path, exercised
indirectly through the idempotent provisioning step even though the outer mutation
itself rejects on the namespace identifier collision).

## 2. Verify namespace deletion rejection (User Story 1, FR-001, FR-002, FR-003)

With the namespace from step 1 (which now has at least its system repository):

```graphql
mutation {
  deleteNamespace(input: { identifier: "quickstart-041" }) {
    deletedIdentifier
  }
}
```

**Expected**: the mutation returns an error containing `contains repositories and
cannot be deleted`. A follow-up `namespace(by: {identifier: "quickstart-041"})` query
still returns the namespace (FR-001, FR-002).

Delete the system repository first (see step 3's precondition — you'll need it to
have zero catalog resources), then delete all remaining repositories in the
namespace, then retry `deleteNamespace`. **Expected**: it now succeeds, and the
follow-up `namespace(by: ...)` query returns `null` (FR-003).

## 3. Verify repository deletion rejection (User Story 2, FR-004, FR-005, FR-006)

Push a category (or product, variant, or collection) into the system repository via
git, so it gets admitted (see `docs/categories/category-taxonomy-spec.md` for the
frontmatter shape), then attempt:

```graphql
mutation {
  deleteRepository(input: { repositoryId: "<system repo Relay ID>" }) {
    deletedRepositoryId
  }
}
```

**Expected**: the mutation returns an error containing `contains catalog resources
and cannot be deleted`. A follow-up `repository(by: {id: ...})` query still returns
the repository, and its git storage is still present on disk (FR-004, FR-005).

Delete the admitted category via git push (delete the corresponding file and push),
then retry `deleteRepository`. **Expected**: it now succeeds — storage is removed and
the repository is no longer queryable (FR-006).
