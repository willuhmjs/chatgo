# ChatGo

A self-hosted, multi-user AI chat webapp in a single Go binary — looks and
feels like ChatGPT / Open WebUI, weighs under 128 MiB of RAM.

> **Not affiliated with Open WebUI.** ChatGo is an original implementation
> (Go + Svelte 5), not a fork; it shares the general "self-hosted chat UI"
> idea only. Licensed under Apache-2.0 (see [LICENSE](LICENSE)).

<!-- TODO(screenshot): capture once UI is final; files live in docs/
     (docs/screenshot-chat.png / screenshot-admin.png / screenshot-palette.png — not committed yet) -->

## Features

- **Sign-in required** — the SPA shell is guarded server-side: anonymous page
  loads 302 to `/login` (static assets and `/s/<token>` share pages stay
  public by design). `GET /api/auth/status` → `{authenticated, first_run}`
  drives the client-side gate.
- **Guided first run** — `/welcome` creates the instance admin under *any*
  `REGISTRATION` policy (a fresh deploy is never bricked by its default), then
  the `/onboarding` admin wizard walks provider → task model → invites /
  approvals.
- **Multi-user with roles** — `admin` / `user` / `pending`, admin user
  management, approval flow for new signups.
- **OIDC SSO** — standard code flow (+nonce); works out of the box with
  Authelia (`OIDC_ISSUER=https://idp.example.com`), Keycloak, Authentik, etc.
  Local email/password auth (bcrypt) alongside or instead.
- **OpenAI-compatible multi-provider** — point it at any number of
  OpenAI-compatible backends; model list sync (`POST /api/providers/{id}/sync`),
  per-chat model & params.
- **SSE streaming** with token-by-token render, sources/citations, usage, and
  regenerate; heartbeats survive proxies.
