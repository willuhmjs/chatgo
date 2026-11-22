# Wave-2 FE plan (fire as soon as FE-COLOR lands; BE wave-1 shapes frozen by then)

## Lane split (disjoint; all after FE-COLOR completes)

- **FE-CHATTER** — owns: routes/ChatView.svelte, routes/Home.svelte, lib/chat/** (incl.
  Composer, MessageRow, MessageList, ModelPicker, ParamDrawer), lib/kb-api.ts+prompts-api
  (new). Builds: 📚 kb collection toggle+multi-select popover in composer (both pill
  variants) + kb:// source chip rendering + citations preview, 👍/👎 rating buttons +
  comment popover on assistant messages (hydrate via GET ratings), "Fork from here" in
  message menu, `/` slash-command popover pulling GET /api/prompts (insert content,
  {{Cursor}}-less v1: plain insert), memory count chip in ParamDrawer (read-only list).
- **FE-PAGES** — owns: NEW routes/Knowledge.svelte (collection CRUD, file upload w/
  progress + chunk counts, test-search box), NEW routes/Images.svelte (generate box →
  grid → feeds existing Gallery pattern), Settings.svelte library tab (prompts CRUD
  table, own + admin-curated), lib/kb-page-api.ts etc (new). Router+App route branches for
  /knowledge + /images = INTEGRATOR 4-liners (same pattern as welcome/onboarding).
- **FE-SEC-FE** — owns: routes/SettingsMe.svelte ONLY: 2FA section (setup shows secret +
  otpauth_uri copyable, code input → enable, disable w/ password), memories manager
  section (list/add/edit/enable-toggle/delete + "Extract from chat" picker calling
  /api/me/memories/extract), accent row untouched (FE-COLOR's).
- **INTEGRATOR (me)** — apimgr wiring lines (kb/library/audit/totp/imggen/memory
  Register + audit.Record call-sites), router/App route additions, memory Enrich binding,
  smoke_v2.sh full run, gates, commit, restart preview.

## Post-wave-2
docs agent (README/SECURITY v2 accurate to shipped code) → RAM re-bench (idle + RAG
upload/search load) → publish steps (author rewrite, GitHub push, Authelia OIDC client).
