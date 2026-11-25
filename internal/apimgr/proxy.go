package apimgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"chatgo/internal/api/httpx"
	"chatgo/internal/providers"
	"chatgo/internal/store"
)

// POST /v1/chat/completions — OpenAI-compatible passthrough bound to a ck_…
// api token (or, as a convenience, a logged-in session when the policy layer
// attaches one). No billing; serve and forward.
//
// CONTRACT-DEVIATION (policy, reported to integrator): api/server.go
// AuthPolicy sends /v1/* through a.Require (SESSION auth), which 401s Bearer
// ck_ tokens before they can reach this handler, and CapJSONBody caps every
// JSON body at httpx.MaxJSONBody (1MiB) before handlers run. server.go must:
// (1) route /v1/* through a.Attach (optional) — this handler enforces Bearer
// itself and falls back to the attached session user; (2) give /v1 a 4MB body
// cap (or exempt it; the cap below is the handler-side second layer).

// maxProxyBody caps /v1 request bodies (task brief: 4MB — longer chat
// histories than the 1MB generic JSON cap).
const maxProxyBody = 4 << 20

// samplingParamAllowlist: OpenAI keys forwarded into StreamOpts.Params.
// Anything else (user, n, tool_calls, …) is dropped (v1 passthrough scope).
var samplingParamAllowlist = map[string]bool{
	"temperature": true, "top_p": true, "max_tokens": true,
	"max_completion_tokens": true, "stop": true,
	"presence_penalty": true, "frequency_penalty": true, "seed": true,
}

type proxyH struct{ d *Deps }

func registerProxy(mux *http.ServeMux, d *Deps) {
	h := &proxyH{d: d}
	mux.HandleFunc("POST /v1/chat/completions", h.handle)
	mux.HandleFunc("GET /v1/models", h.handleModels)
}

// ---------- OpenAI wire shapes ----------

type openAIRequest struct {
	Model    string          `json:"model"`
	Messages []openAIMessage `json:"messages"`
	Stream   bool            `json:"stream"`
}

type openAIMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type openAIErrorBody struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
}

func openAIError(w http.ResponseWriter, status int, msg, typ, code string) {
	httpx.JSON(w, status, map[string]any{"error": openAIErrorBody{Message: msg, Type: typ, Code: code}})
}

// proxyError is an internal resolution/auth failure carrying the HTTP status
// and OpenAI envelope type/code the handler must emit (so resolveModel can
// distinguish 401/403/404/500 instead of one flat 400).
type proxyError struct {
	status int
	msg    string
	typ    string
	code   string
}

func (e *proxyError) Error() string { return e.msg }

