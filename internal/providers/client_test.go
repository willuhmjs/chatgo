package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// testClient is the unguarded client (cfg == nil — see NewClient deviation
// note); all tests hit local httptest servers.
func testClient() *openAIClient { return NewClient(nil).(*openAIClient) }

func TestListModelsHTTP(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %q, want /v1/models", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"m-a","display_name":"Model A"},{"id":"m-b"}]}`)
	}))
	defer srv.Close()

	got, err := testClient().ListModels(context.Background(), srv.URL+"/v1/", "sk-key")
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer sk-key" {
		t.Errorf("auth = %q", gotAuth)
	}
	if len(got) != 2 || got[0].ID != "m-a" || got[0].Display != "Model A" || got[1].Display != "M b" {
		t.Fatalf("got %+v", got)
	}
}

func TestListModelsUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		fmt.Fprint(w, `{"error":{"message":"invalid api key sk-SUPERSECRET-123"}}`)
	}))
	defer srv.Close()

	_, err := testClient().ListModels(context.Background(), srv.URL, "bad")
	var ue *UpstreamError
	if !errors.As(err, &ue) || ue.Status != 401 {
		t.Fatalf("err = %v (%T), want *UpstreamError 401", err, err)
	}
	if strings.Contains(ue.Error(), "SUPERSECRET") {
		t.Fatalf("upstream body leaked into error: %v", ue)
	}
}

func TestStreamChatSplitsDoneAndUsageAfterFinish(t *testing.T) {
	// Full SSE body with CRLF endings, a 4-byte emoji, a role-only
	// heartbeat chunk, finish_reason and usage arriving AFTER the finish.
	full := "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"}}]}\r\n\r\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hel\"}}]}\r\n\r\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"lo 🎈\"}}]}\r\n\r\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\r\n\r\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":7}}\r\n\r\n" +
		"data: [DONE]\r\n\r\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fl := w.(http.Flusher)
		// Hand out the payload 7 bytes at a time: splits lines mid-JSON and
		// the emoji mid-rune.
		for i := 0; i < len(full); i += 7 {
			end := min(i+7, len(full))
			if _, err := w.Write([]byte(full[i:end])); err != nil {
				return
			}
			fl.Flush()
		}
	}))
	defer srv.Close()

	chunks := collect(t, mustStream(t, srv.URL, StreamOpts{
		ModelID:  "gpt-test",
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	}))

	var text strings.Builder
	var finish string
	var usage *Usage
	for _, c := range chunks {
		text.WriteString(c.Content)
		if c.FinishReason != "" {
			finish = c.FinishReason
		}
		if c.Usage != nil {
			usage = c.Usage
		}
	}
	if text.String() != "Hello 🎈" {
		t.Errorf("content = %q", text.String())
	}
	if finish != "stop" {
		t.Errorf("finish = %q", finish)
	}
	if usage == nil || usage.In != 11 || usage.Out != 7 {
		t.Errorf("usage = %+v, want {11 7} after finish", usage)
	}
}

func TestStreamChatMalformedChunksSkipped(t *testing.T) {
	body := ": hello\r\n\r\n" + // comment frame
		"data: \n\n" + // empty payload
		"data: {not json at all\n\n" + // malformed
		"event: ping\ndata: keepalive\n\n" + // non-json named event
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
	srv := httptest.NewServer(sseHandler(body))
	defer srv.Close()

	chunks := collect(t, mustStream(t, srv.URL, StreamOpts{ModelID: "m"}))
	if len(chunks) != 2 || chunks[0].Content != "ok" || chunks[1].FinishReason != "stop" {
		t.Fatalf("got %+v, want [ok, stop]", chunks)
	}
}

func TestStreamChatUpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		fmt.Fprint(w, `{"error":{"message":"token sk-TOPSECRET throttled"}}`)
	}))
	defer srv.Close()

	_, err := testClient().StreamChat(context.Background(),
		Provider{BaseURL: srv.URL, APIKey: "k"}, StreamOpts{ModelID: "m"})
	var ue *UpstreamError
	if !errors.As(err, &ue) || ue.Status != 429 {
		t.Fatalf("err = %v, want UpstreamError 429", err)
	}
	if strings.Contains(ue.Error(), "TOPSECRET") {
		t.Fatalf("upstream body leaked: %v", ue)
	}
}

func TestStreamChatJSONFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json") // gateway ignored stream:true
		fmt.Fprint(w, `{"choices":[{"index":0,"message":{"content":"fallback"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":2,"completion_tokens":3}}`)
	}))
	defer srv.Close()

	chunks := collect(t, mustStream(t, srv.URL, StreamOpts{ModelID: "m"}))
	if len(chunks) != 1 || chunks[0].Content != "fallback" ||
		chunks[0].FinishReason != "stop" || chunks[0].Usage == nil || chunks[0].Usage.Out != 3 {
		t.Fatalf("got %+v", chunks)
	}
}

func TestStreamChatCancelMidStreamNoLeaks(t *testing.T) {
	disconnected := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done() // hold the conn open until the client drops it
		close(disconnected)
	}))
	defer srv.Close()

	c := testClient()
	pumpExit := make(chan struct{})
	c.streamDone = func() { close(pumpExit) }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out, err := c.StreamChat(ctx, Provider{BaseURL: srv.URL}, StreamOpts{ModelID: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if ch := <-out; ch.Content != "hi" {
		t.Fatalf("first chunk = %+v", ch)
	}
	cancel()

	closed := false
	deadline := time.After(3 * time.Second)
	for !closed {
		select {
		case _, ok := <-out:
			closed = !ok
		case <-deadline:
			t.Fatal("channel not closed after ctx cancel")
		}
	}
	select {
	case <-pumpExit:
	case <-time.After(3 * time.Second):
		t.Fatal("pump goroutine did not exit after ctx cancel")
	}
	select {
	case <-disconnected:
	case <-time.After(3 * time.Second):
		t.Fatal("server never observed the connection closing")
	}
}

func TestStreamChatRequestShapeVisionParamsAndPassthrough(t *testing.T) {
	type wireMsg struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	var got struct {
		Model         string          `json:"model"`
		Stream        bool            `json:"stream"`
		StreamOptions map[string]bool `json:"stream_options"`
		Messages      []wireMsg       `json:"messages"`
		Temperature   any             `json:"temperature"`
		TopP          any             `json:"top_p"`
		User          string          `json:"user"`
		Custom        string          `json:"custom_key"`
		Stop          []string        `json:"stop"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("content-type = %q", ct)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	collect(t, mustStream(t, srv.URL, StreamOpts{
		ModelID:   "vision-model",
		SearchCtx: "SEARCH RESULTS HERE",
		Messages: []ChatMessage{
			{Role: "system", Content: "be nice"}, // no images → plain string
			{Role: "user", Content: "what is in this?", Images: []string{"data:image/png;base64,AAA", "data:image/webp;base64,BBB"}},
		},
		Params: map[string]any{
			"temperature": 0.3,
			"top_p":       0.9,
			"stop":        []string{"END"},
			"user":        "u-42",
			"custom_key":  "anything",
			"stream":      false,    // must be ignored (authoritative stream:true)
			"model":       "hijack", // must be ignored
		},
	}))

	if got.Model != "vision-model" || !got.Stream {
		t.Errorf("model/stream = %q/%v", got.Model, got.Stream)
	}
	if got.StreamOptions["include_usage"] != true || len(got.StreamOptions) != 1 {
		t.Errorf("stream_options = %v", got.StreamOptions)
	}
	if fmt.Sprint(got.Temperature) != "0.3" || fmt.Sprint(got.TopP) != "0.9" ||
		got.User != "u-42" || got.Custom != "anything" || len(got.Stop) != 1 || got.Stop[0] != "END" {
		t.Errorf("params passthrough = %+v", got)
	}

	if len(got.Messages) != 3 {
		t.Fatalf("messages = %d, want system + search-user + vision-user", len(got.Messages))
	}
	// msg0: system plain string
	var s0 string
	if err := json.Unmarshal(got.Messages[0].Content, &s0); err != nil || got.Messages[0].Role != "system" || s0 != "be nice" {
		t.Errorf("system message not plain string: %s", got.Messages[0].Content)
	}
	// SearchCtx is a USER turn (untrusted data must not carry system
	// authority) inserted BEFORE the trailing live user message.
	var sSearch string
	search := got.Messages[1]
	if err := json.Unmarshal(search.Content, &sSearch); search.Role != "user" || err != nil || sSearch != "SEARCH RESULTS HERE" {
		t.Errorf("search user msg = %+v", search)
	}
	// msg2: live user turn stays last; vision parts array
	var parts []map[string]any
	if err := json.Unmarshal(got.Messages[2].Content, &parts); err != nil {
		t.Fatalf("vision content not an array: %v (%s)", err, got.Messages[2].Content)
	}
	if got.Messages[2].Role != "user" {
		t.Errorf("live user turn not last: %+v", got.Messages[2])
	}
	if len(parts) != 3 {
		t.Fatalf("parts = %d, want text + 2 images", len(parts))
	}
	if parts[0]["type"] != "text" || parts[0]["text"] != "what is in this?" {
		t.Errorf("part0 = %v", parts[0])
	}
	for i, want := range []string{"data:image/png;base64,AAA", "data:image/webp;base64,BBB"} {
		p := parts[i+1]
		iu, _ := p["image_url"].(map[string]any)
		if p["type"] != "image_url" || iu["url"] != want {
			t.Errorf("part%d = %v, want image_url %s", i+1, p, want)
		}
	}
}

func TestComplete(t *testing.T) {
	var got struct {
		Stream        *bool          `json:"stream"`
		StreamOptions map[string]any `json:"stream_options"`
		Messages      []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		fmt.Fprint(w, `{"choices":[{"message":{"content":"42"},"finish_reason":"stop"}]}`)
	}))
	defer srv.Close()

	c := testClient()
	ctx := context.Background()
	gotTxt, err := c.Complete(ctx, Provider{BaseURL: srv.URL, APIKey: "k"}, "task-model",
		[]ChatMessage{{Role: "user", Content: "answer"}})
	if err != nil {
		t.Fatal(err)
	}
	if gotTxt != "42" {
		t.Errorf("content = %q", gotTxt)
	}
	if got.Stream != nil && *got.Stream {
		t.Errorf("stream = true in Complete request")
	}
	if got.StreamOptions != nil {
		t.Errorf("stream_options present in Complete request")
	}
	if s := string(got.Messages[0].Content); s != `"answer"` {
		t.Errorf("message content not plain string: %s", s)
	}

	// Error surfaces as UpstreamError (404).
	srv404 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer srv404.Close()
	_, err = c.Complete(ctx, Provider{BaseURL: srv404.URL}, "m", nil)
	var ue *UpstreamError
	if !errors.As(err, &ue) || ue.Status != 404 {
		t.Fatalf("err = %v, want UpstreamError 404", err)
	}
}

func TestCompleteNoChoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[]}`)
	}))
	defer srv.Close()
	if _, err := testClient().Complete(context.Background(), Provider{BaseURL: srv.URL}, "m", nil); err == nil {
		t.Fatal("want error for empty choices")
	}
}

