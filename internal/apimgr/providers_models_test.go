package apimgr

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"chatgo/internal/providers"
	"chatgo/internal/store"
)

// Admin model-enablement surface: PUT /api/providers/{id}/models (bulk),
// GET /api/providers/{id}/models, has_key/counts on the provider view, and
// sync semantics (preserves admin choices, counts only new models).

// cachedModelRows is the fixed cache fixture: a=on, b=on, c=off.
var cachedModelRows = []store.ModelRow{
	{ModelID: "a", Display: "Model A", ContextLen: 128000, Enabled: true},
	{ModelID: "b", Display: "Model B", ContextLen: 8192, Enabled: true},
	{ModelID: "c", Display: "Model C", Enabled: false},
}

func putReq(body string) *http.Request {
	return httptest.NewRequest("PUT", "/api/providers/x/models", strings.NewReader(body))
}

func decodeRows(t *testing.T, rec *httptest.ResponseRecorder) []modelRowJSON {
	t.Helper()
	var rows []modelRowJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode rows: %v (body %s)", err, rec.Body)
	}
	return rows
}

func enabledOf(rows []modelRowJSON, id string) (bool, bool) {
	for _, r := range rows {
		if r.ModelID == id {
			return r.Enabled, true
		}
	}
	return false, false
}

func minRoleOf(rows []modelRowJSON, id string) (string, bool) {
	for _, r := range rows {
		if r.ModelID == id {
			return r.MinRole, true
		}
	}
	return "", false
}

func TestProviderModelsListAndBulkToggle(t *testing.T) {
	d, _ := testDeps(t)
	admin := mustUser(t, d.Store, "madm@example.com", store.RoleAdmin)
	p := seedProvider(t, d, "toggleco")
	seedModels(t, d, p, cachedModelRows...)
	h := &providersH{d: d}

	// Admin per-provider list includes the DISABLED row (unlike /api/models).
	rec := httptest.NewRecorder()
	h.providerModels(rec, httptest.NewRequest("GET", "/", nil), p.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, body %s", rec.Code, rec.Body)
	}
	rows := decodeRows(t, rec)
	if len(rows) != 3 {
		t.Fatalf("admin list = %+v, want all 3 cached rows incl. disabled", rows)
	}
	if on, ok := enabledOf(rows, "c"); !ok || on {
		t.Errorf("row c = %v/%v, want present and disabled", on, ok)
	}

	// Bulk: disable a, re-enable c, in ONE call.
	rec = httptest.NewRecorder()
	h.providerModelsUpdate(rec, putReq(`[{"model_id":"a","enabled":false},{"model_id":"c","enabled":true}]`), p.ID, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("toggle status = %d, body %s", rec.Code, rec.Body)
	}
	rows = decodeRows(t, rec) // response is the refreshed full list
	for id, want := range map[string]bool{"a": false, "b": true, "c": true} {
		if on, ok := enabledOf(rows, id); !ok || on != want {
			t.Errorf("response row %s = %v/%v, want %v", id, on, ok, want)
		}
	}
	stored, err := d.Store.Models.ForProvider(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range stored { // display/context survive the toggle
		if m.ModelID == "a" && (m.Display != "Model A" || m.ContextLen != 128000) {
			t.Errorf("toggle clobbered cached fields: %+v", m)
		}
	}

	// Chat surface (GET /api/models) now excludes a, includes c (all rows have
	// min_role='', so a plain user sees every enabled model).
	rec = httptest.NewRecorder()
	h.models(rec, httptest.NewRequest("GET", "/api/models", nil), false)
	var chatModels []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &chatModels); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, m := range chatModels {
		got[m["model_id"].(string)] = true
	}
	if got["a"] || !got["b"] || !got["c"] {
		t.Errorf("GET /api/models after toggle = %v, want b+c only", got)
	}

	// Audit: provider.update carrying the toggle count.
	var detail string
	if err := d.Store.DB.QueryRow(
		`SELECT detail FROM audit_log WHERE action = 'provider.update' ORDER BY id DESC LIMIT 1`).
		Scan(&detail); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail, p.Name) || !strings.Contains(detail, "2") {
		t.Errorf("audit detail = %q, want provider name + count 2", detail)
	}

	// Provider list view: has_key dot + cached-model tallies.
	rec = httptest.NewRecorder()
	h.list(rec, httptest.NewRequest("GET", "/api/providers", nil))
	var views []providerJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &views); err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 {
		t.Fatalf("views = %+v", views)
	}
	v := views[0]
	if !v.HasKey || v.APIKey == "" || v.APIKey == "sk-x" { // "sk-x" is 4 runes → mask is "…"
		t.Errorf("has_key/api_key = %v/%q, want true + masked non-empty", v.HasKey, v.APIKey)
	}
	if v.ModelCount != 3 || v.ModelsEnabled != 2 {
		t.Errorf("counts = %d/%d, want 3 cached / 2 enabled", v.ModelCount, v.ModelsEnabled)
	}
}

