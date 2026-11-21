# Security, Execution and Resource Bounds

These are implementation defaults/acceptance contracts for the split, not claims about the current source. Reuse `internal/middleware/security`, the existing provider registry, `x/time/rate`, gqlgen extensions and `wsregistry`. Do not implement the entirety of doc 038 or introduce a limiter service.

## Order of checks

1. Request identity and configured trusted-proxy handling.
2. Endpoint origin/preflight policy and bounded endpoint-specific IP admission.
3. Security headers, finite in-flight admission and bounded HTTP body handling.
4. Storefront unavailable guard (current production path stops here).
5. Admin bounded document parsing, validation, structural and operation-cost checks; provider authentication and principal admission; authenticated introspection gate; existing field/service authorization; execution. WebSocket upgrade/init authentication also precedes its operation documents.

This follows gqlgen's existing parse/operation middleware lifecycle. Parsing before provider authentication is protected by the HTTP body, document, token, validation and admission bounds; it must never receive an unbounded body. Before execution, every parsed document and APQ cache hit passes the selected endpoint's current validation and authorization checks. Unsupported transports never create another route to Admin. The WebSocket pre-decode allocation limitation remains explicit below.

## Default bound table

| Bound | Admin | Storefront |
| --- | --- | --- |
| HTTP request body | 1 MiB | 1 MiB; do not read it to construct unavailable response |
| Operation document text | 64 KiB | 64 KiB when an executor is later activated |
| Parser token count | 15,000 | 15,000 when activated |
| Maximum selection depth | 15 | 15 when activated |
| Aliases / root fields | 30 / 20 | 30 / 20 when activated |
| Directives / fragment traversal visits | 100 / 1,000 | Same when activated |
| Maximum reported validation errors | 50 | 50 when activated |
| Page size / node IDs | 250 / 50; validate before datastore access | Same where future schema supports them |
| Cost ceiling | 1,000 units/operation | 1,000 units/operation when activated |
| Finite in-flight HTTP operations | 128/replica | 32/replica |
| Admission waiting queue | None; reject immediately | None; reject immediately |
| Non-subscription execution deadline | 10 s | No backend execution now; 10 s for future reads |
| Per-IP rate / burst | Existing 50 requests/s / 100 baseline, independently configurable | 200 requests/s / 400, independently configurable |
| Parsed-query / APQ cache entries | 1,000 / 100 | Separate 1,000 / 100 when executor is used |
| Limiter key entries | 10,000/IP table and 10,000/principal table | 10,000/IP table; no principal cost bucket |
| Idle limiter key lifetime | 10 min; owned stoppable sweep | Same |
| Admin WebSocket active connections | 1,000 per replica; at most 20 operations per principal | No WebSocket upgrade |
| Generated child-resolver worker limit | Explicit existing 1,000 bound; finite operation admission also applies | Same explicit bound |

Reject invalid/nonfinite/zero limit settings at startup. Tests must exercise the exact boundary and one-over-limit values. Numeric defaults can be tuned by explicit configuration, but limits must not silently become unlimited. The existing capacity thresholds are not weakened to accommodate these defaults.

Extend the existing IP limiter with bounded key storage and owned cleanup instead of adding a parallel map/ticker implementation. At map capacity, deny unseen keys until capacity expires; do not evict active keys in a way that resets their quota. Default trusted proxy list is empty; trust forwarded IPs only from explicit configured proxies. Ignore `GitStore-Storefront-Buyer-IP` until a reviewed access-token provider authenticates that claim.

## Complexity and principal cost

Use pinned gqlgen parser-limit/validation hooks and `FixedComplexityLimit(1000)`. Reuse generated `ComplexityRoot` callbacks for connection/list fields, multiplying child cost by validated requested page size (default 100, maximum 250). Charge aliases and expanded fragments per occurrence; cyclic fragments are rejected. Test variables, omitted pagination, negative values, directives and cached documents. A field-count-only complexity estimate is insufficient for list queries.

Cost follows resource work. Scalar/enum values, the stored Node metadata/spec/status envelopes and connection pageInfo have zero fetch cost; structural limits still count every selection. Use gqlgen's supported scalar/ignored-field options because it ignores zero-valued custom callbacks. Keep relationship/resource traversal outside those stored envelopes and prove the envelopes contain no resource nodes. Test the actual controller documents, including 250-row Product pages, against the unchanged 1,000-unit ceiling; do not shrink recovery pages or exempt controllers from the ceiling.

Admin's principal cost budget does not exist today; add it explicitly as part of FR-011 using the same bounded limiter ownership. Default refill is 50 units/second with burst 1,000, charged once per accepted operation after calculated cost is known. Exempt only the authenticated GitStore controller identity according to existing service identity/policy contracts, never a client header, arbitrary ServiceAccount or user-supplied group claim. The exemption applies only to this Admin principal budget; it does not bypass structure, operation ceiling, connection, concurrency or IP bounds. Storefront has no principal budget or controller exemption.

