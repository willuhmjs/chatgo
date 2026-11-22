// Package providers talks to OpenAI-compatible upstreams.
// Real implementation of the frozen docs/CONTRACTS.md §Providers client
// interface (client.go body, sse.go, models.go); the types and interface
// below are frozen and unchanged.
package providers

import (
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

	"chatgo/internal/config"
	"chatgo/internal/search"
)

// Model is one upstream model entry (cacheable into store.ModelRow).
type Model struct {
	ID         string
	Display    string
	ContextLen int // 0 = unknown
}

// Provider is a decrypted provider row (never log it).
type Provider struct {
	ID      string
	Name    string
	BaseURL string
	APIKey  string
}

// ChatMessage is one turn in an upstream request. Images are data: URLs.
type ChatMessage struct {
	Role    string // user|assistant|system
	Content string
	Images  []string // data:<mime>;base64,... (optional, vision models)
}

// StreamOpts is one upstream streaming request.
type StreamOpts struct {
	ProviderID string
	ModelID    string
	Messages   []ChatMessage
	Params     map[string]any // temperature, top_p, … passthrough
	SearchCtx  string         // extra system text; "" = none
}

// Usage reports token counts when the upstream sends them.
type Usage struct {
	In  int
	Out int
}

// Chunk is one streamed delta. FinishReason != "" marks the last Chunk.
type Chunk struct {
	Content      string
	FinishReason string
	Usage        *Usage
}

// Client is the frozen upstream interface used by internal/chat and apimgr.
type Client interface {
	ListModels(ctx context.Context, baseURL, apiKey string) ([]Model, error)
	StreamChat(ctx context.Context, p Provider, opts StreamOpts) (<-chan Chunk, error)
	Complete(ctx context.Context, p Provider, model string, msgs []ChatMessage) (string, error)
}

// NewClient returns the configured Client.
//
// When cfg is non-nil every outbound dial goes through search.ResolveAndCheck
// (the SSRF guard, contract §Providers client). cfg == nil skips the guard —
// CONTRACT-DEVIATION: that escape hatch is added for tests/local development
// only; production (cmd/chatgo) always passes a real *config.Config.
func NewClient(cfg *config.Config) Client {
	return &openAIClient{cfg: cfg}
}

const (
	dialTimeout       = 10 * time.Second
	tlsTimeout        = 15 * time.Second
	maxModelsBody     = 8 << 20  // 8 MiB
	maxCompletionBody = 32 << 20 // 32 MiB
	maxErrBody        = 64 << 10 // just enough to drain for keep-alive
	streamChanBuffer  = 32
)

type openAIClient struct {
	cfg *config.Config
	// streamDone, when non-nil, is called when a StreamChat pump goroutine
	// exits. Test hook for goroutine-leak checks; nil in production.
	streamDone func()
}

var _ Client = (*openAIClient)(nil)

// errStreamHalt ends the pump because ctx was cancelled.
var errStreamHalt = errors.New("providers: stream cancelled")

// errStreamDone ends the pump cleanly on the [DONE] sentinel.
var errStreamDone = errors.New("providers: stream done")

// errStreamUpstream ends the pump on a mid-stream provider error frame. The
// error text is not surfaced because Chunk (frozen) has no error field; the
// consumer sees the channel close instead.

func (c *openAIClient) ListModels(ctx context.Context, baseURL, apiKey string) ([]Model, error) {
	endpoint, err := endpointURL(baseURL, "models")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	authorize(req, apiKey)

	resp, hc, err := c.do(ctx, req)
	if err != nil {
		return nil, err
	}
	defer hc.CloseIdleConnections()
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxModelsBody+1))
	if err != nil {
		return nil, fmt.Errorf("providers: reading model list: %w", err)
	}
	if len(body) > maxModelsBody {
		return nil, errors.New("providers: model list response too large")
	}
	return parseModelList(body)
}

func (c *openAIClient) StreamChat(ctx context.Context, p Provider, opts StreamOpts) (<-chan Chunk, error) {
	if opts.ModelID == "" {
		return nil, errors.New("providers: model id is required")
	}
	endpoint, err := endpointURL(p.BaseURL, "chat/completions")
	if err != nil {
		return nil, err
	}
	body, err := buildBody(opts.ModelID, opts.Messages, opts.SearchCtx, true, opts.Params)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	authorize(req, p.APIKey)

	resp, hc, err := c.do(ctx, req)
	if err != nil {
		return nil, err
	}
	out := make(chan Chunk, streamChanBuffer)
	go c.pumpStream(ctx, resp, hc, out)
	return out, nil
}

