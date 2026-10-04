# Quickstart: Implement Secret Material ADRs

This is an implementation/acceptance guide, not a claim that deployment or
production capacity acceptance has passed.

### Implementation checkpoint (2026-10-03)

The shared module and foundation contracts (T001-T010) are implemented.
Standalone Go 1.25/current-toolchain race tests pass, and a short reference
fuzz run completed. Root build/test/lint aggregation includes the module.
T039's packaging was pulled forward: the original flattened controller image
failed to resolve the new local dependency, and both corrected service images
now build independently with `GOWORK=off`, preserving runtime paths.

The alpha scope correction removes the earlier preparation decoder and
migration inventory work. File now uses the strict typed wrapper directly.
There are no production installations to migrate and no migration command.
See [tasks.md](tasks.md) for verified completion and remaining work.

The active US1 slice is complete. Bare-reference/schema assertions first failed
against the preparation implementation, then passed with strict enforcement.
The shared module and API catalog, validate, GraphQL resolver, catalog-gRPC and
security packages pass `go test -race`. All nine Rust validation-handler tests
pass with the shared File fixture; the API builds independently with
`GOWORK=off go build ./...`. No File runtime operation has been introduced.

Nested configuration, bounded production bootstrap, atomic signing records,
exchange-local signers and fixed-category acquisition telemetry are implemented.
The complete controller race suite and vet checks pass, including its checkpoint
and integration packages. API enrollment, source-provenance and service-account
provider regressions pass. In-process renewal covers two independent sources,
atomic rotation, wrong key pairs, expiry-spanning outage, peer progress,
restoration and a new source. Independent API/controller image builds pass.

These are local development results on a dirty checkout, not release,
deployed two-API/two-controller or production capacity evidence. The external
integration scenarios, capacity dispatcher/verifier extension and remaining
release-evidence tasks are still open. See
[the library guide](../../docs/development/secret-material.md).

## 1. Establish the baseline

- Use branch `063-implement-secret-adrs`.
- Require the spec-061 identity plane including merged #429 on all participating
  APIs/controllers. Preserve existing public-key enrollment and RBAC.
- Read [research.md](research.md) and the [contracts](contracts/), including the
  [typed configuration contract](contracts/configuration.md).
- Recheck the current checkout before implementation; preserve unrelated
  pre-existing worktree edits rather than relying on the original session state.
- The reviewed post-plan baseline is `d065d08`. Preserve current protobuf
  fields, mutable ownership, tracked shutdown/final flush and required Product
  titles; use cursor pagination, not `totalCount`. Keep `/graphql`
  until ADR-0012's independent endpoint migration reaches its client phase.

## 2. Implement test-first in delivery order

Follow the five slices in [plan.md](plan.md). Write failing contract tests before
each implementation slice. The API and controller share contract rules through
the proposed local module; Rust continues to call the Go validation service.

Normalize controller config before extending bootstrap wiring. Use nested
service-account, secret-provider, checkpoint, reconcile and watch groups, with
one struct decode and mechanically derived environment names. Migrate all
renamed keys using the configuration contract; startup rejects old keys even
if new values override them. Update deployment/CLI/bootstrap producers and
checkpoint consumers together, preserving effective values. Rolling replacement
and rollback use binary-matched config snapshots, not an in-place rewrite of
the file still needed by old instances.

Use the root command interface:

```bash
make help
make check TARGET=config
make check TARGET=compose
make test
make build
make lint
```

During implementation, use the smallest package/test selectors for changed
Go/Rust surfaces; aggregate commands remain the repository-level gate. Add the
shared module to those aggregate commands, CI and both independent Docker
builds. Do not rely exclusively on `go.work` for a successful build.

## 3. Author strict File references

GitStore is alpha with no production deployments. File breaking changes are
allowed until Release Candidate; no preparation release, compatibility schema,
audit or write freeze is necessary.

Use an explicit credential type in development manifests:

```yaml
source:
  type: s3
  uri: s3://catalog-assets/products/product-hero.jpg
  credentialsRef:
    kind: CredentialsRef
    type: aws-access-key/v1
    secretRef:
      kind: SecretRef
      name: catalog-assets-writer
```

The optional key is omitted because this type needs a multi-item record.
Provision material out of band; this manifest contains no provider or values.
Push through normal validation/admission and verify nested metadata from both
API replicas. Do not directly edit projection rows.

