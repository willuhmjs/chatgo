// Package imggen proxies OpenAI-compatible image generation
// (POST /v1/images/generations) into ChatGo file rows
// (docs/FEATURES-V2.md §BE-SEC imggen).
//
// Routes (owned here, mounted by Register):
//
//	POST   /api/images/generate     {prompt, provider_id?, size?, n?} → {images:[…]}
//	GET    /api/images                                            → own newest 50
//	DELETE /api/images/{id}                                       → own image, 204
//
// RAM discipline: single 120s request context, 12 MiB upstream response cap
// streamed straight into the JSON decoder (the raw body is never buffered
// separately), and each b64 payload released as soon as its image is saved;
// no goroutines, no caches. Outbound dials go through the SSRF guard
// (search.ResolveAndCheck), mirroring internal/providers/client.go — the
// frozen Client interface has no images method and internal/providers is not
// ours to extend.
package imggen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"chatgo/internal/api/httpx"
	"chatgo/internal/auth"
	"chatgo/internal/config"
	"chatgo/internal/files"
	"chatgo/internal/search"
	"chatgo/internal/store"
)

const (
	// maxDecodedImage mirrors files.Service's 20 MiB per-file cap so results
	// stay servable through /files/{id}/{name}.
	maxDecodedImage = 20 << 20
	// maxPromptLen keeps request bodies proportionate (json body itself is
	// already capped at 1 MiB by CapJSONBody).
	maxPromptLen = 4000
	// maxN caps images per request.
	maxN = 4
	// generatePerMin bounds one user's generations (shared auth limiter).
	generatePerMin = 10

	dialTimeout        = 10 * time.Second
	tlsTimeout         = 15 * time.Second
	responseHeaderTO   = 15 * time.Second
	defaultSize        = "1024x1024"
	defaultResponseFmt = "b64_json"
)

// generateTimeout bounds the whole upstream exchange. vars so tests shrink.
var (
	generateTimeout  = 120 * time.Second
	listRecentLength = 50
	// maxUpstreamResponse caps the /v1/images/generations body. Math: real
	// 1024×1024 PNG results are 1.0–1.9 MiB (DALL·E 3, SDXL) → 1.4–2.6 MiB
	// b64 each (×4/3) → n=4 body ≈ 6–10.4 MiB + JSON framing, so 12 MiB
	// stays generous for 4×1024². Oversized answers (e.g. incompressible
	// 4 MiB PNGs → ~32 MiB body) get the clean 502 cap hint — via
	// Content-Length pre-check or the stream limit backstop — instead of an
	// OOMKill at the 128 MiB container limit. Measured with this cap (cold
	// app, GOMEMLIMIT=100MiB, n=4): realistic oversized answer (honest
	// Content-Length) rejects at ~19 MiB RSS; oversized with lying/absent
	// CL rejects at ~61 (≤cap strings + the in-flight token buffer is the
	// floor); a legit 11.8 MiB body peaks ~56 — vs 121 MiB at the old
	// 40 MiB cap. Var: tests shrink.
	maxUpstreamResponse = 12 << 20
)

// Deps wires the feature; integrator mounts after apimgr.Register:
//
//	imggen.Register(mux, &imggen.Deps{Cfg: cfg, Store: st, Auth: a, Files: filesSvc})
type Deps struct {
	Cfg   *config.Config
	Store *store.Store
	Auth  *auth.Auth
	Files *files.Service // results persist through Save (kind image)
}

type handler struct{ d *Deps }

// Register mounts the imggen surface on the shared mux.
func Register(mux *http.ServeMux, d *Deps) {
	h := &handler{d: d}
	mux.HandleFunc("POST /api/images/generate", h.handleGenerate)
	mux.HandleFunc("GET /api/images", h.handleList)
	mux.HandleFunc("DELETE /api/images/{id}", h.handleDelete)
}

