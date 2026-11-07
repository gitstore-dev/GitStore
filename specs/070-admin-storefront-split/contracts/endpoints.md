# Endpoint and Failure Contract

## Fixed listeners and paths

Each endpoint has one named listener. There is no shared GraphQL listener, generic `api.port`, zero-port sharing convention or routing mode. Proposed defaults are Admin 4000 and Storefront 4001; operators may change ports without changing paths.

| Listener | Configuration | GraphQL | Playground |
| --- | --- | --- | --- |
| Admin (`A`) | `api.admin_port` | `A:/admin/graphql` | `A:/admin/playground` |
| Storefront (`S`) | `api.storefront_port` | `S:/storefront/graphql` | `S:/storefront/playground` |

Both ports must be nonzero, distinct and different from `api.git_port` and `api.grpc_port`. Bind failures remain startup errors. `/graphql`, `/playground` and the other endpoint's prefixed routes return 404 on both listeners; no alias, redirect or schema selection by caller. A reverse proxy may expose both prefixed paths on one public origin without changing this internal two-listener contract.

Controllers, CLI, Makefile targets and Admin clients use a complete Admin URL. HTTP list/read/write and WebSocket watch traffic share that configured `/admin/graphql` URL; changing ports or reverse-proxy origin does not alter journal cursors, list/watch semantics or introduce endpoint discovery/fallback logic.

Root `/health`, `/ready` and the existing `/metrics` compatibility scrape surface belong to the Admin listener; no separate operational listener is introduced. Storefront unavailability does not make an otherwise-ready Admin listener unready. New metrics use OpenTelemetry via shared observability, not direct Prometheus instruments. Catalog gRPC and Git HTTP retain their protocols and named ports.

## Playground endpoint switcher

Provide an Admin/Storefront selector in the existing playground UI, with an explicit editable URL for each endpoint. The currently served endpoint starts with its same-origin prefixed GraphQL path. The operator supplies the peer's browser-accessible URL when origins differ; do not guess a public host/scheme/port from bind configuration or fetch arbitrary URLs server-side. A proxy may make both choices same-origin. This is playground-local convenience, not the capabilities-discovery feature.

Use the existing playground's endpoint/tab settings or a small surrounding selector, not a new frontend application. Switching selects the endpoint's schema/editor session, closes the previous subscription connection, clears authorization/custom headers and requires explicit credentials for the newly selected endpoint. Do not copy tokens, cookies or auth state between endpoint/origin sessions; cross-origin browser requests use no credentials unless explicitly configured for that session and allowed by endpoint CORS. Editing a URL creates a new endpoint identity and likewise clears credentials. Documents/history must not execute automatically on switch.

The Storefront playground remains available to select/examine the endpoint's 503 response while its data plane is unavailable. It cannot bypass that guard or expose Admin introspection. Keep development convenience subject to the existing playground exposure policy; do not make a private Admin listener publicly discoverable through the Storefront playground. Include tests for both directions, unconfigured peer URL, URL changes, no token/header leakage, independent schema sessions, CORS denial and fixed unavailable behavior.

## Transport

- Admin keeps POST GraphQL and the existing GET WebSocket upgrade path; retain existing plain-GET rejection rather than accidentally enabling cached Admin reads.
- Storefront registers GET/POST to the unavailable guard; WebSocket upgrade attempts also receive the same HTTP 503 and never switch protocols.
- CORS preflight is handled by endpoint origin policy before application execution. Allowed preflight may return 204; it does not establish Storefront readiness. Rejected origins receive no permissive allow-origin response.
- Each constructed executor owns finite parsed-query/APQ caches. The fixed Storefront unavailable guard does not construct an unused executor or caches; it cannot resolve Admin hashes. A future Storefront executor must have separate instances. No response cache or GET/persisted-query Storefront read implementation is introduced yet.

## Storefront unavailable response

For a method/path accepted as Storefront traffic, absent prerequisites produce:

```http
HTTP/1.1 503 Service Unavailable
Content-Type: application/json
Cache-Control: no-store
```

```json
{
  "errors": [
    {
      "message": "Storefront is unavailable",
      "extensions": { "code": "STOREFRONT_UNAVAILABLE" }
    }
  ]
}
```

Omit `data`; do not include policy internals, request content or target-existence hints. This response is independent of caller credentials because no data operation executes. Ingress size/origin/rate/concurrency rejection can precede it. Clients may retry boundedly but cannot infer a restoration time; no arbitrary `Retry-After` is invented. Errors are never cached.

The separate generated schema must parse and exclude management fields. Test-only direct invocation can check `node`, foreign IDs and public type identity. It must not expose a production switch or promise successful reads. Storefront `NOT_FOUND` semantics become externally observable only once a real publication/target implementation activates that executor. Until then, the unavailable guard has precedence.

## Authentication and IDs

Admin retains registry authentication and field/service authorization. Introspection requires an authenticated principal. Unsupported/expired credentials retain existing structured errors, not cross-endpoint fallback. Storefront does not share the Admin field authorizer or generated model package. A future public reader must authenticate/select target using a reviewed contract before accessing data.

Global IDs retain their base64 URI format. Each endpoint has a closed kind allow-list. Admin rejects Storefront kinds and the Storefront decoder rejects Admin kinds with the existing not-found presentation. URI validation continues rejecting malformed scheme/host/query/fragment/user-info inputs. Snapshot/release changes do not change public resource UID.

## Construction errors

Invalid/zero ports, equal Admin/Storefront ports, collisions with Git HTTP/catalog and unavailable bind addresses fail startup with the listener name and address, without secrets. Legacy `api.port` configuration is rejected with guidance to use named ports; no compatibility mapping is added. All previously bound listeners close. No serving loop starts before binding succeeds. Shutdown drains owned HTTP resources within the caller's deadline and force-stops catalog gRPC if necessary.
