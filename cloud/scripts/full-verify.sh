#!/usr/bin/env bash
# Full endpoint verification. Exits non-zero on any failure.
# Reports pass/fail per endpoint.

set -u
cd "$(dirname "$0")/.."

export DATABASE_URL='postgres://router:router_dev_pw@localhost:55432/router'
export REDIS_URL='redis://localhost:56379/0'
export CLOUD_MASTER_KEY="${CLOUD_MASTER_KEY:-+rB3s6hXL3gKZmhnywQQeMNUwiWSLkIo7wJRWa3BrY4=}"
export JWT_SECRET='verify-jwt-secret'
export MOCK_OAUTH_CLIENT_ID='cid-mock'

PASS=0
FAIL=0
RESULTS=()

assert_eq() {
  local label="$1" expected="$2" got="$3"
  if [[ "$got" == "$expected" ]]; then
    PASS=$((PASS+1))
    RESULTS+=("  ✓ $label")
  else
    FAIL=$((FAIL+1))
    RESULTS+=("  ✗ $label  expected=$expected  got=$got")
  fi
}

assert_contains() {
  local label="$1" needle="$2" haystack="$3"
  if [[ "$haystack" == *"$needle"* ]]; then
    PASS=$((PASS+1))
    RESULTS+=("  ✓ $label")
  else
    FAIL=$((FAIL+1))
    RESULTS+=("  ✗ $label  needle '$needle' not found in: ${haystack:0:200}")
  fi
}

# ── reset
docker exec 9router-cloud-redis redis-cli FLUSHALL > /dev/null
docker exec 9router-cloud-pg psql -U router -d router -c \
  "TRUNCATE tenants, users, api_keys, connections, combos, pricing, usage_events, usage_summaries RESTART IDENTITY CASCADE" \
  > /dev/null 2>&1

# ── start everything
node scripts/fake-upstream.mjs        > /tmp/v-fake.log  2>&1 &  P_FAKE=$!
node scripts/flaky-upstream.mjs       > /tmp/v-flaky.log 2>&1 &  P_FLAKY=$!
node scripts/mock-oauth-provider.mjs  > /tmp/v-oauth.log 2>&1 &  P_OAUTH=$!
node admin/src/server.js              > /tmp/v-admin.log 2>&1 &  P_ADMIN=$!
node router/src/server.js             > /tmp/v-router.log 2>&1 & P_ROUTER=$!
node admin-ui/server.mjs              > /tmp/v-ui.log     2>&1 & P_UI=$!
trap 'kill $P_FAKE $P_FLAKY $P_OAUTH $P_ADMIN $P_ROUTER $P_UI 2>/dev/null; wait 2>/dev/null' EXIT
sleep 1

