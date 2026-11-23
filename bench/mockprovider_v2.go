//go:build ignore

// Command mockprovider_v2 is the OpenAI-compatible mock used by
// bench/smoke_v2.sh. It is NOT part of the chatgo binary and NOT compiled by
// `go build ./...` (the //go:build ignore tag keeps it out of the package
// while bench/mockprovider.go stays the :9099 RAM-bench mock). The go
// command always includes files explicitly named on its command line, so
// run it standalone:
//
//	go run bench/mockprovider_v2.go
//	go build -o mockv2 bench/mockprovider_v2.go   # what smoke_v2.sh does
//
// Listens on 127.0.0.1:9100. NEVER co-host with the RAM bench: that owns
// 9099 + 8099; 8080/9098 are also off-limits here.
//
// Endpoints (docs/FEATURES-V2.md §BE-RAG/BE-SEC + docs/CONTRACTS.md):
//
//	GET  /healthz                  → "ok" readiness probe
//	GET  /v1/models                → chat + *embed* + *image* models (the
//	                                  kb/imggen packages pick capability by
//	                                  name heuristic, so ids carry "embed"
//	                                  / "image")
//	POST /v1/chat/completions      → echo-style completion of the last user
//	                                  message; honors stream:true with real
//	                                  SSE chunks + data: [DONE]
//	POST /v1/embeddings            → deterministic 64-dim unit vectors from
//	                                  FNV-1a feature hashing of each input
//	                                  (same text → same vector, word
//	                                  overlap → high cosine, so kb
//	                                  top-1 search is meaningfully testable)
//	POST /v1/images/generations    → n × tiny valid 1×1 PNG as b64_json
//	POST /v1/moderations           → 200 no-op (flagged:false per input)
//
// No auth is checked; the API key chatgo sends is ignored (same policy as
// bench/mockprovider.go).
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"net/http"
	"strings"
	"time"
)

const (
	addr      = "127.0.0.1:9100"
	embedDim  = 64 // kb scanCosine handles any dim; 64 is plenty
	maxImages = 16 // generous clamp on ?n (imggen itself caps at 4)
	chatModel = "mock-gpt-v2"
	embModel  = "mock-embed-v2" // "embed" in the id → kb name heuristic
	imgModel  = "mock-image-v2" // "image" in the id → imggen hint list
)