// user resolves the session caller (AuthPolicy's Require already gated the
// routes; this is the handler-level defense-in-depth copy).
func (h *handler) user(w http.ResponseWriter, r *http.Request) (*store.User, bool) {
	u, ok := h.d.Auth.UserFrom(r.Context())
	if !ok {
		httpx.ErrorJSON(w, http.StatusUnauthorized, "authentication required")
		return nil, false
	}
	if u.Role == store.RolePending {
		httpx.ErrorJSON(w, http.StatusForbidden, "account pending approval")
		return nil, false
	}
	return u, true
}

// imageOut is one generated image in the API response (files.Meta-compatible
// plus urls served by the existing /files/{id}/{name} route). CreatedAt is
// unix seconds from the files row (house time convention: every JSON-facing
// created_at in the repo is an int64 unix stamp, cf. apichat.fileDTO).
type imageOut struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Mime      string `json:"mime"`
	Size      int64  `json:"size"`
	URL       string `json:"url"`
	CreatedAt int64  `json:"created_at"`
}

// ---------- POST /api/images/generate ----------

func (h *handler) handleGenerate(w http.ResponseWriter, r *http.Request) {
	u, ok := h.user(w, r)
	if !ok {
		return
	}
	if !h.d.Auth.Allow("imggen:"+u.ID, generatePerMin) {
		httpx.ErrorJSON(w, http.StatusTooManyRequests, "too many image requests, try again later")
		return
	}
	var body struct {
		Prompt     string          `json:"prompt"`
		ProviderID string          `json:"provider_id"`
		Size       json.RawMessage `json:"size"`
		N          int             `json:"n"`
	}
	if err := httpx.Decode(r, &body); err != nil {
		httpx.ErrorJSON(w, http.StatusBadRequest, "invalid request body")
		return
	}
	prompt := strings.TrimSpace(body.Prompt)
	if prompt == "" || len(prompt) > maxPromptLen {
		httpx.ErrorJSON(w, http.StatusBadRequest, fmt.Sprintf("prompt must be 1-%d characters", maxPromptLen))
		return
	}
	size, sizeOK := normalizeSize(body.Size) // default "1024x1024"; accepts "WxH"/"auto"/512 (FE sends plain numbers)
	if !sizeOK {
		httpx.ErrorJSON(w, http.StatusBadRequest, `size must look like "1024x1024" or "auto"`)
		return
	}
	n := body.N
	if n == 0 {
		n = 1
	}
	if n < 1 || n > maxN {
		httpx.ErrorJSON(w, http.StatusBadRequest, "n must be between 1 and 4")
		return
	}

	if h.d.Cfg == nil {
		// SECREV-V2 L4-L1: a nil-Cfg server must refuse image work at the
		// boundary rather than dial out through an unguarded transport.
		slog.Error("imggen: generate requested with nil config — refusing (SSRF guard unavailable)")
		httpx.ErrorJSON(w, http.StatusServiceUnavailable, "image generation is not configured on this server")
		return
	}

	p, model, errMsg, errStatus := h.pickProvider(r.Context(), body.ProviderID)
	if errMsg != "" {
		httpx.ErrorJSON(w, errStatus, errMsg)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), generateTimeout)
	defer cancel()

	dataURLs, err := h.requestImages(ctx, p, model, prompt, size, n)
	if err != nil {
		var ue *upstreamError
		if errors.As(err, &ue) {
			httpx.ErrorJSON(w, http.StatusBadGateway, ue.SafeMessage())
			return
		}
		slog.Error("imggen: generation failed", "user_id", u.ID, "provider_id", p.ID, "err", err)
		httpx.ErrorJSON(w, http.StatusBadGateway, "image generation failed")
		return
	}

	out := make([]imageOut, 0, len(dataURLs))
	for i := range dataURLs {
		b64 := dataURLs[i]
		// RAM discipline: the slice would otherwise keep ALL n base64 strings
		// alive across the whole save loop; dropping each entry once saved
		// means the next image's decode/multipart/thumbnail copies only
		// overlap the payloads still to come.
		dataURLs[i] = ""
		if i+1 < len(dataURLs) {
			// Deterministically bank the freed payload before the next
			// image's decode/multipart/thumbnail churn starts growing spans
			// (keeps the heap goal pinned at the low-live point).
			runtime.GC()
		}
		meta, err := h.saveImage(ctx, u.ID, i, b64)
		if err != nil {
			slog.Error("imggen: store failed", "user_id", u.ID, "err", err)
			httpx.ErrorJSON(w, http.StatusBadGateway, "could not store generated image")
			return
		}
		out = append(out, *meta)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"images": out})
}

