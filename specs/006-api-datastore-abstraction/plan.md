# Implementation Plan: API Datastore Abstraction

**Branch**: `006-api-datastore-abstraction` | **Date**: 2026-05-09 | **Spec**: [spec.md](spec.md)
**Input**: Feature specification from `/specs/006-api-datastore-abstraction/spec.md`

## Summary

Introduce a pluggable `Datastore` interface inside `gitstore-api` that decouples all catalogue persistence paths from their storage technology. Two initial backends are delivered: a `go-memdb`-backed in-memory store for local development and testing, and a ScyllaDB store for production workloads. The active backend is selected at startup via a single configuration value (`GITSTORE_DATASTORE_BACKEND`) with no code changes required to switch between them.

## Technical Context

**Language/Version**: Go 1.25 (`gitstore-api`)
**Primary Dependencies**:
- `github.com/hashicorp/go-memdb` v1.3.x — in-memory backend (new)
- `github.com/gocql/gocql` v1.7.x → `replace github.com/scylladb/gocql v1.18.0` — ScyllaDB driver (new)
- `github.com/scylladb/gocqlx/v3` v3.0.x — ScyllaDB session helper + migration runner (new)
- `github.com/prometheus/client_golang` v1.23.x — metrics (already present)
- `go.uber.org/zap` v1.28.x — structured logging (already present)
- `github.com/spf13/viper` v1.21.x — config (already present)
- `github.com/google/uuid` v1.6.x — ID generation (already present)
- `github.com/testcontainers/testcontainers-go` v0.42.x — ScyllaDB integration tests (already present)

**Storage**: `go-memdb` (in-memory backend) / ScyllaDB 5.x+ (production backend)
**Testing**: `go test` — unit (no build tag), ScyllaDB integration (`-tags scylla` via testcontainers-go)
**Target Platform**: Linux server (production), macOS (local development)
**Project Type**: Internal library within the `gitstore-api` web service
**Performance Goals**: < 500ms p95 catalogue reads (existing SLA); memdb reads < 1ms; ScyllaDB reads < 50ms p99
**Constraints**: No retry/reconnect inside the abstraction (FR-007a); migration serialised via LWT distributed lock (FR-008a); config keys follow feature 005 `GITSTORE_*` prefix convention
**Scale/Scope**: Up to 10,000 catalogue entities; single keyspace; 1–5 concurrent API instances

## Constitution Check

| Principle                | Status | Notes                                                                                                                                        |
|--------------------------|--------|----------------------------------------------------------------------------------------------------------------------------------------------|
| I. Test-First            | ✅ PASS | Contract tests written before any backend implementation; memdb and ScyllaDB test files verified failing first                               |
| II. API-First            | ✅ PASS | `Datastore` interface defined in `contracts/datastore.go` before any backend code                                                            |
| III. Clear Contracts     | ✅ PASS | Interface stabilised before backends; sentinel errors (`ErrNotFound`, `ErrAlreadyExists`) are versioned with the module                      |
| IV. Observability        | ✅ PASS | `InstrumentedDatastore` decorator emits `HistogramVec` latency + `CounterVec` error-rate + zap error logs (FR-011a, FR-011b)                 |
| V. User Story Driven     | ✅ PASS | All tasks labelled US1–US4 with P1→P3 priority order                                                                                         |
| VI. Incremental Delivery | ✅ PASS | P1 (config-switchable backend factory) → P2a (memdb backend) → P2b (ScyllaDB backend) → P3 (parity tests)                                    |
| VII. Simplicity          | ✅ PASS | Exactly two backends; no retry inside abstraction; no multi-tenant scope; decorator for instrumentation instead of embedding in each backend |

*No violations requiring justification.*

## Project Structure

### Documentation (this feature)

```text
specs/006-api-datastore-abstraction/
├── plan.md              # This file (/speckit.plan command output)
├── research.md          # Phase 0 output
├── data-model.md        # Phase 1 output
├── quickstart.md        # Phase 1 output
├── contracts/           # Phase 1 output
│   └── datastore.go     # Go Datastore interface (canonical contract)
└── tasks.md             # Phase 2 output (/speckit.tasks command — NOT created by /speckit.plan)
```

### Source Code (repository root)

```text
gitstore-api/
├── internal/
│   ├── config/
│   │   └── config.go                       # Add DatastoreConfig + ScyllaConfig structs
│   └── datastore/
│       ├── datastore.go                    # Datastore interface + domain types + sentinel errors
│       ├── factory.go                      # NewDatastore(cfg) → Datastore factory + validation
│       ├── instrumented.go                 # InstrumentedDatastore decorator (metrics + logging)
│       ├── instrumented_test.go            # Unit tests for the decorator
│       ├── memdb/
│       │   ├── schema.go                   # go-memdb table/index schema definitions
│       │   ├── backend.go                  # memdbDatastore — implements Datastore
│       │   └── backend_test.go             # Unit tests (no build tag; no external deps)
│       └── scylla/
│           ├── backend.go                  # scyllaDatastore — implements Datastore
│           ├── migration.go                # Migration runner + LWT distributed lock
│           ├── migrations/
│           │   ├── 001_initial_schema.cql  # Keyspace + tables + lock table
│           │   └── 002_add_indexes.cql     # Lookup tables for SKU / slug / category queries
│           └── backend_test.go             # Integration tests (build tag: scylla)
└── tests/
    └── contract/
        └── datastore/
            ├── contract_test.go            # Backend-agnostic CRUD contract suite
            ├── memdb_test.go               # Runs contract suite against memdb (no build tag)
            └── scylla_test.go              # Runs contract suite against ScyllaDB (build tag: scylla)
```

**Structure Decision**: Single-service layout within `gitstore-api/internal/datastore/`. Backends are sub-packages to keep the interface clean and prevent circular imports. Contract tests in `tests/contract/datastore/` mirror the existing `tests/contract/` location. No new top-level service or module is introduced.

## Complexity Tracking

> No constitution violations requiring justification.
