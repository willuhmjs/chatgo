// Package search wraps SearXNG and guards outbound URLs (SSRF).
// BE-data owns searxng.go's logic; shapes are frozen by docs/CONTRACTS.md.
package search

import (
	"context"
	"errors"
	"net/http"

	"chatgo/internal/config"
)

var errNotImplemented = errors.New("search: not implemented yet (scaffold stub)")

// Result is one search hit.
type Result struct {
	Title   string
	URL     string
	Content string // snippet
}

// Service calls SearXNG at cfg.SearXNGURL. Enabled reports whether the
// web-search toggle may be offered at all.
type Service struct {
	BaseURL string
	HC      *http.Client
	Enabled bool
}

// New builds the search service (disabled when SEARXNG_URL is unset).
func New(cfg *config.Config) *Service {
	return &Service{
		BaseURL: cfg.SearXNGURL,
		HC:      &http.Client{}, // BE-data: replace transport with ResolveAndCheck dialer
		Enabled: cfg.SearXNGURL != "",
	}
}

// Search runs GET {base}/search?q=&format=json and maps top n hits.
func (s *Service) Search(ctx context.Context, q string, n int) ([]Result, error) {
	if !s.Enabled {
		return nil, errors.New("search: SearXNG disabled (SEARXNG_URL unset)")
	}
	return nil, errNotImplemented
}

// ComposeContext renders hits as a fenced block of title/url/1200-char
// snippets to splice into a system message.
func ComposeContext(results []Result) string {
	return "" // BE-data implements (contract §search)
}
