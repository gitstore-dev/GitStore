# ADR 0001: SecretRef Reference Contract

**Status**: Proposed

**Date**: 2026-06-24

**Audience**: GitStore API, controller, integration, and deployment authors.

## Context

GitStore resources intentionally separate reviewable desired state from runtime
state. Git-backed resources may need to configure integrations such as object
storage, payment gateways, webhooks, tax providers, carrier services, and future
automation agents. Those integrations need credentials, signing keys, private
certificates, tokens, and provider secrets.

Secret material must not be committed to Git, written into resource `status`,
stored in generated projections, or exposed through GraphQL reads. At the same
time, Git-backed desired state must be able to refer to the secret material that
runtime components need.

The existing resource docs already use fields such as `secretRef` and
`credentialsRef`, but they do not define the common reference contract. This ADR
defines `SecretRef` as the lowest-level, provider-neutral reference primitive.
Future `CredentialsRef` designs build on this primitive by adding credential
type and usage semantics.

## Decision

GitStore will define `SecretRef` as a structured reference object that points to
secret material managed outside GitStore's Git-backed resource store.

`SecretRef` is not a Git-backed `Secret` resource and does not define an API for
reading secret bytes through GitStore. It is a typed pointer stored in specs,
resolved at runtime by authorized components through a deployment-configured
secret provider.

### Canonical Shape

```yaml
kind: SecretRef
name: catalog-assets-writer
key: accessKeyId
```

Fields:

| Field       | Required | Description                                                                                                                      |
|-------------|----------|----------------------------------------------------------------------------------------------------------------------------------|
| `kind`      | yes      | Literal discriminator. Must be `SecretRef`.                                                                                      |
| `name`      | yes      | Logical secret name inside the GitStore namespace.                                                                               |
| `key`       | no       | Optional item name within a multi-key secret. When omitted, the reference targets the whole secret record.                       |
| `namespace` | no       | GitStore namespace override. Defaults to the namespace of the containing resource. Cross-namespace resolution is rejected in v1. |

`SecretRef` deliberately does not include a provider name, provider URI, version
pin, inline value, or optional/fail-open flag.

### Names

`name` uses the same DNS-label-compatible shape as other GitStore resource
names:

```text
[a-z0-9]([-a-z0-9]*[a-z0-9])?
```

The maximum length is 63 characters.

`key`, when set, uses a Kubernetes Secret-compatible key shape:

```text
[A-Za-z0-9._-]+
```

The maximum length is 253 characters. Slash, backslash, path traversal, URI
fragments, and whitespace are invalid.

### Resolution Identity

At runtime, a `SecretRef` resolves with the following context:

```text
(gitstore_namespace, environment, secret_name, optional_key, consumer, purpose)
```

Only `secret_name` and optional `key` are authored in the normal case. The
GitStore namespace comes from the containing resource. The environment is
provided by the runtime context, for example dev, staging, or prod. Consumer and
purpose are provided by the component resolving the secret.

This allows the same Git-backed manifest to be promoted across environments
while resolving to different physical secret material.

### Provider Binding

Physical secret manager binding is an operator concern, not a resource-authoring
concern. A Git-backed manifest must not encode whether a secret lives in
Kubernetes, Vault, AWS Secrets Manager, GCP Secret Manager, SOPS, environment
variables, or another backend.

Provider implementations map the logical identity to physical storage. Examples:

| Provider            | Example physical mapping                                                       |
|---------------------|--------------------------------------------------------------------------------|
| Kubernetes Secret   | `metadata.name = <gitstore-namespace>-<secret-name>`, item from `.data[<key>]` |
| Vault KV            | `kv/gitstore/<environment>/<namespace>/<name>`, item from data key             |
| AWS Secrets Manager | `/gitstore/<environment>/<namespace>/<name>`, item from JSON key               |
| Local development   | Environment or local file adapter owned by the process supervisor              |

The exact mapping is configured outside Git-backed manifests and may vary by
deployment.

### Resolution Contract

Runtime components resolve through a `SecretResolver`-style boundary:

