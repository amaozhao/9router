#!/usr/bin/env bash
# Cross-backend smoke: runs an identical API contract test against either the
# Node backend (admin/, router/, worker/) or the Go backend (cmd/admin,
# cmd/router, cmd/worker). Asserts the same observable behavior from both.
#
#   BACKEND=node  bash scripts/cross-backend-test.sh
#   BACKEND=go    bash scripts/cross-backend-test.sh
#
# Exits non-zero on any failure. PASS count printed at the end.

set -u
BACKEND="${BACKEND:-go}"
[ "$BACKEND" = "node" ] && { echo "node backend removed; only BACKEND=go supported now"; exit 2; }

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

export DATABASE_URL='postgres://router:router_dev_pw@localhost:55432/router'
export REDIS_URL='redis://localhost:56379/0'
export CLOUD_MASTER_KEY="+rB3s6hXL3gKZmhnywQQeMNUwiWSLkIo7wJRWa3BrY4="
export JWT_SECRET='cross-backend-test'
export SUPER_ADMIN_EMAILS='alice@v.test'
export PATH="$HOME/.local/go/bin:$PATH"

ADMIN_PORT=30200
ROUTER_PORT=30100
FAKE_PORT=31999

PASS=0
FAIL=0
ok()  { echo "  ✓ $1"; PASS=$((PASS+1)); }
bad() { echo "  ✗ $1 ($2)"; FAIL=$((FAIL+1)); }
assert_eq() { local label="$1" exp="$2" got="$3"; [ "$exp" = "$got" ] && ok "$label" || bad "$label" "expected '$exp', got '$got'"; }
assert_contains() { local label="$1" needle="$2" hay="$3"; echo "$hay" | grep -q "$needle" && ok "$label" || bad "$label" "missing '$needle' in: $hay"; }
J() { python3 -c "import sys,json,os; d=json.load(sys.stdin); k=os.environ['K']; r=d; [r:=(r[p] if isinstance(r,dict) else r) for p in k.split('.')]; print(r if not isinstance(r,(list,dict)) else json.dumps(r))"; }
jq_get() { K="$1" python3 -c "import sys,json,os
d=json.load(sys.stdin)
k=os.environ['K']
r=d
for p in k.split('.'):
    if isinstance(r,dict): r=r.get(p)
    elif isinstance(r,list): r=r[int(p)]
print(r if not isinstance(r,(list,dict)) else json.dumps(r))" 2>/dev/null; }

# Pre-flight
for p in $ADMIN_PORT $ROUTER_PORT $FAKE_PORT; do
  PIDS=$(lsof -ti:$p 2>/dev/null || true)
  [ -n "$PIDS" ] && kill -9 $PIDS 2>/dev/null || true
done
docker exec lazirouter-cloud-pg psql -U router -d router -c "TRUNCATE tenants RESTART IDENTITY CASCADE; TRUNCATE invite_codes RESTART IDENTITY CASCADE;" > /dev/null 2>&1
docker exec lazirouter-cloud-redis redis-cli FLUSHALL > /dev/null

# Run migrations (Go binary if available; else Node)
if [ -x /home/amaozhao/.local/go/bin/go ] || command -v go >/dev/null 2>&1; then
  (cd "$ROOT" && go run ./cmd/migrate) > /tmp/cbt-migrate.log 2>&1 || true
elif [ -f "$ROOT/scripts/migrate.mjs" ]; then
  (cd "$ROOT/scripts" && node migrate.mjs) > /tmp/cbt-migrate.log 2>&1 || true
fi

# Start fake upstream
node -e "
const http=require('http');
http.createServer((req,res)=>{
  let buf='';req.on('data',c=>buf+=c);req.on('end',()=>{
    const body=JSON.parse(buf||'{}');
    if (body.stream===true) {
      res.writeHead(200,{'content-type':'text/event-stream'});
      res.write('data: {\"id\":\"x\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n');
      res.write('data: {\"id\":\"x\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":2}}\n\n');
      res.write('data: [DONE]\n\n'); res.end();
    } else {
      res.writeHead(200,{'content-type':'application/json'});
      res.end(JSON.stringify({id:'x',object:'chat.completion',model:body.model,choices:[{message:{role:'assistant',content:'hi'}}],usage:{prompt_tokens:4,completion_tokens:2,total_tokens:6}}));
    }
  });
}).listen($FAKE_PORT);
" > /tmp/cbt-fake.log 2>&1 &
P_FAKE=$!

# Start backend
echo "═══ backend=$BACKEND ═══"
if [ "$BACKEND" = "node" ]; then
  node admin/src/server.js   > /tmp/cbt-admin.log 2>&1 &  P_ADMIN=$!
  node router/src/server.js  > /tmp/cbt-router.log 2>&1 & P_ROUTER=$!
elif [ "$BACKEND" = "go" ]; then
  PORT=$ADMIN_PORT  go run ./cmd/admin  > /tmp/cbt-admin.log 2>&1 & P_ADMIN=$!
  PORT=$ROUTER_PORT go run ./cmd/router > /tmp/cbt-router.log 2>&1 & P_ROUTER=$!
else
  echo "Unknown BACKEND=$BACKEND (use node|go)"; exit 2
fi
trap 'kill $P_FAKE $P_ADMIN $P_ROUTER 2>/dev/null; wait 2>/dev/null' EXIT

# Wait for health
for i in 1 2 3 4 5 6 7 8 9 10; do
  curl -s localhost:$ADMIN_PORT/health   >/dev/null 2>&1 && \
  curl -s localhost:$ROUTER_PORT/health  >/dev/null 2>&1 && break
  sleep 1
done

# ── Tests ──────────────────────────────────────────────────────────────────
echo
echo "1) auth"
ALICE=$(curl -s -X POST localhost:$ADMIN_PORT/auth/signup -H 'content-type: application/json' \
  -d '{"email":"alice@v.test","password":"password1!","tenantName":"A"}')
TOK_A=$(echo "$ALICE" | jq_get token)
KEY_A=$(echo "$ALICE" | jq_get defaultApiKey.key)
SUPER=$(echo "$ALICE" | jq_get user.isSuperAdmin)
assert_contains "signup returns sk-lr- key" "sk-lr-" "$KEY_A"
assert_eq      "alice is super-admin"      "True"   "$SUPER"

echo
echo "2) invite mint + signup"
INV=$(curl -s -X POST localhost:$ADMIN_PORT/api/admin/invites -H "authorization: Bearer $TOK_A" -H 'content-type: application/json' -d '{"note":"x"}')
CODE=$(echo "$INV" | jq_get code)
[ -n "$CODE" ] && ok "mint invite" || bad "mint invite" "no code: $INV"
BOB=$(curl -s -X POST localhost:$ADMIN_PORT/auth/signup -H 'content-type: application/json' \
  -d "{\"email\":\"bob@v.test\",\"password\":\"password1!\",\"tenantName\":\"B\",\"inviteCode\":\"$CODE\"}")
TOK_B=$(echo "$BOB" | jq_get token)
KEY_B=$(echo "$BOB" | jq_get defaultApiKey.key)
assert_contains "bob signup with invite"   "sk-lr-" "$KEY_B"
REPLAY=$(curl -s -X POST localhost:$ADMIN_PORT/auth/signup -H 'content-type: application/json' \
  -d "{\"email\":\"r@v.test\",\"password\":\"password1!\",\"tenantName\":\"R\",\"inviteCode\":\"$CODE\"}")
assert_contains "invite replay rejected"   "already used" "$REPLAY"

echo
echo "3) connection + isolation"
CONN=$(curl -s -X POST localhost:$ADMIN_PORT/api/connections -H "authorization: Bearer $TOK_B" -H 'content-type: application/json' \
  -d "{\"provider\":\"openai\",\"name\":\"oa\",\"authType\":\"api_key\",\"credentials\":{\"api_key\":\"sk-fake\"},\"metadata\":{\"base_url\":\"http://localhost:$FAKE_PORT\"}}")
CONN_ID=$(echo "$CONN" | jq_get id)
[ -n "$CONN_ID" ] && ok "bob creates connection" || bad "bob creates connection" "$CONN"
ALICE_CONNS=$(curl -s -H "authorization: Bearer $TOK_A" localhost:$ADMIN_PORT/api/connections | jq_get items)
assert_eq "alice does not see bob's connection" "[]" "$ALICE_CONNS"

echo
echo "4) /v1/chat/completions non-stream"
RESP=$(curl -s -X POST localhost:$ROUTER_PORT/v1/chat/completions -H "authorization: Bearer $KEY_B" -H 'content-type: application/json' \
  -d '{"model":"openai:gpt-4o","messages":[{"role":"user","content":"hi"}]}')
TXT=$(echo "$RESP" | python3 -c "import sys,json;print(json.load(sys.stdin)['choices'][0]['message']['content'])" 2>/dev/null)
assert_eq "response text" "hi" "$TXT"

echo
echo "5) /v1/chat/completions stream"
STREAM=$(curl -sN -X POST localhost:$ROUTER_PORT/v1/chat/completions -H "authorization: Bearer $KEY_B" -H 'content-type: application/json' \
  -d '{"model":"openai:gpt-4o","messages":[{"role":"user","content":"hi"}],"stream":true}')
assert_contains "stream produced [DONE]" "\[DONE\]" "$STREAM"

echo
echo "6) /v1/messages (Anthropic shape via translator)"
MSG=$(curl -s -X POST localhost:$ROUTER_PORT/v1/messages -H "x-api-key: $KEY_B" -H 'content-type: application/json' \
  -d '{"model":"openai:gpt-4o","messages":[{"role":"user","content":"hi"}],"max_tokens":50}')
MSGTYPE=$(echo "$MSG" | jq_get type)
INPUT_TOKENS=$(echo "$MSG" | jq_get usage.input_tokens)
assert_eq "messages.type" "message" "$MSGTYPE"
assert_eq "messages.input_tokens" "4" "$INPUT_TOKENS"

echo
echo "7) /v1/embeddings"
EMB=$(curl -s -X POST localhost:$FAKE_PORT/v1/embeddings -H 'content-type: application/json' -d '{}' 2>/dev/null) # fake doesn't actually support but Go embed path uses /v1/embeddings on upstream
# Actually our fake doesn't reply to /v1/embeddings. Skip with a smoke that the route exists:
EMB_RESP=$(curl -s -o /dev/null -w '%{http_code}' -X POST localhost:$ROUTER_PORT/v1/embeddings -H "authorization: Bearer $KEY_B" -H 'content-type: application/json' -d '{"model":"openai:text-embedding-3-small"}')
assert_eq "embeddings missing input → 400" "400" "$EMB_RESP"

echo
echo "8) /v1/models lists openai well-knowns"
MODELS=$(curl -s -H "authorization: Bearer $KEY_B" localhost:$ROUTER_PORT/v1/models)
assert_contains "models has openai:gpt-4o" "openai:gpt-4o" "$MODELS"

echo
echo "9) quota counter ticks"
Q=$(curl -s -H "authorization: Bearer $TOK_B" localhost:$ADMIN_PORT/api/me/quota)
USED=$(echo "$Q" | jq_get requests.used)
TOK_USED=$(echo "$Q" | jq_get tokens.used)
USED="${USED:-0}"
TOK_USED="${TOK_USED:-0}"
[ "$USED" -ge 3 ] 2>/dev/null && ok "requests.used $USED ≥ 3" || bad "requests.used" "got '$USED'"
[ "$TOK_USED" -ge 12 ] 2>/dev/null && ok "tokens.used $TOK_USED ≥ 12" || bad "tokens.used" "got '$TOK_USED'"

echo
echo "10) auto-routing: 400 without routing, 200 after PUT"
AUTO_NO=$(curl -s -X POST localhost:$ROUTER_PORT/v1/chat/completions -H "authorization: Bearer $KEY_B" -H 'content-type: application/json' \
  -d '{"model":"auto","messages":[{"role":"user","content":"hi"}]}')
assert_contains "auto without routing → validation error" "default 自动路由目标" "$AUTO_NO"
curl -s -X PUT "localhost:$ADMIN_PORT/api/routing/default" -H "authorization: Bearer $TOK_B" -H 'content-type: application/json' -d '{"target":"openai:gpt-4o-mini"}' > /dev/null
AUTO_OK=$(curl -s -X POST localhost:$ROUTER_PORT/v1/chat/completions -H "authorization: Bearer $KEY_B" -H 'content-type: application/json' \
  -d '{"model":"auto","messages":[{"role":"user","content":"hi"}]}')
TXT2=$(echo "$AUTO_OK" | python3 -c "import sys,json;print(json.load(sys.stdin)['choices'][0]['message']['content'])" 2>/dev/null)
assert_eq "auto with routing → 200" "hi" "$TXT2"

echo
echo "11) super-admin tenants list (alice IS, bob ISN'T)"
SUPER_LIST=$(curl -s -o /dev/null -w '%{http_code}' -H "authorization: Bearer $TOK_A" localhost:$ADMIN_PORT/api/admin/tenants)
assert_eq "alice GET /api/admin/tenants" "200" "$SUPER_LIST"
BOB_LIST=$(curl -s -o /dev/null -w '%{http_code}' -H "authorization: Bearer $TOK_B" localhost:$ADMIN_PORT/api/admin/tenants)
assert_eq "bob GET /api/admin/tenants → 403" "403" "$BOB_LIST"

echo
echo "═══════════════════════════════════════════════════════"
echo "  backend=$BACKEND   PASS: $PASS   FAIL: $FAIL"
echo "═══════════════════════════════════════════════════════"
exit $FAIL