func TestStreamChatValidationAndScheme(t *testing.T) {
	c := testClient()
	ctx := context.Background()
	if _, err := c.StreamChat(ctx, Provider{BaseURL: "http://x"}, StreamOpts{}); err == nil {
		t.Error("want error for empty model id")
	}
	if _, err := c.StreamChat(ctx, Provider{BaseURL: "ftp://x"}, StreamOpts{ModelID: "m"}); err == nil {
		t.Error("want error for non-http base URL")
	}
	if _, err := c.ListModels(ctx, "", ""); err == nil {
		t.Error("want error for empty base URL")
	}
}

func TestUpstreamErrorMessages(t *testing.T) {
	for _, st := range []int{401, 403, 404, 429, 500, 503, 418} {
		e := upstreamErrorFor(st)
		if e.Status != st || e.Msg == "" {
			t.Errorf("status %d → %+v", st, e)
		}
	}
}

// withStreamWatchdog shrinks the liveness watchdogs for one test.
func withStreamWatchdog(t *testing.T, first, idle, tick time.Duration) {
	t.Helper()
	pf, pi, pt := streamFirstTokenTimeout, streamIdleTimeout, streamWatchdogInterval
	streamFirstTokenTimeout, streamIdleTimeout, streamWatchdogInterval = first, idle, tick
	t.Cleanup(func() {
		streamFirstTokenTimeout, streamIdleTimeout, streamWatchdogInterval = pf, pi, pt
	})
}