```go
type SecretRef struct {
    Kind      string
    Namespace string
    Name      string
    Key       string
}

type SecretMaterial struct {
    Values   map[string][]byte
    Metadata SecretMetadata
}

type SecretResolver interface {
    ResolveSecret(ctx context.Context, ref SecretRef, req SecretResolutionRequest) (SecretMaterial, error)
}
```

`SecretResolutionRequest` includes environment, resource reference, consumer
kind, purpose, and caller/service identity. It does not include fallback literal
values.

Resolvers must return one of these error classes:

| Error class           | Meaning                                                          |
|-----------------------|------------------------------------------------------------------|
| `InvalidRef`          | The reference object is malformed.                               |
| `NotFound`            | No secret exists for the logical name in the resolution context. |
| `MissingKey`          | The secret exists but the requested key is absent.               |
| `Forbidden`           | The resolver identity is not authorized to read the secret.      |
| `ProviderUnavailable` | The configured secret backend cannot be reached.                 |
| `UnsupportedType`     | The consumer requested a shape the provider cannot return.       |
| `ValueTooLarge`       | The material exceeds configured size limits.                     |

All error classes fail closed. Components must not continue with unsigned,
unauthenticated, or anonymous integration behavior when a required secret cannot
be resolved.

### Validation Phases

GitStore validates `SecretRef` in phases:

| Phase             | Behavior                                                                                                                                                                                      |
|-------------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| Parse/pre-receive | Validate object shape, `kind`, `name`, `key`, and same-namespace rule only. Do not call a secret manager from the stateless Git hook path.                                                    |
| Admission         | May verify provider reachability and existence if the admitting component is authorized and the check is cheap. Existence checks are best-effort unless the resource contract says otherwise. |
| Reconcile/runtime | Resolve just-in-time before the operation that needs secret material. This is the authoritative resolution point.                                                                             |
| Status            | Write only conditions and reason codes, never values.                                                                                                                                         |

Recommended condition names:

| Condition           | True means                                                      | False reasons                                                                   |
|---------------------|-----------------------------------------------------------------|---------------------------------------------------------------------------------|
| `SecretRefAccepted` | The authored reference shape is valid.                          | `InvalidRef`, `CrossNamespaceRef`                                               |
| `SecretResolved`    | Runtime secret resolution succeeded for the required operation. | `NotFound`, `MissingKey`, `Forbidden`, `ProviderUnavailable`, `UnsupportedType` |

`SecretResolved` is operation-specific. A resource can be accepted while a
runtime operation remains blocked until the secret is provisioned.

### OwnerReferences And Managed Secret Material

`SecretRef` is an inline reference object, so it cannot itself be created,
owned, garbage-collected, or carry `ownerReferences`. Controllers do not create
`SecretRef` objects. Controllers may create the managed secret material, or a
future `SecretBinding`/`SecretClaim` resource, only when the containing resource
contract explicitly permits GitStore-managed secrets.

This mirrors the Kubernetes owner chain pattern: a high-level resource may cause
a controller to create lower-level managed objects, and those lower-level
objects carry `ownerReferences` back to the object that caused them. In GitStore,
the owner relationship belongs on the managed object or binding, not on the
inline reference.

Secret lifecycle behavior is contract-specific:

| Resource contract | Controller behavior | Missing target behavior |
|-------------------|---------------------|-------------------------|
| Caller-provided credential | MUST NOT create or overwrite secret material. | Reconciliation fails closed with `SecretResolved=False`, reason `NotFound` or `MissingKey`. |
| GitStore-generated secret | MAY generate secret material if absent and write an owned managed object or provider secret. | Controller creates material, then records `SecretResolved=True` when usable. |
| GitStore-managed secret slot | MAY create an owned `SecretBinding`/`SecretClaim` placeholder without inventing external credentials. | Runtime operation remains blocked until material is provisioned. |
| Optional credential | MUST treat absent `SecretRef` as unauthenticated or disabled behavior defined by the resource contract. | A present but unresolved `SecretRef` still fails closed for the credentialed operation. |

Managed secret objects MUST include enough ownership metadata to support
garbage collection and audit:

