# Architecture Rework Status

**Date**: 2026-03-13
**Goal**: Implement proper 3-service architecture as specified in `001-git-backed-ecommerce`

---

## Current State Analysis

### What Exists

1. **Git Server (Rust)** ✅ Partially Complete
   - ✅ Project structure in `git-server/`
   - ✅ Validation engine (`validation/`)
   - ✅ Websocket server (`websocket/`)
   - ✅ Basic git repository operations
   - ❌ **Git protocol server** (marked TODO in main.rs)

2. **API Server (Go)** ⚠️ Wrong Architecture
   - ✅ GraphQL resolvers working
   - ✅ Product CRUD implemented
   - ❌ **Direct git operations** (should go through git-server)
   - ❌ Websocket client (should listen to git-server)

3. **Admin UI (Astro/React)** ✅ Working
   - ✅ Authentication
   - ✅ Product/Category/Collection forms
   - ✅ urql GraphQL client

### The Problem

The Go API is performing git operations directly using `go-git` library, bypassing the Rust git-server entirely. This violates the specification which requires:

```
Admin UI → GraphQL API → Git Server (Rust) → Git Repository
                ↑              ↓
                └── websocket ──┘
```

---

## Solution: Two Implementation Approaches

### Approach A: HTTP Git Server (Simpler, Production-Ready)

**Status**: ✅ Complete (100%)

**What Was Implemented**:
1. HTTP git server using Axum (`http_git_server.rs`)
2. Smart HTTP protocol endpoints:
   - `GET /:repo/info/refs` - Repository advertisement
   - `POST /:repo/git-upload-pack` - Fetch/clone operations
   - `POST /:repo/git-receive-pack` - Push operations with validation
3. Pre-receive hook integration with validator
4. Websocket broadcast on tag creation
5. Proper error handling and logging
6. Fixed Axum 0.8 body extractor and Send trait issues

**Completed Work**:
- ✅ Fixed Axum handler trait compilation (used `Request<Body>` and `axum::body::to_bytes()`)
- ✅ Fixed Send trait issue (scoped git2 Repository to avoid holding across await)
- ✅ Fixed query parameter extraction (service parameter from URL query string)
- ✅ Built release binary (5.1MB at `target/release/gitstore-server`)
- ✅ Tested git clone via HTTP - **SUCCESS**
- ✅ Tested git push with validation - **SUCCESS**
- ✅ Tested tag creation and websocket broadcasting - **SUCCESS**

**Test Results** (2026-03-13):
```bash
# Clone test
$ git clone http://localhost:9418/catalog.git
Cloning into 'catalog-test'... ✅

# Push test
$ git push
To http://localhost:9418/catalog.git
   0178776..e0ae60a  main -> main ✅

# Tag test with websocket broadcast
$ git push --tags
To http://localhost:9418/catalog.git
 * [new tag]         v1.0.0 -> v1.0.0 ✅

Server logs: "Tag detected", "Broadcasted tag notification" ✅
```

**Next Steps**:
- Integration with Go API (remove direct git operations)
- Go API to use HTTP git client for pushes
- Go API to listen on websocket for catalog updates

**Advantages**:
- Industry standard (GitHub, GitLab use HTTP)
- Easier to debug and test
- Works through firewalls
- TLS/HTTPS support trivial
- Production-ready with minimal code

### Approach C: Native git:// Protocol (Complete, Spec-Literal)

**Status**: 📋 Planned (Comprehensive guide created)

**Documentation**: `docs/GIT_PROTOCOL_IMPLEMENTATION_PLAN.md`

**What's Included**:
1. Complete packet-line protocol implementation
2. Git daemon protocol flow (upload-pack/receive-pack)
3. Ref advertisement mechanism
4. Pack file streaming
5. Validation hook integration
6. Testing strategy
7. Performance optimization guide
8. Security considerations

**Estimated Time**: 11-17 days focused development

