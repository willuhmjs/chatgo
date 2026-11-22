package apimgr

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"chatgo/internal/api/httpx"
	"chatgo/internal/providers"
	"chatgo/internal/store"
)

// Provider admin surface: GET/POST /api/providers, PUT/DELETE /api/providers/{id},
// POST /api/providers/{id}/sync (refresh models cache via Providers.ListModels),
// plus authed GET /api/models for everyone.

type providersH struct{ d *Deps }

func registerProviders(mux *http.ServeMux, d *Deps) {
	h := &providersH{d: d}
	mux.HandleFunc("GET /api/providers", h.handleList)
	mux.HandleFunc("POST /api/providers", h.handleCreate)
	mux.HandleFunc("PUT /api/providers/{id}", h.handleUpdate)
	mux.HandleFunc("DELETE /api/providers/{id}", h.handleDelete)
	mux.HandleFunc("POST /api/providers/{id}/sync", h.handleSync)
	mux.HandleFunc("GET /api/models", h.handleModels)
}

// ---------- JSON shapes (web/src/lib/api.ts is the wire contract) ----------

type providerJSON struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	BaseURL   string `json:"base_url"`
	APIKey    string `json:"api_key"` // MASKED ("…abcd") — never the real key
	Enabled   bool   `json:"enabled"`
	IsDefault bool   `json:"is_default"`
	Sort      int    `json:"sort"`
	CreatedAt int64  `json:"created_at"`
}

func providerView(p *store.Provider) providerJSON {
	return providerJSON{
		ID: p.ID, Name: p.Name, BaseURL: p.BaseURL,
		APIKey: maskKey(p.APIKey), Enabled: p.Enabled, IsDefault: p.IsDefault,
		Sort: p.Sort, CreatedAt: p.CreatedAt,
	}
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
		out = append(out, providerView(p))
	}
	httpx.JSON(w, http.StatusOK, out)
}

// POST /api/providers (admin).
func (h *providersH) handleCreate(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.d.requireAdmin(w, r); !ok {
		return
	}
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
	httpx.JSON(w, http.StatusCreated, providerView(p))
}

// PUT /api/providers/{id} (admin) — partial patch; api_key keeps/clears/sets
// per providerReq semantics above.
func (h *providersH) handleUpdate(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.d.requireAdmin(w, r); !ok {
		return
	}
	p, err := h.d.Store.Providers.Get(r.Context(), r.PathValue("id"))
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
	httpx.JSON(w, http.StatusOK, providerView(p))
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
	w.WriteHeader(http.StatusNoContent)
}

// handleSync: POST /api/providers/{id}/sync — pull the upstream model list
// and atomically replace the cache (context_len persisted, models enabled).
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
	rows := make([]store.ModelRow, 0, len(models))
	for _, m := range models {
		if m.ID == "" {
			continue
		}
		rows = append(rows, store.ModelRow{
			ModelID: m.ID, Display: m.Display, ContextLen: m.ContextLen, Enabled: true,
		})
	}
	if err := h.d.Store.Models.ReplaceForProvider(r.Context(), p.ID, rows); err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]int{"count": len(rows)})
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
