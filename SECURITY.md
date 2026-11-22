# Security Policy

## Reporting a vulnerability

**Do not open a public issue for security problems.** Use GitHub's
private disclosure channel:

1. Go to **Security → Advisories** on this repository and click
   *Report a vulnerability*
   (<https://github.com/OWNER/chatgo/security/advisories/new>), or email
   the maintainers listed there if you cannot use GitHub.

We aim to acknowledge within 7 days and will coordinate a fix and
disclosure timeline with you. This is a small self-hosted hobby project —
please be patient, but do not post details publicly until a fix ships.

## Built-in mitigations

These are implemented deliberately; a finding that defeats one of them is
a vulnerability. This list also serves as the roadmap for an independent
security review — if something below is wrong or incomplete in the code,
that *is* a bug, report it privately.

- **Password storage** — bcrypt, cost 12. Password hashes are never
  returned by any API or written to logs.
- **Sessions** — 32-byte `crypto/rand` opaque tokens delivered as a
  `chatgo_session` cookie that is `HttpOnly`, `SameSite=Lax`, and `Secure`
  whenever the app is served over https (auto via `PUBLIC_URL`, or forced
  with `SECURE_COOKIE`). The server stores only the **SHA-256 hash** of the
  token in SQLite: a stolen database does not yield usable sessions. (The
  short-lived OIDC state cookie *is* HMAC-signed with `SESSION_SECRET`;
  session tokens themselves are plain random values, hashed at rest.)
- **OIDC** — full Authorization Code flow with `state` **and** `nonce`
  (validated against a short-lived cookie / ID-token claim), discovery via
  TLS. No implicit flow, no token in URL fragments.
- **Provider API keys at rest** — encrypted with AES-256-GCM using a key
  derived (SHA-256) from `SESSION_SECRET`; ciphertext column only; keys
  are never echoed back through the admin API after creation.
- **SSRF guard** — every outbound fetch to user/admin-configurable URLs
  (providers, SearXNG) goes through `internal/search/guard.go`: http(s)
  only, DNS is resolved *first* and we dial only the resolved IPs that
  fall in public ranges ∪ `TRUSTED_URL_NETS` (∪ loopback when
  `ALLOW_LOOPBACK`), which pins the dial target and rejects DNS-rebinding
  style responses. Redirects are re-validated, capped at 3 hops.
- **CSP** — strict Content-Security-Policy on all responses; the SPA does
  not require `unsafe-eval`.
- **XSS** — all markdown is rendered through DOMPurify before touching
  the DOM; HTML from models is never trusted.
- **Body caps** — every request body is size-limited (1 MiB JSON, 20 MiB
  multipart uploads) before parsing.
- **Login brute force** — local login is rate-limited to 5 attempts /
  minute / IP (`golang.org/x/time/rate`).
- **SQL injection** — every query is parameterized (no string-built SQL,
  enforced by convention + review); FTS5 user queries run through an
  escaping layer before hitting the `messages_fts` MATCH index.
- **IDOR policy** — resources not owned by the caller return **404**, not
  403, so object existence is never leaked across users; admin-only routes
  return 403 for non-admins.

## Scope notes / known limitations (v1)

These are design boundaries, not bugs — weigh them before deploying:

- **OIDC account linking is email-keyed.** An SSO login is matched to an
  existing local account by verified email, so it trusts the IdP's email
  hygiene: if your IdP lets users self-register or self-verify addresses,
  whoever controls that mailbox controls the account. Binding by stable
  `sub` instead of email is planned; until then run a trusted IdP (Authelia
  with an admin-managed user db) or `REGISTRATION=closed`.
- **No password reset, by design.** For local accounts there is no reset
  flow — identity lifecycle belongs to your SSO. An admin can reset a
  local password from the admin UI; with SSO-only, nothing to reset.
- **No per-user model ACL.** Once a user is approved they can use *every*
  enabled provider/model. Provider API keys stay hidden, but budget/ability
  is all-or-nothing per instance. Run separate instances if you need tiers.
- **Share links are bearer-equivalent.** `/s/<token>` carries no session
  and no expiry by default — anyone with the URL can read the chat. Tokens
  are redacted from server logs, and shared views **strip file
  attachments** (images/files are not served over the public share
  endpoints), but text + images already inlined in messages are visible.
  Revoke by deleting the link.
- Single-node SQLite deployment: no horizontal-scaling session store; a
  compromised `SESSION_SECRET` equals full compromise (sessions *and*
  stored provider keys). Treat it like a TLS private key.
- Uploaded PDFs are stored, not parsed; no AV scanning. Serve uploads from
  an auth-gated path only (the built-in behavior).
- `TASK_MODEL` / title generation sends message excerpts to the configured
  provider — that provider is fully trusted by design.
- Timing attacks on API-token lookup are considered low-risk (sha256
  indexed lookup) but are not constant-time.

## Hardening checklist for operators

- Run behind TLS (k8s manifests use Traefik + https-redirect + HSTS
  middlewares, plus edge rate limits on `/api/auth` and share endpoints —
  see `deploy/middleware.yaml`).
- Set a unique `SESSION_SECRET` (`openssl rand -hex 32`) per deployment.
- If behind a reverse proxy, set `TRUSTED_PROXIES` to *only* the proxy's
  CIDR — otherwise login rate limits and client-IP logging can be spoofed
  via `X-Forwarded-For`.
- Prefer `REGISTRATION=approve` or `closed` unless you want the world in;
  prefer SSO over local passwords.
- Set `PUBLIC_URL` so cookies get the `Secure` flag.
- Leave `TRUSTED_URL_NETS` empty unless you truly host providers in RFC1918;
  set `ALLOW_LOOPBACK=false` unless you run SearXNG on the same host
  (the k8s manifests ship it `false`).
- Respect the k8s memory limit (128 Mi) and `GOMEMLIMIT=100MiB` — they are
  also a DoS blast-radius cap.
