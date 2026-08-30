# ADR 0009: Credential and Secret Material Boundary

**Status**: Proposed

**Date**: 2026-08-30

**Audience**: GitStore API, controller, git-service, integration, and deployment authors.

## Context

GitStore has two distinct reasons to handle sensitive material, and they were
being designed independently, in different documents, with no shared boundary.

**First**, Git-backed resources need to *reference* secret material for outbound
integrations — object storage credentials, webhook signing keys, payment gateway
tokens, carrier service keys. [ADR 0001](0001-secretref-reference-contract.md)
defines `SecretRef` for exactly this: a provider-neutral pointer, authored in a
resource spec, resolved at reconcile time through a deployment-configured
provider. ADR 0001 names a future `CredentialsRef` wrapper but leaves it
unspecified.

**Second**, a GitStore *process* needs to prove its own identity to another
GitStore process — `gitstore-controller-manager` calling `gitstore-api`'s
GraphQL API, `gitstore-git-service` calling `gitstore-api` over gRPC.
`docs/implementation/021-controller_service_account_auth.md`, formalized as spec
`061-controller-serviceaccount-auth`, designs a `ServiceAccount` identity plane
and a controller-side `CredentialSource` for this.

These answer different questions and neither subsumes the other:

| | Resource secret (ADR 0001) | Process identity (spec 061) |
|---|---|---|
| Question | "Where is the secret material this resource needs?" | "How does this process prove who it is?" |
| Plane | Data plane | Control plane |
| Authored by | A resource spec in Git | Deployment configuration |
| Consumer | A reconciler acting *on behalf of* a resource | A client acting *as itself* |
| Direction | GitStore reads a secret to call **out** | GitStore proves identity to call **in** |
| Failure mode | Reconciliation blocked, `SecretResolved=False` | Process cannot authenticate at all |

The problem this ADR fixes is not that these two concepts overlap — it is that
**both ultimately need secret bytes loaded into a process, and each was inventing
its own way to do it.** Spec 061 as originally drafted reads its
`ServiceAccount` private key from a bespoke
`GITSTORE_CONTROLLER__SERVICEACCOUNT__PRIVATE_KEY_FILE` filesystem path, with no
reference to ADR 0001, no provider indirection, and no shared error taxonomy —
precisely the provider-specific-path coupling ADR 0001 exists to prevent.
Left unreconciled, GitStore would ship two parallel credentialing systems with
different configuration surfaces, different failure semantics, different
redaction rules, and different rotation stories.

## Decision

GitStore has **one** secret-material acquisition boundary and **two** consumers
of it. This ADR draws that boundary, specifies the `CredentialsRef` wrapper ADR
0001 deferred, and defines the bootstrap tier that process identity requires.

### 1. The boundary

Every path by which secret bytes enter any GitStore process MUST go through the
`SecretResolver` interface defined in ADR 0001. No component may read secret
material directly from a filesystem path, environment variable, or provider SDK
outside a `SecretResolver` implementation.

```
                    ┌──────────────────────────────┐
   resource spec ──▶│  CredentialsRef → SecretRef  │──┐
   (Git-backed)     └──────────────────────────────┘  │
                                                      ▼
                                            ┌──────────────────┐
                                            │  SecretResolver  │──▶ provider
                                            └──────────────────┘   (Vault, K8s,
                                                      ▲             AWS, file…)
                    ┌──────────────────────────────┐  │
   deployment    ──▶│  identity: bootstrap SecretRef│──┘
   configuration    └──────────────────────────────┘
```

`SecretRef` remains the single reference primitive. `CredentialsRef` (§2) adds
credential-type semantics for consumers that need them. Process identity (§3)
uses the same `SecretRef` shape resolved through a restricted, local-only
provider tier.

**This ADR does not make process identity a Git-backed resource.** A
`ServiceAccount`'s private key is never authored in Git, never referenced from a
resource spec, and never resolvable through a resource's namespace context. The
shared surface is the resolver interface and its error/redaction/observability
contract — not the authoring model.

### 2. `CredentialsRef`

`CredentialsRef` is the semantic wrapper ADR 0001 named but left undefined. It
answers "what protocol is this for, and which fields must be present?" while
delegating "where are the bytes?" to `SecretRef`.

```yaml
credentialsRef:
  kind: CredentialsRef
  type: aws-access-key/v1
  secretRef:
    kind: SecretRef
    name: catalog-assets-writer
```

