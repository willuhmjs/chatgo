package kb

// Embeddings client + model selection.
//
// MODEL SELECTION DECISION (FEATURES-V2 §BE-RAG): the scaffold's models
// cache (store.ModelRow) carries NO capability column and internal/providers
// is frozen, so there is nothing synced to advertise "embeddings". kb
// therefore detects embedding capability by NAME over the synced cache and
// adds one admin override. In priority order:
//
//  1. settings key "kb_embed_model" == "providerID:modelID" → use as-is
//     (escape hatch for providers whose embed model id lacks "embed").
//  2. the task_model's provider (settings "task_model", else cfg.TaskModel):
//     its first enabled cached model whose id OR display contains "embed"
//     (case-insensitive).
//  3. otherwise the first enabled provider (store List order: sort,name)
//     with such a cached model.
//
// If nothing matches, ErrNoEmbedModel carries a toast-worthy message naming
// exactly what to configure. This requires no providers/apimgr edits and
// rides the existing POST /api/providers/{id}/sync flow.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"chatgo/internal/search"
	"chatgo/internal/store"
)

// embedTarget is a resolved provider+model pair (provider row holds a
// decrypted key — never log it).
type embedTarget struct {
	prov  store.Provider
	model string
}

// isEmbedModelID is the name heuristic over synced models.
func isEmbedModelID(id, display string) bool {
	return strings.Contains(strings.ToLower(id), "embed") ||
		strings.Contains(strings.ToLower(display), "embed")
}

// findEmbedModel returns the first enabled cached model of providerID whose
// id or display hints "embed" ("" when none).
func (d *Deps) findEmbedModel(ctx context.Context, providerID string) string {
	models, err := d.Store.Models.ForProvider(ctx, providerID)
	if err != nil {
		return ""
	}
	for _, m := range models {
		if m.Enabled && isEmbedModelID(m.ModelID, m.Display) {
			return m.ModelID
		}
	}
	return ""
}

// pickEmbedModel resolves the embeddings target per the priority order
// documented at the top of this file.
func (d *Deps) pickEmbedModel(ctx context.Context) (*embedTarget, error) {
	// 1. explicit admin override. An override that points at a provider that
	// is gone/disabled must not dial: fall through like the pre-fix `if
	// err == nil && row.Enabled` did, but a stored key the server can no
	// longer decrypt (rotated SESSION_SECRET) answers honestly — the vector
	// space pins the model, so there is nothing to fall back to.
	if v, err := d.Store.Settings.Get(ctx, settingEmbedModel); err == nil && v != nil {
		if provID, modelID, ok := splitSpec(strings.TrimSpace(*v)); ok {
			row, err := d.Store.Providers.Get(ctx, provID)
			if err == nil {
				if row.KeyUnreadable {
					return nil, httpErr(http.StatusServiceUnavailable,
						"the kb embed model's stored provider key can't be decrypted — the server secret changed after it was saved; re-enter the API key in Admin → Providers")
				}
				if row.Enabled {
					return &embedTarget{prov: *row, model: modelID}, nil
				}
			}
		}
	}
	// 2. task_model's provider (rows whose key is unreadable are skipped like
	// disabled ones — an empty bearer is never better than no provider).
	spec := ""
	if v, err := d.Store.Settings.Get(ctx, settingTaskModel); err == nil && v != nil {
		spec = strings.TrimSpace(*v)
	}
	if spec == "" && d.Cfg != nil {
		spec = strings.TrimSpace(d.Cfg.TaskModel)
	}
	if provID, _, ok := splitSpec(spec); ok {
		if row, err := d.Store.Providers.Get(ctx, provID); err == nil && row.Enabled && !row.KeyUnreadable {
			if m := d.findEmbedModel(ctx, row.ID); m != "" {
				return &embedTarget{prov: *row, model: m}, nil
			}
		}
	}
	// 3. first enabled provider with an embed-named cached model.
	provs, err := d.Store.Providers.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, row := range provs {
		if !row.Enabled || row.KeyUnreadable {
			continue
		}
		if m := d.findEmbedModel(ctx, row.ID); m != "" {
			return &embedTarget{prov: *row, model: m}, nil
		}
	}
	return nil, ErrNoEmbedModel
}

// splitSpec parses "providerID:modelID" (model IDs may contain ':').
func splitSpec(spec string) (providerID, modelID string, ok bool) {
	i := strings.IndexByte(spec, ':')
	if i <= 0 || i == len(spec)-1 {
		return "", "", false
	}
	return spec[:i], spec[i+1:], true
}

// embedTexts embeds texts in request-scoped batches of ≤embedBatchMax
// (FEATURES-V2: zero cross-request caching). All transport/status failures
// wrap ErrEmbedUpstream.
func (d *Deps) embedTexts(ctx context.Context, t *embedTarget, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	var out [][]float32
	var dim int
	for start := 0; start < len(texts); start += embedBatchMax {
		end := min(start+embedBatchMax, len(texts))
		vecs, err := d.embedBatch(ctx, t, texts[start:end])
		if err != nil {
			return nil, err
		}
		if dim == 0 {
			dim = len(vecs[0])
		}
		for _, v := range vecs {
			if len(v) != dim {
				return nil, fmt.Errorf("%w: inconsistent embedding dimensions", ErrEmbedUpstream)
			}
			out = append(out, v)
		}
	}
	return out, nil
}

