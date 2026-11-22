package apimgr

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"chatgo/internal/api/httpx"
	"chatgo/internal/audit"
	"chatgo/internal/auth"
	"chatgo/internal/store"
)

// ---------- shared auth guards ----------

// requireUser resolves the session user attached by auth middleware. The
// policy layer (api/server.go AuthPolicy) already gates route groups; this is
// defense-in-depth for handlers reached any other way.
func (d *Deps) requireUser(w http.ResponseWriter, r *http.Request) (*store.User, bool) {
	u, ok := d.Auth.UserFrom(r.Context())
	if !ok {
		httpx.ErrorJSON(w, http.StatusUnauthorized, "authentication required")
		return nil, false
	}
	return u, true
}

// requireAdmin is requireUser + admin role (403 non-admin per contract
// §Non-negotiables).
func (d *Deps) requireAdmin(w http.ResponseWriter, r *http.Request) (*store.User, bool) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return nil, false
	}
	if !u.IsAdmin() {
		httpx.ErrorJSON(w, http.StatusForbidden, "admin required")
		return nil, false
	}
	return u, true
}

// ---------- secrets masking ----------

// maskKey renders a secret as "…"+last-4 (admin views never see the whole
// key). Values of 4 chars or fewer collapse to a bare "…" so short secrets
// are not echoed whole.
func maskKey(secret string) string {
	if secret == "" {
		return ""
	}
	r := []rune(secret)
	if len(r) <= 4 {
		return "…"
	}
	return "…" + string(r[len(r)-4:])
}

// looksMasked reports whether v came back from maskKey (GET → PUT round-trips
// in admin UIs must not overwrite a real key with its own mask).
func looksMasked(v string) bool { return strings.HasPrefix(v, "…") }

// isKeyish names settings whose VALUE must never be echoed (defensive: the
// allowlist below holds no secrets, but legacy rows may).
func isKeyish(key string) bool {
	k := strings.ToLower(key)
	for _, sig := range []string{"key", "secret", "token", "password"} {
		if strings.Contains(k, sig) {
			return true
		}
	}
	return false
}

// ---------- GET/PUT /api/settings (admin) ----------

// maxSettingValue caps one settings value (task: ≤ 8KB).
const maxSettingValue = 8 << 10

// settingValidators is the admin allowlist: reserved global keys and their
// value checks (nil = free text). Keys outside it are rejected (400).
var settingValidators = map[string]func(string) string{
	"title":      nil, // instance display name
	"task_model": validateTaskModel,
	// KB embeddings escape hatch (internal/kb/embed.go picks it up as
	// "providerID:modelID"; empty = auto-detect).
	"kb_embed_model": validateKBEmbedModel,
	"system_prompt":  nil, // global extra system prompt
	"registration":   validateRegistration,
	"search_url":     validateSearchURL,
	// Written once the admin finishes the onboarding wizard (global mirror of
	// the per-user setup_done me-setting).
	"instance_setup_done": nil,
}

func validateTaskModel(v string) string {
	if v == "" {
		return ""
	}
	if !strings.Contains(v, ":") {
		return "task_model must be \"providerID:modelID\" or empty"
	}
	return ""
}

// validateKBEmbedModel mirrors validateTaskModel's "providerID:modelID"-or-
// empty shape (kept as a copy on purpose — no shared helper to refactor into).
// The colon rule matches kb.splitSpec in internal/kb/embed.go exactly: the
// FIRST colon splits provider from model and both halves must be non-empty,
// so ":mid" and "pid:" are rejected here instead of silently ignored by kb.
func validateKBEmbedModel(v string) string {
	if v == "" {
		return ""
	}
	i := strings.IndexByte(v, ':')
	if i <= 0 || i == len(v)-1 {
		return "kb_embed_model must be \"providerID:modelID\" or empty"
	}
	return ""
}

func validateRegistration(v string) string {
	switch v {
	case "", "open", "approve", "closed":
		return ""
	}
	return `registration must be "open", "approve", "closed" or empty`
}

