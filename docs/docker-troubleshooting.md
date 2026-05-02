# Docker Deployment Troubleshooting

## Repository initialisation checklist

Before starting the stack, the catalogue repository must exist on disk:

```bash
# Create and seed the demo catalog
./scripts/init-demo-catalog.sh --data-dir ./data/repos

# Verify the bare git repo was created
ls ./data/repos/catalog.git/HEAD   # should print "ref: refs/heads/main"
```

The `GITSTORE_DATA_DIR` environment variable (or the `--data-dir` flag) must
point to the **parent** of `catalog.git`, not to `catalog.git` itself.

```
data/
└── repos/
    └── catalog.git/   ← bare git repository
```

---

## Volume mount path debugging

```bash
# Print the resolved path docker compose will use
docker compose config | grep -A5 volumes

# Inspect a running container's mounts
docker inspect gitstore-git-server-1 | jq '.[0].Mounts'

# Shell into git-server and verify the repo exists
docker compose exec git-server ls /data/repos/catalog.git
```

If `/data/repos/catalog.git` is missing inside the container the service
will start but all pushes will fail with "repository not found".

---

## Websocket connectivity verification

```bash
# Check the dedicated websocket health endpoint
curl -s http://localhost:9418/websocket/health | jq .

# Expected:
# { "status": "healthy", "active_connections": 1, "timestamp": "..." }
```

If `active_connections` is 0 after the API starts, the API failed to connect
to the git-server websocket. Check:

1. The API's `GITSTORE_WS_URL` env var points to `ws://git-server:8080`.
2. The `git-server` service started before the `api` service
   (`depends_on: [git-server]` in compose.yml).
3. No firewall rule blocks port 8080 between containers.

---

## Common error messages and solutions

### `failed to get products: repository does not exist`

**Cause**: The API cannot find the git repository.  
**Fix**: Ensure the shared volume is mounted and `GITSTORE_GIT_REPO` is set
to the local path (e.g. `/data/repos/catalog.git`), **not** a `git://` URL,
unless you have implemented the full git-protocol loader (T152).

### `send-pack: unexpected disconnect while reading sideband packet`

**Cause**: The git-server rejected a push with HTTP 422 (validation error),
but the error message was in Rust debug format.  
**Fix**: Upgrade to a version that includes T153 (human-readable error
output). The full error is in the git-server container logs:

```bash
docker compose logs git-server | grep -i "validation"
```

### `error: RPC failed; HTTP 422`

**Cause**: Catalogue validation failed on push (invalid YAML frontmatter, missing required fields, duplicate SKU, etc.).  
**Fix**: Read the error detail from git-server logs, then correct the Markdown files in your working tree before pushing again.

### Admin shows blank page after login

> For Admin-specific troubleshooting, see [docs/admin/quickstart.md](admin/quickstart.md).

**Cause**: The API is unreachable from the browser or GraphQL returned an error.  
**Fix**:

```bash
# Test the API directly
curl -s -X POST http://localhost:4000/graphql \
  -H "Content-Type: application/json" \
  -d '{"query":"{ catalogVersion { tag stats { productCount } } }"}' | jq .
```

---

## Log locations for each service

| Service              | How to view                                                        |
|----------------------|--------------------------------------------------------------------|
| gitstore-git-service | `docker compose logs -f git-service`                               |
| gitstore-api         | `docker compose logs -f api`                                       |
| gitstore-admin       | `docker compose -f compose.yml -f compose.admin.yml logs -f admin` |
| All (core)           | `docker compose logs -f`                                           |

All services emit structured JSON logs. Pipe through `jq` for readability:

```bash
docker compose logs -f api 2>&1 | grep '{' | jq .
```

---

## Full stack restart (clean slate)

```bash
# Stop everything and remove volumes
docker compose down --volumes

# Re-initialize catalog
./scripts/init-demo-catalog.sh --data-dir ./data/repos

# Start fresh
docker compose up --build
```
