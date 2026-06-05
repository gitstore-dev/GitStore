---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  name: invalid-media-example
  namespace: acme-store
spec:
  title: Example Product
  media:
  - fileRef:
      kind: File
---

# Invalid: spec.media[0].fileRef.name absent

This file will be rejected because `spec.media[0].fileRef.name` is required.
Expected error: `validate: spec.media[0].fileref.name is required`