func validateSearchURL(v string) string {
	if v == "" {
		return ""
	}
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "search_url must be an absolute http(s) URL or empty"
	}
	return ""
}

type adminH struct{ d *Deps }

func registerAdmin(mux *http.ServeMux, d *Deps) {
	h := &adminH{d: d}
	mux.HandleFunc("GET /api/settings", h.handleGet)
	mux.HandleFunc("PUT /api/settings", h.handlePut)
	mux.HandleFunc("GET /api/me/settings", h.handleMeGet)
	mux.HandleFunc("PUT /api/me/settings", h.handleMePut)
	mux.HandleFunc("POST /api/me/password", h.handleMePassword)
}

// settingsView loads every setting with key-ish values masked and per-user
// "me.*" rows stripped (they are not admin settings).
func settingsView(ctx context.Context, d *Deps) (map[string]string, error) {
	all, err := d.Store.Settings.All(ctx)
	if err != nil {
		return nil, err
	}
	stripNamespacedSettings(all)
	for k, v := range all {
		if isKeyish(k) {
			all[k] = maskKey(v)
		}
	}
	return all, nil
}

// GET /api/settings → map[string]string (secrets masked, never echoed).
func (h *adminH) handleGet(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.d.requireAdmin(w, r); !ok {
		return
	}
	view, err := settingsView(r.Context(), h.d)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, view)
}

// PUT /api/settings {key:value,…} → validates keys against the allowlist and
// values against their validators (≤8KB), upserts all-or-nothing, returns
// the resulting masked map.
func (h *adminH) handlePut(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.d.requireAdmin(w, r); !ok {
		return
	}
	h.put(w, r)
}

// put is the testable core of handlePut (auth already resolved).
func (h *adminH) put(w http.ResponseWriter, r *http.Request) {
	var in map[string]string
	err := httpx.Decode(r, &in)
	if err != nil {
		var mb *http.MaxBytesError
		if errors.As(err, &mb) {
			httpx.ErrorJSON(w, http.StatusRequestEntityTooLarge, "settings too large")
			return
		}
		httpx.ErrorJSON(w, http.StatusBadRequest, "body must be a JSON object of string values")
		return
	}

	// Validate everything before writing anything (no partial apply).
	for k, v := range in {
		validator, ok := settingValidators[k]
		if !ok {
			httpx.ErrorJSON(w, http.StatusBadRequest, "unknown setting key "+k)
			return
		}
		if len(v) > maxSettingValue {
			httpx.ErrorJSON(w, http.StatusBadRequest, "setting value for "+k+" exceeds 8KB")
			return
		}
		if validator != nil {
			if msg := validator(v); msg != "" {
				httpx.ErrorJSON(w, http.StatusBadRequest, msg)
				return
			}
		}
	}
	for k, v := range in {
		if err := h.d.Store.Settings.Set(r.Context(), k, v); err != nil {
			httpx.InternalError(w, r, err)
			return
		}
	}
	view, err := settingsView(r.Context(), h.d)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	actor, _ := h.d.Auth.UserFrom(r.Context())
	audit.Record(r.Context(), h.d.Store.DB, actor, "settings.update", "", h.d.Auth.RequestIP(r))
	httpx.JSON(w, http.StatusOK, view)
}

// ---------- GET/PUT /api/me/settings (self) ----------
//
// CONTRACT-DEVIATION: the schema (scaffold-owned migrate.go) has no
// user_settings table, so per-user prefs are namespaced into `settings` under
// "me.<userID>.<key>". Only meSettingKeys is allowlisted; rows are invisible
// to admin GET /api/settings because that lists… well, All() — the admin view
// filters the "me." namespace out below.

const meSettingPrefix = "me."

// meSettingKeys: "theme" is the UI pref; "setup_done" is written by the
// onboarding wizard (App.svelte's fresh-admin nudge reads it back); "accent"
// is reserved for the color-picker wave; "default_model" pins the composer's
// initial model; "hidden_models" is the picker's per-user hide list; "name"
// is the profile display name SettingsMe's Save writes (api-shell setMeName).
// All plain strings, 8KB-capped like every other setting value; the model
// keys and name are further validated by meSettingValidators below.
var meSettingKeys = map[string]bool{
	"theme":         true,
	"setup_done":    true,
	"accent":        true,
	"default_model": true,
	"hidden_models": true,
	"name":          true,
}

