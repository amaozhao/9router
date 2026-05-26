#!/usr/bin/env bash
# End-to-end verification for per-tenant auto model switching.
# Boots fake-upstream + admin + router. Signs up two tenants, configures
# different routing per tenant, asserts each scenario lands on the right
# upstream and cross-tenant isolation holds.
#
# Expects Postgres + Redis already running (docker container fastworkroom-postgres + 9router-redis).

set -u
cd "$(dirname "$0")/.."

export DATABASE_URL="${DATABASE_URL:-postgres://router:router_dev_pw@localhost:55432/router}"
export REDIS_URL="${REDIS_URL:-redis://localhost:56379/0}"
export CLOUD_MASTER_KEY="${CLOUD_MASTER_KEY:-+rB3s6hXL3gKZmhnywQQeMNUwiWSLkIo7wJRWa3BrY4=}"
export JWT_SECRET='verify-auto-routing'
# Fixed bootstrap super-admin email — used to mint invite codes for the
# scratch tenants A/B created below.
export SUPER_ADMIN_EMAILS='verify-routing-admin@x.io'

# Container names (overridable for non-default deployments)
PG_CONTAINER="${PG_CONTAINER:-9router-cloud-pg}"
REDIS_CONTAINER="${REDIS_CONTAINER:-9router-cloud-redis}"

# Free our ports so any existing process doesn't intercept (otherwise admin/
# router from a separate 'live' session would receive these test requests).
for port in 30100 30200 40991; do
  PIDS=$(lsof -ti:$port 2>/dev/null)
  [ -n "$PIDS" ] && kill $PIDS 2>/dev/null
done
sleep 0.4

PASS=0; FAIL=0; RESULTS=()

assert_eq() {
  local label="$1" expected="$2" got="$3"
  if [[ "$got" == "$expected" ]]; then
    PASS=$((PASS+1)); RESULTS+=("  ✓ $label")
  else
    FAIL=$((FAIL+1)); RESULTS+=("  ✗ $label  expected=$expected  got=$got")
  fi
}

cleanup() {
  [[ -n "${P_FAKE:-}" ]]   && kill "$P_FAKE"   2>/dev/null || true
  [[ -n "${P_ADMIN:-}" ]]  && kill "$P_ADMIN"  2>/dev/null || true
  [[ -n "${P_ROUTER:-}" ]] && kill "$P_ROUTER" 2>/dev/null || true
  wait 2>/dev/null || true
}
trap cleanup EXIT

# ── Reset: drop any scratch tenants from prior runs (cascades to invite_codes via created_by FK).
docker exec "$PG_CONTAINER" psql -U router -d router -q \
  -c "DELETE FROM tenants WHERE name LIKE 'verify-routing-%' OR name LIKE 'verify-routing-admin%'" >/dev/null 2>&1 || true
docker exec "$PG_CONTAINER" psql -U router -d router -q \
  -c "DELETE FROM invite_codes WHERE note = 'verify-auto-routing'" >/dev/null 2>&1 || true

# Flush routing cache so stale keys don't bleed over.
docker exec "$REDIS_CONTAINER" redis-cli KEYS 'routing:*' \
  | xargs -r docker exec -i "$REDIS_CONTAINER" redis-cli DEL >/dev/null 2>&1 || true

# ── Boot processes
FAKE_UPSTREAM_PORT=40991 node scripts/fake-upstream.mjs > /tmp/vr-fake.log 2>&1 & P_FAKE=$!
node admin/src/server.js   > /tmp/vr-admin.log  2>&1 & P_ADMIN=$!
node router/src/server.js  > /tmp/vr-router.log 2>&1 & P_ROUTER=$!
sleep 2

# ── Verify services are up
if ! curl -sf http://localhost:30200/health >/dev/null 2>&1; then
  echo "ERROR: admin server not responding. Logs:" && cat /tmp/vr-admin.log; exit 1
fi
if ! curl -sf http://localhost:30100/health >/dev/null 2>&1; then
  echo "ERROR: router server not responding. Logs:" && cat /tmp/vr-router.log; exit 1
fi
if ! curl -sf http://localhost:40991/ >/dev/null 2>&1; then
  echo "ERROR: fake-upstream not responding. Logs:" && cat /tmp/vr-fake.log; exit 1
fi

echo "Services up. Running assertions..."

