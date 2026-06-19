# Commerce Resource Storage Overview

This section classifies GitStore resource types by where their source of truth
should live. The goal is to keep Git history useful for reviewable desired state,
while keeping high-churn runtime state, PII, payment state, and computed data out
of Git.

The resource shape intentionally follows Kubernetes conventions:

- `apiVersion`
- `kind`
- `metadata`
- `spec`
- `status`

That shape does not imply Git storage. Some resources are Git-authored and
hydrated into ScyllaDB or memDB. Others are datastore-only durable records or
transient request/response objects.

The resource list is anchored in the Kubernetes-style frontmatter initiative
tracked by `gitstore-dev/GitStore#40`. That initiative currently covers
`Product`, `ProductVariant`, `CategoryTaxonomy`, `Collection`, `Translation`,
`File`, `Namespace`, `Repository`, and research tracks for `Order` and
`PaymentIntent`.

## Storage Groups

| Group                 | Source of truth                                                     | Use for                                                                                                      | Details                                                   |
|-----------------------|---------------------------------------------------------------------|--------------------------------------------------------------------------------------------------------------|-----------------------------------------------------------|
| Git + datastore       | Git for author input; datastore for hydrated read model and status  | Reviewable business configuration and catalog desired state                                                  | [Git-backed resources](git-backed.md)                     |
| Datastore only        | ScyllaDB or memDB                                                   | Durable operational records, PII, payments, inventory movement, sessions, audit, and runtime state           | [Datastore-only resources](datastore-only.md)             |
| LFS or object storage | Git LFS or object storage for payloads; Git/datastore for manifests | Binary files, media originals, generated variants, imports, exports, labels, invoices, and archived payloads | [LFS and object storage resources](lfs-object-storage.md) |
| Transient             | Not persisted as resources                                          | Calculations, checks, previews, quotes, admission/review calls, auth reviews, and signed upload requests     | [Transient resources](transient.md)                       |

## Core vs Extension/CRD

Each resource is marked with an initial scope:

| Scope         | Meaning                                                                                                                                                                                            |
|---------------|----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| Core          | Should be built into GitStore because it is part of the base control plane, catalog, checkout, inventory, fulfillment, auth, or platform runtime.                                                  |
| Extension/CRD | Should be implemented through future custom resource definitions, plugins, or optional modules. These are important for enterprise commerce but should not expand the core API surface by default. |

The scope is not a persistence decision. A resource can be `Core` and
datastore-only, for example `Order` or `Session`.

## Common Git-Backed Markdown Shape

Git-backed resources are committed as Markdown files with YAML frontmatter.
Authors set desired state in `spec` and optional Markdown body content. System
fields and `status` are written to the datastore, not committed by users.

```markdown
---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  name: example-product
  namespace: example-store  # optional for current catalog resources; inferred from the repository when omitted
  labels:
    gitstore.dev/brand: example
  annotations:
    gitstore.dev/source: merch-team
spec:
  title: Example Product
---

Markdown body content for the resource.
```

System-managed fields that author files must not set:

- `metadata.uid`
- `metadata.resourceVersion`
- `metadata.generation`
- `metadata.creationTimestamp`
- `metadata.revision`
- `metadata.ownerReferences`
- `status`

For current Git-backed catalog resources, identity is `apiVersion`, `kind`, resolved namespace, and `metadata.name`. File path is source provenance, not identity. A path-only move preserves `metadata.uid` and `generation` while incrementing `resourceVersion`; a spec or Markdown body edit increments both `generation` and `resourceVersion`; deleting and later re-adding the same identity creates a new UID.

## Common Datastore-Only Shape

Datastore-only resources can still use the same API envelope internally and over
GraphQL/HTTP/gRPC. They are created by APIs, controllers, webhooks, or workers
instead of Git pushes.

```yaml
apiVersion: checkout.gitstore.dev/v1beta1
kind: Order
metadata:
  name: ord-10001
  namespace: example-store
spec:
  customerRef:
    kind: Customer
    name: cus-10001
  currencyCode: EUR
status:
  conditions: []
```

## Persistence Rules

Use Git + datastore when:

- humans should review and approve the change;
- the resource is desired state rather than a transaction log;
- rollback through Git history is useful;
- the document does not contain secrets, payment data, regulated PII, or
  high-churn runtime state.

Use datastore only when:

- the resource is created by customers, checkout flows, payment gateways,
  inventory workers, fulfillment systems, auth providers, or controllers;
- the resource changes frequently;
- redaction, retention, privacy, or legal deletion requirements apply;
- the resource is an append-only event, ledger, session, audit, or delivery log.

Use LFS or object storage when:

- the primary payload is binary or large;
- the object is generated or high-churn;
- direct Git storage would make repository history noisy;
- the payload may require retention, encryption, CDN delivery, or signed URLs.

Use transient resources when:

- the output is a calculation or authorization result;
- the resource has no useful independent lifecycle;
- replay can be represented by durable events or logs when needed.

## Subresource Conventions

Subresources should use the same storage decision as the data they represent:

| Subresource style               | Storage group                          | Examples                                                                                                               | Notes                                                                                                        |
|---------------------------------|----------------------------------------|------------------------------------------------------------------------------------------------------------------------|--------------------------------------------------------------------------------------------------------------|
| System status                   | Datastore field on Git-backed resource | `Product/status`, `ProductVariant/status`, `Collection/status`                                                         | Author files must not include `status`; controllers and admission own it.                                    |
| Finalization and deletion gates | Datastore field on durable resource    | `Namespace/finalize`, `Repository/finalize`, `Order/cancel`                                                            | Keep finalizers and deletion blockers out of Git-authored desired state unless they are policy declarations. |
| Review/check endpoints          | Transient                              | `TokenReview`, `SubjectAccessReview`, `AdmissionReview`, `ValidationReview`                                            | Persist only audit or decision logs if required.                                                             |
| Runtime child records           | Datastore only                         | `Order/events`, `PaymentIntent/authorizations`, `PaymentIntent/captures`, `Payment/refunds`, `Shipment/trackingEvents` | These are independent operational records even when exposed under a parent API path.                         |
| Generated projections           | Datastore only or transient            | `Product/searchDocument`, `Collection/members`, `Inventory/availability`                                               | Store only when needed for query performance or watch semantics.                                             |
| Binary variants                 | Object storage                         | `File/variants`, `MediaAsset/renditions`, `Invoice/pdf`                                                                | Store manifest and resolved URLs separately from payload bytes.                                              |

## Related Existing Resource Docs

- [Product Spec Reference](../products/product-spec.md)
- [ProductVariant Spec Reference](../products/product-variant-spec.md)
- [CategoryTaxonomy Spec Reference](../categories/category-taxonomy-spec.md)
- [Collection Spec Reference](../collections/collection-spec.md)
- [Pluggable Identity and Access Design](../implementation/pluggable_auth_design.md)