- **Vision** — attach png/jpg/webp/gif images, sent as data-URL image parts.
- **Web search** — optional [SearXNG](https://docs.searxng.org/) backend;
  per-message toggle injects cited context and shows source chips.
- **Knowledge bases (RAG)** — upload text documents (md/csv/json/code…) into
  per-user collections; chats that select a collection get top-k chunks
  injected as fenced, `[n]`-marked context and the answer carries kb citations
  in the same sources channel as web search (`POST /api/kb/{id}/search`).
- **Memories** — per-user "about the user" facts, hand-written in settings or
  extracted from a chat (`POST /api/me/memories/extract`); enabled memories
  are injected into that user's future prompts. Extracted proposals land
  **disabled** until you approve them.
- **Prompt library + slash commands** — personal and admin-curated prompts,
  inserted from the composer with `/`.
- **Ratings & forks** — 👍/👎 (with optional comment) on any assistant
  message; fork any chat from any message onward.
- **Export** — a chat as `md` or `json` (`GET /api/chats/{id}/export`), or
  your whole account as a streamed `zip` (`GET /api/me/export`: chats +
  redacted personal settings).
- **Full-text chat search, folders, tags & share links** — SQLite FTS5 over
  all your messages; organize by folder/tag, pin chats; read-only public view
  of a chat at `/s/<token>` (file attachments stripped).
- **API tokens + OpenAI-compat proxy** — issue `ck_…` tokens and call
  `POST /v1/chat/completions` against ChatGo itself.
- **Image generation** — `POST /api/images/generate` proxies an
  OpenAI-compatible images endpoint; results land in your file gallery.
- **TOTP 2FA + admin audit log** — optional per-user authenticator
  (`/api/me/2fa/setup`, manual key + `otpauth://` URI); when enabled it is
  required on password *and* SSO logins. Admins get an audit trail
  (`GET /api/audit`) of logins, user/provider/settings/token changes.
- **Colorful accents** — accent-color system (tinted bubbles, active rows,
  chips) with a per-user picker.
- **Command palette** (⌘K), keyboard shortcuts, compare two models
  side-by-side, artifact/image gallery, chat summarizing + context
  compaction for long threads.
- **Small** — single static binary, distroless image, target < 128 Mi RSS.

## Quickstart (Docker)

> Replace `OWNER` below with your GHCR org/username — CI publishes to
> `ghcr.io/<repo-owner>/chatgo` (or build locally: `make docker` → `chatgo:local`).

```sh
docker run -d --name chatgo \
  -p 8080:8080 \
  -v chatgo-data:/data \
  -e SESSION_SECRET="$(openssl rand -hex 32)" \
  -e DATA_DIR=/data \
  ghcr.io/OWNER/chatgo:latest
```

- The `/data` volume holds the SQLite DB and uploads — back it up. Prefer a
  **named volume** (as above): the image ships `/data` owned by uid 65532 and
  Docker initializes fresh named volumes from it. If you bind-mount a host
  directory (`-v ./data:/data`) instead, make it writable by uid 65532 on the
  host first (`chown -R 65532:65532 ./data`).
- The image runs happily on a `--read-only` rootfs — only `/data` needs to be
  writable (the k8s manifests run it that way).
- `SESSION_SECRET` is **required**: hex, **≥64 chars** (32 bytes); the app
  refuses to boot with less. Generate a fresh one with `openssl rand -hex 32`
  and keep it out of git; it keys session signing *and* at-rest encryption of
  provider API keys, so rotating it invalidates sessions **and** makes stored
  provider keys unreadable.
- The first visit redirects to `/login`; on a brand-new instance `/welcome`
  then offers the create-admin form — the first account becomes admin under
  any `REGISTRATION` policy (later signups follow it, landing as `pending`
  under the default `REGISTRATION=approve`), or set `ADMIN_EMAIL` to pin admin
  to a specific account. First sign-in continues into the `/onboarding` admin
  wizard (provider → task model → invites / approvals).

Then open <http://localhost:8080>.

## Configuration

All runtime config is environment variables (see also the scaffold
copy at [docs/CONFIG.md](docs/CONFIG.md)):

| Env var | Default | Meaning |
| --- | --- | --- |
| `PORT` | `8080` | HTTP listen port |
| `HOST` | `0.0.0.0` | HTTP bind host |
| `PUBLIC_URL` | *(none)* | Externally reachable URL; enables Secure cookies + OIDC redirect base |
| `DATA_DIR` | `./data` | Database + files root |
| `DATABASE_PATH` | `$DATA_DIR/chatgo.db` | SQLite path (`:memory:` supported for tests) |
| `SESSION_SECRET` | **required** | Hex secret (**≥64** hex chars = 32 bytes). Session signing + AES-256-GCM key for provider API keys. `openssl rand -hex 32` |
| `OIDC_ISSUER` | *(none)* | OIDC discovery URL; unset = SSO disabled |
| `OIDC_CLIENT_ID` | *(none)* | OIDC client id |
| `OIDC_CLIENT_SECRET` | *(none)* | OIDC client secret |
| `OIDC_DISPLAY_NAME` | `SSO` | Login-button label |
| `REGISTRATION` | `approve` | `open` \| `approve` \| `closed` |
| `ADMIN_EMAIL` | *(none)* | Accounts with this email are created/promoted as **admin** (deterministic bootstrap instead of first-signup-wins) |
| `SECURE_COOKIE` | *(auto)* | `true`/`false` forces the session cookie `Secure` flag; unset = auto (Secure when `PUBLIC_URL` is https or the trusted proxy sends `X-Forwarded-Proto: https`) |
| `TRUSTED_PROXIES` | *(none)* | Comma-separated CIDRs of reverse proxies whose `X-Forwarded-For`/`-Proto` may be believed (client IP for rate limits/logs). Set only when behind a proxy, e.g. `127.0.0.1/32` (host-network Traefik) or `10.42.0.0/16` (k3s pod CIDR) |
| `TASK_MODEL` | *(none)* | `providerID:modelID` for chat titles/summaries |
| `SEARXNG_URL` | *(none)* | SearXNG base URL; unset = web-search toggle hidden |
| `TRUSTED_URL_NETS` | *(none)* | Comma-separated CIDRs allowed for provider/SearXNG URLs beyond the public internet |
| `ALLOW_LOOPBACK` | `true` | Allow loopback for in-cluster SearXNG/providers (the k8s manifests in `deploy/` set `false`) |
| `VERSION` | `dev` | Reported by `/healthz` (overridden at build via `-ldflags -X main.version=`) |

## Building from source

Prereqs: Go ≥ 1.27, Node ≥ 22 (lockfile is npm).

```sh
make build   # web: npm ci + vite → internal/web/dist → go build → bin/chatgo
make run     # local dev server, DATA_DIR=./data
make dev-web # vite dev server with /api proxy (run alongside `make run`)
make test    # go test ./... + svelte-check
make lint    # gofmt + go vet + svelte-check
make docker  # build distroless image (local tag: chatgo:local)
make bench   # RSS benchmark against a dummy provider (see bench/)
```

`VERSION` is stamped from `git describe --tags --always --dirty`
(falls back to `dev`).

## Kubernetes

Ready-made manifests for our homelab (namespace `chatgo`, Traefik ingress,
cert-manager, Authelia OIDC) live in [`deploy/`](deploy/) — each YAML is
commented inline. Secrets are templated in
`deploy/secret.example.yaml` and are intentionally not in the kustomization.

## Memory

Target: **< 128 MiB RSS** in steady state (measured via `make bench`,
which runs the binary against a canned SSE mock provider).

| Scenario | Go version | Platform | Idle RSS | Peak (1 streaming chat) |
| --- | --- | --- | --- | --- |
| Container (this repo's Dockerfile, distroless, `--read-only`) | 1.27.4 | linux/docker | **8.7 MiB** | — |
| Host binary, v2 feature set (`make bench`) | 1.27.4 | darwin/arm64 | 18 MiB | 20 MiB |

Container idle = boot + one API request, cgroup RSS. Host row measured at the
v2 build; the release-tagged binary is re-benched each release.

Peak load is bounded too (macOS harness numbers, see caveat below): knowledge
search streams vectors and only fetches the matched text — scanning a 20k-chunk
collection peaks at ~33 MiB, four concurrent searches ~63 MiB. Worst-case 5 MiB
document ingest streams the chunker and embeds per-batch (~47 MiB peak), and
image generation caps upstream responses at 12 MiB (~57 MiB for a legitimate
4-image run; oversized replies are refused before the full download). Every
scenario fits the 128 MiB budget with headroom; container deployments should
set `GOMEMLIMIT=100MiB` alongside the limit.

Linux `VmRSS` is the number that matters for k8s limits; macOS
measurements run ~1.5–2× higher due to allocator differences — see the
caveat printed by `bench/measure.sh`.

## Security

**Please read [SECURITY.md](SECURITY.md)** before filing a public issue for
anything that looks like a vulnerability — it describes the private
disclosure channel and the mitigations already built in (bcrypt, hashed
session tokens, TOTP 2FA, SSRF dial-guard, CSP, AES-GCM key storage, audit
log, …).

## License

Apache License 2.0 — see [LICENSE](LICENSE). ChatGo is an independent,
original implementation and is not affiliated with, endorsed by, or derived
from Open WebUI or OpenAI.
