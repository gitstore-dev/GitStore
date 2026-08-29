# Implementation Plan: File Resource Contract

**Branch**: `051-file-resource-contract` | **Date**: 2026-08-21 | **Spec**: [spec.md](spec.md)
**Input**: Feature specification from `/specs/051-file-resource-contract/spec.md`

## Summary

Add `File` as a first-class Git-backed resource using the existing frontmatter
parser, admission pipeline, datastore model, and generic resource status/watch
contracts. The resource uses `storage.gitstore.dev/v1beta1`, stores the
author-controlled manifest and Markdown alt text separately from system-owned
status/metadata, and supports source/variant schema validation. This phase does
not fetch payloads, verify checksums, process variants, upload binaries, or add a
File-specific controller.

## Technical Context

**Language/Version**: Go 1.25  
**Primary Dependencies**: Existing frontmatter parser, `go-playground/validator`, gqlgen generic resource/status/watch schemas, go-memdb, Scylla/gocqlx, zap; no new dependency  
**Storage**: New File entity following Product/CategoryTaxonomy persistence fields; go-memdb and Scylla backends, with a forward Scylla migration  
**Testing**: Go unit, parser/validator, admission, datastore backend, generic status/watch, and GraphQL contract tests  
**Target Platform**: Linux server; Darwin/Linux development environments  
**Project Type**: Go API/admission service with Git-backed resources and dual datastore backends  
**Performance Goals**: Single-pass bounded frontmatter validation; one resource-row persistence operation per admitted File; no payload or object-storage I/O  
**Constraints**: Exact API version/kind, namespace/name identity, system-owned status/read-only metadata, immutable name/namespace/contentType, no `status.phase`, no File-specific CRUD or controller  
**Scale/Scope**: One resource kind across existing push batches, namespace/name indexed reads, and generic status/watch delivery; no full-catalog scans  
**Replica/Scaling Model**: API replicas use durable datastore identity and resource-version guarded writes; generic watch/status semantics remain duplicate-safe during rolling upgrades; no process-local File correctness state  
**Authentication/Authorization**: Existing authenticated repository/push admission boundary applies; credentials references are same-namespace metadata only and secret material is never exposed  
**Load/Backpressure Model**: Reuse existing bounded admission batch/error aggregation; datastore operations are keyed by namespace/name; no new queues, workers, retries, or external fetches

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle                                                | Gate evaluation                                                                                                           |
|----------------------------------------------------------|---------------------------------------------------------------------------------------------------------------------------|
| I. Test-First Development                                | PASS — contract, parser, admission, datastore, and generic status/watch tests are defined before implementation.          |
| II. API-First Design                                     | PASS — `contracts/file-frontmatter.md` and generic status/watch compatibility are specified first.                        |
| III. Clear Contracts & Versioning                        | PASS — `storage.gitstore.dev/v1beta1`, fixed kind, field ownership, immutability, and rollout compatibility are explicit. |
| IV. Production Observability & Debuggability             | PASS — existing structured admission/datastore errors identify resource and operation; no silent fallback is added.       |
| V. User Story Driven Development                         | PASS — parser/spec, status model, and durable admission stories have independent acceptance coverage.                     |
| VI. Independently Deployable Delivery                    | PASS — additive File recognition and nullable/forward-compatible persistence can roll across mixed API replicas.          |
| VII. Simplicity with Proven Scale                        | PASS — existing resource, status, watch, and backend abstractions are extended; no new service or queue.                  |
| VIII. Horizontally Replicable Core Services              | PASS — identity, status, and materialization are datastore-owned and resource-version guarded.                            |
| IX. Multi-User Authentication, Authorization & Isolation | PASS — existing admission authorization remains authoritative and SecretRef is namespace-scoped.                          |
| X. Production Capacity, Backpressure & Load Validation   | PASS — keyed persistence and bounded validation add no scans or external work.                                            |

**Pre-design gate result**: PASS. No violations require a complexity exception.

## Project Structure

```text
gitstore-api/
├── internal/catalog/
│   ├── file.go
│   └── status.go
├── internal/validate/
│   ├── validator.go
│   └── validator_test.go
├── internal/cataloggrpc/
│   ├── admission_operations.go
│   ├── server.go
│   └── *_test.go
└── internal/datastore/
    ├── entities.go
    ├── datastore.go
    ├── memdb/
    └── scylla/

shared/schemas/
└── schema.graphqls

specs/051-file-resource-contract/
├── plan.md
├── research.md
├── data-model.md
├── quickstart.md
└── contracts/
    └── file-frontmatter.md
```

