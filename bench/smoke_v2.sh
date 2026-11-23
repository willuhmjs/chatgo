#!/usr/bin/env bash
# bench/smoke_v2.sh — end-to-end smoke test for chatgo v2 features.
#
# Builds bin/chatgo-smoke from the repo root, boots it against a throwaway
# DATA_DIR (:8098) with bench/mockprovider_v2 on :9100 (NEVER the RAM-bench
# ports 9099/9098/8080/8099 — those are owned by other benches; we refuse to
# start on them and kill ONLY our own pids), then drives the full v2 curl
# sequence below. Every assertion prints PASS/FAIL/SKIP; exit ≠ 0 on any
# FAIL. Endpoints answered 404/405/501 mean "feature not wired yet" → their
# whole gated section prints SKIP (so this script runs green against partial
# builds while features land).
#
# USAGE
#   bench/smoke_v2.sh              # full run; cleans DATA_DIR + temp on exit
#   SMOKE_KEEP=1 bench/smoke_v2.sh # keep /tmp/chatgo-smoke-v2 (app/mock logs)
#
# SECTION MAP (SKIP = gated on that feature's first endpoint being wired)
#   S0  preflight + builds (bin/chatgo-smoke, mockv2, totpcode)
#   S1  boot mock :9100 + app :8098 (health-gated)
#   S2  core: register admin → provider CRUD-ready create → sync →
#       GET /api/models (chat/embed/image synced) → task_model settings PUT
#   S3  mock provider direct checks (models/chat JSON/SSE echo/embeddings
#       determinism 64-dim/images n=2 PNG/moderations) — MUST be green
#   S4  KB [SKIP-gate: POST /api/kb] collection → 2× .md upload (secret +
#       noise, multi-chunk filler) → chunk_count>0 → search top-1 =
#       'blue-otter-42' → chat w/ kb_collection_ids → SSE has kb:// source
#       + streamed answer
#   S5  prompts [gate: POST /api/prompts] CRUD + cross-user isolation/reject
#   S6  chat SSE echo (always) + ratings [gate: POST …/rating] 👍+comment →
#       GET ratings map → flip 👎
#   S7  fork [gate: POST /api/chats/{id}/fork] from first msg → msg count +
#       "Fork of" title
#   S8  export [gate: GET /api/chats/{id}/export] md + json + /api/me/export
#       zip (unzip -l)
#   S9  2FA [gate: POST /api/me/2fa/setup] setup secret → enable via
#       bench/totpcode → clean-jar login asserts requires_totp + totp_token
#       + NO session cookie → POST /api/auth/totp issues session
#   S10 audit [gate: GET /api/audit] logins+2fa actions present + cursor
#       paging > 1 page at limit=5 (fodder events: provider create/update/
#       delete + logout/login cycle generated first)
#   S11 images [gate: POST /api/images/generate] n=2 → GET /api/images ≥ 2
set -euo pipefail

cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

APP_PORT="${SMOKE_PORT:-8098}"
MOCK_PORT="${SMOKE_MOCK_PORT:-9100}"
BASE="http://127.0.0.1:${APP_PORT}"
MOCK="http://127.0.0.1:${MOCK_PORT}"
DATA_DIR="${SMOKE_DATA_DIR:-/tmp/chatgo-smoke-v2}"

# Hard firewall: these ports belong to other benches / the dev server.
for p in "$APP_PORT" "$MOCK_PORT"; do
  case "$p" in
    9099|9098|8080|8099)
      echo "refusing to use port $p (owned by RAM bench / dev server)" >&2
      exit 1;;
  esac
done

WORK="$(mktemp -d "${TMPDIR:-/tmp}/chatgo-smoke-v2-run.XXXXXX")"
JAR="$WORK/admin.jar"; JAR2="$WORK/user2.jar"; JAR3="$WORK/clean.jar"
BODY="$WORK/body.json"; SSE="$WORK/sse.txt"
: > "$JAR"; : > "$JAR2"; : > "$JAR3"; : > "$BODY"
MOCK_PID=""; APP_PID=""; CLEANED=0
PASS=0; FAIL=0; SKIP=0; FAILED_LIST=""

log()   { printf '\n== %s\n' "$*"; }
pass()  { PASS=$((PASS+1));  printf 'PASS  %-24s %s\n' "$1" "${2:-}"; }
fail()  { FAIL=$((FAIL+1));  FAILED_LIST="$FAILED_LIST $1"; printf 'FAIL  %-24s %s\n' "$1" "$2"; }
skip()  { SKIP=$((SKIP+1));  printf 'SKIP  %-24s %s\n' "$1" "${2:-}"; }

