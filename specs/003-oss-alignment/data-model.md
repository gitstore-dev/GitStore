# Data Model: OSS Alignment

**Feature**: 003-oss-alignment  
**Date**: 2026-05-02

## Overview

This feature introduces no new data entities, schemas, or storage structures. The only "model" work is defining the observable behaviour at the service boundary that the new integration tests must verify. This document captures those test-boundary definitions.

## Core Service Interaction Model

The integration tests treat `gitstore-api` and `gitstore-git-service` as a black box observed from the outside. The following interactions are the testable contract between the two services.

### Interaction 1 — Valid Push → WebSocket Notification

| Attribute                | Value                                                                               |
|--------------------------|-------------------------------------------------------------------------------------|
| **Initiator**            | Test client (Go HTTP + Git protocol client)                                         |
| **Action**               | Push a valid commit to `gitstore-git-service` on `main` branch                      |
| **Observable outcome**   | `gitstore-git-service` broadcasts a WebSocket notification on `ws://localhost:8080` |
| **Notification payload** | JSON containing: `repository` (string), `ref` (string), `commit_sha` (string)       |
| **Timeout**              | 5 seconds                                                                           |

### Interaction 2 — Release Tag Push → Catalogue Reflects in GraphQL

| Attribute              | Value                                                                                                       |
|------------------------|-------------------------------------------------------------------------------------------------------------|
| **Initiator**          | Test client                                                                                                 |
| **Action**             | Push a release tag (e.g. `v0.0.1-test`) to `gitstore-git-service` after a valid product commit              |
| **Observable outcome** | `gitstore-api` GraphQL query `{ products(first: 1) { edges { node { sku } } } }` returns the pushed product |
| **Poll strategy**      | Retry up to 10 times with 1-second intervals (allow for async indexing latency)                             |
| **Timeout**            | 10 seconds total                                                                                            |

### Interaction 3 — Invalid Push → Structured Rejection

| Attribute              | Value                                                                                                        |
|------------------------|--------------------------------------------------------------------------------------------------------------|
| **Initiator**          | Test client                                                                                                  |
| **Action**             | Push a commit containing a product markdown file with invalid front-matter (e.g., non-numeric `price` field) |
| **Observable outcome** | Push is rejected with a non-zero exit code; rejection message contains the field name that failed validation |
| **No side effects**    | The invalid content does not appear in `gitstore-api` GraphQL responses                                      |

### Interaction 4 — Health Endpoints Reachable

| Attribute              | Value                                                                      |
|------------------------|----------------------------------------------------------------------------|
| **Initiator**          | Test client                                                                |
| **Action**             | HTTP GET `http://localhost:9418/health` and `http://localhost:4000/health` |
| **Observable outcome** | Both return HTTP 200 with `{ "status": "healthy" }` body                   |
| **Purpose**            | Baseline liveness check; gates all other integration test execution        |

## Test Fixture Model

| Fixture                     | Description                                                                                                                          |
|-----------------------------|--------------------------------------------------------------------------------------------------------------------------------------|
| `testCatalogRepo`           | A bare git repository initialized on the test client side, with one valid product file (`products/test-prod.md`) committed on `main` |
| `validProductFrontmatter`   | YAML front-matter with all required fields: `id`, `sku`, `title`, `price` (numeric), `currency`, `category_id`                       |
| `invalidProductFrontmatter` | Same as above but with `price: "not-a-number"` to trigger validation failure                                                         |

## State Transitions Relevant to Integration Tests

```
[clean gitstore-git-service]
        │
        ▼
[valid commit pushed] ──────────────► [WebSocket notification emitted]
        │
        ▼
[release tag pushed] ───────────────► [catalog data visible in gitstore-api GraphQL]
        │
        ▼
[invalid commit pushed] ────────────► [push rejected; catalog unchanged]
```

## No Schema Changes

The GraphQL schema in `shared/schemas/` is not modified by this feature. The integration tests query the existing schema using established operations.
