# Contract: Controller-Side `CredentialSource` and WebSocket Lifecycle

## `CredentialSource` (User Story 4)

```go
// gitstore-controller-manager/internal/graphqlclient/credential.go

type Credential struct {
	AccessToken string
	ExpiresAt   time.Time // zero value = "does not expire" (StaticToken)
}

type CredentialSource interface {
	// Current returns a credential valid for the caller's immediate
	// operation. May block briefly while one exchange is in flight, but
	// must respect ctx and must never block indefinitely.
	Current(ctx context.Context) (Credential, error)
}
```

`graphqlclient.Client.token string` becomes `Client.credentials CredentialSource`.
`do()` and `Subscribe()` call `c.credentials.Current(ctx)` in place of reading a
field. `Subscribe()` uses `Credential.ExpiresAt` to schedule a proactive
reconnect-before-expiry (User Story 6's server-side deadline remains
authoritative regardless of whether the client reconnects in time).

## Construction and sharing across `main.go` (research.md Decision 3)

**Before** (current state, three independent call sites):

```go
// registerNamespace, registerCategoryTaxonomy, registerProductWatch each do:
client := graphqlclient.New(cfg.Controller.ApiURI, cfg.Controller.ApiToken)
```

**After**:

```go
// main(): constructed exactly once
resolver, err := secret.NewBootstrapResolver(cfg.Controller.SecretProviderBootstrap, log) // ADR 0009 §3
if err != nil {
    log.Fatal("failed to build bootstrap secret resolver", zap.Error(err))
}
credentials, err := buildCredentialSource(ctx, cfg, resolver, log) // StaticToken or ServiceAccountSource, per precedence below
if err != nil {
    log.Fatal("failed to build credential source", zap.Error(err))
}
client := graphqlclient.NewWithCredentialSource(cfg.Controller.ApiURI, credentials)

// passed into each registerX function instead of each constructing its own:
registerNamespace(ctx, mgr, checkpointStore, cfg, log, client)
registerCategoryTaxonomy(ctx, mgr, checkpointStore, cfg, log, client, onRelated)
registerProductWatch(ctx, mgr, checkpointStore, cfg, log, client)
```

`graphqlclient.New(baseURL, token string)` remains for any external caller/test
that still wants the trivial immutable-token constructor; it becomes a thin
wrapper: `NewWithCredentialSource(baseURL, StaticToken{Token: token})`.

## `buildCredentialSource` precedence (FR-015)

1. If `cfg.Controller.ServiceAccountKeyRef` is set: resolve that ADR 0001
   `SecretRef` through the configured **bootstrap-tier** `SecretResolver`
   (ADR 0009 §3), and construct a `ServiceAccountSource` from the
   namespace/name/keyID plus the resolved private key. Takes precedence over
   `ApiToken` whenever configured. A resolution failure is **fatal and fails
   closed** — it MUST NOT fall through to step 2.
2. Else if `cfg.Controller.ApiToken != ""`: construct a `StaticToken` —
   the deprecated dev/CI compatibility path (FR-014), unmodified in behavior.
3. Else: fail startup with an actionable error naming both config surfaces —
   mirrors today's existing single-check `ApiToken`-required validation, made
   conditional rather than removed.

Per ADR 0009 §1, the private key is never read directly via `os.ReadFile` from a
configured path; the `type: file` bootstrap provider is the supported
local-development equivalent and keeps provider topology out of the
component's own code.

## `ServiceAccountSource` renewal behavior (User Story 4)

- On first `Current(ctx)` call (or when the cached credential has expired),
  sign a client assertion (`typ: "gitstore-sa-assertion+jwt"`, ≤60s lifetime,
  fresh `jti`), call `issueServiceAccountToken`, cache the result.
- Schedule proactive renewal at `ExpiresAt - refreshThreshold` (a fixed
  fraction of the token's TTL, e.g. 2 minutes for the 10-minute default).
- On renewal failure, apply jittered backoff (reusing the existing
  `cenkalti/backoff/v5` dependency already used elsewhere in
  `gitstore-controller-manager`) rather than a tight retry loop; surface
  "not ready" via the existing `internal/health` handler until a valid
  credential is obtained (FR-016).
- `Current(ctx)` is safe for concurrent use across all call sites sharing this
  one instance (mutex-guarded cached credential + singleflight around the
  exchange, so three concurrent callers approaching expiry trigger exactly one
  exchange, not three).

## WebSocket `InitFunc`/`CloseFunc` contract (User Story 6)

```go
// gitstore-api/internal/app/server.go
gqlServer.AddTransport(transport.Websocket{
    KeepAlivePingInterval: 10 * time.Second,
    InitFunc: func(ctx context.Context, initPayload transport.InitPayload) (context.Context, *transport.InitPayload, error) {
        authHeader := initPayload.Authorization() // or initPayload["Authorization"], matching how the controller already sends it
        principal, decision, err := registry.AuthN().Authenticate(ctx, auth.AuthRequest{Header: headerFrom(authHeader)})
        if err != nil || decision.Outcome != auth.OutcomeAllow || principal == nil || principal.AuthMethod == "none" {
            return nil, nil, errors.New("unauthorized")
        }
        ctx = auth.ContextWithPrincipal(ctx, principal)
        if !principal.ExpiresAt.IsZero() {
            ctx, cancel := context.WithDeadline(ctx, principal.ExpiresAt)
            connectionRegistry.Register(principal.Subject, cancel)
        }
        return ctx, nil, nil
    },
    CloseFunc: func(ctx context.Context) {
        connectionRegistry.Unregister(ctx)
    },
})
```

- Rejects anonymous, expired, disabled, deleted, or UID-mismatched service
  accounts before `connection_ack` (FR-018).
- `connectionRegistry` is an in-memory, single-instance-scoped map from
  `ServiceAccount` UID to a set of cancel functions (mirrors doc 021 §8d's
  design; explicitly not datastore-backed — see spec.md Assumptions on
  multi-replica deferral).
- `deleteServiceAccount`/disabling a `ServiceAccount` MUST call
  `connectionRegistry.CancelAll(uid)` synchronously as part of the same
  mutation handler, satisfying FR-019's "immediately" requirement within the
  single-instance profile this spec targets.
- The access token's `exp`-derived context deadline is the *authoritative*
  boundary (Acceptance Scenario 4 of User Story 6); client-side proactive
  reconnect (User Story 4) is a continuity optimization only, never the
  security boundary — consistent with `graphql-ws`'s own documented rationale
  that mid-connection credential swap has no real security benefit once a
  connection is established (doc 021 §16, citing
  `enisdenjo/graphql-ws` discussion #292).
