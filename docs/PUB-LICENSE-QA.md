# PUB-LICENSE-QA — final legal + leak audit before public Apache-2.0 release

Auditor: PUB-2 (read-only). Date: 2026-11-22. Scope: HEAD + full git history (7 revs),
tracked files, working tree, dependency trees. No files modified except this report.

---

## A) LICENSE COMPATIBILITY (outbound Apache-2.0)

### Go — direct + indirect (from go.mod, licenses fetched from upstream LICENSE files)

| Module | Ver | License | Verdict |
|---|---|---|---|
| github.com/coreos/go-oidc/v3 | v3.21.0 | Apache-2.0 | PASS |
| github.com/google/uuid | v1.6.0 | BSD-3-Clause | PASS |
| golang.org/x/crypto | v0.59.0 | BSD-3-Clause | PASS |
| golang.org/x/image | v0.48.0 | BSD-3-Clause | PASS |
| golang.org/x/oauth2 | v0.38.0 | BSD-3-Clause | PASS |
| golang.org/x/time | v0.16.0 | BSD-3-Clause | PASS |
| modernc.org/sqlite | v1.63.0 | BSD-3-Clause | PASS |
| github.com/dustin/go-humanize | v1.1.0 | MIT | PASS |
| github.com/go-jose/go-jose/v4 | v4.1.4 | Apache-2.0 | PASS |
| github.com/mattn/go-isatty | v0.0.24 | MIT | PASS |
| github.com/ncruces/go-strftime | v1.1.0 | MIT | PASS |
| github.com/remyoudompheng/bigfft | v0.0.0-20230129… | BSD-3-Clause | PASS |
| golang.org/x/sys | v0.50.0 | BSD-3-Clause | PASS |
| modernc.org/libc | v1.80.1 | BSD-3-Clause | PASS |
| modernc.org/mathutil | v1.7.1 | BSD-3-Clause | PASS |
| modernc.org/memory | v1.12.1 | BSD-3-Clause | PASS |

**Zero copyleft violations. No GPL/AGPL/SSPL/CC-BY-NC anywhere.**

go.sum-only modules (build-graph, NOT linked into the binary): google/pprof (Apache-2.0),
hashicorp/golang-lru/v2 (MPL-2.0), golang/x-mod, x-sync, x-tools (BSD-3), modernc.org/cc,
ccgo, gc, opt, strutil, sortutil, token, fileutil, goabi0 (BSD-3 family — cznic "The X
Authors" texts verified for all except modernc.org/util, whose mirror moved; historically
BSD-3, graph-only, zero outbound exposure). All PASS.

### npm — bundled/built deps (licenses from package-lock.json metadata)

Run/bundle-time (ship inside the JS bundle):

| Package | Ver | License | Verdict |
|---|---|---|---|
| dompurify | 3.5.0 | MPL-2.0 OR Apache-2.0 (dual) | PASS (use Apache leg; MPL file-level copyleft fine anyway) |
| highlight.js | 11.12.0 | BSD-3-Clause | PASS |
| marked | 15.0.12 | MIT | PASS |
| lucide-svelte | 0.525.0 | ISC | PASS |
| svelte | 5.59.1 | MIT | PASS |

Build-time only (not shipped): vite 6.4.6 (MIT), @sveltejs/vite-plugin-svelte (MIT),
tailwindcss / @tailwindcss/vite / oxide (MIT), typescript 5.9.3 (Apache-2.0),
svelte-check (MIT), rollup/esbuild (MIT), **lightningcss 1.33.0 (+all platform pkgs)
(MPL-2.0) — PASS**, CSS is emitted and linked, no MPL code enters the bundle, and even
bundled MPL is compatible w/ notice, acorn/aria-query/axobject-query (MIT/Apache-2.0),
graceful-fs (BlueOak-1.0.0 — permissive), tslib (0BSD). Remaining ~90 transitive pkgs in
the lockfile: MIT/ISC/BSD/Apache/0BSD/BlueOak — machine-verified from lock metadata;
**no GPL/AGPL/CC-NC in the entire tree.**

### Repo legal surface

- `LICENSE` — byte-identical to canonical Apache-2.0 (diffed vs apache.org text). PASS.
  - GAP: the appendix still carries the literal `Copyright [yyyy] [name of copyright owner]`
    placeholder and there is **no actual copyright notice anywhere in the repo** (no
    per-file headers, no NOTICE). Apache-2.0 doesn't mandate per-file headers, but ship a
    line: `Copyright 2026 <GitHub handle / legal name you choose to publish>`.
