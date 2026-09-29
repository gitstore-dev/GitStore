# Implementation Plan: Implement Secret Material ADRs

**Branch**: `063-implement-secret-adrs` | **Date**: 2026-09-22 | **Spec**: [spec.md](spec.md)
**Input**: Feature specification from `specs/063-implement-secret-adrs/spec.md`

## Summary

Close the remaining ADR-0001/ADR-0009 obligations rather than rebuild spec
061's identity plane. Normalize File credential metadata to explicit
`CredentialsRef`, share bounded reference/resolution contracts across the Go
services, and replace the controller's process-lifetime signing-key capture
with resolution at credential renewal. Preserve the normalized #429 exchange,
authorization, public-key enrollment, and existing single-flight token cache.

The user selected mandatory migration before strict File enforcement and
contract-tested resource-runtime resolution without a production File consumer.
No File reconciler, payload fetch, object-store write, Secret resource, cloud
provider SDK, or change to spec 056 deletion semantics is included.

## Technical Context

**Language/Version**: Go 1.25 module baseline (API and controller); existing Rust Git service; current Go container builders use 1.26.1.
**Primary Dependencies**: Existing gqlgen, validator, zap, Prometheus client, JWT/crypto and standard library; new repository-local `shared/secretmaterial` Go module, no new third-party dependency.
**Storage**: Existing File spec JSON projections in memdb/Scylla and Git manifests; no table migration. Provider-owned local records and bounded process memory only; no persisted secret cache.
**Testing**: Go table/contract/race tests, existing catalog gRPC isolation tests, Rust hook integration tests, schema fixtures, deployed identity tests, root capacity/chaos runners.
**Target Platform**: Linux production containers; macOS/Linux development with regular local files or process environment.
**Project Type**: Multi-service backend plus one small shared contract library.
**Performance Goals**: Per-File reference validation p95 <=5ms/p99 <=20ms; local resolution p95 <=10ms/p99 <=50ms under healthy load; authenticated recovery <=60s after provider restoration. Full workload gates below.
**Constraints**: No provider I/O in stateless validation; no secret-read API; no private-key cache between exchange attempts; no background refresh goroutine per reference; no unbounded reads.
**Scale/Scope**: 5,000,000 background Product rows, bounded File batches, two APIs and controllers, sustained Git admission plus token renewal.
**Replica/Scaling Model**: Stateless validation on API replicas; existing durable identity/public-key datastore and resource-version concurrency; controller-local token caches are disposable and never authoritative.
**Authentication/Authorization**: Existing pluggable AuthN/AuthZ remains authoritative. Bootstrap resolution is owning-process-only; runtime context is namespace-bound and supplied by the authenticated consumer, never authored by the resource.
**Load/Backpressure Model**: At most 16 in-flight provider calls per resolver, no internal wait queue, 2s resolution budget, caller deadline wins; one controller exchange at a time with existing 10s exchange budget and capped jittered backoff.
**Capacity Profile**: Extend `make capacity TARGET=repository PROFILE=lifecycle` and `tests/integration/repository_lifecycle_capacity_test.go` with the opt-in secret scenario specified in [capacity contract](contracts/capacity-and-rollout.md); do not substitute Namespace-only load for File validation.
**Fault Profile**: Add `tests/chaos/profiles/controller-restart.json` using the existing safe `restart` action. The capacity verifier additionally withdraws/restores only its own isolated provider fixture to prove resolver outage, expiry, and recovery.

## Constitution Check

Pre-design review found three risks, resolved by the design: legacy File shape
compatibility, absence of a File runtime consumer, and static `kid` during key
rotation. No non-negotiable principle is waived.

| Gate                  | Pre-design disposition                         | Post-design disposition                                                  |
|-----------------------|------------------------------------------------|--------------------------------------------------------------------------|
| Test-first            | Existing tests located; gaps identified        | Failing tests precede each phase; matrix below                           |
| API/contract-first    | Bare File shape and bootstrap API differ       | Reference, resolver, identity, capacity/rollout contracts supplied       |
| Core-service boundary | Go owns validation; Rust invokes it over gRPC  | No duplicated Rust resolver or new service boundary                      |
| Replica safety        | Identity datastore already shared              | Two-API/two-controller renewal, outage and replacement evidence required |
| Multi-user isolation  | Separate bootstrap/runtime contexts required   | Disjoint bindings, namespace guards, pluggable-auth regression tests     |
| Production capacity   | No catalogue-wide lookup justified             | Five-million-row background plus fixed-size foreground work              |
| Repeatable evidence   | Existing lifecycle runner reusable             | `make capacity` extension plus `make chaos`; domain verifier required    |
| Bounded work          | Bootstrap read and key lifetime gaps           | Explicit byte/concurrency/deadline/backoff bounds                        |
| Observability         | Resolver logs exist, metrics incomplete        | Fixed-label outcomes/latency/inflight; redacted errors and readiness     |
| Incremental delivery  | Old File readers cannot be presumed compatible | Preparation baseline, migration, then strict rolling release             |
| Simplicity            | No existing cross-service Go module            | One small shared module; no File controller or remote SDK                |

