# Integration TODO — applied by integrator after all agents land (do not delegate blindly)

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
