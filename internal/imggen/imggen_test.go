package imggen

// Internal tests: the fake upstream is a real httptest OpenAI-style
// /v1/images/generations endpoint (guarded client dials loopback via
// cfg.AllowLoopback). Internal package so tests can shrink the response cap.

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"chatgo/internal/auth"
	"chatgo/internal/config"
	"chatgo/internal/files"
	"chatgo/internal/store"
)

// 1x1 transparent PNG.
const tinyPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

var testSecret = []byte("0123456789abcdef0123456789abcdef")

type env struct {
	t           *testing.T
	st          *store.Store
	user        *store.User
	srv         *httptest.Server
	upstreamURL string
	cookie      string
	mu          sync.Mutex
	reqBody     string // raw body of the last upstream request
	body        string // canned response body
	status      int
}

func newEnv(t *testing.T) *env {
	t.Helper()
	name := "file:mem_img_" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()) +
		"?mode=memory&cache=shared"
	st, err := store.Open(name, testSecret)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cfg := &config.Config{
		DataDir:       t.TempDir(),
		SessionSecret: hex.EncodeToString(testSecret),
		Registration:  "open",
		AllowLoopback: true, // upstream httptest server lives on 127.0.0.1
		Version:       "test",
	}
	a, err := auth.New(cfg, st)
	if err != nil {
		t.Fatalf("auth.New: %v", err)
	}

	e := &env{t: t, st: st, status: 200}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/images/generations" {
			http.NotFound(w, r)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		e.mu.Lock()
		e.reqBody = string(raw)
		body, status := e.body, e.status
		e.mu.Unlock()

		if ct := r.Header.Get("Authorization"); ct != "Bearer test-key" {
			e.t.Errorf("upstream Authorization header = %q", ct)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(upstream.Close)
	e.upstreamURL = upstream.URL

	// Caller with a live session.
	u := &store.User{Email: "artist@example.com", Name: "A", Role: store.RoleUser}
	if err := st.Users.Insert(t.Context(), u); err != nil {
		t.Fatal(err)
	}
	e.user = u
	sess, err := a.CreateSession(t.Context(), u.ID, "test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	Register(mux, &Deps{Cfg: cfg, Store: st, Auth: a, Files: files.New(cfg, st)})
	srv := httptest.NewServer(a.Require(mux)) // mirrors AuthPolicy's /api/* branch
	t.Cleanup(srv.Close)
	e.srv = srv
	e.cookie = auth.CookieName + "=" + sess.Token
	return e
}

func (e *env) respond(status int, body string) {
	e.mu.Lock()
	e.status, e.body = status, body
	e.mu.Unlock()
}

func (e *env) post(path, body string) *http.Response {
	e.t.Helper()
	req, err := http.NewRequest(http.MethodPost, e.srv.URL+path, strings.NewReader(body))
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", e.cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	return resp
}

func (e *env) get(path string) *http.Response {
	e.t.Helper()
	req, err := http.NewRequest(http.MethodGet, e.srv.URL+path, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set("Cookie", e.cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	return resp
}

func (e *env) seedProvider(t *testing.T, id, baseURL string, imageModels ...string) *store.Provider {
	t.Helper()
	p := &store.Provider{ID: id, Name: "P-" + id, BaseURL: baseURL, APIKey: "test-key", Enabled: true}
	if err := e.st.Providers.Insert(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	var rows []store.ModelRow
	for _, m := range imageModels {
		rows = append(rows, store.ModelRow{ModelID: m, Display: m, Enabled: true})
	}
	if rows == nil {
		rows = append(rows, store.ModelRow{ModelID: "gpt-4o", Display: "gpt-4o", Enabled: true})
	}
	if err := e.st.Models.ReplaceForProvider(t.Context(), p.ID, rows); err != nil {
		t.Fatal(err)
	}
	return p
}

func decodeBody(t *testing.T, resp *http.Response, dst any) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

type genResult struct {
	Images []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Mime string `json:"mime"`
		Size int64  `json:"size"`
		URL  string `json:"url"`
	} `json:"images"`
	Error string `json:"error"`
}

// TestGenerateB64ToFilesRows: fake OpenAI images endpoint (b64_json) →
// files rows (kind image) → ids+urls out → GET /api/images shows them.
func TestGenerateB64ToFilesRows(t *testing.T) {
	e := newEnv(t)
	p := e.seedProvider(t, "prov-1", e.upstreamURL+"/v1", "dall-e-3")
	e.respond(200, `{"data":[{"b64_json":"`+tinyPNG+`"},{"b64_json":"`+tinyPNG+`"}]}`)

	resp := e.post("/api/images/generate", `{"prompt":"a red fox","n":2}`)
	var out genResult
	decodeBody(t, resp, &out)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d (%s)", resp.StatusCode, out.Error)
	}
	if len(out.Images) != 2 {
		t.Fatalf("images = %d, want 2", len(out.Images))
	}
	for i, img := range out.Images {
		if img.ID == "" || !strings.HasPrefix(img.URL, "/files/"+img.ID+"/") {
			t.Fatalf("image %d = %+v", i, img)
		}
		f, err := e.st.Files.Get(t.Context(), img.ID)
		if err != nil {
			t.Fatalf("files row %d missing: %v", i, err)
		}
		if f.Kind != store.FileKindImage || f.Mime != "image/png" || f.UserID != e.user.ID {
			t.Errorf("files row %d = kind %q mime %q user %q", i, f.Kind, f.Mime, f.UserID)
		}
		if f.Size <= 0 || strings.Contains(f.Path, "..") {
			t.Errorf("files row %d bogus size/path: %+v", i, f)
		}
	}

	// Upstream request shape: model from the cached image heuristic,
	// response_format=b64_json, prompt/n/size passed through.
	e.mu.Lock()
	raw := e.reqBody
	e.mu.Unlock()
	var sent map[string]any
	if err := json.Unmarshal([]byte(raw), &sent); err != nil {
		t.Fatalf("upstream body %q: %v", raw, err)
	}
	if sent["model"] != "dall-e-3" || sent["response_format"] != "b64_json" ||
		sent["prompt"] != "a red fox" || sent["n"] != float64(2) {
		t.Errorf("upstream request = %v", sent)
	}

	// GET /api/images lists them (own, newest-first).
	lr := e.get("/api/images")
	var list genResult
	decodeBody(t, lr, &list)
	if lr.StatusCode != 200 || len(list.Images) != 2 {
		t.Fatalf("list = %d, %d images", lr.StatusCode, len(list.Images))
	}
	if list.Images[0].ID == list.Images[1].ID {
		t.Error("duplicate rows in list")
	}
	_ = p
}

func TestGenerateDefaultSize(t *testing.T) {
	e := newEnv(t)
	e.seedProvider(t, "prov-1", e.upstreamURL+"/v1", "dall-e-3")
	e.respond(200, `{"data":[{"b64_json":"`+tinyPNG+`"}]}`)

	resp := e.post("/api/images/generate", `{"prompt":"a blue van","size":"512x512","provider_id":"prov-1"}`)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	e.mu.Lock()
	raw := e.reqBody
	e.mu.Unlock()
	var sent map[string]any
	if err := json.Unmarshal([]byte(raw), &sent); err != nil {
		t.Fatal(err)
	}
	if sent["size"] != "512x512" || sent["n"] != float64(1) {
		t.Errorf("size/n passthrough = %v", sent)
	}

	// Bare-number size (FE pages-api.ts sends size: 512) → "512x512".
	resp = e.post("/api/images/generate", `{"prompt":"square","size":256}`)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("numeric size status = %d", resp.StatusCode)
	}
	e.mu.Lock()
	raw = e.reqBody
	e.mu.Unlock()
	if err := json.Unmarshal([]byte(raw), &sent); err != nil {
		t.Fatal(err)
	}
	if sent["size"] != "256x256" {
		t.Errorf("numeric size normalization = %v", sent["size"])
	}
}

func TestGenerateOversizedResponseRejected(t *testing.T) {
	e := newEnv(t)
	e.seedProvider(t, "prov-1", e.upstreamURL+"/v1", "dall-e-3")

	old := maxUpstreamResponse
	maxUpstreamResponse = 4 << 10 // 4 KiB for the test
	t.Cleanup(func() { maxUpstreamResponse = old })

	fat := strings.Repeat("A", 32<<10) // way over
	e.respond(200, `{"data":[{"b64_json":"`+fat+`"}]}`)

	resp := e.post("/api/images/generate", `{"prompt":"fat one"}`)
	var out genResult
	decodeBody(t, resp, &out)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("oversized: status = %d, want 502", resp.StatusCode)
	}
}

