# Implementation Plan: Implement Secret Material ADRs

**Branch**: `063-implement-secret-adrs` | **Date**: 2026-09-22 | **Spec**: [spec.md](spec.md)
**Input**: Feature specification from `specs/063-implement-secret-adrs/spec.md`
**Configuration design revised**: 2026-10-03 against checkout `d065d08`;
prior configuration assumptions are superseded by [configuration contract](contracts/configuration.md).
**Post-plan integration review**: 2026-10-03, commits #432 through #438;
see research R11. Shipped changes are prerequisites; proposed ADR features
are compatibility constraints, not additional implementation scope.

## Summary

Close the remaining ADR-0001/ADR-0009 obligations rather than rebuild spec
061's identity plane. Normalize File credential metadata to explicit
`CredentialsRef`, share bounded reference/resolution contracts across the Go
services, and replace the controller's process-lifetime signing-key capture
with resolution at credential renewal. Preserve the normalized #429 exchange,
authorization, public-key enrollment, and existing single-flight token cache.

Normalize controller configuration before adding resolver options: nested
typed structs, one unmarshal, and the API/Git-service `GITSTORE_` plus `__`
environment convention. Do not perpetuate flat keys or post-unmarshal manual
hydration. Operators migrate renamed configuration before deploying new
instances; old keys are rejected, not silently aliased.

The latest user clarification supersedes the migration design: GitStore is
alpha with no production deployments and permits File breaking changes until
Release Candidate. Apply the strict contract directly, without preparation
readers, a transitional schema, mirror scanning or migration audit tooling.
Resource-runtime resolution remains contract-tested without a production File consumer.
No File reconciler, payload fetch, object-store write, Secret resource, cloud
provider SDK, or change to spec 056 deletion semantics is included.

## Technical Context

**Language/Version**: API/controller modules currently require Go 1.26.0; the shared library retains a Go 1.25 baseline. Existing Rust Git service; current Go container builders use 1.26.1.
**Primary Dependencies**: Existing gqlgen, validator, zap, Prometheus client, JWT/crypto and standard library; new repository-local `shared/secretmaterial` Go module, no new third-party dependency.
**Storage**: Existing File spec JSON projections in memdb/Scylla and Git manifests; no table migration. Provider-owned local records and bounded process memory only; no persisted secret cache.
**Testing**: Go table/contract/race tests, existing catalog gRPC isolation tests, Rust hook integration tests, schema fixtures, deployed identity tests, root capacity/chaos runners.
**Target Platform**: Linux production containers; macOS/Linux development with regular local files or process environment.
**Project Type**: Multi-service backend plus one small shared contract library.
**Performance Goals**: Per-File reference validation p95 <=5ms/p99 <=20ms; local resolution p95 <=10ms/p99 <=50ms under healthy load; authenticated recovery <=60s after provider restoration. Full workload gates below.
**Constraints**: No provider I/O in stateless validation; no secret-read API; no private-key cache between exchange attempts; no background refresh goroutine per reference; no unbounded reads.
**Scale/Scope**: 5,000,000 background Product rows, bounded File batches, two APIs and controllers, sustained Git admission plus token renewal.
**Replica/Scaling Model**: Stateless validation on API replicas; existing durable identity/public-key datastore and resource-version concurrency; controller-local token caches are disposable and never authoritative. Exactly one active Git service; no sharding, replication or Git HA scope.
**Authentication/Authorization**: Existing pluggable AuthN/AuthZ remains authoritative. Bootstrap resolution is owning-process-only; runtime context is namespace-bound and supplied by the authenticated consumer, never authored by the resource.
**Load/Backpressure Model**: At most 16 in-flight provider calls per resolver, no internal wait queue, 2s resolution budget, caller deadline wins; one controller exchange at a time with existing 10s exchange budget and capped jittered backoff.
**Capacity Profile**: Extend `make capacity TARGET=repository PROFILE=lifecycle` and `tests/integration/repository_lifecycle_capacity_test.go` with the opt-in secret scenario specified in [capacity contract](contracts/capacity-and-rollout.md); do not substitute Namespace-only load for File validation.
**Fault Profile**: Add `tests/chaos/profiles/controller-restart.json` using the existing safe `restart` action. The capacity verifier additionally withdraws/restores only its own isolated provider fixture to prove resolver outage, expiry, and recovery.

## Constitution Check

Pre-design review found three risks, resolved by the design: legacy File shape
compatibility, absence of a File runtime consumer, and static `kid` during key
rotation. No non-negotiable principle is waived.

