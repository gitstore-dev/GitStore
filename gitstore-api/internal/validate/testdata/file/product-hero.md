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
