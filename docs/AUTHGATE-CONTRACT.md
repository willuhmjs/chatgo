# ChatGo — Auth-gate + Onboarding contract (dispatch: AG-1 backend / AG-2 frontend)

Goal: no unauthenticated access to the app (API **and** SPA); first-run onboarding wizard.
Read docs/CONTRACTS.md + docs/DESIGN-POLISH.md first. Backend + DB already enforce 401 on
/api/*; THIS closes the gaps: SPA served anonymously + no first-run/onboarding flow.

## Behaviors (both agents implement the SAME spec)
1. **SPA guard (backend)**: GET of any non-API route (the embedded SPA shell) with no valid
   session → 302 `/login` (except assets/, healthz, share). Session check = same logic as
   Require middleware. FE client-side routing stays as-is (bootstrap redirects, §FE).
2. **First-run bootstrap (backend)**: setting key `first_run_done` (string "true"). When unset:
   - POST /api/auth/register is allowed EXACTLY ONE admin-bootstrap regardless of REGISTRATION
     policy inside the existing atomic first-user tx; sets first_run_done=true. Race: unique
     users-count tx already exists (auth.go) — extend, don't duplicate.
   - GET /api/auth/status → {authenticated:bool, first_run:bool} (public).
3. **Onboarding state (backend, zero new endpoints)**: me-settings key `setup_done`.
   Admin-only gate: if `instance_setup_done` setting unset AND caller is admin →
   /settings response may include it; FE routes admin to wizard when
   isAdmin && me.settings.setup_done !== "true" && auth.status.first_run === false.
   (Regular approved users: no wizard — they land straight in chat; pending users keep the
   existing pending-banner UX.)
4. **Wizard steps (FE, /onboarding route, 3 screens, skippable for admin except step order)**:
   a) "Connect a provider" — same fields as admin provider modal + Test → on success
      POST /api/providers + POST /api/providers/{id}/sync, toast model count.
   b) "Pick a task model" — select from synced models (used for titles/summaries; saved via
      PUT /api/settings {task_model} — admin allowed key per existing allowlist).
   c) "Invite your humans" — show SSO guidance if OIDC enabled (Authelia client + approval
      queue explainer w/ live pending list from GET /api/users) or one-time `ck_` token hint
      for automation; buttons: [Open admin console] [Start chatting] → PUT /api/me/settings
      {setup_done:"true"} + set setting instance_setup_done=true (admin PUT /api/settings) → navigate /.
   Design: ChatGPT-fidelity per DESIGN-POLISH — centered 48rem card on #212121, steps as
   quiet 1·2·3 dots header, white primary button, skip = ghost text. Dark only.
5. **Login page first-run mode**: when /api/auth/status.first_run → the card becomes
   "Welcome to ChatGo — create your admin account" (name+email+password, no SSO button, note
   "sign-in will use {OIDC name} after setup" when OIDC configured).

## Ownership (hard walls)
- **AG-1 (backend)**: internal/api/server.go (SPA shell guard only — touch nothing else there),
  internal/auth/handlers.go + first-run tx (auth.go/oidc flow ONLY as strictly needed),
  + their tests. Also: make GET /api/auth/config include first_run (keep existing fields).
- **AG-2 (frontend)**: web/src/routes/Onboarding.svelte (NEW), web/src/routes/FirstRun.svelte
  (NEW standalone first-run admin-creation page on its own /welcome route — session bootstrap
  in lib/session.svelte.js sends unauthenticated users to /welcome when status.first_run,
  else /login), web/src/lib/session.svelte.js (ready state + login-required flow + those
  redirects), src/lib/router.svelte.js ONLY if it lacks /onboarding + /welcome patterns.
  **DO NOT touch Login.svelte** (POLISH-3 restyles it concurrently) or api.ts/api-shell.ts —
  put new calls in lib/onboarding-api.ts.
  **APP.SVELTE IS TAKEN** (POLISH-1 runs concurrently): DO NOT edit App.svelte. Instead export
  everything needed (route component + a `requireAuth()` guard helper) and write exact
  2-4 line integration instructions in your report; the integrator applies them.
- NOBODY: go.mod, package.json, CONTRACTS.md, other agents' files. POLISH agents are
  concurrently restyling lib/ui + chat files — do NOT edit those; local styled markup only.

## Gates (each agent)
gofmt clean + go build/test (AG-1 scoped: ./internal/api/... ./internal/auth/...), no new
svelte-check errors (AG-2). No git, no npm install. Report deviations + anything the other
side must know for integration.