echo "════════════════════════════════════════════════════════════════"
echo "  1. Health endpoints"
echo "════════════════════════════════════════════════════════════════"
RES=$(curl -s http://localhost:30200/health)
assert_eq "admin /health" '{"ok":true}' "$RES"
RES=$(curl -s -o /dev/null -w '%{http_code}' http://localhost:30100/health)
assert_eq "router /health" '200' "$RES"
RES=$(curl -s -o /dev/null -w '%{http_code}' http://localhost:30300/)
assert_eq "admin-ui /" '200' "$RES"

echo "════════════════════════════════════════════════════════════════"
echo "  2. /auth (signup, login, me, error paths)"
echo "════════════════════════════════════════════════════════════════"
# 2.1 signup (tenant A)
RES_A=$(curl -s -X POST http://localhost:30200/auth/signup \
  -H 'content-type: application/json' \
  -d '{"email":"alice@v.test","password":"hunter22sercure","tenantName":"A Co"}')
TOK_A=$(echo "$RES_A" | python3 -c "import sys,json; print(json.load(sys.stdin).get('token',''))")
assert_contains "POST /auth/signup A" '"role":"owner"' "$RES_A"
assert_contains "POST /auth/signup A returns JWT" 'eyJhbGc' "$TOK_A"

# 2.2 duplicate email rejected
RES=$(curl -s -o /dev/null -w '%{http_code}' -X POST http://localhost:30200/auth/signup \
  -H 'content-type: application/json' \
  -d '{"email":"alice@v.test","password":"hunter22sercure"}')
assert_eq "POST /auth/signup duplicate → 400" '400' "$RES"

# 2.3 bad password
RES=$(curl -s -o /dev/null -w '%{http_code}' -X POST http://localhost:30200/auth/signup \
  -H 'content-type: application/json' \
  -d '{"email":"x@y.z","password":"short"}')
assert_eq "POST /auth/signup short pw → 400" '400' "$RES"

# 2.4 signup tenant B (for isolation tests)
RES_B=$(curl -s -X POST http://localhost:30200/auth/signup \
  -H 'content-type: application/json' \
  -d '{"email":"bob@v.test","password":"hunter22sercure","tenantName":"B Co"}')
TOK_B=$(echo "$RES_B" | python3 -c "import sys,json; print(json.load(sys.stdin).get('token',''))")
assert_contains "POST /auth/signup B" '"role":"owner"' "$RES_B"

# 2.5 login
RES=$(curl -s -X POST http://localhost:30200/auth/login \
  -H 'content-type: application/json' \
  -d '{"email":"alice@v.test","password":"hunter22sercure"}')
assert_contains "POST /auth/login OK" '"token"' "$RES"

# 2.6 wrong pw
RES=$(curl -s -o /dev/null -w '%{http_code}' -X POST http://localhost:30200/auth/login \
  -H 'content-type: application/json' \
  -d '{"email":"alice@v.test","password":"wrong-pw-here"}')
assert_eq "POST /auth/login wrong pw → 401" '401' "$RES"

# 2.7 me with token
RES=$(curl -s http://localhost:30200/auth/me -H "Authorization: Bearer $TOK_A")
assert_contains "GET /auth/me OK" '"email":"alice@v.test"' "$RES"

# 2.8 me without token
RES=$(curl -s -o /dev/null -w '%{http_code}' http://localhost:30200/auth/me)
assert_eq "GET /auth/me no token → 401" '401' "$RES"

# 2.9 me with bogus token
RES=$(curl -s -o /dev/null -w '%{http_code}' http://localhost:30200/auth/me -H "Authorization: Bearer x.y.z")
assert_eq "GET /auth/me bogus token → 401" '401' "$RES"

echo "════════════════════════════════════════════════════════════════"
echo "  3. /api/keys (CRUD + revoke + cache invalidation)"
echo "════════════════════════════════════════════════════════════════"
# 3.1 create
RES=$(curl -s -X POST http://localhost:30200/api/keys \
  -H "Authorization: Bearer $TOK_A" -H 'content-type: application/json' \
  -d '{"name":"prod","rateLimitRpm":100}')
KEY_A=$(echo "$RES" | python3 -c "import sys,json; print(json.load(sys.stdin).get('key',''))")
KID_A=$(echo "$RES" | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))")
assert_contains "POST /api/keys A returns plaintext" 'sk-9r-' "$KEY_A"

# 3.2 list (A sees 1)
RES=$(curl -s http://localhost:30200/api/keys -H "Authorization: Bearer $TOK_A")
COUNT=$(echo "$RES" | python3 -c "import sys,json; print(len(json.load(sys.stdin)['items']))")
assert_eq "GET /api/keys A count=1" '1' "$COUNT"

# 3.3 tenant B cannot see A's keys
RES=$(curl -s http://localhost:30200/api/keys -H "Authorization: Bearer $TOK_B")
COUNT=$(echo "$RES" | python3 -c "import sys,json; print(len(json.load(sys.stdin)['items']))")
assert_eq "GET /api/keys B count=0 (isolation)" '0' "$COUNT"

# 3.4 revoke
RES=$(curl -s -o /dev/null -w '%{http_code}' -X DELETE \
  http://localhost:30200/api/keys/$KID_A -H "Authorization: Bearer $TOK_A")
assert_eq "DELETE /api/keys/:id → 204" '204' "$RES"

# 3.5 use revoked key against router → 401 (cache invalidation)
RES=$(curl -s -o /dev/null -w '%{http_code}' -X POST http://localhost:30100/v1/chat/completions \
  -H "Authorization: Bearer $KEY_A" -H 'content-type: application/json' \
  -d '{"model":"mock:mock-model-1","messages":[{"role":"user","content":"x"}]}')
assert_eq "router rejects revoked key → 401" '401' "$RES"

# 3.6 create fresh key for further tests
RES=$(curl -s -X POST http://localhost:30200/api/keys \
  -H "Authorization: Bearer $TOK_A" -H 'content-type: application/json' \
  -d '{"name":"main"}')
KEY_A=$(echo "$RES" | python3 -c "import sys,json; print(json.load(sys.stdin).get('key',''))")

# Tenant B key
RES=$(curl -s -X POST http://localhost:30200/api/keys \
  -H "Authorization: Bearer $TOK_B" -H 'content-type: application/json' \
  -d '{"name":"b-main"}')
KEY_B=$(echo "$RES" | python3 -c "import sys,json; print(json.load(sys.stdin).get('key',''))")
assert_contains "POST /api/keys B" 'sk-9r-' "$KEY_B"

echo "════════════════════════════════════════════════════════════════"
echo "  4. /api/connections (CRUD + isolation)"
echo "════════════════════════════════════════════════════════════════"
# 4.1 A: create mock + flaky
RES=$(curl -s -X POST http://localhost:30200/api/connections \
  -H "Authorization: Bearer $TOK_A" -H 'content-type: application/json' \
  -d '{"provider":"mock","name":"main","authType":"api_key","credentials":{"api_key":"fake-upstream-key"},"metadata":{"base_url":"http://localhost:31999"}}')
CID_A_MOCK=$(echo "$RES" | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))")
assert_contains "POST /api/connections A mock" '"provider":"mock"' "$RES"

RES=$(curl -s -X POST http://localhost:30200/api/connections \
  -H "Authorization: Bearer $TOK_A" -H 'content-type: application/json' \
  -d '{"provider":"flaky","name":"flaky-1","authType":"api_key","credentials":{"api_key":"k"},"metadata":{"base_url":"http://localhost:31998"}}')
assert_contains "POST /api/connections A flaky" '"provider":"flaky"' "$RES"

# 4.2 list
RES=$(curl -s http://localhost:30200/api/connections -H "Authorization: Bearer $TOK_A")
COUNT=$(echo "$RES" | python3 -c "import sys,json; print(len(json.load(sys.stdin)['items']))")
assert_eq "GET /api/connections A count=2" '2' "$COUNT"

# 4.3 tenant isolation
RES=$(curl -s http://localhost:30200/api/connections -H "Authorization: Bearer $TOK_B")
COUNT=$(echo "$RES" | python3 -c "import sys,json; print(len(json.load(sys.stdin)['items']))")
assert_eq "GET /api/connections B count=0 (isolation)" '0' "$COUNT"

# 4.4 PATCH (disable)
RES=$(curl -s -X PATCH http://localhost:30200/api/connections/$CID_A_MOCK \
  -H "Authorization: Bearer $TOK_A" -H 'content-type: application/json' \
  -d '{"enabled":false}')
assert_contains "PATCH /api/connections/:id disable" '"enabled":false' "$RES"

# Re-enable for upcoming chat tests
curl -s -X PATCH http://localhost:30200/api/connections/$CID_A_MOCK \
  -H "Authorization: Bearer $TOK_A" -H 'content-type: application/json' \
  -d '{"enabled":true}' > /dev/null

# 4.5 Try to delete with B's token (should 404, isolation)
RES=$(curl -s -o /dev/null -w '%{http_code}' -X DELETE \
  http://localhost:30200/api/connections/$CID_A_MOCK -H "Authorization: Bearer $TOK_B")
assert_eq "DELETE /api/connections cross-tenant → 404" '404' "$RES"

# 4.6 validation: missing credentials
RES=$(curl -s -o /dev/null -w '%{http_code}' -X POST http://localhost:30200/api/connections \
  -H "Authorization: Bearer $TOK_A" -H 'content-type: application/json' \
  -d '{"provider":"x","name":"x"}')
assert_eq "POST /api/connections no creds → 400" '400' "$RES"

echo "════════════════════════════════════════════════════════════════"
echo "  5. /api/combos (CRUD)"
echo "════════════════════════════════════════════════════════════════"
# 5.1 create
RES=$(curl -s -X POST http://localhost:30200/api/combos \
  -H "Authorization: Bearer $TOK_A" -H 'content-type: application/json' \
  -d '{"slug":"smart","nodes":[{"provider":"flaky","model":"f"},{"provider":"mock","model":"mock-model-1"}]}')
assert_contains "POST /api/combos" '"slug":"smart"' "$RES"

# 5.2 list
RES=$(curl -s http://localhost:30200/api/combos -H "Authorization: Bearer $TOK_A")
COUNT=$(echo "$RES" | python3 -c "import sys,json; print(len(json.load(sys.stdin)['items']))")
assert_eq "GET /api/combos count=1" '1' "$COUNT"

# 5.3 PATCH
RES=$(curl -s -X PATCH http://localhost:30200/api/combos/smart \
  -H "Authorization: Bearer $TOK_A" -H 'content-type: application/json' \
  -d '{"name":"Smart Renamed"}')
assert_contains "PATCH /api/combos/:slug" '"name":"Smart Renamed"' "$RES"

# 5.4 invalid: empty nodes
RES=$(curl -s -o /dev/null -w '%{http_code}' -X POST http://localhost:30200/api/combos \
  -H "Authorization: Bearer $TOK_A" -H 'content-type: application/json' \
  -d '{"slug":"bad","nodes":[]}')
assert_eq "POST /api/combos empty nodes → 400" '400' "$RES"

# 5.5 invalid slug
RES=$(curl -s -o /dev/null -w '%{http_code}' -X POST http://localhost:30200/api/combos \
  -H "Authorization: Bearer $TOK_A" -H 'content-type: application/json' \
  -d '{"slug":"BAD slug!","nodes":[{"provider":"x","model":"y"}]}')
assert_eq "POST /api/combos bad slug → 400" '400' "$RES"

echo "════════════════════════════════════════════════════════════════"
echo "  6. /v1/chat/completions (OpenAI compat)"
echo "════════════════════════════════════════════════════════════════"
# 6.1 non-stream
RES=$(curl -s -X POST http://localhost:30100/v1/chat/completions \
  -H "Authorization: Bearer $KEY_A" -H 'content-type: application/json' \
  -d '{"model":"mock:mock-model-1","messages":[{"role":"user","content":"hello"}]}')
assert_contains "POST /v1/chat/completions non-stream" 'pong: hello' "$RES"

# 6.2 stream
RES=$(curl -sN -X POST http://localhost:30100/v1/chat/completions \
  -H "Authorization: Bearer $KEY_A" -H 'content-type: application/json' \
  -d '{"model":"mock:mock-model-1","messages":[{"role":"user","content":"stream"}],"stream":true}')
assert_contains "POST /v1/chat/completions stream returns [DONE]" '[DONE]' "$RES"
assert_contains "POST /v1/chat/completions stream returns chunks" 'chat.completion.chunk' "$RES"

# 6.3 combo fallback (use the combo we created)
RES=$(curl -s -X POST http://localhost:30100/v1/chat/completions \
  -H "Authorization: Bearer $KEY_A" -H 'content-type: application/json' \
  -d '{"model":"combo:smart","messages":[{"role":"user","content":"fallback test"}]}')
assert_contains "POST /v1/chat/completions combo: fallback works" 'pong: fallback test' "$RES"

# 6.4 cooldown set after flaky 500
EXISTS=$(docker exec 9router-cloud-redis redis-cli EXISTS "cooldown:1:flaky:2")
assert_eq "Redis cooldown set after flaky 500" '1' "$EXISTS"

# 6.5 missing model
RES=$(curl -s -o /dev/null -w '%{http_code}' -X POST http://localhost:30100/v1/chat/completions \
  -H "Authorization: Bearer $KEY_A" -H 'content-type: application/json' -d '{}')
assert_eq "POST /v1/chat/completions no model → 400" '400' "$RES"

# 6.6 unknown combo
RES=$(curl -s -o /dev/null -w '%{http_code}' -X POST http://localhost:30100/v1/chat/completions \
  -H "Authorization: Bearer $KEY_A" -H 'content-type: application/json' \
  -d '{"model":"combo:nonexistent","messages":[{"role":"user","content":"hi"}]}')
assert_eq "POST /v1/chat/completions unknown combo → 400" '400' "$RES"

echo "════════════════════════════════════════════════════════════════"
echo "  7. /v1/messages (Anthropic compat)"
echo "════════════════════════════════════════════════════════════════"
# 7.1 Authorization header
RES=$(curl -s -X POST http://localhost:30100/v1/messages \
  -H "Authorization: Bearer $KEY_A" -H 'content-type: application/json' \
  -d '{"model":"mock:mock-model-1","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}')
assert_contains "POST /v1/messages (Authorization)" '"type":"message"' "$RES"
assert_contains "POST /v1/messages has stop_reason" '"stop_reason":"end_turn"' "$RES"

# 7.2 x-api-key header (Anthropic SDK default)
RES=$(curl -s -X POST http://localhost:30100/v1/messages \
  -H "x-api-key: $KEY_A" -H 'content-type: application/json' \
  -d '{"model":"mock:mock-model-1","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}')
assert_contains "POST /v1/messages (x-api-key)" '"type":"message"' "$RES"

# 7.3 stream
RES=$(curl -sN -X POST http://localhost:30100/v1/messages \
  -H "x-api-key: $KEY_A" -H 'content-type: application/json' \
  -d '{"model":"mock:mock-model-1","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"s"}]}')
assert_contains "POST /v1/messages stream message_start" 'event: message_start' "$RES"
assert_contains "POST /v1/messages stream content_block_delta" 'event: content_block_delta' "$RES"
assert_contains "POST /v1/messages stream message_stop" 'event: message_stop' "$RES"

# 7.4 with system prompt
RES=$(curl -s -X POST http://localhost:30100/v1/messages \
  -H "x-api-key: $KEY_A" -H 'content-type: application/json' \
  -d '{"model":"mock:mock-model-1","max_tokens":100,"system":"You are concise","messages":[{"role":"user","content":"hi"}]}')
assert_contains "POST /v1/messages with system" '"type":"text"' "$RES"

# 7.5 tenant B key cannot use tenant A's connections (key works but no connections → 503)
RES=$(curl -s -o /dev/null -w '%{http_code}' -X POST http://localhost:30100/v1/messages \
  -H "x-api-key: $KEY_B" -H 'content-type: application/json' \
  -d '{"model":"mock:mock-model-1","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}')
assert_eq "POST /v1/messages tenant B with no connections → 503" '503' "$RES"

echo "════════════════════════════════════════════════════════════════"
echo "  8. Rate limiting"
echo "════════════════════════════════════════════════════════════════"
# 8.1 create strict key (rpm=2)
docker exec 9router-cloud-redis redis-cli FLUSHALL > /dev/null
sleep 0.2 # avoid mid-second boundary
RES=$(curl -s -X POST http://localhost:30200/api/keys \
  -H "Authorization: Bearer $TOK_A" -H 'content-type: application/json' \
  -d '{"name":"strict","rateLimitRpm":2}')
STRICT_KEY=$(echo "$RES" | python3 -c "import sys,json; print(json.load(sys.stdin).get('key',''))")
CODE1=$(curl -s -o /dev/null -w '%{http_code}' -X POST http://localhost:30100/v1/chat/completions \
  -H "Authorization: Bearer $STRICT_KEY" -H 'content-type: application/json' \
  -d '{"model":"mock:mock-model-1","messages":[{"role":"user","content":"x"}]}')
CODE2=$(curl -s -o /dev/null -w '%{http_code}' -X POST http://localhost:30100/v1/chat/completions \
  -H "Authorization: Bearer $STRICT_KEY" -H 'content-type: application/json' \
  -d '{"model":"mock:mock-model-1","messages":[{"role":"user","content":"x"}]}')
CODE3=$(curl -s -o /dev/null -w '%{http_code}' -X POST http://localhost:30100/v1/chat/completions \
  -H "Authorization: Bearer $STRICT_KEY" -H 'content-type: application/json' \
  -d '{"model":"mock:mock-model-1","messages":[{"role":"user","content":"x"}]}')
assert_eq "rate limit: req 1 → 200" '200' "$CODE1"
assert_eq "rate limit: req 2 → 200" '200' "$CODE2"
assert_eq "rate limit: req 3 → 429" '429' "$CODE3"

echo "════════════════════════════════════════════════════════════════"
echo "  9. /api/usage"
echo "════════════════════════════════════════════════════════════════"
RES=$(curl -s "http://localhost:30200/api/usage/summary?hours=1" -H "Authorization: Bearer $TOK_A")
assert_contains "GET /api/usage/summary has mock" '"provider":"mock"' "$RES"
RES=$(curl -s "http://localhost:30200/api/usage/recent?limit=10" -H "Authorization: Bearer $TOK_A")
COUNT=$(echo "$RES" | python3 -c "import sys,json; print(len(json.load(sys.stdin)['items']))")
[[ "$COUNT" -gt 0 ]] && { PASS=$((PASS+1)); RESULTS+=("  ✓ GET /api/usage/recent count>0  (got $COUNT)"); } \
                    || { FAIL=$((FAIL+1)); RESULTS+=("  ✗ GET /api/usage/recent count=$COUNT"); }

# Tenant B sees nothing (isolation)
RES=$(curl -s "http://localhost:30200/api/usage/recent?limit=10" -H "Authorization: Bearer $TOK_B")
COUNT=$(echo "$RES" | python3 -c "import sys,json; print(len(json.load(sys.stdin)['items']))")
assert_eq "GET /api/usage/recent B count=0 (isolation)" '0' "$COUNT"

echo "════════════════════════════════════════════════════════════════"
echo "  10. /api/oauth (PKCE flow)"
echo "════════════════════════════════════════════════════════════════"
RES=$(curl -s http://localhost:30200/api/oauth/providers -H "Authorization: Bearer $TOK_A")
assert_contains "GET /api/oauth/providers" '"provider":"mock"' "$RES"

START=$(curl -s -X POST http://localhost:30200/api/oauth/mock/start \
  -H "Authorization: Bearer $TOK_A" -H 'content-type: application/json' \
  -d '{"connectionName":"oa-acc","redirectUri":"http://localhost:30200/api/oauth/callback"}')
AUTHURL=$(echo "$START" | python3 -c "import sys,json; print(json.load(sys.stdin)['authorizeUrl'])")
assert_contains "POST /api/oauth/mock/start" 'code_challenge=' "$AUTHURL"

# Follow authorize → callback
curl -s -L "$AUTHURL" > /dev/null
RES=$(docker exec 9router-cloud-pg psql -U router -d router -tA -c \
  "SELECT count(*) FROM connections WHERE tenant_id=1 AND provider='mock' AND auth_type='oauth'")
assert_eq "OAuth callback created connection" '1' "$RES"

# Test refresher rotates the token
TOKEN_BEFORE=$(docker exec 9router-cloud-pg psql -U router -d router -tA -c \
  "SELECT credentials_encrypted FROM connections WHERE tenant_id=1 AND auth_type='oauth' AND provider='mock'")
# Force expires_at to be near now so refresher picks it up
docker exec 9router-cloud-pg psql -U router -d router -c \
  "UPDATE connections SET oauth_expires_at = now() + interval '10 seconds' WHERE tenant_id=1 AND auth_type='oauth' AND provider='mock'" > /dev/null
node -e "
const { refreshOnce, registerRefresher } = await import('./worker/src/jobs/tokenRefresher.js');
const { refreshOpenAiOAuth } = await import('./worker/src/jobs/refreshers/openaiOAuth.js');
registerRefresher('mock', refreshOpenAiOAuth);
const n = await refreshOnce();
process.exit(n === 1 ? 0 : 1);
" --input-type=module > /dev/null 2>&1
REFRESH_RC=$?
[[ $REFRESH_RC -eq 0 ]] && { PASS=$((PASS+1)); RESULTS+=("  ✓ tokenRefresher rotated 1 row"); } \
                       || { FAIL=$((FAIL+1)); RESULTS+=("  ✗ tokenRefresher did not refresh"); }
TOKEN_AFTER=$(docker exec 9router-cloud-pg psql -U router -d router -tA -c \
  "SELECT credentials_encrypted FROM connections WHERE tenant_id=1 AND auth_type='oauth' AND provider='mock'")
[[ "$TOKEN_BEFORE" != "$TOKEN_AFTER" ]] && { PASS=$((PASS+1)); RESULTS+=("  ✓ encrypted credentials blob changed"); } \
                                       || { FAIL=$((FAIL+1)); RESULTS+=("  ✗ blob unchanged after refresh"); }

echo "════════════════════════════════════════════════════════════════"
echo "  11. Usage aggregator job"
echo "════════════════════════════════════════════════════════════════"
node -e "
const m = await import('./worker/src/jobs/usageAggregator.js');
const n = await m.aggregateOnce();
console.log(n);
process.exit(0);
" --input-type=module > /tmp/v-agg.out 2>&1
N=$(grep -E '^[0-9]+$' /tmp/v-agg.out | tail -1)
[[ "$N" -gt 0 ]] && { PASS=$((PASS+1)); RESULTS+=("  ✓ aggregator produced $N summary rows"); } \
                || { FAIL=$((FAIL+1)); RESULTS+=("  ✗ aggregator produced 0 rows"); }

# Tenant A summary exists; tenant B summary does NOT
COUNT_A=$(docker exec 9router-cloud-pg psql -U router -d router -tA -c "SELECT count(*) FROM usage_summaries WHERE tenant_id=1")
COUNT_B=$(docker exec 9router-cloud-pg psql -U router -d router -tA -c "SELECT count(*) FROM usage_summaries WHERE tenant_id=2")
[[ "$COUNT_A" -gt 0 ]] && { PASS=$((PASS+1)); RESULTS+=("  ✓ usage_summaries has tenant 1 rows ($COUNT_A)"); } \
                      || { FAIL=$((FAIL+1)); RESULTS+=("  ✗ tenant 1 has 0 summary rows"); }
assert_eq "usage_summaries tenant 2 has 0 rows (no usage)" '0' "$COUNT_B"

echo "════════════════════════════════════════════════════════════════"
echo "  12. Admin UI static assets"
echo "════════════════════════════════════════════════════════════════"
RES=$(curl -s -o /dev/null -w '%{http_code}' http://localhost:30300/app.js)
assert_eq "GET /app.js" '200' "$RES"
RES=$(curl -s -o /dev/null -w '%{http_code}' http://localhost:30300/styles.css)
assert_eq "GET /styles.css" '200' "$RES"
RES=$(curl -s -o /dev/null -w '%{http_code}' http://localhost:30300/some/unknown/spa/route)
assert_eq "GET /unknown → 200 SPA fallback" '200' "$RES"

# CORS preflight
RES=$(curl -s -o /dev/null -w '%{http_code}' -X OPTIONS http://localhost:30200/api/keys \
  -H 'origin: http://localhost:30300' -H 'access-control-request-method: GET')
assert_eq "CORS preflight on admin" '204' "$RES"

echo
echo "════════════════════════════════════════════════════════════════"
echo "  FINAL RESULTS"
echo "════════════════════════════════════════════════════════════════"
for line in "${RESULTS[@]}"; do
  echo "$line"
done
echo
echo "  PASS: $PASS"
echo "  FAIL: $FAIL"
echo "════════════════════════════════════════════════════════════════"
[[ $FAIL -eq 0 ]] && exit 0 || exit 1