// ---------- provider picking ----------

// imageModelHints match cached model ids when no capability flags exist
// (models table has none — CONTRACT-DEVIATION: heuristic name match instead
// of a flag; explicit provider_id always wins).
var imageModelHints = []string{"image", "dall", "imagen", "flux", "sd3", "stable-diffusion"}

func isImageModel(modelID string) bool {
	m := strings.ToLower(modelID)
	for _, h := range imageModelHints {
		if strings.Contains(m, h) {
			return true
		}
	}
	return false
}

// errMsgKeyUnreadable mirrors the chat send-path sentinel (SECREV4 M3b): a
// row whose sealed key won't decrypt would otherwise dial the upstream
// WITHOUT an Authorization header, and the provider's 401 would then be
// blamed on the admin's API key ("check this provider's API key") when the
// real fault is our unreadable seal. Refuse before any dial instead.
const errMsgKeyUnreadable = "the stored provider key can't be decrypted — the server secret changed after it was saved; re-enter the API key in Admin → Providers"

// pickProvider resolves the provider (+ best model id, "" = let the
// upstream default). Returns (client-safe message, HTTP status) on failure.
func (h *handler) pickProvider(ctx context.Context, explicitID string) (*store.Provider, string, string, int) {
	if explicitID != "" {
		p, err := h.d.Store.Providers.Get(ctx, explicitID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil, "", "provider not found", http.StatusNotFound
			}
			return nil, "", "could not load provider", http.StatusInternalServerError
		}
		if !p.Enabled {
			return nil, "", "provider is disabled", http.StatusBadRequest
		}
		if p.KeyUnreadable {
			return nil, "", errMsgKeyUnreadable, http.StatusServiceUnavailable
		}
		return p, h.bestImageModelFor(ctx, p.ID), "", 0
	}

	// No capability flags exist on models: scan the enabled cache for the
	// first image-shaped model id, else give a helpful 400 (task spec).
	ms, err := h.d.Store.Models.EnabledModels(ctx)
	if err != nil {
		return nil, "", "could not list models", http.StatusInternalServerError
	}
	for _, m := range ms {
		if !isImageModel(m.ModelID) {
			continue
		}
		p, err := h.d.Store.Providers.Get(ctx, m.ProviderID)
		if err != nil || !p.Enabled || p.KeyUnreadable {
			continue // unreadable seals are skipped, never dialed empty (M3b)
		}
		return p, m.ModelID, "", 0
	}
	return nil, "", "no image-capable provider found — pass provider_id explicitly (see GET /api/models for ids)", http.StatusBadRequest
}

// bestImageModelFor picks a cached image model of one provider ("" when the
// cache knows none — the request then omits "model" and the upstream picks).
func (h *handler) bestImageModelFor(ctx context.Context, providerID string) string {
	ms, err := h.d.Store.Models.ForProvider(ctx, providerID)
	if err != nil {
		return ""
	}
	for _, m := range ms {
		if m.Enabled && isImageModel(m.ModelID) {
			return m.ModelID
		}
	}
	return ""
}

// ---------- upstream call ----------

// upstreamError carries an upstream HTTP status with a client-safe message
// (the upstream body is never echoed — it can carry keys/internal URLs,
// mirroring providers.UpstreamError). Msg is an optional override for
// transport-level rejections (response cap hit) that have no upstream status.
type upstreamError struct {
	Status int
	Msg    string
}

func (e *upstreamError) Error() string {
	if e.Msg != "" {
		return "imggen: " + e.Msg
	}
	return fmt.Sprintf("imggen: upstream returned HTTP %d", e.Status)
}