Topology correction (2026-10-03): constitution v3.0.0 supersedes the unsupported
all-service replication mandate. API/controller concurrency remains an evidence
requirement, not a blanket HA claim; Git remains singleton-only.

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
| Incremental delivery  | Alpha File contract may break until RC         | Direct strict schema; no deployed-data compatibility or migration tool   |
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
    configuration.md
  checklists/requirements.md
```

`tasks.md` contains the dependency-ordered implementation checklist generated
after the post-plan integration review.

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
  cmd/gitctl/                             canonical enrollment configuration
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
and honest replica/capacity evidence.

## Phase 1: Design

### Typed configuration prerequisite

Follow [configuration.md](contracts/configuration.md) for the complete tree,
rename matrix, defaults, source precedence and migration policy. Introduce
`Controller.ServiceAccount`, `SecretProviders.Bootstrap`, `Checkpoint`,
`Reconcile` and `Watch` config groups; the root alone owns `controller`.
Remove `bindServiceAccountEnvironment` and `readServiceAccountConfig`, register
all leaf defaults for environment-only decoding, unmarshal once, and pass
typed values to consumers. Preserve API's intentional source-provenance checks.

Migrate config files, environment examples, deployment/CLI/bootstrap producers,
checkpoint cleanup and capacity consumers together. Fail on legacy/unknown
controller keys without rejecting sibling service sections in shared config.
New and old replicas use version-matched config snapshots; rollback restores
the matching config as well as the binary. This is a configuration-only
behavior change, not a change in identity, checkpoint paths or retry defaults.

### Current integration baseline and deferred endpoint split

Use checkout `d065d08` or a descendant preserving #432, #433, #435 and #437.
#429 remains the identity protocol baseline, not the entire compatibility
baseline. All new admission fixtures use `new_commit_sha` and
`authorization.actor`; do not restore removed protobuf fields/fallbacks.
Product fixtures require `spec.title`; list code uses `pageInfo`, never
removed `totalCount` fields.

Token issuance, controller watches and File credential
metadata belong to the Admin API under ADR-0012. The current server still
serves `/graphql`, so preserve that default now. If ADR-0012 ships first,
use its three-release sequence: N adds `/admin/graphql` plus deprecated alias,
N+1 moves clients, N+2 removes the shared-listener alias. Dedicated admin
listeners retain `/graphql`. Never infer a URI change from config nesting or
remove the endpoint alias under this feature's unrelated no-config-alias rule.
After schema relocation, target admin plus common schemas, not a glob spanning
Storefront. No endpoint split or directive framework is implemented here.

Preserve ADR-0010's shipped ownership foundation: resource updates retain reserved
owner annotations and permissions use current mutable-owner behavior
where supported, not creator-as-owner assumptions. ServiceAccount ownership
remains audit-only. Do not claim full provider-side ownership decisions or
introduce ADR-0011's removed `mode`/`SCOPE` machinery.

### Controller operation safety and checkpoint ownership

Apply ADR-0018 without adding a new controller or lease subsystem:

| Operation | Safety/ownership contract |
| --- | --- |
| Reference validation / provider read | Read-only, bounded and independently repeatable; no lease |
| Assertion signing / token renewal | Per-process single-flight, existing identity checks; replicas may independently obtain tokens; no group leader |
| Existing reconcile/status writes | Preserve version checks and idempotency; recompute on conflict, never replay stale patches |
| Operator key enrollment / record rotation | Existing authorization, distinct IDs and ordered overlap; provider record replaced atomically, not a new reconciler side effect |
| Watch checkpoint | Group-owned recovery hint, not write authorization; atomic snapshot/cursor record, bounded shutdown flush and replay-safe recovery |

Treat `checkpoint.dir` as an operator-assigned namespace for the configured
controller deployment's groups. Unrelated deployments/groups must not reuse
the same physical checkpoint root or a secondary-watch cursor. Within the
current fixed registration set, preserve existing kind-specific records and
related replay keys; document the primary writer and consuming group for each
registered watch. Do not infer cross-process write coordination from atomic
file rename or a materializer lease. A resumed older checkpoint may replay
work; existing conditional/idempotent reconciliation must make that safe.
Changing subscription scope requires a separate checkpoint namespace.

No new field manager, group-election mechanism, external side effect or File
status writer is introduced. Future non-idempotent consumers must select CAS,
API-validated fencing or external idempotency under ADR-0018 before adding
their operations. Preserve #435's tracked runners and final checkpoint flush
under the existing bounded shutdown deadline; test cancellation during renewal
and process replacement rather than reverting to untracked goroutines.

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

Use `CredentialsRef { kind: String!, type: String!, secretRef: SecretRef! }`
directly, with no legacy top-level name/key/namespace fields.

File GraphQL `credentialsRef: SecretRef` -> `CredentialsRef` is a breaking
contract, not an additive rename. Publish it as a Conventional Commit/PR
breaking change, apply repository release-version policy, and update development
clients and fixtures together. No compatibility release is required. Preserve all
auth, API version, Git path, File deletion and File status semantics.

ADR-0015 forbids mutating Git-backed admission: authored changes are explicit
commits, never automatic writeback. File payload facts never block
a push. ADRs 0013/0014/0016 add no IR, publication, CRD or readiness work here:
secret material cannot enter IR/snapshots/public projections; inline SecretRef
is not an owned core/CRD reference. Future webhook signing is runtime-tier
consumption, not access to a controller bootstrap identity.

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
rotation; preserve keyed/raw file/env material semantics as a static-ID
compatibility adapter under the new nested config, not legacy config names
or a claim of overlapping-key hot rotation.

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
   builds before wiring consumers.
   Before production bootstrap integration, normalize typed controller
   configuration and its producers/consumers per the configuration contract,
   with old-key rejection and env-only tests. This US2 prerequisite can proceed
   independently of US1's File metadata work.
2. **US1 / strict File contract**: require the explicit wrapper in readers,
   validators, GraphQL and JSON schema. Update development clients/fixtures.
   Bare documents fail closed; no compatibility flag or migration API ships.
3. **US2 / bootstrap**: adapt existing file/env loading to the shared interface,
   exact grammar, bounded reads, classified errors, config/mount validation and
   metrics. Preserve #429 token payloads and existing pluggable AuthN/AuthZ.
4. **US3 / rotation**: resolver-backed exchange signer, atomic key/ID bundle,
   public-key overlap and renewal/backoff tests; runtime fake-consumer rotation
   without adding a File operation.
5. **Production evidence and docs**: extend the existing lifecycle capacity
   gate, add controller restart chaos and isolated record-outage fixture,
   verify cross-replica recovery/redaction, update File and identity runbooks.

There is no preparation/strict release split. Existing Git history is not
rewritten; restored bare manifests must be edited before being submitted as
new desired state. Replica tests use the supported strict contract, not an
obsolete alpha schema. Production-scale goals are test requirements, not
claims that production installations exist.

| Verification layer | Required evidence                                                                                                                                                          |
|--------------------|----------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| Shared contracts   | Boundary lengths, traversal/URI/whitespace, explicit namespace, whole-record/item semantics, all seven failures, wrong tier and principal                                  |
| Configuration      | Nested struct decoding, every env-only leaf, TOML/env precedence, preserved defaults/units, obsolete-key rejection, shared-file sibling sections, version-matched rollback |
| Integration baseline | Current protobuf fields, required Product titles, bounded pagination, mutable ownership preservation, Admin-only endpoint/schema intent |
| Checkpoint recovery | Independent group roots, secondary-watch ownership, related replay keys, conditional duplicate-safe work and tracked final flush on shutdown |
| Material types     | `aws-access-key/v1` required keys, unsupported type, no provider call on invalid input, no typed object on error                                                           |
| API/hook           | Same invalid fixture rejected by validation and admission; no durable write; Rust receives redacted file/field reason                                                      |
| GraphQL/schema     | Nested wrapper preserved; no material field; generated schemas agree; development clients updated                                                                      |
| Identity           | Startup fails closed; no secret in config/logs; renewal resolves fresh pair; overlap with distinct key IDs; revoked/mismatched key denied                                  |
| Authorization      | Namespace isolation, denied runtime binding, owning-subject-only issuance, enabled AuthN/AuthZ providers unaffected                                                        |
| Concurrency/fault  | Single-flight, <=16 provider calls, caller cancellation, bounded retry, outage spanning token expiry, no stale fallback                                                    |
| Deployment         | Independent API/controller image builds; controller-only mounts; two replicas and replacement on the supported strict contract                                        |
| Production         | [Capacity contract](contracts/capacity-and-rollout.md), including true File pushes and provider restoration correctness                                                    |

Run Rust hook integration and the lifecycle workload with exactly one active
Git service, matching the existing gate. Repository sharding and placement-aware
routing are not implemented; disjoint volumes do not make multi-Git deployment
supported. Git HA is outside this feature, not a blocked acceptance prerequisite.

## Complexity Tracking

No constitution violation is accepted. One shared Go module is the only new
structural abstraction; it prevents divergent reference/error contracts and
cross-service `internal` imports. No additional service, database, queue,
durable cache, or production File reconciler is introduced.
