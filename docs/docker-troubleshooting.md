# Docker Deployment Troubleshooting

This page covers common local Docker Compose issues for the current GitStore stack.

## Current Compose Shape

The core compose stack includes:

| Service | Container | Ports |
|---|---|---|
| API | `gitstore-api` | `4000`, `5000`, `6000` |
| Git service | `gitstore-git-service` | `50051` |
| Controller manager | `gitstore-controller-manager` | `5001` |

The optional admin compose override adds:

| Service | Container | Port |
|---|---|---|
| Admin | `gitstore-admin` | `3000` |

Git clients connect to the API on Git Smart HTTP port `5000`. The Git service is internal gRPC storage/transport and stores bare repositories in the `git-repo-data-root` volume.

## Basic Health Checks

```bash
make ps
```

Check API health:

```bash
curl -s http://localhost:4000/health | jq .
curl -s http://localhost:4000/ready | jq .
```

Check controller-manager health:

```bash
curl -s http://localhost:5001/health | jq .
```

Check GraphQL:

```bash
curl -s http://localhost:4000/graphql \
  -H "Content-Type: application/json" \
  -d '{"query":"query { namespaces(first: 1) { totalCount } }"}' | jq .
```

## Bootstrap Checklist

Repositories are created through the API, not by manually placing a default repository in a shared volume.

Create the default namespace and repository:

```bash
make bootstrap ADMIN_PASSWORD=<admin-password>
```

The command prints a clone URL:

```text
http://localhost:5000/gitstore-test/catalog.git
```

If bootstrap fails, get a token explicitly:

```bash
make bootstrap-token ADMIN_PASSWORD=<admin-password>
make bootstrap BOOTSTRAP_TOKEN=<token>
```

## Volume Debugging

Inspect the Git service volume:

```bash
docker inspect gitstore-git-service | jq '.[0].Mounts'
```

List repository data inside the Git service container:

```bash
docker compose exec git-service ls -la /data/repos
```

The exact repository storage path is generated from the repository identity and is exposed on the `Repository.storagePath` GraphQL field:

```graphql
query Repository {
  repository(
    by: {
      namespacePath: {
        namespace: "gitstore-test"
        name: "catalog"
      }
    }
  ) {
    id
    storagePath
  }
}
```

## Common Errors

### API container exits during startup

Check required auth settings:

- `GITSTORE_AUTH__ADMIN__USERNAME`
- `GITSTORE_AUTH__ADMIN__PASSWORD_HASH`
- `GITSTORE_AUTH__JWT__SECRET`

Inspect logs:

```bash
make logs SERVICE=api
```

### Clone or push reports repository not found

Confirm the namespace and repository exist:

```bash
curl -s http://localhost:4000/graphql \
  -H "Content-Type: application/json" \
  -d '{"query":"query { repository(by: { namespacePath: { namespace: \"gitstore-test\", name: \"catalog\" } }) { id name } }"}' | jq .
```

Use the API-fronted clone URL with the `.git` suffix:

```text
http://localhost:5000/gitstore-test/catalog.git
```

### Push rejected with validation errors

Read the `remote:` lines in Git output. Also inspect API and Git service logs:

```bash
make logs SERVICE=git-service
make logs SERVICE=api
```

Common causes include invalid YAML frontmatter, wrong `apiVersion` or `kind`, missing `metadata.name`, authoring `status`, or invalid product variant pricing/inventory fields.

### `send-pack: unexpected disconnect while reading sideband packet`

The Git client can surface a transport-level message when the server rejects a push. Check the same validation diagnostics and service logs used for rejected pushes.

### GraphQL returns empty catalogue data

Check that:

- The repository was bootstrapped.
- You pushed to the expected branch.
- The push completed successfully.
- Your query includes the correct namespace where required.
- You are querying current fields, such as `Product.spec` and `ProductVariant.spec`, rather than old flat product fields.

### Admin shows a blank page

Check the admin and API logs:

```bash
make admin-logs
make logs SERVICE=api
```

The compose default is:

```text
GITSTORE_GRAPHQL_URL=http://api:4000/graphql
```

For more admin-specific checks, see [docs/admin/quickstart.md](admin/quickstart.md).

## Log Commands

| Scope | Command |
|---|---|
| API | `make logs SERVICE=api` |
| Git service | `make logs SERVICE=git-service` |
| Controller manager | `make logs SERVICE=controller-manager` |
| Admin | `make admin-logs` |
| All services | `make logs` |

All services emit structured logs. Pipe JSON lines through `jq` when needed:

```bash
make logs SERVICE=api 2>&1 | grep '{' | jq .
```

## Clean Restart

Stop and remove containers:

```bash
make down
```

Remove volumes when you explicitly want to discard local repository and datastore state:

```bash
docker compose -f compose.yml -f compose.scylla.yml -f compose.admin.yml down --volumes
```

Start again:

```bash
make compose DETACH=1
make bootstrap ADMIN_PASSWORD=<admin-password>
```