# notwired STATUS → true when the endpoint is not registered yet (partial build).
notwired() { case "$1" in 404|405|501) return 0 ;; *) return 1 ;; esac; }

cleanup() {
  [ "$CLEANED" = 1 ] && return 0
  CLEANED=1
  # Kill ONLY processes we started (never anything pre-existing).
  for pid in "$APP_PID" "$MOCK_PID"; do
    [ -n "$pid" ] && kill "$pid" 2>/dev/null || true
  done
  for pid in "$APP_PID" "$MOCK_PID"; do
    [ -n "$pid" ] && wait "$pid" 2>/dev/null || true
  done
  if [ "${SMOKE_KEEP:-0}" = 1 ]; then
    echo "smoke: logs kept in $DATA_DIR (app.log / mock.log)" >&2
  else
    case "$DATA_DIR" in
      /tmp/chatgo-smoke-v2|*/chatgo-smoke-v2) rm -rf "$DATA_DIR" ;;
    esac
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

# api METHOD PATH [JSON] [JAR] [OUTFILE] → echoes HTTP status ("000" = curl
# transport failure); response body lands in OUTFILE (default $BODY).
api() {
  local m=$1 p=$2 d=${3:-} j=${4:-$JAR} out=${5:-$BODY}
  local args=(-sS -o "$out" -w '%{http_code}' -X "$m" -b "$j" -c "$j" --max-time 120)
  [ -n "$d" ] && args+=(-H 'Content-Type: application/json' -d "$d")
  curl "${args[@]}" "$BASE$p" 2>/dev/null || echo 000
}

# sse_send CHAT_ID CONTENT → status; raw SSE stream captured in $SSE.
sse_send() {
  curl -sS -N --max-time 120 -o "$SSE" -w '%{http_code}' -b "$JAR" \
    -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg c "$2" '{content:$c}')" \
    "$BASE/api/chats/$1/messages" 2>/dev/null || echo 000
}

# sse_text → streamed answer text reassembled from token events (concatenated
# — a phrase routinely spans several token events, so line-wise grep misses it).
sse_text() {
  grep '^data: ' "$SSE" 2>/dev/null | sed 's/^data: //' \
    | jq -Rr 'fromjson? | .content? // empty' 2>/dev/null \
    | tr -d '\n' || true
}

jqe() { jq -e "$1" "$BODY" >/dev/null 2>&1; }                       # jqe EXPR
jqv() { jq -r "$1 // empty" "$BODY" 2>/dev/null | head -1 || true; } # jqv EXPR
body() { head -c 240 "$BODY" | tr '\n' ' '; }                        # debug snippet

# ---------------------------------------------------------------- S0 preflight
log "S0 preflight + build"
for t in curl jq unzip go; do
  command -v "$t" >/dev/null || { echo "missing tool: $t" >&2; exit 1; }
done
for p in "$APP_PORT" "$MOCK_PORT"; do
  if lsof -nP -iTCP:"$p" -sTCP:LISTEN >/dev/null 2>&1; then
    echo "port $p already in use — refusing to start (and refusing to kill it)" >&2
    exit 1
  fi
done
case "$DATA_DIR" in
  /tmp/chatgo-smoke-v2|*/chatgo-smoke-v2) rm -rf "$DATA_DIR" ;;
  *) echo "unexpected DATA_DIR $DATA_DIR — refusing to wipe" >&2; exit 1 ;;
esac
mkdir -p "$DATA_DIR" bin

go build -trimpath -o bin/chatgo-smoke ./cmd/chatgo || { echo "build bin/chatgo-smoke failed" >&2; exit 1; }
# mockprovider_v2.go carries //go:build ignore — go includes explicitly named
# files regardless, so this stays out of `go build ./...` yet builds standalone.
go build -o "$WORK/mockv2" bench/mockprovider_v2.go || { echo "build mockprovider_v2 failed" >&2; exit 1; }
go build -o "$WORK/totpcode" ./bench/totpcode || { echo "build totpcode failed" >&2; exit 1; }
"$WORK/totpcode" -selftest >/dev/null || { echo "totpcode selftest failed" >&2; exit 1; }
pass build "chatgo-smoke + mockv2 + totpcode"