// embedBatch performs ONE POST {base}/embeddings exchange. The connection
// goes through the shared SSRF guard exactly like the providers client.
func (d *Deps) embedBatch(ctx context.Context, t *embedTarget, batch []string) ([][]float32, error) {
	if d.Cfg == nil {
		return nil, fmt.Errorf("%w: kb has no config (SSRF guard unavailable)", ErrEmbedUpstream)
	}
	endpoint, err := embeddingsEndpoint(t.prov.BaseURL)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, embedTimeout)
	defer cancel()

	body, err := json.Marshal(map[string]any{"model": t.model, "input": batch})
	if err != nil {
		return nil, fmt.Errorf("%w: encode request: %v", ErrEmbedUpstream, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%w: build request: %v", ErrEmbedUpstream, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if t.prov.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+t.prov.APIKey)
	}

	hc, err := guardedHTTPClient(ctx, d, endpoint)
	if err != nil {
		return nil, err
	}
	defer hc.CloseIdleConnections()

	resp, err := hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%w: %v", ErrEmbedUpstream, ctx.Err())
		}
		return nil, fmt.Errorf("%w: request: %v", ErrEmbedUpstream, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, embedBodyMax+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read response: %v", ErrEmbedUpstream, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// Never echo the upstream body (may embed keys/URLs).
		return nil, fmt.Errorf("%w: status %s", ErrEmbedUpstream, resp.Status)
	}
	if len(raw) > embedBodyMax {
		return nil, fmt.Errorf("%w: response too large", ErrEmbedUpstream)
	}
	return parseEmbeddingsResponse(raw, len(batch))
}

// parseEmbeddingsResponse maps the OpenAI {"data":[{index,embedding}]} shape
// into ordered vectors. Pure function.
func parseEmbeddingsResponse(raw []byte, want int) ([][]float32, error) {
	var payload struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("%w: malformed response: %v", ErrEmbedUpstream, err)
	}
	if len(payload.Data) != want {
		return nil, fmt.Errorf("%w: got %d embeddings for %d inputs", ErrEmbedUpstream, len(payload.Data), want)
	}
	out := make([][]float32, want)
	seen := make([]bool, want)
	for _, item := range payload.Data {
		if item.Index < 0 || item.Index >= want || seen[item.Index] {
			return nil, fmt.Errorf("%w: bad embedding index", ErrEmbedUpstream)
		}
		if len(item.Embedding) == 0 {
			return nil, fmt.Errorf("%w: empty embedding vector", ErrEmbedUpstream)
		}
		out[item.Index] = item.Embedding
		seen[item.Index] = true
	}
	return out, nil
}

// embeddingsEndpoint joins base (typically "…/v1") with "embeddings".
func embeddingsEndpoint(base string) (string, error) {
	b := strings.TrimRight(strings.TrimSpace(base), "/")
	if b == "" {
		return "", fmt.Errorf("%w: provider base URL is empty", ErrEmbedUpstream)
	}
	u, err := url.Parse(b)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("%w: provider base URL must be http(s)", ErrEmbedUpstream)
	}
	return b + "/embeddings", nil
}

// hopDialer holds the guard-validated dialer for the current redirect hop
// (mirrors internal/providers' frozen pattern; every hop is re-validated).
type hopDialer struct {
	mu sync.Mutex
	fn func(ctx context.Context, network, addr string) (net.Conn, error)
}

func (h *hopDialer) set(fn func(ctx context.Context, network, addr string) (net.Conn, error)) {
	h.mu.Lock()
	h.fn = fn
	h.mu.Unlock()
}

func (h *hopDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	h.mu.Lock()
	fn := h.fn
	h.mu.Unlock()
	if fn == nil {
		return nil, errors.New("kb: no validated dialer for this hop")
	}
	return fn(ctx, network, addr)
}

// guardedHTTPClient builds a per-request client whose every dial (including
// redirect hops, max 3) is pre-resolved and policy-checked by
// search.ResolveAndCheck.
func guardedHTTPClient(ctx context.Context, d *Deps, rawURL string) (*http.Client, error) {
	dial, err := search.ResolveAndCheck(ctx, rawURL, d.Cfg)
	if err != nil {
		return nil, fmt.Errorf("%w: url blocked: %v", ErrEmbedUpstream, err)
	}
	hop := &hopDialer{}
	hop.set(dial)
	tr := &http.Transport{
		DialContext:           hop.DialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 45 * time.Second,
		MaxIdleConns:          2,
	}
	return &http.Client{
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("kb: too many redirects")
			}
			next, err := search.ResolveAndCheck(req.Context(), req.URL.String(), d.Cfg)
			if err != nil {
				return fmt.Errorf("kb: redirect blocked: %w", err)
			}
			hop.set(next)
			return nil
		},
	}, nil
}
