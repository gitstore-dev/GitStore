---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  name: invalid-title-example
  namespace: acme-store
spec:
  title: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
---

# Invalid: spec.title exceeds 200 characters

This file will be rejected because `spec.title` is 201 characters (exceeds the 200-character limit).
Expected error: `validate: spec.title failed max`
