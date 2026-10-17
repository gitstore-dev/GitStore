# Runbook: Controller Authentication

Use this runbook when `gitstore-controller-manager` cannot obtain or renew its
service-account credential, after an identity/key change, or when revoking a
controller credential. Controller authentication uses only the service-account
flow.

## Safety rules

- Never copy an access token, signed assertion, or private key into a ticket,
  command history, config file, or log.
- The API issuer signing key and the controller enrollment private key are
  different secrets. Mount each read-only into its owning service only.
- Treat a suspected controller private-key disclosure as a key compromise:
  remove its `kid` or disable the ServiceAccount before investigating.

## Enroll a controller identity

Enrollment is an installation-time administrative operation. It registers only
public material; controller startup and renewal must not require an
administrator-issued bearer token.

1. Generate an Ed25519 (preferred) or ECDSA P-256 key pair using the
   deployment's approved key-management tooling. Store the private PKCS#8 PEM
   in a controller-only secret. Do not export it to the shared
   `/config/gitstore.toml` mount.
2. With an authorized administrator session, create the ServiceAccount or add
   its public key through `gitctl enroll-serviceaccount`, `createServiceAccount`,
   or `rotateServiceAccountKey`. Give the key a unique `kid`; record the
   returned ServiceAccount UID.
3. In a v1beta1 policy, bind `{kind: ServiceAccount, name: <name>}` to the
   controller role and a separate `serviceAccount/token` issue role restricted
   by `resourceNames: [<name>]`. The authenticated subject is
   `system:serviceaccount:<name>`. Enrollment does not grant authorization.
4. Configure `controller.serviceaccount.name` and `.uid`, plus
   a complete `controller.serviceaccount.key_ref` (`kind = "SecretRef"`,
   `name = "<logical-secret-name>"`; `key` must be omitted — the resolver
   reads one atomic JSON signing record, not an individual field). For the
   `file` bootstrap resolver, a `key_ref.name` of `controller-manager`
   resolves `<base_path>/controller-manager.json` (default base path
   `/run/secrets`).
5. Mount the resolved signing-record path read-only into the
   controller-manager alone. Configure the API issuer key independently with
   `GITSTORE_API__AUTH__SERVICEACCOUNT__SIGNING_KEY`; no other service may receive
   that mount.
6. Roll out the controller and check `GET /health`. A working credential source
   returns HTTP 200 with `"credentialReady":true`. The response intentionally
   contains no credential value or exchange error.

The controller requires `controller.serviceaccount.key_ref`; static controller
API tokens are not supported.

ServiceAccounts are cluster-scoped. `--namespace`,
`controller.serviceaccount.namespace`, its environment equivalent and the
`CONTROLLER_SERVICEACCOUNT_NAMESPACE` Make variable are errors, even when empty.
Old v1 policies and namespaced token subjects are rejected; there is no
translation or dual-format period. The shipped Compose enrollment job uses
name-only enrollment and keeps the existing isolated key/UID mounts.

For deployed credential validation, use `make test TARGET=secret-integration`
with `SECRET_TEST_OWNED_DEPLOYMENT=1`, two distinct `SECRET_TEST_API_A` and
`SECRET_TEST_API_B` endpoints, and a private `SECRET_TEST_TOKEN_FILE`.
Both test APIs must clamp ServiceAccount tokens to 60 seconds. The probe starts
its own controllers and enrolls and retires only its generated keys on the
selected `SECRET_TEST_SERVICEACCOUNT` (default `gitstore-controller-manager`).
The selected identity needs controller permissions. The suite also races
cluster-scoped account creation across the two APIs, checks shared assertion
replay rejection and peer-issued token authentication, and recreates a unique
test account to verify that fresh assertions using its old UID are rejected.
The unique account must remain unbound: a valid assertion must reach an
authorization denial, distinct from an invalid or replayed assertion's
authentication denial. Only that test-created account is deleted; the enrolled
controller identity is retained. These are functional checks, not sustained-load,
rolling-upgrade or recovery-time acceptance, and they do not test old access
tokens across account recreation unless the additional fixture below is supplied.
`SECRET_TEST_NAMESPACE` is rejected before building or starting a controller,
even when explicitly empty. Never run this probe against an operator deployment.