# ── Bootstrap super-admin (skips invite gate) and use it to mint invite codes.
TS=$(date +%s)
RESP_ADMIN=$(curl -s -X POST http://localhost:30200/auth/signup \
  -H 'content-type: application/json' \
  -d "{\"email\":\"verify-routing-admin@x.io\",\"password\":\"abcd1234abcd\",\"tenantName\":\"verify-routing-admin-${TS}\"}")
TOKEN_ADMIN=$(python3 -c "import sys,json; d=json.loads(sys.argv[1]); print(d.get('token',''))" "$RESP_ADMIN" 2>/dev/null)
if [[ -z "$TOKEN_ADMIN" ]]; then
  echo "ERROR: bootstrap super-admin failed: $RESP_ADMIN"; exit 1
fi

mint_invite() {
  curl -s -X POST http://localhost:30200/api/admin/invites \
    -H 'content-type: application/json' -H "Authorization: Bearer $TOKEN_ADMIN" \
    -d '{"maxUses":1,"note":"verify-auto-routing"}' \
    | python3 -c "import sys,json; print(json.load(sys.stdin)['code'])"
}

INVITE_A=$(mint_invite)
INVITE_B=$(mint_invite)

# ── Sign up tenant A (with invite)
RESP_A=$(curl -s -X POST http://localhost:30200/auth/signup \
  -H 'content-type: application/json' \
  -d "{\"email\":\"verify-routing-a-${TS}@x.io\",\"password\":\"abcd1234abcd\",\"tenantName\":\"verify-routing-a-${TS}\",\"inviteCode\":\"${INVITE_A}\"}")
TOKEN_A=$(python3 -c "import sys,json; d=json.loads(sys.argv[1]); print(d.get('token',''))" "$RESP_A" 2>/dev/null)
TENANT_A_ID=$(python3 -c "import sys,json; d=json.loads(sys.argv[1]); print(d.get('tenant',{}).get('id',''))" "$RESP_A" 2>/dev/null)
if [[ -z "$TOKEN_A" ]]; then
  echo "ERROR: signup tenant A failed: $RESP_A"; exit 1
fi

# ── Sign up tenant B (with invite)
RESP_B=$(curl -s -X POST http://localhost:30200/auth/signup \
  -H 'content-type: application/json' \
  -d "{\"email\":\"verify-routing-b-${TS}@x.io\",\"password\":\"abcd1234abcd\",\"tenantName\":\"verify-routing-b-${TS}\",\"inviteCode\":\"${INVITE_B}\"}")
TOKEN_B=$(python3 -c "import sys,json; d=json.loads(sys.argv[1]); print(d.get('token',''))" "$RESP_B" 2>/dev/null)
if [[ -z "$TOKEN_B" ]]; then
  echo "ERROR: signup tenant B failed: $RESP_B"; exit 1
fi

# ── For each tenant: create a connection pointing at fake-upstream
# base_url goes in metadata (chatCompletions.js reads account.metadata.base_url)
# api_key must match fake-upstream's Bearer check: "fake-upstream-key" prefix
make_connection() {
  local token="$1"
  curl -s -X POST http://localhost:30200/api/connections \
    -H "authorization: Bearer $token" \
    -H 'content-type: application/json' \
    -d '{"provider":"openai","name":"fake","authType":"api_key","credentials":{"api_key":"fake-upstream-key"},"metadata":{"base_url":"http://localhost:40991"}}'
}
CONN_A=$(make_connection "$TOKEN_A")
CONN_B=$(make_connection "$TOKEN_B")

CONN_A_ID=$(python3 -c "import sys,json; print(json.loads(sys.argv[1]).get('id',''))" "$CONN_A" 2>/dev/null)
CONN_B_ID=$(python3 -c "import sys,json; print(json.loads(sys.argv[1]).get('id',''))" "$CONN_B" 2>/dev/null)
if [[ -z "$CONN_A_ID" ]]; then
  echo "ERROR: create connection A failed: $CONN_A"; exit 1
fi
if [[ -z "$CONN_B_ID" ]]; then
  echo "ERROR: create connection B failed: $CONN_B"; exit 1
fi

# ── For each tenant: create a client API key
make_apikey() {
  local token="$1"
  RESP=$(curl -s -X POST http://localhost:30200/api/keys \
    -H "authorization: Bearer $token" \
    -H 'content-type: application/json' \
    -d '{"name":"verify"}')
  # apiKeys.js returns: ok(res, { ...serialize(rows[0]), key }, 201)
  python3 -c "import sys,json; d=json.loads(sys.argv[1]); print(d.get('key',''))" "$RESP" 2>/dev/null
}
SK_A=$(make_apikey "$TOKEN_A")
SK_B=$(make_apikey "$TOKEN_B")
if [[ -z "$SK_A" ]]; then
  echo "ERROR: create API key A failed"; exit 1
