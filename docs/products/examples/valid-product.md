---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  name: macbook-pro-m4-max
  namespace: acme-store
  labels:
    gitstore.dev/brand: Apple
    gitstore.dev/tier: premium
  annotations:
    gitstore.dev/notes: flagship laptop
spec:
  title: MacBook Pro M4 Max
  categoryRef:
    kind: CategoryTaxonomy
    name: personal-computers
  tags: [laptop, apple-silicon, pro]
  options:
  - name: color
    title: Colour
    values: [silver, space-black]
  - name: ram
    title: RAM
    values: [36GB, 64GB, 128GB]
  - name: storage
    title: Storage
    values: [1TB, 2TB, 4TB]
  media:
  - fileRef:
      kind: File
      name: hero-image
---

# MacBook Pro M4 Max

The most powerful MacBook Pro ever, featuring the M4 Max chip.
