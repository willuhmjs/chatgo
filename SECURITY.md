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
- **Sessions** — opaque 32-byte random tokens, stored server-side in
  SQLite (hashed), delivered as a `chatgo_session` cookie that is
  `HttpOnly`, `SameSite=Lax`, and `Secure` whenever `PUBLIC_URL` is
  https. Session tokens are HMAC-signed with `SESSION_SECRET`.
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

- Run behind TLS (k8s manifests use Traefik + https-redirect middleware).
- Set a unique `SESSION_SECRET` (`openssl rand -hex 32`) per deployment.
- Prefer `REGISTRATION=approve` or `closed` unless you want the world in.
- Set `PUBLIC_URL` so cookies get the `Secure` flag.
- Leave `TRUSTED_URL_NETS` empty unless you truly host providers in RFC1918.
- Respect the k8s memory limit (128 Mi) — it is also a DoS blast-radius cap.