```yaml
apiVersion: platform.gitstore.dev/v1beta1
kind: SecretBinding
metadata:
  name: erp-order-events-signing
  namespace: acme-store
  ownerReferences:
  - apiVersion: integrations.gitstore.dev/v1beta1
    kind: WebhookEndpoint
    name: erp-order-events
    uid: 018f6f1e-8a3c-7000-9000-000000000001
spec:
  secretRef:
    kind: SecretRef
    name: erp-order-events-signing
  purpose: webhook.hmac
  managementPolicy: GenerateIfAbsent
```

Examples:

- A `WebhookEndpoint` MAY allow GitStore to generate an HMAC signing secret
  because GitStore is the producer and verifier of that secret.
- A `PaymentGatewayConfig` MUST require a pre-existing provider credential
  unless its resource contract explicitly defines a safe generated credential
  flow with the payment provider.
- An S3 `credentialsRef` for object storage MUST NOT cause GitStore to invent
  AWS credentials. The secret must already exist or be provisioned by an
  external operator workflow.

Controller-created secret material MUST follow the same security rules as
externally provisioned material: no secret bytes in Git, status, projections,
logs, metrics labels, traces, GraphQL responses, or audit diffs.

### Security Requirements

Secret handling uses these mandatory rules:

- Secret bytes MUST NOT be committed to Git.
- Secret bytes MUST NOT be stored in GitStore resource specs, status, read-model
  projections, audit payload diffs, or GraphQL responses.
- Secret bytes MUST NOT be logged, included in errors, exposed in metrics labels,
  or attached to traces.
- `SecretRef` names are identifiers, not secret values. They MAY appear in
  specs and status, but logs SHOULD prefer structured fields and avoid
  high-cardinality metric labels.
- Cross-namespace `SecretRef` resolution is rejected in v1, even when the field
  is present.
- Secret provider selection and physical paths are operator-controlled.
  Git-authored resources MUST NOT contain provider-specific secret URIs.
- Resolved material MUST remain in process memory only. Persisted caches of
  secret bytes are out of scope for v1.
- In-memory caches, if used, MUST have bounded TTLs and MUST be disabled for
  private keys unless a component explicitly owns that risk.
- All required secret resolution failures fail closed.
- GitStore API surfaces MUST NOT provide a generic "read secret value" operation.

### Rotation

`SecretRef` points to the active secret material for a logical name. It does not
pin a physical provider version in v1.

Rotation is performed out of band in the configured secret provider. Components
that cache resolved material must use short TTLs and must retry resolution after
authentication or signing failures that are consistent with rotation.

If strict version pinning is required later, it should be added as an explicit
extension after the rotation and rollback model is designed. It should not be
added to the base v1 reference shape.

### Relationship To CredentialsRef

`SecretRef` is intentionally opaque. It answers only "where is the secret
material?" It does not answer "what protocol is this for?", "which fields are
required?", or "how should these bytes be interpreted?"

`CredentialsRef` will build on `SecretRef` by adding credential semantics:

```yaml
credentialsRef:
  kind: CredentialsRef
  type: aws-access-key/v1
  secretRef:
    kind: SecretRef
    name: catalog-assets-writer
```

For compatibility with early resource docs, fields named `credentialsRef` may
temporarily accept a direct `SecretRef` when the consumer contract defines the
expected secret shape:

```yaml
credentialsRef:
  kind: SecretRef
  name: catalog-assets-writer
```

That short form means "use the consumer-defined default credential type and load
the whole secret record." New designs should prefer `CredentialsRef` once it is
specified.

### Examples

Webhook signing secret:

```yaml
apiVersion: integrations.gitstore.dev/v1beta1
kind: WebhookEndpoint
metadata:
  name: erp-order-events
  namespace: acme-store
spec:
  url: https://erp.example.com/gitstore/events
  eventTypes:
  - order.created
  secretRef:
    kind: SecretRef
    name: erp-webhook-signing
    key: hmac
```

Object storage credentials using the temporary direct `SecretRef` form:

```yaml
source:
  type: s3
  uri: s3://catalog-assets/products/product-hero.jpg
  credentialsRef:
    kind: SecretRef
    name: catalog-assets-writer
```

