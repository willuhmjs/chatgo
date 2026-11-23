// Command mockprovider is a tiny canned OpenAI-compatible SSE server used by
// bench/measure.sh. It is NOT part of the chatgo binary — `go run ./bench`
// only, on 127.0.0.1:9099. No auth is checked; the API key chatgo sends is
// ignored. Kept in package main under bench/ so `go build ./...` type-checks it.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

const canned = "This is a canned benchmark response from the mock provider."

type chatReq struct {
	Model  string `json:"model"`
	Stream bool   `json:"stream"`
}

func main() {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "ok")
	})

	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data": []map[string]any{
				{"id": "mock-gpt", "object": "model", "created": 0, "owned_by": "bench"},
			},
		})
	})

	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var req chatReq
		_ = json.NewDecoder(r.Body).Decode(&req) // best-effort; body shape irrelevant
		if req.Model == "" {
			req.Model = "mock-gpt"
		}

		if !req.Stream {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":      "chatcmpl-mock",
				"object":  "chat.completion",
				"model":   req.Model,
				"choices": []any{map[string]any{"index": 0, "message": map[string]string{"role": "assistant", "content": canned}, "finish_reason": "stop"}},
				"usage":   map[string]int{"prompt_tokens": 10, "completion_tokens": 12, "total_tokens": 22},
			})
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		flusher, _ := w.(http.Flusher)

		chunk := func(o map[string]any) {
			b, _ := json.Marshal(o)
			fmt.Fprintf(w, "data: %s\n\n", b)
			if flusher != nil {
				flusher.Flush()
			}
		}
		chunk(delta(map[string]string{"role": "assistant", "content": ""}))
		for _, word := range strings.Fields(canned) {
			time.Sleep(20 * time.Millisecond) // simulate token cadence
			chunk(delta(map[string]string{"content": word + " "}))
		}
		chunk(map[string]any{
			"id": "chatcmpl-mock", "object": "chat.completion.chunk", "model": req.Model,
			"choices": []any{map[string]any{"index": 0, "delta": map[string]string{}, "finish_reason": "stop"}},
			"usage":   map[string]int{"prompt_tokens": 10, "completion_tokens": 12, "total_tokens": 22},
		})
		fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	})

	log.Println("mockprovider listening on :9099")
	log.Fatal(http.ListenAndServe("127.0.0.1:9099", mux))
}

func delta(d map[string]string) map[string]any {
	return map[string]any{
		"id": "chatcmpl-mock", "object": "chat.completion.chunk", "model": "mock-gpt",
		"choices": []any{map[string]any{"index": 0, "delta": d}},
	}
}
