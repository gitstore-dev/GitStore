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
namespace = "controllers"
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

Native `make controller` and `make dev` preserve the enrolled PEM key and use
`gitctl generate-signing-key --private-key-path <absolute-pem-path>
--record-output-path <absolute-record-path> --key-id <enrolled-id>` to publish
its private record atomically. Repeated generation preserves matching material;
a differing existing record is refused rather than silently rotated.

## Safe rotation order

1. Generate the new key in an operator-controlled private location and enroll
   its public key with a **distinct** ID using the existing `gitctl
   enroll-serviceaccount` flow. Keep the old public key enrolled during overlap.
   The ID is the enrolled identifier, not an inferred hash of private material.
2. Write the complete new key/ID record to a mode-0600 temporary file in the
   same provider directory, then atomically rename it over the old record.
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

## Failure and evidence

For a test-owned outage, withdraw only one replica's fixture, let its token
expire, and confirm its readiness becomes false while the peer still works.
Restore the complete valid record and confirm fresh issuance on both sources.
Do not inject faults into an operator's real credentials.

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

The guarded capacity observer now brackets each health read with two
process-identified metrics scrapes. It rejects a process change or counter reset
during the observation, requires explicit credential readiness, and treats
HTTP 503 with exhausted credentials as an observed failure state. Original
processes must increase both fresh-token and Repository-success counters;
a replacement must have a new identity and its own positive counters, not
subtract the old process's totals. Before/after-load snapshots are not recovery
timers. The guarded owned-record scheduler now measures the outage, renewal and
restart windows separately. Process resource accounting and final evidence
assembly are now connected, with original and replacement logs retained and
scanned after all writers close. Running this scenario requires explicitly owned
fixtures and `CHAOS_CONFIRM=1`; no full production-capacity acceptance is claimed.

For local macOS Docker Desktop runs, Docker may report provider directory binds
with `/host_mnt` while reporting config file binds without it. The verifier
normalizes these only for a verified local Desktop Unix-socket endpoint, resolves
host bind paths and still enforces read-only, replica-owned provider/config
mounts. Do not broaden mounts or disable ownership checks to work around path
differences; see the [capacity guide](../../tests/capacity/README.md).

File bootstrap in the capacity harness reads the known File names from persisted
`file(namespace:, name:)` queries on both APIs before subscribing, because an
empty watch cursor does not replay existing state. File subscriptions use the
shared durable journal. Functional CDC coverage does not satisfy full
production-readiness acceptance; that requires a complete production evidence
bundle.

The in-process regression exercises two independent credential sources and
HTTP token verifiers, atomic replacement, cached-token reuse, mismatched pairs,
expiry-spanning provider failure, peer progress, restoration and a new source.
It is **not** evidence of deployed two-API/two-controller recovery or the
production capacity gate; the separate deployed evidence below covers the former.

The deployed acceptance harness is available through:

```bash
make test TARGET=secret-integration \
  SECRET_TEST_OWNED_DEPLOYMENT=1 \
  SECRET_TEST_API_A=http://127.0.0.1:14000 \
  SECRET_TEST_API_B=http://127.0.0.1:14001 \
  SECRET_TEST_TOKEN_FILE=/private/test-admin-token
```

Use an isolated two-API deployment sharing Scylla with one Git service. Set both
APIs' `auth.serviceaccount.default_ttl` and `max_ttl` to `60s`. The existing
`controllers/gitstore-controller-manager` ServiceAccount must have controller
RBAC permissions; `SECRET_TEST_NAMESPACE` and `SECRET_TEST_SERVICEACCOUNT`
override that test identity. The command builds the current controller binary,
starts two test-owned processes with separate private records/checkpoint roots,
enrolls only uniquely named test keys, withdraws one record for 90 seconds,
tests overlap and retirement across token expiry, and replaces a process.
Cleanup stops those processes and removes only their enrolled keys, leaving
the pre-existing key set intact. Never use an operator deployment.

This is functional deployed acceptance, not a production capacity run. It does
not establish sustained File-push latency, the five-million-Product dataset or
the production fault schedule.

**Observed 2026-10-03:** the expanded harness passed in 293.56s with two real API
processes sharing three Scylla nodes, two test-owned controller processes and
one Git service. Both APIs clamped tokens to 60s. The 90s record outage
exhausted controller A's credential while B remained authenticated; A recovered
in **6.103361708s** after restoration. A subsequent mismatched key/ID record
also failed closed after expiry. Both controllers remained authenticated across
new-key overlap and old-key retirement, and a replacement process bootstrapped
without changing the reference/config snapshot. Controller logs and all four
API/controller metrics were scanned for PEM/JWT material and credential markers;
the isolated API logs contained no PEM/JWT matches. Test-owned services, volumes,
records and token files were removed.

This deployed functional test is not release provenance or a capacity pass.

Preserve deployment/group-specific checkpoint roots during replacement.
Changing config names must not change effective checkpoint paths, related replay
keys or final-flush behavior. Existing conditional/idempotent writes protect
replay; atomic checkpoint files and datastore materializer leases are not
controller-write fencing. ServiceAccount ownership remains audit-only.
