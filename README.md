# ChatGo

A self-hosted, multi-user AI chat webapp in a single Go binary — looks and
feels like ChatGPT / Open WebUI, weighs under 128 MiB of RAM.

> **Not affiliated with Open WebUI.** ChatGo is an original implementation
> (Go + Svelte 5), not a fork; it shares the general "self-hosted chat UI"
> idea only. Licensed under Apache-2.0 (see [LICENSE](LICENSE)).

<!-- TODO(screenshot): capture once UI is final; files live in docs/
     (docs/screenshot-chat.png / screenshot-admin.png / screenshot-palette.png — not committed yet) -->

## Features

- **Multi-user with roles** — `admin` / `user` / `pending`, admin user
  management, approval flow for new signups.
- **OIDC SSO** — standard code flow (+nonce); works out of the box with
  Authelia (`OIDC_ISSUER=https://sso.will.net`), Keycloak, Authentik, etc.
  Local email/password auth (bcrypt) alongside or instead.
- **OpenAI-compatible multi-provider** — point it at any number of
  OpenAI-compatible backends; model list sync, per-chat model & params.
- **SSE streaming** with token-by-token render, sources, usage, and
  regenerate; heartbeats survive proxies.
- **Vision** — attach png/jpg/webp/gif images, sent as data-URL image parts.
- **Web search** — optional [SearXNG](https://docs.searxng.org/) backend;
  per-message toggle injects cited context and shows source chips.
- **Full-text chat search** — SQLite FTS5 over all your messages.
- **Folders & tags** for chat organization, plus pinning.
- **Share links** — read-only public view of a chat at `/s/<token>`.
- **API tokens + OpenAI-compat proxy** — issue `ck_…` tokens and call
  `POST /v1/chat/completions` against ChatGo itself.
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
- `SESSION_SECRET` is **required**: hex, **≥64 chars** (32 bytes); the app
  refuses to boot with less. Generate a fresh one with `openssl rand -hex 32`
  and keep it out of git; it keys session signing *and* at-rest encryption of
  provider API keys, so rotating it invalidates sessions **and** makes stored
  provider keys unreadable.
- First registered user should be given the admin role (subsequent
  signups land as `pending` under the default `REGISTRATION=approve`), or set
  `ADMIN_EMAIL` to pin admin to a specific account.

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
cert-manager, Authelia OIDC) live in [`deploy/`](deploy/) — see the notes
in `deploy/README` comments. Secrets are templated in
`deploy/secret.example.yaml` and are intentionally not in the kustomization.

## Memory

Target: **< 128 MiB RSS** in steady state (measured via `make bench`,
which runs the binary against a canned SSE mock provider).

| Scenario | Go version | Platform | Idle RSS | Peak (1 streaming chat) |
| --- | --- | --- | --- | --- |
| *placeholder — run `make bench` on the server node* | | linux/arm64 | | |

Linux `VmRSS` is the number that matters for k8s limits; macOS
measurements run ~1.5–2× higher due to allocator differences — see the
caveat printed by `bench/measure.sh`.

## Security

**Please read [SECURITY.md](SECURITY.md)** before filing a public issue for
anything that looks like a vulnerability — it describes the private
disclosure channel and the mitigations already built in (bcrypt, HMAC
session cookies, SSRF dial-guard, CSP, AES-GCM key storage, …).

## License

Apache License 2.0 — see [LICENSE](LICENSE). ChatGo is an independent,
original implementation and is not affiliated with, endorsed by, or derived
from Open WebUI or OpenAI.
