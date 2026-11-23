#!/usr/bin/env bash
# bench/measure.sh — RSS + latency smoke benchmark for chatgo.
#
# Boots the binary against a temp DATA_DIR with a generated secret, starts a
# canned OpenAI-compatible SSE mock on :9099, fires 50 sequential /healthz
# requests, attempts one scripted SSE chat (registration + provider + chat +
# message; skipped gracefully if any API step differs), then reports idle RSS.
#
# USAGE NOTE — RSS platform caveat: on Linux we read /proc/<pid>/status VmRSS
# (the number that matters for k8s limits). On macOS there is no /proc, so we
# fall back to `ps -o rss=`, and macOS allocator behavior typically reports
# ~1.5–2× the Linux figure. Compare Linux-to-Linux only.
set -euo pipefail

cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

PORT="${PORT:-18099}"
MOCK_PORT="${MOCK_PORT:-9099}"
BASE="http://127.0.0.1:${PORT}"
MOCK_BASE="http://127.0.0.1:${MOCK_PORT}"
EMAIL="bench-$$@example.invalid"
PASSWORD="bench-$$-passw0rd"

DATA_DIR="$(mktemp -d "${TMPDIR:-/tmp}/chatgo-bench.XXXXXX")"
JAR="$(mktemp "${TMPDIR:-/tmp}/chatgo-bench-cookies.XXXXXX")"
CHATGO_PID=""
MOCK_PID=""

cleanup() {
  [ -n "$CHATGO_PID" ] && kill "$CHATGO_PID" 2>/dev/null || true
  [ -n "$MOCK_PID" ]   && kill "$MOCK_PID"   2>/dev/null || true
  wait 2>/dev/null || true
  rm -rf "$DATA_DIR" "$JAR"
}
trap cleanup EXIT

say()  { printf 'bench: %s\n' "$*"; }
warn() { printf 'bench: WARN: %s\n' "$*" >&2; }

command -v curl >/dev/null || { warn "curl required"; exit 1; }

# ---- 1. build --------------------------------------------------------------
say "building binary + mock"
go build -trimpath -o bin/chatgo ./cmd/chatgo
go build -o "${DATA_DIR}/mockprovider" ./bench

# ---- 2. start mock + chatgo ------------------------------------------------
"${DATA_DIR}/mockprovider" >/dev/null 2>&1 &
MOCK_PID=$!

SESSION_SECRET="$(openssl rand -hex 32)" \
DATA_DIR="$DATA_DIR" PORT="$PORT" HOST=127.0.0.1 \
ALLOW_LOOPBACK=true REGISTRATION=open \
  ./bin/chatgo >"${DATA_DIR}/chatgo.log" 2>&1 &
CHATGO_PID=$!

say "waiting for ${BASE}/healthz (pid ${CHATGO_PID})"
for _ in $(seq 1 60); do
  curl -fsS -o /dev/null "$BASE/healthz" 2>/dev/null && break
  kill -0 "$CHATGO_PID" 2>/dev/null || { warn "chatgo died:"; cat "${DATA_DIR}/chatgo.log" >&2; exit 1; }
  sleep 0.5
done
curl -fsS -o /dev/null "$BASE/healthz" || { warn "healthz never came up"; exit 1; }

# ---- 3. idle RSS baseline (before any load) ---------------------------------
rss_mib() { # kB → MiB, Linux /proc first, macOS ps fallback
  local pid=$1 kb=""
  if [ -r "/proc/${pid}/status" ]; then
    kb="$(awk '/^VmRSS:/{print $2}' "/proc/${pid}/status")"
  else
    kb="$(ps -o rss= -p "$pid" | tr -d ' ')"
  fi
  [ -n "$kb" ] || { echo "?"; return; }
  awk -v k="$kb" 'BEGIN{printf "%.1f", k/1024}'
}
RSS_IDLE="$(rss_mib "$CHATGO_PID")"

# ---- 4. 50 sequential /healthz requests -------------------------------------
say "50 sequential /healthz requests"
TIMES="$(mktemp "${TMPDIR:-/tmp}/chatgo-bench-times.XXXXXX")"
for _ in $(seq 1 50); do
  curl -sS -o /dev/null -w '%{time_total}\n' "$BASE/healthz" >>"$TIMES"