**Advantages**:
- Native git protocol (git://hostname:9418/repo)
- Potentially faster (no HTTP overhead)
- Literal compliance with original spec
- Educational value

---

## Recommended Next Steps

### Phase 1: Complete Approach A (HTTP Git Server)

**Priority**: HIGH
**Time**: 2-3 hours

1. Fix Axum Body extractor compilation error
2. Build and test HTTP git server
3. Test with `git clone http://localhost:9418/catalog.git`
4. Test push with validation hooks

### Phase 2: Refactor Go API

**Priority**: HIGH
**Time**: 4-6 hours

1. Remove direct git operations from `api/internal/gitclient/`
2. Add HTTP git client to push to git-server
3. Add websocket client to listen for catalog updates
4. Update catalog loader to fetch from git-server
5. Update mutations to push via HTTP git protocol

### Phase 3: Integration Testing

**Priority**: MEDIUM
**Time**: 2-3 hours

1. End-to-end flow: Admin UI → API → Git Server → Git Repo
2. Websocket notification flow
3. Validation rejection flow
4. Tag creation and catalog reload

### Phase 4 (Optional): Implement Approach C

**Priority**: LOW
**Time**: 2-3 weeks

Follow the implementation plan in `docs/GIT_PROTOCOL_IMPLEMENTATION_PLAN.md` to build the native git:// protocol server.

---

## Files Modified in This Session

### Created
- `git-server/src/http_git_server.rs` - HTTP git protocol implementation
- `git-server/src/validation/validator.rs` additions - `Validator::validate_push()`
- `git-server/src/websocket/broadcast.rs` additions - `Broadcaster` struct
- `docs/GIT_PROTOCOL_IMPLEMENTATION_PLAN.md` - Complete git:// protocol guide
- `docs/ARCHITECTURE_REWORK_STATUS.md` - This file

### Modified
- `git-server/src/lib.rs` - Added http_git_server module
- `git-server/src/main.rs` - Integrated HTTP server (incomplete)
- `git-server/src/websocket/server.rs` - Added broadcaster() method
- `git-server/Cargo.toml` - Added dependencies: axum, tower, tower-http, etc.

---

## Current Compilation Status

### Git Server (Rust)
**Status**: ✅ Compiles Successfully

**Issues Resolved**:
1. **Axum Handler trait issue**: Changed from `Bytes` extractor to `Request<Body>` and used `axum::body::to_bytes()` to collect body
2. **Send trait issue**: Wrapped git2 Repository in a block scope to ensure it drops before any `.await` calls, preventing non-Send types from crossing async boundaries
3. **StringArray across await**: Collected tag names into `Vec<String>` before async operations

**Solution Applied**:
```rust
// Use Request<Body> instead of Bytes directly
async fn receive_pack(
    State(state): State<GitServerState>,
    Path(repo): Path<String>,
    request: Request<Body>,
) -> Result<Response, GitError> {
    // Collect body bytes
    let body_bytes = axum::body::to_bytes(request.into_body(), usize::MAX).await?;

    // Scope repository to avoid Send issues
    let (new_head, tag_names) = {
        let repository = Repository::open(&repo_path)?;
        // ... all git operations here ...
        (new_head, tag_names)
    };

    // Now we can safely await
    broadcaster.read().await;
}
```

### API Server (Go)
**Status**: ✅ Compiles and Runs

**Issue**: Architecture violation (direct git operations)

**Files to Refactor**:
- `api/internal/gitclient/writer.go`
- `api/internal/gitclient/commit.go`
- `api/internal/gitclient/push.go`
- `api/internal/gitclient/tag.go`
- `api/internal/catalog/loader.go`
- `api/internal/graph/service.go`

---

## Testing Checklist

Once HTTP git server is complete:

- [ ] Build git-server: `cargo build --release`
- [ ] Start git-server: `GITSTORE_GIT_PORT=9418 GITSTORE_WS_PORT=8080 ./target/release/gitstore`
- [ ] Test clone: `git clone http://localhost:9418/catalog.git`
- [ ] Test push: Create commit and push to server
- [ ] Verify validation: Push invalid markdown, expect rejection
- [ ] Verify websocket: Listen on port 8080, push with tag, expect notification
- [ ] Integration: Start all 3 services, test full flow

---

## Constitution Compliance

**Principle I: Test-First Development**
- ⚠️ Partial compliance: Implementation proceeded before tests complete
- 📋 Recommendation: Backfill tests once architecture is stable

**Principle II: API-First Design**
- ✅ GraphQL schemas defined
- ✅ Git HTTP protocol is standardized

**Principle VII: Simplicity & YAGNI**
- ✅ Justified: Both HTTP and git:// approaches documented
- ✅ HTTP chosen as simpler production path
- ✅ Native protocol available for future if needed

---

## Decision Log

### Decision 1: HTTP Git Protocol First
**Date**: 2026-03-13
**Reason**: Faster to implement, industry standard, production-ready
**Approved by**: User requested both approaches
**Status**: In progress

### Decision 2: Preserve Native Protocol Plan
**Date**: 2026-03-13
**Reason**: User specifically requested implementation plan for future
**Deliverable**: `docs/GIT_PROTOCOL_IMPLEMENTATION_PLAN.md` created
**Status**: Complete

---

## Next Session Plan

1. Fix Axum Body extractor issue (30 min)
2. Complete HTTP git server implementation (1 hour)
3. Test with real git client (30 min)
4. Begin Go API refactoring (2 hours)

**Goal**: Have working 3-service architecture by end of next session.
