# Integration TODO — applied by integrator after all agents land (do not delegate blindly)

## Wave-2 integration wiring (handed over by feature agents)

- BE-RAG: `kb.Register(mux, &kb.Deps{Cfg: d.Cfg, Store: d.Store, Auth: d.Auth, Chat: d.Chat})`
  + import; run AFTER apimgr.Register; Register self-inits DDL + registers kb enricher.
  REAL Enricher sig: `func(ctx, chat *store.Chat, userMsg providers.ChatMessage) ([]chat.SourceItem, string, error)`
  via `(*chat.Service).RegisterEnricher(name, fn)`. ⚠️ BE-MEMORY built against the
  contract's draft sig — integrator writes ~8-line adapter binding memory.Enrich →
  RegisterEnricher("memory", …) using chat.UserID. Escape hatch setting: kb_embed_model.
- BE-MEMORY done: `memory.Register(mux, &memory.Deps{Cfg,Store,Auth,Providers})` + exact
  enrich binding provided (chatSvc.RegisterEnricher("memory", …memory.Enrich(ctx,st,
  c.UserID, um.Content)→nil,extra,err)). Extract dedupes vs existing; ignore userMsg v1.
- BE-SOCIAL: `library.Register(mux, &library.Deps{Store: st, Auth: a})` returns error —
  wire where feature registers live (apimgr); self-inits DDL; no AuthPolicy change.
  (Deps may need their withUser fallback review: they mirrored chatJSON DTOs — fine.)
- FE lanes all DONE (CHATTER/PAGES/SEC-FE/AUDIT/COLOR). kb_collection_ids persists under
  chat.params → PATCH /api/chats/{id} — matches BE-RAG's params_json trigger ✓.
  ParamDrawer now merge-preserves non-sampling keys (verified needed).
- FE-PAGES: Knowledge/Images pages + Settings Library tab + sidebar nav rows built by
  them. INTEGRATOR: add router patterns /knowledge + /images + App branches. Shape audit
  vs BE reports: all match (bytes ✓ DELETE path ✓ field 'file' ✓ {prompts} envelope ✓).
  ONE FLAG: FE sends size as NUMBER (512) — verify BE-SEC imggen accepts; else flip one
  line in pages-api generateImages to `${size}x${size}`.
- FE-SEC-FE done: SettingsMe 2FA+memory UI. Notes: probe = bodiless setup POST (400→
  hide=OIDC-only); memory UI PATCH→PUT fallback on 405 (BE-MEMORY ships PUT — fine);
  copyText imported from lib/chatview-api (verify survives FE-CHATTER's edits).

## Original queue
1. [x] App.svelte mount DONE by INTEG-B: route imports, bare-list += welcome/onboarding,
       branches added, nudge $effect using api.getMeSettings() with mid-flight re-check +
       catch. svelte-check 0 errors. Spot-check hunks at final review.
2. [x] lib/extra/markdown.css blockquote → §4 recipe (DONE by INTEG-A: 2px border-inline-start,
       dark #4b4b4b/#b3b3b3, normal style, single margin source; matches Markdown.svelte now).
3. [ ] If AG-1 changed /api/auth/config or added /api/auth/status shape deviations →
       reconcile with AG-2's onboarding-api.ts expectations.
4. [ ] Gates: npx svelte-check --threshold error (0) · npm run build · go build ./... ·
       go vet ./... · go test ./... (all green).
5. [ ] Commit polish+authgate round (author will <will@will.local> per publish plan? no —
       local commit as-is; author rewrite is pre-push step only).
6. [ ] Restart :8080 preview from BLANK DATA_DIR (/tmp/chatgo-ui fresh) → user sees
       /welcome first-run → wizard. Ensure mock provider on :9099 up (restart
       bench/mockprovider if the RAM bench killed it).
7. [ ] RAM bench results → report idle/peak/settled vs 128Mi target.
8. [ ] README: document sign-in-required + first-run/onboarding flow (currently says
       open app at /). SECURITY.md: SPA 302 guard strengthens the auth story — one line.
9. [ ] V2 INTEGRATION: wiring lines per ledger above; router+App branches for
       /knowledge + /images; imggen size-format flag; run bench/smoke_v2.sh; full gates;
       commit v2.
10. [ ] SECURITY REVIEW V2: 8-lens swarm per docs/SECURITY-REVIEW-V2.md (user-mandated),
        then fixer swarm + re-verify + SECURITY.md + commit. Then RAM re-bench w/ v2 load.