# ------------------------------------------------------------------- S1 boot
log "S1 boot mock :${MOCK_PORT} + app :${APP_PORT}"
"$WORK/mockv2" > "$DATA_DIR/mock.log" 2>&1 &
MOCK_PID=$!

SESSION_SECRET="$(openssl rand -hex 32 2>/dev/null || head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')" \
DATA_DIR="$DATA_DIR" PORT="$APP_PORT" HOST=127.0.0.1 \
REGISTRATION=open ALLOW_LOOPBACK=true SECURE_COOKIE=false \
  ./bin/chatgo-smoke > "$DATA_DIR/app.log" 2>&1 &
APP_PID=$!

wait_http() { # url pid logfile
  local i
  for i in $(seq 1 80); do
    curl -fsS -o /dev/null --max-time 2 "$1" 2>/dev/null && return 0
    kill -0 "$2" 2>/dev/null || { tail -25 "$3" >&2; return 1; }
    sleep 0.25
  done
  return 1
}
wait_http "$MOCK/healthz" "$MOCK_PID" "$DATA_DIR/mock.log" || { echo "mock failed to boot" >&2; exit 1; }
wait_http "$BASE/healthz" "$APP_PID" "$DATA_DIR/app.log" || { echo "app failed to boot" >&2; exit 1; }
pass boot "mock:$MOCK_PID app:$APP_PID"

EMAIL="smoke-admin-$$@example.invalid"
PASSWORD="Smoke-v2-$RANDOM$RANDOM-pw"
EMAIL2="smoke-user2-$$@example.invalid"
PASSWORD2="Smoke2-v2-$RANDOM$RANDOM-pw"
MSG_TEXT="hello v2 run $$"   # unique marker threaded through chat/fork/export

# ------------------------------------------------------------------ S2 core
log "S2 auth + provider + models + task_model"
st=$(api POST /api/auth/register "$(jq -nc --arg e "$EMAIL" --arg p "$PASSWORD" '{email:$e,password:$p,name:"Smoke Admin"}')")
if [ "$st" = 200 ] || [ "$st" = 201 ]; then pass register-admin "$st"; else fail register-admin "status $st: $(body)"; fi

st=$(api GET /api/auth/me)
if [ "$st" = 200 ] && jqe '.role == "admin"'; then pass first-user-admin "role=admin"
else fail first-user-admin "status $st: $(body)"; fi

st=$(api POST /api/auth/register "$(jq -nc --arg e "$EMAIL2" --arg p "$PASSWORD2" '{email:$e,password:$p,name:"Smoke User2"}')" "$JAR2")
if [ "$st" = 200 ] || [ "$st" = 201 ]; then pass register-user2 "$st (jar user2)"; else fail register-user2 "status $st: $(body)"; fi

st=$(api POST /api/providers '{"name":"mock-v2","base_url":"'"$MOCK"'/v1","api_key":"mk-smoke-key","enabled":true,"is_default":true}')
PROV=$(jqv '.id')
if [ "$st" = 201 ] && [ -n "$PROV" ]; then pass provider-create "id=${PROV:0:8}…"
else fail provider-create "status $st: $(body)"; fi

st=$(api POST "/api/providers/$PROV/sync")
[ "$st" = 200 ] && pass provider-sync || fail provider-sync "status $st: $(body)"

st=$(api GET /api/models)
if [ "$st" = 200 ] && jqe '[.[].model_id] | (index("mock-gpt-v2") != null) and (index("mock-embed-v2") != null) and (index("mock-image-v2") != null)'
then pass models-synced "chat+embed+image"
else fail models-synced "status $st: $(body)"; fi

st=$(api PUT /api/settings "{\"task_model\":\"$PROV:mock-gpt-v2\"}")
if [ "$st" = 200 ]; then
  st=$(api GET /api/settings)
  if [ "$st" = 200 ] && jqe '.task_model == "'"$PROV"':mock-gpt-v2"'; then pass task-model-set
  else fail task-model-set "readback: $(body)"; fi
else fail task-model-set "status $st: $(body)"; fi

# ----------------------------------------------------- S3 mock provider direct
log "S3 mockprovider_v2 direct checks (must be green)"
st=$(curl -sS -o "$BODY" -w '%{http_code}' "$MOCK/v1/models")
[ "$st" = 200 ] && jqe '[.data[].id] | (index("mock-gpt-v2") != null) and (index("mock-embed-v2") != null) and (index("mock-image-v2") != null)' \
  && pass mock-models || fail mock-models "status $st: $(body)"

