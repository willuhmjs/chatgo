package api

import (
	"net/http"

	"chatgo/internal/api/httpx"
)

// SSE helpers — frozen entrypoints for message streams (contract §SSE).
// Implementation lives in httpx (see package doc for the cycle rationale).

// SSEWriter streams `event: <name>\ndata: <json>\n\n` frames with 15s
// `: ping` heartbeats and client-disconnect propagation.
type SSEWriter = httpx.SSEWriter

// NewSSE takes over the response for one SSE stream. Handlers MUST
// defer w.Close(). Usage:
//
//	w, err := api.NewSSE(r, rw)
//	if err != nil { httpx.InternalError(...) ; return }
//	defer w.Close()
//	w.Event("meta", map[string]string{"message_id": id, "chat_id": chatID})
func NewSSE(r *http.Request, w http.ResponseWriter) (*SSEWriter, error) {
	return httpx.NewSSE(r, w)
}