// maxHiddenModels caps one hidden_models list (task: ≤512 entries; the total
// stays under maxSettingValue via the existing 8KB check).
const maxHiddenModels = 512

// meSettingValidators mirrors settingValidators for the per-user surface:
// only keys present here get value checks in handleMePut (theme/setup_done/
// accent stay free text). Values arrive already unmarshalled from JSON.
var meSettingValidators = map[string]func(string) string{
	"default_model": validateMeDefaultModel,
	"hidden_models": validateMeHiddenModels,
	"name":          validateMeName,
}

// maxMeNameRunes caps the profile display name (P0-2: SettingsMe's Save 400'd
// because "name" was missing from meSettingKeys entirely).
const maxMeNameRunes = 100

// validateMeName accepts "" (cleared — the UI falls back to the account
// email) and anything up to 100 runes. Rune count, not bytes: the 8KB byte
// cap in handleMePut already guards storage, and a byte budget here would
// punish CJK/emoji names ~3× per character.
func validateMeName(v string) string {
	if utf8.RuneCountInString(v) > maxMeNameRunes {
		return "name must be at most 100 characters"
	}
	return ""
}

// validateMeDefaultModel accepts "" (unset) or "providerID:modelID" with the
// same first-colon, both-halves-non-empty shape as validateKBEmbedModel.
func validateMeDefaultModel(v string) string {
	if v == "" {
		return ""
	}
	i := strings.IndexByte(v, ':')
	if i <= 0 || i == len(v)-1 {
		return `default_model must be "providerID:modelID" or empty`
	}
	return ""
}

// validateMeHiddenModels accepts a JSON array of "providerID:modelID"
// strings, e.g. `["ollama:llama3","openai:gpt-4"]` (the FE JSON.parses the
// stored value back). "null" and other JSON scalars are rejected: the value
// must literally be an array. Entry strings get the same colon rule as
// validateMeDefaultModel.
func validateMeHiddenModels(v string) string {
	trimmed := strings.TrimSpace(v)
	if trimmed == "" || trimmed[0] != '[' {
		return `hidden_models must be a JSON array of "providerID:modelID" strings`
	}
	var entries []string
	if err := json.Unmarshal([]byte(trimmed), &entries); err != nil {
		return `hidden_models must be a JSON array of "providerID:modelID" strings`
	}
	if len(entries) > maxHiddenModels {
		return "hidden_models must have at most 512 entries"
	}
	for _, e := range entries {
		i := strings.IndexByte(e, ':')
		if i <= 0 || i == len(e)-1 {
			return `hidden_models entries must be "providerID:modelID"`
		}
	}
	return ""
}

// handleMeGet returns the caller's own prefs (namespaced keys stripped) plus
// the computed search_enabled capability the composer probes for.
func (h *adminH) handleMeGet(w http.ResponseWriter, r *http.Request) {
	u, ok := h.d.requireUser(w, r)
	if !ok {
		return
	}
	out := h.meSettings(r, u.ID)
	enabled := h.d.Cfg != nil && h.d.Cfg.SearXNGURL != ""
	if !enabled {
		if v, err := h.d.Store.Settings.Get(r.Context(), "search_url"); err == nil && v != nil && *v != "" {
			enabled = true
		}
	}
	out["search_enabled"] = strconv.FormatBool(enabled)
	httpx.JSON(w, http.StatusOK, out)
}

