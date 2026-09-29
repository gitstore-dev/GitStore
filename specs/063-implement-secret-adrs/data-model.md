# Data Model: Secret References and Resolution

No database table or Git-backed Secret resource is introduced. Only authored
File spec metadata changes; decoded material remains transient provider output.

## Authored reference entities

| Entity         | Field       | Rules                                                                             |
|----------------|-------------|-----------------------------------------------------------------------------------|
| SecretRef      | `kind`      | Required literal `SecretRef`                                                      |
| SecretRef      | `name`      | Required DNS label, 1..63 ASCII characters                                        |
| SecretRef      | `key`       | Optional; if present, 1..253 characters `[A-Za-z0-9._-]+`, excluding `.` and `..` |
| SecretRef      | `namespace` | Optional; if present nonempty and equal to containing resource namespace          |
| CredentialsRef | `kind`      | Required literal `CredentialsRef`                                                 |
| CredentialsRef | `type`      | Required versioned identifier, <=128 characters; grammar in reference contract    |
| CredentialsRef | `secretRef` | Required complete SecretRef                                                       |

Reject unknown properties, explicit empty optional values, null required
objects, whitespace, URI fragments and path separators in reference components.
An omitted key denotes the complete secret record. References have no UID,
ownerReferences, resourceVersion, status or lifecycle of their own.

`File.spec.source.credentialsRef` is optional, but whenever present it must be
the explicit wrapper. File metadata identifies the namespace; the nested
reference cannot override it. Existing File spec JSON storage is reused, with
normal Git admission updating projections rather than direct datastore edits.
Migration audit is operator-initiated bounded/paginated work, not a request-time
catalogue scan.

## Runtime-only entities

### ResolutionContext

- Tier: bootstrap or runtime; selected by service construction, never authored.
- Environment: deployment-owned logical environment.
- Namespace: owning resource's namespace for runtime; absent for bootstrap.
- Consumer and purpose: registered finite categories.
- Principal: authenticated caller/service identity supplied by the consumer.
- Resource identity: kind, namespace, repository and UID/name where applicable;
  metadata only, never an authorization decision supplied by an untrusted body.
- Binding: operator-owned provider configuration and tier authorization.
- Deadline: minimum of caller deadline and resolver budget.

Bindings for the two tiers are distinct even when both use the same provider
implementation. A resource cannot request the bootstrap binding or service
identity namespace. Runtime cross-namespace checks precede provider calls.

### SecretMaterial

`Values map[string][]byte` plus non-sensitive provider category and optional
opaque provider revision. Entire result is limited to 32 items/128 KiB decoded;
individual items are at most 64 KiB and encoded input at most 256 KiB.
No default JSON/YAML serialization or generic debug formatting of bytes is
permitted. Errors never return partial material.

Consumers own buffers only for the current operation. Clear mutable byte
buffers where feasible, drop parsed-key references afterward, and do not claim
guaranteed erasure of Go runtime copies. There is no private-key cache or
persisted resolver cache.

### CredentialType

Versioned consumer-owned registry. Initial runtime type:
`aws-access-key/v1` requires nonempty `accessKeyId` and `secretAccessKey`;
`sessionToken` is optional. Additional required keys mean a new type version.
Syntactically valid but unsupported types fail at consumption with
`UnsupportedType`; they do not trigger provider I/O merely to be rejected.

For a multi-item credential type, selecting a single item does not implicitly
grant access to siblings: missing required items yields `MissingKey`.

### BootstrapSigningRecord

Provider-side JSON envelope, not resource metadata:

```text
format: serviceaccount-signing-key/v1
values.privateKey: one supported PKCS#8 PEM private key
values.keyID: nonempty enrolled public-key identifier
```

Both items are read atomically from one record. The key ID is non-secret but
must match the assertion public key enrolled through existing spec-061 flows.
ServiceAccount UID, namespace, name and audiences stay in deployment config.
The record does not authorize a different subject. For this record the
deployment keyRef omits `key`; it addresses the whole record.

Existing raw/keyed configurations retain the configured key ID. Operators
migrate to the record form before claiming hot rotation with distinct IDs.

## State transitions

| Flow                      | Transition                                                          | Failure behavior                                     |
|---------------------------|---------------------------------------------------------------------|------------------------------------------------------|
| Reference validation      | authored -> accepted/rejected                                       | Field/reason only; no provider call or material      |
| Runtime contract consumer | accepted -> resolve -> type-check -> operation                      | Any failure blocks operation; retry re-resolves      |
| Bootstrap startup         | configured -> validate -> resolve -> sign -> authenticated          | Classified fatal failure; no anonymous fallback      |
| Token renewal             | usable token -> single-flight resolve/sign/exchange -> usable token | Bounded backoff; no extension beyond usable lifetime |
| Provider rotation         | old record -> atomic new record                                     | Enrollment overlap prevents mismatched key ID        |
| Process replacement       | no local state -> bootstrap                                         | Must authenticate using current provider material    |

Runtime acceptance does not imply `SecretResolved=True`. No File resolution
condition/status transition is written by this feature, because the production
File consumer is explicitly deferred.
