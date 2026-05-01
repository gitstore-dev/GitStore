# T152: Implement Proper Git Protocol Solution (Production Architecture)

**Date**: 2026-03-22
**Status**: 📝 PLANNED (Deferred to post-MVP)
**Priority**: MEDIUM (not blocking MVP, but needed for production)

---

## Current State: Temporary Solution (T146)

### What We Have Now

**Quick fix applied for MVP**:
```yaml
# compose.yml
api:
  volumes:
    - ${GITSTORE_DATA_DIR:-git-data}:/data/repos:ro  # Shared volume
  environment:
    - GITSTORE_GIT_REPO=/data/repos/catalog.git      # Filesystem path
```

**How it works**:
- Both git-server and API mount same volume
- API reads catalog directly from filesystem
- Fast and simple for single-host deployment

---

## Why It's Temporary

### Limitations of Shared Volume Approach

| Aspect                | Current (Shared Volume)       | Desired (Git Protocol) |
|-----------------------|-------------------------------|------------------------|
| **Deployment**        | Single host only              | Distributed ok         |
| **Coupling**          | Tight (shared filesystem)     | Loose (HTTP protocol)  |
| **Scalability**       | Can't scale API independently | Can scale separately   |
| **Architecture**      | Violates microservices        | True microservices     |
| **Remote git-server** | Not possible                  | Supported              |
| **K8s/Cloud**         | Complex volume sharing        | Native support         |

### What We Agreed On

From T145 investigation:

> **Option 1: Git Protocol (Network-Based)** ✅ RECOMMENDED
> - API clones from `git://git-server:9418/catalog.git`
> - Requires implementing clone logic in catalog loader
> - True microservices architecture
> - Works in distributed deployment

**Decision**: Use Option 2 (shared volume) for **quick unblocking**, migrate to Option 1 (git protocol) for **production**.

---

## The Proper Solution

### Architecture

```
┌─────────────┐
│   User      │
└──────┬──────┘
       │ git push
       ↓
┌──────────────────┐
│  Git Server      │
│  Port 9418       │
│  Serves:         │
│  - clone         │
│  - fetch         │
│  - push          │
└──────┬───────────┘
       │ git://git-server:9418/catalog.git
       ↓
┌──────────────────┐
│  API             │
│  Clones & pulls  │
│  from git-server │
└──────────────────┘
```

**No shared volume** - API accesses repository via HTTP/git protocol only.

---

## Implementation Plan

### Changes Required

#### 1. Update API Catalog Loader

**File**: `api/internal/catalog/loader.go`

**Current**:
```go
func NewLoader(repoPath string, logger *zap.Logger) *Loader {
    return &Loader{
        repoPath: repoPath,  // Expects filesystem path
        logger:   logger,
    }
}

func (l *Loader) LoadFromTag(ctx context.Context, tag string) (*Catalog, error) {
    repo, err := git.PlainOpen(l.repoPath)  // Opens local path
    // ...
}
```

**New**:
```go
type Loader struct {
    repoURL    string         // git:// URL or local path
    localPath  string         // Where cloned repo is cached
    logger     *zap.Logger
    isRemote   bool           // Track if URL is remote
}

func NewLoader(repoURL string, logger *zap.Logger) *Loader {
    isRemote := strings.HasPrefix(repoURL, "git://") ||
                strings.HasPrefix(repoURL, "http://") ||
                strings.HasPrefix(repoURL, "https://")

    var localPath string
    if isRemote {
        // Create temp directory for cloned repo
        tmpDir, _ := os.MkdirTemp("", "gitstore-catalog-*")
        localPath = filepath.Join(tmpDir, "catalog")
    } else {
        localPath = repoURL  // Already local
    }

    return &Loader{
        repoURL:   repoURL,
        localPath: localPath,
        logger:    logger,
        isRemote:  isRemote,
    }
}

func (l *Loader) ensureRepository(ctx context.Context) error {
    // If local path, nothing to do
    if !l.isRemote {
        return nil
    }

    // Check if already cloned
    if _, err := os.Stat(filepath.Join(l.localPath, ".git")); err == nil {
        // Already exists, pull updates
        return l.pullUpdates(ctx)
    }

    // Clone from remote
    l.logger.Info("Cloning catalog repository",
        zap.String("url", l.repoURL),
        zap.String("dest", l.localPath),
    )

    _, err := git.PlainClone(l.localPath, false, &git.CloneOptions{
        URL: l.repoURL,
    })

    if err != nil {
        return fmt.Errorf("failed to clone repository: %w", err)
    }

    l.logger.Info("Repository cloned successfully")
    return nil
}

func (l *Loader) pullUpdates(ctx context.Context) error {
    repo, err := git.PlainOpen(l.localPath)
    if err != nil {
        return err
    }

    worktree, err := repo.Worktree()
    if err != nil {
        return err
    }

    l.logger.Debug("Pulling repository updates")

    err = worktree.Pull(&git.PullOptions{
        RemoteName: "origin",
    })

    if err == git.NoErrAlreadyUpToDate {
        l.logger.Debug("Repository already up to date")
        return nil
    }

    return err
}

func (l *Loader) LoadFromTag(ctx context.Context, tag string) (*Catalog, error) {
    // Ensure repository is available
    if err := l.ensureRepository(ctx); err != nil {
        return nil, fmt.Errorf("failed to ensure repository: %w", err)
    }

    // Now open local copy
    repo, err := git.PlainOpen(l.localPath)
    if err != nil {
        return nil, fmt.Errorf("failed to open repository: %w", err)
    }

    // Rest of loading logic unchanged...
}
```

