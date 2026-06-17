# gitstore-admin

Optional Astro/React admin UI for GitStore.

## Purpose

`gitstore-admin` provides a browser-facing backoffice surface that connects to `gitstore-api`.

It owns:

- Astro page shell and React application components.
- GraphQL client setup.
- Login-protected routes.
- Product, category, and collection UI screens.
- Health and readiness endpoints for the admin container.

Catalogue writes are Git-driven today. The admin UI is the attachment point for future Git-backed editing workflows, but direct catalogue GraphQL CRUD is not the documented write model.

## Boundaries

- Talks to `gitstore-api` over GraphQL.
- Does not talk directly to `gitstore-git-service`.
- Does not mount or own repository storage.
- Runs as an optional service on port `3000`.

## Configuration Highlights

| Variable | Default in compose | Purpose |
|---|---|---|
| `GITSTORE_GRAPHQL_URL` | `http://api:4000/graphql` | API endpoint used by admin |
| `GITSTORE_SESSION_TIMEOUT` | `3600` | Session timeout in seconds |

## Project Structure

```text
gitstore-admin/
├── src/
│   ├── components/    # React components
│   ├── graphql/       # GraphQL operations and generated types
│   ├── layouts/       # Astro layout
│   ├── lib/           # Client, auth, validation helpers
│   └── pages/         # Astro routes and health endpoints
├── tests/e2e/         # Playwright tests
├── astro.config.mjs
├── codegen.yml
├── package.json
└── playwright.config.ts
```

## Commands

From the repository root:

```bash
make admin-compose DETACH=1
make admin-logs
make admin-stop
make admin-down
```

From this module:

```bash
npm install
npm run dev
npm run build
npm run test
npm run test:e2e
npm run codegen
```

## Ports

| Port | Purpose |
|---:|---|
| `3000` | Admin UI |

## Deeper Docs

- [Admin docs](../docs/admin/README.md)
- [Admin architecture](../docs/admin/architecture.md)
- [Admin quickstart](../docs/admin/quickstart.md)
- [API Reference](../docs/api-reference.md)

## License

AGPL-3.0-or-later. See [LICENSE](../LICENSE).
