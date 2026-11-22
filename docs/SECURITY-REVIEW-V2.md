# SECURITY REVIEW V2 — swarm roster (fire after wave-1+2 integration commit)

Target = merged v2 build (kb, library, memory, totp/audit/imggen, accent UI, all pages).
Same protocol as round 1: each lens = one agent, PoC required (failing test or curl
transcript), severity CVSS-ish + file:line, NO fixes by reviewers. Owned-file walls do
NOT apply (review = read + throwaway /tmp PoCs only, no repo edits). Then fixer swarm,
one agent per confirmed finding cluster, re-verify, commit, SECURITY.md update.

## Lenses

1. **AUTHN/2FA** — BE-SEC's totp_token stateless design (forgeable without root secret?
   purpose-bound? expiry? replay-nonce map growth bound?), timing-safe code compare,
   ±1-step window abuse, secret at-rest encryption (HKDF reuse — per-user nonce?),
   rate limits on verify, **password-based 2FA disable = downgrade vector? verdict +
   recommendation**, session issuance identical to password login path (no cookie
   divergence), OIDC flows untouched by totp gating, probe-endpoint (bodiless setup)
   info leak / limiter cost.
2. **IDOR/MULTI-TENANT** — every new route family (kb*, prompts, ratings, fork, export,
   me/memories*, images, audit): cross-user CRUD matrix, 404-vs-403 policy adherence,
   fork target-message must be in-chat, export zip contents (me-settings redaction
   actually drops secrets? chats of deleted state?), ratings on foreign chat's messages,
   kb enricher SQL re-check (chat.UserID) can a shared/forked chat id leak others' chunks.
3. **INJECTION/XSS** — new FE render paths: AuditTab detail column, Knowledge excerpts,
   search results, prompts insert, memories content, Images names, SettingsPrompts —
   text-interpolation everywhere or DOM sinks bypassing DOMPurify? kb fenced-block
   backtick-run neutralization bypass in enrich framing; export md structure injection
   (fake system turns when re-imported — low); SQL: every new query parameterized (grep
   for fmt.Sprintf SQL in new pkgs).
4. **SSRF/NETWORK** — kb embeddings client (ResolveAndCheck reuse correct + all hops),
   imggen proxy (admin-set provider base = trusted? document), extract's provider
   Complete path, timeout/deadline propagation (30s/60-120s), redirect-on-POST leaks,
   response-size caps actually enforced mid-stream (io.LimitReader before decode, not after).
5. **RESOURCES/RAM** — the user's core constraint: peak RSS under worst-case legit use —
   8×5MiB multipart chunks held where (per-file tx claim vs real buffers)?, 64-text
   embed batch payload build, brute-scan cost N chunks×dim for a 10k-chunk collection
   concurrent searches, 40MB image response→b64 decode memory, audit trim DELETE plan,
   zip stream buffer sizes, TOTP nonce map + 2fa pending-secret map eviction under churn.
   MEASURED numbers on the built binary with GOMEMLIMIT=100MiB, vs 128Mi budget.
6. **CRYPTO** — TOTP vs RFC 6238 vectors independently (write own implementation-compare),
   HMAC token construction (compare-then-parse timing, constant-time), base32 padding,
   secret entropy source (crypto/rand vs math/rand!), HKDF key separation vs provider
   keys (same info string = same keystream? nonce discipline), session token unchanged.
7. **BOUNDARY REGRESSIONS** — full route table re-audit vs AuthPolicy buckets incl. new
   /api/* families (any accidentally in public bucket?), SPA guard exemptions unchanged,
   admin allowlist additions (setup_done/accent/instance_setup_done: value caps,
   me.<uid>. namespace spoof attempt via dotted key in admin PUT), CSP adequacy for new
   features (img urls), CSP connect-src vs new endpoints, audit log content hygiene
   (secrets/PII into detail field?), rate-limiter coverage of new sensitive endpoints.
8. **TRUST/PUBLISH** — prompt-injection escalation chain: kb doc → enrich block → model
   exfiltration of OTHER chats? (model has no tools — but system_prompt+memory block =
   persistent state; can a kb doc steer memory extraction into poisoned 'About the user'
   facts that then ride every chat? verdict + mitigation options), extract-noise filter
   bypass, dependency scan (govulncheck, npm audit), go.mod diff, README/SECURITY claims
   vs shipped reality, Known Limitations honesty (2FA no-recovery? fork-of-shared chat
   share links still live?).

## Output protocol
Each lens: findings table (sev, file:line, PoC ref) + 'clean' areas list. Integrator
de-dupes, builds fix clusters by file-ownership, dispatches fixer swarm (max parallel,
disjoint files), re-runs full gates + smoke_v2.sh + targeted re-test of every PoC,
updates SECURITY.md Known Limitations, commits.
