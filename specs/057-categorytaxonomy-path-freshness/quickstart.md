# Quickstart: validating spec 057 locally

Prerequisites: `make compose`, or `make compose DATASTORE=scylla` for the index and repair checks, plus `make bootstrap TARGET=all ADMIN_PASSWORD=…`. `NS` is the bootstrap namespace (default `gitstore-test`); `$TOKEN` is the cached bootstrap token.

```bash
gql() { curl -s -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "$(jq -n --arg q "$1" '{query:$q}')" http://localhost:4000/graphql | jq; }
```

## 1. Create a tree through the API (User Story 4)

```bash
for c in 'electronics:' 'computers:electronics' 'laptops:computers' 'computers-refurb:electronics'; do
  name=${c%%:*}; parent=${c#*:}
  ref=$([ -n "$parent" ] && echo ", parentRef: { name: \"$parent\" }")
  gql "mutation { createCategory(input: { metadata: { namespace: \"$NS\", name: \"$name\" },
       spec: { title: \"$name\" $ref } }) { category { metadata { name } } } }"
done
# gitstore-system history now shows 4 "Create CategoryTaxonomy <name>" commits (clone it or use `git log` on the bare repo under GIT_DATA_DIR)
```

Expected: each call returns the category, and `status.resolved` is `null` until the controller reconciles it.

## 2. Hierarchy and subtree filter (User Stories 1, 7, 8)

```bash
gql "{ categories(namespace: \"$NS\", filter: { descendantOf: \"computers\", includeSelf: true }) {
       edges { node { metadata { name } status { resolved { path depth } } } } } }"
```

Expected: `computers` (depth 0) and then `laptops`. `computers-refurb` must not appear.

```bash
gql "{ category(by: { namespacePath: { namespace: \"$NS\", name: \"computers\" } }) { parent { metadata { name } } children { metadata { name } } } }"
```

Expected: parent `electronics`, children `[laptops]`.

## 3. Re-parent and cascade (User Story 5)

```bash
gql "mutation { updateCategory(input: { metadata: { namespace: \"$NS\", name: \"laptops\" },
     spec: { title: \"Laptops\", parentRef: { name: \"computers-refurb\" } } }) { category { metadata { name } } } }"
```

Expected:
- after the cascade, `laptops.status.resolved.path = [electronics, computers-refurb, laptops]`;
- the `computers` subtree no longer contains it;
- the Markdown body is unchanged.

## 4. Diagnostics (FR-020)

```bash
gql "mutation { createCategory(input: { metadata: { namespace: \"$NS\", name: \"loop\" },
     spec: { title: \"x\", parentRef: { name: \"loop\" } } }) { category { metadata { name } } } }"
```

Expected:
- `errors[0].extensions.code = ADMISSION_REJECTED`, `phase = PRE_RECEIVE`, `commit = null`;
- the diagnostic `field` is `spec.parentRef.name`;
- no new commit.

Push the same manifest with `git push` and compare the `ng` reason text with `errors[0].message`; they must be identical.

## 5. Git-backed delete (User Story 6)

```bash
gql "mutation { deleteCategory(input: { id: \"<electronics id>\" }) { outcome } }"   # FAILED_PRECONDITION: child categories present
gql "mutation { deleteCategory(input: { id: \"<laptops id>\" }) { outcome category { metadata { name } } } }"  # TERMINATION_STARTED
gql "mutation { deleteCategory(input: { id: \"<laptops id>\" }) { outcome } }"   # ALREADY_TERMINATING, no commit
```

## 6. Removed fields

```bash
gql "{ categories(namespace: \"$NS\") { edges { node { path } } } }"   # validation error: Cannot query field "path"
```

## 7. Scylla repair and backfill (FR-028, FR-029)

```bash
gitctl scylla-projection-audit --hosts localhost:9042 --keyspace gitstore      # reports category_ancestor_index findings
gitctl scylla-projection-repair --hosts localhost:9042 --keyspace gitstore --dry-run
gitctl scylla-projection-repair --hosts localhost:9042 --keyspace gitstore --confirm
```

## 8. Test suites

```bash
make test                                   # unit + resolver + security + cataloggrpc
make test TARGET=datastore                  # memdb ancestor-index contracts
make test TARGET=datastore DATASTORE=scylla SCYLLA_TEST_ADDR=localhost:9042
make capacity TARGET=category PROFILE=hierarchy MODE=diagnostic
```