Future explicit credentials wrapper:

```yaml
source:
  type: s3
  uri: s3://catalog-assets/products/product-hero.jpg
  credentialsRef:
    kind: CredentialsRef
    type: aws-access-key/v1
    secretRef:
      kind: SecretRef
      name: catalog-assets-writer
```

### GraphQL And API Representation

GraphQL may expose the `SecretRef` object as authored:

```graphql
type SecretRef {
  kind: String!
  name: String!
  key: String
  namespace: String
}
```

GraphQL must not expose resolved values. Mutations that accept resources
containing `SecretRef` validate only the reference object unless the specific
mutation contract explicitly performs an existence check.

### Observability

Secret resolution logs should use structured fields:

```text
event=secret_resolution_failed
namespace=acme-store
environment=prod
secret_name=catalog-assets-writer
consumer=FileProcessor
purpose=object-storage.write
reason=MissingKey
```

Metrics should aggregate by consumer, purpose, provider, and reason. They should
not use secret names as labels by default.

Recommended metrics:

| Metric                                        | Type      | Labels                                                 |
|-----------------------------------------------|-----------|--------------------------------------------------------|
| `gitstore_secret_resolution_total`            | counter   | `consumer`, `purpose`, `provider`, `outcome`, `reason` |
| `gitstore_secret_resolution_duration_seconds` | histogram | `consumer`, `purpose`, `provider`                      |
| `gitstore_secret_cache_hit_total`             | counter   | `consumer`, `purpose`, `provider`                      |

### Compatibility

This ADR does not require a datastore migration. `SecretRef` is stored only as
part of existing resource spec blobs until a typed schema is added for resources
that use it.

Existing docs that mention `credentialsRef.kind: SecretRef` remain valid as a
temporary short form. Future docs should reference this ADR and use
`CredentialsRef` where credential semantics matter.

## Consequences

Positive consequences:

- Git-backed resources can safely refer to required secret material without
  storing secret bytes.
- Resource specs stay portable across environments and secret-manager vendors.
- `CredentialsRef` can be designed as a semantic wrapper instead of duplicating
  low-level secret resolution rules.
- Runtime components get a single fail-closed resolution model.

Negative consequences:

- Users must provision secrets outside GitStore before runtime operations can
  succeed.
- Pre-receive validation cannot guarantee that a referenced secret exists.
- Runtime failures can occur after a manifest is accepted if the provider is
  unavailable or the secret is missing.
- No version pinning means reproducibility depends on provider audit logs and
  rotation discipline.

## Alternatives Considered

### Git-backed Secret resource

Rejected. Committing encrypted or cleartext secret resources to Git creates
review, rotation, deletion, and access-control hazards. Even encrypted blobs
would require key-management semantics that are outside the current Git-backed
resource model.

### Provider-specific URI

Rejected for authored resources:

```yaml
secretRef: vault://kv/prod/acme/catalog-assets-writer#accessKeyId
```

This leaks deployment topology into portable manifests, makes environment
promotion harder, and expands the validation and security surface.

### Kubernetes SecretKeyRef clone

Partially adopted. `name` and `key` follow the proven Kubernetes shape, but
GitStore does not expose Kubernetes namespaces or require Kubernetes as the
physical backend. GitStore namespace and runtime environment are the logical
resolution context.

### Inline values with redaction

Rejected. Redaction is not a security boundary. Once a secret value appears in a
Git-backed document, it can leak through history, review tools, generated
artifacts, logs, screenshots, and caches.

### Optional/fail-open flag on SecretRef

Rejected for v1. Optionality belongs to the containing resource contract. A
present `SecretRef` that cannot be resolved should fail closed.

## Open Questions

- Should GitStore define an operator-managed `SecretBinding` or `SecretStore`
  resource for mapping logical refs to physical providers?
- Should future `CredentialsRef` be an inline object only, a datastore-only
  resource, or both?
- What conformance tests should provider adapters pass for rotation, caching,
  and failure classification?
- Should any production deployments require admission-time existence checks for
  specific high-risk resource kinds such as `PaymentGatewayConfig`?