func TestProviderModelsToggleErrors(t *testing.T) {
	d, _ := testDeps(t)
	admin := mustUser(t, d.Store, "merr@example.com", store.RoleAdmin)
	p := seedProvider(t, d, "errco")
	seedModels(t, d, p, cachedModelRows...)
	h := &providersH{d: d}

	cases := []struct {
		name, body, id string
		want           int
	}{
		{"unknown provider", `[{"model_id":"a","enabled":false}]`, "nope", http.StatusNotFound},
		{"foreign model id", `[{"model_id":"b","enabled":false},{"model_id":"ghost","enabled":true}]`, p.ID, http.StatusNotFound},
		{"empty array", `[]`, p.ID, http.StatusBadRequest},
		{"blank model id", `[{"model_id":"","enabled":true}]`, p.ID, http.StatusBadRequest},
		{"not an array", `{"model_id":"a"}`, p.ID, http.StatusBadRequest},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		h.providerModelsUpdate(rec, putReq(tc.body), tc.id, admin)
		if rec.Code != tc.want {
			t.Errorf("%s: status = %d, want %d (body %s)", tc.name, rec.Code, tc.want, rec.Body)
		}
		if !strings.Contains(rec.Body.String(), `"error"`) {
			t.Errorf("%s: body not an error envelope: %s", tc.name, rec.Body)
		}
	}

	// Atomicity: the rejected batch (which included b→off) applied NOTHING.
	rows, err := d.Store.Models.ForProvider(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range rows {
		if m.ModelID == "b" && !m.Enabled {
			t.Error("failed toggle mutated b")
		}
	}

	// Unknown provider on the GET list too.
	rec := httptest.NewRecorder()
	h.providerModels(rec, httptest.NewRequest("GET", "/", nil), "nope")
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown provider GET = %d, want 404", rec.Code)
	}

	// Guards run before any store touch (no session in unit-test ctx → 401).
	rec = httptest.NewRecorder()
	h.handleProviderModels(rec, httptest.NewRequest("GET", "/api/providers/x/models", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous model list = %d, want 401", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.handleProviderModelsUpdate(rec, putReq(`[{"model_id":"a","enabled":false}]`))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous model toggle = %d, want 401", rec.Code)
	}
}

