# GitStore User Guide

GitStore lets you manage a commerce catalogue as Markdown files in Git and read the admitted catalogue through GraphQL.

This guide treats GitStore as a running product. It focuses on what you do as a user: start the stack, create a namespace and repository, push catalogue manifests, query GraphQL, and troubleshoot common issues.

To grant users or groups access, see [Configure local authorization](authorization.md)
for policy examples and namespace and resource-name restrictions.

## Prerequisites

- Docker and Docker Compose
- Git
- Make, curl, and jq

## Start GitStore

Start the core stack:

```bash
make compose DETACH=1
make ps
```

The core stack exposes:

| Service | Local endpoint |
|---|---|
| GraphQL API | http://localhost:4000/admin/graphql |
| GraphQL Playground | http://localhost:4000/admin/playground |
| Git Smart HTTP | http://localhost:9000 |
| Controller manager health | http://localhost:5001/health |

## Manage Identities

With a capable UserDir configured and the required administration grants, use
the Admin API to look up Staff, Customers and ServiceAccounts. Human types share
the `User` interface; workloads remain `Actor` objects, not human users:

```graphql
query {
  actor(by: {username: "alice"}) {
    id
    ... on User {
      fullName
      metadata { name generation resourceVersion }
      groups
      lifecycle { state }
    }
  }
}
```

Choose exactly one lookup selector: `id`, human `username` or
`serviceAccountName`. A human and a workload with the same name are distinct
identities. Unified `actors` uses forward `first`/`after` Relay pages and rejects
`last`/`before`; the source-backed `serviceAccounts` connection supports both
directions and works without UserDir. Unified lists may omit workload mirrors
after missed delivery.

`createStaff` sends a setup invitation, and `resendStaffInvitation` resends it
while setup/verification remains unfinished. No password or setup link is
returned. `suspendUser`, `deactivateUser`, `reactivateUser` and `deleteUser`
administer human accounts. Verification and password setup govern activation;
reactivation does not restore revoked sessions. Lifecycle states are `INVITED`,
`PENDING_VERIFICATION`, `ACTIVE`, `INACTIVE` and `SUSPENDED`.

Use `assignUserGroup` and `removeUserGroup` with bare ordinary group names for
Staff and ServiceAccount memberships; these mutations return `Actor` payloads.
Customer memberships are configured globally: assigning a configured group is
a no-op, while individual removal or assignment outside the configuration is
rejected. Workload groups never change source keys, UID or enablement.

