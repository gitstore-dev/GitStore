---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  name:  macbook-pro-64gb-1tb-ssd-m4 # slug
  namespace: ensi-store
  labels:
    gitstore.dev/brand: Apple
    gitstore.dev/vendor: Apple
spec: 
  title: Macbook Pro 64GB 1TB SSD M4
  categoryRef: 
    kind: CategoryTaxonomy
    name: personal-computers
  tags: [laptop, apple]
  options:
  - name: color
    values: [silver, space-black]
  - name: ram
    values: [64GB, 128GB]
  - name: storage
    values: [1TB, 2TB]
  media:
  - fileRef:
      kind: File
      name: hero-image
      optional: true
---

# MacBook Pro 16" with M3 Max

The most powerful MacBook Pro ever. Supercharged by the M3 Max chip.

## Features

- M3 Max chip with up to 40-core GPU
- 36GB unified memory
- 1TB SSD storage
- 16-inch Liquid Retina XDR display
