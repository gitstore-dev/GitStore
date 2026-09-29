# Research: Implement Secret Material ADRs

**Date**: 2026-09-22
**Status**: Planning decisions resolved; implementation evidence still required.

## R1. Reuse the shipped identity plane

**Decision**: Reuse spec 061 with merged #429 as the minimum identity contract.
Do not rebuild ServiceAccount storage, issuance authorization, enrollment, or
the token client.

**Evidence**: `gitstore-controller-manager/internal/graphqlclient/credential.go`
already sends `audiences`/`expirationSeconds` and reads
`tokenRequest.status.token`/`expirationTimestamp`. It has single-flight
exchange, a 10s exchange timeout, 45s assertion lifetime, 30s refresh margin,
and exponential backoff. `cmd/controller/main.go` resolves bootstrap material
before constructing authenticated clients. See spec 061's
`contracts/serviceaccount-mutations.md` and PR #429.

**Rationale**: This work changes how key bytes are obtained and refreshed, not
who can issue or receive tokens.

**Alternatives considered**: A new token endpoint, a parallel credential source,
or restoring pre-#429 response fields would duplicate shipped behavior and
weaken the explicit upgrade baseline.

## R2. Make File reference validation canonical

**Decision**: Introduce explicit `CredentialsRef` and strict shared validation;
keep provider calls out of validation/admission in this release.

**Evidence**: `gitstore-api/internal/catalog/file.go` currently declares
`FileSourceDefinition.CredentialsRef *SecretRef` and checks namespace but not
all ADR name/key/discriminator rules. `file_test.go` includes `kind: Secret`.
`schemas/gitstore/v1beta1/file.schema.json` defines a bare, permissive reference;
`shared/schemas/file.graphqls` and `internal/graph/resolver/converters.go` expose
it. Existing fixture:
`gitstore-api/internal/validate/testdata/file/product-hero.md`.

`gitstore-git-service/src/git/hooks/validation_handler.rs` calls
`CatalogService.ValidateResources`; `gitstore-api/internal/cataloggrpc/server.go`
routes structural validation before policy/admission. Rust need not parse
secret records. The JSON schema is a maintained contract artifact, not the
runtime Go validator.

**Rationale**: A single enforcement path keeps Git-hook and admission outcomes
consistent without provider/network work on push validation.

**Alternatives considered**: JSON-schema-only changes leave Go enforcement
incomplete; a Rust-only validator duplicates policy; provider existence checks
on every push introduce unnecessary outages and unbounded external work.

## R3. Resolve legacy compatibility explicitly

**Decision**: User selected migration before strict deployment; the strict
release rejects every bare File credential reference. A separately versioned
preparation baseline supports migration and compatible readers before that
cutover. No permanent dual shape or post-cutover acceptance flag.

**Evidence**: ADR-0009's temporary legacy allowance conflicts with spec 063's
strict new contract unless explicitly scoped. Both the current JSON schema
and GraphQL type describe the old object, not the new wrapper. Here "bare"
means `{kind: SecretRef, name: ...}`, not a scalar string. Do not confuse
object parsing with support for the new nested wrapper.

**Rationale**: Requiring migration without first providing compatible readers
can silently lose projected metadata or break client reads. A zero-legacy
audit and declared rollback floor reconcile the user's decision with
independently deployable service requirements.

**Alternatives considered**: Indefinite dual support was declined by the user.
A simultaneous fleet restart or direct projection edits would violate rolling
deployment/Git authority. Assuming old Go decoders understand nested fields is
not sufficient compatibility evidence.

## R4. Scope runtime resources honestly

**Decision**: Deliver a reusable runtime resolver, typed-material validation
and contract-test consumer only. Wire the production controller identity
consumer; defer File runtime consumption and credential-readiness status.

**Evidence**: No existing File source-fetch/credential-readiness reconciler was
found in controller reconciliation/listwatch registration. Current File
conversion copies metadata; it does not perform an outbound operation.

**Rationale**: The user explicitly chose this scope. Adding a File watcher,
status API and reconciler would create unrelated lifecycle work and misleading
readiness without a real payload operation.

**Alternatives considered**: A credential-readiness-only File controller was
offered and declined. Calling providers from GraphQL reads or validation would
violate the phase boundary. Tests do not constitute a production File feature;
docs must say so.

## R5. Share one small Go contract module

**Decision**: Add `shared/secretmaterial` as a repository-local Go module with
pure contracts and bounded local adapters; service logging, config, metrics
registration and JWT signing stay in their owning modules.

**Evidence**: `go.work` contains independent API/controller/OIDC/integration
modules, with no reusable shared Go contract module. Controller
`internal/secret/resolver.go` cannot be imported by the API. Current Dockerfiles
copy only one service and flatten it under `/build`.

