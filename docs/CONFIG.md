# ChatGo — Configuration

All runtime config is env-driven (loaded in `internal/config`). The README
table lives with the OPS agent; this is the authoritative scaffold copy.

| Env var | Default | Meaning |
| --- | --- | --- |
| `PORT` | `8080` | HTTP listen port |
| `HOST` | `0.0.0.0` | HTTP bind host |
| `PUBLIC_URL` | *(none)* | Externally reachable URL; enables Secure cookies + OIDC redirect base |
| `DATA_DIR` | `./data` | Database + files root |
| `DATABASE_PATH` | `$DATA_DIR/chatgo.db` | SQLite path (`:memory:` supported for tests) |
| `SESSION_SECRET` | **required** | Hex secret (≥64 hex chars = 32 bytes; boot fails below that). Session signing + AES-256-GCM key for provider API keys (key = SHA-256(secret)). `openssl rand -hex 32` |
| `OIDC_ISSUER` | *(none)* | OIDC discovery URL; unset = SSO disabled |
| `OIDC_CLIENT_ID` | *(none)* | OIDC client id |
| `OIDC_CLIENT_SECRET` | *(none)* | OIDC client secret |
| `OIDC_DISPLAY_NAME` | `SSO` | Login-button label |
| `REGISTRATION` | `approve` | `open` \| `approve` \| `closed` |
| `ADMIN_EMAIL` | *(none)* | Accounts with this email are created/promoted as admin (deterministic bootstrap; first-signup otherwise) |
| `SECURE_COOKIE` | *(auto)* | `true`/`false` forces session cookie `Secure`; unset = auto (`PUBLIC_URL` https or trusted `X-Forwarded-Proto: https`) |
| `TRUSTED_PROXIES` | *(none)* | Comma-separated CIDRs whose `X-Forwarded-For`/`-Proto` are believed (client IP for rate limits/logs). Only set behind a proxy |
| `TASK_MODEL` | *(none)* | `providerID:modelID` for chat titles/summaries |
| `SEARXNG_URL` | *(none)* | SearXNG base URL; unset = web-search toggle hidden |
| `SEARCH_MAX_RESULTS` | `5` | Max SearXNG hits fed into a prompt per turn (clamped to 20) |
| `TRUSTED_URL_NETS` | *(none)* | Comma-separated CIDRs allowed for provider/SearXNG URLs beyond public internet |
| `ALLOW_LOOPBACK` | `true` | Allow loopback for in-cluster SearXNG/providers (`deploy/` manifests ship `false`) |
| `VERSION` | `dev` | Reported by `/healthz` (overridden by `-ldflags -X main.version=`) |
