# Shared secret-material boundary

`shared/secretmaterial` is a dependency-free Go 1.25 module. API/controller
services currently use Go 1.26. Root `make build`, `make test` and `make lint`
include the module. For focused work:

```sh
cd shared/secretmaterial
GOWORK=off go test -race -count=1 ./...
GOWORK=off go vet ./...
```

The controller's production bootstrap adapter uses this module. It binds its
owner and reference at construction and resolves a fresh signing record for
every token exchange; only access tokens are cached. The File integration
remains metadata validation plus a contract-only runtime consumer.

Use pure `ValidateSecretRef`/`ValidateCredentialsRef` for admission. JSON
decoding rejects unknown/duplicate/case-folded fields and explicit null
optionals; validation against the containing namespace remains mandatory.
Well-formed unknown credential types are accepted structurally.

Runtime construction binds environment, namespace and a non-nil trusted
authorizer. Bootstrap construction binds one owner and logical reference.
Authenticated principals/resource identities come from the consuming service,
not the manifest. Resolution takes at most 16 concurrent operations per
resolver, queues none, makes one provider attempt and applies the earlier of
the caller deadline or two seconds. Authorizers and providers must honor
cancellation; no timeout goroutine hides blocking work.

Local file providers pin an `os.Root`, use containment-safe, nonblocking opens,
reject nonregular descriptors and bound reads before parsing. The caller owns
the provider's `Close`; keep it open while operations are active. Supported
platforms are Linux and macOS on regular local filesystems, not network
filesystems with unbounded I/O. JSON records live at `<root>/<name>.json` for
bootstrap and `<root>/<environment>/<namespace>/<name>.json` for runtime.
Atomic replacement must occur inside the pinned root; replacing the root
directory itself requires recreating the provider.

Environment bindings are explicit, injective mappings. The legacy raw
bootstrap helper freezes the existing escaped environment variable mapping
for exactly one configured reference; other spellings cannot alias it.
Changing a parent process's environment requires replacing the consumer
process. Use atomic local file replacement for live rotation.

Provider `Read` transfers ownership of a fresh byte buffer to the resolver.
The resolver clears it after parsing. `SecretMaterial` hides its value map;
explicit accessors return copies, formatting is redacted and JSON/YAML
serialization fails. Call `Clear` after use and clear consumer copies where
feasible. This is lifetime minimization, not guaranteed Go-memory erasure.
No raw provider records, assertions or tokens belong in logs or evidence.

The `Observer` adapter is service-owned: `Inflight` supplies balanced slot
deltas, while `Observe` records every completed request (including early
denials/saturation). Labels are fixed consumer/purpose/tier/provider categories;
reasons are success, the seven contract classes or cancellation/deadline.
Callbacks must be concurrency-safe and nonblocking. No Prometheus dependency
or process registry is created by the library.

Runtime and bootstrap roots/variable sets must be disjoint in deployment.
Host ACLs and external access policy remain operator responsibilities.
Runtime resolution additionally rejects bootstrap signing records. Future
production consumers must supply their own authorization and observability
integration before using this library.

Controller settings use `controller.serviceaccount`,
`controller.secret_providers.bootstrap`, `controller.checkpoint`,
`controller.reconcile` and `controller.watch`. Dots map to `__` after
`GITSTORE_`, with one typed decode and no aliases for old flat keys.
Provider material remains outside shared TOML and API/Git-service mounts.
See [configuration](../configuration.md) and
[rotation](../runbooks/secret-material-rotation.md).

The controller exports `gitstore_secret_resolution_total`,
`gitstore_secret_resolution_duration_seconds` and
`gitstore_secret_resolution_inflight` through its existing registry. These
measure bounded acquisition, not successful authentication. Parsing or token
issuance can still fail after a successful read; readiness requires an unexpired
issued token. Labels contain only fixed consumer/purpose/tier/provider/reason
categories. Failures never log references, provider paths or bytes.
