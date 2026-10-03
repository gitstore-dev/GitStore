# Contract: Resource Secret References

**Applies to**: Go validation/admission, File JSON schema, authored GraphQL
projection, Git hook rejection behavior. This is the strict-release contract.

## Authored shape

```yaml
credentialsRef:
  kind: CredentialsRef
  type: aws-access-key/v1
  secretRef:
    kind: SecretRef
    name: catalog-assets-writer
```

Only `SecretRef` may carry optional `key` and `namespace`.

| Field                      | Validation                                                                   |
|----------------------------|------------------------------------------------------------------------------|
| `CredentialsRef.kind`      | Exactly `CredentialsRef`                                                     |
| `CredentialsRef.type`      | `^[a-z][a-z0-9-]*/v[1-9][0-9]*$`, max 128 ASCII characters                   |
| `CredentialsRef.secretRef` | Required non-null object                                                     |
| `SecretRef.kind`           | Exactly `SecretRef`                                                          |
| `SecretRef.name`           | `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`, length 1..63                              |
| `SecretRef.key`            | If present, `^[A-Za-z0-9._-]+$`, length 1..253, not `.` or `..`              |
| `SecretRef.namespace`      | If present, nonempty valid namespace name equal to containing File namespace |

Reject unknown properties at both levels, explicit null optional fields,
empty optional strings, wrong scalar types, path separators, URI fragments
and whitespace. Reject a bare `{kind: SecretRef, name: ...}` at
`credentialsRef`; never infer a default credential type in the strict release.
No `provider`, `path`, `value`, version pin or optional/fail-open property.

No key means the whole record. A key selects one item and does not expand
access to sibling items. Valid shape does not prove provider existence,
type implementation or required material availability.

The wrapper's absence remains valid wherever the File source already permits
no credentials. A present but unresolved wrapper never implies anonymous use.

## Enforcement

1. Go parser preserves presence information and rejects unknown fields in the
   reference subtree; permissive YAML decoding must not erase malformed input
   before validation.
2. Shared validation checks shape, type syntax, namespace and key/name rules.
3. Both `ValidateResources` and `AdmitResources` reject the same invalid
   reference before a File projection is written.
4. Rust's existing pre-receive gRPC call surfaces the file/field/reason. It
   performs no secret material lookup and receives no secret bytes.
5. JSON schema carries the same literals/patterns/lengths/closed objects.
   Namespace equality is a semantic Go check, not falsely claimed as an
   ordinary static JSON-schema property.

Errors contain a bounded field path and reason, not rejected values. Use
`InvalidRef` for malformed references and `CrossNamespaceRef` as the admission
reason for a namespace mismatch; the resolver's corresponding class remains
`InvalidRef`. Unsupported syntactically valid credential types fail in the
runtime consumer as `UnsupportedType`, not during stateless schema validation.

## GraphQL output

`FileSource.credentialsRef` returns the strict object below directly.
GitStore is alpha with no production deployments: there is no transitional
schema or legacy-reader guarantee. Update development clients to select nested
metadata. No type is inferred and no secret material is exposed.

```graphql
type CredentialsRef {
  kind: String!
  type: String!
  secretRef: SecretRef!
}

type SecretRef {
  kind: String!
  name: String!
  key: String
  namespace: String
}
```

Change `FileSource.credentialsRef` to nullable `CredentialsRef`. Preserve all
other FileSource fields and existing authorization. No secret value, provider
binding, resolved material or private key field is added.

This is an Admin projection under ADR-0012. If schemas have moved at
implementation time, modify admin/common sources and their generator inputs,
never a glob loading both endpoints. Do not add Storefront fields or resurrect
ADR-0011's removed directive `mode`/`SCOPE`. Inline SecretRef is not a
core/CRD object-reference edge and carries no ownership/readiness lifecycle.
Resolved bytes cannot enter Markdown IR, release snapshots or public
projections (ADRs 0013/0014/0016).

This is a breaking field-type change. Do not disguise the nested object as
the old type or return fabricated empty names. gqlgen output is regenerated
from source schemas. Preparatory readers and clients must satisfy the rollout
contract before strict deployment.

## Verification fixtures

Use the same fixture matrix in Go and schema tests:

- valid wrapper with omitted key/namespace, matching namespace, boundary lengths;
- wrong kind, bare reference, missing/empty type, unsupported but well-formed type;
- missing/null nested reference, unknown fields, non-string values;
- names of 0/64 characters, uppercase, leading/trailing dash;
- keys of 0/254 characters, slash/backslash, `.`, `..`, whitespace and `#`;
- empty/null/foreign namespace;
- selected single key versus whole record.

Schema/Go outcome parity excludes only documented contextual checks such as
namespace equality. Add API projection/GraphQL round-trip and Rust-hook
rejection tests, with no provider invocation for any stateless fixture.
Use current `new_commit_sha` and `authorization.actor` protobuf fields.
Git-backed admission is validating-only (ADR-0015); rejected legacy references
are migrated by operator-authored commits, not admission mutation/writeback.
