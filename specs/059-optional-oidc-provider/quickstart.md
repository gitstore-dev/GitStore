# Quickstart: Optional Reference OIDC Provider (Ory Hydra + Ory Kratos)

## Test-first implementation order

1. **Config/deploy files** (`deploy/oidc/`): add the Kratos identity schema and Hydra/Kratos serve config. Verify both images boot against them with a manual `docker compose -f compose.oidc.yml up hydra-postgres hydra-migrate hydra kratos-postgres kratos-migrate kratos` before any Go code exists.
2. **`gitstore-oidc-bridge` scaffold**: add the new Go module, config loader, and a `/healthz`-only HTTP server. Add a failing config-validation test (missing required `GITSTORE_OIDC_BRIDGE__HYDRA__ADMIN_URL`/`KRATOS__PUBLIC_URL`/`KRATOS__ADMIN_URL` fails startup) and implement until green.
3. **`GET /login`**: add failing tests — valid Kratos session → login challenge accepted with `subject` = Kratos identity id; no session → redirect to Kratos's self-service login URL with `return_to` preserved; Hydra/Kratos API failure → login challenge rejected, no raw 500. Implement `login.go` until green.
4. **`GET /consent`**: add failing tests — requested scopes fully within the registered client's permitted set → accepted with `email`/`preferred_username` claims populated from the looked-up identity; a requested scope outside the permitted set → only the permitted subset is granted; Hydra/Kratos API failure → consent challenge rejected. Implement `consent.go` until green.
5. **Compose assembly**: add `docker/oidc-bridge.Dockerfile` and `compose.oidc.yml` (all services from `plan.md`'s Project Structure), including the idempotent `hydra-client-setup` one-shot registration service. Verify a full manual Authorization Code + PKCE round trip (see "Manual verification" below).
6. **`Makefile` targets**: add `oidc`, `compose-oidc`, `oidc-down`, `oidc-stop`, `oidc-logs`, mirroring `scylla`/`compose-scylla`/`admin-down`/`admin-stop`/`admin-logs` exactly. Verify `make oidc` then `make oidc` again is a no-op with respect to already-provisioned Hydra client registration.
7. **Docs**: add the Phase 7 addendum to `docs/implementation/020-pluggable_auth_architecture.md` §7.
8. **Full verification**: `make build`, `make test`, `make lint`, `make pr-ready` — confirm zero regressions in `gitstore-api`, `gitstore-git-service`, `gitstore-controller-manager`.

## Manual verification

```bash
# 1. Start the reference OIDC stack (does not touch the core api/git-service/controller-manager stack)
make oidc
# or, to run everything together:
# make compose-oidc

# 2. Confirm the issuer is reachable
curl -s http://localhost:4444/.well-known/openid-configuration | jq '.issuer, .authorization_endpoint, .token_endpoint'

# 3. Register a new Kratos identity via self-service registration (browser flow)
open http://localhost:4433/self-service/registration/browser
# ...complete the form with an email + username; mailslurper (http://localhost:4436) catches any
# verification/recovery email in dev, since no real SMTP is configured.

# 4. Drive a standard Authorization Code + PKCE flow against Hydra as the registered client
#    (any bring-your-own client app can do this; a minimal manual walk-through:)
#    a. Browser -> http://localhost:4444/oauth2/auth?client_id=gitstore&response_type=code
#         &scope=openid+profile+email+offline_access&redirect_uri=<your redirect>&state=...
#         &code_challenge=...&code_challenge_method=S256
#    b. Hydra redirects to gitstore-oidc-bridge's /login -> bridge checks the Kratos session
#       (from step 3, a session cookie already exists in the same browser) -> accepted automatically
#    c. Hydra redirects to gitstore-oidc-bridge's /consent -> accepted automatically, no screen shown
#    d. Hydra redirects back to your redirect_uri with an authorization `code`
#    e. Exchange it: curl -s -X POST http://localhost:4444/oauth2/token \
#         -d grant_type=authorization_code -d code=<code> -d redirect_uri=<your redirect> \
#         -d client_id=gitstore -d client_secret=<GITSTORE_OIDC_CLIENT_SECRET> \
#         -d code_verifier=<your PKCE verifier>

# 5. Inspect the resulting ID token's claims (decode the JWT payload) and confirm the mapping in
#    data-model.md: `sub` = the Kratos identity's own id (not its email), `email`/`preferred_username`
#    match the traits entered at registration.

# 6. Point gitstore-api's (future) OIDCJWTProvider at this issuer and confirm token verification —
#    no gitstore-api code change required:
#    GITSTORE_AUTH__OIDC__ISSUER_URL=http://localhost:4444
#    GITSTORE_AUTH__OIDC__CLIENT_ID=gitstore
#    GITSTORE_AUTH__AUTHN__CHAIN=oidc-jwt,static-admin,anonymous
#    (present the access token from step 4e as `Authorization: Bearer <token>` against gitstore-api's
#    GraphQL endpoint once Phase 7 ships; this spec only guarantees the token side of this contract.)

# 7. Tear down
make oidc-down
```

## Expected token claims shape (post-login, decoded ID token payload)

```json
{
  "iss": "http://localhost:4444/",
  "sub": "018f2e3a-....-....-....-............",
  "aud": ["gitstore"],
  "email": "user@example.com",
  "preferred_username": "exampleuser",
  "scope": "openid profile email offline_access"
}
```

`sub` is the Kratos identity's own UUID — never `user@example.com` — per the stability contract in `contracts/kratos-identity-schema.md`.