// handleMePassword rotates a local account's password and revokes every
// session (the current request gets a freshly issued one).
func (h *adminH) handleMePassword(w http.ResponseWriter, r *http.Request) {
	u, ok := h.d.requireUser(w, r)
	if !ok {
		return
	}
	// Per-session 5/min gate: an authenticated attacker must not grind the
	// current-password check for hash oracles or CPU.
	gateKey := "mepass:" + u.ID
	if s, sok := h.d.Auth.SessionFrom(r.Context()); sok {
		gateKey = "mepass:" + s.Token
	}
	if !h.d.Auth.Allow(gateKey, auth.PasswordChangePerMin) {
		httpx.ErrorJSON(w, http.StatusTooManyRequests, "too many password change attempts, try again later")
		return
	}
	if u.PasswordHash == nil || *u.PasswordHash == "" {
		httpx.ErrorJSON(w, http.StatusBadRequest, "this account signs in through single sign-on")
		return
	}
	var body struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := httpx.Decode(r, &body); err != nil {
		httpx.ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// Cheap checks before any bcrypt runs (bytes, not characters: bcrypt
	// truncates at 72 BYTES).
	if len(body.NewPassword) < auth.MinPasswordLen || len(body.NewPassword) > auth.MaxPasswordLen {
		httpx.ErrorJSON(w, http.StatusBadRequest, "new password must be 8-72 bytes")
		return
	}
	release, err := h.d.Auth.AcquireBcrypt(r.Context())
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	currentOK := auth.VerifyPassword(*u.PasswordHash, body.CurrentPassword)
	var hash string
	if currentOK {
		hash, err = auth.HashPassword(body.NewPassword)
	}
	release()
	if !currentOK {
		httpx.ErrorJSON(w, http.StatusUnauthorized, "current password incorrect")
		return
	}
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	if err := h.d.Store.Users.SetPassword(r.Context(), u.ID, hash); err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	if err := h.d.Store.Sessions.DeleteAllForUser(r.Context(), u.ID); err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	sess, err := h.d.Auth.CreateSession(r.Context(), u.ID, r.UserAgent(), h.d.Auth.RequestIP(r))
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	h.d.Auth.SetSessionCookie(w, r, sess.Token, time.Unix(sess.ExpiresAt, 0))
	audit.Record(r.Context(), h.d.Store.DB, u, "password.change", "", h.d.Auth.RequestIP(r))
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *adminH) handleMePut(w http.ResponseWriter, r *http.Request) {
	u, ok := h.d.requireUser(w, r)
	if !ok {
		return
	}
	var raw map[string]json.RawMessage
	if err := httpx.Decode(r, &raw); err != nil {
		httpx.ErrorJSON(w, http.StatusBadRequest, "body must be a JSON object")
		return
	}
	for k, v := range raw {
		if !meSettingKeys[k] {
			httpx.ErrorJSON(w, http.StatusBadRequest, "unknown setting key "+k)
			return
		}
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			httpx.ErrorJSON(w, http.StatusBadRequest, k+" must be a string")
			return
		}
		if len(s) > maxSettingValue {
			httpx.ErrorJSON(w, http.StatusBadRequest, k+" too large")
			return
		}
		if validator, ok := meSettingValidators[k]; ok {
			if msg := validator(s); msg != "" {
				httpx.ErrorJSON(w, http.StatusBadRequest, msg)
				return
			}
		}
		if err := h.d.Store.Settings.Set(r.Context(), meSettingPrefix+u.ID+"."+k, s); err != nil {
			httpx.InternalError(w, r, err)
			return
		}
	}
	httpx.JSON(w, http.StatusOK, h.meSettings(r, u.ID))
}

// meSettings collects "me.<uid>.*" rows into a bare {key:value} map.
func (h *adminH) meSettings(r *http.Request, userID string) map[string]string {
	out := map[string]string{}
	all, err := h.d.Store.Settings.All(r.Context())
	if err != nil {
		return out
	}
	prefix := meSettingPrefix + userID + "."
	for k, v := range all {
		if rest, ok := strings.CutPrefix(k, prefix); ok && meSettingKeys[rest] {
			out[rest] = v
		}
	}
	return out
}

// stripNamespacedSettings removes per-user "me.*" rows from an admin listing.
func stripNamespacedSettings(m map[string]string) {
	for k := range m {
		if strings.HasPrefix(k, meSettingPrefix) {
			delete(m, k)
		}
	}
}
