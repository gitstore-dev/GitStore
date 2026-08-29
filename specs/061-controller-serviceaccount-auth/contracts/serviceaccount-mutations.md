# Contract: ServiceAccount GraphQL Mutations

New file `shared/schemas/serviceaccount.graphqls`. All four mutations are additive
— no existing mutation's shape changes (Constitution Principle III).

```graphql
"""
Issues a short-lived token after the caller proves possession of an enrolled
ServiceAccount private key. Mirrors Kubernetes' TokenRequest result while
replacing kubelet attestation with a portable signed client assertion.
"""
type Mutation {
  issueServiceAccountToken(input: IssueServiceAccountTokenInput!): IssueServiceAccountTokenPayload!
  createServiceAccount(input: CreateServiceAccountInput!): CreateServiceAccountPayload!
  rotateServiceAccountKey(input: RotateServiceAccountKeyInput!): CreateServiceAccountPayload!
  deleteServiceAccount(input: DeleteServiceAccountInput!): DeleteServiceAccountPayload!
}

input ObjectMetaInput {
  namespace: String!
  name: String!
}

type ObjectMeta {
  namespace: String!
  name: String!
  uid: String!
  creationTimestamp: DateTime!
}

input IssueServiceAccountTokenInput {
  apiVersion: String! = "authentication.gitstore.dev/v1beta1"
  kind: String! = "TokenRequest"
  metadata: ObjectMetaInput!
  spec: TokenRequestSpec!
}

input TokenRequestSpec {
  audience: String    # defaults to "gitstore-api"
  ttlSeconds: Int      # server clamps to auth.serviceaccount.max_ttl
}

type IssueServiceAccountTokenPayload {
  apiVersion: String!
  kind: String!
  metadata: ObjectMeta!
  status: TokenRequestStatus!
}

type TokenRequestStatus {
  token: String!
  expiresAt: DateTime!
}

input ServiceAccountPublicKeyInput {
  kid: String!
  algorithm: String! # "Ed25519" preferred; "ECDSA-P256" acceptable
  publicKeyPEM: String!
}

input CreateServiceAccountInput {
  apiVersion: String! = "authentication.gitstore.dev/v1beta1"
  kind: String! = "ServiceAccount"
  metadata: ObjectMetaInput!
  publicKeys: [ServiceAccountPublicKeyInput!]!
}

input RotateServiceAccountKeyInput {
  metadata: ObjectMetaInput!
  add: [ServiceAccountPublicKeyInput!]!
  removeKids: [String!]!
}

type CreateServiceAccountPayload {
  apiVersion: String!
  kind: String!
  metadata: ObjectMeta!
  keyIDs: [String!]!
  disabled: Boolean!
}

input DeleteServiceAccountInput {
  apiVersion: String! = "authentication.gitstore.dev/v1beta1"
  kind: String! = "ServiceAccount"
  metadata: ObjectMetaInput!
}

type DeleteServiceAccountPayload {
  apiVersion: String!
  kind: String!
  metadata: ObjectMeta!
}
```

## Authorization per mutation

| Mutation | Required authentication | Required authorization | FR |
|---|---|---|---|
| `issueServiceAccountToken` | `AuthMethod == "serviceaccount-assertion"` | Field-level gate: asserted subject/UID exactly matches `input.metadata` — no `rbac-local` role can substitute for this; see `serviceaccount-provider.md`'s field-gate note. | FR-006, FR-010 |
| `createServiceAccount` | Any authenticated principal | `rbac-local` action `serviceaccount.create` (new action string, bound only to `admin` by default policy example, never hardcoded) | FR-002, FR-003 |
| `rotateServiceAccountKey` | Any authenticated principal | `rbac-local` action `serviceaccount.key.rotate` | FR-002, FR-004 |
| `deleteServiceAccount` | Any authenticated principal | `rbac-local` action `serviceaccount.delete` | FR-002, FR-005 |

`createServiceAccount`/`rotateServiceAccountKey`/`deleteServiceAccount` are authorized through the same `GraphQLFieldAuthorizer` seam `category.status.write` already uses — no new authorization mechanism, only new action strings, exactly as FR-002 requires ("via the existing rbac-local/AuthZ mechanism, not a new hardcoded check").

## Validation rules

- `createServiceAccount`: reject if a `ServiceAccount` already exists for `metadata.namespace`/`metadata.name` (FR-003); reject if `publicKeys` is empty (FR-003, Edge Cases).
- `rotateServiceAccountKey`: `add` and `removeKids` may both be non-empty in the same call (overlap-window rotation, FR-004); reject if the resulting key set would be empty (mirrors `createServiceAccount`'s zero-key rejection — an account must always have at least one enrolled key while enabled).
- `issueServiceAccountToken`: `spec.ttlSeconds`, if provided, is clamped to `auth.serviceaccount.max_ttl`, never exceeded (FR-013's TTL edge case); `spec.audience`, if provided, must be a value the server is configured to issue for (default `gitstore-api`).
- `deleteServiceAccount`: idempotent — deleting an already-deleted account (by UID) is a no-op success, mirroring the general pattern already used for `deleteRepository`-style idempotent deletion elsewhere in this codebase.