- `NOTICE` file — none. Needed: go-oidc, go-jose, typescript (Apache-2.0 upstreams with
  NOTICE files) are statically linked / bundled. Recommended, low effort: one NOTICE
  listing those copyright lines. Not blocking (several upstream NOTICEs are trivial), but
  cheap compliance credit.
- `web/package.json` — **no `license` field**. OK-ish (`"private": true` blocks npm
  publish) but add `"license": "Apache-2.0"` for scanners (licensee/REUSE/GitHub linguist).
- `go.mod` header — clean (`module chatgo`, `go 1.27.4`, no oddities). PASS.

---

## B) LEAK SWEEP

### B1. Personal identifiers — TRACKED FILES (working tree + HEAD)

`will\.net | @will | will@ | /Users/will | ZeroClaw | Faircloth | gmail` → **0 hits in the
working tree.** All tracked-file emails are synthetic fixtures (`a@x.io`, `victim@x.io`,
`alice@example.com`); all IPs are RFC5737/doc or k3s CIDRs; obviously synthetic. PASS.

### B2. High-entropy strings (40+ b64, hex64) — every hit judged

| Location | Content | Judgment |
|---|---|---|
| Dockerfile:10,32,55,62,63 + deploy/deployment.yaml:38,39 | sha256 image digests | synthetic-by-design (supply-chain pinning) — PASS |
| .github/workflows/ci.yml:56,58,77,99 | action commit SHAs | PASS |
| internal/imggen/imggen_test.go:25 | `tinyPNG` 1×1 PNG base64 | obviously fake fixture — PASS |
| remaining 40+ "hits" | long camelCase test-func names | false positives — PASS |

