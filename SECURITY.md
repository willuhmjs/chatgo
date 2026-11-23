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
  (validated against a short-lived cookie / ID-token claim), plus PKCE. No
  implicit flow, no token in URL fragments. Discovery fetches the issuer
  document lazily over TLS **when your `OIDC_ISSUER` is https** — https is
  strongly recommended; plain-`http` issuers are accepted so homelab IdPs on
  trusted networks can run without certs (you then trust the network path).
- **TOTP 2FA** — optional per user (RFC 6238, ±1-step window, constant-time
  compares, one-use enforcement); secrets encrypted at rest with the same
  AES-GCM key as provider keys. When enabled it is required on password
  *and* OIDC logins before any session is issued.
- **Provider API keys at rest** — encrypted with AES-256-GCM using a key
  derived (SHA-256) from `SESSION_SECRET`; ciphertext column only; keys
  are never echoed back through the admin API after creation.
- **SSRF guard** — every outbound fetch to user/admin-configurable URLs
  (providers, SearXNG) goes through `internal/search/guard.go`: http(s)
  only, DNS is resolved *first* and we dial only the resolved IPs that
  fall in public ranges ∪ `TRUSTED_URL_NETS` (∪ loopback when
  `ALLOW_LOOPBACK`), which pins the dial target and rejects DNS-rebinding
  style responses. Cloud-metadata and CGNAT ranges are denied *always*,
  even when loopback/trusted nets are on. Redirects are re-validated,
  capped at 3 hops.
- **CSP** — strict Content-Security-Policy on all responses; the SPA does
  not require `unsafe-eval`.
- **SPA shell guard** — the app shell is server-side gated: unauthenticated
  page loads redirect to `/login`; static assets and `/s/<token>` share pages
  are intentionally public.
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

## Scope notes / known limitations

These are design boundaries, not bugs — weigh them before deploying:

- **OIDC account linking is email-keyed.** An SSO login is matched to an
  existing local account by verified email, so it trusts the IdP's email
  hygiene: if your IdP lets users self-register or self-verify addresses,
  whoever controls that mailbox controls the account. Binding by stable
  `sub` instead of email is planned; until then run a trusted IdP (Authelia
  with an admin-managed user db) or `REGISTRATION=closed`.
- **No password-reset flow, by design.** For local accounts there is no
  self-service reset — identity lifecycle belongs to your SSO. The admin
  escape hatch is **Reset 2FA** (`POST /api/users/{id}/reset-2fa`, admin
  users UI): it clears the user's authenticator, revokes all of that user's
  sessions, and writes an audit entry; local accounts additionally get a
  **one-time password returned once in the admin's response** (never
  stored or logged) which the user then replaces in Settings, while
  OIDC-only accounts simply get their authenticator cleared. With SSO-only
  and no 2FA, there is nothing to reset.
- **2FA recovery is admin-mediated; no backup codes.** There are no
  self-service recovery codes — lose your authenticator and an admin must
  run Reset 2FA (above). Disabling 2FA from inside your own session
  requires your password. TOTP verification attempts are rate-limited and
  repeated failures invalidate outstanding challenges, so brute-forcing a
  6-digit code is impractical — but so is guessing your way back in.
- **Admin-configured providers/search are fully-trusted egress targets.**
  Whatever base URL an admin configures sees your prompts (and any file
  parts you send) in the clear from the app's point of view — the SSRF
  guard pins the dial to validated IPs and always denies
  metadata/CGNAT ranges, but it cannot evaluate what a *legitimate*
  upstream does with your traffic (loopback is allowed by default locally;
  the `deploy/` k8s manifests set `ALLOW_LOOPBACK=false`). Anyone who can
  edit providers picks your egress; treat the admin role as root.
- **No per-user model ACL or budget.** Once a user is approved they can use
  *every* enabled provider's API key, at any rate the login/API limiters
  allow — there is no per-user quota, spend cap, or model allowlist.
  Provider keys stay hidden, but budget/ability is all-or-nothing per
  instance. Run separate instances if you need tiers.
- **Share links are bearer-equivalent.** `/s/<token>` carries no session
  and no expiry by default — anyone with the URL can read the chat. Tokens
  are redacted from server logs, exports (md/json/zip) do not embed the
  token, and shared views **strip file attachments** (images/files are not
  served over the public share endpoints), but text + images already
  inlined in messages are visible. Forking a shared chat produces an
  unshared copy, but the **original** chat's link stays live until you
  delete it. Revoke by deleting the link.
- **Knowledge-base content is untrusted input.** Uploaded documents are
  chunked and injected into chats as fenced, `[n]`-marked context; the
  model is told to treat them as data, but a crafted document can still
  steer the answer it gets you (a persuasion attack, not an injection into
  other users' chats or the app — collections are private per user and the
  enricher re-checks ownership on every lookup).
- **Memories ride every prompt.** Memory entries — hand-written *or
  model-extracted from a chat* — are injected into that user's future
  conversations, so a poisoned conversation can fabricate "about the
  user" facts. Mitigations: extracted memories are stored **disabled** and
  only reach the wire after you approve them in Settings, and entries are
  labelled with their provenance (extracted vs user-declared). Review
  extracted items before enabling them; delete anything you didn't say.
- **The audit log is append-only but not tamper-evident.** Admins cannot
  edit or delete rows through the API, but nothing external seals it: a
  determined admin can flood logins to force the 10k-row trim and evict
  history, and the DB itself is only as trusted as the host. The log is
  **global across admins** (every admin sees every admin's entries) with a
  sequential integer cursor. Entries carry emails, IPs, and free-text
  detail — i.e. PII — so don't mirror it somewhere weaker than the app DB.
- Single-node SQLite deployment: no horizontal-scaling session store; a
  compromised `SESSION_SECRET` equals full compromise (sessions *and*
  stored provider keys). Treat it like a TLS private key.
- Uploaded PDFs are stored, not parsed; no AV scanning. Serve uploads from
  an auth-gated path only (the built-in behavior).
- `TASK_MODEL` / title generation sends message excerpts to the configured
  provider — that provider is fully trusted by design (see the egress note
  above).
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
