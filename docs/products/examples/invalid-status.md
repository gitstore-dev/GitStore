---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  name: invalid-status-example
  namespace: acme-store
spec:
  title: Example Product
status:
  conditions:
  - type: Ready
    status: "True"
---

# Invalid: status key present

This file will be rejected because `status` is system-managed.
Expected error: `validate: status is system-managed and must not be set by authors`
