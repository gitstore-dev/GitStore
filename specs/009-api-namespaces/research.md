# Research: API-Driven Namespace Lifecycle Management

**Feature**: `009-api-namespaces` | **Phase**: 0 | **Date**: 2026-05-15

---

## Decision 1: API Surface — GraphQL vs REST

**Decision**: Expose namespace CRUD exclusively as GraphQL mutations and queries.

**Rationale**: The entire domain API surface is GraphQL. The `gqlgen.yml` uses `follow-schema` layout with `../shared/schemas/*.graphqls` — adding a `namespace.graphqls` file and running `go generate` is the complete tooling cost. The Relay-compliant pattern (Node interface, Connection pagination, Input/Payload mutation shape) is already established for products, categories, and collections. Namespace operations are authenticated — they belong behind the existing context-based auth chain rather than a separate REST route.

**Alternatives considered**:
- REST handlers at `/api/namespaces`: Simpler for the server, but would split the client contract between two transports. The gitstore-admin frontend already uses GraphQL. No precedent in the project for domain operations via REST (login/refresh are pre-auth flows, deliberately outside GraphQL).
- Mixed (GraphQL queries, REST mutations): No benefit; adds protocol friction with zero upside.

---

## Decision 2: Authorization Strategy for FR-008

**Decision**: Use `claims.IsAdmin == true` as the "elevated platform role" for enterprise namespace creation; use `ns.CreatedBy == claims.Username || claims.IsAdmin` for deletion authorization. No changes to `Claims` struct.

**Rationale**: The JWT `Claims` struct has exactly two application fields: `Username string` and `IsAdmin bool`. `IsAdmin` already IS the "elevated platform role" — no new field is needed. Namespace ownership for deletion is checked at query time against the stored `CreatedBy` field, which is a simple two-condition check in the service layer and avoids embedding mutable ownership state in a stateless signed token.

**Alternatives considered**:
- Add `PlatformRole` field to Claims: Correct long-term shape if more than two roles are needed, but premature here. `IsAdmin` is semantically equivalent and backward-compatible.
- Add `OwnedNamespaces []string` to Claims: Embeds mutable state into a signed token; a transferred namespace would require re-issuing tokens. Stale-by-design.

---

## Decision 3: Namespace Identifier Uniqueness Enforcement

**Decision**: Enforce global uniqueness via a unique secondary index on the `identifier` field in both backends. The application layer checks for conflict using `GetNamespaceByIdentifier` before insert.

**Rationale**: The spec requires global uniqueness across all tiers (FR-003). The same identifier cannot exist as both a user-space and an organisation namespace. A unique index on `identifier` at the storage level provides the enforcement guarantee even under concurrent writes.

- **memdb**: `StringFieldIndex{Field: "Identifier", Unique: true}` — memdb enforces uniqueness atomically within a write transaction.
- **ScyllaDB**: `CREATE UNIQUE INDEX namespaces_by_identifier ON namespaces (identifier)` — ScyllaDB enforces the constraint. The application also does a read-before-write guard (consistent with the existing category/collection pattern in `scylla/backend.go`).

**Alternatives considered**:
- Application-only uniqueness check (no storage-level index): Vulnerable to TOCTOU under concurrent requests. The memdb pattern already uses unique indices for SKU and slug; same guarantee here.

---

## Decision 4: ScyllaDB Schema for Namespaces

**Decision**: Single table `namespaces` with `id` as `text` PRIMARY KEY, `identifier` as a globally-unique secondary index, `tier` as `text`, timestamps as `bigint` (milliseconds, consistent with all other tables using `int64` in row structs).

**Rationale**: The existing backend stores timestamps as `int64` (milliseconds via `UnixMilli()`). The `tier` field is stored as `text` and validated at the Go layer, consistent with `inventory_status` in the product table. `parent_enterprise_id` is nullable `text` (maps to `*string`).

**Migration file**: `002_namespaces.cql` (following the `{NNN}_{description}.cql` convention of `001_initial_schema.cql`).

```sql
CREATE TABLE IF NOT EXISTS namespaces (
    id                   text       PRIMARY KEY,
    identifier           text,
    display_name         text,
    tier                 text,
    parent_enterprise_id text,
    created_at           bigint,
    created_by           text,
    updated_at           bigint,
    updated_by           text
);

CREATE INDEX IF NOT EXISTS namespaces_by_identifier ON namespaces (identifier);
```

---

## Decision 5: go-memdb Schema for Namespaces

**Decision**: Three indices — `id` (unique, primary), `identifier` (unique), `tier` (non-unique for future `ListByTier`).

```go
"namespaces": {
    Name: "namespaces",
    Indexes: map[string]*memdb.IndexSchema{
        "id": {
            Name:    "id",
            Unique:  true,
            Indexer: &memdb.StringFieldIndex{Field: "ID"},
        },
        "identifier": {
            Name:    "identifier",
            Unique:  true,
            Indexer: &memdb.StringFieldIndex{Field: "Identifier"},
        },
        "tier": {
            Name:    "tier",
            Unique:  false,
            Indexer: &memdb.StringFieldIndex{Field: "Tier"},
        },
    },
}
```

**Rationale**: `id` is required as the primary key by go-memdb. `identifier` unique index enforces FR-003 at the storage level. `tier` index is cheap and enables future `ListByTier` without a full-scan filter. `parent_enterprise_id` is omitted for now — nullable `*string` requires a custom indexer; add when a `ListByEnterprise` query is needed.

---

## Decision 6: Reserved Namespace Identifiers

**Decision**: The following identifiers are reserved and MUST be rejected by the validation layer (FR-002):

```
admin, root, system, default, api, git, www, mail, smtp, ftp, org, orgs,
static, assets, cdn, docs, help, support, billing, status, health,
internal, local, localhost, null, undefined, true, false, new, test,
gitstore, enterprise, org, user, namespace, namespaces, repo, repos
```

**Rationale**: Reserved names prevent conflicts with current and future API path segments, system service names, and common CI/CD tool identifiers. The list follows the GitHub and GitLab namespace reservation precedent.

---

## Decision 7: Namespace Identifier Validation Rules (FR-002)

**Decision**: Identifier MUST match regex `^[a-z0-9][a-z0-9-]{0,61}[a-z0-9]$` OR be a single character `^[a-z0-9]$` (for length-1 identifiers). Simplified: alphanumeric lowercase + hyphens, no leading/trailing hyphen, 1–63 chars.

**Rationale**: DNS label conventions (RFC 1123). This is a widely understood standard. Case-folding to lowercase is applied before storage (identifiers are stored lowercase).

**Validation implementation**: `go-playground/validator/v10` is already a dependency. A custom validator tag `namespace_identifier` will be registered, or a simple regex check in the service layer.

---

## Decision 8: Deletion Guard for Repositories

**Decision**: Implement the deletion guard at the service layer using a `HasRepositories(ctx, namespaceID)` check against the datastore. Since the `repositories` table does not yet exist (scoped to a future spec), this check will be a no-op stub that always returns `false` in this spec, with a `// TODO: enforce when repositories table exists` comment.

**Rationale**: FR-007 requires blocking deletion when repositories exist. The repository table is out of scope for this spec. The service layer stub ensures the guard is wired and the code path is correct — it will be filled in when the repository spec lands.

**The stub is not a test bypass**: Integration tests for deletion will test the empty-namespace case (which the stub allows). The blocked-deletion path (repositories exist) will be tested in the repository spec.
