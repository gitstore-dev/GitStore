# Contract: Shared Secret Material Resolution

**Proposed package**: `shared/secretmaterial` (new repository-local Go module).
No GraphQL/gRPC endpoint is added.

## API boundary

The exported contracts live in `shared/secretmaterial/types.go`; the module
path is `github.com/gitstore-dev/gitstore/secretmaterial`. Services currently
use Go 1.26; the dependency-free library retains a Go 1.25 baseline.
Concrete entry points:

```go
type SecretResolver interface {
    ResolveSecret(ctx context.Context, ref SecretRef, req ResolutionRequest) (SecretMaterial, error)
}

func ValidateSecretRef(ref SecretRef, namespace string) error
func ValidateCredentialsRef(ref CredentialsRef, namespace string) error
func NewBootstrapResolver(p Provider, binding BootstrapBinding, observer Observer) (*Resolver, error)
func NewRuntimeResolver(p Provider, binding RuntimeBinding, observer Observer) (*Resolver, error)
func (r *Resolver) ResolveCredentials(ctx context.Context, ref CredentialsRef, req ResolutionRequest) (SecretMaterial, error)
```

`SecretMaterial` keeps its values private and offers explicit `Value`, `Values`,
`RecordFormat` and `Clear` methods. Value access returns copies; formatting is
redacted and JSON/YAML serialization fails. This narrows the conceptual public
map to prevent accidental default serialization. `SecretRef` follows
[references.md](references.md); request/metadata fields
follow [data-model.md](../data-model.md). Constructors bind an instance to one
tier, one operator-owned provider configuration and a trusted authorization
policy. Request fields cannot override those bindings.
Bootstrap binding freezes the owner and logical reference; runtime binding
freezes environment/namespace and requires an explicit authorizer. Runtime
requests carry the authenticated principal and resource identity. Provider
`Read` receives only the constructor-selected scope. Observations use fixed
bootstrap identity/runtime contract categories, never request-supplied labels.

Provide pure `ValidateSecretRef`/`ValidateCredentialsRef` functions and a typed
resolution operation that validates the reference, checks type support, resolves
material, then validates required items before returning it to the consumer.
The initial supported runtime type is `aws-access-key/v1`; require nonempty
`accessKeyId`/`secretAccessKey`, permit optional `sessionToken`.

## Tier and authorization isolation

| Context                      | Required behavior                                                                                                              |
|------------------------------|--------------------------------------------------------------------------------------------------------------------------------|
| Bootstrap                    | Owning service identity and logical ref from deployment config; no resource namespace; no GitStore-authenticated provider call |
| Runtime                      | Authenticated consumer principal, owning resource namespace/environment; explicit binding authorization before provider access |
| Wrong tier                   | `Forbidden`, zero provider calls                                                                                               |
| Foreign explicit namespace   | `InvalidRef` / `CrossNamespaceRef`, zero provider calls                                                                        |
| Unauthorized subject/binding | `Forbidden`, zero provider calls                                                                                               |

No permissive default authorizer. The bootstrap constructor allows only the
configured owning process; a runtime consumer supplies its policy-bound
principal/context after service AuthN/AuthZ. The two contexts cannot be
converted by a resource-authored discriminator.

This feature wires only bootstrap in production. Runtime provider adapters and
authorizer behavior are exercised through test consumers. The production API
imports validation functions, not a privileged runtime resolver.

## Provider records and compatibility

Retain `file` and `env` categories. Introduce explicit provider binding
`format = "json-record"` for new multi-item/whole-record usage; existing
configured raw/keyed usage remains `format = "raw"` with its established
mapping. Resolve omitted format as raw only for previously valid keyed
bootstrap configurations; reject new ambiguous whole-record configurations.

JSON records use a closed envelope:

```text
{
  "format": "secret-record/v1" | "serviceaccount-signing-key/v1",
  "values": { "<valid-item-name>": "<base64-encoded bytes>" }
}
```