func (c *openAIClient) Complete(ctx context.Context, p Provider, model string, msgs []ChatMessage) (string, error) {
	if model == "" {
		return "", errors.New("providers: model id is required")
	}
	endpoint, err := endpointURL(p.BaseURL, "chat/completions")
	if err != nil {
		return "", err
	}
	body, err := buildBody(model, msgs, "", false, nil)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	authorize(req, p.APIKey)

	resp, hc, err := c.do(ctx, req)
	if err != nil {
		return "", err
	}
	defer hc.CloseIdleConnections()
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxCompletionBody+1))
	if err != nil {
		return "", fmt.Errorf("providers: reading completion: %w", err)
	}
	if len(raw) > maxCompletionBody {
		return "", errors.New("providers: completion response too large")
	}
	var full fullCompletion
	if err := json.Unmarshal(raw, &full); err != nil {
		return "", errors.New("providers: could not parse provider completion response")
	}
	if len(full.Choices) == 0 {
		return "", errors.New("providers: provider returned no completion choices")
	}
	return full.Choices[0].Message.Content, nil
}

// do builds the (guarded) per-request client, sends req and handles non-2xx.
// On success the caller owns resp and hc (CloseIdleConnections when done).
func (c *openAIClient) do(ctx context.Context, req *http.Request) (*http.Response, *http.Client, error) {
	hc, err := c.httpClientFor(ctx, req.URL.String())
	if err != nil {
		return nil, nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		hc.CloseIdleConnections()
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return nil, nil, fmt.Errorf("providers: request to provider failed: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// Drain a bounded prefix so the conn can be reused, but NEVER include
		// the body in the error (it can leak secrets).
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrBody))
		resp.Body.Close()
		hc.CloseIdleConnections()
		return nil, nil, upstreamErrorFor(resp.StatusCode)
	}
	return resp, hc, nil
}

// pumpStream parses the SSE body into out and ALWAYS closes out. ctx
// cancellation closes the response body (unblocking the read) and stops the
// goroutine — no leaks. Mid-stream upstream failures surface as an early
// channel close (Chunk has no error field; frozen).
func (c *openAIClient) pumpStream(ctx context.Context, resp *http.Response, hc *http.Client, out chan<- Chunk) {
	defer close(out)
	defer resp.Body.Close()
	defer hc.CloseIdleConnections()
	if c.streamDone != nil {
		defer c.streamDone()
	}

	// Unblock the blocking body Read when ctx is cancelled.
	stopWatch := context.AfterFunc(ctx, func() { _ = resp.Body.Close() })
	defer stopWatch()

	send := func(ch Chunk) error {
		select {
		case out <- ch:
			return nil
		case <-ctx.Done():
			return errStreamHalt
		}
	}

	// Tolerate gateways that ignore stream:true and answer with one JSON body.
	if ct := resp.Header.Get("Content-Type"); strings.Contains(ct, "application/json") && !strings.Contains(ct, "event-stream") {
		c.pumpJSONFallback(resp, send)
		return
	}

	err := parseSSE(resp.Body, func(ev sseEvent) error {
		if ev.Event == "error" { // provider-sent error frame; stop cleanly
			return errStreamUpstreamEnd
		}
		data := strings.TrimSpace(ev.Data)
		if data == "" {
			return nil
		}
		if data == sseDone {
			return errStreamDone
		}
		var raw chunkRaw
		if json.Unmarshal([]byte(data), &raw) != nil {
			return nil // malformed chunk → skip, keep streaming
		}
		if raw.Error != nil {
			return errStreamUpstreamEnd
		}
		var usage *Usage
		if raw.Usage != nil {
			usage = &Usage{In: raw.Usage.PromptTokens, Out: raw.Usage.CompletionTokens}
		}
		for _, ch := range raw.Choices {
			if ch.Index != 0 { // first choice only
				continue
			}
			emit := Chunk{Content: ch.Delta.Content, FinishReason: ch.FinishReason, Usage: usage}
			usage = nil // don't double-report usage across choices
			if emit.Content == "" && emit.FinishReason == "" && emit.Usage == nil {
				continue // pure role-delta / heartbeat chunks
			}
			return send(emit)
		}
		if usage != nil { // usage-only chunk (arrives after finish_reason too)
			return send(Chunk{Usage: usage})
		}
		return nil
	})
	// err is nil (EOF), errStreamDone, errStreamHalt or a read/parse failure;
	// all end the stream — the closed channel is the terminal signal.
	_ = err
}

// pumpJSONFallback handles a non-SSE (application/json) answer to a stream
// request by emitting it as a single Chunk.
func (c *openAIClient) pumpJSONFallback(resp *http.Response, send func(Chunk) error) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxCompletionBody))
	if err != nil {
		return
	}
	var full fullCompletion
	if json.Unmarshal(raw, &full) != nil || len(full.Choices) == 0 {
		return
	}
	ch := Chunk{
		Content:      full.Choices[0].Message.Content,
		FinishReason: full.Choices[0].FinishReason,
	}
	if ch.FinishReason == "" {
		ch.FinishReason = "stop"
	}
	if full.Usage != nil {
		ch.Usage = &Usage{In: full.Usage.PromptTokens, Out: full.Usage.CompletionTokens}
	}
	_ = send(ch)
}

// errStreamUpstreamEnd stops parsing on a provider error frame.
var errStreamUpstreamEnd = errors.New("providers: upstream signalled an error")

