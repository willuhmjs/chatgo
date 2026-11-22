// Package providers talks to OpenAI-compatible upstreams.
// BE-providers owns the real implementation (client.go body, sse.go, models.go);
// the types and interface below are frozen by docs/CONTRACTS.md.
package providers

import (
	"context"
	"errors"

	"chatgo/internal/config"
)

// errNotImplemented is returned by scaffold stubs until the owning agent
// replaces them. Replace the body, keep the signature.
var errNotImplemented = errors.New("providers: not implemented yet (scaffold stub)")

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
// CONTRACT-DEVIATION (scaffold): returns a stub that fails with
// errNotImplemented until BE-providers replaces stubClient's methods.
func NewClient(cfg *config.Config) Client {
	return &stubClient{cfg: cfg}
}

type stubClient struct{ cfg *config.Config }

func (c *stubClient) ListModels(ctx context.Context, baseURL, apiKey string) ([]Model, error) {
	return nil, errNotImplemented
}

func (c *stubClient) StreamChat(ctx context.Context, p Provider, opts StreamOpts) (<-chan Chunk, error) {
	return nil, errNotImplemented
}

func (c *stubClient) Complete(ctx context.Context, p Provider, model string, msgs []ChatMessage) (string, error) {
	return "", errNotImplemented
}