To check still-unexpired access tokens across recreation, also supply
`SECRET_TEST_REINCARNATION_API` and
`SECRET_TEST_REINCARNATION_SERVICEACCOUNT` together. The name must initially
be absent and remain unbound on both target APIs. Use a separate, disposable
issuer API sharing their datastore and access-token signing material, with
test-only permission to issue tokens for that name. Do not alter the target
APIs' policy or delete an enrolled controller identity. The probe creates the
account itself and checks that its token authenticates on both targets but
receives their expected authorization denial. Deletion and same-name,
same-key recreation must instead produce authentication denial for the old
token while a new-UID token authenticates. It verifies the old token has not
expired and neither target process changed; only its created account is removed.
Without this additional fixture, the suite explicitly reports that the
access-token reincarnation case was not exercised.

## Renewal, readiness, and backoff

### Policy reload diagnostics

Validate the replacement with `make check TARGET=config CONFIG_FILE=<path>`
before installing it. Send SIGHUP to each intended API process to rebuild and
atomically swap its complete authentication/authorization provider set. A local
signal does not update another replica, and editing a mounted file alone does
not prove the running policy changed.

The API's `auth providers reloaded atomically` message confirms publication.
`rbac-local policy reloaded` carries the candidate `policy_digest`; correlate it
with the registry success message because construction precedes publication.
The API's `/ready` response also exposes an `auth` object containing the active
provider names, a fingerprint of the compiled action catalog, and optional policy
snapshot metadata. For `rbac-local`, `policy.loaded` must be true; `policy.digest`
and the role, binding and rule counts come from one immutable active snapshot.
An unsupported provider omits policy metadata rather than inventing a fingerprint.
Compare these fields on each replica before and after reload or replacement.
They contain no policy rules, subjects, credentials or private material, and
collecting them performs no policy-file, datastore or network reads.
On `auth provider reload failed; previous configuration remains active`, fix the
candidate and retry: do not restart merely to discard the valid old snapshot.
Malformed/v1 policy, legacy settings and ineffective local-user grants fail
validation. Decision logs include provider, request ID, action, canonical kind,
outcome and reason; rbac-local reasons identify the policy digest and matching
binding/role/rule or bounded denial category. Compare exact allow/deny probes on
each replica after success, not just HTTP status or a matching file on disk.

For owned-deployment reload probes, give each replica a separate read-only
policy **directory** mount and point `api.auth.rbac_local.policy_file` at its
policy file. A shared source changes both replicas' candidates; a single-file
bind mount can retain the old inode after atomic file replacement. Verify the
mounted file's hash before signalling, then verify the active snapshot and
exact named-resource allow/deny decisions on both replicas. Rejection probes
should cover malformed input, legacy policy versions and loss of effective
local-user grants. Restore both the original file and active snapshot after
the probe; restoring a file alone does not undo a published policy.

