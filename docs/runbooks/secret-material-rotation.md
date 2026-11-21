# Controller signing-material rotation

The controller resolves its bootstrap key at startup and again on each token
exchange. No parsed private signer is retained between exchanges. The existing
access-token cache, 30-second refresh margin and single-flight exchange remain;
acquisition has a two-second budget inside the total ten-second exchange budget.
Retries are caller-driven and capped at 30 seconds including jitter. A canceled
caller does not impose a retry delay on other callers.

## Formats and canonical configuration

Controllers require an atomic JSON signing record containing both the private
key and its enrolled ID. Raw-key configuration, `bootstrap.format`,
`serviceaccount.key_id` and `key_ref.key` are no longer accepted.
Use a whole-record reference:

```toml
[controller.serviceaccount]
name = "gitstore-controller-manager"
uid = "<enrolled-service-account-uid>"
assertion_audience = "gitstore-api/serviceaccount-token"
access_token_audience = "gitstore-api"
key_ref = { kind = "SecretRef", name = "controller-signing-key" }

[controller.secret_providers.bootstrap]
type = "file"
base_path = "/run/secrets"
```

The record is `<base_path>/controller-signing-key.json`:

```json
{
  "format": "serviceaccount-signing-key/v1",
  "values": {
    "privateKey": "<base64 of one PKCS#8 Ed25519 or ECDSA P-256 PEM key>",
    "keyID": "<base64 of the corresponding enrolled key ID>"
  }
}
```

Provision this record outside GitStore; never put real values in Git, shared
config, command-line arguments, screenshots or evidence bundles. The record is
provider-owned, not a Git-backed resource. Whole-record environment mode uses
`type = "env"` and an explicit `env_variable` naming the variable holding the JSON.
Environment changes require process replacement.

For separately started native services, prepare credentials before launching
the controller. With a local API already listening, use this setup sequence:

```bash
make secret TARGET=signing-key
make enroll-controller-serviceaccount
make controller
```

Generation creates or preserves the matching controller PEM and JSON record;
it needs no API connection. Enrollment uses the existing PEM, registers its
public key and saves the API-assigned ServiceAccount UID. It does not generate
private material or JSON. ServiceAccounts are cluster-scoped; do not configure
a namespace.

`make controller` is launch-only: it consumes the existing JSON record and
enrolled UID (or an explicitly supplied `CONTROLLER_SERVICEACCOUNT_UID`) and
fails if either is missing. Keep `CONTROLLER_*` overrides consistent across
the sequence. `DESTINATION_PATH` and `RECORD_OUTPUT_PATH` affect generation
only; they do not reconfigure enrollment or startup.

`make dev` instead prepares the local files, starts Git and the API, waits for
API health, and enrolls the controller before launching it. An explicit
`CONTROLLER_SERVICEACCOUNT_UID` skips enrollment. These local workflows use
development identity/policy fixtures, not a deployed rotation procedure.
Repeated generation preserves matching material; a differing existing record
is refused rather than silently rotated. For rotation, stage new material and
follow the order below; do not generate over the active record.

## Safe rotation order

1. Generate a new PEM and matching JSON record with `make secret
   TARGET=signing-key`, selecting fresh `DESTINATION_PATH` and
   `RECORD_OUTPUT_PATH` values and a **distinct**
   `CONTROLLER_SERVICEACCOUNT_KEY_ID`. Stage the new record in the same provider
   directory as the active record. Enroll the new PEM's public key with that
   same ID using `gitctl enroll-serviceaccount --replace-existing-key`, pointing
   `--private-key-path` at the new PEM and `--key-id` at the distinct ID. This
   updates the existing ServiceAccount without changing its UID; keep the old
   public key enrolled during overlap.
   The ID is the enrolled identifier, not an inferred hash of private material.
2. Verify the complete new record is mode `0600`, then atomically rename it
   over the old record only after enrollment succeeds.
   Do not rewrite it in place or replace the pinned root directory. The
   provisioner writes; controllers retain read-only mounts.
3. Observe successful renewal on every replica. A cached access token may
   remain in use until renewal is due. Exercise a replacement process too.
4. Retire the old public key only after every replica uses the new record and
   the assertion-lifetime/clock-skew overlap window has elapsed.

Never silently fall back to an old key after resolution fails. Missing,
malformed, oversized, unsupported or incomplete records fail closed. A wrong
key/ID pair fails the normal API assertion verification. Do not restore
readiness by injecting a static token.

## Failure diagnosis and verification

If the provider record becomes unavailable, a previously issued token remains
usable only until its expiry. The affected controller then becomes unready.
Restore the complete valid record and confirm fresh issuance and reconciliation
progress on every affected replica. Do not inject faults into real credentials
as a diagnostic step.

For renewal, require increases in
`gitstore_controller_credential_exchange_total{result="success"}` on each
controller, together with authenticated reconciliation progress. A successful
`gitstore_secret_resolution_total` read alone does not prove key parsing or
token issuance. Monitor `gitstore_controller_credential_exchange_peak_inflight`
(one with the current single-source process) and
`gitstore_controller_credential_retry_max_seconds` (at most 30). These gauges
reset on process replacement; correlate them with the process-instance identity,
not just the endpoint. A canceled waiter is not an exchange attempt, and
caller cancellation does not schedule shared retry backoff.

A replacement must have its own process identity and positive exchange and
reconciliation counters; do not subtract the old process's counters across a
reset. Require explicit `credentialReady:true` plus resumed authenticated work,
not HTTP 200 alone or a successful secret read. Measure outage and recovery
times separately from before/after snapshots.

Preserve deployment/group-specific checkpoint roots during replacement.
Changing config names must not change effective checkpoint paths, related replay
keys or final-flush behavior. Existing conditional/idempotent writes protect
replay; atomic checkpoint files and datastore materializer leases are not
controller-write fencing. ServiceAccount ownership remains audit-only.

Contributor test setup and capacity evidence requirements are maintained in
[shared secret material](../development/secret-material.md),
[integration test instructions](../../tests/AGENTS.md) and the
[capacity guide](../../tests/capacity/README.md). Functional rotation checks do
not establish sustained-load, dataset-scale or production fault acceptance.