**Gate result**: PASS for planning after the documented user clarifications.
Implementation and production readiness remain gated on actual evidence; this
document does not claim tests or capacity gates have passed.

## Project Structure

### Documentation (this feature)

```text
specs/063-implement-secret-adrs/
  spec.md
  plan.md
  research.md
  data-model.md
  quickstart.md
  contracts/
    references.md
    resolver.md
    bootstrap-identity.md
    capacity-and-rollout.md
  checklists/requirements.md
```

`tasks.md` is deliberately not generated by this command.

### Source Code (existing unless marked proposed)

```text
shared/
  secretmaterial/                         proposed Go module
  schemas/file.graphqls
schemas/gitstore/v1beta1/file.schema.json
gitstore-api/
  internal/catalog/file.go
  internal/validate/
  internal/cataloggrpc/
  internal/graph/{resolver,model,generated}/
  cmd/gitctl/                             migration audit integration
gitstore-controller-manager/
  internal/secret/                        adapter to shared contract
  internal/graphqlclient/
  internal/config/
  cmd/controller/main.go
gitstore-git-service/src/git/hooks/validation_handler.rs
tests/
  integration/
  capacity/
  chaos/profiles/controller-restart.json  proposed
docker/{api,controller-manager}.Dockerfile
compose.local.yml
compose.capacity.yml
config/config.toml
scripts/{run-capacity-target,validate-capacity-evidence,run-chaos}.sh
Makefile
go.work
.github/workflows/ci.yml
docs/ADRs/
docs/implementation/
docs/resource-storage/
```

**Structure decision**: Put pure validation, classified failures, resolver
context, provider adapters, and typed-material checks in a public, dependency-
light repository-local module. API/controller `internal` packages cannot be
shared directly. Logging/metrics registration and signing remain service-owned.
Do not make the controller import the API's application module.

## Phase 0: Research Results

See [research.md](research.md) for evidence and alternatives. Existing delivery
already supplies file/env bootstrap resolution, process-fatal startup failures,
token single-flight/backoff, per-controller mounts, and the #429 token client.
Remaining work is precise validation, complete error/size contracts, reusable
runtime context, observability, reloadable signing with atomic key-ID pairing,
and honest migration/capacity evidence.

## Phase 1: Design

### Resource metadata and stateless validation

File `source.credentialsRef` becomes an optional explicit wrapper. If absent,
existing source behavior is unchanged; if present, missing/wrong discriminators,
type, nested reference, namespace, name, key, or extra fields fail admission.
Type support and material completeness are runtime concerns; structural
validation never resolves a provider.

Go catalog validation is the enforcement point used by both
`CatalogService.ValidateResources` and admission. Rust's hook already delegates
to that service; extend its rejection tests, not its material-handling code.
Manually maintained JSON schema and GraphQL output contracts must match the Go
contract. Run existing gqlgen generation rather than hand-edit generated files.

File GraphQL `credentialsRef: SecretRef` -> `CredentialsRef` is a breaking
contract, not an additive rename. Publish it as a Conventional Commit/PR
breaking change, apply repository release-version policy, and require the
preparation baseline and client migration in the rollout contract. Preserve all
auth, API version, Git path, File deletion and File status semantics.

### Shared resolver and production bootstrap consumer

The shared library defines one material-acquisition interface and explicit,
non-interchangeable bootstrap/runtime construction contexts. Implement local
file/env adapters and a fake provider for contracts. No remote provider is
promised. Runtime contexts include environment, namespace, consumer, purpose,
resource identity and authenticated subject; bootstrap never derives identity
from a resource or calls back into GitStore.

No production API resolver instance or File runtime status write is introduced.
The API uses pure reference validation; resource-runtime resolution, typed key
sets and rotation are exercised by bounded contract consumers. This is the
explicit user-selected scope, not an unimplemented File controller hidden in
the plan.

Controller startup validates and resolves its own key before authenticated work.
Later token exchanges resolve fresh key material through the same boundary;
there is no retained private signer between exchanges. Keep usable access
tokens in the existing cache, with no stale extension after the refresh cutoff.
Use a versioned atomic identity record carrying `privateKey` and `keyID` for
rotation; preserve existing keyed/raw file/env configuration as a static-ID
compatibility adapter, not as a claim of overlapping-key hot rotation.

### Bounds and errors

| Work                       | Bound                                                                          |
|----------------------------|--------------------------------------------------------------------------------|
| Encoded provider record    | 256 KiB, enforced before parsing                                               |
| Decoded record             | <=32 items, <=64 KiB/item, <=128 KiB total                                     |
| Provider calls             | <=16 in flight per instance, no internal wait queue                            |
| Resolution                 | 2s or remaining caller budget, whichever is shorter                            |
| Exchange                   | Existing 10s total deadline including resolution/signing                       |
| Token exchanges            | One in flight per controller credential source                                 |
| Backoff                    | 1s exponential base, maximum 30s including jitter                              |
| Secret cache               | None in this release; exchange-local material only                             |
| Reference validation batch | Existing request limits plus capacity fixture cap of 10 Files/128 KiB per push |

