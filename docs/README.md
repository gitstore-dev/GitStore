# GitStore Documentation

GitStore is in early development. Use the guides below for current behavior;
proposed designs and historical plans are not supported-feature references.

## Users and integrators

- [User guide](user-guide.md): start a local stack, author and push manifests,
  query admitted catalogue state.
- [Admin API reference](api-reference.md): queries, mutations, authentication
  and error contracts.
- [Local authorization](authorization.md): roles, bindings and provider boundaries.
- [Storefront boundary](storefront.md): current unavailable response and limits.

### Catalogue and control-plane resources

- [Namespace](namespace/namespace-spec.md) and [Namespace watches](namespace/namespace-watch.md)
- [Repository](repository/repository-spec.md) and [Repository watches](repository/repository-watch.md)
- [Product](products/product-spec.md) and [ProductVariant](products/product-variant-spec.md)
- [CategoryTaxonomy](categories/category-taxonomy-spec.md)
- [Collection](collections/collection-spec.md)
- [Push validation](products/push-validation.md)
- [Storage placement](storage-groups/README.md): current patterns and explicitly
  identified intended resource scope

## Operators

- [Configuration reference](configuration.md)
- [Current architecture](architecture/README.md)
- [Datastore resource storage](architecture/datastore-resource-storage.md)
- [Operations and Recovery](runbooks/README.md): deployment, upgrade, rotation
  and symptom-driven recovery procedures

## Contributors and design provenance

- [Developer guide](developer-guide.md)
- [Make command reference](development/make-reference.md)
- [Production-readiness testing](development/production-readiness-testing.md)
- [Release process](development/release-process.md)
- [Secret-material development](development/secret-material.md)
- [Architecture decisions and open implementation items](ADRs/README.md)
- [Implementation designs](implementation/): proposals and historical context,
  not deployment instructions
- [Contributor planning references](ADRs/README.md#contributor-planning-references):
  requirements, plans and acceptance history

Publication, custom resources, lifecycle extensions and future commerce
workflows remain design work where their records say Proposed. Retain their
unmet acceptance gates; do not treat a design document as a readiness result.