func TestProviderSyncPreservesEnablement(t *testing.T) {
	d, fake := testDeps(t)
	p := seedProvider(t, d, "synckeep")
	h := &providersH{d: d}
	admin := mustUser(t, d.Store, "msync@example.com", store.RoleAdmin)

	sync := func(ids ...string) map[string]int {
		t.Helper()
		fake.modelsResp = nil
		for i, id := range ids {
			fake.modelsResp = append(fake.modelsResp,
				providers.Model{ID: id, ContextLen: 1000 + i})
		}
		rec := httptest.NewRecorder()
		h.sync(rec, httptest.NewRequest("POST", "/api/providers/"+p.ID+"/sync", nil), p.ID, admin)
		if rec.Code != http.StatusOK {
			t.Fatalf("sync status = %d, body %s", rec.Code, rec.Body)
		}
		var resp map[string]int
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		return resp
	}
	flags := func() map[string]bool {
		t.Helper()
		rows, err := d.Store.Models.ForProvider(context.Background(), p.ID)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for _, m := range rows {
			out[m.ModelID] = m.Enabled
		}
		return out
	}
	roles := func() map[string]string {
		t.Helper()
		rows, err := d.Store.Models.ForProvider(context.Background(), p.ID)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, m := range rows {
			out[m.ModelID] = m.MinRole
		}
		return out
	}

	// First sync: every model is new → count 3, added 3, all enabled.
	if resp := sync("a", "b", "c"); resp["count"] != 3 || resp["added"] != 3 {
		t.Errorf("first sync = %v, want count 3 added 3", resp)
	}
	// Admin disables b (and marks it admins-only). A vanished upstream model
	// must not resurrect enabled - covered below.
	rec := httptest.NewRecorder()
	h.providerModelsUpdate(rec, putReq(`[{"model_id":"b","enabled":false,"min_role":"admin"}]`), p.ID, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("toggle status = %d, body %s", rec.Code, rec.Body)
	}

	// Re-sync with a superset: existing flags (incl. disabled b) survive, the
	// new model d lands enabled and is the only "added".
	if resp := sync("a", "b", "c", "d"); resp["count"] != 4 || resp["added"] != 1 {
		t.Errorf("resync = %v, want count 4 added 1", resp)
	}
	if want := map[string]bool{"a": true, "b": false, "c": true, "d": true}; !maps.Equal(flags(), want) {
		t.Errorf("flags after resync = %v, want %v", flags(), want)
	}
	// min_role survives re-sync exactly like enabled: b keeps 'admin', the rest
	// (incl. the newly added d) sit at '' (visible to all).
	if want := map[string]string{"a": "", "b": "admin", "c": "", "d": ""}; !maps.Equal(roles(), want) {
		t.Errorf("roles after resync = %v, want %v", roles(), want)
	}

	// Idempotent re-sync adds nothing and still preserves b=off.
	if resp := sync("a", "b", "c", "d"); resp["added"] != 0 {
		t.Errorf("idempotent sync added = %d, want 0", resp["added"])
	}
	// Upstream removal drops the row entirely (c vanishes).
	if resp := sync("a", "b", "d"); resp["count"] != 3 {
		t.Errorf("shrink sync count = %d, want 3", resp["count"])
	}
	if _, seen := flags()["c"]; seen {
		t.Error("vanished model survived sync")
	}
}

func TestProviderModelsMinRoleAccess(t *testing.T) {
	d, _ := testDeps(t)
	admin := mustUser(t, d.Store, "mrole@example.com", store.RoleAdmin)
	p := seedProvider(t, d, "roleco")
	seedModels(t, d, p,
		store.ModelRow{ModelID: "pub", Display: "Public", Enabled: true},
		store.ModelRow{ModelID: "sec", Display: "Secret", Enabled: true, MinRole: store.RoleAdmin},
	)
	h := &providersH{d: d}

	chatIDs := func(adminView bool) map[string]bool {
		t.Helper()
		rec := httptest.NewRecorder()
		h.models(rec, httptest.NewRequest("GET", "/api/models", nil), adminView)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/models status = %d, body %s", rec.Code, rec.Body)
		}
		var rows []map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
			t.Fatal(err)
		}
		got := map[string]bool{}
		for _, m := range rows {
			got[m["model_id"].(string)] = true
		}
		return got
	}

	// Non-admin catalog: admins-only row hidden.
	if got := chatIDs(false); !got["pub"] || got["sec"] {
		t.Errorf("non-admin catalog = %v, want pub only", got)
	}
	// Admin catalog: both rows present.
	if got := chatIDs(true); !got["pub"] || !got["sec"] {
		t.Errorf("admin catalog = %v, want pub + sec", got)
	}

	// Admin management view carries min_role so the editor can render it.
	rec := httptest.NewRecorder()
	h.providerModels(rec, httptest.NewRequest("GET", "/", nil), p.ID)
	rows := decodeRows(t, rec)
	if role, ok := minRoleOf(rows, "sec"); !ok || role != store.RoleAdmin {
		t.Errorf("admin view sec min_role = %q/%v, want admin", role, ok)
	}
	if role, ok := minRoleOf(rows, "pub"); !ok || role != "" {
		t.Errorf("admin view pub min_role = %q/%v, want empty", role, ok)
	}

	// PUT: promote pub to admins-only, demote sec to everyone.
	rec = httptest.NewRecorder()
	h.providerModelsUpdate(rec, putReq(`[{"model_id":"pub","enabled":true,"min_role":"admin"},{"model_id":"sec","enabled":true,"min_role":""}]`), p.ID, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("min_role update status = %d, body %s", rec.Code, rec.Body)
	}
	// Flipped: the non-admin catalog now shows sec, not pub.
	if got := chatIDs(false); got["pub"] || !got["sec"] {
		t.Errorf("non-admin catalog after flip = %v, want sec only", got)
	}
}