st=$(curl -sS -o "$BODY" -w '%{http_code}' -X POST "$MOCK/v1/chat/completions" -H 'Content-Type: application/json' \
     -d '{"model":"mock-gpt-v2","messages":[{"role":"user","content":"qa-mock-echo-42"}]}')
[ "$st" = 200 ] && jqe '.choices[0].message.content | contains("qa-mock-echo-42")' \
  && pass mock-chat-json || fail mock-chat-json "status $st: $(body)"

curl -sS -N -X POST "$MOCK/v1/chat/completions" -H 'Content-Type: application/json' \
  -d '{"stream":true,"messages":[{"role":"user","content":"qa-mock-stream"}]}' > "$WORK/mock.sse" 2>/dev/null
mocktext=$(grep '^data: ' "$WORK/mock.sse" | sed 's/^data: //' | jq -Rr 'fromjson? | .choices[0].delta.content? // empty' | tr -d '\n' || true)
if grep -q 'data: \[DONE\]' "$WORK/mock.sse" && printf '%s' "$mocktext" | grep -q 'qa-mock-stream'
then pass mock-chat-sse "\"$(printf '%s' "$mocktext" | head -c 40)\""
else fail mock-chat-sse "no DONE or echo lost"; fi

eA=$(curl -sS -X POST "$MOCK/v1/embeddings" -H 'Content-Type: application/json' -d '{"input":"the secret passphrase is blue-otter-42"}' | jq -c '.data[0].embedding')
eB=$(curl -sS -X POST "$MOCK/v1/embeddings" -H 'Content-Type: application/json' -d '{"input":"the secret passphrase is blue-otter-42"}' | jq -c '.data[0].embedding')
eC=$(curl -sS -X POST "$MOCK/v1/embeddings" -H 'Content-Type: application/json' -d '{"input":"entirely unrelated corpus"}' | jq -c '.data[0].embedding')
if [ "$(printf '%s' "$eA" | jq 'length')" = 64 ] && [ "$eA" = "$eB" ] && [ "$eA" != "$eC" ]
then pass mock-embeddings "dim=64 deterministic"
else fail mock-embeddings "dim=$(printf '%s' "$eA" | jq 'length') same=$([ "$eA" = "$eB" ] && echo y || echo n)"; fi

curl -sS -X POST "$MOCK/v1/images/generations" -H 'Content-Type: application/json' -d '{"prompt":"sq","n":2}' > "$WORK/mock.img"
nimgs=$(jq '.data | length' "$WORK/mock.img")
pngmagic=$(jq -r '.data[0].b64_json' "$WORK/mock.img" | base64 -D 2>/dev/null | od -An -c | head -1 | tr -d ' ')
[ "$nimgs" = 2 ] && printf '%s' "$pngmagic" | grep -q '211PNG' \
  && pass mock-images "n=2, valid PNG" || fail mock-images "n=$nimgs magic=$pngmagic"

st=$(curl -sS -o "$BODY" -w '%{http_code}' -X POST "$MOCK/v1/moderations" -H 'Content-Type: application/json' -d '{"input":["a","b"]}')
[ "$st" = 200 ] && jqe '(.results | length) == 2 and ([.results[].flagged] | all(. == false) | not | not)' \
  && pass mock-moderations || fail mock-moderations "status $st: $(body)"

# -------------------------------------------------------------------- S4 kb
log "S4 knowledge base (gate: POST /api/kb)"
KB=0
st=$(api POST /api/kb '{"name":"smoke-kb"}')
if notwired "$st"; then
  skip kb-collection "status $st — not wired yet (rest of KB skipped)"
elif [ "$st" = 201 ] || [ "$st" = 200 ]; then
  KB=1; KBCOL=$(jqv '.id'); pass kb-collection "id=${KBCOL:0:8}…"
else
  fail kb-collection "status $st: $(body)"
fi

