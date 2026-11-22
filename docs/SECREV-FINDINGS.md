# SECREV-V2 consolidated findings (integrator ledger — feeds fixer swarm)

Target: c49d515 + 50b1ff6. Protocol: fixers per cluster (disjoint files), re-run PoC,
re-run bench/smoke_v2.sh, full gates, commit, SECURITY.md delta.

## LENS 2 (IDOR) — CLEAN; 3 Info items
- [L2-I1] internal/library/export.go:294,302-310 — value-signature redaction drops
  benign settings whose VALUE contains key/secret/token (mood="all good no secrets"
  vanished from user's own export). Fix: redact on key-signature only OR emit
  "redacted":[...] manifest entry in me-settings.json. PoC: /tmp/secrev-2/work/uxz2/.
- [L2-I2] audit global-across-admins + sequential integer cursor — BY DESIGN, wording
  note for SECURITY.md only (lens 8 delta covers).
- [L2-I3] original share link stays live after fork (fork itself correctly unshared) —
  existing Known-Limitation, keep + wording.
- Verified strong: kb enricher double re-check (enrich.go:31 + store.go:297), fork no
  share_token/ratings copy, zip excludes deleted + strips me.* + masks admin values,
  imggen strictly owner-scoped (admins too), me-settings dotted-key spoof rejected.

## LENS 3 (XSS/INJECTION) — CLEAN (no MED+); 2 LOW + 3 INFO
- [L3-L1] LOW library/export.go:147 — chat TITLE with newlines fabricates fake
  `## system · ts` turn in md export (structure spoof on re-import). Fix: flatten
  title to one line (pattern already exists at :174). PoC /tmp/secrev-3.
- [L3-L2] LOW/INFO library/export.go:179 — source Title/URL raw into md → crafted md
  links possible in exported file (no in-app renderer). Fix: <url> wrap + escape []{}.
- [L3-I1] INFO kb/enrich.go:105 fence defense BRUTE-FORCE PROVEN unbreakable (no ≥3
  backtick run survives ```→''' substitution); note for future JSON-sourced names.
- [L3-I2] INFO extra/md.ts:16 mutates SHARED DOMPurify instance (benign today; hook
  also applies to chat sanitizer). Fix: DOMPurify(window) per-instance.
- [L3-I3] INFO chat/markdown.ts:162 enhance() re-serializes sanitized HTML w/o re-sanitize
  (DOMParser round-trip; all mXSS payloads verified dead in jsdom — pattern hardening
  only: RETURN_DOM + final sanitize).
- Verified clean: {@html} inventory=3 sinks all DOMPurify-gated (payload matrix), every
  new surface text-interpolation, safeHref gating, kb:// inert spans, SQL all-bound,
  SSE json.Marshal framing, no zip-slip, mime allowlist no html/svg, zero eval/innerHTML.

## LENS 8 (TRUST/PUBLISH) — 2 MED, 2 LOW, 1 INFO, PUB items
- [L8-M1] MED memory extract poisoning (extract.go:27-30,173-197 + enrich.go:72-107):
  extractor prompt has NO untrusted framing; facts stored verbatim; injection block has
  no provenance label. PoC A direct, PoC B kb→model→extract→wire-confirmed. Fix set:
  (a) transcript framed as untrusted data + never-follow instructions (mirror kb/
  enrich.go:80-101 wording), (b) label (extracted)/(user-declared) in block, (c) store
  extracted as enabled:false pending user confirm, (d) one extract per chat limit.
- [L8-M2] MED unbounded audit growth: anon failed-login email audited UNVALIDATED
  (200KB → row 200,005 bytes measured); provider name length-unvalidated (300KB into
  detail). Fix: truncate email/detail in audit.Record (320B/1KB) + validate email shape
  pre-Record + cap provider name ≤200 runes.
- [L8-L1] LOW extract has no rate limiter (6×200 OK, each 24KB transcript + 30s model
  call). Fix: Auth.Allow("extract:"+uid, ~4/hour).
- [L8-L2] LOW export zip embeds LIVE share_token (bearer-equivalent). Fix: scrub from
  export JSON shape only (chat API keeps it).
- [L8-I1] INFO audit append-only ✓ but flooding evicts via 10k trim; not tamper-evident.
  → SECURITY.md (draft delivered, ready-to-apply markdown block).
- [L8-PUB] README stale (v1 features only, RAM table placeholder, version dev), NO v0.2.0
  tag exists (go.mod diff done vs f2d385e: EMPTY), govulncheck + npm audit clean, OWUI
  code-origin grep ZERO (design values docs-only, attribution present — Apache clean),
  OWNER placeholders in README/deploy (values TBD by USER). HOSTNAME DECISION MADE:
  public repo scrubbed to chat.example.com/idp.example.com (DONE); real deploy =
  chat.will.net replacing Open WebUI via k3s manifests in ~/git/homelab (private);
  publish-time filter-branch must sed any residual will.net + author rewrite.
- Chain verdicts: kb chat-leg framing CORRECT (live-wire verified); extract-leg = F-8.1;
  prompt library clean; fork share_token NULL invariant verified; XFF spoof holds.

## LENS 4 (SSRF) — CLEAN (no exploitable non-admin SSRF); 2 LOW, 5 INFO/DOC
- Guard VERIFIED strong: per-hop revalidation on redirect (307→169.254/10.x blocked, 3-hop
  cap), PINNED-IP dialing = DNS-rebind immune (live-proof w/ ALLOW_LOOPBACK=false),
  40MiB imggen cap enforced stream-side (measured abort at ~40MiB), ctx propagation ok,
  metadata/CGNAT hard-denied even with loopback on. All egress = admin-owned config;
  non-admin cannot steer a single byte (all steerable writes 403).
- [L4-L1] LOW imggen.go:420-428 nil-Cfg unguarded-dialer hatch in prod handler. Fix:
  refuse nil-Cfg dial outside tests.
- [L4-L2] LOW any approved user can exercise every enabled provider's key via provider_id
  → Known-Limitations line (doc, merge into C5).
- [L4-I1] dead setting: kb_embed_model documented as admin escape hatch but NOT in
  settingsValidators allowlist → unwritable. Add validator "providerID:modelID" or drop
  claim. (→ C3, apimgr/admin.go)
- [L4-I2] OIDC http:// issuer accepted vs "discovery via TLS" claim — soften doc or
  require https. [L4-I3] search shared dialer swap race = fail-closed availability.
  [L4-I4] 6to4/Teredo not decoded (theoretical) — deny 2002::/16+2001::/32 (cheap, C3).
  [L4-DOC] consolidate admin-power SSRF posture statement in SECURITY.md (C5).

## LENS 1 (AUTHN/2FA) — 1 HIGH, 2 MED, 2 LOW, 1 INFO
- [L1-H1] HIGH unauth memory-DoS: verify limiter keyed sha256(attacker token)[:8] →
  rotating garbage tokens mint never-throttled buckets + audit INSERT per req (40k/5s,
  0×429, +17.5MB/5s measured). Fix (C6): coarse per-IP cap BEFORE attacker-keyed bucket;
  structurally-invalid tokens rejected w/o audit insert.
- [L1-M1] MED verify budget amplification: per IP+token buckets × fresh-token-per-login
  ≈550/min steady state (measured 119/min one IP vs claimed 10/min). Fix (C6): parse
  token first + per-userID limiter.
- [L1-M2] MED OIDC login BYPASSES 2FA (email-link, mock-IdP PoC → full session, totp
  never touched). Fix (C7): oidc.go callback gates on totpEnabled → issue totp_token +
  redirect to challenge page. Undocumented today — must not ship silent.
- [L1-L1] LOW 2fa probe ordering: 409/400/401 before password check + bodiless POSTs
  burn setup bucket pre-validation (locks out legit setup). Fix (C6): password first,
  400 before bucket consume.
- [L1-L2] LOW ops: NO 2FA recovery path — disable needs session∧password; and
  SECURITY.md CLAIMS an admin password-reset endpoint that DOES NOT EXIST (doc lie).
  Fix (C8): admin reset-2fa action (clear TOTP + set-once password w/ audit) OR
  recovery codes; correct SECURITY.md either way.
- [L1-I1] INFO last-step replay map process-local → ≤90s code replay across restart.
- VERDICTS: password-disable = accepted (rec: re-auth + notify later); mass-setup maps
  bounded (pending 1024 cap); window −1/+2 ≤90s one-use verified.
- Pre-verified for lens 6: HKDF 4-way domain separation, constant-time compares,
  crypto/rand secrets, AES-GCM fresh nonces, token unforgeability.

## LENS 7 (BOUNDARY) — CLEAN (no boundary breaks); 2 MED(cost), 2 LOW
- 59-route table fully classified — ZERO bucket mismatches; raw-socket normalization
  matrix proves AuthPolicy/mux never diverge (307-clean before dispatch, re-enter after);
  pending/demote covers all v2; first_run bootstrap stable across 3 policy restart
  cycles; cookie parity password==TOTP; CSP fits imggen; /v1 hard 401 unchanged.
- [L7-M1] MED kb upload+search UNLIMITED (12/12 201 — embedding/provider spend + scan
  CPU per user). Fix (C9): Auth.Allow kb:upload + kb:search per-user.
- [L7-M2] MED extract unthrottled (8/8 200) — SAME fix as L8-L1 → C2.
- [L7-L1] LOW totp per-token limiter floor = lens-1 H1 same root → C6 (coarse per-IP).
- [L7-L2] LOW GET /assets/ anonymous DIR LISTING (dist.Open succeeds on dirs) — leaks
  chunk names today harmless, leaks feature map post-code-split. Fix (C9): Stat+IsDir
  reject → fall through to guard.
- INFO: /s/ bare stricter-than-intent (fine); oidc anon 302 by design (fine).

## LENS 4 — CLEAN (details above)
## LENS 6 (CRYPTO) — primitives CLEAN/exemplary; 1 HIGH (policy, merges w/ L1-M1)
- [L6-H1] == L1-M1 root, worse math: refillable per-(IP,token) buckets + token renewal
  per login + nonce burned ONLY on success ⇒ ~500 guesses/min/IP; adaptive p=3e-6/try →
  median break ~7.7h ONE IP (50 IPs ≈ 9 min). E2E-proved: 10×401-then-429 per token,
  refill confirmed. NOT fixed by shortening TTL. Fix (C6, NIST SP 800-63B §5.2.2):
  burn token nonce on EVERY verify attempt (forces re-login per guess, ≤5/min/IP)
  + per-user consecutive-failure counter (~15) invalidating all outstanding tokens
  + audit alert. This is THE must-fix before publish.
- Info×3 (all fail-closed/none-required): dotted-uid token unparseable→fail-closed
  (uuid-unreachable); base32 re-pad edge unreachable (server-only secrets); RFC-inherent
  DP bias 4.7e-4 (universal, document-only).
- CLEAN with proof: RFC6238 exact (3 independent impls incl bench/totpcode; 2038/2106/
  year-9999 bit-match; 5k random agreement) · 90s window math · constant-time everywhere
  (code hmac.Equal everywhere incl oidc state) · verify-then-expire unexploitable ·
  GCM nonce: 400 seal cycles + 100k bulk + raw DB hexdumps = ZERO reuse · 4 HKDF info-
  strings pairwise-distinct keys, no raw-secret derivation · no math/rand in prod ·
  SESSION_SECRET fail-fast ≥32B no fallback.

## LENS 5 — pending

## Fix status
- C4 DONE (md.ts+markdown.ts per-instance DOMPurify + final re-sanitize; 66/66 parity
  harness incl. reproduced-then-fixed hook leak; ADD_ATTR target correction).
  ⚠️ FOLLOW-UP discovered (pre-existing, preserved): <button> ∈ FORBID_TAGS ⇒ enhance()
  .code-copy/.code-wrap buttons stripped ⇒ CODE COPY BUTTONS MAY NEVER RENDER. Verify
  live at integration; if broken, fix = move copy/wrap to Svelte layer or allowlist
  button w/ type check. File: lib/chat/Markdown.svelte + markdown.ts (owner: integrator).
  → RESOLVED by C10 (see below).
- C10 DONE (verdict: NOT a C4 regression — copy buttons were broken since v0.1: marked's
  code renderer emitted them pre-pass-1 and FORBID_TAGS killed them from day one; Svelte
  delegation handlers were dead code. Fix: button creation moved INTO enhance() via DOM
  APIs post-pass-1; ENHANCE_CFG FORBID_TAGS = copy minus 'button' (attacker buttons still
  die in pass 1; ENHANCE_CFG strips on*/form/formaction/autofocus as 2nd line). 198-check
  harness: 0 foreign buttons across 21 smuggle vectors ×2, restored output byte-identical
  to original renderer markup, Markdown.svelte untouched. Copy feature LIVE for 1st time.)
- C2 DONE (extractor prompt = UUID-stamped BEGIN/END untrusted-frame (forge-proof), no
  instructions-inside rule; injected block gets provenance header + per-line (extracted)/
  (user-declared) labels; extracted rows enabled:false until PUT toggle (manual stays on;
  FE toggle+badge VERIFIED real, no FE change); dual limiter: 1/chat/10min + ~4/hr per
  user, 400s-before-bucket, upstream untouched while limited. 14/14 tests. smoke_v2 needs
  NO edits (zero memory assertions in bench/ — grep-verified).)
- C1 DONE (share_token scrubbed at the ONE writeChatJSON marshal choke point via
  exportChatShape wrapper (fork/API chatJSON mirror untouched; zip members + bulk clean);
  md title flatMDLine (single '# ' line guaranteed, JSON stays lossless); source links:
  http(s)-only → escaped label + percent-encoded <dest>, kb:///javascript:/malformed →
  plain name; L2-I1 verdict = keys UNREACHABLE from exports (me.* namespace allowlist) →
  value-signature silent-drop REMOVED (benign values ship), excluded keys listed in new
  redacted-settings.json manifest. SECURITY.md line already true, no tweak. -race ok.)
- C9b DONE (kb: 5/min upload + 20/min search + 10/min create per-user via Auth.Allow,
  429 pre-body-parse, isolation tested; NO re-embed route exists (chat-turn embed rides
  the chat path — integrator note). /assets/ listing WAS live anonymously (proven 200 +
  <pre> index) → Stat/IsDir reject before FileServerFS, dirs fall to auth guard. imggen
  nil-Cfg was an SSRF HATCH: cfg==nil returned plain net.Dialer w/ ZERO guard, reached
  upstream in repro → handler 503 up front + guardedClient refuses nil outright (hatch
  deleted). All pkg tests green incl full ./internal/api.)
- C3 DONE (audit chokepoint RecordWithRetain clamps email/user_id 320B + detail 1KB w/
  UTF-8-rune-safe "…", truncate-never-error; login now validEmail-precheck BEFORE bucket/
  bcrypt/audit → malformed = 400 zero-audit-rows, enumeration-safe (same gate as register/
  OIDC); provider name 200-rune cap create+update (validate()), patch semantics kept.
  Final gate: -race ALL ok incl FULL internal/auth suite 268s green → C6's tree compiles
  + passes as of C3 finish.)
- HOMELAB SCAFFOLD DONE (~/git/homelab/apps/chatgo/, 12 files STAGED, nothing applied):
  chat.will.net w/ 3-ingress ratelimit pattern, 64Mi/128Mi + GOMEMLIMIT=100MiB, /healthz
  probes, TRUSTED_PROXIES=10.42.0.0/16 (Traefik pod mode via MetalLB VIP), homelab-ca-
  issuer cert, local-path PVC manual-apply, secret.example per repo style. CRITICAL
  FINDINGS: (1) Authelia puts email_verified in UserInfo ONLY by default but chatgo
  parses it from id_token (oidc.go:79) → AUTHELIA-CLIENT.md adds claims_policy w/
  email_verified (same pattern as headlamp/radar/semaphore). (2) Go+distroless needs
  homelab-ca-cert mounted + SSL_CERT_FILE=/certs/ca.crt or internal-CA OIDC discovery
  SILENTLY fails — deployment.yaml handles it. (3) chat2.will.net staging host = free
  (wildcard DNS). (4) apps/kustomization.yaml NOT edited on purpose — Flux handover is
  CUTOVER step 4 sed. CUTOVER.md: build→client→stage on chat2→swap host→OWUI dark 1wk.
  ⚠️ homelab had an AUTONOMOUS git add -A sweeper: commit 0a6cd81 "security:
  allowPrivilegeEscalation" (pushed to origin/main) silently swallowed 4 of the scaffold
  files w/ unrelated message — suspected ZeroClaw agent; remaining 8 files untracked,
  may be swept next run. USER SHOULD AUDIT what commits in their repo. Chat history NOT
  migrated (schema diff) — restic snapshot noted pre-OWUI-delete.
- PUB-1 DONE (container path VERIFIED end-to-end: image 25.2 MiB unpacked / 6.4 compressed,
  nonroot UID 65532, idle cgroup RSS 8.7 MiB, --read-only+vol works, CI↔embed coherent
  (workflow = ci.yml), quickstart + all 19 README env vars + 9 routes verified real).
  Docs fixed by integrator: RAM table FILLED (8.7 container / 17-26 host rows), phantom
  deploy/README ref killed, --read-only quickstart bullet added, make docker tag →
  chatgo:local (Makefile; CI unaffected), Dockerfile engines comment. #5 .gitignore
  archive claim = FALSE ALARM (index.html tracked; git archive contains it — proven).
  docs/PUB-IMG-QA.md written by agent.
- PUB-2 DONE (LICENSE AUDIT CLEAN: 16 Go mods + 135 npm pkgs, zero GPL/AGPL/SSPL/CC-NC;
  dompurify MPL/Apache dual passes on Apache leg, lightningcss MPL is build-time-only).
  No live secrets in tree OR history (7 revs swept); fixtures synthetic; PoC docs
  narrative-only. Applied by integrator: .gitignore += deploy/secret.yaml (was a real
  add-A hazard), web/package.json license field, LICENSE appendix → "Copyright 2026 The
  ChatGo Authors" (user can restyle). PUBLISH-TIME HISTORY WORK (mandatory): mailmap
  willuhmjs@gmail.com + will@local; sed will.net→example.com across revs (commit the
  tree fix FIRST); reword/squash "swarm" commit subjects. EXCLUDE from public repo:
  docs SECREV-FINDINGS / SECURITY-REVIEW-V2 / INTEGRATION-TODO / WAVE2-PLAN / PUB-*.md
  (filter-repo paths, or ship whole after release if user wants radical transparency).
  docs/PUB-LICENSE-QA.md written by agent.
- C6 DONE (THE HIGH CLOSED: limiter chain ip30→parse→uid10→killstamp→burn-per-attempt
  →consume→15-fail lockout+audit alert; token format FROZEN (killstamp deny-list via
  derived mint-second — epoch-in-MAC structurally impossible vs C7/lens-6 asserts);
  forge/expiry = NO audit rows (amplifier closed); zero attacker-keyed state (uid keys
  only from valid tokens); forgetTOTPState = single scrub point incl new maps; PoC:
  119→4 code-checks/min, 40k flood → 0 audit rows, lockout trips @15 then recovery
  works; budget 7.7h→32d/IP, ≥16d distributed. 6 green runs incl -race. Integrator:
  fixed C7 test flake (35s → next-step formula) — C6's one-liner, 2 sites.)
- DOC RE-LINKS ALL PASS (SECURITY.md: throttle sentence==C6 killstamp semantics, reset-
  2fa==C8, exports==C1 scrub, memories disabled pending approval==C2, OIDC gate==C7).
- GAUNTLET: BACKEND GREEN (gofmt/vet/build + go test ./... 17/17 pkgs) · FE GREEN
  (svelte-check 0 err, vite built) · smoke_v2 50/50 PASS on fixed tree · committed
  7f781e1 · preview :8080 restarted on v0.3.1-fixround w/ SESSION_SECRET preserved
  (user stays logged in) · RAM RE-BENCH DONE: 18.0 idle / 20.2 steady (ps-rss caveat,
  ~1.5-2x vs linux) → README host row refreshed 17/26→18/20. LENS 5 CLOSED BY
  REPLACEMENT: linux numbers from PUB-1 container audit (8.7 MiB cgroup idle), state-
  eviction design verified by C6's map table. GAUNTLET COMPLETE.
- C5 DONE (README v2 + SECURITY.md post-fix delta; refused premature claims; re-link
  deps on C1/C2/C6 at final verify; RAM row placeholder).
- C7 DONE (oidc.go 2FA gate reusing issueTOTPToken — no wrapper needed; Login.svelte
  full TOTP UX both legs w/ URL scrub; token-in-URL risk documented+bounded; replay +
  no-provision-dup tests; C6-semantics-tolerant tests; FE treats 401/403/429).
- C8 DONE (admin POST /api/users/{id}/reset-2fa: once-password w/ audited no-leak,
  sessions ALWAYS revoked, OIDC-only never gets fabricated password, fail-intact
  ordering; 4 tests; UsersTab 2FA column action w/ confirm + shown-once reveal).
  NOTE: tab-local fetch (api-admin.ts outside wall) — hoist later maybe.
- C9a DONE (guard.go: 6to4 2002::/16 + Teredo 2001::/32 literal deny, override-proof,
  mixed-answer pins global-only; apimgr: kb_embed_model validator, splitSpec-exact).
- C9b HELD w/ C1/C2/C3/C6 until LENS 5 (kb rate limits, /assets/ dir-listing,
  imggen nil-Cfg, kb handlers).
  → HOLD RELEASED (integrator judgment): Lens 5 measures the RUNNING binary, so source
  edits can't skew its RSS numbers (only code refs may drift — re-anchor at integration);
  Lens 5 likely orphaned by the earlier pkill incident anyway. C6/C2/C1/C3/C9b ALL
  DISPATCHED in parallel, disjoint walls (C6: totp*+auth.go+twofa tests; C3: audit.go +
  handlers.go + providers.go — same pkg as C6, disjoint files; C2: internal/memory;
  C1: library/export.go; C9b: kb/handlers + api/server static + imggen). Each re-runs
  its PoC on throwaway ports (8094/8095; 8080/8097/8099/9099/9100 reserved).
## Fix clusters (build as findings land)
- C1 EXPORT cluster — library/export.go (+library.go shape): scrub share_token (L8-L2),
  flatten title newlines (L3-L1), wrap/escape source links (L3-L2), key-only redaction
  + redacted-manifest (L2-I1). One agent, one file + tests.
- C2 MEMORY cluster — internal/memory/: untrusted-data extractor prompt (a), provenance
  labels in block (b), extracted stored enabled:false (c), per-chat once + Auth.Allow
  rate limit (d=L8-L1). Tests + verify no FE change needed (SettingsMe shows enabled
  toggle + source badge already).
- C3 AUDIT-CAP cluster — internal/audit (truncate email 320B/detail 1KB in Record) +
  internal/auth/handlers.go (email shape validate pre-RecordIdentity) +
  internal/apimgr/providers.go (name ≤200 runes). Note providers.go also touched by…
  nobody — safe.
- C4 WEB-HARDEN cluster — extra/md.ts DOMPurify(window) per-instance (L3-I2) +
  chat/markdown.ts enhance() final sanitize (L3-I3).
- C5 DOCS cluster — SECURITY.md apply lens-8 delta (MINUS the share-token bullet —
  fixed by C1), README v2 features + RAM table (needs LENS 5 numbers), drop (v1) suffix.
- C6 TOTP-THROTTLE cluster (TOP PRIORITY) — internal/auth/totp_handlers.go + totp.go +
  auth.go limiter sites: coarse per-IP pre-cap before attacker-keyed bucket; parse-token
  then PER-USER limit; BURN NONCE ON EVERY ATTEMPT (L6-H1); per-user consecutive-failure
  counter (~15) → invalidate outstanding tokens + audit alert; invalid tokens rejected
  before audit/bucket; probe ordering password-first; 400 before bucket consumption.
  Tests: replay lens-1 poc3/poc4 + E2E token-lifecycle (expect ~2%/day guess budget).
- C7 OIDC-2FA cluster — internal/auth/oidc.go (+ minimal FE: challenge route reuse —
  Login.svelte totp step exists? verify; may need ?totp= pending-token UX) + test w/
  fake IdP à la poc7.
- C8 RECOVERY cluster — apimgr/users.go + internal/auth new file (admin reset-2fa:
  clear totp_secret + issue set-once password, audited, admin-only) + web UsersTab
  button + SECURITY.md truth. (OR recovery codes — bigger; pick minimal admin action.)
- C9 MISC-BACKEND cluster — kb/handlers.go (kb:upload/kb:search per-user limits L7-M1)
  + api/server.go (Stat+IsDir reject on /assets/ L7-L2) + imggen/imggen.go (nil-Cfg
  refuse L4-L1) + search/guard.go (deny 2002::/16+2001::/32 L4-I4) + apimgr/admin.go
  (kb_embed_model validator, dead-door fix L4-I1).
- PUB (needs USER): v0.2.0 retro-tag or accept diff-from-f2d385e; OWNER placeholder
  values. (Hostname scrub DONE Nov 22 — see L8-PUB note: public=example.com, real
  deploy chat.will.net via homelab repo.)

## Coordination note
A security agent's cleanup SIGTERMed the RAM bench + :8080 preview (pattern pkill).
Lenses warned; if recurrence, re-check preview pidfile after swarm.
