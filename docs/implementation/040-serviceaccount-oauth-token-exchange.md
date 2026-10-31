# ServiceAccount OAuth Token Exchange: Proposed Contract

**Status**: Proposed; documentation only. No endpoint or configuration below is implemented.

**Decision**: [ADR 0020](../ADRs/0020-serviceaccount-oauth-client-assertions.md).

This contract adds an optional OAuth client-credentials transport over GitStore's
existing ServiceAccount identity, enrolled public keys, assertion replay store,
token issuer and provider-owned authorization. It does not replace enrollment,
change resource credentials or register clients dynamically.

## 1. Endpoint and request

`POST <public-api-base>/oauth/token` accepts an UTF-8
`application/x-www-form-urlencoded` body. For public base
`https://store.example.com/gitstore`, the endpoint and assertion audience are
exactly `https://store.example.com/gitstore/oauth/token`. TLS and ordinary server
certificate validation are required. Do not follow redirects with an assertion.
The endpoint URL has no query/fragment. Preserve configured reverse-proxy prefixes.

| Parameter | Required | Contract |
| --- | --- | --- |
| `grant_type` | yes | Exact `client_credentials` |
| `client_id` | yes | Canonical ServiceAccount subject, identical to signed `iss` and `sub` |
| `client_assertion_type` | yes | Exact `urn:ietf:params:oauth:client-assertion-type:jwt-bearer` |
| `client_assertion` | yes | One compact signed JWT satisfying §2 |
| `scope` | no | Omitted/empty means default service identity access under live RBAC; any nonempty request returns `invalid_scope` |

```http
POST /gitstore/oauth/token HTTP/1.1
Host: store.example.com
Content-Type: application/x-www-form-urlencoded
Accept: application/json

grant_type=client_credentials&client_id=system%3Aserviceaccount%3Aacme-search&client_assertion_type=urn%3Aietf%3Aparams%3Aoauth%3Aclient-assertion-type%3Ajwt-bearer&client_assertion=SIGNED_JWT
```

`SIGNED_JWT` is a placeholder produced by the workload signer, not a usable
credential. The request has no Authorization header, client secret or cookie.
HTTP Basic, bearer access tokens and existing GraphQL assertion bearer headers
are not alternate authentication methods at this endpoint. An Authorization
header or simultaneous credential methods fails `invalid_request` rather than
falling through to ordinary API authentication. Recognized unsupported
credential fields such as `client_secret` and `assertion` are rejected rather
than accepted as additional authentication. A JWT bearer grant request fails
`unsupported_grant_type`.

Parse the body once with fixed byte/parameter limits. Reject duplicate parameter
names after decoding, invalid percent encoding, invalid UTF-8, multiple assertion
values, JSON/multipart bodies and any nonempty request query string.
Treat empty parameter values as omitted before required-field validation.
Bound and ignore unrecognized extension parameters as required by the OAuth
request rules; never pass them to the token issuer or interpret them as claims.
In particular, unrecognized `audience`, `resource` or expiration parameters
cannot change the configured API audience or lifetime. Clients requiring
another audience/profile must not rely on those ignored parameters.

GET and other methods return 405 with `Allow: POST`. Wrong media type returns
415; excessive input returns 413 before JWT parsing. Route-level rate limits
can return 429. These HTTP failures are distinct from OAuth validation failures.
Requests are server-to-server; the initial contract adds no browser cookies,
interactive approval redirect or permissive cross-origin token delivery.

## 2. Signed assertion profile

The protected header and claims below illustrate the decoded JWT. The shown
times are illustrative NumericDate values, not a reusable credential.

```json
{
  "alg": "ES256",
  "typ": "JWT",
  "kid": "search-key-2026-11"
}
```

```json
{
  "iss": "system:serviceaccount:acme-search",
  "sub": "system:serviceaccount:acme-search",
  "aud": "https://store.example.com/gitstore/oauth/token",
  "iat": 1793491200,
  "exp": 1793491260,
  "jti": "c4de26bb-5bb9-4c89-9d70-d4868ff61d07",
  "sa_uid": "6d9afec3-668e-492e-bb30-82ff8ed59647"
}
```