if [ "$KB" = 1 ]; then
  # One doc carries the target sentence; both docs get ~1.5k words of filler
  # (chunkWords=800 / step=720 in internal/kb) to force multi-chunk ingest.
  # Filler deliberately omits every query word (what/is/the/secret/…).
  filler="alpha bravo charlie delta foxtrot golf hotel india juliet kilo lima mike november oscar papa quebec romeo sierra tango uniform victor whiskey xray yankee zulu quartz cobalt linen harbor meadow ember thistle gravel"
  { echo "# Incident notes"; echo "The confidential answer is: the secret passphrase is blue-otter-42. Record it verbatim."; \
    for i in $(seq 1 45); do echo "$filler"; done; } > "$WORK/secret.md"
  { echo "# Ops chatter"; for i in $(seq 1 35); do echo "$filler"; done; } > "$WORK/noise.md"

  st=$(curl -sS -o "$BODY" -w '%{http_code}' -b "$JAR" -c "$JAR" --max-time 120 \
       -F "file=@$WORK/secret.md" -F "file=@$WORK/noise.md" "$BASE/api/kb/$KBCOL/files")
  if [ "$st" = 201 ] && jqe '(.items | length) == 2 and ([.items[].chunks] | all(. > 0))'
  then
    pass kb-embeddings "chunks=$(jq -c '[.items[].chunks]' "$BODY")"
    if jq -e '([.items[].chunks] | max) > 1' "$BODY" >/dev/null; then pass kb-multichunk
    else fail kb-multichunk "no file exceeded 1 chunk"; fi
  else fail kb-embeddings "status $st: $(body)"; fi

  st=$(api GET "/api/kb/$KBCOL/files")
  [ "$st" = 200 ] && jqe '(.items // . | length) == 2' \
    && pass kb-files-list || fail kb-files-list "status $st: $(body)"

  st=$(api POST "/api/kb/$KBCOL/search" '{"q":"what is the secret passphrase","top_k":4}')
  if [ "$st" = 200 ] && jq -e '[.items[]? | select(.content | contains("blue-otter-42"))] | length > 0' "$BODY" >/dev/null 2>&1 \
     && jq -e '.items[0].content | contains("blue-otter-42")' "$BODY" >/dev/null 2>&1
  then pass kb-search-top1 "score=$(jq -r '.items[0].score' "$BODY")"
  else fail kb-search-top1 "status $st: $(body)"; fi

  st=$(api POST /api/chats "$(jq -nc --arg p "$PROV" --arg m mock-gpt-v2 --arg c "$KBCOL" \
       '{title:"kb smoke", provider_id:$p, model_id:$m, params:{kb_collection_ids:[$c]}}')")
  CHAT_KB=$(jqv '.id')
  if [ "$st" = 201 ] && [ -n "$CHAT_KB" ]; then
    st=$(sse_send "$CHAT_KB" "what is the secret passphrase?")
    if [ "$st" = 200 ] && grep -q '^event: token' "$SSE" && grep -q 'kb://' "$SSE"
    then pass kb-chat-sources "kb:// citation surfaced in SSE"
    else fail kb-chat-sources "status $st; kb:// missing or no tokens"; fi
  else fail kb-chat-create "status $st: $(body)"; fi
fi

# ---------------------------------------------------------------- S5 prompts
log "S5 prompt library (gate: POST /api/prompts)"
st=$(api POST /api/prompts '{"title":"smoke-prompt","content":"Reply as a pirate about {{topic}}"}')
if notwired "$st"; then
  skip prompts-create "status $st — not wired yet (rest of prompts skipped)"
else
  PROMPT=$(jqv '.id')
  if [ "$st" = 201 ] || [ "$st" = 200 ] && [ -n "$PROMPT" ]
  then pass prompts-create "id=${PROMPT:0:8}…"
  else fail prompts-create "status $st: $(body)"; fi

  prompts_arr() { jq -c 'if type=="array" then . else (.items // .prompts // .data // []) end' "$1"; }

  st=$(api GET /api/prompts)
  if [ "$st" = 200 ] && prompts_arr "$BODY" | jq -e --arg i "$PROMPT" '[.[].id] | index($i) != null' >/dev/null
  then pass prompts-list || true
  else fail prompts-list "status $st: $(body)"; fi

  st=$(api PUT "/api/prompts/$PROMPT" '{"content":"Reply as a pirate about {{topic}}, politely"}')
  [ "$st" = 200 ] && pass prompts-update || fail prompts-update "status $st: $(body)"

  # Cross-user: user2 must not SEE (list) nor TOUCH (404, no-existence-leak).
  st=$(api GET /api/prompts "" "$JAR2")
  if [ "$st" = 200 ] && ! prompts_arr "$BODY" | jq -e --arg i "$PROMPT" '[.[].id] | index($i) != null' >/dev/null
  then pass prompts-isolation "user2 cannot see admin prompt"
  else fail prompts-isolation "LEAK: user2 list contains $PROMPT"; fi

  st=$(api PUT "/api/prompts/$PROMPT" '{"content":"hijacked"}' "$JAR2")
  if [ "$st" = 404 ]; then pass prompts-cross-user "404 (no existence leak)"
  else fail prompts-cross-user "expected 404, got $st"; fi

  st=$(api DELETE "/api/prompts/$PROMPT")
  [ "$st" = 204 ] || [ "$st" = 200 ] && pass prompts-delete || fail prompts-delete "status $st: $(body)"
