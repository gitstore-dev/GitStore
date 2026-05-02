# E2E Test Status

## Summary

This status file reflects the codebase as of **2026-04-18**.

The Playwright suite is present and reasonably comprehensive in scope, but it is **not currently aligned with the implemented admin UI**. Some earlier notes in this file were stale: the backend is further along than they claimed, while parts of the admin UI are still using mock data and simulated mutations.

## Current Assessment

- **Playwright infrastructure exists**: config, browser support, screenshots/traces, and test files are present.
- **Login endpoint is implemented**: `POST /api/login` exists in the Go API and returns JWT session data.
- **Core backend GraphQL functionality exists**: the API contains resolvers, git client logic, auth middleware, publish flow code, and catalog loading.
- **Admin UI is only partially wired to live backend data**: several screens still use local mock state instead of real GraphQL queries and mutations.
- **E2E specs are currently out of sync with the UI**: several selectors, route expectations, and field names in tests do not match the current pages/components.

## What Is Implemented

### Backend

- **Authentication**
  - `/api/login` is implemented and validates Basic Auth credentials.
  - JWT session generation exists.
- **Catalog/API**
  - GraphQL server startup, catalog loading, caching, websocket invalidation, and mutation plumbing are present.
  - Product, category, collection, and publish-related code exists in the API package.
- **Git server**
  - Repository init/open logic, validation, HTTP git routes, and websocket broadcasting code are present.

### Frontend/Admin UI

- **Page shell and routing**
  - Login page exists.
  - Products, categories, collections, and product create/edit pages exist.
  - Protected-route structure and auth context exist.
- **Shared UI**
  - Markdown editor exists.
  - Conflict modal exists.
  - Publish button/modal exist.

## What Is Still Mocked or Partial

### Admin UI data integration

The following components still contain placeholder types, mock data, or simulated network behavior instead of live GraphQL-backed behavior:

- `src/components/products/ProductList.tsx`
- `src/components/products/CreateProductPage.tsx`
- `src/components/products/EditProductPage.tsx`
- `src/components/products/ProductForm.tsx`
- `src/components/categories/CategoryList.tsx`
- `src/components/collections/CollectionList.tsx`
- `src/lib/publish.ts`
- `src/graphql/generated.ts`

Specific gaps:

- Product list uses mock sample products.
- Product create simulates an API call, then redirects.
- Product edit loads mock product data and simulates conflict handling.
- Product form loads mock categories and collections.
- Category list builds a mock hierarchy locally.
- Collection list uses mock collections and simulated reorder/delete behavior.
- Publish flow does not yet perform a real “uncommitted changes” check.
- Generated GraphQL types/hooks file is still a placeholder.

### Auth/session gaps

- Login is real.
- Token refresh is **not fully wired**:
  - the frontend calls `/api/refresh-token`
  - no matching backend route was found in this pass
- Token revocation is only a placeholder in the backend session manager.

## E2E Coverage vs Reality

### Test files present

- `product_crud.spec.ts`
- `category_reorder.spec.ts`

### Coverage intent

The tests aim to cover:

- login
- product create/read/update/delete
- validation
- optimistic locking
- markdown preview
- category drag-and-drop reorder
- persistence after reload

### Current alignment issues

The tests do not currently match the implemented UI in several places. Examples:

- Tests expect dynamic product edit routes like `/products/prod_...`, but the current UI uses `/products/edit?id=...`.
- Tests fill fields like `slug`, `inventory`, and `status=published`, but the current product form does not match those names/options.
- Tests assume fully live CRUD-backed list pages, while current list pages still use mock state.
- Category reorder tests assume creation/reordering flows that are not fully wired to backend mutations yet.

Because of those mismatches, the suite should be treated as **authored but not currently reliable as an end-to-end implementation signal**.

## Recommended Status

- **E2E suite presence**: complete enough to guide intended workflows
- **E2E suite readiness**: not production-ready
- **Backend readiness for E2E**: partial to substantial
- **Frontend readiness for E2E**: partial
- **End-to-end readiness**: not yet verified

## Next Steps

1. Replace placeholder `src/graphql/generated.ts` with actual codegen output.
2. Wire product, category, and collection pages to real GraphQL queries/mutations.
3. Implement a real backend-backed pending-changes check for publish flow.
4. Add or wire a real `/api/refresh-token` endpoint, or remove the frontend dependency on it.
5. Update Playwright specs so routes, selectors, and field names match the current UI.
6. Run the suite against the real stack and record actual pass/fail results here.
