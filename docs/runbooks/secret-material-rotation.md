# Controller signing-material rotation

The controller resolves its bootstrap key at startup and again on each token
exchange. No parsed private signer is retained between exchanges. The existing
access-token cache, 30-second refresh margin and single-flight exchange remain;
acquisition has a two-second budget inside the total ten-second exchange budget.
Retries are caller-driven and capped at 30 seconds including jitter. A canceled
caller does not impose a retry delay on other callers.

## Formats and canonical configuration

Raw mode preserves an existing controller-only key at
`<base_path>/<key_ref.name>/<key_ref.key>`. Set
`controller.secret_providers.bootstrap.format = "raw"` and supply the enrolled
`controller.serviceaccount.key_id`. The raw environment mapping also remains
available under canonical configuration keys; it is frozen to one configured
reference, not available to resource-runtime callers.

For overlapping-key hot rotation, use a whole-record reference:

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
format = "json-record"
base_path = "/run/secrets"
```

Omit `key_ref.key`. Omit static `key_id` for hot rotation; if supplied, it must
equal the record's ID and prevents changing that ID without updating config.
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
the configured prefix plus the escaped name, without the raw keyed suffix:
`GITSTORE_SECRET__CONTROLLER_DASH_SIGNING_DASH_KEY` for this example.
Environment changes require process replacement.

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

The in-process regression exercises two independent credential sources and
HTTP token verifiers, atomic replacement, cached-token reuse, mismatched pairs,
expiry-spanning provider failure, peer progress, restoration and a new source.
It is **not** evidence of deployed two-API/two-controller recovery or the
production capacity gate; the separate deployed evidence below covers the former.

The deployed acceptance harness is available through:

```bash
make test-secret-integration \
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

Sanitized logs, artifact identities and an explicitly non-production summary are
under `.gitstore/capacity/secret-functional/20261003/`. This is a local functional
record from a dirty checkout, not clean release provenance or a capacity pass.

Preserve deployment/group-specific checkpoint roots during replacement.
Changing config names must not change effective checkpoint paths, related replay
keys or final-flush behavior. Existing conditional/idempotent writes protect
replay; atomic checkpoint files and datastore materializer leases are not
controller-write fencing. ServiceAccount ownership remains audit-only.