---

#### 2. Update compose.yml

**File**: `compose.yml`

**Remove volume mount from API**:
```yaml
api:
  # volumes:  # ← REMOVE THIS
  #   - ${GITSTORE_DATA_DIR:-git-data}:/data/repos:ro
  environment:
    - GITSTORE_GIT_REPO=git://git-server:9418/catalog.git  # ← Change back to git URL
```

---

#### 3. Update Cache Reload Logic

**File**: `api/cmd/server/main.go`

**Current**:
```go
// Websocket notification triggers immediate reload
wsClient := websocket.NewClient(*gitWS, func(event websocket.GitEvent) {
    cacheManager.Invalidate()
    // Reload in background
    go func() {
        if _, err := cacheManager.Get(context.Background()); err != nil {
            logger.Log.Error("Failed to reload catalog", zap.Error(err))
        }
    }()
}, logger.Log)
```

**Add**:
```go
// Websocket notification triggers pull + reload
wsClient := websocket.NewClient(*gitWS, func(event websocket.GitEvent) {
    logger.Log.Info("Received tag notification, pulling updates",
        zap.String("tag", event.Tag),
    )

    cacheManager.Invalidate()

    // Pull latest and reload
    go func() {
        // Pull will fetch new tag from git-server
        if _, err := cacheManager.Get(context.Background()); err != nil {
            logger.Log.Error("Failed to reload catalog", zap.Error(err))
        }
    }()
}, logger.Log)
```

---

### Benefits After Migration

#### 1. Distributed Deployment

```yaml
# Can deploy on different hosts
version: '3.8'
services:
  git-server:
    deploy:
      placement:
        constraints: [node.role == manager]

  api:
    deploy:
      replicas: 3  # Scale API independently!
      placement:
        constraints: [node.role == worker]
```

#### 2. Kubernetes Support

```yaml
# No need for shared PersistentVolume
apiVersion: apps/v1
kind: Deployment
metadata:
  name: gitstore-api
spec:
  replicas: 5  # Scale freely!
  template:
    spec:
      containers:
      - name: api
        env:
        - name: GITSTORE_GIT_REPO
          value: "git://git-server-service:9418/catalog.git"
        # No volume mount needed!
```

#### 3. Remote Git Server

```go
// Can point to external git server
export GITSTORE_GIT_REPO=git://external-git.company.com:9418/catalog.git
```

---

## Testing Plan

### Test 1: Local Path Still Works

```bash
# Should support both modes
export GITSTORE_GIT_REPO=/data/repos/catalog.git  # Filesystem
docker compose up
# Should work as before
```

### Test 2: Git Protocol Works

```bash
# Should support git protocol
export GITSTORE_GIT_REPO=git://git-server:9418/catalog.git  # Network
docker compose up
# Should clone and work
```

### Test 3: Updates Propagate

```bash
# Push new tag
git tag -a v1.1.0 -m "Update"
git push origin v1.1.0

# API should:
# 1. Receive websocket notification
# 2. Pull from git-server
# 3. Load new catalog
# 4. Serve updated data
```

### Test 4: API Scales

```bash
# Start multiple API instances
docker compose up --scale api=3

# All should clone independently
# All should receive websocket notifications
# All should reload on tag push
```

---

## Migration Path

### Phase 1: Make Code Support Both (Backward Compatible)

1. Update catalog loader to detect URL type
2. Support both filesystem and git protocol
3. Test with filesystem path (current setup)
4. Test with git protocol
5. Deploy - no breaking changes

### Phase 2: Update Deployment (When Ready)

1. Change `GITSTORE_GIT_REPO` to git URL
2. Remove volume mount from compose.yml
3. Restart services
4. Verify API clones successfully
5. Test push → notification → reload

### Phase 3: Remove Filesystem Support (Optional)

1. Simplify loader code (remove filesystem path support)
2. Always require git URL
3. Update documentation

---

## When to Migrate

### Now (MVP) ✅

Current approach is fine for:
- Single host deployment
- Development/testing
- Small scale (1 API instance)
- Quick delivery

### Migrate When

You need:
- Multiple API instances (horizontal scaling)
- Kubernetes deployment
- High availability
- Separate hosts for services
- Production architecture

---

## Estimated Effort

| Task                  | Time          | Complexity |
|-----------------------|---------------|------------|
| Update catalog loader | 2-3 hours     | Medium     |
| Update compose.yml    | 15 min        | Low        |
| Testing               | 1-2 hours     | Medium     |
| Documentation         | 30 min        | Low        |
| **Total**             | **4-6 hours** | **Medium** |

**Not urgent** - can be done anytime after MVP launch.

---

## Summary

| Aspect         | Current (T146)   | Future (T152)     |
|----------------|------------------|-------------------|
| **Status**     | ✅ Working        | 📝 Planned        |
| **Use case**   | MVP, single host | Production, scale |
| **Complexity** | Simple           | Medium            |
| **Blocking**   | No               | No                |
| **Priority**   | Done             | Medium            |

**Recommendation**: Keep current solution for MVP, migrate to proper git protocol when scaling requirements arise or when deploying to production/Kubernetes.

---

**Created**: 2026-03-22
**Tracking**: T152 in tasks.md
**Dependencies**: None (can be done anytime)
**Blocks**: Nothing (nice-to-have improvement)
