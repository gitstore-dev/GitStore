# LFS And Object Storage Resources

This group covers binary or large payloads. GitStore should keep reviewable
manifests in Git and hydrated metadata in the datastore, while payload bytes live
in Git LFS or object storage.

## Storage Rules

Use Git LFS when:

- the asset is catalog-owned desired state;
- versioning the original is useful;
- churn is low enough that repository history remains useful.

Use object storage when:

- the asset is generated, temporary, customer-provided, or high-churn;
- the asset needs CDN delivery, encryption, retention, legal hold, or signed URLs;
- the asset contains PII or operational documents.

Use a secret manager or KMS, not Git or LFS, for credentials, signing keys,
webhook secrets, payment provider secrets, private certificates, and encryption
keys.

## Manifest Shape

```markdown
---
apiVersion: storage.gitstore.dev/v1beta1
kind: File
metadata:
  name: product-hero
  namespace: example-store
spec:
  contentType: image/jpeg
  type: gitstore.dev/media
  source:
    type: git
    uri: git:///media/product-hero.jpg?ref=main
    checksum:
      algorithm: sha256
      value: example
  processing:
    image:
      variants:
      - name: thumbnail
        width: 400
        format: webp
---

Alt text or file description.
```

## Git-Backed Manifests

| Resource       | Scope         | Summary                                                                              | Initial spec shape                                               |
|----------------|---------------|--------------------------------------------------------------------------------------|------------------------------------------------------------------|
| `File`         | Core          | Generic file manifest with source URI, checksum, content type, and processing hints. | `spec: {contentType, type, source, processing}`                  |
| `MediaAsset`   | Core          | Catalog-facing semantic asset that points at a `File` and adds role/alt/focal metadata. | `spec: {fileRef, role, focalPoint, altTextRef}`                  |
| `DigitalAsset` | Extension/CRD | Downloadable commerce asset controlled by entitlement or delivery rules.             | `spec: {fileRef, entitlementPolicyRef, deliveryPolicy, version}` |

## Payload Classes

These are not usually standalone API resources. They are payload classes
referenced by `File`, `MediaAsset`, `DigitalAsset`, or datastore-only records.

| Payload class           | Scope         | Preferred storage         | Summary                                               | Initial metadata shape                              |
|-------------------------|---------------|---------------------------|-------------------------------------------------------|-----------------------------------------------------|
| `ProductImageOriginal`  | Core          | Git LFS or object storage | Original catalog image.                               | `{contentType, sizeBytes, checksum, sourceRef}`     |
| `ProductImageVariant`   | Core          | Object storage            | Generated resized or reformatted image.               | `{originalRef, width, height, format, url}`         |
| `VideoOriginal`         | Extension/CRD | Object storage            | Original product or content video.                    | `{contentType, sizeBytes, checksum, duration}`      |
| `VideoTranscode`        | Extension/CRD | Object storage            | Generated video rendition.                            | `{originalRef, codec, bitrate, resolution, url}`    |
| `DocumentAsset`         | Core          | Git LFS or object storage | Manual, warranty, spec sheet, certificate, or PDF.    | `{contentType, checksum, locale, title}`            |
| `ImportExportFile`      | Core          | Object storage            | Bulk import, export, report, or reconciliation file.  | `{jobRef, contentType, checksum, expiresAt}`        |
| `CustomerUpload`        | Core          | Object storage            | File uploaded by a customer, such as return evidence. | `{customerRef, purpose, contentType, retention}`    |
| `InvoicePDF`            | Core          | Object storage            | Generated invoice or credit note PDF.                 | `{orderRef, invoiceNumber, contentType, checksum}`  |
| `ReturnLabel`           | Core          | Object storage            | Carrier-generated return label.                       | `{returnRef, carrier, trackingNumber, contentType}` |
| `WebhookPayloadArchive` | Core          | Object storage            | Archived inbound or outbound webhook body.            | `{deliveryRef, eventType, checksum, retainedUntil}` |
| `BackupArtifact`        | Extension/CRD | Object storage            | Backup, restore, or migration artifact.               | `{source, createdAt, checksum, encryptionRef}`      |

## Object URI References

Prefer explicit source types:

```yaml
source:
  type: s3
  uri: s3://catalog-assets/products/product-hero.jpg
  checksum:
    algorithm: sha256
    value: example
```

```yaml
source:
  type: git
  uri: git:///media/product-hero.jpg?ref=main
  checksum:
    algorithm: sha256
    value: example
```

```yaml
source:
  type: b2
  uri: b2://catalog-assets/products/product-hero.jpg
  checksum:
    algorithm: sha256
    value: example
```

Credentials should be referenced indirectly:

```yaml
source:
  type: s3
  uri: s3://catalog-assets/products/product-hero.jpg
  credentialsRef:
    kind: SecretRef
    name: catalog-assets-writer
```

The `SecretRef` target is not a Git-backed secret. It should resolve through the
deployment secret manager or Kubernetes secret integration.

`File` is the technical binary/media primitive. It owns the manifest, source
metadata, processing hints, and resolved variants. `MediaAsset` is a sibling
catalog resource that points at a `File` and adds presentation metadata such as
`role`, `altTextRef`, and `focalPoint`.