GraphQL clients select
`credentialsRef { kind type secretRef { kind name key namespace } }`.
Bare references and old top-level `name` selections are rejected. History is
not rewritten; restored old manifests must be edited before admission.

## 4. Prove bootstrap and rotation

Keep private key material in a controller-only source, never the shared config
file. For hot rotation, select the `json-record` file binding and a
whole-record keyRef; provision the atomic signing bundle outside GitStore.
Raw/static-ID material remains supported under the new nested config names,
but is not sufficient evidence for overlapping-key rotation.

Start the local stack using its existing deployment procedure:

```bash
make compose
```

Verify startup fails closed for an absent/malformed/oversized record and that a
valid record yields an authenticated ready controller. Do not print token
responses to the terminal or capture them in committed evidence.

Enroll a second public key under a different ID, atomically replace the same
logical signing record, and observe renewal without a manifest/keyRef change.
Retire the old enrolled key only after every replica has transitioned and the
assertion/skew overlap window is satisfied.

## 5. Verify failure, isolation and recovery

Exercise all seven error classes and context cancellation in shared contract
tests. Test whole-record and selected-item semantics, wrong-tier requests,
foreign namespaces, unsupported credential types and oversize/count limits.
Use a contract consumer to prove runtime resource rotation and blocked
operations; no production File runtime status is expected.

In deployed tests, prove wrong-subject issuance is denied, shared identity
mounts fail managed checks, no token or key leaks into any telemetry, and
replacement controllers re-bootstrap without manual token injection.

The new scenario is recognized but currently fails explicitly before starting
a stack or workload. Do not remove that guard until the File workload and
scheduled fault verifier are connected; running the old Repository-only
profile with an ignored flag is not spec-063 evidence. After implementing it:

```bash
make capacity TARGET=repository PROFILE=lifecycle MODE=diagnostic \
  REPOSITORY_CAPACITY_SECRET_SCENARIO=1
```

Then use alpha for local bounded evidence and production only with the complete
external topology/dataset and sanitized manifests defined in the
[capacity contract](contracts/capacity-and-rollout.md). Diagnostic cannot pass;
alpha is not a production claim. Use `make chaos` only against the explicit
test-owned controller container, with confirmation.

## 6. Complete documentation and readiness

### Acceptance additions (2026-10-03)

- `make test` includes negative deployment-isolation fixtures through
  `scripts/test-secret-config.sh`; `make check TARGET=compose` checks the real
  JSON-rendered Compose topology, including per-consumer read-only key mounts.
- Shared runtime tests exercise atomic file replacement after an outage and
  reject partial credentials before dependent work. An actual subprocess
  proves an environment replacement does not alter the old process's values.
  The complete shared race suite also passed with `GOWORK=off` on Go 1.25.0.
- Checkpoint tests preserve group/replica-specific snapshots, cursors, pending
  and related replay keys; main registration tests inspect the final flushed
  checkpoint after cancellation.
- `make test TARGET=secret-integration` runs the real two-API/two-controller bootstrap
  and rotation harness. See the [rotation runbook](../../docs/runbooks/secret-material-rotation.md)
  for test-deployment ownership, 60-second API TTL, identity and token-file
  prerequisites. Compilation or a skipped test is not deployed evidence.
- `make pr-ready` passed after removing deprecated mutation of ECDSA key
  internals under Go 1.26. Parsed ECDSA references are dropped; no guaranteed
  Go-memory erasure is claimed. Shared-module race coverage was 86.1%.

### Readiness review (2026-10-03)

All three functional stories have acceptance evidence. The expanded deployed
bootstrap/rotation run passed in 293.56s, with 6.103361708s recovery after the
90s isolated record outage. It covered mismatched-record rejection, peer
authenticated availability, key overlap/retirement and fresh process bootstrap.
The topology was two API processes, two controller processes, three Scylla
nodes and exactly one Git service. No controller leases, File runtime consumer
or Git sharding was introduced.