For reproducible malformed-policy probes on a disposable owned API, use
`make chaos CHAOS_PROFILE=api-invalid-policy-reload` with the explicit target,
owned Compose project, confirmation and `CHAOS_POLICY_DIR`. The dedicated
directory must have an exact-container `owned-policy.json` marker and a
read-only directory bind; see
[`tests/chaos/README.md`](../../tests/chaos/README.md#invalid-policy-reload).
Integrated k6 runs accept `CAPACITY_CHAOS_POLICY_DIR` with that profile. The
runner requires native registry rejection, unchanged active metadata, original
bytes and active snapshot restored, and retained process/settings within the
unchanged recovery budget. This narrow malformed-policy probe does not replace
the other rejection cases, peer semantic checks or maximum-policy reload soak.

Do not copy tokens, assertions, private material or arbitrary resource attributes
into diagnostics. Sustained-load/replacement acceptance is tracked in
[#451](https://github.com/gitstore-dev/GitStore/issues/451), not established by
these operator checks.

### Token lifecycle

The controller signs a fresh, 45-second proof-of-possession assertion and
exchanges it for a short-lived access token. It reuses an access token only
until 30 seconds before expiry, then renews automatically. Concurrent requests
share one exchange attempt. A failed exchange uses jittered exponential backoff
capped at 30 seconds.

1. Alert on `GET /health` returning HTTP 503 with
   `"credentialReady":false`; this means no usable credential is available.
   Do not derive a token or private-key value from application logs.
2. Confirm the API is reachable at `controller.api_uri`, then verify the API
   authentication chain includes both `serviceaccount-assertion` and
   `serviceaccount-jwt`.
3. Compare only non-secret identity metadata: name, UID, `kid`, and
   both configured audiences. Confirm the `kid` remains enrolled and the
   ServiceAccount is enabled.
4. For a file resolver, verify the mounted directory and logical
   `SecretRef` components, ownership, and read permission from the controller
   container. Verify the file is exactly one supported PKCS#8 PEM key without
   printing it. For an environment resolver, verify the expected variable is
   present without displaying its value.
5. If credential readiness is true but a watch still reconnects, diagnose the
   watch/replay layer with
   [controller-watch-status](controller-watch-status.md) and
   [controller-replay-window-exceeded](controller-replay-window-exceeded.md).
   Credential backoff and cursor-expiry recovery are separate failure modes.

## Controller public-key rotation and re-enrollment

Use overlapping enrolled keys to rotate a controller key without interrupting
work:

1. Generate and securely mount the replacement private key. Add its public
   key with a new `kid`, retaining the old `kid`.
2. Roll controller replicas one at a time with the replacement key, `kid`, and
   unchanged ServiceAccount UID. Verify each replica reports
   `"credentialReady":true` and resumes watches from its checkpoint.
3. Wait for the old access-token maximum lifetime and for all replicas to
   renew. Remove the old `kid` only after no replica uses it.
4. If the old private key is compromised, disable the account or remove that
   `kid` immediately. Expect affected controllers to become unready until they
   are rolled to another enrolled key.

API issuer signing-key rotation also requires an overlap: deploy the new
signing `kid` while every API replica continues to verify the old key, let
controllers renew and reconnect with newly signed tokens, then retire the old
verifier only after the maximum old-token lifetime. Do not roll a single-key
issuer configuration that invalidates still-live tokens; stage verification
overlap first and validate every replica before promotion.

## Replay, revocation, and multiple replicas

- Assertion `jti` values are consumed once in the shared datastore. All API
  replicas must use the same production datastore so a replay accepted by one
  replica is rejected by every other replica. Do not use independent in-memory
  stores for a multi-replica deployment.
- Disabling or deleting a ServiceAccount prevents new exchanges and API
  authentication. Each API replica owns its in-memory WebSocket registry, so
  revocation rollout must ensure every replica receives the account-change
  invalidation and closes its local sockets. Verify this on every replica;
  expiry remains the hard fallback boundary.
- A token-expiry or revoked-socket reconnect resumes from the persisted
  `resourceVersion`. If the replay window has expired, the controller must
  re-list and rebuild its cache; do not manually advance a checkpoint.
- During rolling changes, keep identity/key state and replay storage compatible
  across the entire API fleet. A replica that cannot enforce the current
  service-account contract must not receive controller watch traffic.

## Recover an accidentally deleted ServiceAccount

Deletion invalidates the old UID. Recreating the same name does not
restore the old identity.

1. Keep the controller stopped or remove it from service while it is not ready.
2. Create a new ServiceAccount, enroll a new public key, and record its new
   UID. Do not attempt to reuse the deleted UID, old assertion, or old access
   token.
3. Update the controller's key reference, `kid`, and UID together; roll
   replicas one at a time and confirm readiness plus watch resume.
4. Remove the old private-key secret according to the deployment's incident
   process, then audit access and policy changes using non-secret identity
   metadata only.

If recovery repeatedly returns to backoff, stop retrying manual token copies
and repeat the readiness diagnosis above. Escalate suspected key disclosure or
unexpected cross-replica assertion acceptance as a security incident.
