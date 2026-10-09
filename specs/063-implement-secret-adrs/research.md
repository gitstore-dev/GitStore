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

**Decision (superseding the original migration design)**: GitStore is alpha
with no production deployments, and File breaking changes are permitted until
Release Candidate. Reject bare references directly. No preparation release,
dual-shaped GraphQL object, migration audit or mirror inventory is needed.

**Evidence**: ADR-0009's temporary legacy allowance conflicts with spec 063's
strict new contract unless explicitly scoped. Both the current JSON schema
and GraphQL type describe the old object, not the new wrapper. Here "bare"
means `{kind: SecretRef, name: ...}`, not a scalar string. Do not confuse
object parsing with support for the new nested wrapper.

**Rationale**: The earlier migration design assumed deployed data that does
not exist. Update development fixtures and GraphQL selections to the strict
contract without maintaining an unused compatibility schema. Independent
builds, replica correctness and bounded identity renewal still require evidence.

**Alternatives considered**: Preparation/audit tooling and indefinite dual
support were rejected as unnecessary. Do not rewrite Git history, mutate
admission input or automatically edit persisted development data.

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

**Topology correction (2026-10-03)**: The two-Git requirement in the original
plan/capacity contract was wrong. Constitution v2.0.0, introduced by `5f69102`
(#363), required replicas for every core service; its templates and architecture
guidance propagated that assumption into spec 063 in `f384044` (#431). The user
confirmed that Git is not replica-safe and repository sharding is unimplemented.
Constitution v3.0.0 and its dependent guidance now require singleton Git.
Keep the existing single-Git URI/callback checks for all capacity modes and
retain two-API/two-controller evidence. Separate volumes do not establish
supported routing or writer safety. Git HA is not an acceptance prerequisite.

## R10. Normalize configuration before extending bootstrap wiring

**Decision (2026-10-03)**: Follow API's nested typed configuration convention,
not controller's flat TOML schema/custom environment aliases. Use
`controller.serviceaccount.*`, `controller.secret_providers.bootstrap.*`,
`controller.checkpoint.*`, `controller.reconcile.*` and `controller.watch.*`.
Decode once and pass typed values. The user requires migration before
deployment and explicit rejection of old names; no alias/normalization layer.

**Evidence**: Re-inspected current checkout `d065d08`, after #431 committed the
plan and subsequent fixes/features landed. Both
`gitstore-api/internal/config/config.go` and
`gitstore-controller-manager/internal/config/config.go` already call
`SetEnvPrefix("GITSTORE")`, replace `.` with `__`, and use `AutomaticEnv`.
API registers leaf defaults and unmarshals nested structs. Controller then
adds `bindServiceAccountEnvironment` and `readServiceAccountConfig`, duplicating
the mapping with a manual `GetString` pass. Rust
`gitstore-git-service/src/config.rs` uses prefix separator `_` and path separator
`__`. The issue is not a missing prefix; it is incompatible configuration shape
and duplicate hydration. API's explicit source-provenance check is intentional.

**Rationale**: New resolver fields must not expand a flawed mapping. Matching
file paths, environment names and typed fields removes ambiguity and reduces
duplicate configuration code. Checkpoint/watch/reconcile nesting also prevents
another flat group from persisting beside the new identity tree.

**Alternatives considered**: Preserving the old names merely for consistency
was the original design error. A temporary compatibility alias layer was
offered and declined. A generic cross-service config framework is not justified
by similar setup calls alone; service validation and provenance remain local.

**Consequences**: The [configuration contract](contracts/configuration.md)
defines every relocation, the env-only default-registration requirement,
source-scoped old/unknown-key rejection, all producer/consumer updates, and
version-matched rollout/rollback snapshots. Raw provider material remains
supported under canonical new names; this does not grandfather old config.
Unrelated uncommitted implementation edits were not part of this design change.

## R11. Incorporate post-plan commits without implementing every new ADR

**Decision (2026-10-03)**: Use `d065d08` as the reviewed integration baseline.
Treat new proposed ADRs as design constraints while retaining spec 063's
explicit runtime-consumer and publication exclusions.

| Change since #431 | Consequence for this feature |
| --- | --- |
| #432 | Use `new_commit_sha` and `authorization.actor`; removed proto fields remain reserved |
| #433 / ADR-0010 | Preserve mutable-owner annotations and current ownership authorization during resource updates; ServiceAccount ownership remains audit-only |
| #435 | Preserve runner tracking, final checkpoint flush and cancellation classification |
| #437 / ADR-0017 | Product title is mandatory; `totalCount` removed; audit via complete pagination, never live aggregate fields |
| ADR-0012 / #434 | Admin-only identity/watch/credential surfaces; defer URI/schema move to N/N+1/N+2 rollout |
| ADR-0011 amendments | No new `mode`/`SCOPE` directive contract; no framework implementation here |
| ADR-0013 | No Markdown parsing/IR added; resolved material must never enter body output |
| ADR-0014 | No release/publication feature; resolved material cannot enter snapshots/public indexes |
| ADR-0015 | Validating-only Git admission; payload facts are asynchronous; future webhook secrets use runtime context |
| ADR-0016 | SecretRef remains inline external material, not a CRD ownership/readiness edge |
| ADR-0018 / #438 | Group-owned checkpoints/RBAC; conditional existing writes; no lease for read-only resolution or independent token renewal |

**Evidence**: Reviewed `f384044..d065d08` and the current ADR text. The committed
API handler still exposes `/graphql`, not the proposed split. Current
`internal/checkpoint/filesystem.go` atomically stores a complete per-kind
snapshot/cursor; `checkpoint.go` retains related replay keys. Atomic rename is
not controller fencing. #435 tracks runners and waits for final flush with a
5s shutdown budget.

**Rationale**: Ignoring these changes risks stale generated contracts, skipped
audit data, broken shutdown or overstated controller safety. Implementing all
proposed ADRs would instead introduce unrelated endpoints, controllers and
datastore work.

**Alternatives considered**: Rebase assumptions on #429 alone (too old);
immediately move all callers to `/admin/graphql` (server does not serve it);
add a group lease for token renewal (unnecessary and not a safety proof);
count via removed `totalCount` or cached aggregate hints (cannot prove audit
completeness). All rejected.
