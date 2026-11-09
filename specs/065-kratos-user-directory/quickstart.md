# Quickstart: Kratos User Directory

This quickstart describes supported local reference-stack validation. It is
not a production deployment guide.

## Start the reference identity stack

1. Start the existing OIDC/Kratos Compose profile:

   ```sh
   make compose IDENTITY=oidc
   ```

2. Configure the API's UserDir provider as `kratos`, with the internal Kratos
   Admin URL, bounded request timeout/concurrency, cache bounds and injected
   API gateway credentials only if that endpoint authenticates them. Keep
   UserDir configuration independent from the AuthN provider chain. Enable
   `api.auth.kratos.webhook_uri` on a private/container address and opt in to
   `kratos.yml,gitstore-hooks.yml` through `KRATOS_CONFIG_FILES` using the
   reference environment example. Do not publish the receiver port; standalone
   OIDC must not configure callbacks without the enabled provider/listener.
   The hook overlay also selects HTTP courier delivery: enable the development
   notification provider and privately inject its shared 32+ character mail key.
3. Confirm readiness for both API and identity services before creating test
   users. Use test-only credentials and the local mail catcher; never put
   secrets or captured messages in versioned files.

## Verify customer and staff login

1. Register a customer through Kratos self-service. Confirm registration
   selects only the customer schema.
2. Sign in with email/password, then sign in with username/password; both
   credentials must resolve to the same Kratos identity and principal.
3. Attempt to reuse either identifier in another human schema. Kratos must
   reject the duplicate without exposing the existing account.
4. Create Staff through the action/input/payload Admin mutation. Verify
   lifecycle.state INVITED, resend the invitation, complete required
   verification/setup and verify ACTIVE plus both login forms.
5. Add a staff group and verify the corresponding request receives that
   directory-derived group. Confirm a customer cannot access Admin management
   operations and cannot acquire a staff group.
6. Change the customer email and confirm the old verified address remains
   deliverable until the new address is verified.
7. Suspend and deactivate humans; verify SUSPENDED and INACTIVE remain
   distinct, valid tokens fail admission, and reactivation never bypasses
   unfinished invitation/verification.
8. Resolve Staff, Customer and ServiceAccount using actor(by:) and node(id:).
   Validate forward Relay edges/cursors/PageInfo and the unified diagnostic
   for backward requests against the selected pin. Validate initiating owner
   and best-effort Staff revisions/unsupported Customer -1/"-1" counters.
   Verify public immutable usernames, protected metadata and blocked persistence
   when the receiver fails. Direct Admin writes bypass these guards and can
   overwrite metadata or lose increments; neither PATCH nor the observed-version
   check is CAS. Do not add a public filter/search
   argument or capability advertisement before their respective features.

## Verify ServiceAccount and mail behavior

1. Create a ServiceAccount through the existing Admin operation. Confirm a
   credentialless `serviceAccount` schema identity is synchronized by the
   configured UserDir's event subscriber, uses an immutable UID-derived
   external_id and cannot
   complete login, recovery or verification.
2. Disable and delete it with the provider available, then verify successful
   event delivery updates the Kratos mirror. With delivery failing/discarded,
   verify GitStore-authoritative UID/enablement still prevent access. Native
   source operations must work without Kratos being configured.
3. Trigger a verification or recovery message and confirm Kratos delivers it
   through the authenticated GitStore adapter to the local mail catcher.
4. Stop the identity service in an owned test deployment. Confirm authenticated
   requests fail closed; restore it and verify authentication recovery.
5. Verify ServiceAccount changes use the shared controller-facing resource
   journal backend through direct internal reads/subscribers, with cluster
   scope, bounded buffers and MemDB parity. No GraphQL/WebSocket
   loop or filesystem checkpoint directory is needed.
6. Fail event application through the finite retry budget. Verify redacted
   retry/drop outcomes, discard after exhaustion and processing of subsequent
   events. Exercise overflow and duplicate/stale events across replicas;
   a reused name must not inherit an old UID's groups.
7. Crash during application and restart. Verify live consumption resumes
   without a durable provider checkpoint or missed-history replay. Missing/
   stale mirrors are an accepted best-effort outcome, not an automatic repair
   target. No DLQ, sync/backfill CLI or ServiceAccount controller is required.

## Automated validation

The focused HTTP/Git/GraphQL orchestration, independent static directory,
trusted-username policy and WebSocket admission/cache-lease tests pass with
race detection and Go vet. These are in-process/provider tests, not deployed
two-API policy-equivalence, mixed-version replacement or sustained-load proof.
Local generation-safe cache/connection invalidation is verified on the actual
WebSocket transport; freshly resolved admissions remain allowed.

The exact owned Kratos fixture proves static/OIDC Staff policy-equivalent
group/action outcomes with two independent provider caches, allowing the bound
namespace and denying another. It does not merge identities by login email or
coincidentally equal subjects. Native forward pages resume at interior edges;
empty terminal pages and no-follow Link semantics pass focused tests.

