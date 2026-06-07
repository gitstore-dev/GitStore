# Quickstart: Collection Resource Contract

**Branch**: `022-collection-resource-contract` | **Date**: 2026-06-07

This guide shows how to author, push, and query a `Collection` resource end-to-end using the git-backed workflow.

---

## Prerequisites

- Running GitStore stack (`make dev` or `make compose`)
- A namespace and repository already bootstrapped (`make bootstrap`)
- A valid bearer token (`make bootstrap-token ADMIN_PASSWORD=<password>`)
- `git` clone of the catalog repository

---

## Step 1 — Author a Collection document

Create a Markdown file in your catalog repository. The filename becomes part of the git history but the canonical name is `metadata.name`.

```markdown
---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Collection
metadata:
  name: apple-laptops
  namespace: gitstore-test
  labels:
    gitstore.dev/featured: "true"
spec:
  title: Apple Laptops
  media:
  - fileRef:
      name: collection-hero
      kind: File
      optional: true
  selector:
    matchLabels:
      gitstore.dev/brand: apple
    matchExpressions:
    - key: gitstore.dev/product-type
      operator: In
      values:
      - laptop
      - notebook
---

A curated selection of Apple laptop computers for professional and personal use.
```

Save as `collections/apple-laptops.md` in your catalog repository.

---

## Step 2 — Push to the catalog repository

```bash
git add collections/apple-laptops.md
git commit -m "feat(catalog): add apple-laptops collection"
git push origin main
```

The pre-receive hook validates the document. If `spec.title` is missing or the selector has invalid operators, the push is rejected with a descriptive error.

---

## Step 3 — Query the Collection via GraphQL

```graphql
query {
  collection(by: { namespacePath: { namespace: "gitstore-test", name: "apple-laptops" } }) {
    id
    metadata {
      name
      namespace
      uid
      generation
      creationTimestamp
      labels { key value }
    }
    spec {
      title
      selector {
        matchLabels { key value }
        matchExpressions { key operator values }
      }
      media {
        fileRef { name kind optional }
      }
    }
    status {
      observedGeneration
      conditions { type status reason message }
      resolved {
        memberCount
      }
    }
  }
}
```

---

## Step 4 — Paginate matched products

```graphql
query {
  collection(by: { namespacePath: { namespace: "gitstore-test", name: "apple-laptops" } }) {
    metadata { name }
    status { resolved { memberCount } }
    products(first: 20) {
      edges {
        node {
          id
          metadata { name }
          spec { title }
        }
      }
      pageInfo {
        hasNextPage
        endCursor
      }
    }
  }
}
```

Subsequent pages pass `after: "<endCursor>"`. All pages in a single traversal reflect membership as evaluated when the first page was requested (snapshot-at-query-time semantics).

---

## Step 5 — Update the selector

Edit `collections/apple-laptops.md` to narrow the selector:

```yaml
spec:
  selector:
    matchLabels:
      gitstore.dev/brand: apple
      gitstore.dev/product-type: laptop
```

Push the change:

```bash
git add collections/apple-laptops.md
git commit -m "feat(catalog): narrow apple-laptops selector to laptops only"
git push origin main
```

After push, `status.resolved.memberCount` will decrease to reflect the narrower selector.

---

## Validation Errors

| Error | Cause | Fix |
|-------|-------|-----|
| `spec.title is required` | `spec.title` missing or empty | Add `title:` to `spec` |
| `kind "Collection" is not a recognized catalog resource type` | Wrong `kind` value | Use `kind: Collection` |
| `matchExpressions[0].operator must be one of In, NotIn, Exists, DoesNotExist` | Invalid operator | Use a supported operator |
| `matchExpressions[0].values must be non-empty for operator In` | `In` with empty values | Add at least one value |
| `spec.selector.targetRef.kind must be Product` | Unsupported target kind | Remove `targetRef` or set `kind: Product` |

---

## Example Collection with no selector

A collection with no selector admits successfully but always resolves to zero members:

```markdown
---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Collection
metadata:
  name: placeholder-collection
spec:
  title: Placeholder (no members yet)
---
```

This is useful for creating a named collection before products with matching labels exist.
