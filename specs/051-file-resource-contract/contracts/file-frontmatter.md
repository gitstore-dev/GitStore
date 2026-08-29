---
apiVersion: storage.gitstore.dev/v1beta1
kind: File
metadata:
  name: product-hero
  labels:
    role: hero
spec:
  contentType: image/jpeg
  type: gitstore.dev/media
  source:
    type: s3
    uri: s3://acme-assets/products/product-hero.jpg
    checksum:
      algorithm: sha256
      value: "<hex>"
    credentialsRef:
      kind: SecretRef
      name: media-credentials
  processing:
    image:
      variants:
        - name: thumbnail-webp
---
Product hero image

# Contract rules:
# - kind is exactly File and apiVersion is storage.gitstore.dev/v1beta1.
# - name, contentType, source.type, and source.uri are required.
# - source.type is git, lfs, s3, or gcs.
# - credentialsRef is same-namespace only.
# - status, read-only metadata, and status.phase are not author-writable.
# - the Markdown body is alt text, including an empty body.
