# Public Discovery Contract (Proposed)

## Transport and Bootstrap

- `GET /.well-known/gitstore` on the existing Admin listener; external proxies may publish a full prefixed URL. HEAD shares headers; OPTIONS uses explicitly configured discovery CORS. No management schema is mounted on Storefront.
- Anonymous route placed outside authentication and RBAC-readiness wrappers, with independent admission/rate protection. Supplied credentials never change the public representation. No cookies or `Set-Cookie`; cross-origin reads use configured origins without credentials. Authorization failures cannot disable endpoint discovery.
- Enabled and valid: HTTP 200, `Content-Type: application/json`, strong `ETag`, `Cache-Control: public,max-age=N,must-revalidate`, `Vary: Origin` if CORS differs. N <=60 and <=remaining metadata validity. Support `If-None-Match`/304 only after recomputing current availability; no stale 304 after expiry. HEAD has no body.
- Disabled: 404/no-store. Invalid publication profile or unmet local publication floor sets a dedicated discovery-valid=false and returns 503/no-store, including after a rejected reload; last coherent bytes may be retained internally but are not served. General API/AuthZ readiness failure alone does not invalidate discovery. Metadata failure degrades dependent flows inside the public 200 document. Overload: 429/no-store with bounded Retry-After; no private diagnostic detail. HTTP errors use HTTP status and minimal `{ "code": "DISCOVERY_UNAVAILABLE" }` or `{ "code": "RATE_LIMITED" }`; they are not a replacement GraphQL error contract.
- New endpoint/schema extensions are additive; `schemaVersion` uses semantic versioning. Clients ignore unknown optional IDs/fields. Unsupported major versions fail explicitly. No public GraphQL duplicate in this release.

## Example: Proxy Publication and an Explicit Native Registration

The example is a proposed fixture, not a claim of current provider support. The digest is computed by the implementation; its full literal value is omitted here.

```json
{
  "schemaVersion": "1.0.0",
  "revision": "sha256:<computed digest>",
  "profileId": "store-production-v1",
  "endpoints": [
    {"id": "admin", "protocol": "graphql-over-http", "url": "https://shop.example.com/platform/admin/graphql", "authentication": {"anonymousAccess": "OPERATION_DEPENDENT", "credentials": [{"providerId": "company", "scheme": "BEARER", "deliveries": ["AUTHORIZATION_HEADER"], "clientProfileIds": ["cli"]}]}},
    {"id": "admin-subscriptions", "protocol": "graphql-transport-ws", "url": "wss://shop.example.com/platform/admin/graphql", "authentication": {"anonymousAccess": "OPERATION_DEPENDENT", "credentials": [{"providerId": "company", "scheme": "BEARER", "deliveries": ["CONNECTION_INIT_AUTHORIZATION"], "clientProfileIds": ["cli"]}]}},
    {"id": "git", "protocol": "git-smart-http", "urlTemplate": "https://shop.example.com/platform/git/{namespace}/{repository}", "variables": ["namespace", "repository"], "authentication": {"anonymousAccess": "OPERATION_DEPENDENT", "credentials": [{"providerId": "company", "scheme": "BEARER", "deliveries": ["AUTHORIZATION_HEADER"], "clientProfileIds": ["cli"]}]}}
  ],
  "authentication": {
    "providers": [{
      "id": "company",
      "displayName": "Company account",
      "kind": "OIDC",
      "metadataState": "AVAILABLE",
      "discoveryUrl": "https://identity.example.com/.well-known/openid-configuration",
      "metadata": {
        "issuer": "https://identity.example.com",
        "authorization_endpoint": "https://identity.example.com/authorize",
        "token_endpoint": "https://identity.example.com/token",
        "device_authorization_endpoint": "https://identity.example.com/device",
        "grant_types_supported": ["authorization_code", "urn:ietf:params:oauth:grant-type:device_code"],
        "response_types_supported": ["code"],
        "code_challenge_methods_supported": ["S256"],
        "token_endpoint_auth_methods_supported": ["none"]
      },
      "clientProfiles": [{
        "id": "cli",
        "applicationKind": "NATIVE",
        "bootstrap": {"method": "PRE_REGISTERED", "clientId": "gitstore-cli"},
        "scopes": ["openid", "profile"],
        "tokenEndpointAuthMethod": "none",
        "callback": {"kind": "LOOPBACK", "host": "127.0.0.1", "path": "/callback", "dynamicPort": true},
        "resourceTarget": {"parameter": "resource", "value": "https://resources.example.com/gitstore", "grants": ["authorization_code"]},
        "flows": [
          {"name": "AUTHORIZATION_CODE_PKCE", "availability": "AVAILABLE"},
          {"name": "DEVICE_AUTHORIZATION", "availability": "AVAILABLE"}
        ]
      }]
    }]
  },
  "extensions": [],
  "search": []
}
```

Endpoint templates percent-encode each UTF-8 variable as exactly one path segment; reject slash-containing values rather than treating them as path fragments. Git URL is the clone base; native smart-HTTP suffixes are transport behavior. No direct native Git gRPC endpoint is disclosed. Storefront is absent while its handler is a placeholder.

Required top-level keys are those in the example; empty collections are allowed. Endpoint/profile/provider IDs are unique in their declared scope. Metadata fields are allowlisted standard names; missing fields follow the relevant standard's defaults and are never proof of optional grant support. On metadata expiry `metadataState=UNAVAILABLE`, safe reason is set, and dependent flow availability becomes UNAVAILABLE; retain only the pinned public metadata, never the failed upstream response. Discovery still returns 200 for unrelated endpoints/providers.

