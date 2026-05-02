# GitStore Admin — Quickstart

`gitstore-admin` is an optional layer that provides a web UI for managing the product catalogue.

## Prerequisites

- The core stack must be running. Start it first:
  ```bash
  docker compose up -d
  ```
- Verify both core services are healthy:
  ```bash
  docker compose ps
  # Expected: gitstore-git-service and gitstore-api both "running"
  ```

## Starting the Full Stack (Core + Admin)

Use the `compose.admin.yml` override together with the base `compose.yml`:

```bash
docker compose -f compose.yml -f compose.admin.yml up -d
```

**Expected output**:
```
NAME                 STATUS              PORTS
gitstore-git-service running             0.0.0.0:9418->9418/tcp, 0.0.0.0:8080->8080/tcp
gitstore-api         running             0.0.0.0:4000->4000/tcp
gitstore-admin       running             0.0.0.0:3000->3000/tcp
```

## Accessing the Admin UI

Open **http://localhost:3000** in your browser.

Log in with the credentials configured in `GITSTORE_AUTH_SECRET` (default: `admin123` — change this before any real deployment).

## Managing Products

### Creating a Product

1. Navigate to **Products** → **New Product**
2. Fill in the product details form (title, SKU, price, category)
3. Optionally add to collections
4. Click **Save Draft**
5. When ready to publish, proceed to the Publish step below

### Updating a Product

1. Navigate to **Products** and click the product to edit
2. Modify the fields
3. Click **Save Draft**

### Deleting a Product

1. Navigate to **Products**
2. Click the product → **Delete**
3. Confirm deletion (this stages a deletion commit)

## Publishing Changes

`gitstore-admin` accumulates draft changes in memory. Publishing creates a git commit and a release tag, which triggers the catalogue to update.

1. Click **Publish** in the top navigation
2. Enter a version tag (e.g., `v1.0.3`)
3. Enter a commit message describing what changed
4. Click **Publish Catalogue**

This creates a git commit and annotated tag in `gitstore-git-service`, which broadcasts a websocket notification. `gitstore-api` receives the notification and reloads the catalogue.

## Stopping the Admin

```bash
docker compose -f compose.yml -f compose.admin.yml down
```

To stop only the admin container while keeping the core stack running:

```bash
docker compose -f compose.yml -f compose.admin.yml stop admin
```

## Troubleshooting

### Admin shows stale data

**Problem**: Admin UI doesn't reflect recent git changes.

**Solution**: The admin caches catalogue data. To refresh:
1. Make git changes via CLI or AI agent
2. Push and create a new release tag
3. Wait for the websocket notification (check: `docker compose logs git-service`)
4. Refresh the Admin browser tab

### Merge conflicts when publishing

**Problem**: Admin says "Push failed: merge conflicts detected."

**Solution**: Someone pushed changes via git while you were editing in the admin. Resolve manually:
1. Clone or pull the latest changes locally
2. Resolve conflicts using git
3. Commit and push
4. Return to the Admin and refresh

### Admin container won't start

**Problem**: `gitstore-admin` fails to start or exits immediately.

**Checklist**:
- Is the core stack healthy? Run `docker compose ps` first.
- Is `gitstore-api` reachable on port 4000? The admin has a `depends_on: api` health check.
- Is `GITSTORE_GRAPHQL_URL` set correctly in `compose.admin.yml`? Default: `http://api:4000/graphql`.

## Building the Admin from Source

```bash
cd gitstore-admin
npm install
npm run dev  # Development server at http://localhost:3000

# Production build
npm run build
npm run preview
```

## Architecture

For a diagram showing how `gitstore-admin` connects to the core stack, see [docs/admin/architecture.md](architecture.md).
