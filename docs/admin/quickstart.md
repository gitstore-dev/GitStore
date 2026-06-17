# GitStore Admin Quickstart

`gitstore-admin` is an optional browser UI for the GitStore core stack.

## Prerequisites

Start the core stack first:

```bash
make compose DETACH=1
make ps
```

The core services should include:

```text
gitstore-api                 4000, 5000, 6000
gitstore-git-service         50051
gitstore-controller-manager  5001
```

Bootstrap a namespace and repository if you have not already:

```bash
make bootstrap ADMIN_PASSWORD=<admin-password>
```

## Start Admin

Use the root Make wrapper:

```bash
make admin-compose DETACH=1
```

Then check the stack:

```bash
make ps
```

Expected admin port:

```text
gitstore-admin  0.0.0.0:3000->3000/tcp
```

## Open The UI

Open http://localhost:3000 in your browser and log in with the admin credentials configured for `gitstore-api`.

## Current Catalogue Workflow

Catalogue writes are Git-driven today:

1. Clone the repository URL printed by `make bootstrap`.
2. Edit catalogue resource files locally.
3. Commit and push through Git Smart HTTP.
4. Use GraphQL or the admin UI to inspect admitted state.

The admin UI connects to `gitstore-api` for authentication and catalogue reads. Direct catalogue GraphQL CRUD and publish flows are not the documented write path while Git-backed editing through GraphQL is being finalized.

## Stop Admin

Stop only the admin service:

```bash
make admin-stop
```

Stop and remove the admin compose stack:

```bash
make admin-down
```

## Local Development

```bash
cd gitstore-admin
npm install
npm run dev
```

The Astro development server listens on http://localhost:3000 by default.

Production build checks:

```bash
npm run build
npm run preview
```

## Troubleshooting

### Admin container does not start

Check the core stack first:

```bash
make ps
make logs SERVICE=api
make admin-logs
```

The admin container depends on the API health check and uses `GITSTORE_GRAPHQL_URL=http://api:4000/graphql` in compose.

### Login fails

Verify the API auth configuration:

- `GITSTORE_AUTH__ADMIN__USERNAME`
- `GITSTORE_AUTH__ADMIN__PASSWORD_HASH`
- `GITSTORE_AUTH__JWT__SECRET`

You can create or refresh a bootstrap token with:

```bash
make bootstrap-token ADMIN_PASSWORD=<admin-password>
```

### Catalogue data looks empty

Confirm that you pushed valid catalogue resources to the bootstrapped repository and that GraphQL queries include the correct namespace, for example `gitstore-test`.

For core workflow troubleshooting, see the [user guide](../user-guide.md#troubleshooting).

## Architecture

For service boundaries and ports, see [architecture.md](architecture.md).