done
awk 'BEGIN{min=1e9;max=0} {s+=$1; n++; if($1<min)min=$1; if($1>max)max=$1}
     END{printf "bench: healthz x%d: total %.3fs  avg %.1fms  max %.1fms\n", n, s, 1000*s/n, 1000*max}' "$TIMES"
rm -f "$TIMES"

# ---- 5. scripted SSE chat vs mock provider (best effort) --------------------
# Requires jq; API shapes per docs/CONTRACTS.md. Any non-2xx → warn + skip,
# the RSS measurement above/below stands on its own.
chat_done=0
if command -v jq >/dev/null; then
  H=(-H 'Content-Type: application/json' -b "$JAR" -c "$JAR" -sS -o /dev/null -w '%{http_code}')
  code=$(curl "${H[@]}" -X POST "$BASE/api/auth/register" \
    -d "{\"email\":\"$EMAIL\",\"password\":\"$PASSWORD\",\"name\":\"bench\"}")
  code=$(curl "${H[@]}" -X POST "$BASE/api/auth/login" \
    -d "{\"email\":\"$EMAIL\",\"password\":\"$PASSWORD\"}")
  [ "$code" = 200 ] || warn "login/register → HTTP $code (pending approval? skipping chat)"

  PROV_RES=$(curl -b "$JAR" -c "$JAR" -sS -X POST "$BASE/api/providers" \
    -H 'Content-Type: application/json' \
    -d "{\"name\":\"mock\",\"base_url\":\"${MOCK_BASE}/v1\",\"api_key\":\"sk-mock\"}" 2>/dev/null || true)
  PROVIDER_ID=$(echo "$PROV_RES" | jq -r '.id // .provider.id // empty' 2>/dev/null || true)
  if [ -n "${PROVIDER_ID:-}" ]; then
    curl -b "$JAR" -sS -o /dev/null -X POST "$BASE/api/providers/$PROVIDER_ID/sync" || true
    MODEL=$(curl -b "$JAR" -sS "$BASE/api/models" 2>/dev/null | jq -r --arg p "$PROVIDER_ID" \
      '[.[] | select(.provider_id==$p)][0].model_id // empty' 2>/dev/null || true)
    CHAT_ID=$(curl -b "$JAR" -sS -X POST "$BASE/api/chats" -H 'Content-Type: application/json' \
      -d "{\"title\":\"bench\",\"provider_id\":\"$PROVIDER_ID\",\"model_id\":\"$MODEL\"}" \
      2>/dev/null | jq -r '.id // empty' 2>/dev/null || true)
    if [ -n "${MODEL:-}" ] && [ -n "${CHAT_ID:-}" ]; then
      say "streaming one chat message (chat $CHAT_ID, model $MODEL)"
      SSE="$(mktemp "${TMPDIR:-/tmp}/chatgo-bench-sse.XXXXXX")"
      if curl -b "$JAR" -sS -N --max-time 60 -X POST "$BASE/api/chats/$CHAT_ID/messages" \
           -H 'Content-Type: application/json' -d '{"content":"ping"}' >"$SSE" 2>/dev/null \
         && grep -q '^event: token' "$SSE"; then
        chat_done=1
        say "SSE ok: $(grep -c '^event: token' "$SSE") token events, $(grep -c '^event: done' "$SSE") done"
      else
        warn "SSE stream failed (see ${DATA_DIR}/chatgo.log if kept)"
      fi
      rm -f "$SSE"
    else
      warn "could not resolve model/chat id — skipping SSE chat"
    fi
  else
    warn "provider creation failed (need admin role?) — skipping SSE chat"
  fi
else
  warn "jq not installed — skipping scripted SSE chat (RSS still measured)"
fi

# ---- 6. idle RSS report ------------------------------------------------------
sleep 2   # let GC/settling happen; we want *idle*, not peak
RSS_IDLE_AFTER="$(rss_mib "$CHATGO_PID")"
say "platform: $(uname -s) $(uname -m)"
say "idle RSS at startup: ${RSS_IDLE} MiB"
say "idle RSS after bench: ${RSS_IDLE_AFTER} MiB"
[ "$chat_done" = 1 ] && say "note: above includes one completed mock chat (steady state)" || warn "chat portion skipped — idle-only figure"
if [ ! -r "/proc/${CHATGO_PID}/status" ]; then
  say "CAVEAT: macOS ps-rss fallback; expect ~1.5–2x vs Linux VmRSS. Gate with Linux numbers."
fi
say "target: < 128 MiB (see README RAM table)"