fi

# ------------------------------------------------------ S6 chat SSE + ratings
log "S6 chat round-trip + ratings"
st=$(api POST /api/chats "$(jq -nc --arg p "$PROV" --arg m mock-gpt-v2 '{title:"smoke chat", provider_id:$p, model_id:$m}')")
CHAT=$(jqv '.id')
if [ "$st" != 201 ] || [ -z "$CHAT" ]; then
  fail chat-create "status $st: $(body)"
  CHAT=""
else
  pass chat-create "id=${CHAT:0:8}…"
  st=$(sse_send "$CHAT" "$MSG_TEXT")
  if [ "$st" = 200 ] && grep -q '^event: token' "$SSE" && grep -q '^event: done' "$SSE"
  then
    if sse_text | grep -q "$MSG_TEXT"; then pass chat-sse-echo "echo round-trip ok"
    else fail chat-sse-echo "tokens streamed but echo missing '$MSG_TEXT'"; fi
  else fail chat-sse-echo "status $st; SSE missing token/done events"; fi

  st=$(api GET "/api/chats/$CHAT/messages")
  UMID=$(jq -r '[.[] | select(.role=="user")][0].id // empty' "$BODY")
  AMID=$(jq -r '[.[] | select(.role=="assistant")][0].id // empty' "$BODY")
  if [ -n "$UMID" ] && [ -n "$AMID" ]; then pass chat-messages "user+assistant persisted"
  else fail chat-messages "status $st: $(body)"; fi
fi

if [ -n "$CHAT" ] && [ -n "$AMID" ]; then
  st=$(api POST "/api/chats/$CHAT/messages/$AMID/rating" '{"value":1,"comment":"thumbs up v2"}')
  if notwired "$st"; then
    skip ratings-map "status $st — not wired yet"; skip rating-flip "with ratings"
  elif [ "$st" = 200 ] && st=$(api GET "/api/chats/$CHAT/ratings") && [ "$st" = 200 ] \
       && jq -e --arg m "$AMID" '.[$m].value == 1 and .[$m].comment == "thumbs up v2"' "$BODY" >/dev/null
  then
    pass ratings-map "value=1 + comment"
    st=$(api POST "/api/chats/$CHAT/messages/$AMID/rating" '{"value":-1}')
    st=$(api GET "/api/chats/$CHAT/ratings")
    if [ "$st" = 200 ] && jq -e --arg m "$AMID" '.[$m].value == -1' "$BODY" >/dev/null
    then pass rating-flip "👍→👎 upsert in place"
    else fail rating-flip "readback: $(body)"; fi
  else fail ratings-map "status $st: $(body)"; skip rating-flip "with ratings"; fi
fi

# -------------------------------------------------------------------- S7 fork
log "S7 fork (gate: POST /api/chats/{id}/fork)"
if [ -n "$CHAT" ] && [ -n "$UMID" ]; then
  st=$(api POST "/api/chats/$CHAT/fork" "{\"message_id\":\"$UMID\"}")
  if notwired "$st"; then
    skip fork-count "status $st — not wired yet"; skip fork-title "with fork"
  elif [ "$st" = 201 ]; then
    NEW=$(jqv '.id')
    st=$(api GET "/api/chats/$NEW/messages")
    if [ "$st" = 200 ] && jq -e 'length == 1 and .[0].content == "'"$MSG_TEXT"'"' "$BODY" >/dev/null
    then pass fork-count "1 message ≤ fork point"
    else fail fork-count "status $st: $(body)"; fi
    if jq -e '.title | startswith("Fork of")' "$BODY" >/dev/null 2>&1 || \
       { st=$(api GET "/api/chats/$NEW"); [ "$st" = 200 ] && jqe '.title | startswith("Fork of")'; }
    then pass fork-title
    else fail fork-title "title not 'Fork of …': $(body)"; fi
  else fail fork-count "status $st: $(body)"; skip fork-title "with fork"; fi
