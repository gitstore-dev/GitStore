# Contract: Bootstrap Identity Renewal and Isolation

## Baseline and unchanged protocol

Require spec 061 plus merged #429 on every participating API/controller.
Continue using existing enrollment, public-key rotation, assertion verification
and AuthN/AuthZ providers. The following shape is already implemented:

```graphql
mutation IssueServiceAccountToken($input: IssueServiceAccountTokenInput!) {
  issueServiceAccountToken(input: $input) {
    tokenRequest {
      status { token expirationTimestamp }
    }
  }
}
```

Input metadata uses shared `ObjectMetaInput`; spec uses `audiences` and
`expirationSeconds`. Lifecycle payloads contain `serviceAccount`, including
the separate rotation payload. Sharing metadata does not give a resource access
to a ServiceAccount private key or make that identity Git-backed.

Issuance still requires the owning assertion subject and ServiceAccount UID.
An administrator, another service, or a previous access token cannot mint a
token for this identity without its private key.

## Configuration

Retain existing controller config names for service-account identity,
audiences, `serviceaccount_key_ref` and `secret_provider_bootstrap`. Add the
provider-format selection documented in the resolver contract; do not introduce
a raw private-key path alternative or inline private-key option.

For overlap rotation, the stable keyRef identifies a whole JSON signing record:

```text
serviceaccount_key_ref.kind = SecretRef
serviceaccount_key_ref.name = controller-signing-key
serviceaccount_key_ref.key = omitted
secret_provider_bootstrap.type = file
secret_provider_bootstrap.format = json-record
```

Actual TOML nesting/environment aliases must follow existing config binding
patterns. These lines describe logical fields, not an unverified ready-to-paste
config block. Existing raw/keyed config continues to use configured
`serviceaccount_key_id`; record format uses the record's `keyID`. Reject
ambiguous simultaneous configured and record IDs unless they agree; hot-rotation
deployments must remove the static ID before switching records.
Configuration validation requires the static ID only for raw/keyed mode; in
record mode startup validates the required ID from the resolved bundle before
any signing or authenticated work.

Record content is provider-owned as defined in [resolver.md](resolver.md).
`privateKey` must contain a supported single PKCS#8 PEM key; `keyID` must match
the enrolled corresponding public key. A key ID is not derived implicitly from
the private key: spec 061 permits explicit enrollment IDs.

## Startup, renewal and failure

1. Validate exact reference grammar, provider binding and ownership constraints.
2. Resolve and parse before any authenticated controller work. Missing,
   forbidden, oversized, malformed or unavailable material causes a classified
   fatal startup failure; there is no static-token/anonymous fallback.
3. Sign the existing bounded-lifetime assertion, exchange over the existing
   authenticated protocol and cache only the returned access token/expiry.
4. On each renewal, single-flight resolution obtains a fresh complete signing
   record and creates an exchange-local signer. Do not retain the signer or
   raw/parsed key between exchange attempts.
5. A failed attempt updates one shared retry deadline. Subsequent attempts
   re-resolve after bounded backoff; never let waiters cause a retry storm.
   Waits observe caller cancellation. No new per-reference goroutines.
6. The 10s exchange budget includes resolution (at most 2s) and signing.
   Exponential retry starts at 1s and clamps the final jittered delay to 30s.
   All retry work remains caller-driven through the existing credential source.

A currently usable access token may continue to serve requests while renewal
is not yet required; this is not anonymous fallback. Once no usable token
remains, new authenticated calls fail closed and credential readiness is false.
No expiry extension or stale private-key fallback is allowed. Provider
restoration with valid enrolled material restores authenticated progress within
60s under the declared capacity workload.

## Atomic rotation sequence

1. Generate/provision a new private/public key pair outside GitStore.
2. Enroll the new public key under a distinct `keyID`, retaining the old one.
3. Atomically replace the same logical provider record with the new
   `privateKey`/`keyID` pair. Neither Git manifests nor keyRef change.
4. Observe successful renewals on every controller replica, including one
   process replacement, using the new enrolled key.
5. Retire the old public key only after all replicas transitioned and the
   configured assertion lifetime plus permitted clock-skew window elapsed.
   Account for any still-valid access-token lifetime separately; deleting a
   public assertion key is not equivalent to revoking issued access tokens.

Provider record update and public enrollment are not one distributed
transaction; ordering and overlap are mandatory. Test missing new enrollment,
torn/invalid record, premature old-key removal, cancelled exchange, and rollback
to an old pair while it remains enrolled.

## Source isolation and safe output

Shared `config/config.toml` may hold logical refs/provider categories, not
private keys. Identity-bearing mounts/variables must be restricted to their
owning service. Extend managed config/Compose validation to reject a private-key
source mounted into API/Git-service and controller together. Replicas of the
same owning service may receive authorized material; unrelated service
identities may not. Use separate controller records/subjects when deployment
policy requires per-replica rather than per-service identity.

Startup can validate its own binding and permissions but cannot inspect every
other process's mount/IAM policy. Document that distinction instead of claiming
runtime proof of global isolation.

Access-token delivery is the sole intentional GraphQL secret-bearing output
here, and only for the authorized requesting process. Never log/persist the
token response as resource status. Do not echo assertions, return provider
records or include material in metrics, audit, traces, config dumps or evidence.
Public enrollment keys are verifier material and are not secret bytes.

API access-token signing configuration, JWT/HMAC config and human password
verifiers keep ADR-0009's explicit deferrals; this feature must not silently
migrate or broaden them.