// hopDialer holds the validated dialer for the CURRENT redirect hop;
// CheckRedirect swaps it so every hop is re-validated (contract: no blind
// redirects).
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
		return nil, errors.New("providers: no validated dialer for this hop")
	}
	ctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	return fn(ctx, network, addr)
}

// httpClientFor builds a per-request http.Client. With a config, the initial
// URL and each redirect hop (max 3) are validated by search.ResolveAndCheck
// and dialed only through the returned (pre-resolved-IP) dialer.
func (c *openAIClient) httpClientFor(ctx context.Context, rawURL string) (*http.Client, error) {
	base := http.Transport{
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   tlsTimeout,
		ExpectContinueTimeout: 5 * time.Second,
		MaxIdleConns:          4,
	}
	if c.cfg == nil {
		// Unguarded path: tests + local dev only (see NewClient deviation).
		d := &net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}
		base.DialContext = d.DialContext
		return &http.Client{
			Transport:     &base,
			CheckRedirect: limitRedirects,
		}, nil
	}
	dial, err := search.ResolveAndCheck(ctx, rawURL, c.cfg)
	if err != nil {
		return nil, fmt.Errorf("providers: url blocked: %w", err)
	}
	hop := &hopDialer{}
	hop.set(dial)
	base.DialContext = hop.DialContext
	return &http.Client{
		Transport: &base,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("providers: too many redirects")
			}
			next, err := search.ResolveAndCheck(req.Context(), req.URL.String(), c.cfg)
			if err != nil {
				return fmt.Errorf("providers: redirect blocked: %w", err)
			}
			hop.set(next)
			return nil
		},
	}, nil
}

func limitRedirects(_ *http.Request, via []*http.Request) error {
	if len(via) >= 3 {
		return errors.New("providers: too many redirects")
	}
	return nil
}

// endpointURL joins base ("https://api.example.com/v1") with a path segment,
// validating the scheme (defense-in-depth for the unguarded path).
func endpointURL(base, path string) (string, error) {
	b := strings.TrimRight(strings.TrimSpace(base), "/")
	if b == "" {
		return "", errors.New("providers: provider base URL is empty")
	}
	u, err := url.Parse(b)
	if err != nil {
		return "", fmt.Errorf("providers: invalid provider base URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("providers: provider base URL must be http(s), got %q", u.Scheme)
	}
	return b + "/" + path, nil
}

func authorize(req *http.Request, apiKey string) {
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
}

// buildBody renders the chat-completions request body. Params are merged
// over the defaults as-is (per task item 3), except model/messages/stream
// which stay authoritative — stream:false in Params would break the SSE
// contract. stream_options.include_usage is always sent (permissive; legacy
// providers that reject it should be handled by admin config, not here).
func buildBody(model string, msgs []ChatMessage, searchCtx string, stream bool, params map[string]any) (string, error) {
	body := map[string]any{
		"model":    model,
		"messages": buildMessages(msgs, searchCtx),
		"stream":   stream,
	}
	if stream {
		body["stream_options"] = map[string]any{"include_usage": true}
	}
	for k, v := range params {
		switch k {
		case "model", "messages", "stream":
			continue
		}
		body[k] = v
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("providers: invalid params passthrough: %w", err)
	}
	return string(raw), nil
}

// buildMessages maps ChatMessages to the wire form: plain "content" strings
// (max gateway compat) unless Images are present, in which case the
// OpenAI vision parts array is used. SearchCtx, when set, is appended as a
// trailing system message.
func buildMessages(msgs []ChatMessage, searchCtx string) []any {
	out := make([]any, 0, len(msgs)+1)
	for _, m := range msgs {
		role := m.Role
		if role == "" {
			role = "user"
		}
		if len(m.Images) == 0 {
			out = append(out, plainMessage{Role: role, Content: m.Content})
			continue
		}
		parts := make([]contentPart, 0, 1+len(m.Images))
		if m.Content != "" {
			parts = append(parts, contentPart{Type: "text", Text: m.Content})
		}
		for _, img := range m.Images {
			parts = append(parts, contentPart{Type: "image_url", ImageURL: &imageRef{URL: img}})
		}
		out = append(out, partsMessage{Role: role, Content: parts})
	}
	if searchCtx != "" {
		out = append(out, plainMessage{Role: "system", Content: searchCtx})
	}
	return out
}

type plainMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type partsMessage struct {
	Role    string        `json:"role"`
	Content []contentPart `json:"content"`
}

type contentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *imageRef `json:"image_url,omitempty"`
}

type imageRef struct {
	URL string `json:"url"`
}

// chunkRaw is one streamed SSE JSON object (OpenAI chat.completion.chunk).
// Everything is optional; unknown fields are ignored.
type chunkRaw struct {
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *rawUsage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// fullCompletion is the non-streaming response shape (Complete + the
// stream-fallback path).
type fullCompletion struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *rawUsage `json:"usage"`
}

type rawUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}