type chatChoice struct {
	Index        int         `json:"index"`
	Message      chatMsgRole `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

type chatMsgRole struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatCompletion struct {
	ID      string       `json:"id"`
	Object  string       `json:"object"`
	Created int64        `json:"created"`
	Model   string       `json:"model"`
	Choices []chatChoice `json:"choices"`
	Usage   chatUsage    `json:"usage"`
}

type chatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Stream chunks (object chat.completion.chunk).
type streamChunk struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []streamChoice `json:"choices"`
	Usage   *chatUsage     `json:"usage,omitempty"`
}

type streamChoice struct {
	Index        int         `json:"index"`
	Delta        streamDelta `json:"delta"`
	FinishReason *string     `json:"finish_reason"`
}

type streamDelta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

// ---------- handler ----------

func (h *proxyH) handle(w http.ResponseWriter, r *http.Request) {
	// 1. Auth: Bearer ck_… api token (own check, task §5), else an attached
	// session user if the policy layer attached one.
	user, ok := h.authenticate(w, r)
	if !ok {
		return
	}

	// 2. Body ≤ 4MB (handler-side; see policy deviation note above).
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxProxyBody))
	if err != nil {
		var mb *http.MaxBytesError
		if errors.As(err, &mb) {
			openAIError(w, http.StatusRequestEntityTooLarge, "request body exceeds 4MB", "invalid_request_error", "context_length_exceeded")
			return
		}
		openAIError(w, http.StatusBadRequest, "could not read request body", "invalid_request_error", "")
		return
	}
	// Loose decode on purpose: OpenAI clients send fields we ignore (n, user,
	// tools…); rejecting unknown fields would break interop (the contract's
	// "reject unknown" rule targets our own DTOs, not the OpenAI wire).
	var req openAIRequest
	if err := json.Unmarshal(body, &req); err != nil {
		openAIError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error(), "invalid_request_error", "")
		return
	}
	if strings.TrimSpace(req.Model) == "" {
		openAIError(w, http.StatusBadRequest, "model is required", "invalid_request_error", "")
		return
	}
	if len(req.Messages) == 0 {
		openAIError(w, http.StatusBadRequest, "messages is required", "invalid_request_error", "")
		return
	}

	// 3. Model resolution: "prov:model" (provider id or name) or a model id
	// that is unique across enabled models.
	prov, modelID, err := h.resolveModel(r.Context(), req.Model, user.Role == store.RoleAdmin)
	if err != nil {
		var pe *proxyError
		if errors.As(err, &pe) {
			openAIError(w, pe.status, pe.msg, pe.typ, pe.code)
		} else {
			openAIError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "model_not_found")
		}
		return
	}

	// 4. Translate messages + sampling params.
	msgs, err := toChatMessages(req.Messages)
	if err != nil {
		openAIError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "")
		return
	}
	opts := providers.StreamOpts{
		ProviderID: prov.ID,
		ModelID:    modelID,
		Messages:   msgs,
		Params:     extractParams(body),
	}
	up := upstreamProvider(prov)

	id := "chatcmpl-" + uuid.NewString()
	created := time.Now().Unix()

	if req.Stream {
		h.serveStream(w, r, id, created, req.Model, up, opts)
		return
	}

	content, err := h.d.Providers.Complete(r.Context(), up, modelID, msgs)
	if err != nil {
		slog.Error("proxy: upstream complete failed", "user_id", user.ID, "provider_id", prov.ID, "err", err)
		openAIError(w, http.StatusBadGateway, "upstream completion failed", "server_error", "")
		return
	}
	httpx.JSON(w, http.StatusOK, chatCompletion{
		ID: id, Object: "chat.completion", Created: created, Model: req.Model,
		Choices: []chatChoice{{Index: 0,
			Message:      chatMsgRole{Role: "assistant", Content: content},
			FinishReason: "stop"}},
		// Complete has no usage channel (contract interface); zeros, not lies.
		Usage: chatUsage{},
	})
}

// GET /v1/models — OpenAI-shaped catalog of what THIS caller may call:
// enabled models of enabled providers, minus min_role='admin' rows for
// non-admins (same hiding rule as GET /api/models and resolveModel, so the
// list never advertises an id the same token would get 404 on). Ids are
// "ProviderName:model_id"; a bare model_id is listed additionally when it is
// unique among the caller's visible set (both forms resolve server-side).
func (h *proxyH) handleModels(w http.ResponseWriter, r *http.Request) {
	user, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	ms, err := h.d.Store.Models.EnabledModels(r.Context())
	if err != nil {
		slog.Error("proxy: /v1/models listing failed", "err", err)
		openAIError(w, http.StatusInternalServerError, "model listing failed", "server_error", "")
		return
	}
	admin := user.Role == store.RoleAdmin
	// Provider created_at for the `created` field (one cheap query).
	created := map[string]int64{}
	if provs, err := h.d.Store.Providers.List(r.Context()); err == nil {
		for _, p := range provs {
			created[p.ID] = p.CreatedAt
		}
	}
	// Bare-id uniqueness over the caller-visible subset.
	visible := make([]store.ModelWithProvider, 0, len(ms))
	bareCount := map[string]int{}
	for _, m := range ms {
		if !admin && m.MinRole == "admin" {
			continue
		}
		visible = append(visible, m)
		bareCount[m.ModelID]++
	}
	data := make([]map[string]any, 0, len(visible)*2)
	for _, m := range visible {
		data = append(data, map[string]any{
			"id": m.ProviderName + ":" + m.ModelID, "object": "model",
			"created": created[m.ProviderID], "owned_by": m.ProviderName,
		})
		if bareCount[m.ModelID] == 1 {
			data = append(data, map[string]any{
				"id": m.ModelID, "object": "model",
				"created": created[m.ProviderID], "owned_by": m.ProviderName,
			})
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

// authenticate resolves the caller (Bearer ck_ token owner, or an attached
// session user) and THEN applies the role gate: only activated accounts
// (user/admin) may consume providers.
func (h *proxyH) authenticate(w http.ResponseWriter, r *http.Request) (*store.User, bool) {
	user, ok := h.authenticateIdentity(w, r)
	if !ok {
		return nil, false
	}
	// Security (HIGH): the session-fallback path bypasses Require's pending
	// check, and HashLookup joins users without looking at role, so pending
	// or demoted accounts could previously spend provider quota. Both paths
	// carry the CURRENT user row (Attach/UserFrom re-read users; HashLookup
	// joins users live), so this gate covers session and token auth alike.
	if user.Role != store.RoleUser && user.Role != store.RoleAdmin {
		openAIError(w, http.StatusForbidden, "account not activated", "permission_error", "account_not_active")
		return nil, false
	}
	return user, true
}

// authenticateIdentity: "Authorization: Bearer ck_…" → sha256 → Tokens.HashLookup
// (also bumps last_used). A present-but-bad Bearer is a hard 401 (no silent
// session fallback). With no Authorization header, an attached session user
// (policy Attach) may serve. Role enforcement lives in authenticate.
func (h *proxyH) authenticateIdentity(w http.ResponseWriter, r *http.Request) (*store.User, bool) {
	raw := strings.TrimSpace(r.Header.Get("Authorization"))
	if raw == "" {
		if u, ok := h.d.Auth.UserFrom(r.Context()); ok {
			return u, true
		}
		openAIError(w, http.StatusUnauthorized,
			"missing api key (Authorization: Bearer ck_…)", "authentication_error", "invalid_api_key")
		return nil, false
	}
	token := raw // RFC 7235: scheme token is case-insensitive
	if len(raw) >= 7 && strings.EqualFold(raw[:7], "Bearer ") {
		token = strings.TrimSpace(raw[7:])
	}
	if !strings.HasPrefix(token, "ck_") {
		openAIError(w, http.StatusUnauthorized, "invalid api key", "authentication_error", "invalid_api_key")
		return nil, false
	}
	user, _, err := h.d.Store.Tokens.HashLookup(r.Context(), store.HashToken(token))
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			slog.Error("proxy: token lookup failed", "err", err)
			openAIError(w, http.StatusInternalServerError, "internal error", "server_error", "")
			return nil, false
		}
		openAIError(w, http.StatusUnauthorized, "invalid api key", "authentication_error", "invalid_api_key")
		return nil, false
	}
	return user, true
}

// resolveModel maps the requested model string to (provider, upstream model id).
// The explicit "prov:model" form must name an ENABLED provider AND the exact
// model id must sit in the admin models cache for that provider with
// enabled=1 — otherwise any "prov:whatever" string would reach upstream
// (hidden/never-listed models reachable, cost exposure). A bare model id must
// be unique among enabled models of enabled providers.
//
// admin mirrors the caller's role: models gated with min_role='admin' are
// invisible to everyone else — answered as plain "not found" (404), matching
// GET /api/models, which hides them outright rather than leaking existence.
func (h *proxyH) resolveModel(ctx context.Context, requested string, admin bool) (*store.Provider, string, error) {
	if provPart, modelID, ok := strings.Cut(requested, ":"); ok && provPart != "" && modelID != "" {
		p, err := h.lookupProvider(ctx, provPart)
		if err != nil {
			return nil, "", err
		}
		if err := h.checkCachedModel(ctx, p, modelID, admin); err != nil {
			return nil, "", err
		}
		return p, modelID, nil
	}
	if strings.Contains(requested, ":") { // "prov:" / ":model" — malformed
		return nil, "", fmt.Errorf("model %q must be \"provider:model\"", requested)
	}
	// Bare model id: must be unique among enabled models of enabled providers.
	ms, err := h.d.Store.Models.EnabledModels(ctx)
	if err != nil {
		return nil, "", &proxyError{http.StatusInternalServerError, "model lookup failed", "server_error", ""}
	}
	var hit *store.ModelWithProvider
	n := 0
	for i := range ms {
		if !admin && ms[i].MinRole == "admin" {
			continue // gated model: does not exist for this caller
		}
		if ms[i].ModelID == requested {
			n++
			hit = &ms[i]
		}
	}
	switch {
	case n == 0:
		// OpenAI answers 404 model_not_found for unknown models; match it.
		return nil, "", &proxyError{http.StatusNotFound,
			fmt.Sprintf("model %q not found", requested), "invalid_request_error", "model_not_found"}
	case n > 1:
		return nil, "", fmt.Errorf("model %q is ambiguous — use \"provider:%s\"", requested, requested)
	}
	p, err := h.lookupProvider(ctx, hit.ProviderID)
	if err != nil {
		return nil, "", err
	}
	return p, hit.ModelID, nil
}

// lookupProvider finds an ENABLED provider by id, then by name.
func (h *proxyH) lookupProvider(ctx context.Context, idOrName string) (*store.Provider, error) {
	if p, err := h.d.Store.Providers.Get(ctx, idOrName); err == nil {
		if !p.Enabled {
			return nil, disabledProviderError(idOrName)
		}
		if p.KeyUnreadable {
			return nil, unreadableKeyError(idOrName)
		}
		return p, nil
	}
	provs, err := h.d.Store.Providers.List(ctx)
	if err != nil {
		return nil, &proxyError{http.StatusInternalServerError, "provider lookup failed", "server_error", ""}
	}
	for _, p := range provs {
		if p.Name == idOrName || strings.EqualFold(p.Name, idOrName) {
			if !p.Enabled {
				return nil, disabledProviderError(idOrName)
			}
			if p.KeyUnreadable {
				return nil, unreadableKeyError(idOrName)
			}
			return p, nil
		}
	}
	return nil, &proxyError{http.StatusNotFound,
		fmt.Sprintf("model %q not found: unknown provider", idOrName), "invalid_request_error", "model_not_found"}
}

func disabledProviderError(idOrName string) error {
	return &proxyError{http.StatusForbidden,
		fmt.Sprintf("provider %q is disabled", idOrName), "permission_error", "model_not_available"}
}

// unreadableKeyError: the stored key won't decrypt (SESSION_SECRET rotated or
// the blob corrupted). Sending upstream with an empty key would surface as a
// confusing provider 401, so we answer honestly — this needs an admin to
// re-enter the key, retrying from the client cannot help.
func unreadableKeyError(idOrName string) error {
	return &proxyError{http.StatusServiceUnavailable,
		fmt.Sprintf("provider %q stored key can't be decrypted — an admin must re-enter it (the server secret changed)", idOrName),
		"server_error", "provider_key_unreadable"}
}

// checkCachedModel enforces the admin models cache for the explicit
// "provider:model" form: the exact model id must be cached for this provider
// (else 404 — never synced/listed), must be flagged enabled (else 403), and
// must not be min_role='admin' when the caller is not an admin (404 — hidden,
// not "forbidden", so the proxy never leaks a gated model's existence;
// mirrors GET /api/models hiding).
func (h *proxyH) checkCachedModel(ctx context.Context, p *store.Provider, modelID string, admin bool) error {
	ms, err := h.d.Store.Models.ForProvider(ctx, p.ID)
	if err != nil {
		slog.Error("proxy: model cache lookup failed", "provider_id", p.ID, "err", err)
		return &proxyError{http.StatusInternalServerError, "model lookup failed", "server_error", ""}
	}
	for _, m := range ms {
		if m.ModelID != modelID {
			continue
		}
		if !admin && m.MinRole == "admin" {
			return &proxyError{http.StatusNotFound,
				fmt.Sprintf("model %q not found for provider %q", modelID, p.Name), "invalid_request_error", "model_not_found"}
		}
		if !m.Enabled {
			return &proxyError{http.StatusForbidden, "model not available", "permission_error", "model_not_available"}
		}
		return nil
	}
	return &proxyError{http.StatusNotFound,
		fmt.Sprintf("model %q not found for provider %q", modelID, p.Name), "invalid_request_error", "model_not_found"}
}

// ---------- message / params translation ----------

// toChatMessages flattens OpenAI content (string | parts array) into the
// providers shape; data: URLs become Images.
func toChatMessages(in []openAIMessage) ([]providers.ChatMessage, error) {
	out := make([]providers.ChatMessage, 0, len(in))
	for i, m := range in {
		if strings.TrimSpace(m.Role) == "" {
			return nil, fmt.Errorf("messages[%d]: role is required", i)
		}
		text, images, err := flattenContent(m.Content)
		if err != nil {
			return nil, fmt.Errorf("messages[%d]: %w", i, err)
		}
		out = append(out, providers.ChatMessage{Role: m.Role, Content: text, Images: images})
	}
	return out, nil
}

func flattenContent(raw json.RawMessage) (string, []string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil, nil
	}
	var parts []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL *struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", nil, errors.New("content must be a string or a parts array")
	}
	var texts []string
	var images []string
	for _, p := range parts {
		switch p.Type {
		case "text":
			texts = append(texts, p.Text)
		case "image_url":
			if p.ImageURL != nil && p.ImageURL.URL != "" {
				images = append(images, p.ImageURL.URL)
			}
		default: // audio/file/etc: not in v1 passthrough scope
		}
	}
	return strings.Join(texts, "\n"), images, nil
}

// extractParams picks the allowlisted sampling params out of the raw body.
func extractParams(body []byte) map[string]any {
	var all map[string]any
	if err := json.Unmarshal(body, &all); err != nil {
		return nil
	}
	var out map[string]any
	for k, v := range all {
		if samplingParamAllowlist[k] {
			if out == nil {
				out = map[string]any{}
			}
			out[k] = v
		}
	}
	return out
}

// ---------- streaming (OpenAI SSE wire format) ----------

func (h *proxyH) serveStream(w http.ResponseWriter, r *http.Request, id string, created int64,
	model string, up providers.Provider, opts providers.StreamOpts) {

	// Start upstream BEFORE writing SSE headers so connect failures answer
	// plain JSON errors.
	ch, err := h.d.Providers.StreamChat(r.Context(), up, opts)
	if err != nil {
		slog.Error("proxy: upstream stream failed", "provider_id", up.ID, "err", err)
		openAIError(w, http.StatusBadGateway, "upstream stream failed", "server_error", "")
		return
	}
	f, ok := w.(http.Flusher)
	if !ok {
		openAIError(w, http.StatusInternalServerError, "streaming unsupported", "server_error", "")
		return
	}
	hdr := w.Header()
	hdr.Set("Content-Type", "text/event-stream")
	hdr.Set("Cache-Control", "no-cache")
	hdr.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	f.Flush()

	// OpenAI chunks carry NO event: field — raw "data: …" frames only, so
	// httpx.SSEWriter (named events) is not usable here.
	send := func(c streamChunk) bool {
		b, err := json.Marshal(c)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
			return false
		}
		f.Flush()
		return true
	}
	frame := func(d streamDelta, finish *string, usage *chatUsage) streamChunk {
		return streamChunk{ID: id, Object: "chat.completion.chunk", Created: created, Model: model,
			Choices: []streamChoice{{Index: 0, Delta: d, FinishReason: finish}}, Usage: usage}
	}

	// First frame announces the assistant role (OpenAI convention).
	if !send(frame(streamDelta{Role: "assistant"}, nil, nil)) {
		return
	}

	var usage *chatUsage
	finished := false
	for c := range ch {
		select {
		case <-r.Context().Done():
			return // client gone; upstream ctx cancel propagates (contract)
		default:
		}
		if c.Usage != nil {
			usage = &chatUsage{PromptTokens: c.Usage.In, CompletionTokens: c.Usage.Out,
				TotalTokens: c.Usage.In + c.Usage.Out}
		}
		if c.Content != "" && !send(frame(streamDelta{Content: c.Content}, nil, nil)) {
			return
		}
		if c.FinishReason != "" {
			fr := c.FinishReason
			finished = true
			if !send(frame(streamDelta{}, &fr, usage)) {
				return
			}
		}
	}
	// Channel closed without an explicit finish (clean EOF, or upstream died
	// mid-flight — Chunk carries no error signal): close the turn honestly.
	if !finished {
		send(frame(streamDelta{}, strPtr("stop"), usage))
	}
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	f.Flush()
}

func strPtr(s string) *string { return &s }