fi
if [[ -z "$SK_B" ]]; then
  echo "ERROR: create API key B failed"; exit 1
fi

# ── Tenant A routing: configure all 6 scenarios
# target format: "provider:model" — validated by TARGET_RE in routing.js
set_routing() {
  local token="$1" scenario="$2" target="$3"
  STATUS=$(curl -s -o /tmp/vr-routing-put.log -w '%{http_code}' \
    -X PUT "http://localhost:30200/api/routing/$scenario" \
    -H "authorization: Bearer $token" \
    -H 'content-type: application/json' \
    -d "{\"target\":\"$target\"}")
  if [[ "$STATUS" != "200" ]]; then
    echo "ERROR: PUT /api/routing/$scenario returned $STATUS: $(cat /tmp/vr-routing-put.log)"
    exit 1
  fi
}

set_routing "$TOKEN_A" "default"      "openai:fake-default"
set_routing "$TOKEN_A" "think"        "openai:fake-think"
set_routing "$TOKEN_A" "vision"       "openai:fake-vision"
set_routing "$TOKEN_A" "web"          "openai:fake-web"
set_routing "$TOKEN_A" "tool_use"     "openai:fake-tool"
set_routing "$TOKEN_A" "long_context" "openai:fake-long"

# Tenant B: only default configured
set_routing "$TOKEN_B" "default" "openai:tenant-b-default"

# Short sleep to let Redis routing cache invalidation settle
sleep 1

# ── Helper: POST to router, capture the echoed model from the JSON response body.
# fake-upstream returns: {"model": <body.model>, ...}
# The router passes this JSON through unchanged, so we can read result.model.
hit() {
  local sk="$1" body="$2"
  # Pipe body via stdin (`--data-binary @-`) so payloads larger than ARG_MAX
  # (long_context assertion uses 260K chars) don't blow up the curl argv.
  RESP=$(printf '%s' "$body" | curl -s -X POST http://localhost:30100/v1/chat/completions \
    -H "authorization: Bearer $sk" \
    -H 'content-type: application/json' \
    --data-binary @-)
  printf '%s' "$RESP" | python3 -c "import sys,json; d=json.loads(sys.stdin.read()); print(d.get('model',''))" 2>/dev/null
}

# ────────────────────────────────────────────────────────────────
# Assertion 1: Tenant A / default → fake-default
# ────────────────────────────────────────────────────────────────
M=$(hit "$SK_A" '{"model":"auto","messages":[{"role":"user","content":"hello"}]}')
assert_eq "tenantA / default → fake-default" "fake-default" "$M"

# ────────────────────────────────────────────────────────────────
# Assertion 2: Tenant A / think → fake-think  (reasoning_effort=high)
# ────────────────────────────────────────────────────────────────
M=$(hit "$SK_A" '{"model":"auto","reasoning_effort":"high","messages":[{"role":"user","content":"hello"}]}')
assert_eq "tenantA / think → fake-think" "fake-think" "$M"

