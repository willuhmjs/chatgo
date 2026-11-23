package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"chatgo/internal/auth"
	"chatgo/internal/config"
	"chatgo/internal/providers"
	"chatgo/internal/store"
)

// ---------- fixtures ----------

type env struct {
	t     *testing.T
	d     *Deps
	store *store.Store
	srv   http.Handler // Register(mux) behind auth.Require (the /api/* policy)
}

func newEnv(t *testing.T) *env {
	t.Helper()
	name := "file:mem_test_" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	st, err := store.Open(name, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	cfg := &config.Config{}
	a, err := auth.New(cfg, st)
	if err != nil {
		t.Fatalf("auth.New: %v", err)
	}
	d := &Deps{Cfg: cfg, Store: st, Auth: a, Providers: providers.NewClient(nil)}
	mux := http.NewServeMux()
	Register(mux, d) // also runs Init
	return &env{t: t, d: d, store: st, srv: a.Require(mux)}
}

// user creates a user + live session and returns (userID, cookie value).
func (e *env) user(email, role string) (string, string) {
	e.t.Helper()
	u := &store.User{Email: email, Name: "T", Role: role}
	if err := e.store.Users.Insert(context.Background(), u); err != nil {
		e.t.Fatalf("Users.Insert: %v", err)
	}
	sess, err := e.d.Auth.CreateSession(context.Background(), u.ID, "test-agent", "10.0.0.1")
	if err != nil {
		e.t.Fatalf("CreateSession: %v", err)
	}
	return u.ID, sess.Token
}

// do runs a request against the mounted (Require-wrapped) mux.
func (e *env) do(cookie, method, path, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: cookie})
	}
	w := httptest.NewRecorder()
	e.srv.ServeHTTP(w, r)
	return w
}

func (e *env) chat(userID string) string {
	e.t.Helper()
	c := &store.Chat{UserID: userID, ProviderID: "p", ModelID: "m"}
	if err := e.store.Chats.Create(context.Background(), c); err != nil {
		e.t.Fatalf("Chats.Create: %v", err)
	}
	return c.ID
}

func (e *env) msg(chatID, role, content string) {
	e.t.Helper()
	m := &store.Message{ChatID: chatID, Role: role, Content: content}
	if err := e.store.Messages.Append(context.Background(), m); err != nil {
		e.t.Fatalf("Messages.Append: %v", err)
	}
}

// setTaskModel points the "task_model" setting at a fake OpenAI-compatible
// upstream (httptest server) that always answers with completion.
func (e *env) setTaskModel(completion string) *upstreamCall {
	e.t.Helper()
	call := &upstreamCall{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		call.record(r.URL.Path, string(body))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"message":{"content":%s}}]}`, jsonOf(completion))
	}))
	e.t.Cleanup(srv.Close)

	p := &store.Provider{Name: "fake", BaseURL: srv.URL + "/v1", APIKey: "k", Enabled: true}
	if err := e.store.Providers.Insert(context.Background(), p); err != nil {
		e.t.Fatalf("Providers.Insert: %v", err)
	}
	if err := e.store.Settings.Set(context.Background(), "task_model", p.ID+":task-model-1"); err != nil {
		e.t.Fatalf("Settings.Set: %v", err)
	}
	return call
}

func jsonOf(s string) string { b, _ := json.Marshal(s); return string(b) }

// setFailingTaskModel points task_model at an upstream that always 500s.
func (e *env) setFailingTaskModel() *upstreamCall {
	e.t.Helper()
	call := &upstreamCall{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call.record(r.URL.Path, "")
		http.Error(w, `{"error":"boom"}`, http.StatusInternalServerError)
	}))
	e.t.Cleanup(srv.Close)
	p := &store.Provider{Name: "failing", BaseURL: srv.URL + "/v1", APIKey: "k", Enabled: true}
	if err := e.store.Providers.Insert(context.Background(), p); err != nil {
		e.t.Fatalf("Providers.Insert: %v", err)
	}
	if err := e.store.Settings.Set(context.Background(), "task_model", p.ID+":task-model-1"); err != nil {
		e.t.Fatalf("Settings.Set: %v", err)
	}
	return call
}

// upstreamCall captures what the fake upstream received. The real client
// calls Complete synchronously, so a response has fully landed before any
// test read — no locking needed.
type upstreamCall struct {
	path     string
	model    string
	system   string
	userText string
	hits     int
}

func (c *upstreamCall) record(path, body string) {
	c.hits++
	c.path = path
	var req struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		return
	}
	c.model = req.Model
	for _, m := range req.Messages {
		switch m.Role {
		case "system":
			c.system += m.Content
		case "user":
			c.userText += m.Content
		}
	}
}

// ---------- small assertion helpers ----------

func decodeList(t *testing.T, w *httptest.ResponseRecorder) listResponse {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body)
	}
	var lr listResponse
	if err := json.Unmarshal(w.Body.Bytes(), &lr); err != nil {
		t.Fatalf("decode list: %v (%s)", err, w.Body)
	}
	return lr
}

func decodeMemory(t *testing.T, w *httptest.ResponseRecorder) Memory {
	t.Helper()
	var m Memory
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode memory: %v (%s)", err, w.Body)
	}
	return m
}

func mustStatus(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("status = %d, want %d (body %s)", w.Code, want, w.Body)
	}
}
