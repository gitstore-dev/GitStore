# Quickstart: Product Category and Readiness Reconciliation

Manual verification of the two primary convergence paths (User Story 1 and
User Story 2) against a local `make compose` stack.

## Prerequisites

```bash
make compose
make bootstrap TARGET=all
```

## 1. Category already exists — Product becomes Ready immediately

```bash
# CategoryTaxonomy has no GraphQL create mutation — push it to the
# namespace's repository, matching every other Git-backed resource.
mkdir -p categories
cat > categories/laptops.md <<'EOF'
---
apiVersion: catalog.gitstore.dev/v1beta1
kind: CategoryTaxonomy
metadata:
  name: laptops
spec:
  title: Laptops
---
EOF
git add categories/laptops.md
git commit -m "Add laptops category"
git push origin main

# Then create a Product referencing it.
curl -s $API_URL/graphql -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{
  "query": "mutation($ns: String!) { createProduct(input: { apiVersion: \"catalog.gitstore.dev/v1beta1\", kind: \"Product\", metadata: { namespace: $ns, name: \"macbook-pro\" }, spec: { title: \"MacBook Pro\", categoryRef: { kind: \"CategoryTaxonomy\", name: \"laptops\" } } }) { product { id status { conditions { type status reason } } } } }",
  "variables": { "ns": "gitstore-test" }
}'
```

Poll the Product until `CategoryResolved=True` and `Ready=True`:

```bash
curl -s $API_URL/graphql -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{
  "query": "query($ns: String!) { product(by: { namespacePath: { namespace: $ns, name: \"macbook-pro\" } }) { status { conditions { type status reason } resolved { category { name uid } } } } }",
  "variables": { "ns": "gitstore-test" }
}'
```

Expected: `conditions` includes `{type: "CategoryResolved", status: "TRUE", reason: "CategoryFound"}` and `{type: "Ready", status: "TRUE"}`; `resolved.category.uid` is a non-empty opaque id (same shape as the CategoryTaxonomy's own `id`), not a raw UUID.

## 2. Category created after the Product — watch-driven convergence

```bash
# Create the Product first, referencing a category that does not exist yet.
curl -s $API_URL/graphql -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{
  "query": "mutation($ns: String!) { createProduct(input: { apiVersion: \"catalog.gitstore.dev/v1beta1\", kind: \"Product\", metadata: { namespace: $ns, name: \"early-bird\" }, spec: { title: \"Early Bird\", categoryRef: { kind: \"CategoryTaxonomy\", name: \"gadgets\" } } }) { product { id } } }",
  "variables": { "ns": "gitstore-test" }
}'
```

Query the Product's status — expect `CategoryResolved=False`/`CategoryNotFound`, `Ready=False`. Then push the matching category:

```bash
cat > categories/gadgets.md <<'EOF'
---
apiVersion: catalog.gitstore.dev/v1beta1
kind: CategoryTaxonomy
metadata:
  name: gadgets
spec:
  title: Gadgets
---
EOF
git add categories/gadgets.md
git commit -m "Add gadgets category"
git push origin main
```

Re-query the Product within a few seconds without touching it again: expect `CategoryResolved=True`/`Ready=True`, converged by the watch-driven re-enqueue (FR-009), not by a new Product push.

## 3. Verify unrelated state is untouched

```bash
# CategoryTaxonomy product counts (spec 042) must be unaffected by this feature.
curl -s $API_URL/graphql -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{
  "query": "query($ns: String!) { category(by: { namespacePath: { namespace: $ns, name: \"laptops\" } }) { status { resolved { productCount } } } }",
  "variables": { "ns": "gitstore-test" }
}'
```

Expected: `productCount` reflects only the products actually referencing "laptops" — SC-003.

## Cleanup

```bash
make stop
```