// capMessage is the client-safe text for a response that tripped
// maxUpstreamResponse; it names the actionable knobs (size, n).
func capMessage() string {
	size, unit := maxUpstreamResponse>>20, "MiB"
	if maxUpstreamResponse < 1<<20 {
		size, unit = maxUpstreamResponse>>10, "KiB" // shrunk cap in tests
	}
	return fmt.Sprintf("image provider sent too large a response (over the %d %s cap) — try a smaller size or n=1", size, unit)
}

// SafeMessage maps the upstream status to an English one-liner + our 502.
func (e *upstreamError) SafeMessage() string {
	switch {
	case e.Msg != "":
		return e.Msg
	case e.Status == 401 || e.Status == 403:
		return fmt.Sprintf("image provider rejected the credentials (upstream HTTP %d) — check this provider's API key", e.Status)
	case e.Status == 404:
		return fmt.Sprintf("image endpoint not found on provider (upstream HTTP %d) — does this provider support /images/generations?", e.Status)
	case e.Status == 429:
		return "image provider is rate limiting requests (upstream HTTP 429) — try again in a moment"
	case e.Status >= 500:
		return fmt.Sprintf("image provider is having server trouble (upstream HTTP %d)", e.Status)
	default:
		return fmt.Sprintf("image provider returned an unexpected status (upstream HTTP %d)", e.Status)
	}
}

// generationRequest is the OpenAI /v1/images/generations body subset we send.
type generationRequest struct {
	Model          string `json:"model,omitempty"`
	Prompt         string `json:"prompt"`
	N              int    `json:"n"`
	Size           string `json:"size,omitempty"`
	ResponseFormat string `json:"response_format"`
}

type generationResponse struct {
	Data []struct {
		B64JSON string `json:"b64_json"`
		URL     string `json:"url"`
	} `json:"data"`
}

// countingReader tallies bytes pulled through it (cap-truncation detection
// for the streaming decode in requestImages).
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// requestImages POSTs the generation request and returns b64 payloads.
func (h *handler) requestImages(ctx context.Context, p *store.Provider, model, prompt, size string, n int) ([]string, error) {
	endpoint, err := endpointURL(p.BaseURL, "images/generations")
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(generationRequest{
		Model: model, Prompt: prompt, N: n, Size: size, ResponseFormat: defaultResponseFmt,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(payload)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if p.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.APIKey)
	}

	hc, err := guardedClient(ctx, endpoint, h.d.Cfg)
	if err != nil {
		return nil, err
	}
	defer hc.CloseIdleConnections()

	resp, err := hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("imggen: %w (timeout after %s)", ctx.Err(), generateTimeout)
		}
		return nil, fmt.Errorf("imggen: request to provider failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10)) // drain for keep-alive; never echoed
		return nil, &upstreamError{Status: resp.StatusCode}
	}

	// Up-front rejection when the upstream volunteers an oversized length —
	// not a single byte of a 40 MiB answer is pulled. Lying/absent
	// Content-Length (chunked) is covered by the byte limit on the stream
	// below.
	if resp.ContentLength > int64(maxUpstreamResponse) {
		return nil, &upstreamError{Msg: capMessage()}
	}

	// Stream the capped body straight into the JSON decoder: buffering the
	// raw bytes first and json.Unmarshal-ing a SECOND full copy of the b64
	// strings doubled peak RSS (n=4 at cap meant ~2× the body coexisting).
	// countingReader tells a cap-truncation failure apart from a genuine
	// parse error.
	limited := io.LimitReader(resp.Body, int64(maxUpstreamResponse)+1)
	counter := &countingReader{r: limited}
	var parsed generationResponse
	decodeErr := json.NewDecoder(counter).Decode(&parsed)
	if counter.n > int64(maxUpstreamResponse) {
		return nil, &upstreamError{Msg: capMessage()}
	}
	if decodeErr != nil {
		return nil, errors.New("imggen: could not parse provider image response")
	}
	var out []string
	for _, d := range parsed.Data {
		if strings.TrimSpace(d.B64JSON) == "" {
			continue // url-only results are not fetched (SSRF surface we do not need)
		}
		out = append(out, d.B64JSON)
	}
	if len(out) == 0 {
		return nil, errors.New("imggen: provider returned no b64_json images")
	}
	return out, nil
}