func TestStreamChatFirstTokenWatchdogKillsSilentStream(t *testing.T) {
	// Headers arrive, then absolute silence: indistinguishable from a dead
	// connection → the watchdog must close the stream.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()

	withStreamWatchdog(t, 150*time.Millisecond, time.Minute, 20*time.Millisecond)
	start := time.Now()
	chunks := collect(t, mustStream(t, srv.URL, StreamOpts{ModelID: "m"}))
	if len(chunks) != 0 {
		t.Fatalf("chunks = %d, want 0", len(chunks))
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("first-token watchdog took %v", d)
	}
}

func TestStreamChatIdleWatchdogKillsStalledStream(t *testing.T) {
	// One chunk, then silence longer than the idle cap → stream ends.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()

	withStreamWatchdog(t, time.Minute, 150*time.Millisecond, 20*time.Millisecond)
	chunks := collect(t, mustStream(t, srv.URL, StreamOpts{ModelID: "m"}))
	if len(chunks) != 1 || chunks[0].Content != "hi" {
		t.Fatalf("chunks = %+v, want [hi]", chunks)
	}
}

func TestStreamChatKeepalivesResetIdleWatchdog(t *testing.T) {
	// SSE comment pings are bytes too: a chatty-but-heartbeating upstream must
	// NOT be killed by the idle watchdog (it would also die to the
	// first-token cap if bytes did not count as liveness).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		for i := 0; i < 12; i++ {
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			fl.Flush()
			select {
			case <-time.After(30 * time.Millisecond):
			case <-r.Context().Done():
				return
			}
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"late\"}}]}\n\n"+
			"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"+
			"data: [DONE]\n\n")
	}))
	defer srv.Close()

	withStreamWatchdog(t, 80*time.Millisecond, 80*time.Millisecond, 20*time.Millisecond)
	chunks := collect(t, mustStream(t, srv.URL, StreamOpts{ModelID: "m"}))
	var text, finish string
	for _, c := range chunks {
		text += c.Content
		if c.FinishReason != "" {
			finish = c.FinishReason
		}
	}
	if text != "late" || finish != "stop" {
		t.Fatalf("chunks = %+v, want late…stop", chunks)
	}
}

func TestResponseHeaderTimeoutBoundsSlowUpstream(t *testing.T) {
	old := responseHeaderTimeout
	responseHeaderTimeout = 100 * time.Millisecond
	defer func() { responseHeaderTimeout = old }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(3 * time.Second):
			fmt.Fprint(w, `{"data":[]}`)
		case <-r.Context().Done(): // client hung up; stop the handler
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := testClient().ListModels(ctx, srv.URL, "k"); err == nil {
		t.Fatal("want header-timeout error against silent upstream")
	}
}

// ── helpers ────────────────────────────────────────────────────────────────

func sseHandler(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(body))
	}
}

func mustStream(t *testing.T, base string, opts StreamOpts) <-chan Chunk {
	t.Helper()
	out, err := testClient().StreamChat(context.Background(), Provider{BaseURL: base, APIKey: "k"}, opts)
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	return out
}

// collect drains chunks until the channel closes or a deadline hits.
func collect(t *testing.T, ch <-chan Chunk) []Chunk {
	t.Helper()
	var got []Chunk
	deadline := time.After(5 * time.Second)
	for {
		select {
		case c, ok := <-ch:
			if !ok {
				return got
			}
			got = append(got, c)
		case <-deadline:
			t.Fatalf("timeout waiting for stream close (got %d chunks)", len(got))
		}
	}
}
