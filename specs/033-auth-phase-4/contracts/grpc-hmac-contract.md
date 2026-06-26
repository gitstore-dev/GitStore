# Contract: gRPC HMAC Inter-Service Authentication

**Branch**: `033-auth-phase-4` | **Date**: 2026-06-26

This document defines the contract between `gitstore-api` (gRPC client) and
`gitstore-git-service` (gRPC server) for inter-service authentication.

---

## 1. Protocol

**Transport**: gRPC over TCP (plain, no TLS in Phase 4)  
**Authentication mechanism**: Bearer token in gRPC metadata header `authorization`  
**Token format**: `Bearer <hmac_secret>` where `<hmac_secret>` is the raw shared secret string

---

## 2. Request Contract (client → server)

Every gRPC call from `gitstore-api` to `gitstore-git-service` MUST include:

```
Metadata key:   authorization
Metadata value: Bearer <GITSTORE_AUTH__GRPC__HMAC_SECRET>
```

The token is injected automatically by `hmacCreds.GetRequestMetadata()` on the Go side.
No individual resolver or RPC call site needs to be modified.

---

## 3. Server Validation Contract

The `HmacInterceptor` on the Rust side MUST:

1. Extract the `authorization` metadata value from every incoming call.
2. Strip the `"Bearer "` prefix.
3. Compare the extracted token to `hmac_secret` using constant-time equality.
4. If `hmac_secret_previous` is configured (rotation window open), also accept a token
   matching `hmac_secret_previous`.
5. If the token matches either accepted secret → pass the request to the handler unchanged.
6. If the token does not match or is absent → return `Status::unauthenticated` immediately;
   the handler MUST NOT be invoked.

---

## 4. Error Responses

| Condition | gRPC Status | Message |
|-----------|-------------|---------|
| `authorization` header absent | `UNAUTHENTICATED` | `"missing inter-service token"` |
| Token present but wrong | `UNAUTHENTICATED` | `"invalid inter-service token"` |
| Token matches primary or previous secret | — | call proceeds normally |

The Go API surfaces these as `grpc.Status` errors. Callers should treat
`UNAUTHENTICATED` from the git service as a configuration error (mismatched secrets),
not as a user-facing auth error.

---

## 5. Configuration Contract

Both services MUST be configured with the same value for `GITSTORE_AUTH__GRPC__HMAC_SECRET`.
During a rotation:

1. Set `GITSTORE_AUTH__GRPC__HMAC_SECRET_PREVIOUS=<old_value>` on the git service.
2. Set `GITSTORE_AUTH__GRPC__HMAC_SECRET=<new_value>` on both services.
3. Deploy the git service first (now accepts old + new).
4. Deploy the API (now sends new).
5. Remove `GITSTORE_AUTH__GRPC__HMAC_SECRET_PREVIOUS` from git service config.
6. Redeploy git service (now accepts only new).

---

## 6. Startup Invariants

| Service | Invariant | Failure mode |
|---------|-----------|-------------|
| git-service | `hmac_secret` MUST be non-empty | `ConfigErrors` → process exits 1 |
| gitstore-api | `auth.grpc.hmac_secret` MUST be non-empty | startup validation → process exits 1 |

---

## 7. Observability

**git-service startup log** (info level):
```json
{"level":"INFO","msg":"gRPC HMAC auth active","rotation_window_open":false}
```

**gitstore-api rejection log** (when receiving `UNAUTHENTICATED` from git service):
The standard gRPC error is propagated; no additional log line is required at the client
(the git service already logged the rejection).
