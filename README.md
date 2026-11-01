# GitStore Admin

<picture><source media="(prefers-color-scheme: dark)" srcset="gitstore-admin/public/branding/readme-dark.png"><img src="gitstore-admin/public/branding/readme-light.png" alt="GitStore — Change boldly. Keep a way back."></picture>

An isolated, mock-first Admin foundation: React, strict TypeScript, Vite, TanStack Router, React Aria, and custom CSS Modules. All 79 PRD destinations are navigable placeholders. There are no commerce pages, authentication, backend calls, or GraphQL runtime yet.

## Start

Use Node from `.nvmrc` and pnpm 12.6.0, then:

```sh
make install
make dev TARGET=admin
# In another terminal:
make dev TARGET=storybook
```

Vite prints the app URL; Storybook uses port 6006. Use the workspace picker for local demo contexts. Cmd/Ctrl+K searches pages. The Layout menu switches standard, inspector, and Focus Mode. Profile preferences include reduced motion; system reduced motion always wins.

## Validate

```sh
pnpm --dir gitstore-admin exec playwright install chromium firefox webkit
make check TARGET=all
make test TARGET=e2e
# Or the complete gate:
make pr-ready
```

Browser tests require both app and Storybook builds, produced by `make check` / `make build TARGET=all`. Screenshots and reports are written beneath `gitstore-admin/test-results` and `playwright-report`. CI performs only frontend checks.

## Build one page at a time

Each thin file in `gitstore-admin/src/routes` renders the shared placeholder by a registry ID. Replace that route component with a feature page, keep its navigation identity, then add its stories and browser coverage. Keep UI preferences separate from future server data. Add Apollo Client 4, typed operations and contract-backed mocks when the first data-driven page needs them. No fake profile/permission API has been introduced for the shell.

[Admin PRD](gitstore-admin/docs/admin-prd.md) · [Architecture and workflow](gitstore-admin/docs/development.md)

## Branch boundary

This branch deliberately removes backend and deployment content while retaining GitStore history and the license. **Do not merge this entire branch into the backend branch.** Import the Admin directory and deliberately selected frontend tooling/documentation; exclude backend deletions and root-file replacements. Current backend worktrees and shared Git configuration are untouched. The copied PRD is planning context, not a declaration of shipped commerce functionality.
