# SECREV-4 — adversarial security review of v0.4.10..v0.4.14 + the production rollout

Protocol: four parallel adversarial lenses with disjoint file walls, each running its
own scratch instance from the v0.4.14 binary (ports 8151/8152+9152/8153+9153, own
DATA_DIRs; the :8080 preview DB and chat.will.net were OFF-LIMITS for writes — the
production lens was strictly read-only + GET/HEAD). Every finding below carries a live
PoC transcript in /tmp/secrev4/{A,B,C,D}.md. Scope delta: everything since ef79d9f
(retract endpoint, key-unreadable errors, markdown list renderer, task-model/CI churn)
PLUS the first-ever audit of the live deployment (Traefik/Authelia/manifests/git-history).
Prior-round invariants (SECREV-FINDINGS.md, SECREV3-FINDINGS.md ledgers) treated as
established; re-verified spot-checks only.

## Findings (worst-first)

### S4-1 (MED, FIXED) — Task-model dispatch ignores the admin kill-switch
`taskProvider` (internal/chat/prompt.go) checked only KeyUnreadable: with
`providers.enabled=0` AND the task model's own row `enabled=0`, title generation and
rolling summaries still dialed the provider with its stored key (A: egress observed on
a normal user turn; C: disabled task provider's reply BECAME the chat title). The
S2-1 asymmetry, relocated from the send path to the utility paths.
Fix: `store.Models.ModelState` gates provider-enabled + model-row-enabled +
key-readable before dispatch; chat-adjacent paths (title/summarize) fall back silently,
user-facing extract answers honest 503s. `service_taskgate_test.go` (4 cases) +
`TestExtractRefusesDisabledTaskModelRow`.

### S4-2 (MED, FIXED) — Background paths ship transcripts UNAUTHENTICATED when a key is unreadable
Memory-extract, imggen, provider sync and kb-embed lacked the pre-dial KeyUnreadable
gate that chat send got in eaf440c: the mock upstream received full request bodies with
`Auth: None` and users got "check this provider's API key" AFTER third parties saw the
data. Fix: honest error before any dial on all four (4 tests asserting zero upstream
hits). Deviation: kb-embed refuses (503) rather than silently switching embedding
models — vector-space consistency.

### S4-3 (MED, FIXED — data-loss) — Editing a provider with a rotated secret DESTROYS its sealed key
`PUT /api/providers/{id}` with no `api_key` field re-sealed `APIKey=""` → column NULL.
Proven: rename such a provider, restore the original SESSION_SECRET, and the key is
gone forever while sibling rows decrypt fine. `clearOtherDefaults` shared the sink.
Fix: repo `Get`/`List` capture the raw ciphertext (unexported `sealedKey`) and `Update`
carries it verbatim for unreadable rows — one choke point fixes both callers.
`TestUpdatePreservesUnreadableSealedKey` (S1→S2→rename→S1 decrypts).

### S4-4 (MED, FIXED) — Model sync has no read deadline
Slow-drip upstream pinned two handler goroutines for 120s (size was capped 8 MiB,
time was not). Fix: 60s `syncTimeout` context around `ListModels` only, honest 504;
chat streaming untouched. `TestProviderSyncTimesOut`.

### S4-5 (LOW, FIXED) — The documented "CSRF dance" did not exist
Repo-wide grep for csrf in internal/ was EMPTY; the X-CSRF-Token header every client
(the review recipes included) dutifully sends is ignored. Sole barrier: SameSite=Lax —
sound for POST-XHR today, but undocumented depth. Fix: `OriginGuard` middleware —
mutating /api/* with an Origin/Referer header must match Host (scheme-insensitive,
fail-closed); headerless CLI/API-key clients pass (documented contract); /v1 exempt
(Bearer contract). 9-case matrix `TestOriginGuard` + live 4-way spot check on the
v0.4.15 preview (evil-origin 403, same-origin & no-origin reach auth, /v1 untouched).

### S4-6 (LOW, FIXED) — ADMIN_EMAIL minted admins FOREVER
The bootstrap knob applied at every registration/OIDC provisioning, so email-takeover
of the ADMIN_EMAIL address = admin at any point in the instance's life. Fix: admin
grant is now strictly first-account-bootstrap (tx-safe); knob deprecated in
config/README. `TestAdminEmailNeverPromotes` / `TestFirstAccountIsAdminBootstrap` +
OIDC twins. (Prod unaffected: the admin row already exists.)

### S4-7 (LOW, FIXED) — provider sync was the only unaudited admin mutation
Whole-cache Replace (incl. auto-enabling new models) wrote zero audit rows. Fix:
`provider.sync` rows — `name · N models (M new)` on success, `name · sync failed` on
failure; never upstream error text (which can carry the base_url). `TestProviderSyncAudits`.

### S4-8 (LOW, FIXED) — DOM clobbering from model output
Assistant markdown could inject `id="app"`/`name` collisions into the admin app and
anonymous share pages (live dup-count 2). Inert today (sole getElementById is
bootstrap-time) but load-bearing for ID selectors later. Fix: `SANITIZE_NAMED_PROPS:
true` in the shared PURIFY_CFG (share/compare pipeline inherits). Task-list
checked-state rides classes → unaffected.

### S4-9 (LOW, FIXED) — CI: govulncheck@latest (supply-chain drift, same class as the gosec incident)
Pinned `@v1.10.0` (tag verified upstream).

## Accepted (documented, not fixed)

- **A4-L1 differential /v1 errors** (503 key-unreadable w/ name vs 403 disabled vs 404):
  lets an approved user enumerate provider existence/seal state. Every bit disclosed is
  already visible to that role in the picker; locking it down costs supportability.
- **A4-L2 key-blob AAD=nil** — ciphertext replays across provider rows. Precondition is
  write access to the SQLite file, at which point the attacker holds the DB outright.
  Re-keying every stored key to add an AAD is worse than the risk.
- **B4-1 CSP-violation noise** (~18 script-src-attr logs per poisoned render): inherent
  to parsing raw HTML pre-sanitize; proof-of-safety is that nothing executes even with
  CSP removed.
- **B4-3/4** protocol-relative & attacker-styled links on /s/ (phishing surface only);
  RTL/bidi marks in bodies (markdown twin of SECREV3 S3-4).
- **D4** /healthz version string + /api/auth/config first_run/oidc_name unauth disclosure:
  ops value outweighs; both were already in SECREV-3-era behavior.

## Held under fire (live PoC, negative results worth recording)

- Retract endpoint: ownership-404 before lookup, 409-before-mutation, all 8 cross-user/
  cross-chat/share/anchor/double-delete IDOR cases clean; stream-guard race proven.
- XSS: ~90 vectors × 3 rounds through BOTH render pipelines, headless Chrome with every
  string→DOM sink hooked, with AND without CSP — zero execution; `{@html}` inventory 3,
  all sanitizer-gated; CSP covers /s/ too.
- SSRF: loopback/metadata/IPv6/decimal/hex IPs, DNS-fail, and per-hop redirect checks all
  blocked; zero egress in prod posture. No cookie/session leak through the proxy. No key
  sentinel ever in logs/audit. Seal nonce freshness + SESSION_SECRET fail-fast intact.
- Send/regenerate/switch/proxy/kb gates (S2-1 enforcement) re-confirmed on this tree.
- Production posture (all PASS, evidence in D.md): zero secret values in ANY commit
  (secret.yaml untracked + gitignored, `git log --all -S` empty; authelia commits
  argon2-only); rate limiter live (~13 rps 429s); HSTS + 308 + CSP frame-ancestors none;
  pprof/metrics behind auth; OIDC state cookie HttpOnly/Secure + PKCE-S256; GHCR exposure
  scoped to chatgo; CI SHA-pinned, no pull_request_target, least-priv GITHUB_TOKEN;
  open-webui rollback box intact (0/0, Bound Retain PV).
- Deployment follow-ups landed same night (homelab repo): pod seccomp RuntimeDefault +
  automountServiceAccountToken:false (verified live), image digest-pinning adopted.

## Ops note

Mid-review, the Mac's kube current-context was hijacked by a stale local kind cluster
(`backshots-e2e`, unrelated project leftover) — the hardening apply failed SAFE
(missing-namespace error) and production was never touched; context renamed to
`kind-backshots-e2e-unused` and explicit `--context default` adopted for prod kubectl.

## Gates on the remediation tree (v0.4.15)

`gofmt`/`go vet` CLEAN · `go test ./... -count=1` 15/15 pkgs ok · svelte-check 0 errors ·
web build ✓ · SMOKE-V2 OK · Origin guard live spot-check 4/4.
