# File Resource Contract Quickstart

## Example manifest

```markdown
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
    type: lfs
    uri: media/product-hero.jpg
    checksum:
      algorithm: sha256
      value: "0123456789abcdef"
    credentialsRef:
      kind: SecretRef
      name: media-credentials
  processing:
    image:
      variants:
        - name: thumbnail
---
Product hero image
```

The body is alt text. An omitted namespace is inherited from the repository
push context. `status`, read-only metadata, and `status.phase` are forbidden in
the Git document.

## Focused checks

After implementation:

```bash
cd gitstore-api
go test ./internal/validate ./internal/cataloggrpc ./internal/datastore/...
cd ..
make pr-ready
```

The Scylla backend contract tests use the `scylla` build tag and require a live
Scylla instance; the default command exercises the runnable MemDB and API
contracts.

The contract phase does not fetch the URI, verify checksums, process variants,
upload binaries, purge payloads, or reconcile File status.
