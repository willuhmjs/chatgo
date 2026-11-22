package apimgr

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"chatgo/internal/api/httpx"
	"chatgo/internal/audit"
	"chatgo/internal/providers"
	"chatgo/internal/store"
)

// Provider admin surface: GET/POST /api/providers, PUT/DELETE /api/providers/{id},
// POST /api/providers/{id}/sync (refresh models cache via Providers.ListModels),
// GET/PUT /api/providers/{id}/models (admin view of the cache incl. disabled
// rows + bulk enable/disable), plus authed GET /api/models for everyone.

// MaxProviderNameLen caps the display name in RUNES (L8-M2): the name lands
// verbatim in audit detail rows (provider.create/update) and in every
// /api/providers + /api/models list payload, so a megabyte name was
// permanent storage/UI abuse. 200 runes is generous for a real provider.
const MaxProviderNameLen = 200

type providersH struct{ d *Deps }

func registerProviders(mux *http.ServeMux, d *Deps) {
	h := &providersH{d: d}
	mux.HandleFunc("GET /api/providers", h.handleList)
	mux.HandleFunc("POST /api/providers", h.handleCreate)
	mux.HandleFunc("PUT /api/providers/{id}", h.handleUpdate)
	mux.HandleFunc("DELETE /api/providers/{id}", h.handleDelete)
	mux.HandleFunc("POST /api/providers/{id}/sync", h.handleSync)
	mux.HandleFunc("GET /api/providers/{id}/models", h.handleProviderModels)
	mux.HandleFunc("PUT /api/providers/{id}/models", h.handleProviderModelsUpdate)
	mux.HandleFunc("GET /api/models", h.handleModels)
}

// ---------- JSON shapes (web/src/lib/api.ts is the wire contract) ----------

type providerJSON struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	BaseURL   string `json:"base_url"`
	APIKey    string `json:"api_key"` // MASKED ("…abcd") — never the real key
	HasKey    bool   `json:"has_key"` // key status dot without revealing anything
	Enabled   bool   `json:"enabled"`
	IsDefault bool   `json:"is_default"`
	Sort      int    `json:"sort"`
	CreatedAt int64  `json:"created_at"`
	// Cached-model tallies so the admin table can render counts without
	// pulling every row (the expandable editor fetches them per provider).
	ModelCount    int `json:"model_count"`
	ModelsEnabled int `json:"models_enabled"`
}

func providerView(p *store.Provider) providerJSON {
	return providerJSON{
		ID: p.ID, Name: p.Name, BaseURL: p.BaseURL,
		APIKey: maskKey(p.APIKey), HasKey: p.APIKey != "" || p.KeyUnreadable,
		Enabled: p.Enabled, IsDefault: p.IsDefault,
		Sort: p.Sort, CreatedAt: p.CreatedAt,
	}
}

// view is providerView plus the cached-model tallies (one ForProvider query
// per row; provider tables are admin-scale and tiny).
func (h *providersH) view(ctx context.Context, p *store.Provider) (providerJSON, error) {
	out := providerView(p)
	ms, err := h.d.Store.Models.ForProvider(ctx, p.ID)
	if err != nil {
		return out, err
	}
	for _, m := range ms {
		out.ModelCount++
		if m.Enabled {
			out.ModelsEnabled++
		}
	}
	return out, nil
}

// modelRowJSON is one cached model row for the admin per-provider view. Unlike
// GET /api/models this includes DISABLED rows and carries the enabled flag —
// it is the management surface, so requireAdmin gates it.
type modelRowJSON struct {
	ModelID    string `json:"model_id"`
	Display    string `json:"display"`
	ContextLen int    `json:"context_len"`
	Enabled    bool   `json:"enabled"`
}

func modelsView(ms []store.ModelRow) []modelRowJSON {
	out := make([]modelRowJSON, 0, len(ms))
	for _, m := range ms {
		out = append(out, modelRowJSON{
			ModelID: m.ModelID, Display: m.Display,
			ContextLen: m.ContextLen, Enabled: m.Enabled,
		})
	}
	return out
}

// modelToggleReq is one entry of the bulk PUT /api/providers/{id}/models body
// ([{model_id, enabled}, …]). Bulk beats per-model PATCH: the "select all /
// none" UI and every single toggle land in one round-trip, and the whole
// write is one store transaction (no half-applied batches).
type modelToggleReq struct {
	ModelID string `json:"model_id"`
	Enabled bool   `json:"enabled"`
}