`updateUser` permits authorized full-name, email and consent changes, not
username/type/group/lifecycle changes. The previous verified deliverable address
remains in use until the replacement is verified. Administrative deliverability
updates use `setUserEmailDeliverability`. Staff revisions are best-effort;
Customer counters are `-1`/`"-1"`, not concurrency tokens. Changes can partially
succeed across directory, session and mail operations, and other API replicas
retain bounded freshness. See [identity administration](api-reference.md#identity-administration)
for permissions, payloads and error behavior.

## Bootstrap A Repository

Create the default namespace and repository:

```bash
make bootstrap TARGET=all ADMIN_PASSWORD=<admin-password>
```

By default this creates:

| Resource | Default |
|---|---|
| Namespace | `gitstore-test` |
| Repository | `catalog` |
| Default branch | `main` |

Common overrides:

```bash
make bootstrap \
  TARGET=all \
  ADMIN_PASSWORD=<admin-password> \
  NAMESPACE=my-store \
  NAMESPACE_DISPLAY_NAME="My Store" \
  REPOSITORY=catalog \
  DEFAULT_BRANCH=main
```

The command prints a clone URL like:

```text
http://localhost:9000/gitstore-test/catalog.git
```

Clone it:

```bash
git clone http://localhost:9000/gitstore-test/catalog.git catalog-work
cd catalog-work
```

## Author Catalogue Files

Catalogue resources are Markdown files with YAML frontmatter. You can organize files in directories that make sense for your team; GitStore detects resources by their frontmatter envelope.

A typical repository might look like:

```text
catalog-work/
├── products/
│   └── macbook-pro.md
├── variants/
│   └── macbook-pro-16-m4-64gb.md
├── categories/
│   └── laptops.md
└── collections/
    └── featured.md
```

### Enum spelling

Frontmatter enum values use UpperCamelCase; GraphQL enums use
SCREAMING_SNAKE_CASE. For example, author `Retired`, `Deny` and `NotIn` in Git;
GraphQL returns `RETIRED`, `DENY` and `NOT_IN`. Existing uppercase/lowercase
manifest spellings remain accepted through explicit compatibility mappings.
See the [enum spelling reference](../schemas/gitstore/v1beta1/README.md#enum-spelling)
for the complete list. Labels, names, option values, URIs and Markdown are
never case-converted.

### Product

`Product` is the non-sellable parent descriptor.

```markdown
---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  namespace: gitstore-test
  name: macbook-pro
  labels:
    gitstore.dev/brand: apple
    gitstore.dev/type: laptop
spec:
  title: MacBook Pro
  tags:
    - laptop
    - workstation
  options:
    - name: memory
      values: ["36gb", "64gb"]
    - name: storage
      values: ["1tb", "2tb"]
---

Portable workstation for demanding creative and engineering work.
```

### ProductVariant

`ProductVariant` is the purchasable SKU unit. Pricing and inventory live on variants, not on the parent product.

```markdown
---
apiVersion: catalog.gitstore.dev/v1beta1
kind: ProductVariant
metadata:
  namespace: gitstore-test
  name: macbook-pro-16-m4-64gb-1tb
spec:
  title: MacBook Pro 16 M4 Max 64GB 1TB
  sku: MBP-16-M4-64GB-1TB
  productRef:
    name: macbook-pro
  selectedOptions:
    - name: memory
      value: 64gb
    - name: storage
      value: 1tb
  pricing:
    priceSet:
      name: default
      prices:
        - name: usd
          currencyCode: USD
          amount: "3499.00"
          priority: 0
          strategy:
            type: Fixed
  inventory:
    managed: true
    policy: Deny
---

High-memory configuration for large builds, media pipelines, and local AI work.
```

### Category

```markdown
---
apiVersion: catalog.gitstore.dev/v1beta1
kind: CategoryTaxonomy
metadata:
  namespace: gitstore-test
  name: laptops
spec:
  title: Laptops
---

Portable computers and workstations.
```

### Collection

```markdown
---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Collection
metadata:
  name: featured-laptops
  namespace: gitstore-test
spec:
  title: Featured Laptops
  selector:
    matchLabels:
      gitstore.dev/type: laptop
---

Featured laptop products.
```

For the full resource formats, use the dedicated references:

- [Product spec](products/product-spec.md)
- [ProductVariant spec](products/product-variants.md)
- [CategoryTaxonomy spec](categories/category-taxonomy.md)
- [Collection spec](collections/collection-spec.md)
- [Push validation](products/push-validation.md)

## Push Changes

Commit and push your catalogue files:

```bash
git add products variants categories collections
git commit -m "Add initial laptop catalogue"
git push origin main
```

If validation fails, Git prints the rejection reason in the push output. Fix the files and push again.

After an accepted push, query GraphQL to inspect the admitted state. Post-receive admission runs asynchronously, so a push can succeed before every resource has been projected into the datastore. Some status fields may update after reconciliation, so repeat the query after a few seconds if you are checking computed status.

GitStore tracks catalog resources by `apiVersion`, `kind`, namespace, and `metadata.name`, not by file path. 
You can move a resource file to another directory without changing its `metadata.uid`. Editing the spec or Markdown body increments 
`generation` and `resourceVersion`; moving the file with the same content only increments `resourceVersion`. 
Deleting a resource file removes it from GraphQL reads after admission processes the commit. If you delete a resource and add 
the same identity again later, it receives a new UID.

Admission conflicts that require datastore state, such as a duplicate `ProductVariant.spec.sku`, cannot reject a Git push after refs have already been accepted. In that case the existing variant remains unchanged and the conflicting incoming variant is skipped. Check API logs when a pushed resource does not appear after structural validation passed.

## Query GraphQL

Open http://localhost:4000/admin/playground or use curl.

### List Products

```graphql
query ListProducts {
  products(namespace: "gitstore-test", first: 10) {
    edges {
      node {
        id
        metadata {
          name
          namespace
          generation
        }
        spec {
          title
          tags
          options {
            name
            values
          }
        }
        status {
          conditions {
            type
            status
          }
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

### Query A Variant By SKU Data

ProductVariant lookup is by global ID or namespace plus resource name. SKU is a field on the variant spec.

```graphql
query GetVariant {
  productVariant(
    by: {
      namespacePath: {
        namespace: "gitstore-test"
        name: "macbook-pro-16-m4-64gb-1tb"
      }
    }
  ) {
    id
    metadata {
      name
      namespace
    }
    spec {
      sku
      title
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
    }
  }
}
```

### List Categories

```graphql
query ListCategories {
  categories(namespace: "gitstore-test", first: 20) {
    edges {
      node {
        metadata {
          name
        }
        spec {
          title
        }
        path
        depth
        children {
          metadata {
            name
          }
        }
      }
    }
  }
}
```

### List Collections

```graphql
query ListCollections {
  collections(namespace: "gitstore-test", first: 20) {
    edges {
      node {
        metadata {
          name
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
        products(first: 5) {
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
  }
}
```

### Query With Curl

```bash
curl -s http://localhost:4000/admin/graphql \
  -H "Content-Type: application/json" \
  -d '{"query":"query { products(namespace: \"gitstore-test\", first: 5) { edges { node { metadata { name } } } } }"}' | jq .
```

## Control-plane Operations

Use GraphQL mutations for authentication, namespaces, and repositories. The Make bootstrap targets wrap these calls for the common local workflow.

GitStore delegates OAuth2/OIDC federation to external identity providers. The GraphQL
`login(input:)` mutation is a convenience flow for local providers (for example `static-users`).

Login:

```graphql
mutation Login {
  login(input: { username: "admin", password: "<password>" }) {
    token {
      accessToken
      tokenType
      expiresIn
      refreshToken
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
    }
  }
}
```

`scope` requests are currently unsupported by local providers.

Create a namespace and repository manually when you need custom provisioning. See [API Reference](api-reference.md#mutation-operations) for the exact inputs.

Catalogue writes are Git-driven today. Product, category, collection, and variant CRUD over GraphQL will be documented after the Git-backed design is finalized.

## Administer human identities

Use the Admin endpoint with cluster-scoped `user.*` grants. For example:

```graphql
mutation InviteStaff {
  createStaff(input: {
    metadata: {name: "carol"}
    fullName: "Carol"
    primaryEmail: "carol@example.test"
    groups: ["merchandiser"]
  }) {
    staff { id metadata { name uid generation resourceVersion } lifecycle { state } }
  }
}
```

Deliver setup links through the configured private mail provider, not GraphQL.
Use the returned ID for invitation resend, group and human lifecycle operations.
Setup/verification must complete before activation; reactivation cannot bypass
those prerequisites. A delivery or session-revocation failure may leave the
preceding identity write applied.

Look up humans with `actor(by: {username: "carol"})`; use
`serviceAccountName` for a workload, even when it shares a human's name.
Use `actors(first: 50)` for forward directory pages and `serviceAccounts`
for authoritative workload listing independent of directory availability.
See [identity administration](api-reference.md#identity-administration) for
inputs, permissions, cursor behavior and limitations.

## Troubleshooting

### Stack Is Not Healthy

Check service state and logs:

```bash
make ps
make logs SERVICE=api
make logs SERVICE=git-service
make logs SERVICE=controller-manager
```

If required auth configuration is missing, the API will not become healthy. Set `GITSTORE_API__AUTH__STATIC_USERS__USERS_FILE` and `GITSTORE_API__AUTH__JWT__SECRET`, or use the provided compose defaults for local development.

### Bootstrap Fails

`make bootstrap TARGET=all` needs a valid admin password unless you provide `BOOTSTRAP_TOKEN` or have a cached token.

```bash
make bootstrap TARGET=token ADMIN_PASSWORD=<admin-password>
make bootstrap TARGET=all BOOTSTRAP_TOKEN=<token>
```

Bootstrap is create-oriented. If a namespace or repository already exists, either use different `NAMESPACE` / `REPOSITORY` values or keep the existing resources.

### Clone Or Push Says Repository Not Found

Use the clone URL printed by `make bootstrap`. It should include the namespace, repository name, and `.git` suffix:

```text
http://localhost:9000/gitstore-test/catalog.git
```

Verify the repository exists:

```graphql
query Repository {
  repository(
    by: {
      namespacePath: {
        namespace: "gitstore-test"
        name: "catalog"
      }
    }
  ) {
    id
    name
    defaultBranch
  }
}
```

### Push Is Rejected

Read the `remote:` lines in the Git output. Common causes:

- Wrong `apiVersion` or `kind`.
- Missing `metadata.name`.
- Authoring system-managed `status` or read-only metadata fields.
- Invalid YAML frontmatter.
- Invalid product variant options, pricing, or inventory fields.

Fix the file and push again.

### GraphQL Query Returns Empty Results

Check that:

- You are querying the right namespace.
- Your push completed successfully.
- You are querying variant fields on `ProductVariant`, not old flat fields on `Product`.
- The resource name in `namespacePath` matches `metadata.name`.

### Images Or Media Do Not Load

Catalogue manifests reference media; they do not make GitStore an image CDN. Host images or binary assets externally or through the File workflow, then reference them from catalogue resources. Use `File` for the technical asset and `MediaAsset` for catalog-facing presentation metadata.

## Best Practices

- Keep one logical catalogue change per commit.
- Use descriptive filenames and resource names.
- Review diffs before pushing bulk updates.
- Keep product sellability on `ProductVariant`; use `Product` for shared product description and option definitions.
- Prefer labels for collection membership so collections remain declarative.

## Resources

- [API Reference](api-reference.md)
- [Developer Guide](developer-guide.md)
- [Configuration](configuration.md)
- [Docker Troubleshooting](runbooks/docker-troubleshooting.md)