| Field | Validation |
| --- | --- |
| `alg` | `ES256` for an enrolled ECDSA-P256 key, or `EdDSA` for an enrolled Ed25519 key; public key type must match |
| `kid` | Required, nonempty, bounded; selects a currently enrolled key only |
| `typ` | Omitted or exactly `JWT`; other types are rejected at this endpoint |
| `iss`, `sub` | Required exact canonical client ID; no case folding, alias resolution or namespace inference |
| `aud` | Exact configured external endpoint URL, as a string or singleton string array |
| `iat`, `exp` | Required NumericDates; `0 < exp - iat <= 60` seconds; validate actual time bounds with the configured assertion skew |
| `nbf` | Optional NumericDate; validated with the same bounded skew and cannot extend expiration |
| `jti` | Required unpredictable unique identifier, bounded; generate at least 128 random bits per assertion |
| `sa_uid` | Required immutable UID matching the current authoritative ServiceAccount record |

For server time `now` and leeway `L`, require `iat <= now + L`,
`now < exp + L`, `iat >= now - 60s - L`, and, if supplied, `nbf <= now + L`
and `nbf < exp`. Require finite numeric values representable by the verifier;
do not round malformed/non-numeric strings into timestamps. `L` is separately
configured, starts at a proposed maximum of 30 seconds and is part of the
published profile. The 60-second signed lifetime remains independent of skew.
Replay tombstones live through the entire `exp + L` acceptance window.

Reject duplicate header/claim JSON members, unsigned JWTs, HMAC algorithms,
unknown critical extensions, nested/encrypted/compressed payloads and token-supplied
`jwk`, `jku`, `x5u` or `x5c` verification material. Unknown noncritical claims do not
become trusted groups, roles, owners, scopes or resource attributes. Untrusted
client ID/claims may select a bounded candidate enrollment record; they cannot
authorize anything before signature verification and post-verification checks.

The immutable UID requirement is a documented extension of the basic client
assertion profile. The signer must be able to set this claim. No endpoint bypass
accepts UID-less assertions from otherwise recognizable OAuth client software.

## 3. Verification and issuance ordering

Use one transport-independent service with explicit profile input. The logical
steps are:

1. Enforce route configuration, TLS/proxy trust, body/rate limits and form syntax.
2. Validate supported grant/method and request parameter shape. Parse a bounded
   candidate JWT; do not consume replay state for malformed unauthenticated input.
3. Resolve candidate ServiceAccount and enrolled `kid`; check current enabled
   state and UID. Verify signature, algorithm, claims, audience, type and time.
4. Reconstruct trusted principal/subject facts from the verified identity and
   authoritative record. Delegate `serviceAccount.token.issue` to the configured
   authorizer for that cluster-scoped account. Validate requested/default scope.
5. Atomically consume the replay ID through the existing durable shared store.
   A store timeout/uncertain consume must not lead to token issuance.
6. Recheck current identity/key/policy revisions and invoke the existing issuer
   for the configured API audience/default bounded lifetime.
7. Return the OAuth envelope with no-store headers and sanitized diagnostics.

The final recheck must have a documented concurrency/linearization contract
covering account disable/delete/recreate and enrolled-key changes. A stale
worker cannot mint under a newly removed enrollment. If optimistic guards
conflict, abort issuance; a client retries with a newly signed assertion. Do
not claim arbitrary cross-table ACID transactions: implement and verify the
backend-neutral ordering/conditional protocol using the existing datastore
contracts before enabling the route.

The existing assertion verifier currently consumes replay state during AuthN.
Refactoring it into shared verification/consumption phases must retain the
GraphQL path's single-use and mutation-only restrictions. Tests must prove
that extracting common code does not make its authentication result available
for ordinary queries or grant the OAuth handler unrestricted provider authority.

Both transports reuse the same replay-store operation and legacy-compatible
digest of `jti`. At most one concurrent request using the same ID can pass
consumption across replicas/transports. Do not salt a new namespace by client,
key, route, profile or deployment replica. Two profile-correct assertions with
the same `jti` are still a collision and only one may issue. TTL changes during
rolling replacement must never remove a record while either profile can accept
the corresponding assertion. Keep a safe high-water retention ceiling until
all older accepted windows expire.

