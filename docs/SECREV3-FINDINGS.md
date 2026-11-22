# SECREV-3 — adversarial security review of the v0.4.x wave (daefd64..354a0c6)

Protocol: threat-model first on the wave's NEW surface (2fa-status, user model-prefs,
provider-models, image DELETE, me-settings name/hidden_models, live titles, FE busses),
then code, then live PoC on a scratch instance (/tmp/secrev3-chatgo, pid-scoped,
ports 8144 app / 8145 fake-upstream; :8080 preview untouched). Gates re-run on the
wave tree: `go vet ./...` CLEAN · `go test ./... -count=1` 17/17 pkgs ok.

Prior-round invariants (SECREV-FINDINGS.md ledger: C1..C10, L1-H1/C6 nonce-burn,
401-before-409, no-existence-leak 404, bounded budgets, HKDF sealing) were treated as
established and are NOT re-flagged.

## Findings (worst-first)

### S2-1 — Admin per-model DISABLE is not enforced on the send path (pre-existing chats keep consuming a killed model)
The wave makes `models.enabled=false` reachable for the first time (before 4950b48, sync
forced enabled=true on every row — nothing in the product could write false; the
validateChatModel check at chats.go:228-238 was dormant). Enforcement now exists on
3 of 4 paths:
- new chat create + PATCH model-switch → 400 "model is disabled" (internal/apichat/chats.go:208,235)
- /v1 proxy → 403 model_not_available (internal/apimgr/proxy.go:356-373)
- **message send/regenerate → NO model check.** internal/chat/service.go:420-428 re-reads
  the provider row and states the policy in its own comment ("admins disable … to stop
  all traffic immediately, even into pre-existing chats") — but only checks
  `provRow.Enabled`. The chat's stored `model_id` is used as-is.

Impact: an attacker (any approved user) who created a chat on model X before admin
disables it keeps full access to X forever — quota/cost spend on exactly the model the
admin just turned off; the live-picker broadcast (chatgo:models-changed) even hides X
from the UI while it still burns upstream. Also covers rows DELETED upstream by sync
(vanished model = no row = no check).

PoC (proven live; scratch fake-upstream echoing `pong-from-<model>`):
```
admin:  PUT /api/providers/{pid}/models  [{"model_id":"killme","enabled":false}] → 200
attacker GET /api/models → killme GONE (picker truth)
attacker POST /api/chats {provider_id,model_id:"killme"} → 400   (create path checks)
attacker POST /api/chats/{pre-existing-id}/messages {"content":"proof"} →
  event: token  data: {"content":"pong-from-"}
  event: token  data: {"content":"killme"}        ← disabled model ANSWERED
control: PUT /api/providers/{pid} {"enabled":false} then same send → error-only
  (provider-level kill IS enforced — the asymmetry is the bug).
```
Fix: mirror proxy.go's `checkCachedModel` inside `Service.run` after the provider check
(or thread validateChatModel down); decide + document the vanished-row policy (refuse
vs. allow) while you're there.

### S3-1 — Encoded/unclean `/api/…` paths return the SPA shell (200 text/html) instead of JSON 404
Go 1.22 ServeMux matches the ESCAPED path (`RawPath`) while `spaHandler` JSON-404-gates
on the DECODED+CLEAN path (internal/api/server.go:192-196); when `%2f`+`..` collapses
out of `/api/` (e.g. `/api/images/..%2f..%2fadmin` → clean `/admin`), the request falls
through to the shell. Proven for GET and DELETE. Auth classification is safe: AuthPolicy
(server.go:145) evaluates the decoded path, and every probe
(`/api/users%2f1%2fmodel-prefs`, `/api/users/../settings`, `/api/providers/..%2f..%2fme`,
`/api/me/../../api/users/{id}/model-prefs`) answered the STRICTER-or-equal group
(401/403), never a handler. So: no bypass, no data — static shell bytes to a session
that can already fetch it — but it violates the "/api/* answers JSON 404" contract, and
a client that follows such a path gets HTML where it parses JSON. Fix: check the prefix
on `r.URL.Path` BEFORE `path.Clean` (or on the escaped path) at server.go:194.

### S3-2 — Bulk PUT /api/providers/{id}/models: no entry cap (admin-only DoS-by-parser + error echo)
providers.go:402-447: bounded overall (1 MiB body — CapJSONBody middleware +
`httpx.DecodeLoose` double-cap, render.go:19,60) — but 1 MiB ≈ 30-50k toggle entries
into a Go map; measured 10k entries → 404 in 3 ms, 3 MB body → 400. Reachable by ADMINS
only (policy admin branch server.go:158-161 + requireAdmin defense-in-depth both
verified), so impact is self-harm/typo, not attack. Same class: admin.go:558 echoes an
attacker-named 1 MiB key back in the 400 (`unknown setting key <k>`), response≈request
amplification, admin-only. Consider a 512-entry cap for symmetry with hidden_models.

### S3-3 — TOCTOU between PUT models and POST sync (whole-set Replace, admin-vs-admin)
`providerModelsUpdate` reads the row set (providers.go:~410 ForProvider) then
`ReplaceForProvider` (:441) wholesale; a concurrent `/sync` (:360) does the same.
Last writer silently wins: sync's just-added models can be dropped by an in-flight
toggle batch, or the reverse. Integrity nit at admin scale; a per-row
`UPDATE enabled WHERE provider_id AND model_id` would remove the window.

### S3-4 (NOTE) — `cleanTitle` keeps bidi/zero-width controls; me-settings `name` stores HTML raw
- Title path: user msg → task-model → cleanTitle (internal/chat/title.go:112-122: first
  line, 60-rune cap) → SSE → App/Sidebar. Every render is Svelte text interpolation
  (Sidebar.svelte:393, App.svelte:241, document.title) and the wave added ZERO new
  `{@html}` sinks (diff grep clean; the 3 sinks are the DOMPurify-gated markdown ones,
  C4 unchanged). No execution path; residual is visual spoof (U+202E RTL override,
  zero-width) in sidebar rows. Cheap win: strip Unicode categories Cf/Bidi_Control +
  any `[\p{Cc}\p{Cf}]` in cleanTitle.
- `name` (meSettingValidators, admin.go:278-298): rune-capped at 100, stored raw
  (`<img src=x onerror=1>` persisted verbatim — proven), served JSON-escaped; consumers
  all text-interpolate (Sidebar displayName, Avatar `initials()` = Unicode-letter-only
  transform chat-grouping.ts:59-64, UsersTab {u.name}). Self-only value, inert. Side
  observation (correctness, not security): `users.name` is never overlaid from
  me-settings in /api/auth/me, so the saved display name reverts to the registration
  name after reload — worth telling the FE owner.

### S3-5 (NOTE) — no rate bucket on chat creation → title-goroutine spawning; bounded, accepted
`POST /api/chats` has no `Auth.Allow` budget (grep: buckets exist only for
login/register/totp/password/imggen/kb/extract). A user can mint 10k chats, but a title
goroutine spawns only on the first MESSAGE turn — one per SSE request that already holds
its own request goroutine + upstream call — with a buffered chan(1), detached ctx and
`defer cancel()` (title.go:42-51): no leak, no fan-out ratio. Consistent with the C9
decision to throttle spend-bearing surfaces only. Note, not action.

## VERIFIED INVARIANTS HELD (live PoC, scratch :8144)

AuthN/AuthZ:
- GET /api/me/2fa/status: 401 anonymous; session-scoped read of OWN
  `users.totp_secret` only ( userID from session, totp_handlers.go:79-91); no bcrypt, no
  bucket consumed, no audit row, no cross-user state → no enumeration vector.
- AuthPolicy /api/users* claim VERIFIED at the layer, not trusted: admin branch
  (server.go:158-161) precedes the authed branch; attacker GET/PUT
  /api/users/{admin-id}/model-prefs → 403 (incl. self-target PUT → 403 — no user-side
  alias for the admin surface); defense-in-depth requireAdmin present in-handler;
  unknown user → 404 for admins, 403 for non-admins (no existence leak).
- /api/providers/{id}/models GET/PUT: 403 non-admin, unauth 401.
- PUT model-prefs writes ONLY `me.<target-uid>.{default_model,hidden_models}` (uuid ids,
  dotted-spoof id/key unreachable); `{"name":…}` on the admin route → 400 unknown key —
  admin power bounded to the documented model pair; audit row present and correct
  (`user.update · "<actor> set model prefs for <target>"`).
- me-settings dotted-key spoof `{"me.<admin-uid>.name":"evil"}` → 400.

Injection/limits:
- hidden_models: 513 entries → 400; 60k entries (>8KB) → 400 "too large"; `null`/scalar
  JSON → 400; size-check-before-validator on both me and admin paths. name 101 runes →
  400. 10 MiB body → 400 (MaxBytesReader, double-capped).
- Model cross-provider confusion: `shared-model` seeded under B; toggle against A →
  404 "model not found", zero rows touched (ForProvider-scoped, providers.go:402-447).
- Image DELETE: foreign id 404 · own-nonimage (txt) 404 · unknown 404 · unknown-vs-foreign
  indistinguishable · own 204 · re-delete 404 (race window answered honestly via
  row-first files.Delete, imggen/save.go:160-180, files/store.go:285-298; path is
  UUID-derived from the ROW, never from the URL id — resolvePath refuses escapes,
  store.go:312-321).
- Gallery paging: server still clamps per ≤ 100 (apichat/files.go:112-117).
- handleMeGet: `search_enabled` is a real JSON boolean (probe-typed); FE probe matches.
- Login.svelte TOTP leg: 401 → generic "wrong or expired" + re-mint flow, 429-honest
  branch; no enrollment signal (C6 semantics preserved).
- chatgo:models-changed is a payload-less Event (server refetch, no injection surface);
  chatgo:title detail is client-generated from the owner's own SSE stream and rendered
  text-only.
- SSE framing unchanged (json.Marshal payloads); wave diff adds NO new HTML sinks, no
  os/exec, no fmt.Sprintf→SQL, no uncapped io.ReadAll on new paths.
- Gates: go vet clean; go test ./... -count=1 all 17 packages green on this tree.

## Scratch hygiene
Built to /tmp/secrev3-chatgo/chatgo; DATA_DIR /tmp/secrev3-chatgo/data; ports 8144/8145
only; own pids 42440 + fakeup (recorded), killed individually — no pkill, :8080 untouched.