fi

# ------------------------------------------------------------------ S8 export
log "S8 export (gate: GET /api/chats/{id}/export)"
if [ -n "$CHAT" ]; then
  st=$(api GET "/api/chats/$CHAT/export?format=md" "" "$JAR" "$WORK/export.md")
  if notwired "$st"; then
    skip export-md "status $st — not wired yet"; skip export-json "with export"; skip export-zip "with export"
  elif [ "$st" = 200 ] && grep -q "$MSG_TEXT" "$WORK/export.md"; then
    pass export-md "markdown contains transcript"
    st=$(api GET "/api/chats/$CHAT/export?format=json" "" "$JAR" "$WORK/export.json")
    if [ "$st" = 200 ] && jq -e . "$WORK/export.json" >/dev/null 2>&1 && grep -q "$MSG_TEXT" "$WORK/export.json"
    then pass export-json
    else fail export-json "status $st"; fi

    st=$(api GET /api/me/export "" "$JAR" "$WORK/me.zip")
    if [ "$st" = 200 ] && nfiles=$(unzip -l "$WORK/me.zip" 2>/dev/null | awk 'NR>3 && NF>=4 {c++} END {print c+0}') && [ "${nfiles:-0}" -ge 1 ]
    then pass export-zip "zip lists $nfiles file(s)"
    else fail export-zip "status $st or unzip failed"; fi
  else fail export-md "status $st"; skip export-json "with export"; skip export-zip "with export"; fi
fi

# -------------------------------------------------------------------- S9 2FA
log "S9 2FA (gate: POST /api/me/2fa/setup)"
TWOFA=0
st=$(api POST /api/me/2fa/setup "{\"password\":\"$PASSWORD\"}")
if notwired "$st"; then
  skip 2fa-setup "status $st — not wired yet (2FA login flow skipped)"
elif [ "$st" = 200 ] || [ "$st" = 201 ]; then
  SECRET=$(jqv '.secret'); OTPAUTH=$(jqv '.otpauth_uri')
  if [ -n "$SECRET" ] && [ -n "$OTPAUTH" ]; then
    TWOFA=1; pass 2fa-setup "secret + otpauth_uri"
    CODE="$("$WORK/totpcode" "$SECRET")"
    st=$(api POST /api/me/2fa/enable "{\"code\":\"$CODE\"}")
    if [ "$st" = 200 ] || [ "$st" = 201 ]; then pass 2fa-enable "code=$CODE accepted"
    else fail 2fa-enable "status $st: $(body)"; TWOFA=0; fi
  else fail 2fa-setup "missing secret/otpauth_uri: $(body)"; fi
else fail 2fa-setup "status $st: $(body)"; fi

if [ "$TWOFA" = 1 ]; then
  : > "$JAR3"; : > "$WORK/login.hdrs"
  st=$(curl -sS -o "$BODY" -D "$WORK/login.hdrs" -w '%{http_code}' -b "$JAR3" -c "$JAR3" \
       -H 'Content-Type: application/json' -d "{\"email\":\"$EMAIL\",\"password\":\"$PASSWORD\"}" \
       "$BASE/api/auth/login" 2>/dev/null || echo 000)
  if [ "$st" = 200 ] && jqe '.requires_totp == true'; then pass 2fa-challenge "requires_totp=true"
  else fail 2fa-challenge "status $st: $(body)"; fi
  TT=$(jqv '.totp_token')
  if [ -n "$TT" ]; then pass 2fa-token "totp_token issued"; else fail 2fa-token "no totp_token in $(body)"; fi
  if grep -i '^set-cookie:' "$WORK/login.hdrs" | grep -q 'chatgo_session'; then
    fail 2fa-no-session "session cookie issued BEFORE TOTP!"
  else pass 2fa-no-session "no Set-Cookie chatgo_session"; fi

  CODE="$("$WORK/totpcode" "$SECRET")"
  st=$(api POST /api/auth/totp "{\"totp_token\":\"$TT\",\"code\":\"$CODE\"}" "$JAR3")
  if [ "$st" = 200 ] && grep -q 'chatgo_session' "$JAR3"; then pass 2fa-totp-login "session issued via /api/auth/totp"
  else fail 2fa-totp-login "status $st: $(body)"; fi
  st=$(api GET /api/auth/me "" "$JAR3")
  [ "$st" = 200 ] && jq -e --arg e "$EMAIL" '.email == $e' "$BODY" >/dev/null \
    && pass 2fa-totp-session "me OK on new jar" || fail 2fa-totp-session "status $st: $(body)"
