# Research: Namespace Types — Remove Enterprise

**Branch**: `030-remove-enterprise-namespace` | **Date**: 2026-06-19

## Q1 — What is the full surface area of `enterprise` in the codebase?

**Decision**: The `enterprise` namespace type lives entirely in `gitstore-api` (Go). The git service (Rust) and proto definitions have zero knowledge of namespace types.

**Rationale**: The git service operates only on `repo_id` — namespace resolution is done upstream. This scopes all changes to one service.

**Affected files (authoritative list)**:

| File                                                                                  | Nature of change                                                                                                                                                                                                                                     |
|---------------------------------------------------------------------------------------|------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `shared/schemas/namespace.graphqls`                                                   | Remove `ENTERPRISE` from `NamespaceTier` enum; rename `ORGANISATION` → `ORGANIZATION`; remove `parentEnterpriseId` field from `Namespace` type; remove `parentEnterpriseIdentifier` from `CreateNamespaceInput`; update mutation doc string          |
| `gitstore-api/internal/datastore/entities.go`                                         | Remove `NamespaceTierEnterprise` constant; rename `NamespaceTierOrganisation` → `NamespaceTierOrganization`; remove `ParentEnterpriseID *string` from `Namespace` struct                                                                             |
| `gitstore-api/internal/datastore/scylla/models.go`                                    | Remove `ParentEnterpriseID *string` from `namespaceRow`; remove from column list                                                                                                                                                                     |
| `gitstore-api/internal/datastore/scylla/backend.go`                                   | Remove `"parent_enterprise_id"` from column list                                                                                                                                                                                                     |
| `gitstore-api/internal/datastore/scylla/migrations/004_drop_parent_enterprise_id.cql` | New migration: `ALTER TABLE namespaces DROP parent_enterprise_id;`                                                                                                                                                                                   |
| `gitstore-api/internal/graph/resolver/service.go`                                     | Remove enterprise-tier admin gate (lines 239–258); remove `parentEnterpriseID` variable and all enterprise tier logic; remove `"enterprise"` from `reservedIdentifiers`; update `datastoreNamespaceTierFromModel` to use `NamespaceTierOrganization` |
| `gitstore-api/internal/graph/resolver/converters.go`                                  | Remove `ParentEnterpriseID` mapping in `datastoreNamespaceToModel` (lines 30–34, 40); update `datastoreNamespaceTierToModel` to map `NamespaceTierOrganization` → `model.NamespaceTierOrganization` (lines 620–621)                                  |
| `gitstore-api/internal/graph/model/models_gen.go`                                     | Regenerated — do not edit by hand; run `make generate`                                                                                                                                                                                               |
| `gitstore-api/internal/graph/generated/namespace.generated.go`                        | Regenerated — do not edit by hand                                                                                                                                                                                                                    |
| `gitstore-api/internal/graph/generated/root_.generated.go`                            | Regenerated — do not edit by hand                                                                                                                                                                                                                    |
| `gitstore-api/gqlgen.yml`                                                             | Remove commented-out enterprise line                                                                                                                                                                                                                 |
| `gitstore-api/internal/graph/resolver/namespace_service_test.go`                      | Remove `TestCreateNamespace_enterpriseTier_withoutAdmin_denied` and `TestCreateNamespace_enterpriseTier_withAdmin_succeeds`; add `TestCreateNamespace_enterpriseTier_rejected` regression test                                                       |
| `gitstore-api/tests/contract/datastore/contract_test.go`                              | Remove enterprise namespace fixture (line 514)                                                                                                                                                                                                       |
| `docs/architecture.md`                                                                | Update namespace tier table, remove `parentEnterpriseId` from examples                                                                                                                                                                               |
| `docs/api-reference.md`                                                               | Remove `ENTERPRISE` from tier table, remove `parentEnterpriseIdentifier` input docs, remove `parentEnterpriseId` field                                                                                                                               |
| `docs/resources/git-backed.md`                                                        | Remove `parentEnterpriseRef` from namespace resource spec                                                                                                                                                                                            |

**Alternatives considered**: Soft-deprecate (keep enum value, add a warning on use) — rejected because the spec explicitly states no production data contains enterprise namespaces and the type was never in a stable public release.

---

## Q2 — Do generated Go files need manual edits or only `make generate`?

**Decision**: Generated files (`models_gen.go`, `namespace.generated.go`, `root_.generated.go`) MUST NOT be edited by hand. Run `make generate` (or the equivalent `go generate ./...` in `gitstore-api/`) after updating the `.graphqls` schema. The generation step is a task, not a manual edit task.

**Rationale**: `models_gen.go` is produced by gqlgen from the GraphQL schema. Hand-editing it would be overwritten on the next `make generate` run and is an anti-pattern.

---

## Q3 — Does `parent_enterprise_id` need a Scylla migration?

**Decision**: Yes. A new CQL migration `004_drop_parent_enterprise_id.cql` drops the column with `ALTER TABLE namespaces DROP parent_enterprise_id;`.

**Rationale**: The column serves no purpose once the enterprise tier is removed. Keeping it as dead schema creates confusion for future developers. The user has confirmed no production data depends on this column, making the drop safe. ScyllaDB supports `ALTER TABLE DROP` natively and the operation does not require a full table rewrite.

**Alternatives considered**: Retain the column as a silent nullable — rejected by the user ("remove it in place, I don't need it").

---

## Q6 — Should `organisation` → `organization` (American spelling) apply to stored values too?

**Decision**: The stored datastore string constant (`"organisation"` in `entities.go`) is kept as-is. Only the user-facing layer changes: the GraphQL enum value changes from `ORGANISATION` to `ORGANIZATION`, and the Go constant is renamed from `NamespaceTierOrganisation` to `NamespaceTierOrganization` for consistency. No data migration is needed for existing rows.

**Rationale**: Changing the stored string value from `"organisation"` to `"organization"` would require a full-table UPDATE across all existing namespace rows in Scylla (which has no free WHERE-scan on a non-partition-key column). Keeping the stored value avoids that complexity. The converter layer maps the stored `"organisation"` string to the `ORGANIZATION` GraphQL enum value, so the user-facing API is entirely American-spelled.

**Alternatives considered**: Change stored value too (requires a Scylla data migration UPDATE across all rows by bucket) — deferred; can be done as a separate housekeeping spec if desired.

---

## Q4 — Does the `parentEnterpriseId` GraphQL field removal break existing clients?

**Decision**: Removing the field is safe. The spec assumption is that `enterprise` was never in a stable public release. Per Constitution Principle III (Clear Contracts & Versioning), removing a field that was never shipped in a stable version is not a breaking change.

**Rationale**: The issue explicitly states: "Enterprise membership should not be represented as a namespace type." No currently-supported client depends on this field.

---

## Q5 — What is the correct regression test shape?

**Decision**: Replace the two existing enterprise tests with a single test `TestCreateNamespace_enterpriseTier_rejected` that:
1. Calls `CreateNamespace` with `tier: ENTERPRISE` and `isAdmin: true`
2. Asserts a validation error is returned
3. Asserts the error message names `enterprise` as the invalid value

This covers both the old "without admin" and "with admin" paths since neither should succeed after this change.

**Rationale**: A single well-named rejection test is clearer than two tests that test a permission gate which no longer exists. The regression test must ensure that even an admin cannot create an enterprise namespace.
