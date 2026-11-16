# Docker Deployment Troubleshooting

This page covers common local Docker Compose issues for the current GitStore stack.

## Current Compose Shape

The core compose stack includes:

| Service            | Container                     | Ports                                  |
|--------------------|-------------------------------|----------------------------------------|
| API                | `gitstore-api`                | `4000` (Admin), `4100` (Storefront host), `9000` (Git HTTP host), `6000` |
| Git service        | `gitstore-git-service`        | `50051`                                |
| Controller manager | `gitstore-controller-manager` | `5001`                                 |

Git clients connect to the API on Git Smart HTTP port `9000`. The Git service is internal gRPC storage/transport and stores bare repositories in the `git-repo-data-root` volume.

Core services are enabled by the `local` profile. Use the root Make targets so
the profile and `compose.local.yml` are applied consistently. They validate the
selected `CONFIG_FILE` and `config/policy.yaml` before startup. Both files are
mounted read-only; `CONFIG_FILE=./config/config.prod.toml` selects an alternate
host filename without changing container arguments.

## Basic Health Checks

```bash
make ps
```

Check API health:

```bash
curl -s http://localhost:4000/health | jq .
curl -s http://localhost:4000/ready | jq .
```

The API image contains one static Go binary and CA certificates, runs as
UID/GID `65532:65532`, and has no shell or `wget`. Operator commands run as
`/app/api gitctl <command>` without starting the server. Compose probes use:

```bash
docker compose exec api /app/api gitctl healthcheck
```

For live process verification without installing tools in the container, use
`docker compose exec api /app/api gitctl process-executable`; PID 1 should be
`/app/api`.

For a custom Admin port, pass `--url http://127.0.0.1:<port>/ready`.
The probe requires HTTP 200, rejects redirects, and times out after one second
by default (`--timeout` overrides it). Host-side `curl` remains available for
response-body inspection.

The local overlay loads the issuer PEM through
`--serviceaccount-signing-key-file /run/secrets/serviceaccount-signing-key`;
keep that mount API-only. Credential initialization uses a separate Alpine
debug image as root to prepare the named volumes and assign the issuer PEM to
the API UID. Enrollment also needs write access to its identity volume; the
long-running API remains non-root. Preserve existing volumes and key material.
Configuration, policy and user-directory bind mounts must also be readable by
UID `65532`; keep private issuer keys mode `0600` and owned by that UID.

For an interactive diagnostic shell, build the opt-in target from the same
reviewed source as the runtime image, then start a disposable container:

```bash
docker build --target debug -f docker/api.Dockerfile -t gitstore-api:debug .
docker run --rm -it gitstore-api:debug /bin/sh
```

The debug target defaults to the same non-root identity. It is not the default
published runtime image. Do not mount production credentials or writable data
volumes merely to inspect the image.

Check controller-manager health:

```bash
curl -s http://localhost:5001/health | jq .
```

Check GraphQL:

```bash
curl -s http://localhost:4000/admin/graphql \
  -H "Content-Type: application/json" \
  -d '{"query":"query { namespaces(first: 1) { pageInfo { hasNextPage } } }"}' | jq .
```

## Bootstrap Checklist

Repositories are created through the API, not by manually placing a default repository in a shared volume.

Create the default namespace and repository:

```bash
make bootstrap TARGET=all ADMIN_PASSWORD=<admin-password>
```

The command prints a clone URL:

```text
http://localhost:9000/gitstore-test/catalog.git
```

If bootstrap fails, get a token explicitly:

```bash
make bootstrap TARGET=token ADMIN_PASSWORD=<admin-password>
make bootstrap TARGET=all BOOTSTRAP_TOKEN=<token>
```

## Volume Debugging

Inspect the Git service volume:

```bash
docker inspect gitstore-git-service | jq '.[0].Mounts'
```

List repository data inside the Git service container:

```bash
docker compose exec git-service ls -la /var/lib/gitstore/repos
```

The exact repository storage path is generated from the repository identity
and published after reconciliation in `Repository.status.resolved.storagePath`:

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
    status {
      resolved {
        storagePath
      }
    }
  }
}
```

## Common Errors

### API container exits during startup

With `make compose`, first validate the shared fixture and resolved mounts:

```bash
make check TARGET=config
CONFIG_FILE=./config/config.toml docker compose --profile local \
  -f compose.yml -f compose.local.yml config
```

For non-profile/manual startup, check the active auth settings:

- `api.auth.static_users.users_file` points to a readable users file
- `GITSTORE_API__AUTH__JWT__SECRET`

Inspect logs:

```bash
make logs SERVICE=api
```

### Clone or push reports repository not found

Confirm the namespace and repository exist:

```bash
curl -s http://localhost:4000/admin/graphql \
  -H "Content-Type: application/json" \
  -d '{"query":"query { repository(by: { namespacePath: { namespace: \"gitstore-test\", name: \"catalog\" } }) { id metadata { name } } }"}' | jq .
```

Use the API-fronted clone URL with the `.git` suffix:

```text
http://localhost:9000/gitstore-test/catalog.git
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
make logs SERVICE=api
make logs SERVICE=controller-manager
```

Configure the Admin client with a complete browser-accessible Admin URL, such
as `http://localhost:4000/admin/graphql`. A server-side client on the Compose
network uses:

```text
http://api:4000/admin/graphql
```

Configure CORS to allow the browser origin if it differs from the API origin.
The external Admin client's environment-variable name depends on its release.

## Log Commands

| Scope              | Command                                |
|--------------------|----------------------------------------|
| API                | `make logs SERVICE=api`                |
| Git service        | `make logs SERVICE=git-service`        |
| Controller manager | `make logs SERVICE=controller-manager` |
| All services       | `make logs`                            |

All services emit structured logs. Pipe JSON lines through `jq` when needed:

```bash
make logs SERVICE=api 2>&1 | grep '{' | jq .
```

## Clean Restart

Stop and remove containers while retaining persistent state:

```bash
make down
```

Normal restarts do not require deleting volumes or re-bootstrapping existing
state. The following is a destructive reset, only for disposable local fixtures
you explicitly intend to discard. Never use it for an operator deployment or a
retained capacity baseline:

```bash
docker compose -f compose.yml -f compose.scylla.yml down --volumes
```

Start again; bootstrap only when initializing fresh state:

```bash
make compose DETACH=1
make bootstrap TARGET=all ADMIN_PASSWORD=<admin-password>
```
