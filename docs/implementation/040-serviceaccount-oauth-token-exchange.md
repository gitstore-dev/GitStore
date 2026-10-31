# Reference-Issuer Workload Authentication: Proposed Contract

**Status**: Proposed; documentation only. No new route, identity adapter or configuration is implemented.

**Decision**: [ADR 0020](../ADRs/0020-serviceaccount-oauth-client-assertions.md).

This contract uses GitStore's optional reference OAuth/OIDC issuer and the
[UserDir draft](https://github.com/gitstore-dev/GitStore/pull/454).
The issuer authenticates workload clients and signs tokens; GitStore remains
authoritative for ServiceAccount identity, resolves directory profile/groups,
and delegates issuance/resource decisions to its configured AuthZ provider.
Native ServiceAccount GraphQL issuance stays available without this stack.

## 1. Current baseline and required additions

| Component | Current behavior | Proposed addition |
| --- | --- | --- |
| Reference issuer | JWT access tokens; browser client with authorization-code/refresh grants | Distinct client-credentials/private_key_jwt workload registrations and mandatory token hook |
| Browser bridge | Login and consent against human directory sessions | Preserve human handlers; do not run workloads through browser consent |
| Generic oidc-jwt provider | Issuer/JWKS/audience verification; external username rules | Reuse cryptographic verification, with an explicit trusted workload mapping stage |
| Principal normalization | Reserved SA subjects accepted only from native SA providers | Narrow provenance-aware validation for a registered external workload adapter |
| UserDir | Existing selectable provider abstraction | #454's three profiles, explicit provider links and directory-owned groups |
| Native SA record | UID, public keys and disabled/deleted state | Retain authority; reconcile external client/profile projections |
| Secrets | Bootstrap identity key and resource credential boundaries | Reuse; no private keys in App manifests, directory profiles or issuer registration |

The checked-in reference images are Hydra v2.3.0 and Kratos v1.3.1. Generic
current documentation is not proof of support in those versions. Each
capability below must pass pinned-version integration tests before advertisement.
No image upgrade or Compose/configuration edit is included in this proposal.

## 2. Enrollment records and activation

A workload registration contains trusted issuer identity, exact token endpoint,
OAuth client ID, expected token subject, ServiceAccount UID/name, public-key
projection digest, immutable registration generation, allowed API audience and
enabled state. Administrative writes are authorized/audited; neither a
self-signed assertion nor a directory profile can create this registration.

Create a separate client ID for each account UID and immutable projected
key-set revision, for example gs-sa-<uid>-r3. Retain explicit overlap only for
registrations whose entire key sets remain approved. Never mutate an active
client's key set in place and pretend its revision stayed unchanged: a stale
issuer cache could otherwise authenticate a retired key while a hook stamps a
new revision. A new key-set revision creates a new client ID; clients roll
their non-secret configuration along with their selected signer.

The registration lifecycle is:
1. Provision/reuse the GitStore ServiceAccount with separately authorized key enrollment.
2. Reconcile the credential-less serviceAccount directory profile and provider link.
3. Stage an issuer workload client with approved public JWKs, grant/method and audience.
4. Verify the issuer projection, hook configuration and GitStore mapping agree on UID/revision.
5. Activate that immutable registration with conditional revision checks.
6. On retirement, reject old registrations in the hook/resource adapter, disable
   issuer clients, and reconcile profile/client cleanup with retention rules.

This is bounded resumable work across independent stores, not an atomic
distributed transaction. Use UID/revision keys, idempotent reconciliation and
staged disablement. A partially provisioned identity cannot acquire external
workload authority. GitStore UID/key/enablement state wins every conflict.

The UserDir draft allows either a retryable all-or-nothing SA create or durable
reconciliation of a later profile. Implementations must choose/document the
supported recovery path and ensure neither a permanently missing profile nor
an orphan directory identity remains. Do not let reconciliation invent keys
or human login credentials to repair a missing profile.

## 3. Issuer protocol contract

Use the registered issuer's token endpoint, discovered only from a trusted
configured issuer or configured explicitly. The reference endpoint path is
/oauth2/token; it is not a new gitstore-api /oauth/token route.

The following is an illustrative request to a public, proxy-prefixed issuer
endpoint. SIGNED_JWT is a signer-generated placeholder, not a credential.

```http
POST /identity/oauth2/token HTTP/1.1
Host: identity.example.com
Content-Type: application/x-www-form-urlencoded
Accept: application/json

grant_type=client_credentials&client_id=gs-sa-6d9afec3-668e-492e-bb30-82ff8ed59647-r3&client_assertion_type=urn%3Aietf%3Aparams%3Aoauth%3Aclient-assertion-type%3Ajwt-bearer&client_assertion=SIGNED_JWT
```

The decoded assertion illustrates ES256:

```json
{
  "alg": "ES256",
  "typ": "JWT",
  "kid": "workload-key-r3"
}
```

```json
{
  "iss": "gs-sa-6d9afec3-668e-492e-bb30-82ff8ed59647-r3",
  "sub": "gs-sa-6d9afec3-668e-492e-bb30-82ff8ed59647-r3",
  "aud": "https://identity.example.com/identity/oauth2/token",
  "iat": 1793491200,
  "exp": 1793491260,
  "jti": "c4de26bb-5bb9-4c89-9d70-d4868ff61d07"
}
```

Require iss/sub equal to the registered client ID, exact token-endpoint
audience, a fresh unpredictable jti, permitted kid/algorithm, positive lifetime
at most 60 seconds and explicitly bounded skew (proposed ceiling 30 seconds).
iat is required by this workload profile. Accept no arbitrary subject override,
remote key URLs or caller-defined verification keys. Test ES256/EdDSA separately
against the enrolled key type and pinned issuer; unsupported algorithms remain
unadvertised rather than silently converted.

The standard assertion has no mandatory GitStore-specific UID claim. The
registered immutable client ID and server-controlled mapping bind the UID.
A caller-supplied sa_uid, groups, roles, profile or registration-revision claim
must not override that mapping.

The issuer owns form parsing, OAuth validation/errors, rate limits and response
headers. Verify duplicate/oversized/malformed input handling and no-store token
responses for the selected implementation. Do not claim the old API-owned
400/503 error table as the issuer's behavior. The normal success envelope is:

```json
{
  "access_token": "ISSUER_ACCESS_TOKEN",
  "token_type": "Bearer",
  "expires_in": 600
}
```

The token issuer/lifetime are the registered external issuer's values and
configured caps. No refresh token, ID token, human scopes or personal account
credential is part of this workload contract. Grant/audience restrictions come
from the workload client registration; issuer scopes never authorize GitStore
resource actions or directory group membership.

Use a distinct API access-token audience rather than the browser or workload
OAuth client ID. Verify token purpose from the issuer's tested access-token
profile and registered grant; an ID token signed by the same issuer is not an
API credential. Unsupported token shapes fail closed, without routing through
a more permissive human authentication path.

## 4. Issuer token-hook boundary

Propose an internal, authenticated issuer callback adapter at the logical route
/internal/identity/issuer-token-check. The exact route/API version is future
implementation work. It does not accept arbitrary user requests or issue an
access token itself. Use deployment bootstrap trust to authenticate the issuer,
with bounded internal egress and strict caller allowlisting; callback credentials
must not require the token being requested.

The adapter must:
- authenticate the issuer and validate the versioned, bounded hook envelope;
- resolve registered requester client context and grant against local mapping,
  never infer authority from an arbitrary request payload field;
- check the current GitStore SA UID, enablement and accepted immutable
  registration/key-set revision;
- validate requested/granted audience and permitted workload grant;
- resolve UserDir profile/active/groups using its explicit provider link;
- call the configured authorizer for serviceAccount.token.issue on that account;
- return approval plus server-derived UID/client/registration evidence, or deny
  without returning tokens, keys, groups or full profile/contact data.

The reference token hook receives issuer requester/session context and can
deny issuance. Hook context is not automatically proof of completed client
authentication. Pin and test issuer ordering: client assertion verification
and replay rejection must still occur before a token is returned. Hook retries
are idempotent permission/evidence checks; they do not consume assertions in
GitStore's native replay table or create profile/grant side effects.

Authenticate callback traffic independently from ordinary workload AuthN.
Never accept a bearer issued through this same callback as its bootstrap trust.
Do not promote a requester-named account to a principal merely to skip policy.
The candidate identity comes from the controlled registration; issuer-side
possession proof remains required for successful final issuance.

The hook runs for all grant types in the pinned reference implementation.
A dispatcher preserves existing human authorization-code/refresh processing
without adding workload claims; approved machine registrations take the strict
workload branch. Unknown/disabled clients cannot select the human branch by
sending a different grant or omitting custom fields. Human/customer tokens
cannot obtain SA identity through username/email claims or hook parameters.

Because the reference hook is issuer-wide, callback outage can also prevent
human token issuance or refresh even when its human branch adds no claims.
Measure and document that failure domain explicitly. Do not claim machine-only
availability impact, enable an unauthenticated fail-open human branch, or roll
out the hook without testing the existing browser/refresh flows under failure.

Timeout, authentication failure, missing directory facts, denied permission,
stale registration or malformed hook response must fail issuance closed.
An optional best-effort claims hook is insufficient. The pinned adapter must
test 200 approval, denial and failure behavior rather than assume all issuer
versions have identical statuses or JSON envelopes.

Some issuer hook payloads may contain sensitive request fields. Treat the
entire callback body as credential-bearing: verify sanitization for the pinned
version, parse only allowed fields and disable body capture across proxy,
application, tracing and test artifacts. Never log client_assertion while
diagnosing a failed hook. Issuer callbacks and app lifecycle webhooks are
different trust surfaces and do not share a generic public subscription sink.

## 5. Token evidence and trusted workload adaptation

The issuer's signature authenticates issuer-controlled identity evidence.
For the pinned adapter, document the exact serialized claim paths for:
client identity, ServiceAccount UID, immutable registration ID/generation,
projected-key digest and allowed API audience. Preserve ordinary issuer
iss/sub/exp semantics. The reference hook can add access-token session claims;
it must not be assumed able to replace reserved JWT subject/issuer fields.

The implementation must freeze and test one versioned claim namespace before
activation. Do not copy arbitrary assertion/form values into it. It deliberately
contains no authoritative groups, directory profile, active state or email.

At GitStore request ingress:
1. Verify the external JWT's cryptographic issuer, audience, expiry and token purpose.
2. Classify only explicitly configured workload issuer/subject registrations.
3. Require signed mapping evidence and match it to the current SA UID and
   accepted registration/key-set revision.
4. Construct a provenance-checked canonical ServiceAccount principal.
5. Resolve UserDir facts using that AuthN-provider-specific link.
6. Enforce profile/endpoint restrictions and authorize the requested resource action.

The current central validator rejects oidc-jwt principals impersonating reserved
SA subjects. Add a narrow trusted workload adapter and corresponding validator
contract; do not loosen the generic external username rule or accept arbitrary
system: claims. A known workload client with invalid/missing evidence fails
closed instead of falling back to an ordinary human identity path.

Trust is scoped to issuer plus registered client/subject, never to a bare sub
string. A different issuer or AuthN provider with the same text cannot acquire
the link. Retired UIDs, accepted-revision changes and directory deactivation
invalidate authorization even while a signature/expiration remains valid.
Issuer signing-key revocation and workload enrollment-key retirement are
distinct checks; directory profile state cannot reactivate a deleted SA.

## 6. UserDir orchestration from #454

Apply the same enrichment after any supported AuthN method and before AuthZ:
static-users, human oidc-jwt, native serviceaccount-assertion/serviceaccount-jwt
and the proposed external workload adapter. Anonymous requests are not resolved.
The optional external issuer must not be the only path receiving directory groups.

The reference directory has three versioned schemas: staff, customer and
serviceAccount. Schema/admin-controlled metadata determines profile type, active
state and groups. Customer self-registration cannot create staff or SA profiles.
SA profiles have no email/login/recovery/verification credentials. Native SA
key/UID/enablement authority stays in GitStore.

A linked SA whose directory schema resolves as staff/customer is a profile-link
configuration error and must fail closed; the adapter cannot reinterpret the
same enrolled workload as a human identity. Reconciliation repairs the link
under authorized administration, never by copying token claims into traits.

Provider links are explicit per AuthN source and issuer where relevant.
Render permitted groups as system:group:name; reject/ignore reserved system:
group input and preserve only platform-synthesized system groups. Customer
groups cannot confer staff/operator admission. Token role/group claims are
non-authorizing data even when signed by the reference issuer.

A configured none or provider without groups yields no directory groups
legitimately. A successful unlinked lookup yields no profile/groups and
authorizes on those facts. A timeout/error fails closed with a retryable
result; do not substitute an empty profile to continue. External registration
activation can require a linked SA profile without changing native unlinked
semantics globally.

Directory group changes and deactivation affect new requests and existing
subscriptions within the draft's five-minute bound. Bound per-replica caches,
invalidate or re-authorize/close watches, and preserve user-directory outage
semantics on warm/missing/expired entries. This bound does not extend SA
UID/registration revocation beyond the native identity contract.

Group/profile lookups use bounded concurrency/timeouts and an immutable
authorization fact snapshot per operation. AuthZ evaluation remains I/O-free;
the orchestration resolves directory data before invoking the evaluator.
Do not query the directory inside CEL/policy evaluation or manufacture an
allow decision while directory refresh is pending.

## 7. Replay, key rotation and recovery

Issuer replicas must share durable client-assertion replay persistence and
single-use semantics across restarts. The existing native GitStore assertion
store remains separate. No claim is made that reusing the same jti across
issuer and native paths is a shared-store event; disjoint audiences/profiles
reject the same assertion at the wrong endpoint.

Test assertion lifetime/skew enforcement specifically for private_key_jwt,
not only a similarly named JWT authorization-grant setting. Verify replay TTL
covers every allowed window, including rolling changes to skew.
On uncertain token response, create a fresh assertion and retry with bounded
backoff; a timeout can follow successful issuance. No cached credential response
is reconstructed from a replay record.

Key rotation provisions a new immutable client/key-set revision, verifies
projection and callback, then rolls the workload's public client ID/signer
selection. Explicit overlap retains only still-approved registrations. Removing
an old key retires all registrations containing it; resource and issuance
adapters reject those revisions before asynchronous issuer cleanup completes.
Client/key/projection updates never revive an old UID or implicitly change groups.

Native already-issued tokens retain their current semantics; this proposed
external provenance contract does not retrofit native key-level revocation.
Do not promise revocation of a committed business operation. Keep auditable
registration/tombstone history and non-secret reconciliation progress.

## 8. Rollout and implementation evidence

Enable compatible workload adapters, directory orchestration and enrollment
reconciliation across all API/controller replicas before exposing issuer clients.
Validate callback/network trust, exact claims and replay handling for the pinned
issuer. Stage workload registrations separately from existing browser clients.
No supported registration is activated against an incompatible replica.

Rollback selects an explicitly configured transport and requires profile-correct
fresh assertions. Do not fall back automatically to a native/human/admin token
on issuer rejection. Preserve both replay histories, UID tombstones, retained
registrations and native signing keys. No shared Git-service topology changes
are implied; it remains exactly one active stateful process.

| Boundary | Required implementation evidence |
| --- | --- |
| Issuer enrollment | Staged disablement, correct public JWK projection, immutable client revisions, no private keys in issuer/directory |
| Directory | All AuthN/UserDir/AuthZ combinations; three profiles; explicit links; missing versus failed lookup; no token-sourced groups |
| Issuance hook | Authenticated caller, registered requester, fail-closed deny/outage, bounded retries, human-grant dispatch unaffected |
| External mapping | Reserved-subject guard retained, missing evidence rejected, issuer/client/UID binding, customer/human impersonation denied |
| Replay | Two issuer replicas, duplicate assertions, restart, expiry/skew, wrong-endpoint rejection; no invented cross-store guarantee |
| Revocation | SA disable/delete/recreate, key-set retirement, old token provenance, profile deactivation and live watch group removal |
| Recovery | Partial enrollment and orphan cleanup, issuer/directory outages, uncertain response with fresh assertion retry |
| Capacity | Sustained issuance plus Git push and two-controller reconciliation; bounded callback/directory queues, caches and timeouts |
| Rollout | API/controller/issuer rolling replacement, explicit transport rollback, no unsafe unknown-profile fallback |
| Leakage | Assertions, access tokens, callback secrets and identity mail/contact data absent from captured logs/traces/evidence |

Use the existing root Make families and Compose selectors. An implementing
configuration/Compose change must flag the quickstart repository follow-up.
A proposal passing make pr-ready only validates the unchanged code baseline
and document structure; it does not establish any of the above runtime support.

## Sources

- [UserDir draft at reviewed revision](https://github.com/gitstore-dev/GitStore/blob/9306c732904d67d9810f241e0d29fb759177fe22/specs/065-kratos-user-directory/spec.md).
- [Reference Compose](../../compose.oidc.yml) and [issuer configuration](../../config/oidc/hydra/config.yaml).
- [Hydra v2.3.0 token hook source](https://github.com/ory/hydra/blob/v2.3.0/oauth2/token_hook.go)
  and [pinned configuration schema](https://github.com/ory/hydra/blob/v2.3.0/.schema/config.schema.json).
- [RFC 7523](https://www.rfc-editor.org/rfc/rfc7523.html) and
  [RFC 6749](https://www.rfc-editor.org/rfc/rfc6749.html).
- [ADR 0009](../ADRs/0009-credential-secret-boundary.md) and
  [ADR 0010](../ADRs/0010-authorization-model.md).
