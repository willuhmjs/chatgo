# PUB-IMG-QA — container path + README fresh-eyes audit (2026-11-22)

Subagent PUB-1. Build + runtime verification for public release.
Build cmd (matches `ci.yml` docker jobs: context `.`, build-arg VERSION):
`docker build -t chatgo-pub-qa --build-arg VERSION=pub-qa .` → **OK**

## Headline numbers

| Metric | Value | Notes |
| --- | --- | --- |
| Image size (unpacked) | **25.2 MiB** (26.4 MB, `docker images`) | distroless/static:nonroot + 13 MB static binary |
| Image size (compressed layers) | **6.4 MiB** (6,700,281 B) | what GHCR pull transfers |
| Container idle RSS (boot + 1 failed login) | **8.7 MiB** (`docker stats --no-stream`) | well under the 15–20 MiB expectation; k8s limit story safe |
| Binary (linux/amd64-equivalent, `-s -w`) | ~13 MB inside image | darwin bare build: 13.8 MB |

## PASS/FAIL

| # | Check | Result | Evidence |
| --- | --- | --- | --- |
| 1a | Image builds as CI would | **PASS** | exact context `.` + `VERSION` arg; `version=pub-qa` in `/healthz` proves ldflags wiring end-to-end |
| 1b | Nonroot | **PASS** | `Config.User=nonroot:nonroot`; `docker top` → UID **65532**. (`exec id` impossible — distroless has no shell; that's the point) |
| 1c | Boot on :8096 (PORT=8080 per EXPOSE, named volume /data) | **PASS** | `/healthz` 200; `/` → **302 /login**; login HTML 200; `/assets/index-*.js` 200 (507 KB), `/assets/index-*.css` 200 (78 KB) |
| 1d | Idle RSS | **PASS** | 8.7 MiB (< 40 MiB flag line) |
| 1e | `--read-only` rootfs, volume only on `/data` | **PASS** | healthz/302/failed-login 401 (real sqlite write, 446 ms) on `:8093`; **no writable /tmp needed**. Caveat below (doc gap) |
| 1f | CI ↔ Dockerfile ↔ embed coherence | **PASS** | workflow is `ci.yml` (not `build.yml`); it builds web **inside** the image (stage 1), and a tracked placeholder `internal/web/dist/index.html` makes bare `go build`/`go vet`/`go test` work pre-npm. Fresh clone → docker build works. Risk note below |
| 2 | README quickstart literal run (`go build` + run :8093, temp DATA_DIR) | **PASS** | `/healthz` 200, `/` 302 → `/login` 200, sqlite + WAL in DATA_DIR; killed |
| 2b | All 19 README env vars exist in `config.go` | **PASS** | PORT/HOST/PUBLIC_URL/DATA_DIR/DATABASE_PATH/SESSION_SECRET/OIDC_{ISSUER,CLIENT_ID,CLIENT_SECRET,DISPLAY_NAME}/REGISTRATION/ADMIN_EMAIL/SECURE_COOKIE/TRUSTED_PROXIES/TASK_MODEL/SEARXNG_URL/TRUSTED_URL_NETS/ALLOW_LOOPBACK/VERSION — all in `internal/config/config.go`; defaults match (8080, 0.0.0.0, ./data, SSO, approve, loopback=true, dev). SESSION_SECRET ≥64-hex rejection verified in code (`config.go:81–91`) |
| 2c | README API claims (`/api/auth/status{authenticated,first_run}`, sync, extract, export, kb/search, images/generate, 2fa/setup, audit) | **PASS** | all routes exist; OIDC is code flow + nonce (+PKCE, README underclaims — fine) |
| 3 | docker-compose example | **N/A** | none in README/repo; `docker run` block verified as literal (adapted to :8096) |

## Doc-lie / doc-gap list (file:line)

1. **README.md:150** — "see the notes in `deploy/README` comments": **`deploy/README` does not exist** (notes live inside the YAML comments). Stranger hits this wall.
2. **README.md:72,139 vs Makefile:6** — README says `make docker` → tag `chatgo:local`; Makefile default is `IMAGE ?= ghcr.io/OWNER/chatgo:local`. Literal command yields a different tag than documented.
3. **README.md:158–160** — Memory table row is an unfilled placeholder: `[RAM v2 — integrator fills]`. Publishes an empty table. Suggested fill from this audit: idle container RSS 8.7 MiB (linux/arm64 container), image 25.2 MiB.
4. **README.md:131** — "Go ≥ 1.27": go.mod says `go 1.27.4`; Go 1.27.0–1.27.3 auto-resolve the toolchain (GOTOOLCHAIN=auto), so it works, but "≥ 1.27.4" would be honest. Minor.
5. **.gitignore:5 vs git** — `internal/web/dist/` is gitignored, yet `internal/web/dist/index.html` is a **tracked** go:embed placeholder. Works for `git clone` (tracked beats ignore), but **GitHub source-archive tarballs and `git archive` exclude ignored paths** → `go build ./...` from a tarball fails (`embed: pattern all:dist: no matching files`). Either un-ignore `!internal/web/dist/index.html` or accept "git clone only".
6. **Dockerfile:9** — comment claims node:22 "matches … package.json engines": `web/package.json` has **no `engines` field**. Stale comment.
7. **README Docker quickstart — doc gap (not a lie):** nothing tells users the app runs fine with `--read-only` (verified here), even though `deploy/deployment.yaml:82` already sets `readOnlyRootFilesystem: true`. One line in Quickstart (`--read-only`) would make the docker path match the k8s hardening story.
8. Task brief referenced `.github/workflows/build.yml`; the actual (and only) workflow is `.github/workflows/ci.yml`.

## Deviations from brief

- Ran `go build` + host binary on :8093 (not `make build` — that triggers `npm ci`/vite which would overwrite tracked `internal/web/dist`; npm prohibited + report-only). Placeholder embed is exactly what a fresh clone ships, so the test is faithful.
- `exec id` replaced with `docker top` UID check (distroless has no `/usr/bin/id`).
- RSS sampled once via `docker stats` (cgroup memory incl. slab, post-login-attempt); not a `make bench` VmRSS measurement — bench ports were off-limits.

## Cleanup

`chatgo-pubqa`, `chatgo-pubqa-ro` containers, `chatgo-pubqa-data`, `chatgo-pubqa-ro-data` volumes, `chatgo-pubqa` image, host binary on :8093 — all removed. No repo files modified (this doc is the only addition).
