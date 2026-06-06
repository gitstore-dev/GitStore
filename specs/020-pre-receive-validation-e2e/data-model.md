# Data Model: Pre-Receive Validation End-to-End (spec#020)

## Overview

This spec introduces no new domain entities and makes no schema changes to either the memdb or ScyllaDB datastore. All changes are to CI workflow orchestration.

The relevant existing entities are documented here for reference to confirm unchanged scope.

## Existing Entities (unchanged)

### Product

Already defined in `gitstore-api/internal/datastore/entities.go`. Stored by `AdmitResources` after a successful push. Key fields relevant to this spec:

| Field | Type | Notes |
|-------|------|-------|
| `UID` | string (UUID v7) | Assigned at creation |
| `Namespace` | string | From `metadata.namespace` in pushed YAML |
| `Name` | string | From `metadata.name` in pushed YAML |
| `Spec` | `[]byte` (JSON) | Marshalled from parsed product spec |
| `Body` | string | Markdown body after the YAML front matter |
| `GitCommitSHA` | string | SHA of the push commit |
| `GitRef` | string | e.g. `refs/heads/main` |
| `Status` | `[]byte` (JSON) | `AdmissionAccepted: True` condition written at admission |

### ValidationError (proto, not persisted)

Returned by `CatalogService.ValidateResources`. Not stored in any datastore.

| Field | Type | Notes |
|-------|------|-------|
| `file_path` | string | Path of the failing product file in the push commit |
| `field` | string | Dot-path of the violating field (e.g. `spec.title`) |
| `constraint` | string | Violated rule (e.g. `max=200`, `system-managed`) |
| `message` | string | Human-readable error forwarded to `git push` output |

## CI Workflow Entities

### integration-test job (memdb)

Runs the full product-lifecycle suite with `compose.yml` (memdb backend). No changes to job logic — only adds a port-6000 readiness step.

### integration-test-scylla job (new)

New CI job. Runs the same product-lifecycle suite with `compose.yml` + `compose.scylla.yml` (ScyllaDB backend). Requires an additional wait step for ScyllaDB health before bootstrap.

## State Transitions

No new state transitions. The existing push pipeline state machine is:

```
push received
  → pre-receive: ValidateResources (blocking)
      → rejected: error returned to git client (no ref committed)
      → accepted: refs committed
          → post-receive: AdmitResources (fire-and-forget)
              → product created/updated in datastore
              → product queryable via GraphQL
```