These budgets protect one replica. They are not cluster-wide billing quotas; replicas do not need a new coordination datastore. Readiness and documentation must not describe local traffic counters as globally exact. Every replica uses the same validated profile/configuration and authorization semantics.

## Failures

| Condition | HTTP / connection result | Diagnostic |
| --- | --- | --- |
| Request too large | 413, no execution | `REQUEST_TOO_LARGE` |
| Structure/page/ID batch bound | Existing validation envelope, 422 | Stable bound-specific code |
| Cost above ceiling | gqlgen 422 | `COMPLEXITY_LIMIT_EXCEEDED` |
| IP/principal quota or in-flight/key capacity exhausted | 429, no execution | `THROTTLED` and bounded retry guidance where calculable |
| Non-subscription deadline | Existing deadline error envelope; no retry loop | Existing timeout code |
| Admin introspection without authentication | Existing authorization error | Existing authn/authz code |
| Storefront prerequisites absent | 503, no data, no-store | `STOREFRONT_UNAVAILABLE` |

Use the existing GraphQL error presenter/admission diagnostics instead of embedding policy internals. Do not add automatic retries to the API pipeline. Explicit cancellation must release capacity, including failed auth, parser errors, WS disconnects and shutdown.

## WebSocket obligations

HTTP `MaxBytesReader` does not bound WebSocket frames. Preserve the existing Admin connection-init authentication, signed/service identity handling, expiry/revocation, connection registry, lifetime cancellation and journal-backed replay. Keep long-lived connections outside the 10-second HTTP operation deadline; initial upgrade/auth is bounded to 10 seconds, and operation concurrency is released correctly on completion/cancellation.

**Inherited production-hardening gap**: pinned gqlgen exposes no connection/read-limit hook. Copying just its WebSocket transport does not compile; it relies on seven transport files, private context/error helpers and roughly 1,100 lines of protocol code. Do not add that fork or a raw frame parser to this split. Parser/document limits bound accepted operations after decoding, not allocation for an oversized incoming frame. Record this distinction in the implementation's readiness evidence. A supported upstream read-limit integration and oversized/fragmented-message tests are required before claiming adversarial WebSocket payload bounds or full production bounded-memory readiness. This gap is tracked with #495's unverified production paths; controlled-client load results cannot close it.

Storefront rejects upgrades before opening a socket. No Storefront subscription implementation or future event model is introduced.

## Origin, ID and cache isolation

Configure CORS independently under `api.admin.cors` and `api.storefront.cors` using the existing gin-contrib/cors middleware. Fields are `allowed_origins`, `allow_credentials`, `allowed_headers`, `exposed_headers` and `max_age`. Both default to an empty origin list and `allow_credentials = false`: same-origin playground access needs no extra setup; cross-origin browser access requires explicit configuration. Methods remain constrained to each endpoint's supported transports (GET, POST and OPTIONS), not arbitrary new methods.

Accept exact normalized HTTP(S) origins; permit `*` only when credentials are false, and reject wildcard-plus-credentials, malformed origins/headers and invalid cache durations at startup. Do not reflect arbitrary Origin headers or infer allowed origins from the playground URL picker. Apply policy to preflight and normal/error responses, with appropriate `Vary: Origin` behavior for exact allow-lists. CORS does not authorize non-browser clients or replace WebSocket Origin validation; Admin upgrades must apply the same configured origin policy. Tests cover allow/deny, empty lists, wildcard/credentials rejection, independent endpoint settings and environment overrides.

Extract pure global-ID URI parsing only if needed to share it; keep closed endpoint kind allow-lists separate. Well-formed opposite-endpoint IDs produce `NOT_FOUND` without a lookup. Preserve current malformed-ID diagnostics (`BAD_USER_INPUT` / `INVALID_GLOBAL_ID`); do not silently change all parsing errors to not-found. Unknown valid kinds cannot trigger datastore access.

Admin and Storefront never share APQ/query cache instances or generated model types. No response cache is added. The later public-reader implementation must scope responses by trusted target, snapshot and document before enabling cacheable reads. This release does not invent that cache or public-reader dependency API.

## Observability

Use OpenTelemetry Metrics SDK instruments through `shared/observability` and its existing provider/export lifecycle. Do not add new direct Prometheus collectors or a second metrics registry. Existing legacy instrumentation/compatibility export remains outside this feature's migration scope. Add only bounded `endpoint`, `outcome` and `reason` attributes to request/rejection/in-flight observations; never use query, ID, IP, principal or token as metric attributes. Record admitted, rejected, canceled and completed work separately. Preserve existing auth/admission/watch signal names and semantics; test SDK metric collection/export as well as listener/limiter/connection cleanup.