# ────────────────────────────────────────────────────────────────
# Assertion 3: Tenant A / long_context → fake-long  (>200k chars)
# ────────────────────────────────────────────────────────────────
BIG_BODY=$(python3 -c "
import json, sys
big = 'a' * 260000  # > 256000 char threshold (64k tokens * 4 chars/token)
body = {'model': 'auto', 'messages': [{'role': 'user', 'content': big}]}
sys.stdout.write(json.dumps(body))
")
M=$(hit "$SK_A" "$BIG_BODY")
assert_eq "tenantA / long_context → fake-long" "fake-long" "$M"

# ────────────────────────────────────────────────────────────────
# Assertion 4: Tenant A / vision → fake-vision  (image_url content part)
# ────────────────────────────────────────────────────────────────
M=$(hit "$SK_A" '{"model":"auto","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/img.png"}}]}]}')
assert_eq "tenantA / vision → fake-vision" "fake-vision" "$M"

# ────────────────────────────────────────────────────────────────
# Assertion 5: Tenant A / tool_use → fake-tool  (non-web tool)
# ────────────────────────────────────────────────────────────────
M=$(hit "$SK_A" '{"model":"auto","tools":[{"type":"function","function":{"name":"get_weather","description":"Get weather"}}],"messages":[{"role":"user","content":"hi"}]}')
assert_eq "tenantA / tool_use → fake-tool" "fake-tool" "$M"

# ────────────────────────────────────────────────────────────────
# Assertion 6: Tenant A / web → fake-web  (web_search tool)
# ────────────────────────────────────────────────────────────────
M=$(hit "$SK_A" '{"model":"auto","tools":[{"type":"function","function":{"name":"web_search","description":"Search the web"}}],"messages":[{"role":"user","content":"hi"}]}')
assert_eq "tenantA / web → fake-web" "fake-web" "$M"

# ────────────────────────────────────────────────────────────────
# Assertion 7: Tenant B / default → tenant-b-default
# ────────────────────────────────────────────────────────────────
M=$(hit "$SK_B" '{"model":"auto","messages":[{"role":"user","content":"hello"}]}')
assert_eq "tenantB / default → tenant-b-default" "tenant-b-default" "$M"

# ────────────────────────────────────────────────────────────────
# Assertion 8: Tenant B / think falls back to default → tenant-b-default
# (B only configured 'default', so think falls back to it)
# ────────────────────────────────────────────────────────────────
M=$(hit "$SK_B" '{"model":"auto","reasoning_effort":"high","messages":[{"role":"user","content":"hello"}]}')
assert_eq "tenantB / think falls back to default → tenant-b-default" "tenant-b-default" "$M"

# ────────────────────────────────────────────────────────────────
# Assertion 9: Explicit model bypasses classifier
# model="openai:explicit-model" should skip auto-routing entirely
# ────────────────────────────────────────────────────────────────
M=$(hit "$SK_A" '{"model":"openai:explicit-model","messages":[{"role":"user","content":"hello"}]}')
assert_eq "tenantA / explicit model bypasses classifier" "explicit-model" "$M"

# ────────────────────────────────────────────────────────────────
# Assertion 10: usage_events.routed_model set when model=auto
# Scoped to tenant A, checking that the default-scenario row exists
# (routed_model='openai:fake-default'). We look for the oldest auto
# row for tenant A (assertion 1 was the first hit).
# ────────────────────────────────────────────────────────────────
ROUTED_AUTO=$(docker exec "$PG_CONTAINER" psql -U router -d router -tAc \
  "SELECT routed_model FROM usage_events WHERE model='auto' AND tenant_id=${TENANT_A_ID} ORDER BY id ASC LIMIT 1" 2>/dev/null | tr -d '[:space:]')
assert_eq "usage_events.routed_model set when model=auto" "openai:fake-default" "$ROUTED_AUTO"

# ────────────────────────────────────────────────────────────────
# Assertion 11: usage_events.routed_model is NULL when client sent
# an explicit non-auto model (classifier was bypassed). Assertion 9
# above hit the router with model='openai:explicit-model'.
# ────────────────────────────────────────────────────────────────
ROUTED_EXPLICIT=$(docker exec "$PG_CONTAINER" psql -U router -d router -tAc \
  "SELECT COALESCE(routed_model, 'NULL') FROM usage_events WHERE model='openai:explicit-model' AND tenant_id=${TENANT_A_ID} ORDER BY id DESC LIMIT 1" 2>/dev/null | tr -d '[:space:]')
assert_eq "usage_events.routed_model NULL when client sent explicit model" "NULL" "$ROUTED_EXPLICIT"

# ────────────────────────────────────────────────────────────────
# Assertion 12: Cross-tenant isolation — tenantA's routing rules
# not visible to tenantB
# ────────────────────────────────────────────────────────────────
LIST_B=$(curl -s -H "authorization: Bearer $TOKEN_B" http://localhost:30200/api/routing)
LONG_TARGET=$(python3 -c "
import sys, json
d = json.loads(sys.argv[1])
items = d.get('items', [])
match = next((i for i in items if i['scenario'] == 'long_context'), None)
print(match['target'] if match and match['target'] is not None else 'null')
" "$LIST_B" 2>/dev/null)
assert_eq "tenantB.long_context not affected by tenantA PUT (target=null)" "null" "$LONG_TARGET"

# ── Final results
echo ""
echo "════════════════════════════════════════════════════════════════"
echo "  RESULTS"
echo "════════════════════════════════════════════════════════════════"
for line in "${RESULTS[@]}"; do
  echo "$line"
done
echo ""
echo "Pass: $PASS   Fail: $FAIL"
echo "════════════════════════════════════════════════════════════════"
[[ $FAIL -eq 0 ]] && exit 0 || exit 1