// providerReq is the POST/PUT body. Pointers = "field present": PUT patches
// only what was sent; an api_key that is absent OR round-tripped-masked keeps
// the stored key, an explicit "" clears it.
type providerReq struct {
	Name      string  `json:"name"`
	BaseURL   string  `json:"base_url"`
	APIKey    *string `json:"api_key"`
	Enabled   *bool   `json:"enabled"`
	IsDefault *bool   `json:"is_default"`
	Sort      *int    `json:"sort"`
}

func (req *providerReq) validate(requireName bool) string {
	if requireName && strings.TrimSpace(req.Name) == "" {
		return "name is required"
	}
	if utf8.RuneCountInString(strings.TrimSpace(req.Name)) > MaxProviderNameLen {
		return "name must be at most " + strconv.Itoa(MaxProviderNameLen) + " characters"
	}
	if req.BaseURL == "" && requireName {
		return "base_url is required"
	}
	if req.BaseURL != "" {
		u, err := url.Parse(req.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return "base_url must be an absolute http(s) URL"
		}
	}
	return ""
}

// ---------- handlers ----------

// GET /api/providers → [{…,api_key:"…abcd"}] (admin).
func (h *providersH) handleList(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.d.requireAdmin(w, r); !ok {
		return
	}
	h.list(w, r)
}

func (h *providersH) list(w http.ResponseWriter, r *http.Request) {
	provs, err := h.d.Store.Providers.List(r.Context())
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	out := make([]providerJSON, 0, len(provs))
	for _, p := range provs {
		v, err := h.view(r.Context(), p)
		if err != nil {
			httpx.InternalError(w, r, err)
			return
		}
		out = append(out, v)
	}
	httpx.JSON(w, http.StatusOK, out)
}

// POST /api/providers (admin).
func (h *providersH) handleCreate(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.d.requireAdmin(w, r)
	if !ok {
		return
	}
	h.create(w, r, actor)
}

// create is the POST core (list/sync/del split pattern; tests drive it with
// a seeded admin).
func (h *providersH) create(w http.ResponseWriter, r *http.Request, actor *store.User) {
	var req providerReq
	if err := httpx.DecodeLoose(r, &req); err != nil {
		httpx.ErrorJSON(w, http.StatusBadRequest, "invalid provider body")
		return
	}
	if msg := req.validate(true); msg != "" {
		httpx.ErrorJSON(w, http.StatusBadRequest, msg)
		return
	}
	p := &store.Provider{
		Name: strings.TrimSpace(req.Name), BaseURL: strings.TrimSpace(req.BaseURL),
		Enabled: boolOr(req.Enabled, true), IsDefault: boolOr(req.IsDefault, false),
		Sort: intOr(req.Sort, 0),
	}
	if req.APIKey != nil {
		p.APIKey = *req.APIKey
	}
	if err := h.d.Store.Providers.Insert(r.Context(), p); err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	if p.IsDefault {
		if err := h.clearOtherDefaults(r.Context(), p.ID); err != nil {
			httpx.InternalError(w, r, err)
			return
		}
	}
	audit.Record(r.Context(), h.d.Store.DB, actor, "provider.create", p.Name, h.d.Auth.RequestIP(r))
	v, err := h.view(r.Context(), p)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, v)
}

// PUT /api/providers/{id} (admin) — partial patch; api_key keeps/clears/sets
// per providerReq semantics above.
func (h *providersH) handleUpdate(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.d.requireAdmin(w, r)
	if !ok {
		return
	}
	h.update(w, r, r.PathValue("id"), actor)
}

