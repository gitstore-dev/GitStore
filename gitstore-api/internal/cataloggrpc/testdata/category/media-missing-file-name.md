---
apiVersion: catalog.gitstore.dev/v1beta1
kind: CategoryTaxonomy
metadata:
  name: televisions
  namespace: gitstore
spec:
  title: Televisions
  media:
    - fileRef:
        kind: File
---
