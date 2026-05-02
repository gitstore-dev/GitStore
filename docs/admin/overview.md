# GitStore Admin — Overview

The `gitstore-admin` service is an **optional** layer for GitStore. It provides a web-based interface for non-technical users to manage the product catalogue without using Git directly.

## What It Adds to the Core Stack

The core stack (`gitstore-api` + `gitstore-git-service`) is fully functional on its own — developers and AI agents interact with the catalogue via Git and GraphQL. `gitstore-admin` adds:

- A drag-and-drop product management UI
- Category and collection editors
- One-click publish (creates git commits and release tags automatically)
- A visual diff view for resolving concurrent edits

## When to Use the Admin

| Scenario                                    | Recommendation            |
|---------------------------------------------|---------------------------|
| Technical users, CI/CD pipelines, AI agents | Use the core stack only   |
| Non-technical merchandisers needing a UI    | Add `gitstore-admin`      |
| Minimal production footprint                | Core stack only           |
| Full-featured team workflow                 | Core stack + admin add-on |

## Prerequisites

- The **core stack must be running** before starting `gitstore-admin`. See the [quickstart](../developer-guide.md) for core stack setup.
- Node.js 18+ (for local development only; Docker handles this in production)
- Docker 24+ (for the compose-based deployment)

## Architecture

`gitstore-admin` sits entirely in front of `gitstore-api`. It makes GraphQL mutations to create, update, and delete catalogue entities, and calls `publishCatalog` to trigger release-tag creation.

```
gitstore-admin  →  gitstore-api (GraphQL)  →  gitstore-git-service (Git)
```

For a full architecture diagram see [docs/admin/architecture.md](architecture.md).

## Deployment

See [docs/admin/quickstart.md](quickstart.md) for step-by-step deployment instructions using `compose.admin.yml`.