The authenticated development mail route passes malformed/oversized/auth/
saturation/redaction cases and respects the smaller Admin body bound. The
owned exact-pin HTTP courier delivers native verification mail to the selected
SMTP catcher, accepts duplicates and returns retryable failure during catcher
outage. The selected catcher uses implicit SMTPS and needs explicit
development-only self-signed certificate handling. Native request Jsonnet takes
a top-level `function(ctx)`; nested native API-key environment binding is proved.
The same owned fixture now proves native credentialless Staff creation/resend,
private mail receipt, actual recovery/password setup, session revocation and
reactivation without resurrecting sessions. Fresh subject-keyed contact tests
cover address replacement, old-address bounce, verification switch, internally
timestamped consent and deletion. Initial deliverability remains false without
a trusted observation. Creation/reservation/delivery and state/revocation are
separate native operations and can partially apply; no remote CAS is claimed.

Customer public-flow tests reject non-Customer schema selection, preserve
protected metadata and immutable usernames, and return conflict errors without
another actor's UID/schema. Native browser sessions obtained by username or
email pass through the bridge's real Kratos client and accept the same immutable
Hydra subject. The additional native Hydra fixture exercises the actual HTTP
login/consent handlers, authorization-code redirects, S256 PKCE exchange and
native token introspection for Staff and Customers using both login forms.
It shares the Kratos hostname with the bridge, so the browser jar sends real
session cookies without copying them across hosts. Both API replicas verify
Hydra-issued access tokens through discovery/JWKS and enrich their UID subjects.
Customers remain denied Admin admission; adding a Staff directory group
authorizes already-issued native credentials on either API. Credentials and
captured tokens stay in private temporary files. The bridge forwards sessions,
not password identifiers, so no login-handler change is required.
Focused admission tests deny Customers even with forged privileged groups and
an allow-all AuthZ provider; Staff and source-backed workloads retain the
existing boundary. Storefront remains unavailable.

The owned deployed fixture additionally runs two current API binaries, two
native Kratos processes sharing PostgreSQL, a cryptographic OIDC discovery/JWKS
issuer, the development catcher and exactly one current Git service. It proves
authorized/denied administration, all human lifecycle states, real invitation
setup, safe contact changes, Customer mandatory groups, concurrent best-effort
writes, stable-endpoint cross-process Relay continuation, redacted mutation
receipts, WebSocket expiry, API process restart and physical directory
pause/restoration within the existing-profile 30-second bound. The issuer is
controlled RS256 OIDC; the separate native Hydra fixture supplies the real
token-exchange and two-API credential-admission proof.

The shared-Scylla variant reuses the canonical Compose source services with an
owned private override. Both deployed APIs consume native CDC events and
administer UID-linked credentialless mirrors. Real enrolled assertions issue
60-second access tokens; protected workload groups authorize reads. Both
consumers emit exhaustion receipts during a physical directory pause. A
physically killed/restarted API begins at the current journal boundary: the
discarded disabled-source update leaves an active stale mirror, but source
authority rejects its still-valid credential. Later source events deliver,
deletion removes the mirror, and name reuse transfers neither UID nor groups.
Fresh-journal startup and injected redacted consumer logging have regression
coverage.

Run the deployed fixtures explicitly from `tests/integration`:

```sh
USER_DIRECTORY_TEST_OWNED_DEPLOYMENT=1 go test -race . \
  -run '^TestOwned(UserDirectoryAdministrationAcrossAPIs|ScyllaWorkloadDirectoryAcrossAPIs|NativeHydraDirectoryAcrossAPIs)$' \
  -count=1 -timeout=10m
```

They build current Linux binaries/images, create only labelled owned
containers/network/storage, verify labels before faults/cleanup, and remove
their private fixtures. These are bounded functional/fault proofs, not
mixed-version/provider-switch acceptance or sustained identity-scale capacity.

The separate `TestOwnedUserDirectoryRollingUpgrade` fixture builds the exact
pre-directory source and keeps an old API serving source-backed requests
through forward migration and replacement. It uses version-compatible policy
configuration, tests new human operations only on new binaries, and reconstructs
static/Kratos/disabled providers through real restarts at stable endpoints.
Source authority and live-consumer cancellation survive the switch; an old
binary refuses startup against the forward migration ledger. This is bounded
rolling acceptance, not sustained chaos or a supported binary downgrade.

`make lint` and `make check TARGET=all` now pass. The reported Go test files
were formatted without behavior changes, and the ignored local API environment
now uses `GITSTORE_API__ADMIN_PORT` and `GITSTORE_API__STOREFRONT_PORT` instead
of the removed `GITSTORE_API__PORT`. No credentials were changed.

Run the API unit/integration and generated GraphQL checks through the existing
Makefile targets. Capacity acceptance uses the existing profile and its owned
fixtures:

Before testing, update affected reference-stack dependencies to explicit
latest stable compatible pins selected at implementation time; validate their
schema, flow, external_id, pagination and courier capabilities. Historical
planning pins are not the release target.

```sh
make capacity TARGET=api PROFILE=readiness MODE=alpha
```

The readiness run must use the feature's seeded identity dataset, verify each
request's expected groups and authorization decision, and preserve the
existing API readiness, dataset, topology and resource gates. Run the new
identity restart fault only through the existing `make chaos` interface and
with explicit test-owned container names and confirmation.