An assertion consumed before an issuer failure is spent. A dropped successful
response is not retried with the same assertion. No issued token or plaintext
assertion is stored in the replay record. A client signing a new assertion can
obtain another token; the service does not promise exactly one live token per
account or return cached token secrets on replay.

## 4. Response and errors

```http
HTTP/1.1 200 OK
Content-Type: application/json
Cache-Control: no-store
Pragma: no-cache
```

```json
{
  "access_token": "ISSUED_ACCESS_TOKEN",
  "token_type": "Bearer",
  "expires_in": 600
}
```

The access token placeholder is illustrative. Compute `expires_in` from the
actual issuer expiration after default/max-TTL handling, conservatively rounded
down to whole seconds. Do not return a token already expired or with zero usable
lifetime. The issuer's configured default may differ from the example. No
refresh token, ID token, private key or assertion is returned. Scope is omitted
because this profile has no nonempty OAuth scope vocabulary; live RBAC still
governs every resource request. Clients must not interpret scope omission as a
cluster-administrator grant.

OAuth validation failures use bounded JSON, ordinarily HTTP 400, with the same
no-store headers:

```json
{
  "error": "invalid_client",
  "error_description": "Client authentication failed"
}
```

| Condition | Response |
| --- | --- |
| Missing required field, repeated parameter, conflicting credential methods or malformed request | 400 `invalid_request` |
| Unsupported `grant_type`, including JWT bearer grant | 400 `unsupported_grant_type` |
| Well-formed request for unsupported client assertion authentication method | 400 `invalid_client` |
| Unknown/disabled/deleted account, stale UID, unknown key, invalid signature/claims/audience/type or replay | 400 `invalid_client`, same generic description |
| Authenticated client denied `serviceAccount.token.issue` | 400 `unauthorized_client` |
| Any nonempty scope request | 400 `invalid_scope` after successful client authentication |
| Oversized body or wrong media type | 413 or 415; no echo of input |
| Rate/concurrency quota reached | 429 with bounded `Retry-After` |
| Identity/replay store or issuer temporarily unavailable | 503 with bounded `Retry-After`; no token |

The 503 body may contain `{"error":"temporarily_unavailable"}` as an explicitly
documented operational extension, not an additional RFC 6749 §5.2 token error.
Clients use HTTP status to decide retry. Do not mask a replay-store outage as
invalid signature, and do not return account/key existence details. HTTP 401
challenges are not needed for this body-authentication-only profile; do not
advertise Basic authentication as a recovery path. Internal bounded reason codes
remain available to authorized operators without returning JWT contents.

## 5. Limits and abuse controls

The following are proposed initial ceilings, subject to implementation measurement;
they are not claims of tested throughput or currently available configuration:

| Work | Proposed ceiling |
| --- | --- |
| Decoded form body | 16 KiB, checked during streaming read |
| Compact assertion | 8 KiB before parsing; bounded decoded header/claims |
| Parameter count | 16 total, including ignored extensions |
| Parameter names | 128 UTF-8 bytes |
| Non-assertion value | 1 KiB; client IDs additionally obey existing ServiceAccount name bounds |
| JWT ID/key ID | 256 bytes each |
| Assertion lifetime/skew | 60 s / at most 30 s |
| Handler wall time | 2 s end-to-end, including backend work |
| JSON error response | 1 KiB; fixed sanitized descriptions |

Bound active requests, signature verifications, identity lookup work, replay
records, and retries per deployment. Pre-authentication limits use trusted peer
information and a bounded global queue; attacker-selected client IDs cannot
allocate unlimited limiter entries. Apply per-verified-client fairness after
authentication. Proxy forwarding headers are trusted only from configured
proxies. Never hold admission/reconciliation workers behind an unbounded token
exchange queue or spawn one goroutine per unlimited incoming assertion.

JWT verification imports no remote key source. Backend identity/replay work has
deadlines/cancellation and bounded retries. Reuse existing datastore and replay
tables; schema changes, if justified by an implementing feature, follow the
normal migration policy rather than introducing a second credential database.