// update is the PUT core (see create for why the split).
func (h *providersH) update(w http.ResponseWriter, r *http.Request, id string, actor *store.User) {
	p, err := h.d.Store.Providers.Get(r.Context(), id)
	if err != nil {
		providerLookupFail(w, r, err)
		return
	}
	var req providerReq
	if err := httpx.DecodeLoose(r, &req); err != nil {
		httpx.ErrorJSON(w, http.StatusBadRequest, "invalid provider body")
		return
	}
	if msg := req.validate(false); msg != "" {
		httpx.ErrorJSON(w, http.StatusBadRequest, msg)
		return
	}
	if req.Name != "" {
		p.Name = strings.TrimSpace(req.Name)
	}
	if req.BaseURL != "" {
		p.BaseURL = strings.TrimSpace(req.BaseURL)
	}
	switch {
	case req.APIKey == nil, looksMasked(*req.APIKey): // keep stored key
	case *req.APIKey == "":
		p.APIKey = ""
	default:
		p.APIKey = *req.APIKey
	}
	if req.Enabled != nil {
		p.Enabled = *req.Enabled
	}
	if req.IsDefault != nil {
		p.IsDefault = *req.IsDefault
	}
	if req.Sort != nil {
		p.Sort = *req.Sort
	}
	if err := h.d.Store.Providers.Update(r.Context(), p); err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	if p.IsDefault {
		if err := h.clearOtherDefaults(r.Context(), p.ID); err != nil {
			httpx.InternalError(w, r, err)
			return
		}
	}
	audit.Record(r.Context(), h.d.Store.DB, actor, "provider.update", p.Name, h.d.Auth.RequestIP(r))
	v, err := h.view(r.Context(), p)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

// DELETE /api/providers/{id} (admin) → 204.
func (h *providersH) handleDelete(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.d.requireAdmin(w, r); !ok {
		return
	}
	h.del(w, r, r.PathValue("id"))
}

func (h *providersH) del(w http.ResponseWriter, r *http.Request, id string) {
	if err := h.d.Store.Providers.Delete(r.Context(), id); err != nil {
		providerLookupFail(w, r, err)
		return
	}
	actor, _ := h.d.Auth.UserFrom(r.Context())
	audit.Record(r.Context(), h.d.Store.DB, actor, "provider.delete", id, h.d.Auth.RequestIP(r))
	w.WriteHeader(http.StatusNoContent)
}

// handleSync: POST /api/providers/{id}/sync — pull the upstream model list
// and refresh the cache (context_len persisted). Admin enable/disable choices
// are PRESERVED: models already cached keep their enabled flag, only models
// new to the cache start enabled=true. Models that vanished upstream are
// dropped (the whole cache row-set is replaced).
func (h *providersH) handleSync(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.d.requireAdmin(w, r); !ok {
		return
	}
	h.sync(w, r, r.PathValue("id"))
}

func (h *providersH) sync(w http.ResponseWriter, r *http.Request, id string) {
	p, err := h.d.Store.Providers.Get(r.Context(), id)
	if err != nil {
		providerLookupFail(w, r, err)
		return
	}
	models, err := h.d.Providers.ListModels(r.Context(), p.BaseURL, p.APIKey)
	if err != nil {
		// Log server-side only (upstream errors may embed URLs); client gets a
		// generic, safe message (contract: internals never leak).
		slog.Error("providers: sync failed", "provider_id", p.ID, "err", err)
		httpx.ErrorJSON(w, http.StatusBadGateway, "could not fetch models from provider")
		return
	}
	cached, err := h.d.Store.Models.ForProvider(r.Context(), p.ID)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	prevEnabled := make(map[string]bool, len(cached))
	for _, m := range cached {
		prevEnabled[m.ModelID] = m.Enabled
	}
	rows := make([]store.ModelRow, 0, len(models))
	added := 0
	for _, m := range models {
		if m.ID == "" {
			continue
		}
		enabled, isNew := prevEnabled[m.ID]
		if !isNew {
			enabled = true // first-seen model: instance-default is enabled
			added++
		}
		rows = append(rows, store.ModelRow{
			ModelID: m.ID, Display: m.Display, ContextLen: m.ContextLen, Enabled: enabled,
		})
	}
	if err := h.d.Store.Models.ReplaceForProvider(r.Context(), p.ID, rows); err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]int{"count": len(rows), "added": added})
}

// handleProviderModels: GET /api/providers/{id}/models — the FULL cached model
// list of one provider incl. disabled rows (admin; GET /api/models stays the
// enabled-only chat surface).
func (h *providersH) handleProviderModels(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.d.requireAdmin(w, r); !ok {
		return
	}
	h.providerModels(w, r, r.PathValue("id"))
}

func (h *providersH) providerModels(w http.ResponseWriter, r *http.Request, id string) {
	if _, err := h.d.Store.Providers.Get(r.Context(), id); err != nil {
		providerLookupFail(w, r, err)
		return
	}
	ms, err := h.d.Store.Models.ForProvider(r.Context(), id)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, modelsView(ms))
}