fi

# ------------------------------------------------------------------ S10 audit
log "S10 audit (gate: GET /api/audit)"
# Audit fodder first (these v1 endpoints should work regardless of wiring):
# provider create/update/delete + a logout/login cycle → guarantees >5 events
# so the cursor-paging probe (limit=5) is meaningful.
st=$(api POST /api/providers '{"name":"audit-fodder","base_url":"'"$MOCK"'/v1","api_key":"k"}')
FP=$(jqv '.id')
st2=$(api PUT "/api/providers/$FP" '{"name":"audit-fodder-2","enabled":false}')
st3=$(api DELETE "/api/providers/$FP")
{ [ "$st" = 201 ] && [ "$st2" = 200 ] && [ "$st3" = 204 -o "$st3" = 200 ]; } \
  && pass provider-crud "create/update/delete fodder" || fail provider-crud "status $st/$st2/$st3"

st=$(api POST /api/auth/logout "" "$JAR2")
st=$(api POST /api/auth/login "$(jq -nc --arg e "$EMAIL2" --arg p "$PASSWORD2" '{email:$e,password:$p}')" "$JAR2")
[ "$st" = 200 ] || fail audit-fodder-login "user2 re-login status $st — audit events may be thin"

st=$(api GET '/api/audit?limit=5&cursor=0')
if notwired "$st"; then
  skip audit "status $st — not wired yet"
elif [ "$st" = 200 ]; then
  : > "$WORK/actions.txt"
  cursor=0; pages=0
  while :; do
    if [ "$pages" -gt 0 ]; then
      st=$(api GET "/api/audit?limit=5&cursor=$cursor")
      [ "$st" = 200 ] || { fail audit-paging "page $((pages+1)): status $st"; break; }
    fi
    jq -r '(.items // .entries // .logs // .data // [])[] | .action' "$BODY" >> "$WORK/actions.txt" 2>/dev/null || true
    pages=$((pages+1))
    next=$(jq -r '.next_cursor // .next // empty' "$BODY" 2>/dev/null || true)
    [ -z "$next" ] || [ "$next" = null ] && break
    cursor="$next"
    [ "$pages" -ge 20 ] && break
  done
  if [ "$pages" -gt 1 ]; then pass audit-paging "$pages pages at limit=5"
  else fail audit-paging "only $pages page(s) with limit=5"; fi
  if grep -Eq 'login\.(success|fail)|logout' "$WORK/actions.txt"; then pass audit-actions "login/logout events"
  else fail audit-actions "no login/logout in: $(tr '\n' ' ' < "$WORK/actions.txt")"; fi
  if [ "$TWOFA" = 1 ]; then
    if grep -Eq '2fa\.|totp\.' "$WORK/actions.txt"; then pass audit-2fa "2FA events recorded"
    else fail audit-2fa "no 2fa.*/totp.* actions recorded"; fi
  else skip audit-2fa "2FA not exercised (skipped upstream)"; fi
else fail audit "status $st: $(body)"; fi

# ----------------------------------------------------------------- S11 images
log "S11 image generation (gate: POST /api/images/generate)"
st=$(api POST /api/images/generate "{\"prompt\":\"a tiny indigo square\",\"n\":2,\"provider_id\":\"$PROV\"}")
if notwired "$st"; then
  skip images-generate "status $st — not wired yet"; skip images-list "with images"
elif [ "$st" = 200 ] && jq -e '(.images // .items // .data // []) | length == 2' "$BODY" >/dev/null
then
  pass images-generate "n=2 stored"
  st=$(api GET /api/images)
  if [ "$st" = 200 ] && jq -e '(.images // .items // .data // []) | length >= 2' "$BODY" >/dev/null
  then pass images-list
  else fail images-list "status $st: $(body)"; fi
else fail images-generate "status $st: $(body)"; skip images-list "with images"; fi

# ------------------------------------------------------------------- summary
log "summary"
printf 'PASS=%d FAIL=%d SKIP=%d\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -gt 0 ] && printf 'failed:%s\n' "$FAILED_LIST"
[ "$FAIL" -eq 0 ] && echo "SMOKE-V2 OK" || echo "SMOKE-V2 FAILED"
[ "$FAIL" -eq 0 ]