**Structure Decision**: Extend the existing API resource stack in place. File
uses a new catalog entity and shared generic status/watch exposure rather than
a parallel parser, private metadata type, or File-only status mutation.

## Phase 0: Research Outcomes

Research is captured in [research.md](research.md). Decisions resolved:

- Add File to the existing parser, validator, admission comparison, changed-path
  materialization, and persistence dispatch.
- Persist File with the same system metadata, Git provenance, spec/body, status,
  finalizer, and deletion-timestamp fields used by current Git-backed entities.
- Do not add `generateName` until the shared metadata contract is separately
  extended; File must not fork `ObjectMeta`.
- Validate File condition types per kind because the current shared validator
  contains product-specific condition names.
- Reuse the generic status/watch contracts already on main; do not invent
  duplicate File-specific mutations in this schema phase.
- Keep checksum verification, source access, processing, upload, purge,
  fileRef back-reference checks, and controller ownership deferred.

## Phase 1: Design and Contracts

### Frontmatter and model contract

The authoritative document is [contracts/file-frontmatter.md](contracts/file-frontmatter.md).
It defines exact `apiVersion`/`kind`, writable metadata, required content/source
fields, same-namespace SecretRef, named image variants, Markdown alt text, and
forbidden system fields. `status.phase` is explicitly absent.

Add `FileResource`, `FileSpec`, `FileSourceDefinition`, `FileProcessingDefinition`,
`FileVariantRequest`, `FileStatus`, `ResolvedFileDefinition`, and `SecretRef`
using the existing YAML/JSON model conventions. Reuse current `ObjectMeta`,
including finalizers and deletion timestamp in the hydrated datastore view; do
not add File-only `generateName`.

### Admission and persistence design

- Dispatch exact `kind: File` through `ParseResource` and common pre-parse checks.
- Validate source type/URI, content type, variant names, credentials namespace,
  File condition vocabulary, and no author status/read-only metadata.
- Preserve body as alt text, including empty body.
- Add File to `ParsedResource`, `comparableForParsed`, changed-path operations,
  duplicate identity checks, create/update/delete materialization, and status
  initialization.
- Add a datastore File entity, namespace/name indexes, memdb implementation,
  Scylla model/query/migration, and read conversion. Use the existing resource
  version/generation conventions and repository-scoped owner references.
- Initialize admission status with `AdmissionAccepted=True` and `Ready=True`
  as required by the specification; leave source/processing conditions absent.
- Expose File status through the generic status/watch contract with resolved
  JSON; defer typed File CRUD/read APIs unless a later contract adds them.

### Test-first implementation order

1. Add failing frontmatter/model and contract tests.
2. Add failing parser/validator tests for all required/forbidden fields,
   namespace inheritance, body handling, source/variant constraints, and
   kind-aware conditions.
3. Add failing admission tests for materialization, identity collisions,
   path moves/deletes, status initialization, and content-type immutability.
4. Add failing memdb/Scylla contract tests and generic status/watch tests.
5. Implement models, parser dispatch, validation, persistence, and generic
   projections in dependency order.
6. Run focused suites, then `make pr-ready`.

### Post-design Constitution Check

| Principle            | Result                                                                               |
|----------------------|--------------------------------------------------------------------------------------|
| Test-First           | PASS — red-green order covers every changed layer.                                   |
| API-First            | PASS — frontmatter and generic status/watch contracts precede handlers.              |
| Clear Contracts      | PASS — ownership, status, identity, immutability, and rollout are explicit.          |
| Observability        | PASS — errors retain resource/operation context; no credentials are logged.          |
| User Story Driven    | PASS — parser/spec, status, and durable admission slices are independently testable. |
| Incremental Delivery | PASS — old replicas ignore File while new replicas add it additively.                |
| Replica Safety       | PASS — datastore identity and version guards prevent process-local divergence.       |
| Multi-User Security  | PASS — existing admission auth and namespace isolation remain enforced.              |
| Production Capacity  | PASS — keyed reads/writes and bounded parsing avoid scans and external I/O.          |
| Simplicity           | PASS — existing resource abstractions are reused.                                    |

**Post-design gate result**: PASS. No complexity exceptions required.

## Complexity Tracking

No constitution violations require justification.