// endpointURL joins base ("https://api.example/v1") with a path segment
// (same rules as the providers client, which keeps its copy unexported).
func endpointURL(base, path string) (string, error) {
	b := strings.TrimRight(strings.TrimSpace(base), "/")
	if b == "" {
		return "", errors.New("imggen: provider base URL is empty")
	}
	u, err := url.Parse(b)
	if err != nil {
		return "", fmt.Errorf("imggen: invalid provider base URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("imggen: provider base URL must be http(s), got %q", u.Scheme)
	}
	return b + "/" + path, nil
}

// hopDialer swaps the validated dialer per redirect hop (re-validated, max
// 3 hops) — the providers client's SSRF pattern, kept local because
// internal/providers is not ours to edit.
type hopDialer struct {
	mu sync.Mutex
	fn func(ctx context.Context, network, addr string) (net.Conn, error)
}

func (hd *hopDialer) set(fn func(ctx context.Context, network, addr string) (net.Conn, error)) {
	hd.mu.Lock()
	hd.fn = fn
	hd.mu.Unlock()
}

func (hd *hopDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	hd.mu.Lock()
	fn := hd.fn
	hd.mu.Unlock()
	if fn == nil {
		return nil, errors.New("imggen: no validated dialer for this hop")
	}
	ctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	return fn(ctx, network, addr)
}

// guardedClient builds the per-request client: with a config every dial
// (initial + redirects) goes through search.ResolveAndCheck. A nil cfg is
// REFUSED outright (SECREV-V2 L4-L1): unlike providers.NewClient's
// documented test-only escape hatch, no imggen test or production path may
// dial unguarded — internal tests dial loopback through a real config with
// AllowLoopback instead.
func guardedClient(ctx context.Context, rawURL string, cfg *config.Config) (*http.Client, error) {
	base := http.Transport{
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   tlsTimeout,
		ResponseHeaderTimeout: responseHeaderTO,
		ExpectContinueTimeout: 5 * time.Second,
		MaxIdleConns:          2,
	}
	if cfg == nil {
		return nil, errors.New("imggen: refusing nil-Cfg dial (SSRF guard unavailable)")
	}
	dial, err := search.ResolveAndCheck(ctx, rawURL, cfg)
	if err != nil {
		return nil, fmt.Errorf("imggen: url blocked: %w", err)
	}
	hop := &hopDialer{}
	hop.set(dial)
	base.DialContext = hop.DialContext
	return &http.Client{
		Transport: &base,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("imggen: too many redirects")
			}
			next, err := search.ResolveAndCheck(req.Context(), req.URL.String(), cfg)
			if err != nil {
				return fmt.Errorf("imggen: redirect blocked: %w", err)
			}
			hop.set(next)
			return nil
		},
	}, nil
}

// validSize accepts OpenAI's WxH forms (and "auto"); the upstream validates
// the actual supported set.
func validSize(s string) bool {
	if s == "auto" {
		return true
	}
	w, hgt, ok := strings.Cut(s, "x")
	if !ok || w == "" || hgt == "" {
		return false
	}
	return allDigits(w) && allDigits(hgt) && len(w) <= 5 && len(hgt) <= 5
}

// normalizeSize accepts the wire shapes seen in the wild: absent → default,
// "1024x1024"/"auto" strings, or a bare number (web/src/lib/pages-api.ts
// sends size: 512 → "512x512"). Returns (size, ok).
func normalizeSize(raw json.RawMessage) (string, bool) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return defaultSize, true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		s = strings.TrimSpace(s)
		if s == "" {
			return defaultSize, true
		}
		return s, validSize(s)
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil && f > 0 && f == float64(int(f)) && f <= 99999 {
		s := strconv.Itoa(int(f))
		return s + "x" + s, true
	}
	return "", false
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