Cancellation is preserved through `errors.Is`; provider failures use the seven
ADR classes without exposing SDK/OS error strings. Saturation is classified
`ProviderUnavailable` with a fixed `saturated` detail; caller cancellation is
not retried. Local providers accept regular local files only, not FIFO/device/
network-filesystem timeout promises. Context checks do not justify leaking a
goroutine around an indefinitely blocking read.

### Observability and deployment isolation

Emit resolver counters, duration and inflight signals with bounded
`consumer`, `purpose`, `tier`, `provider`, `reason` dimensions. No names, keys,
paths, namespace, subject or resource UID in metric labels; no key or token
bytes in any telemetry/error. Register shared-library observations with the
existing process registry rather than a separate metrics server.

Startup failure remains a classified fatal exit before authenticated work;
do not add a health-only process mode. During renewal failure, readiness uses
the existing credential usability gate. Existing valid token use is not
anonymous fallback, but no expired token is ever extended.

Config checks reject inline private keys and bootstrap bindings under the
shared config source. Compose validation rejects identity-bearing mounts
visible to another service. A process cannot prove all external IAM/host ACLs:
document that deployment-policy enforcement owns exclusivity outside managed
Compose. Use separate provider roots for resource-runtime bindings.

### Build and packaging

Wire the proposed module into `go.work`, API/controller `go.mod` requirements
and repository-local replacements. Add it to root build/test/lint and CI.
Current Dockerfiles flatten each service under `/build`; update layouts to
retain `/build/gitstore-api`, `/build/gitstore-controller-manager` and
`/build/shared/secretmaterial` so relative replacements work with `GOWORK=off`.
Preserve runtime binary/schema locations and test isolated image builds.
Do not add a module that works only through a developer's workspace.

## Phase 2: Implementation Sequence and Evidence

1. **Foundational contracts and packaging**: add the shared module and fixtures;
   prove validation, error redaction, limits, tier separation and independent
   builds before wiring consumers. Specify migration audit and dry-run behavior.
2. **US1 / preparation baseline**: add explicit File readers/validators,
   GraphQL conversion and schema parity, temporarily preserving legacy decoding
   only in the separately versioned preparation release. Migrate consumers and
   current manifests/projections. Supply paginated/read-only audit through
   `gitctl`, exposed by proposed `make check TARGET=secret-migration`.
3. **US1 / strict release**: after the zero-legacy audit, remove preparation
   compatibility and require the wrapper everywhere. No hidden compatibility
   flag ships in the strict release; malformed legacy documents fail closed.
4. **US2 / bootstrap**: adapt existing file/env loading to the shared interface,
   exact grammar, bounded reads, classified errors, config/mount validation and
   metrics. Preserve #429 token payloads and existing pluggable AuthN/AuthZ.
5. **US3 / rotation**: resolver-backed exchange signer, atomic key/ID bundle,
   public-key overlap and renewal/backoff tests; runtime fake-consumer rotation
   without adding a File operation.
6. **Production evidence and docs**: extend the existing lifecycle capacity
   gate, add controller restart chaos and isolated record-outage fixture,
   verify cross-replica recovery/redaction, update File and identity runbooks.

The preparation/strict release split is a compatibility prerequisite, not
permission to accept legacy references after strict deployment. Existing
history is not rewritten; restored historical manifests must be migrated
before being submitted as new desired state.

| Verification layer | Required evidence                                                                                                                         |
|--------------------|-------------------------------------------------------------------------------------------------------------------------------------------|
| Shared contracts   | Boundary lengths, traversal/URI/whitespace, explicit namespace, whole-record/item semantics, all seven failures, wrong tier and principal |
| Material types     | `aws-access-key/v1` required keys, unsupported type, no provider call on invalid input, no typed object on error                          |
| API/hook           | Same invalid fixture rejected by validation and admission; no durable write; Rust receives redacted file/field reason                     |
| GraphQL/schema     | Nested wrapper preserved; no material field; generated schemas agree; old clients explicitly migrated                                     |
| Identity           | Startup fails closed; no secret in config/logs; renewal resolves fresh pair; overlap with distinct key IDs; revoked/mismatched key denied |
| Authorization      | Namespace isolation, denied runtime binding, owning-subject-only issuance, enabled AuthN/AuthZ providers unaffected                       |
| Concurrency/fault  | Single-flight, <=16 provider calls, caller cancellation, bounded retry, outage spanning token expiry, no stale fallback                   |
| Deployment         | Independent API/controller image builds; controller-only mounts; two replicas and replacement; preparation/strict mixed-version run       |
| Production         | [Capacity contract](contracts/capacity-and-rollout.md), including true File pushes and provider restoration correctness                   |

For unaffected Rust production code, run hook integration with at least two
Git-service instances assigned distinct repositories; do not imply that two
writers sharing an unfenced bare-repository volume is supported. The existing
managed alpha stack has one Git service and cannot establish Git-service HA.

## Complexity Tracking

No constitution violation is accepted. One shared Go module is the only new
structural abstraction; it prevents divergent reference/error contracts and
cross-service `internal` imports. No additional service, database, queue,
durable cache, or production File reconciler is introduced.
