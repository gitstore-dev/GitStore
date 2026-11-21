# Proposed Consumer and Implementation Walkthrough

Examples target the replacement contract and are not supported by current runtime. These source-grounded examples illustrate the Admin subset using its existing HTTP/WebSocket transports. They do not define Storefront types or recommendation serving. Shared semantics and separate exposure are defined in [API surfaces](contracts/api-surfaces.md); finalize Storefront examples after its first substantive contract. No query Payload.

## Typed indexed query

```graphql
query Products($namespace: String!, $after: String) {
  products(namespace: $namespace, first: 50, after: $after,
    query: "metadata.labels[\"tier\"]:gold") {
    edges { cursor node { id metadata { name } } }
    pageInfo { hasNextPage hasPreviousPage startCursor endCursor }
  }
}
```

Only advertised complete combinations work. Cluster connections such as `namespaces(first:50, query:"metadata.name:store")` omit namespace. Nested `Product.productVariants` inherits its parent; query cannot widen it. Category subtree example: `descendantOf:"footwear" includeSelf:true maxDepth:<=2`.

## Advanced typed query and the same provider through a union

```graphql
query Discovery($namespace: String!) {
  products(namespace: $namespace, first: 20,
    query: "metadata.labels[\"tier\"]:gold | semantic(\"comfortable shoes\")") {
    edges { cursor node { id } }
    pageInfo { hasNextPage endCursor }
  }
  search(namespace: $namespace, first: 20,
    query: "kind:[Product,CategoryTaxonomy] | semantic(\"comfortable shoes\")") {
    edges {
      cursor
      node {
        __typename
        ... on Product { id metadata { name } }
        ... on Category { id metadata { name } }
        ... on ProductVariant { id metadata { name } }
      }
    }
    pageInfo { hasNextPage hasPreviousPage startCursor endCursor }
  }
}
```

One SearchItemConnection pages the selected kind set with a shared order and cursor; SearchItem is the resource-node union. A kind scalar/array narrows the advertised default set. Both advanced operations above depend on the same registered provider/index capabilities; unsupported cross-kind combinations, filters or modes reject explicitly. Recommendation serving belongs to Storefront and remains reserved until its published-read and configured surface/context contracts exist; it is not enabled by this Admin example or by installing a vector engine.

## Journal watch

```graphql
subscription Changes($namespace: String!, $resourceVersion: String) {
  watchProducts(namespace: $namespace, resourceVersion: $resourceVersion,
    query: "metadata.labels[\"tier\"]:gold") {
    type name namespace resourceVersion
    product { id metadata { name resourceVersion } }
  }
}
```

Use existing controller bookmark-before-list bootstrap, page an eligible authoritative indexed query, then replay from that bookmark. Resource metadata.resourceVersion, page cursor and journal resourceVersion are different purposes. An omitted subscription cursor starts future delivery, not an implicit initial list. No retrieval/order pipeline is valid on watches; a provider outage has no effect on their execution source.

## Contributor workflow

1. Read the source inventory in the plan/execution contract; write failing semantic/security/schema fixtures first.
2. Implement registry/compiler/adapters and update canonical SDL/owned operations together. Use `.specify/feature.json` for this feature; existing helper scripts that insist on numeric branch prefixes can use `SPECIFY_FEATURE=073-uniform-query-surface` without renaming the branch.
3. Run existing `make generate TARGET=all`, `make test`, `make check TARGET=all`. Verify normal checks catch deliberate registry/query/cursor/watch mutations; no standalone duplicate schema map.
4. Extend and run existing capacity/chaos selectors under explicit owned fixtures after functional checks. Follow full acceptance and preserve retained data. Run full `make pr-ready` before opening a PR.
5. Coordinate major contract cutover and independently released consumers; document matched rollback. Audit ADR lifecycle/spec closure only when implementation/evidence is complete.

This planning change does not execute those runtime qualification steps or publish a release.