Local sanitized evidence: `.gitstore/capacity/secret-functional/20261003/`.
`summary.json` SHA-256:
`f3c2535abeb8f815fe5b4aba1cae169a328d9dca833b1b2fb8c1cc158e797e3b`.
`artifacts.json` SHA-256:
`5e45d1dc5fdfa9fd941429cd34c95d9452a4d6812142a796dbe03014fbb9e3b4`.
The summary includes digests of the full PR-readiness and deployed-test logs.
Builds use the current dirty `d065d08` checkout; no release provenance is claimed.
Independent API/controller image builds completed with IDs
`sha256:bdbc9e9630c04eefc8508529337a169cbdef3c29f2ec479df1ebdee507e9c04e`
and `sha256:09f7f28f195280431a2b8588259c842f54c111c6e1f56a5ac15af22d1d2636dd`.
The deployed image IDs are separately recorded in `artifacts.json`.

`make test` also runs the production secret-capacity assertion matrix. It
rejects reduced load, missing offline dataset proof, missing real File-push
observations, incomplete per-process measurements, multi-Git topology, absent
fault/recovery proofs and exact threshold violations. These typed unit fixtures
establish verifier rules, not measurements of a deployed capacity workload.

T050's shell and file-based evidence rejection tests now pass through
`make test`, including race testing. The reusable loader
strictly decodes a complete JSON observation bundle, verifies actual artifact
sizes/digests and process coverage, and scans bounded files for credential
patterns/private markers without echoing content. These helpers are connected to
the collector and final whole-run scan required by T055.
No additional deployment or production-capacity evidence was generated.

The guarded lifecycle path now includes the bounded real Git File-push kernel,
cross-API typed projection checks, local regular-file resolution and measured
32-caller/16-slot contention, deadline and denial probes. It writes separate
`secret/file-workload.json` component observations, never a passing scenario
envelope. New controller exchange metrics distinguish successful issuance from
acquisition and expose peak inflight/post-jitter retry delay. Local bare-Git,
provider, scheduler and controller race suites passed; no new deployed capacity
run has been performed.

Offline dataset verification is now connected before workload resource creation:
the root Makefile exports `REPOSITORY_CAPACITY_SECRET_DATASET_MANIFEST`,
`REPOSITORY_CAPACITY_SECRET_DATASET_NAMESPACE` and
`REPOSITORY_CAPACITY_SECRET_DATASET_PAGE_SIZE`. The bounded verifier checks
acknowledged titled Products against both APIs, emits `secret/dataset.json`,
and rejects undersized production fixtures. Process-identified controller
snapshots also record actual authentication/reconciliation progress before and
after load. These are guarded component observations, not evidence of a
five-million-row deployment or scheduled fault recovery.

**Implementation checkpoint:** The owned stack, minute-15 outage, minute-30
rotation/retirement and minute-45 confirmed controller restart are connected.
Resource accounting covers original/replacement Go processes and singleton Git;
both RSS segments use the original warmed baseline. Bounded process logs and
metrics include both replacements. The dispatcher closes verifier/postflight
writers before assembling, hashing and scanning `secret-evidence.json`; changed
source state, failed components, missing artifacts or contamination prevent a pass.
Component-to-bundle positive/negative fixtures and the full `make pr-ready`
workflow pass. The code graph was refreshed (18 pre-existing inputs still yield
no nodes).

The complete #439 branch/worktree redundancy review retained required generated,
service-adapter and layered-test surfaces. It removed an unused exported raw-env
provider wrapper, a duplicate controller configuration table, unrelated TODO-only
changes and stale preparation/implementation wording. The wrapper's mapping and
case-fold rejection coverage remains in the shared-module race suite. Capacity
helpers reuse existing files and Compose configuration; no second capacity test
selector or command path was retained.

**T058 remains blocked, not passed:** No new deployment, five-million-Product
fixture or full scheduled capacity run was performed. Production requires a clean
committed verifier, matching release images, an explicitly owned two-API/
two-controller/Scylla/singleton-Git deployment, an acknowledged dataset manifest
and explicit `CHAOS_CONFIRM=1` approval. Local commit, isolated preparation and
the full fault run are now authorized; no production bundle exists yet.
Diagnostic and functional results cannot satisfy this gate.

Update File storage/lifecycle docs, controller identity enrollment/rotation
runbooks, provider-format/config docs, root command help and agent command
reference. Preserve the explicit deferrals: no production File consumer,
payload retrieval, object-store writes, remote provider SDK or JWT/HMAC config
migration.

Before opening a PR:

```bash
make pr-ready
```

Include failing-then-passing test evidence, the alpha breaking-change notice,
deployed outage/rotation/replacement proof and the declared capacity evidence.
