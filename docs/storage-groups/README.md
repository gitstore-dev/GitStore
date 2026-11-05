# Resource Envelope Storage Groups

This section classifies resource envelopes by their source of truth, durability,
and payload placement. The shared `apiVersion`, `kind`, `metadata`, `spec`, and
`status` shape does **not** imply that a resource belongs in Git.

## Storage groups

| Storage group | Source of truth | Appropriate data |
| --- | --- | --- |
| [Git-backed](git-backed.md) | Git for authored desired state; datastore for hydrated records and system status | Reviewable catalogue and configuration documents |
| [Datastore-only](datastore-only.md) | Datastore, without a Git-authored manifest | Transactions, sessions, inventory movement, audit records, private data, and other durable runtime state |
| [LFS / object storage](lfs-object-storage.md) | LFS or object storage for payload bytes; Git or datastore for the manifest | Binary assets, media, generated variants, imports, exports, and archived documents |
| [Transient](transient.md) | Request/response only, without an independently persisted resource | Calculations, previews, checks, authorization reviews, and signed upload requests |

These are placement decisions, not mutually exclusive backend implementations.
A Git-backed File manifest can describe an object-storage payload; its resolved
metadata and processing status still live in the datastore. A transient review
can produce a separately persisted audit event.

## Choosing a group

Use Git when changes are reviewable desired state and history is useful for
rollback. Do not put credentials, payment data, regulated personal data, or
high-churn transaction logs in Git.

Use the datastore when APIs, controllers, customers, or providers create durable
operational records, especially where retention, redaction, or legal deletion
matters. Durability depends on the chosen backend and deployment; an in-memory
backend is not persistent storage.

Keep large or binary payloads outside ordinary Git history. Choose LFS for
versioned, low-churn authored assets and object storage for generated, temporary,
private, or high-churn payloads. Credentials belong in a secret manager or KMS,
not in either payload store.

Use a transient contract when the result has no useful independent lifecycle.
Persist any required audit trail or downstream event as a separate durable
record.

## Resource catalogue scope

The group pages retain an initial placement catalogue. **Core** means intended
built-in scope; **Extension/CRD** means intended extension or optional-module
scope. Neither label proves that every listed resource, provider, or extension
mechanism is implemented. Consult the resource-specific contracts for supported
behavior.

Scope and storage are independent: a core resource can be datastore-only, and
an extension can be Git-backed.

## Subresources

Choose storage for the data represented, not for the parent endpoint's name.

| Data | Placement |
| --- | --- |
| Generated status, resource versions, finalizers, and deletion state | Datastore fields on the durable envelope; not user-authored Git content |
| Review and check results | Transient; persist audit records separately when required |
| Runtime child records such as payment captures or tracking events | Datastore-only |
| Search, membership, and availability projections | Datastore when persistence is needed for queries or watches; otherwise transient |
| Binary variants and generated documents | Object storage, with manifest and resolved metadata stored separately |

## Related contracts

- [Datastore envelopes, query projections, pagination, and CDC](../architecture/datastore-resource-storage.md)
- [Namespace](../namespace/namespace-spec.md)
- [Product](../products/product-spec.md) and [ProductVariant](../products/product-variant-spec.md)
- [Product and Variant Publication Lifecycle](../products/publication-lifecycle.md)
- [CategoryTaxonomy](../categories/category-taxonomy-spec.md)
- [Collection](../collections/collection-spec.md)

File manifests are described in the [Git-backed group](git-backed.md). Payload
placement does not imply that every File processing or delivery capability is
currently supported.