| Field       | Required | Description                                                                       |
|-------------|----------|-----------------------------------------------------------------------------------|
| `kind`      | yes      | Literal discriminator. Must be `CredentialsRef`.                                  |
| `type`      | yes      | Credential type identifier with an explicit version suffix, e.g. `aws-access-key/v1`. |
| `secretRef` | yes      | An ADR 0001 `SecretRef` locating the material.                                     |

A credential `type` defines the required key set within the resolved secret.
Resolution fails with ADR 0001's `MissingKey` when a required key for the
declared type is absent, and with `UnsupportedType` when a consumer does not
implement the declared type. Type identifiers are versioned so a new required
key is a new type version, never a silent change to an existing one.

ADR 0001's temporary short form — a `credentialsRef` field holding a bare
`SecretRef` — remains valid for existing resource docs and means "use the
consumer-defined default credential type, load the whole record." New resource
contracts MUST use the explicit `CredentialsRef` form.

### 3. Bootstrap tier for process identity

A process's own identity credential is subject to a constraint no resource
secret has: **it is the credential the process needs in order to reach the
network at all.** Resolving `gitstore-controller-manager`'s `ServiceAccount`
private key through a resolver that itself requires an authenticated call to
`gitstore-api` is circular and cannot work.

Therefore `SecretResolver` implementations are classified into two tiers:

| Tier | May be used for | Network dependency | Providers |
|---|---|---|---|
| **Bootstrap** | A process's own identity material, resolved during startup before any authenticated call | MUST NOT perform any network call that itself requires a GitStore-issued credential | Local file, environment variable, mounted volume, process supervisor, or a secret manager reachable with ambient platform credentials (e.g. IRSA, workload identity) |
| **Runtime** | Resource-referenced secret material (`SecretRef`/`CredentialsRef` in a resource spec) | Unrestricted | Any ADR 0001 provider |

Rules:

- Identity material MUST be resolved through a bootstrap-tier resolver. A
  component MUST NOT accept a raw filesystem path or inline key value as
  configuration in place of a resolver reference.
- A bootstrap-tier resolver MUST NOT be used to satisfy a resource-authored
  `SecretRef`. The tiers are not interchangeable in either direction.
- Bootstrap resolution failure is fatal to process startup and MUST fail closed,
  using ADR 0001's error classes unchanged.
- Because the ambient-credential case (IRSA, workload identity) is a network
  call that does *not* depend on a GitStore-issued credential, it is permitted —
  the prohibition is specifically on circular dependency, not on all I/O.

Configuration therefore names a logical reference plus a bootstrap provider
binding, not a physical path:

```yaml
# deployment configuration (not Git-backed)
identity:
  serviceAccount:
    namespace: controllers
    name: category-taxonomy
  keyRef:
    kind: SecretRef
    name: category-taxonomy-signing-key
    key: privateKey
secretProviders:
  bootstrap:
    type: file           # or: env | k8s | vault | aws-secrets-manager
    basePath: /etc/gitstore/secrets
```

The `type: file` bootstrap provider is the local-development and
Docker-Compose-friendly default, and is the direct, supported replacement for
a bespoke private-key-path environment variable.

### 4. Uniform obligations

Both tiers and both consumers inherit ADR 0001's rules without modification:

- Fail closed. No component continues with unauthenticated or anonymous
  behavior when required material cannot be resolved.
- Secret bytes never appear in Git, resource `status`, projections, GraphQL
  responses, logs, errors, metrics labels, traces, or audit diffs. This
  explicitly covers access tokens, client assertions, and private keys.
- Resolved material stays in process memory; persisted caches are out of scope.
  In-memory caches carry bounded TTLs and are disabled for private keys unless
  a component explicitly owns that risk.
- Rotation is performed out of band in the provider. Consumers re-resolve after
  authentication or signing failures consistent with rotation.
- Observability uses ADR 0001's structured fields and metric shapes, with
  `consumer` and `purpose` distinguishing identity resolution from resource
  secret resolution.

### 5. What this ADR does not change

- ADR 0001's `SecretRef` shape, names, resolution identity, error classes,
  validation phases, and `ownerReferences` rules are unchanged.
- Spec 061's `ServiceAccount` datastore model, assertion/token claim contracts,
  and provider chain behavior are unchanged. `ServiceAccount` **public** keys
  are not secret material and correctly remain in the datastore, outside this
  boundary entirely.
- Spec 060's `users.yaml` bcrypt password hashes are verifier material for
  human credentials, not process identity, and are out of scope here.