// handleProviderModelsUpdate: PUT /api/providers/{id}/models (admin) — bulk
// enable/disable, body [{model_id, enabled}, …]. Every model_id must exist in
// the provider's cache (one unknown id → 404, nothing applied). Returns the
// refreshed full list so the admin UI can reconcile without a follow-up GET.
func (h *providersH) handleProviderModelsUpdate(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.d.requireAdmin(w, r)
	if !ok {
		return
	}
	h.providerModelsUpdate(w, r, r.PathValue("id"), actor)
}

func (h *providersH) providerModelsUpdate(w http.ResponseWriter, r *http.Request, id string, actor *store.User) {
	p, err := h.d.Store.Providers.Get(r.Context(), id)
	if err != nil {
		providerLookupFail(w, r, err)
		return
	}
	var reqs []modelToggleReq
	if err := httpx.DecodeLoose(r, &reqs); err != nil {
		httpx.ErrorJSON(w, http.StatusBadRequest, "invalid model body")
		return
	}
	if len(reqs) == 0 {
		httpx.ErrorJSON(w, http.StatusBadRequest, "at least one model toggle is required")
		return
	}
	want := make(map[string]bool, len(reqs)) // duplicate ids: last write wins
	for _, t := range reqs {
		if t.ModelID == "" {
			httpx.ErrorJSON(w, http.StatusBadRequest, "model_id is required")
			return
		}
		want[t.ModelID] = t.Enabled
	}
	rows, err := h.d.Store.Models.ForProvider(r.Context(), id)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	seen := 0
	for i := range rows {
		if en, ok := want[rows[i].ModelID]; ok {
			rows[i].Enabled = en
			seen++
		}
	}
	if seen != len(want) {
		httpx.ErrorJSON(w, http.StatusNotFound, "model not found")
		return
	}
	if err := h.d.Store.Models.ReplaceForProvider(r.Context(), id, rows); err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	audit.Record(r.Context(), h.d.Store.DB, actor, "provider.update",
		p.Name+" · "+strconv.Itoa(seen)+" model enablement(s) updated", h.d.Auth.RequestIP(r))
	httpx.JSON(w, http.StatusOK, modelsView(rows))
}

// handleModels: GET /api/models — flattened enabled models (authed, any role).
func (h *providersH) handleModels(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.d.requireUser(w, r); !ok {
		return
	}
	h.models(w, r)
}

func (h *providersH) models(w http.ResponseWriter, r *http.Request) {
	ms, err := h.d.Store.Models.EnabledModels(r.Context())
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	out := make([]map[string]any, 0, len(ms))
	for _, m := range ms {
		out = append(out, map[string]any{
			"provider_id": m.ProviderID, "provider": m.ProviderName,
			"model_id": m.ModelID, "display": m.Display, "context_len": m.ContextLen,
		})
	}
	httpx.JSON(w, http.StatusOK, out)
}

// ---------- helpers ----------

// clearOtherDefaults keeps is_default exclusive (repo has no unique guard).
func (h *providersH) clearOtherDefaults(ctx context.Context, keepID string) error {
	provs, err := h.d.Store.Providers.List(ctx)
	if err != nil {
		return err
	}
	for _, p := range provs {
		if p.ID != keepID && p.IsDefault {
			p.IsDefault = false
			if err := h.d.Store.Providers.Update(ctx, p); err != nil {
				return err
			}
		}
	}
	return nil
}

// providerLookupFail maps store errors: ErrNotFound → 404, everything else
// logs and answers a generic 500.
func providerLookupFail(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrNotFound) {
		httpx.ErrorJSON(w, http.StatusNotFound, "provider not found")
		return
	}
	httpx.InternalError(w, r, err)
}

// upstreamProvider maps a store row to the providers-client shape.
func upstreamProvider(p *store.Provider) providers.Provider {
	return providers.Provider{ID: p.ID, Name: p.Name, BaseURL: p.BaseURL, APIKey: p.APIKey}
}

func boolOr(b *bool, def bool) bool {
	if b == nil {
		return def
	}
	return *b
}

func intOr(i *int, def int) int {
	if i == nil {
		return def
	}
	return *i
}
