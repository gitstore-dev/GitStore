# GitStore Admin

`gitstore-admin` is the optional web UI for GitStore. It is designed for users who prefer a browser-based backoffice over direct Git commands while still relying on the core API as the system boundary.

## What It Adds

The core stack runs without the admin UI. Developers and automation can manage catalogue files through Git Smart HTTP and read admitted state through GraphQL.

The admin service adds:

- Browser login backed by `gitstore-api` authentication.
- Catalogue browsing and editing screens built with Astro and React.
- A UI attachment point for future Git-backed write workflows.
- Health checks and a standalone compose override for local testing.

Catalogue writes are Git-driven today. The older direct catalogue GraphQL CRUD and publish flow is not the documented write path while Git-backed CRUD over GraphQL is being finalized.

## When To Use It

| Scenario | Recommendation |
|---|---|
| Developers, CI jobs, AI agents, and bulk catalogue updates | Use the core stack and Git workflow |
| Merchandisers who need a browser UI | Add `gitstore-admin` |
| Minimal local development footprint | Core stack only |
| Full product experience testing | Core stack plus admin |

## Prerequisites

- The core stack must be running first. See the [user guide](../user-guide.md).
- Node.js 18+ for local admin development.
- Docker 24+ for compose-based deployment.

## Architecture

`gitstore-admin` connects to `gitstore-api` over GraphQL. It does not talk directly to `gitstore-git-service`, does not mount repository storage, and does not own catalogue admission.

```text
Browser -> gitstore-admin -> gitstore-api -> datastore
                              |
                              v
                        gitstore-git-service
```

For the full topology, see [architecture.md](architecture.md).

## Deployment

Use the compose wrapper documented in [quickstart.md](quickstart.md):

```bash
make admin-compose DETACH=1
```

The UI is served at http://localhost:3000.