func TestGenerateUpstreamErrorsMapTo502(t *testing.T) {
	e := newEnv(t)
	e.seedProvider(t, "prov-1", e.upstreamURL+"/v1", "dall-e-3")

	for _, tc := range []struct{ status int }{{500}, {401}, {429}} {
		e.respond(tc.status, `{"error":{"message":"secret internal detail /api?sk=LEAK"}}`)
		resp := e.post("/api/images/generate", `{"prompt":"x"}`)
		var out genResult
		decodeBody(t, resp, &out)
		if resp.StatusCode != http.StatusBadGateway {
			t.Fatalf("upstream %d → status %d, want 502", tc.status, resp.StatusCode)
		}
		if strings.Contains(out.Error, "LEAK") {
			t.Errorf("upstream body echoed to client: %q", out.Error)
		}
		if !strings.Contains(out.Error, "502") && !strings.Contains(out.Error, "HTTP") && !strings.Contains(out.Error, "image") {
			t.Errorf("unhelpful error: %q", out.Error)
		}
	}

	e.respond(200, `{"data":[]}`) // 200 but empty → 502 (nothing storable)
	resp := e.post("/api/images/generate", `{"prompt":"x"}`)
	if resp.StatusCode != http.StatusBadGateway {
		resp.Body.Close()
		t.Fatalf("empty data: status = %d, want 502", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestGenerateNoImageProvider400(t *testing.T) {
	e := newEnv(t)
	e.seedProvider(t, "chat-only", e.upstreamURL+"/v1", "gpt-4o") // only chat models

	resp := e.post("/api/images/generate", `{"prompt":"anything"}`)
	var out genResult
	decodeBody(t, resp, &out)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if !strings.Contains(out.Error, "provider_id") {
		t.Errorf("400 should hint at provider_id: %q", out.Error)
	}

	// Explicit unknown provider → 404 (IDOR rule: unknown = not found).
	resp = e.post("/api/images/generate", `{"prompt":"x","provider_id":"nope"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown provider = %d, want 404", resp.StatusCode)
	}
}

func TestGenerateBodyValidation(t *testing.T) {
	e := newEnv(t)
	e.seedProvider(t, "prov-1", e.upstreamURL+"/v1", "dall-e-3")
	e.respond(200, `{"data":[{"b64_json":"`+tinyPNG+`"}]}`)

	for _, body := range []string{
		`{"prompt":""}`,
		`{"prompt":"x","n":5}`,
		`{"prompt":"x","n":0,"size":"bogus"}`,
		`{"prompt":"x","size":"1024"}`,
	} {
		resp := e.post("/api/images/generate", body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("body %s → %d, want 400", body, resp.StatusCode)
		}
	}
}

func TestListImagesIsOwnOnly(t *testing.T) {
	e := newEnv(t)
	other := &store.User{Email: "other@example.com", Role: store.RoleUser}
	if err := e.st.Users.Insert(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	for i, u := range []*store.User{e.user, other} {
		f := &store.File{UserID: u.ID, Name: "own.png", Mime: "image/png", Size: 70,
			Path: "files/x.png", Kind: store.FileKindImage}
		if err := e.st.Files.Insert(t.Context(), f); err != nil {
			t.Fatal(err)
		}
		_ = i
	}
	resp := e.get("/api/images")
	var list genResult
	decodeBody(t, resp, &list)
	if len(list.Images) != 1 || list.Images[0].Name != "own.png" {
		t.Fatalf("list leaked or missed rows: %+v", list.Images)
	}
}

func TestValidSizeAndMime(t *testing.T) {
	for _, s := range []string{"1024x1024", "512x768", "auto", "256x256"} {
		if !validSize(s) {
			t.Errorf("validSize(%q) = false", s)
		}
	}
	for _, s := range []string{"", "x", "1024", "1024x", "ax1024", "1024x102400000", "1024*1024"} {
		if validSize(s) {
			t.Errorf("validSize(%q) = true", s)
		}
	}
	raw, err := base64.StdEncoding.DecodeString(tinyPNG)
	if err != nil {
		t.Fatal(err)
	}
	if m, err := imageMime(raw); err != nil || m != "image/png" {
		t.Errorf("imageMime(png) = %q, %v", m, err)
	}
	if _, err := imageMime([]byte("just some text, definitely not an image")); err == nil {
		t.Error("text payload accepted as image")
	}
}
