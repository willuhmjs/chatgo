// Package chat orchestrates a chat turn: history compaction, persistence,
// upstream streaming, titles and summaries. BE-chat owns the logic;
// these types and the constructor shape are frozen by docs/CONTRACTS.md.
package chat

import (
	"context"
	"errors"

	"chatgo/internal/config"
	"chatgo/internal/providers"
	"chatgo/internal/search"
	"chatgo/internal/store"
)

var errNotImplemented = errors.New("chat: not implemented yet (scaffold stub)")

// Event is an api-side SSE event (see contract §SSE protocol). Type is one of
// meta|title|sources|token|done|error; Data is the JSON payload for that type.
type Event struct {
	Type string
	Data any
}

// SendOpts are per-turn options from POST /api/chats/{id}/messages.
type SendOpts struct {
	Search bool     // enable SearXNG web search
	Images []string // data: URLs attached to the user message
	From   string   // message id to regenerate from ("" = normal append)
}

// Service drives chat turns. Dependencies are interfaces/pointers wired in main.
type Service struct {
	Cfg    *config.Config
	Store  *store.Store
	Prov   providers.Client
	Search *search.Service
}

// New builds the chat service.
func New(cfg *config.Config, st *store.Store, prov providers.Client, srch *search.Service) *Service {
	return &Service{Cfg: cfg, Store: st, Prov: prov, Search: srch}
}

// Send persists the user message, streams the assistant reply and returns a
// channel of Events terminated by done|error. The channel is closed after the
// terminal event. BE-chat implements compaction/title/summarize per contract.
func (s *Service) Send(ctx context.Context, userID, chatID string, userMsg providers.ChatMessage, opts SendOpts) (<-chan Event, error) {
	return nil, errNotImplemented
}
