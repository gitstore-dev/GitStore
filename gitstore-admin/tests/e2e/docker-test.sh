#!/usr/bin/env bash
# End-to-end Docker compose test
# Validates: init → compose up → health checks → git push → GraphQL query → websocket notification
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PASS=0
FAIL=0
ERRORS=()

log()  { echo "[$(date '+%H:%M:%S')] $*"; }
pass() { log "  PASS: $*"; PASS=$((PASS + 1)); }
fail() { log "  FAIL: $*"; ERRORS+=("$*"); FAIL=$((FAIL + 1)); }

# ─── Health polling ───────────────────────────────────────────────────────────
wait_healthy() {
  local url="$1" label="$2" timeout="${3:-60}"
  log "Waiting for $label ($url) ..."
  local elapsed=0
  until curl -sf "$url" >/dev/null 2>&1; do
    sleep 2; elapsed=$((elapsed + 2))
    if [ "$elapsed" -ge "$timeout" ]; then
      fail "$label did not become healthy within ${timeout}s"
      return 1
    fi
  done
  pass "$label healthy"
}

# ─── Cleanup ─────────────────────────────────────────────────────────────────
cleanup() {
  log "Stopping Docker compose ..."
  docker compose -f "$REPO_ROOT/compose.yml" down --remove-orphans --volumes >/dev/null 2>&1 || true
}
trap cleanup EXIT

# ─── Step 1: Initialize demo catalog ─────────────────────────────────────────
log "Step 1: Initialize demo catalog"
DATA_DIR="$(mktemp -d)"
"$REPO_ROOT/scripts/init-demo-catalog.sh" --data-dir "$DATA_DIR"
pass "Demo catalog initialized at $DATA_DIR"

# ─── Step 2: Start Docker compose ────────────────────────────────────────────
log "Step 2: Starting Docker compose"
GITSTORE_DATA_DIR="$DATA_DIR" docker compose -f "$REPO_ROOT/compose.yml" up -d --build
pass "Docker compose started"

# ─── Step 3: Wait for health checks ──────────────────────────────────────────
log "Step 3: Waiting for services to become healthy"
wait_healthy "http://localhost:9418/health"    "git-server"  90
wait_healthy "http://localhost:4000/health"    "api"         90
wait_healthy "http://localhost:3000"           "admin-ui"    90

# ─── Step 4: Verify websocket health ─────────────────────────────────────────
log "Step 4: Check websocket health endpoint"
WS_HEALTH=$(curl -sf "http://localhost:9418/websocket/health" 2>/dev/null || echo '{}')
WS_STATUS=$(echo "$WS_HEALTH" | grep -o '"status":"[^"]*"' | head -1 | cut -d'"' -f4)
if [ "$WS_STATUS" = "healthy" ]; then
  pass "Websocket health: $WS_STATUS"
else
  fail "Websocket health returned unexpected status: $WS_STATUS (body: $WS_HEALTH)"
fi

# ─── Step 5: Query GraphQL for products ──────────────────────────────────────
log "Step 5: Query GraphQL API for products"
GQL_RESPONSE=$(curl -sf -X POST http://localhost:4000/graphql \
  -H "Content-Type: application/json" \
  -d '{"query":"{ products { edges { node { id sku title } } } }"}' 2>/dev/null || echo '{}')

if echo "$GQL_RESPONSE" | grep -q '"sku"'; then
  PRODUCT_COUNT=$(echo "$GQL_RESPONSE" | grep -o '"sku"' | wc -l | tr -d ' ')
  pass "GraphQL returned $PRODUCT_COUNT product(s)"
else
  fail "GraphQL products query returned no products. Response: $GQL_RESPONSE"
fi

# ─── Step 6: Create a release tag and verify cache reload ────────────────────
log "Step 6: Create release tag and verify cache reload via API logs"
CLONE_DIR="$(mktemp -d)"
git clone "http://localhost:9418/catalog.git" "$CLONE_DIR" 2>/dev/null || {
  fail "Could not clone catalog repo"
}
cd "$CLONE_DIR"
git tag -a "v1.0.0-test" -m "E2E test release" 2>/dev/null || true
git push origin "v1.0.0-test" 2>/dev/null && pass "Release tag v1.0.0-test pushed" \
  || fail "Failed to push release tag"
cd "$REPO_ROOT"

# Allow a moment for websocket notification to propagate
sleep 3

# Verify API logs show websocket notification received
API_LOGS=$(docker compose -f "$REPO_ROOT/compose.yml" logs api 2>/dev/null || echo "")
if echo "$API_LOGS" | grep -qi "websocket\|cache.*reload\|catalog.*reload"; then
  pass "API logs show websocket/cache reload activity"
else
  fail "API logs do not show websocket notification. Check docker compose logs api"
fi

# ─── Summary ─────────────────────────────────────────────────────────────────
echo ""
echo "════════════════════════════════════════"
echo "  E2E Test Results: PASS=$PASS  FAIL=$FAIL"
echo "════════════════════════════════════════"
if [ "${#ERRORS[@]}" -gt 0 ]; then
  echo "Failures:"
  for e in "${ERRORS[@]}"; do
    echo "  ✗ $e"
  done
  exit 1
fi
echo "All tests passed."