The bootstrap identity variant requires `privateKey` and `keyID`; ordinary
runtime records use `secret-record/v1`. The JSON/base64 envelope exists only in
the provider, never in Git-backed resources, API responses or evidence.
Duplicate JSON keys, unknown envelope fields, malformed base64 and unrecognized
record format fail explicitly (`InvalidRef` for malformed binding/encoding;
`UnsupportedType` for an unsupported record format).

File bindings:

- Bootstrap JSON record: `<base_path>/<name>.json`, with no resource namespace.
- Runtime JSON record:
  `<base_path>/<environment>/<namespace>/<name>.json`.
- Environment/namespace path components are validated deployment context, not
  blindly interpolated request strings.
- Canonicalize the configured root, use containment-safe file opening, reject
  symlink escapes and non-regular files, and retain containment across
  open/read to prevent path-swap races. Do not authorize a path only by a string
  prefix before opening it.
- Read at most encoded limit + 1; record replacement is atomic and read-only
  from the consumer. Never merge two file revisions into one record.
- Existing raw/keyed bootstrap mapping is preserved by an adapter; it must
  receive the same containment and size protections.

Environment bindings map logical identity to explicit operator-configured
variable names. Validate mappings as one-to-one rather than lossy normalizing
arbitrary key strings. A process environment does not change when an operator
edits a parent environment: env-provider rotation requires process replacement;
hot-rotation evidence uses atomic file records. Never dump the environment.

File/env implementation code may be reused, but runtime and bootstrap bindings
must have disjoint roots/variable mappings and independent authorization.
Managed deployment checks reject known overlap. External IAM/ACL exclusivity
is the deployment operator's responsibility.

When `ref.key` is omitted, return all values within aggregate limits. When
present, return only that value, preserving its key name. Do not bypass
type-required fields by silently loading sibling items. The legacy raw adapter
returns a single configured item; it cannot invent a multi-item record.

## Errors

| Class | Examples | Consumer response |
| --- | --- | --- |
| `InvalidRef` | Malformed reference, cross-namespace, bad configured record | Block; fix input/config |
| `NotFound` | Logical record absent | Block; provision record |
| `MissingKey` | Selected or type-required item absent/empty | Block; repair record |
| `Forbidden` | Wrong tier, principal or denied filesystem access | Block; correct authorization |
| `ProviderUnavailable` | Unavailable provider, resolver saturated | Block; bounded retry |
| `UnsupportedType` | Unimplemented credential/record/provider format | Block; no fallback |
| `ValueTooLarge` | Encoded/decoded/item/count limit exceeded | Block; reduce material |

Keep the ADR spelling `ValueTooLarge`, not a competing `OversizedValue` class.
No partial material accompanies an error. Safe error text includes class,
consumer, purpose and field category only, not OS paths, provider error bodies
or material. Wrapped causes preserve cancellation/deadline detection for code
without formatting unsafe causes. Cancellation/deadline terminate the call;
do not introduce an eighth protocol class or retry cancellation.

## Bounds, rotation and observability

Use plan bounds: 256 KiB encoded, 128 KiB decoded total, 64 KiB/item, 32 items,
16 concurrent resolutions, no internal queue, 2s or shorter caller budget.
On saturation, return `ProviderUnavailable` with fixed detail `saturated`.
The provider performs one attempt; consumer backoff owns retries. No background
refresh loops, persistent caches or unbounded reference-keyed maps.

Private keys are operation-local; ordinary runtime material is also uncached
in this release. Each operation/retry therefore re-resolves. Tests inject a
provider revision change between operations and verify new bytes are consumed
without modifying the authored reference.

Publish service-owned Prometheus observations:

- `gitstore_secret_resolution_total{consumer,purpose,tier,provider,reason}`;
- `gitstore_secret_resolution_duration_seconds{consumer,purpose,tier,provider}`;
- `gitstore_secret_resolution_inflight{consumer,purpose,tier,provider}`.

Labels come from fixed registered sets; `reason` is `success`, one of the seven
classes, `canceled` or `deadline_exceeded`. No logical names, key names,
namespaces, identifiers or paths in labels. Test every public formatter,
returned error, log and trace using injected marker secrets.