## Endpoint Credential Bindings

Login profiles describe how a client obtains credentials. Each endpoint's authentication descriptor describes which credentials that protocol actually accepts and how to deliver them; it references provider/profile IDs rather than copying login flows. Credentials use explicit scheme (BASIC or BEARER), supported delivery channel(s), providerId, and compatible clientProfileIds where applicable. A matching issuer alone does not prove endpoint audience/scope compatibility. Anonymous access is operation-dependent, not a blanket authorization grant; protected operations retain their own checks.

Validate bindings against the actual endpoint handler, configured provider chain, token/audience contract, and public profile registration. Derive support from typed provider/transport descriptors, not middleware function names or CapAuthenticate alone. Unknown/incompatible bindings fail publication validation. BASIC username/password does not need an OAuth profile. Do not advertise bearer-in-basic-password, cookies, browser redirects or token exchange unless an implementation explicitly supports that mechanism. Do not forward credentials to an endpoint merely because its URL appeared in discovery.

Current source evidence: Git's BasicAuthenticator forwards the original Authorization header to the shared AuthN chain; oidc-jwt accepts Bearer, static-users accepts Basic/password and Bearer sessions. The Git middleware's Basic challenge does not imply Basic-only parsing, and no OIDC token-as-Basic-password conversion exists. GraphQL HTTP also passes headers to the chain; WebSocket connection_init requires its authorization payload; optional non-browser upgrade headers are separately prevalidated and do not replace connection_init credentials. Endpoint-specific contract tests must verify these combinations and negative token-audience cases. Native Git clients need an actual Bearer-header delivery mechanism to use OIDC tokens; standard password prompting alone does not establish that capability.

The sample is hypothetical configured-provider evidence, not a blanket claim for every deployment. Its login profile may be shared by Git and GraphQL only when resulting tokens are accepted by both. If an endpoint has different audience/resource requirements, publish a distinct compatible profile/binding or mark that path unavailable; do not duplicate flow definitions or guess resource indicators.

## Other Identity Bootstrap Descriptors

```json
{
  "method": "DYNAMIC_REGISTRATION",
  "registrationEndpoint": "https://identity.example.com/register",
  "registrationAccess": "OPEN"
}
```

RFC 7591 descriptor: endpoint must match validated provider metadata. OPEN requires explicit operator/provider evidence; presence alone is UNKNOWN. Flow availability is REQUIRES_REGISTRATION until the client obtains accepted registration metadata. PROTECTED/UNKNOWN also carry a safe prerequisite reason and never imply anonymous registration. No initial access token, issued secret or registration management credential is public. Registration persistence/reuse/lifecycle are client-owned. The descriptor does not turn GitStore into a registration endpoint.

```json
{
  "method": "CLIENT_ID_METADATA_DOCUMENT",
  "profileRevision": "draft-ietf-oauth-client-id-metadata-document-03"
}
```

CIMD descriptor requires validated advertised provider support plus explicit deployment compatibility. The client chooses its owned URL and the provider evaluates it. Flow availability is REQUIRES_CLIENT_METADATA, never automatic approval. Preserve the pinned draft's semantics; do not silently use -00 behavior. The device grant needs separate provider/profile interoperability proof. GitStore fetches no caller-supplied client documents.

No automatic priority/fallback between methods. A CLI selects an explicitly compatible method; unavailable prerequisites are actionable states. Token authentication metadata is method-specific: do not copy one protocol's defaults into another. Pre-registered confidential clients may be described for a server-side consumer but must not be represented as directly usable native public profiles.

## Provider and Feature Registration Rules

- Publish login descriptors only for providers that implement a new optional public-login-description capability. Internal `CapAuthenticate` alone advertises no interactive flow. Static-users describe the existing Admin `login` mutation with the Admin endpoint ID; bearer-only providers need no login choice.
- OIDC grants are the intersection of configured public subset and current validated support. Pre-registration grants require operator-attested accepted client configuration; third-party drift is still possible, and the provider remains final authority. No online client-grant introspection is claimed.
- Optional RFC 8707 target is explicitly configured per profile/grant and compatible with API verification; not inferred from endpoint/proxy URL. Unsupported grant combinations omit it. Proprietary `audience` parameters remain a separate future provider-specific contract.
- Extension entries: `{id, version, enabled}`. Search entries: `{kind, supportedTerms}`. Terms come from owning implementations; do not register semantic/recommendation terms merely because a dependency is installed. Declaring IDs neither implements optional services nor accepts ADR 0019.
- Canonical registry construction rejects duplicates, unknown references and over-limit/oversized documents. Never serialize raw runtime config, provider policy, groups, credentials or private upstreams.

## Configuration Contract

Add `api.discovery` alongside existing endpoint policies: enabled/profile ID, required feature floor, CORS/public admission limits, full endpoint descriptors, expected public provider metadata and separate interactive profiles. Validate all configured references at startup. Existing verifier settings retain their meaning. Configuration examples and exact TOML decoding tests are implementation deliverables; no new env alias bypassing normal Viper mapping.

An advertised floor entry such as `admin.self-access-review@1` must correspond to implemented code and the selected provider adapter. Unknown mandatory floor entries fail ready. Metadata refresh may reduce availability, never add to the operator profile. See [operations](operations.md) for fleet activation and bounds.