**Rationale**: The canonical shape and error taxonomy must not drift between
resource and process consumers. Local module replacements plus explicit Docker
copy/layout changes support independent builds without publishing a package.

**Alternatives considered**: Copying regex/error rules, controller imports of
API internals, or a resolver microservice each adds drift or coupling. A remote
secret backend is not needed for the local file/env scope.

## R6. Bound local providers and separate their contexts

**Decision**: Retain file/env bootstrap adapters, add explicit JSON-record
format for whole records, and expose independently configured runtime adapters.
Enforce 256 KiB encoded / 128 KiB decoded aggregate / 64 KiB item / 32-item
limits, 16 in-flight operations, a 2s budget, and the seven ADR error classes.
No secret cache or provider-internal retry.

**Evidence**: Existing controller resolver validates paths/symlink containment
and classifies several failures, but reads values without an explicit size
ceiling, has incomplete ADR errors, and returns a single byte value. Bootstrap
name grammar is looser than ADR-0001. Tests already cover redaction, traversal
and cancellation and should be extended.

**Rationale**: Local providers suffice for development and mounted production
secrets. Bootstrap/runtime is a binding and authorization distinction, not
merely two strings passed to the same permissive resolver instance.

**Alternatives considered**: Remote SDKs/ambient-credential integrations remain
future adapters. Unbounded `ReadFile`, FIFO reads or timeout goroutines leave
resource limits unenforced. Cloud SDK errors must never become public errors.

## R7. Pair rotating private keys with their enrolled key IDs

**Decision**: Resolve on every token exchange, not every application request.
Use an atomic `serviceaccount-signing-key/v1` provider record containing
`privateKey` and `keyID` for overlap rotation. Preserve existing static-ID raw
input as a compatibility adapter without promising overlapping-ID hot rotation.

**Evidence**: `internal/graphqlclient/private_key_signer.go` takes an explicit
key ID and writes it into JWT `kid`; the API
`internal/auth/provider/serviceaccountassertion/provider.go` selects the
enrolled public key by that ID. Startup currently captures the key in a signer.
Replacing only PEM bytes while retaining a different key's `kid` cannot prove
successful rotation. API access-token signing-key fingerprint derivation is a
different concern and does not establish IDs for arbitrary enrolled assertions.

**Rationale**: Atomic key/ID records support operator-chosen IDs and prevent
torn pairs. New public key enrollment precedes record replacement; old key
retirement follows overlap. Cached access tokens keep normal requests cheap.

**Alternatives considered**: Deriving `kid` unconditionally breaks existing
arbitrary IDs; independent files permit torn updates; retaining the private
signer forever requires restart and fails the renewal scenario.

## R8. Preserve fatal startup and bounded renewal behavior

**Decision**: Startup remains fatal on required material failure. Existing
token readiness and single-flight remain; clamp jittered retry delay to 30s
and preserve caller cancellation. No new autonomous retry/refresh goroutine.

**Evidence**: `cmd/controller/main.go` builds credentials before serving health;
`credential.go` caches access tokens and uses a retry deadline. Existing
jitter around a capped base does not by itself prove the final delay is capped.

**Rationale**: Fail-closed does not require inventing a health-only startup mode
or invalidating still-usable tokens during a transient provider outage. It does
require rejecting calls once no usable credential remains.

**Alternatives considered**: Serving anonymously, indefinitely extending cached
tokens, or retrying in every waiter violates the contract. API-token signing
keys, `auth.jwt.secret`, gRPC HMAC and human verifier material retain ADR-0009's
existing explicit deferrals.

## R9. Extend existing capacity evidence, not a parallel benchmark

**Decision**: Extend `repository/lifecycle` with actual File pushes and repeated
controller token renewal, fixture-scoped provider outage/restore, and controller
replacement. Add a controller restart profile using the existing chaos runner.

**Evidence**: `scripts/run-capacity-target.sh` already dispatches lifecycle;
`tests/integration/repository_lifecycle_capacity_test.go` requires two API and
controller identities. `tests/capacity/README.md` documents alpha/production
thresholds, clean-checkout evidence and the managed stack's singleton Git
service. `scripts/run-chaos.sh` supports only explicitly targeted restart/pause.

**Rationale**: Existing namespace-only traffic does not test File reference
validation; a fake resolver proves library bounds but not real controller
renewal. Both scoped tests and deployed evidence are necessary.

**Alternatives considered**: A new public capacity entry point, diagnostic-only
results labeled production, or a Pumba success used as recovery proof are
rejected. Do not generalize a singleton Git volume into an HA claim.
