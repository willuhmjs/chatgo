package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// SSE protocol per docs/CONTRACTS.md:
//
//	event: <name>\ndata: <json>\n\n   ... plus `: ping` heartbeat every 15s.

// SSEHeartbeatInterval is the contract's `: ping` cadence.
const SSEHeartbeatInterval = 15 * time.Second

// ErrSSEClosed is returned after Close().
var ErrSSEClosed = errors.New("httpx: sse writer closed")

// SSEWriter serializes SSE frames for one response, flushing each event and
// heartbeating while the handler waits on upstream.
type SSEWriter struct {
	w      http.ResponseWriter
	flush  http.Flusher
	reqCtx context.Context

	mu     sync.Mutex
	closed bool
	done   chan struct{}
}

// NewSSE sets the event-stream headers, flushes immediately (contract: SSE
// responses flush immediately) and starts the heartbeat loop, which stops on
// client disconnect (r.Context) or Close.
func NewSSE(r *http.Request, w http.ResponseWriter) (*SSEWriter, error) {
	f, ok := w.(http.Flusher)
	if !ok {
		return nil, errors.New("httpx: ResponseWriter without http.Flusher")
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	f.Flush()

	s := &SSEWriter{w: w, flush: f, reqCtx: r.Context(), done: make(chan struct{})}
	go s.heartbeat()
	return s, nil
}

func (s *SSEWriter) heartbeat() {
	t := time.NewTicker(SSEHeartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-s.reqCtx.Done():
			s.Close()
			return
		case <-t.C:
			if err := s.frame(": ping\n\n"); err != nil {
				return
			}
		}
	}
}

// Event writes `event: name\ndata: <json>\n\n` and flushes.
func (s *SSEWriter) Event(name string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return s.frame(fmt.Sprintf("event: %s\ndata: %s\n\n", name, b))
}

// Comment writes a comment line (`: ping`).
func (s *SSEWriter) Comment(text string) error {
	return s.frame(": " + text + "\n\n")
}

func (s *SSEWriter) frame(raw string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrSSEClosed
	}
	if err := s.reqCtx.Err(); err != nil {
		return err // client gone; propagate upstream-cancel signal
	}
	if _, err := fmt.Fprint(s.w, raw); err != nil {
		return err
	}
	s.flush.Flush()
	return nil
}

// Done is closed once the writer is closed (client disconnect or Close).
func (s *SSEWriter) Done() <-chan struct{} { return s.done }

// Close stops the heartbeat. Safe to call multiple times; handlers must defer
// it so the loop never writes to a finished ResponseWriter.
func (s *SSEWriter) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.done)
	}
}
