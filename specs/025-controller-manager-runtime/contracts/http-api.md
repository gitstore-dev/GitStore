# HTTP API Contract: Controller Manager Runtime

**Module**: `gitstore-controller-manager`  
**Base path**: `/controller/v1`  
**Default port**: `5001` (`GITSTORE_CONTROLLER__PORT`)  
**Date**: 2026-06-11

---

## Health & Metrics

### `GET /health`

Returns a JSON summary of per-kind controller health. Intended for human operators and liveness/readiness probes.

**Response `200 OK`**:
```json
{
  "status": "healthy",
  "kinds": [
    {
      "kind": "Product",
      "activeWorkers": 4,
      "queueDepth": 12,
      "poisonItems": 1,
      "lastReconcileAt": "2026-06-11T14:32:01Z",
      "stalled": false
    }
  ]
}
```

`status` is `"degraded"` if any kind has `stalled: true` or `poisonItems > 0`.

### `GET /metrics`

Prometheus scrape endpoint. Exposes all `gitstore_controller_*` metrics.

---

## Poison Item Management

### `GET /controller/v1/poison/{kind}`

List all quarantined poison items for the given resource kind. Use `kind=_all` to list across all kinds.

**Path params**:
- `kind` — resource kind string (e.g., `Product`) or `_all`

**Response `200 OK`**:
```json
{
  "items": [
    {
      "kind": "Product",
      "namespace": "gitstore-test",
      "name": "widget-pro",
      "attempts": 5,
      "lastError": "spec validation failed: price must be positive",
      "lastAttemptAt": "2026-06-11T14:30:00Z",
      "quarantinedAt": "2026-06-11T14:30:05Z"
    }
  ]
}
```

### `POST /controller/v1/poison/{kind}/{namespace}/{name}/requeue`

Re-queue a specific quarantined item. Resets retry budget to zero. The item re-enters the active work queue and goes through the normal reconcile → retry → quarantine lifecycle.

**Path params**:
- `kind` — resource kind
- `namespace` — resource namespace
- `name` — resource name

**Response `204 No Content`** — item re-queued successfully.

**Response `404 Not Found`** — item is not in quarantine (may have already been re-queued or never existed).

**Response `409 Conflict`** — controller manager is shutting down; re-queue rejected.

**Response `503 Service Unavailable`** — controller manager not yet ready (cache not synced).

---

## Error Format

All error responses use a consistent JSON body:
```json
{
  "error": "string description",
  "code": "NOT_FOUND | CONFLICT | UNAVAILABLE"
}
```
