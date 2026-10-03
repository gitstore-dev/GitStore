# ADR 0012: Separate Admin and Storefront GraphQL Endpoints

**Status**: Proposed

**Date**: 2026-09-30

**Audience**: GitStore API, schema, storefront, admin UI, and controller authors.

## Context

`gitstore-api` serves one GraphQL endpoint, `/graphql`. It is consumed by the admin UI, the
controller-manager, capacity tooling, and third-party apps. Doc 022, ADR 0010, ADR 0011, doc 037 and
doc 038 were written assuming this single endpoint would also serve storefronts and buyers.

Under that assumption, the public/management distinction has to be decided **per field, at runtime**:

- [Doc 022](../implementation/022-opa-data-authorization.md) has the authorizer emit a `PUBLIC` or
  `MANAGEMENT` decision scope for each request.
- [ADR 0011 §9](0011-graphql-authorization-directive.md#9-mode-scope-is-declared-per-field-never-at-objectinterfaceunion-level)
  confines `mode: SCOPE` to explicit field declarations, so mutation payloads don't deny a caller
  their own unpublished resource.
- [Publication Lifecycle](../products/publication-lifecycle.md) requires the scope-specific query
  plan on *every* Product/ProductVariant access path: direct lookup, Relay list, `node`, variants,
  Category/Collection relationships, counts and watches. "It is not sufficient to protect a new root
  alone."

For GitStore, the two scopes are not two filters over one table. `MANAGEMENT` reads the admitted,
current Git-backed rows. `PUBLIC` reads a target's immutable publication snapshot, held in separate
query-specific Scylla projections. Filtering after pagination and `ALLOW FILTERING` are both
forbidden. A shared field that serves both scopes therefore carries two physical query plans, and
missing the branch on any one path leaks drafts or produces short pages and mismatched counts.

Saleor runs dashboard and storefront on one endpoint successfully. It can do that because its
visibility is a predicate on the same Postgres rows. Shopify separates the Admin API from the
Storefront API, with different types, tokens and limits. The Storefront API doesn't rate-limit buyer
traffic, but it does limit per-query complexity, bots and checkout creation.
[Doc 038](../implementation/038-graphql-cost-and-rate-limits.md) needs per-principal cost buckets for
admin callers, and those don't suit buyer traffic.

The storefront and buyer surfaces don't exist yet, so this is the cheapest point to decide.

## Decision

### 1. Two endpoints, two schemas, one binary

| Endpoint           | Shared listener (`api.port`) | Dedicated listener                  | Schema            | Audience                                                                             |
|--------------------|------------------------------|-------------------------------------|-------------------|--------------------------------------------------------------------------------------|
| **Admin API**      | `/admin/graphql`             | `/graphql` on `api.admin.port`      | admin schema      | admin UI, `gitstore-controller-manager`, third-party apps and controllers, operators |
| **Storefront API** | `/storefront/graphql`        | `/graphql` on `api.storefront.port` | storefront schema | storefront frontends (browser and server-side), buyers                               |

- **Path rule.** When both APIs share one listener, the path prefix names the API. When an API has
  its own listener, the port names it and the path is plain `/graphql`. A listener never serves
  both schemas under the same path.
  - The playground follows the same rule: `/admin/playground` or `/storefront/playground` on a
    shared listener, `/playground` on a dedicated one.
  - Setting `api.admin.port` or `api.storefront.port` to a value different from `api.port` moves
    that API to its own listener. Leaving it unset (or equal to `api.port`) keeps it on the shared
    listener.
- Both endpoints are served by the existing `gitstore-api` process. They share:
  - the datastore;
  - the AuthN/AuthZ provider registry;
  - `ratelimit.Limiter` (doc 038);
  - the watch journal;
  - the transport/extension builder.
- A separate storefront service or binary is deferred. The split is by schema, not by deployable.
- `api.storefront.enabled` defaults to `false` until publication and storefront reads ship.
- A dedicated admin listener lets operators keep the Admin API on an internal network only, as
  `api.git_port` already does for Git smart-HTTP.

**Migration of today's `/graphql` (shared listener).** Today every client calls `/graphql` on
`api.port`: the controller-manager (`GITSTORE_CONTROLLER__API_URI`), the admin UI, compose files,
`make bootstrap` (`API_URL`), `gitctl`, and the capacity profiles.

Migration takes three releases, so mixed-version API and controller-manager replicas always agree
on a path:

| Release | API (shared listener)                                                    | In-repo client defaults    |
|---------|--------------------------------------------------------------------------|----------------------------|
| N       | serves `/admin/graphql`; `/graphql` becomes a **deprecated alias** of it | unchanged (`/graphql`)     |
| N+1     | unchanged                                                                | switch to `/admin/graphql` |
| N+2     | alias removed; `/graphql` on the shared listener returns 404             | unchanged                  |

- **Why three releases.** New controller-manager replicas only move to `/admin/graphql` once every
  API replica serves it, and the alias is only removed once no supported client still uses it.
- **Alias responses** serve the admin schema and carry `Deprecation` and
  `Link: </admin/graphql>; rel="successor-version"` headers.
- **Enabling the storefront doesn't change the alias.** With `api.storefront.enabled = true` on a
  shared listener, the alias still points to the Admin API.
- **After removal, a shared listener has no `/graphql`**, so the path is never ambiguous between
  the two schemas.
- **Defaults that switch in N+1:**
  - `GITSTORE_CONTROLLER__API_URI`;
  - `API_URL` in the Makefile;
  - `compose*.yml`;
  - `gitctl`;
  - `tests/capacity`;
  - the admin UI.

### 2. The schemas are disjoint; types are not shared across the boundary

```text
shared/schemas/
├── admin/*.graphqls        # Admin API: today's files, moved unchanged
├── common/*.graphqls       # value types with identical meaning on both endpoints
└── storefront/*.graphqls   # Storefront API: read-only, snapshot-backed
```

- **Admin** (`shared/schemas/admin/`): today's `shared/schemas/*.graphqls` files, moved as-is
  except for the definitions extracted to `common/`.
  - every control-plane kind, mutation and `watch*` subscription;
  - `Query.node`/`nodes`;
  - ServiceAccounts and status mutations.
- **Storefront** (`shared/schemas/storefront/`, new): read-only catalog types backed by the target's
  active publication snapshot.
  - Catalog types: `StorefrontProduct`, `StorefrontProductVariant`, `StorefrontCategory`,
    `StorefrontCollection`, `StorefrontMedia`.
  - No `generation`, `resourceVersion`, `finalizers`, `status`, owner references, or other
    management fields.
  - Later: the buyer plane's cart/checkout/order roots (doc 037).
- **Common** (`shared/schemas/common/`, new): only value types with identical meaning on both
  sides, such as custom scalars (including `Markdown` and `MarkdownDocument`,
  [ADR 0013](0013-markdown-body-intermediate-representation.md)), `Money`, `PageInfo` and the `Node` interface. No resource type
  lives here.
- **Generation.** Each endpoint loads its own directory plus `common/`, and no glob spans both
  endpoint directories:

  | Consumer | Schema globs |
  |---|---|
  | `gitstore-api/gqlgen.yml` (admin exec package) | `../shared/schemas/admin/*.graphqls`, `../shared/schemas/common/*.graphqls` |
  | `gitstore-api/gqlgen.storefront.yml` (new, storefront exec package) | `../shared/schemas/storefront/*.graphqls`, `../shared/schemas/common/*.graphqls` |
  | `gitstore-admin/codegen.yml` | admin + common |
  | `docker/api.Dockerfile` | copies `shared/schemas` recursively (no change needed) |

#### Storefront entry points

The storefront schema has flat, Shopify-style roots rather than a single facade object:

```graphql
type Query {
  products(
    first: Int, after: String, last: Int, before: String
    query: String
    sortKey: StorefrontProductSortKeys, reverse: Boolean
  ): StorefrontProductConnection!

  search(
    first: Int, after: String, last: Int, before: String
    query: String!
    types: [StorefrontSearchType!]
  ): SearchResultConnection!

  node(id: ID!): Node
}

union SearchResultItem = StorefrontProduct | StorefrontCollection | StorefrontCategory
```

- **`query`** is a server-parsed search string (for example `tag:sale price:<50 "red shoe"`), not a
  client-supplied filter object. Its grammar is limited to fields that have a snapshot projection
  or index. An unsupported term is a validation error, never a scan or post-pagination filter, so
  doc 022's rule against filtering after pagination still holds.
- **`products`** without `query` lists the target's published Products in the snapshot's order.
  With `query` it is a filtered listing that returns only Products.
- **`search`** is the cross-type, relevance-ranked entry point. The union follows doc 038 §6.4:
  its cost is the most expensive member type.
- **The target isn't a root argument.** The active target comes from the request's storefront
  identity (§4): a storefront access token binds a target. How anonymous callers select a target
  is part of the deferred target-binding decision below.
- These roots replace Publication Lifecycle's `storefrontCatalog(target:)` facade.

### 3. The endpoint fixes the data plane and the decision scope

|                                        | Admin API                                 | Storefront API                                                                         |
|----------------------------------------|-------------------------------------------|----------------------------------------------------------------------------------------|
| Rows read                              | admitted, current Git-backed rows         | the target's active publication snapshot projections only                              |
| Doc 022 scope                          | always `MANAGEMENT`                       | always `PUBLIC`                                                                        |
| Product/Variant read action (ADR 0010) | `product.management.read` / `.list`       | `product.read` / `.list` with visibility bands (ADR 0010 §8), e.g. via `public-reader` |
| Other kinds                            | existing `<kind>.read`/`.list`, unchanged | the published projection's `<kind>.read`/`.list`                                       |
| Writes                                 | all control-plane mutations               | none for catalog. Buyer-plane commands only (doc 037).                                 |

- **No runtime scope branching.** No resolver, service or datastore entry point switches between
  current rows and snapshots based on the caller. The resolver package a field lives in determines
  its plan. The doc 022 requirement that an unscoped catalog read be "unavailable by construction"
  is then met structurally: storefront resolvers depend only on snapshot readers, and admin
  resolvers only on current-row readers.
- **Action strings are unchanged.** ADR 0010 already separates the public view (`product.read`)
  from the management view (`product.management.read`). The endpoint decides which of the two is
  checked. That choice no longer comes from a per-request decision scope.
- **No `SCOPE` mode is needed for the Product/ProductVariant catalog split, and `@authorize` carries no
  mode argument at all** (ADR 0011 §6). On the Admin API, `type Product @authorize(permission:
  "product.management.read")` propagates to mutation payloads as ADR 0011 §2 describes. The built-in
  authoring roles therefore include `product.management.read`, because anyone who creates or edits
  drafts must be able to read them. ADR 0011 §9's concern, "a caller denied their own just-created
  resource", becomes a role design rule instead of a directive-mode rule.

### 4. Identity per endpoint

- Both endpoints authenticate through the same `ProviderRegistry`. There is no second AuthN stack.
- **Admin principals:** static users, OIDC, ServiceAccounts, controllers (unchanged).
- **Storefront principals:**
  - **anonymous**, bound to `public-reader`;
  - **storefront access tokens**, which identify a storefront client and target. Their provider
    kind is deferred to the storefront spec.
  - **buyer tokens**, deferred to the buyer-plane spec (doc 037).
- An admin credential isn't needed on the storefront, and a storefront or buyer credential grants
  no admin action. The mechanism is policy (no bindings), not a separate token format.
- Doc 038 §8.2's controller exemption applies to the Admin API only.

### 5. Global IDs are endpoint-specific

- Storefront `Node` IDs encode storefront kinds (`StorefrontProduct`, …). Admin `decodeGlobalID`
  rejects storefront kinds, and the storefront decoder rejects admin kinds, each with `NOT_FOUND`.
  An ID taken from one endpoint can't be used to probe the other.
- Storefront IDs encode the stable resource UID, not the snapshot or release. IDs, caches and future
  cart lines stay stable across releases, and the active snapshot is resolved per request.

### 6. Limits and transport per endpoint (doc 038)

| Doc 038 layer                  | Admin API                             | Storefront API                                                                                                                           |
|--------------------------------|---------------------------------------|------------------------------------------------------------------------------------------------------------------------------------------|
| Structural limits (§7)         | yes                                   | yes, same defaults                                                                                                                       |
| Per-operation cost ceiling     | 1 000                                 | 1 000                                                                                                                                    |
| Per-principal cost bucket (L1) | yes; GitStore's own controller exempt | **no**. Buyer reads aren't budgeted (Shopify Storefront model).                                                                          |
| Per-IP limiter (L0)            | yes                                   | yes, higher default. Keyed on a trusted buyer IP (below).                                                                                |
| Write throttle                 | —                                     | cart/checkout creation per buyer and per storefront token, returning `THROTTLED`                                                         |
| Introspection                  | authenticated only                    | enabled. The storefront schema is a public contract.                                                                                     |
| Subscriptions                  | `watch*`, `watchResources`            | none initially. Storefront change events from Publication Lifecycle are a later, storefront-typed subscription.                          |
| Caching                        | none                                  | GET + persisted queries. Responses are cacheable by target, active snapshot and document hash, with an `ETag` derived from the snapshot. |
| CORS                           | as today                              | explicit origin allow-list per storefront token or target                                                                                |

- **Buyer IP header.** Server-side storefronts put all buyers behind one server IP. A
  `GitStore-Storefront-Buyer-IP` header (no `X-` prefix, as in doc 038 §10.2) keys L0 on the real
  buyer IP. It is honoured **only** on requests authenticated with a storefront access token, and
  otherwise ignored. That prevents the same spoofing as doc 038's `X-Forwarded-For` finding.
- **Bots.** Bot and crawler protection is left to the operator's CDN or WAF in alpha.

### 7. Server wiring

- One builder, `newGraphQLHandler(schema, profile)`, produces each handler. It applies:
  - the shared transports;
  - the query cache and APQ;
  - the authentication interceptor;
  - the endpoint's authorizer;
  - doc 038's limit profile.
- `internal/app/server.go` mounts the handlers by the §1 path rule:
  - **shared listener:** `/admin/graphql` (plus the alias during releases N and N+1) and, when
    enabled, `/storefront/graphql`;
  - **dedicated listener:** `/graphql`.
- Admin resolvers stay in `internal/graph/resolver`. Storefront resolvers live in
  `internal/storefront/graph/resolver` and import only snapshot-backed services. An import-boundary
  test enforces that.

## Alternatives Considered

1. **One endpoint with shared types (Saleor model).** Rejected. It keeps per-field, per-caller
   branching between two physical query plans on every access path. That is cheap for a SQL predicate
   and costly and leak-prone over GitStore's separate snapshot projections.
2. **One endpoint with separate storefront roots and types.** Rejected. It removes the shared-type
   problem, but:
   - Anonymous callers share the admin introspection surface.
   - Doc 038 would have to pick a limit profile from an operation's root fields.
   - Operations mixing admin and storefront roots need an extra rule.
   - Admin and storefront can't be isolated at the network level.
3. **Separate storefront service.** Deferred. The disjoint schemas, the import boundary (§7) and the
   optional listener (§1) keep this a packaging change later.

## Consequences

- The public/management split is enforced by package and schema structure instead of runtime scope
  checks. Missing the branch on one access path is no longer a possible bug.
- Existing admin clients keep working through the `/graphql` alias until release N+2 (§1). The
  storefront ships dark behind `api.storefront.enabled`.
- Costs:
  - a second gqlgen target and schema tree;
  - moving today's schema files to `shared/schemas/admin/` and updating `gqlgen.yml`,
    `gitstore-admin/codegen.yml`, the READMEs and the file-path comments in the controller-manager;
  - a small `common/` extraction from `schema.graphqls`;
  - storefront types that deliberately duplicate some admin fields;
  - a second resolver package.
- Multi-replica and rolling upgrade: `/admin/graphql` and `/storefront/graphql` are additive, and
  the `/graphql` move follows §1's three-release sequence. Replicas without `/storefront/graphql`
  return 404 until the rollout completes, and `api.storefront.enabled` is flipped only after every replica runs the
  new version (Publication Lifecycle rollout step 5).
- **Required amendments** (follow-up edits that reference this ADR):
  - **[Doc 022](../implementation/022-opa-data-authorization.md):**
    - The `PUBLIC`/`MANAGEMENT` decision scope is fixed by the endpoint, not emitted per request.
    - The dual query plan for each catalog field is removed.
    - The `Authorized*Query` types keep their "unavailable by construction" role, split per endpoint.
  - **[ADR 0010](0010-authorization-model.md):** no grammar change. §5/§8 note which endpoint checks
    `product.read` vs `product.management.read`. Built-in authoring roles include
    `product.management.read`.
  - **[ADR 0011](0011-graphql-authorization-directive.md):**
    - `@authorize` applies to both schemas, `permission` only — no mode argument.
    - §9's `SCOPE` rationale no longer covers the Product/ProductVariant catalog split, and is kept
      only as historical record of the gqlgen stacking fact behind the placement rule.
    - **Resolved, not left open:** with the catalog split gone, `mode`/`AuthzMode` had no remaining
      live consumer, so ADR 0011 §6 removes it from the directive entirely (YAGNI) rather than keep it
      declared with zero fields using it. List-level visibility-band filtering (§7 of ADR 0011) stays
      deferred; reintroducing a scope-output mechanism for it is a future amendment against a concrete
      field, not a standing feature of `@authorize` today.
  - **[Doc 037](../implementation/037-custom-commerce-workflows.md):** the seller plane is the Admin
    API and the buyer plane is the Storefront API.
  - **[Doc 038](../implementation/038-graphql-cost-and-rate-limits.md):** limits become per-endpoint
    profiles (§6). The current design is the admin profile, and the storefront profile is added.
  - **[Publication Lifecycle](../products/publication-lifecycle.md):** the Storefront API roots are
    `products(query:)` and `search(query:)` (§2), replacing `storefrontCatalog(target:)`. The
    "every access path" requirement applies within each endpoint's single plan.
- **Not addressed here (deferred):**
  - the storefront access-token provider kind and target binding, including how anonymous callers
    select a target;
  - the `query` search grammar and the search index behind `search`;
  - buyer identity;
  - storefront subscriptions;
  - persisted-query or trusted-document enforcement;
  - the concrete storefront types' field lists.

## References

- [ADR 0010 — Authorization Model](0010-authorization-model.md)
- [ADR 0011 — Declarative GraphQL Authorization via `@authorize`](0011-graphql-authorization-directive.md)
- [Doc 022 — OPA Data-Aware Authorization](../implementation/022-opa-data-authorization.md)
- [Doc 037 — Custom Seller and Buyer Workflows](../implementation/037-custom-commerce-workflows.md)
- [Doc 038 — GraphQL Cost Analysis, Rate Limiting, and Request Limits](../implementation/038-graphql-cost-and-rate-limits.md)
- [Publication Lifecycle](../products/publication-lifecycle.md)
- Shopify, [Storefront API rate limits](https://shopify.dev/docs/api/storefront#rate_limits) and
  [API limits](https://shopify.dev/docs/api/usage/limits)
- Saleor, single GraphQL API for dashboard and storefront (permission- and channel-filtered querysets)
- Live wiring: `gitstore-api/internal/app/server.go`, `gitstore-api/gqlgen.yml`, `gitstore-admin/codegen.yml`,
  `gitstore-api/internal/middleware/security/graphql.go` (`decodeGlobalID`)