- `auth.grpc.hmac_secret` and `auth.jwt.secret` continue to be read from
  configuration as they are today. Routing them through a bootstrap resolver is
  a natural follow-on but is deliberately deferred so this ADR's adoption is not
  coupled to a change in two already-shipped configuration surfaces.

## Consequences

Positive consequences:

- One secret-acquisition boundary, one error taxonomy, one redaction rule set,
  and one rotation story across resource integrations and process identity.
- Process identity gains provider portability for free: the same deployment can
  move its controller signing key from a local file to Vault or a Kubernetes
  Secret with no code change and no new configuration key.
- `CredentialsRef` is specified, closing an ADR 0001 open question and removing
  the ambiguity that let spec 061 drift.
- The bootstrap/runtime tier split makes the circular-dependency hazard explicit
  and reviewable instead of implicit in each component's startup code.

Negative consequences:

- Spec 061 must replace its `ServiceAccountPrivateKeyFile` configuration surface
  with a `SecretRef` plus bootstrap provider binding before it lands, and its
  `CredentialSource` must depend on a resolver rather than `os.ReadFile`.
- A minimal file-based bootstrap provider must exist before any component can
  adopt this, adding a small prerequisite to spec 061's critical path.
- Two tiers is more concept than one resolver, and the tier rule is a review
  obligation that static typing alone does not enforce.

## Cross-references

- [ADR 0001: SecretRef Reference Contract](0001-secretref-reference-contract.md)
  — the reference primitive and resolver interface this ADR builds on. ADR 0001's
  "Relationship To CredentialsRef" section is specified here; its open question
  "Should future `CredentialsRef` be an inline object only, a datastore-only
  resource, or both?" is answered: inline object only, in v1.
- `docs/implementation/021-controller_service_account_auth.md` — the process
  identity design this ADR bounds.
- `specs/061-controller-serviceaccount-auth/` — derives from this ADR.
- `docs/implementation/020-pluggable_auth_architecture.md` — the AuthN provider
  chain that consumes the identity credential this ADR governs the acquisition of.

## Dependency graph position

This ADR sits above ADR 0001 and below any resource contract or spec that
consumes secret material:

```
ADR 0001 (SecretRef primitive, SecretResolver interface)
    │
    ▼
ADR 0009 (this: boundary, CredentialsRef, bootstrap tier)
    │
    ├──▶ spec 061 (process identity: ServiceAccount signing key)
    └──▶ resource contracts (File/Product/WebhookEndpoint credentialsRef)
```

## Alternatives considered

### Amend ADR 0001 in place

Rejected. ADR 0001 is deliberately scoped as the lowest-level, provider-neutral
reference primitive for Git-backed resources. Folding process-identity bootstrap
and tier rules into it would blur that scope and make the resource-authoring
contract harder to read for its actual audience. A separate ADR that ADR 0001 is
cross-referenced from keeps each document single-purpose.

### Let process identity keep its own configuration surface

Rejected — this is the status quo this ADR exists to correct. It yields two
credentialing systems with divergent failure, redaction, and rotation behavior,
and it hard-codes provider topology (a filesystem path) into deployment
configuration, which is the same coupling ADR 0001 rejected for manifests.

### Model `ServiceAccount` identity keys as Git-backed resources with `SecretRef`

Rejected. Private keys must never be referenced from Git-backed desired state,
and a resource-authored reference would inherit resource namespace resolution
semantics that do not apply to a process's own bootstrap identity. Reusing the
`SecretRef` *shape* through a restricted bootstrap tier gets the consistency
benefit without the authoring hazard.

### One untiered resolver

Rejected. Without an explicit bootstrap tier, nothing prevents a deployment from
configuring a process's identity key to resolve through a provider that requires
that same identity to reach — a circular dependency that fails at startup in a
way that is hard to diagnose. The tier makes the constraint declarative.

## Open questions

- Should `auth.grpc.hmac_secret` and `auth.jwt.secret` migrate to bootstrap-tier
  resolution, and if so, on what deprecation schedule for the existing
  environment variables?
- Should a `CredentialsRef` type registry be enforced at admission, or remain a
  consumer-side contract validated only at resolution time?
- Do bootstrap-tier providers need a conformance test suite distinct from ADR
  0001's proposed provider conformance tests?
- Should the bootstrap tier support a "no-op/absent" mode for deployments that
  legitimately run with no process identity (for example, a single-process
  development stack with `anonymous` in the AuthN chain)?
