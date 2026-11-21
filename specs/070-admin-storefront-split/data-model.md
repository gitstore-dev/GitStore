# Data Model and Runtime State

No new persisted resource, datastore table, migration, journal type or publication record is introduced by this feature. Existing Admin models, resource UIDs, versions, finalizers, admission diagnostics and `status.resolved` remain authoritative.

## Endpoint profile

An immutable construction-time profile has `kind` (`admin` or `storefront`), schema/executor, transport policy, configurable CORS policy, IP/cost policy and request bounds. Profile kind comes from its fixed listener, never a token or caller field. Profiles share one registry/datastore runtime where applicable, but have disjoint executors and finite APQ/query caches.

Admin retains its existing resolver/service dependencies. Storefront boundary dependencies contain no Admin datastore or Git writer. The current production Storefront handler is unavailable; unit-test readers are not a production activation mechanism.

## Listener assignment

| Field | Validation and meaning |
| --- | --- |
| `api.admin_port` | Default 4000; 1–65535. Serves only `/admin/graphql`, its playground and root operational routes. |
| `api.storefront_port` | Default 4001; 1–65535. Serves only `/storefront/graphql` and its playground. |
| `api.git_port` / `api.grpc_port` | Existing Git HTTP/catalog meanings; all four listener ports must be distinct. |
| `api.admin.cors` / `api.storefront.cors` | Independently configurable origin, credentials, allowed/exposed header and preflight-cache policy; empty origins deny cross-origin browser use. |
| endpoint limits | Validated positive finite resource limits. |
| trusted proxies | Explicit configured trusted proxy addresses; default no trusted forwarded headers. |

Zero/equal ports and collisions are invalid; there is no shared mode or generic `api.port`. Reject the removed key with a named-port migration diagnostic instead of mapping it silently. Resolve configuration once, then pre-bind all four listeners. Viper defaults must register the named port and nested CORS/limit fields for environment overrides. No `enabled`, role or legacy-mode property exists. Operational endpoints belong to Admin, so no fifth listener is needed.

## Global resource IDs

Reuse the existing wire format: `base64("gid://GitStore/<kind>/<uid>")`.

- Admin allow-list stays unchanged: Product, Category, Collection, Namespace, Repository, ProductVariant and File.
- Storefront has a separate allow-list; the initial identity-only contract contains StorefrontProduct. Future public resource types require explicit registration and schema contracts.
- Reuse/extract the pure URI/encoding validation, while each decoder retains its own closed kind allow-list. Neither package imports the other's generated models.
- Resource UID is stable across publication release changes. A snapshot or target is not encoded as a replacement resource UID; target scoping belongs to request/reader context when implemented.
- Well-formed opposite-endpoint kinds map to `NOT_FOUND` without a datastore lookup. Malformed IDs retain current `BAD_USER_INPUT` / `INVALID_GLOBAL_ID` diagnostics. The production unavailable guard takes precedence before any Storefront lookup in this release.

## Availability

This release has one Storefront outcome: unavailable. It is a fixed implementation state caused by absent publication/target support, not mutable configuration. HTTP 503 contains `errors[0].extensions.code = STOREFRONT_UNAVAILABLE`, no data and `Cache-Control: no-store`.

The later publication implementation may transition between ready and unavailable based on trusted dependency state. It must not add management fallback or process-local publication authority. Fixture state transitions test the future contract only, not a shippable “ready” toggle.

## Bounded ephemeral state

- Parsed query caches: 1,000 entries per endpoint. APQ caches: 100 entries per endpoint. Eviction affects efficiency, never authorization or schema isolation. No response cache is added.
- IP limiter and Admin principal cost entries: bounded maps with owned cleanup and 10-minute idle expiry; at capacity reject new-key admission rather than grow or bypass a limit.
- In-flight operations: finite nonblocking admission counters by endpoint, released on completion/cancellation; no waiting queue.
- Existing durable Admin watch state remains in the shared journal; connection bookkeeping remains local and reconstructible, with finite admission limits and existing auth-revocation teardown.

Per-replica traffic counters are load-protection state, not cluster-wide quotas or correctness authority. Document aggregate allowances increasing with replica count; authorization, sessions and resource consistency retain their durable/provider contracts.

## Lifecycle

`construct → validate → bind all → serve → drain → close` owns all listener, limiter-cleanup and connection resources. Any failure before serve closes previously acquired resources. Shutdown uses one deadline, stops new admissions, closes registered WebSockets through existing hooks and force-stops gRPC if graceful termination exceeds the deadline. Unavailable Storefront support does not make the Admin readiness check fail.