// png1x1 encodes a solid 1×1 PNG once per color and returns its base64.
// Real image/png output so files.Service (image decode, thumb path) accepts
// the bytes just like a real provider response.
func png1x1(c color.RGBA) string {
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, c)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		log.Fatalf("mockprovider_v2: png encode: %v", err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

var (
	imgA, imgB string // two variants so multi-image runs produce distinct files
)

func main() {
	imgA = png1x1(color.RGBA{0x63, 0x66, 0xf1, 0xff}) // indigo pixel
	imgB = png1x1(color.RGBA{0x10, 0xa3, 0x7f, 0xff}) // green pixel

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("GET /v1/models", handleModels)
	mux.HandleFunc("POST /v1/chat/completions", handleChat)
	mux.HandleFunc("POST /v1/embeddings", handleEmbeddings)
	mux.HandleFunc("POST /v1/images/generations", handleImages)
	mux.HandleFunc("POST /v1/moderations", handleModerations)

	log.Printf("mockprovider_v2 listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ---------- /v1/models ----------

func handleModels(w http.ResponseWriter, _ *http.Request) {
	model := func(id string) map[string]any {
		return map[string]any{"id": id, "object": "model", "created": 1, "owned_by": "bench"}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data":   []map[string]any{model(chatModel), model(embModel), model(imgModel)},
	})
}

// ---------- /v1/chat/completions ----------

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model    string        `json:"model"`
	Stream   bool          `json:"stream"`
	Messages []chatMessage `json:"messages"`
}

// echoOf is the canned reply: "Echo: " + the last non-empty user message
// (truncated), so smoke scripts can assert round-trip fidelity end to end.
func echoOf(req chatRequest) string {
	var last string
	for _, m := range req.Messages {
		if m.Role == "user" && strings.TrimSpace(m.Content) != "" {
			last = m.Content
		}
	}
	r := []rune(strings.TrimSpace(last))
	if len(r) > 300 {
		last = string(r[:300]) + "…"
	}
	if last == "" {
		return "Echo: (no user message)"
	}
	return "Echo: " + last
}

func usageFor(prompt, completion string) map[string]int {
	in := len(prompt)/4 + 1
	out := len(completion)/4 + 1
	return map[string]int{"prompt_tokens": in, "completion_tokens": out, "total_tokens": in + out}
}

func handleChat(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	_ = json.NewDecoder(r.Body).Decode(&req) // best-effort; unknown fields irrelevant
	if req.Model == "" {
		req.Model = chatModel
	}
	reply := echoOf(req)

	if !req.Stream {
		writeJSON(w, http.StatusOK, map[string]any{
			"id":     "chatcmpl-mock-v2",
			"object": "chat.completion",
			"model":  req.Model,
			"choices": []any{map[string]any{
				"index":         0,
				"message":       map[string]string{"role": "assistant", "content": reply},
				"finish_reason": "stop",
			}},
			"usage": usageFor(promptText(req), reply),
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

	chunk(idObj(req.Model, map[string]any{"index": 0, "delta": map[string]string{"role": "assistant", "content": ""}}))
	for _, word := range strings.Fields(reply) {
		time.Sleep(time.Millisecond) // token cadence, much faster than the RAM mock
		chunk(idObj(req.Model, map[string]any{"index": 0, "delta": map[string]string{"content": word + " "}}))
	}
	chunk(idObj(req.Model, map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"},
		usageFor(promptText(req), reply)))
	fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

// idObj builds one SSE chunk envelope (usage attached only on the final one).
func idObj(model string, choice map[string]any, usage ...map[string]int) map[string]any {
	o := map[string]any{
		"id": "chatcmpl-mock-v2", "object": "chat.completion.chunk", "model": model,
		"choices": []any{choice},
	}
	if len(usage) > 0 {
		o["usage"] = usage[0]
	}
	return o
}

func promptText(req chatRequest) string {
	var sb strings.Builder
	for _, m := range req.Messages {
		sb.WriteString(m.Content)
	}
	return sb.String()
}

// ---------- /v1/embeddings ----------

// embedVec maps text → unit-length 64-dim float32 via signed FNV-1a feature
// hashing over lowercased alphanumeric word tokens: each word picks one bin
// and one sign from its hash and contributes ±1. Deterministic across
// processes (no map iteration or randomness involved), identical text always
// yields the identical vector, and cosine similarity tracks shared-vocabulary
// overlap — which is exactly what kb's brute-scan top-k needs to be testable.
func embedVec(text string) []float32 {
	var acc [embedDim]float64
	for _, w := range wordTokens(text) {
		h := fnv.New64a()
		_, _ = h.Write([]byte(w))
		s := h.Sum64()
		bin := s % embedDim
		if (s>>32)&1 == 1 {
			acc[bin]--
		} else {
			acc[bin]++
		}
	}
	var norm float64
	for _, v := range acc {
		norm += v * v
	}
	norm = math.Sqrt(norm)
	out := make([]float32, embedDim)
	if norm == 0 {
		return out // all-zero vector for empty/punctuation-only input
	}
	for i, v := range acc {
		out[i] = float32(v / norm)
	}
	return out
}

// wordTokens lowercases and splits on any non-alphanumeric byte (ASCII fast
// path; other runes act as separators, which is fine for hashing).
func wordTokens(s string) []string {
	var words []string
	for i := 0; i < len(s); {
		j := i
		for j < len(s) && !isAlnumASCII(s[j]) {
			j++
		}
		k := j
		for k < len(s) && isAlnumASCII(s[k]) {
			k++
		}
		if k > j {
			words = append(words, strings.ToLower(s[j:k]))
		}
		i = k
	}
	return words
}

func isAlnumASCII(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

func handleEmbeddings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model string          `json:"model"`
		Input json.RawMessage `json:"input"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]any{"message": "invalid JSON body", "type": "invalid_request_error"},
		})
		return
	}
	if req.Model == "" {
		req.Model = embModel
	}
	// OpenAI accepts a bare string OR an array of strings.
	var inputs []string
	if err := json.Unmarshal(req.Input, &inputs); err != nil {
		var one string
		if err := json.Unmarshal(req.Input, &one); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": map[string]any{"message": `input must be a string or array of strings`, "type": "invalid_request_error"},
			})
			return
		}
		inputs = []string{one}
	}
	data := make([]map[string]any, 0, len(inputs))
	for i, in := range inputs {
		data = append(data, map[string]any{
			"object":    "embedding",
			"index":     i,
			"embedding": embedVec(in),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"model":  req.Model,
		"data":   data,
		"usage":  map[string]int{"prompt_tokens": len(inputs), "total_tokens": len(inputs)},
	})
}

// ---------- /v1/images/generations ----------

func handleImages(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model string `json:"model"`
		N     int    `json:"n"`
		Size  string `json:"size"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	n := req.N
	if n < 1 {
		n = 1
	}
	if n > maxImages {
		n = maxImages
	}
	data := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		b64 := imgA
		if i%2 == 1 {
			b64 = imgB
		}
		data = append(data, map[string]any{
			"b64_json":       b64,
			"revised_prompt": fmt.Sprintf("mock v2 pixel #%d", i),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"created": time.Now().Unix(),
		"data":    data,
	})
}

// ---------- /v1/moderations ----------

func handleModerations(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Input json.RawMessage `json:"input"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	count := 1
	var many []string
	if json.Unmarshal(req.Input, &many) == nil {
		count = max(len(many), 1)
	}
	results := make([]map[string]any, 0, count)
	for i := 0; i < count; i++ {
		results = append(results, map[string]any{
			"flagged":         false,
			"categories":      map[string]bool{},
			"category_scores": map[string]float64{},
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":      "modr-mock-v2",
		"model":   "mock-moderation-v2",
		"results": results,
	})
}
