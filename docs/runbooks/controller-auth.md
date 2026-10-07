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
3. Ensure the RBAC policy binds
   `serviceaccount:<namespace>:<name>` to only the controller role. Enrollment
   does not grant authorization by itself.
4. Configure `controller.serviceaccount_namespace`, `_name`, `_key_id`, and
   `_uid`, plus a complete `controller.serviceaccount_key_ref`. For a file
   resolver, `SecretRef{name: "controller-manager", key: "privateKey"}`
   resolves `/run/secrets/controller-manager/privateKey`.
5. Mount `/run/secrets/controller-manager` read-only into the
   controller-manager alone. Configure the API issuer key independently with
   `GITSTORE_AUTH__SERVICEACCOUNT__SIGNING_KEY`; no other service may receive
   that mount.
6. Roll out the controller and check `GET /health`. A working credential source
   returns HTTP 200 with `"credentialReady":true`. The response intentionally
   contains no credential value or exchange error.

The controller requires `controller.serviceaccount_key_ref`; static controller
API tokens are not supported.

## Renewal, readiness, and backoff

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
3. Compare only non-secret identity metadata: namespace, name, UID, `kid`, and
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

Deletion invalidates the old UID. Recreating the same namespace/name does not
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