func TestProviderModelsMinRoleErrors(t *testing.T) {
	d, _ := testDeps(t)
	admin := mustUser(t, d.Store, "mroleerr@example.com", store.RoleAdmin)
	p := seedProvider(t, d, "roleerrco")
	seedModels(t, d, p, cachedModelRows...)
	h := &providersH{d: d}

	// An unknown min_role value is rejected (400) and nothing is applied — the
	// batch carried a real disable of "a" that must NOT take effect.
	rec := httptest.NewRecorder()
	h.providerModelsUpdate(rec, putReq(`[{"model_id":"a","enabled":false,"min_role":"root"}]`), p.ID, admin)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad min_role status = %d, want 400 (body %s)", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"error"`) {
		t.Errorf("body not an error envelope: %s", rec.Body)
	}
	rows, err := d.Store.Models.ForProvider(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range rows {
		if m.ModelID == "a" && !m.Enabled {
			t.Error("rejected batch applied a→disabled")
		}
	}
}

func TestProviderPutPartialKeepsKey(t *testing.T) {
	d, _ := testDeps(t)
	admin := mustUser(t, d.Store, "mkey@example.com", store.RoleAdmin)
	p := seedProvider(t, d, "keyco") // APIKey "sk-x"
	h := &providersH{d: d}
	ctx := context.Background()

	put := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.update(rec, httptest.NewRequest("PUT", "/api/providers/"+p.ID, strings.NewReader(body)), p.ID, admin)
		return rec
	}
	key := func() string {
		got, err := d.Store.Providers.Get(ctx, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got.APIKey
	}

	// Name-only patch: base_url + key untouched, response masks the key.
	rec := put(`{"name":"KeyCo v2"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch status = %d, body %s", rec.Code, rec.Body)
	}
	if key() != "sk-x" {
		t.Errorf("key after name patch = %q, want kept", key())
	}
	// Round-tripped mask (GET → PUT in the admin UI) keeps the stored key.
	if rec = put(`{"api_key":"…"}`); rec.Code != http.StatusOK || key() != "sk-x" {
		t.Errorf("masked round-trip: status %d key %q, want 200/kept", rec.Code, key())
	}
	// Explicit replace, then explicit clear.
	if rec = put(`{"api_key":"sk-fresh-9"}`); rec.Code != http.StatusOK || key() != "sk-fresh-9" {
		t.Errorf("replace: status %d key %q, want 200/sk-fresh-9", rec.Code, key())
	}
	if rec = put(`{"api_key":""}`); rec.Code != http.StatusOK || key() != "" {
		t.Errorf("clear: status %d key %q, want 200/empty", rec.Code, key())
	}
	// Cleared key flips has_key off in the list view.
	rec = httptest.NewRecorder()
	h.list(rec, httptest.NewRequest("GET", "/api/providers", nil))
	var views []providerJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &views); err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].HasKey || views[0].APIKey != "" {
		t.Errorf("view after clear = %+v, want has_key=false api_key=\"\"", views)
	}
	// Invalid base_url is still rejected (400 envelope, row untouched).
	if rec = put(`{"base_url":"ftp://nope"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad base_url status = %d, want 400 (body %s)", rec.Code, rec.Body)
	}
}