Public error responses, proxies and telemetry redact request form bodies,
Authorization headers and successful token response bodies. Do not use full
client IDs, JWT IDs or audience URLs as unbounded metric labels. Record outcome,
fixed failure category, policy revision and approved audit identity fields under
existing sanitized diagnostics. Credential bytes are never evidence artifacts.

## 6. Client behavior and migration

Clients select `GraphQLAssertion` or `OAuthClientAssertion` explicitly in a
future versioned credential-source configuration. These are logical profile
names, not implemented configuration keys. The OAuth choice requires the exact
external endpoint URL, canonical client ID/current UID, enrolled key ID,
supported algorithm and bootstrap signer. It does not fetch or replace trust
material based on server errors. No automatic OIDC metadata inference is added.

Obtain a token before expiry using jitter and one in-flight exchange per
identity/endpoint/audience. Honor `expires_in`, keep tokens in bounded memory,
and create a fresh unpredictable `jti` for every request. On timeout, 429 or 503,
retry under a bounded exponential backoff and a new assertion. On invalid client,
unsupported grant, invalid scope or denied authority, surface actionable local
status and stop the rapid renewal loop; do not fall back to a personal/admin
token or another assertion audience.

A definitive resource-server authentication rejection can invalidate the cached
token and trigger one coalesced reacquisition, preserving the existing distinction
between authentication and authorization failures. Never blindly replay a business
mutation with uncertain commitment. Resume watches from the recorded resource
version after successful reacquisition and reconnect; expired access tokens are
not refresh credentials.

Rollout deploys support everywhere, validates proxy routing/endpoint URL and
shared replay retention, then enables the optional route. Only then enable
OAuth clients. GraphQL clients keep their existing type/audience. Disable the
new route only after moving OAuth clients to an explicitly configured compatible
transport; route removal cannot require clearing replay/identity state.
No old/new profile is silently accepted at the wrong endpoint.

## 7. Verification matrix

| Boundary | Required cases |
| --- | --- |
| Protocol | Valid form; percent decoding; UTF-8; empty/missing/duplicate fields; ignored bounded extensions; method/media/size errors |
| Profile | Both algorithms; absent/JWT type; exact proxy-prefixed audience; wrong subject/issuer/UID; multiple audiences; duplicate JSON members; unsupported headers/keys |
| Time | Lifetime and skew boundaries; future/past `iat`; `nbf`; retention across changed leeway and replica replacement |
| Authorization | Pluggable allow/deny; no proof-only issuance; account self-binding; scope rejection; no groups/roles injection |
| Replay | Two APIs, same assertion and different assertions sharing `jti`; both transports; crash/store uncertainty; retained records across restart |
| Enrollment | Key overlap/removal; disabled/deleted/recreated UID; no dynamic self-enrollment; no token resurrection |
| Clients | Dropped response and fresh assertion retry; renewal coalescing; 429/503 backoff; definitive authentication rejection; no blind mutation replay |
| Rollout | Mixed compatible replicas; proxy route selection; explicit transport rollback; existing GraphQL restrictions unchanged |
| Load | Valid/invalid assertion storms alongside sustained push and two-controller reconciliation; bounded queues, memory and replay storage |
| Leakage | Request/response logging, proxy capture, errors, traces, metrics and exported test evidence contain no credentials |

Run the reusable backend/provider contracts against both memdb and shared Scylla
where applicable. Use two APIs and two controllers for concurrent exchange and
renewal/replacement evidence. Report actually measured limits and latency rather
than labeling the entire identity plane highly available. Git remains exactly
one active stateful service with retained storage and non-overlapping replacement.
The full default `make pr-ready` remains required for an implementation PR.

## References

- [RFC 7523](https://www.rfc-editor.org/rfc/rfc7523.html): JWT client authentication profile.
- [RFC 6749](https://www.rfc-editor.org/rfc/rfc6749.html): request encoding, client credentials, token response and errors.
- [RFC 9700](https://www.rfc-editor.org/rfc/rfc9700.html): current OAuth security guidance.
- [ADR 0009](../ADRs/0009-credential-secret-boundary.md): secret resolver and bootstrap material.
- [ADR 0010](../ADRs/0010-authorization-model.md): provider-owned authorization and issuance action.
