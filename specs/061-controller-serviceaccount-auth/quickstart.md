# Quickstart: Controller-Manager Service-Account Authentication (Phase 1)

## Test-first implementation order

1. **Datastore**: failing contract tests for `CreateServiceAccount`/`GetServiceAccountBySubject`/`UpdateServiceAccountKeys`/`SetServiceAccountDisabled`/`DeleteServiceAccount` against both `memdb` and Scylla backends (mirroring `file_test.go`'s shape). Implement `entities.go`, `datastore.go`, `memdb/backend.go`, `scylla/serviceaccount.go`, `scylla/migrations/007_service_account.cql` until green.
2. **`serviceaccount-assertion`**: failing unit tests for claim validation (typ/kid/aud/exp/jti), signature verification against enrolled keys, replay rejection, `OutcomeChallenge` vs. `OutcomeDeny`. Implement `provider.go`/`replay.go` until green.
3. **`serviceaccount-jwt`**: failing unit tests for claim validation, multi-key overlap-window verification, disabled/deleted/UID-mismatch denial, empty `Roles`. Implement `provider.go`/`keys.go` until green.
4. **Mutations**: failing resolver tests for `createServiceAccount` (dup rejection, zero-key rejection), `rotateServiceAccountKey` (overlap window, empty-result rejection), `deleteServiceAccount` (idempotent, cancels future auth), `issueServiceAccountToken` (subject/UID field-gate, TTL clamping). Implement `serviceaccount.resolvers.go`, `shared/schemas/serviceaccount.graphqls`, and the field-level authorizer extension until green.
5. **Wiring**: add both providers to `buildProviderRegistry`'s `switch`, confirm the default chain and startup behavior are unchanged when neither is listed (SC-005).
6. **`CredentialSource`**: failing unit tests for `StaticToken` (unchanged behavior), `ServiceAccountSource` (sign/exchange/cache/proactive-renew/backoff/singleflight). Implement `credential.go`, rewire `client.go`, consolidate `main.go`'s three call sites into one shared construction.
7. **`gitctl enroll-serviceaccount`**: failing idempotency test. Implement the subcommand.
8. **WebSocket**: failing tests for `InitFunc` accept/reject and `CloseFunc`/revocation-on-disable. Implement `transport.Websocket.InitFunc`/`CloseFunc` and the connection registry.
9. **Integration**: end-to-end `tests/integration/serviceaccount_auth_test.go` covering the full lifecycle; update `docs/implementation/020-pluggable_auth_architecture.md` (Phase 7 addendum), configuration docs, and `docs/runbooks/controller-auth.md`.

## Manual verification steps

```bash
# 1. Start gitstore-api with serviceaccount-assertion/serviceaccount-jwt opted into the chain
GITSTORE_AUTH__AUTHN__CHAIN='["static-users","serviceaccount-assertion","serviceaccount-jwt","anonymous"]' \
GITSTORE_AUTH__SERVICEACCOUNT__SIGNING_KEY="$(cat issuer_ed25519.pem)" \
make api

# 2. As an admin, create a ServiceAccount and enroll a public key
#    (via GraphQL createServiceAccount, or gitctl enroll-serviceaccount once User Story 5 ships)

# 3. Sign a client assertion with the matching private key and exchange it
#    (via a small test script, or gitctl enroll-serviceaccount's issuance helper)
curl -sS $API_URL -d '{"query":"mutation IssueToken($input: IssueServiceAccountTokenInput!) { issueServiceAccountToken(input: $input) { status { token expiresAt } } }", "variables": {"input": {"metadata": {"namespace":"controllers","name":"category-taxonomy"}, "spec": {}}}}' \
  -H "Authorization: Bearer $ASSERTION"

# 4. Use the returned access token exactly like today's static-admin/static-users token
GITSTORE_CONTROLLER__API_TOKEN="$SA_ACCESS_TOKEN" make controller

# 5. Confirm least privilege: bind serviceaccount:controllers:gitstore-controller-manager
#    to a scoped role in policy.yaml's role_bindings, restart, confirm
#    category.status.write succeeds and an admin-only action is denied.

# 6. Rotate: add a new key via rotateServiceAccountKey, confirm the old key
#    still works during overlap, remove the old key, confirm it stops working.

# 7. Revoke: disable the ServiceAccount, confirm new assertion exchanges and
#    already-issued access tokens are both rejected, and any open WebSocket
#    subscription for that account is closed immediately.
```

## What "done" looks like for this spec's P1 scope (User Stories 1-3)

An operator with zero `static-admin` and zero `static-users` identities
configured can still: create a `ServiceAccount`, enroll a key, exchange an
assertion for an access token, use that token to authenticate a GraphQL
request, and have that token's authorization determined entirely by an
`rbac-local` `role_bindings` entry that grants it exactly the actions it
needs — never `admin`. That is `SC-001`/`SC-002`/`SC-003` in one sentence.