- **No live credentials found in any doc.** docs/SECREV-FINDINGS.md, SECURITY-REVIEW-V2.md,
  SECURITY.md, docs/*.md: zero credential-shaped strings (checked b64/hex64/base32-TOTP/
  `chatgo_session=`/`otpauth://`/`sk-`/PEM). SECREV-FINDINGS *describes* PoCs (share_token,
  TOTP findings) narratively — no pasted cookie/secret values. LIVE-VS-SYNTHETIC verdict:
  **no live material.**
- bench/measure.sh:51, bench/smoke_v2.sh:156, Makefile:9 — `SESSION_SECRET="$(openssl
  rand -hex 32)"` generated at runtime. **No hardcoded values, now or ever** (verified with
  `git log --all -S SESSION_SECRET -p` + hex64 sweep across all 7 revs). PASS.

### B3. secret.yaml / .env

- `deploy/secret.yaml` NOT tracked (only `secret.example.yaml`, placeholder-only). PASS.
- **GAP (MED): `.gitignore` does NOT cover `deploy/secret.yaml`.** No `.gitignore` line for
  it (only `.env`, `data/`, `*.db*` etc.). If a live fixer writes a real secret.yaml and
  publish uses `git add -A`, it commits a live SESSION_SECRET. Add `deploy/secret.yaml` to
  .gitignore before the publish commit.
- No `.env` file anywhere tracked or untracked. PASS.

### B4. Git history (7 commits, `b44700d..50b1ff6`) — MUST-FIX before publish

| # | Finding | Severity | Detail |
|---|---|---|---|
| H1 | **Real author identity in commit metadata**: 4 of 7 commits authored `William Faircloth <willuhmjs@gmail.com>`; 3 by `will <will@local>` | **HIGH (identity leak)** | Appears in `git log`/GitHub blame for everyone, forever. Not fixable by sed on files — needs `git filter-repo --mailmap` (or env-filter) or squash to one clean init commit. |
| H2 | **will.net hostname in past blobs**: README.md:26, deploy/certificate.yaml:12, deploy/configmap.yaml:2,10-11, deploy/ingress.yaml (×6), deploy/middleware.yaml:40, docs/CONTRACTS.md:208 in revs c49d515/0c1f6dc/50b1ff6 (`chat2.will.net`, `sso.will.net`, `chat.will.net`) | **MED** | Working tree already rewrites these to `chat.example.com`/`idp.example.com`, but the fix is UNCOMMITTED — history rewrite (the planned hostname sed) must cover `will.net`, `chat2.will.net`, `sso.will.net`, `chat.will.net` across `--all`. |
| H3 | Commit subjects: "security: fix round from 8-lens review swarm", "integration: wire swarm outputs…" | LOW (optics) | Publishes the AI-swarm dev-process + an audit narrative. Same rewrite pass can squash/reword. |
| H4 | `/Users/will`, `ZeroClaw`, names, emails in blobs/history | — | **CLEAN, 0 hits in all history.** |

filter-branch/filter-repo sed list (add to the planned hostname rewrite):
1. `s/chat2\.will\.net/chat.example.com/g; s/sso\.will\.net/idp.example.com/g; s/will\.net/example.com/g` (blobs, all revs)
2. author/committer mailmap → chosen public identity (covers `William Faircloth`, `willuhmjs@gmail.com`, `will <will@local>`)
3. optional: squash/reword commits mentioning "swarm"/"lens"
Nothing else found: no absolute paths, no tokens, no names in blobs.

### B5. .github/workflows

Single `ci.yml`. Actions pinned by full SHA, permissions minimal, only `secrets.GITHUB_TOKEN`
(auto-provided) referenced — no missing repo secrets, no personal org names
(`github.repository_owner` is dynamic). PASS.

### B6. Embedded binary artifacts

`internal/web/dist/` ships **only a placeholder index.html** (per internal/web/web.go
go:embed contract). No JS/CSS/`.map` tracked → no sourcemap leakage possible. No will.net
in it. Note: index.html references `assets/index-BPgC52bG.js` which doesn't exist in git —
harmless placeholder, but consider making it a neutral stub. PASS.

---

## C) META / inventory + docs keep-exclude

266 tracked files, largest = web/package-lock.json 76K — no junk, no binaries, no giant
blobs. LICENSE ✅ README ✅ SECURITY.md ✅ CONTRIBUTING ✅. `OWNER` placeholders in
README/SECURITY/deployment are intentional ("replace OWNER" note in README:71) — fill at
publish. Untracked stray files near the publish commit: `docs/PUB-IMG-QA.md`,
`docs/SECREV-FINDINGS.md`, WIP `internal/auth/admin_recovery*.go` — decide consciously,
don't let `git add -A` sweep them.

### docs/*.md judgment (honest security worklogs — ship or drop, don't half-redact)

| Doc | Content | Recommendation |
|---|---|---|
| docs/SECREV-FINDINGS.md (untracked, 255L) | Findings ledger: file:line map of HIGH/MED issues incl. fix status | **EXCLUDE** (gitignore / don't add). Highest attacker value, no user value post-fix. |
| docs/SECURITY-REVIEW-V2.md (tracked, 61L) | Audit "lens" roster — checklist of every attack surface probed | **EXCLUDE** — pure find-map scaffolding once findings are fixed; SECURITY.md already carries the honest posture. |
| docs/INTEGRATION-TODO.md (tracked, 51L) | Internal multi-agent handoff notes (BE-RAG/FE lanes) | **EXCLUDE** — process noise, zero reader value, broadcasts the agent pipeline. |
| docs/WAVE2-PLAN.md (tracked, 27L) | Same family (wave/agent plan) | EXCLUDE for consistency (borderline). |
| docs/AUTHGATE-CONTRACT, CONFIG, CONTRACTS, DESIGN-*, FE-*, FEATURES-V2 | Design/contract docs | KEEP (light "BE-SEC/lens" jargon is harmless; CONTRACTS.md:208 already scrubbed of hostnames in the working tree). |
| docs/PUB-IMG-QA.md / PUB-LICENSE-QA.md | This audit | EXCLUDE from the publish commit (internal audit trail). |

If the opposite "radical transparency" taste wins, shipping SECURITY-REVIEW-V2 +
SECREV-FINDINGS is *defensible* only after all findings are verified fixed and a release
has run — but default recommendation is ship none of them.

---

## Verdict

**License: GO.** 16 Go modules + ~135 npm packages, all permissive (MIT/BSD/ISC/Apache/
MPL-2.0 dual/BlueOak/0BSD). No copyleft conflict with Apache-2.0 outbound. Two cheap
shoulds: real copyright line + optional NOTICE; add `"license"` to web/package.json.

**Leaks: GO-with-mandatory-history-work.** Working tree is clean; everything bad lives in
git metadata/history: (1) real name+gmail author identity [HIGH], (2) will.net hostnames in
old blobs [MED, rewrite already planned — extend to all four hostnames + commit the
working-tree fix first], (3) swarm-flavored commit subjects [LOW/optics], plus one pre-
commit hygiene item: gitignore `deploy/secret.yaml` before any `git add -A`. No live
secrets anywhere in tree or history.
