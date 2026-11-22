package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"chatgo/internal/config"
)

func searxngPayload(n int) []byte {
	type res struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Content string `json:"content"`
	}
	p := struct {
		Results []res `json:"results"`
	}{}
	for i := range n {
		p.Results = append(p.Results, res{
			Title:   fmt.Sprintf("Result %d", i),
			URL:     fmt.Sprintf("https://example.com/%d", i),
			Content: fmt.Sprintf("snippet %d", i),
		})
	}
	b, _ := json.Marshal(p)
	return b
}

func TestSearchMapping(t *testing.T) {
	var gotPath, gotQ, gotFormat, gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQ = r.URL.Query().Get("q")
		gotFormat = r.URL.Query().Get("format")
		gotUA = r.UserAgent()
		w.Header().Set("Content-Type", "application/json")
		w.Write(searxngPayload(7))
	}))
	defer srv.Close()

	cfg := &config.Config{SearXNGURL: srv.URL, AllowLoopback: true}
	svc := New(cfg)
	if !svc.Enabled {
		t.Fatal("Enabled = false with SEARXNG_URL set")
	}

	// n<=0 defaults to the 5-result cap (contract: ≤5 results).
	results, err := svc.Search(context.Background(), "golang wasm", 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if gotPath != "/search" || gotQ != "golang wasm" || gotFormat != "json" || gotUA != "chatgo/1" {
		t.Fatalf("request: path=%q q=%q format=%q ua=%q", gotPath, gotQ, gotFormat, gotUA)
	}
	if len(results) != MaxResults {
		t.Fatalf("len(results) = %d, want %d", len(results), MaxResults)
	}
	if results[0].Title != "Result 0" || results[0].URL != "https://example.com/0" || results[0].Content != "snippet 0" {
		t.Fatalf("mapping mismatch: %+v", results[0])
	}

	// Explicit smaller n is honored.
	results, err = svc.Search(context.Background(), "x", 2)
	if err != nil {
		t.Fatalf("Search(n=2): %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
}

func TestSearchFollowsRevalidatedRedirect(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/final", http.StatusFound)
	})
	mux.HandleFunc("/final", func(w http.ResponseWriter, _ *http.Request) {
		w.Write(searxngPayload(1))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	svc := New(&config.Config{SearXNGURL: srv.URL, AllowLoopback: true})
	results, err := svc.Search(context.Background(), "q", 1)
	if err != nil {
		t.Fatalf("Search through redirect: %v", err)
	}
	if len(results) != 1 || results[0].Title != "Result 0" {
		t.Fatalf("results = %+v", results)
	}
}

func TestSearchDisabled(t *testing.T) {
	svc := New(&config.Config{})
	if svc.Enabled {
		t.Fatal("Enabled = true without SEARXNG_URL")
	}
	if _, err := svc.Search(context.Background(), "q", 5); err == nil {
		t.Fatal("Search on disabled service succeeded")
	}
}

func TestSearchUpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	svc := New(&config.Config{SearXNGURL: srv.URL, AllowLoopback: true})
	if _, err := svc.Search(context.Background(), "q", 5); err == nil {
		t.Fatal("Search against 500 succeeded")
	}
}

func TestComposeContext(t *testing.T) {
	long := strings.Repeat("é", 2000) // multibyte: truncation must be rune-safe
	got := ComposeContext([]Result{
		{Title: "First `title`", URL: "https://a.test/1", Content: long},
		{Title: "", URL: "https://b.test/2", Content: "short snippet"},
	})
	if !strings.HasPrefix(got, "Web search results") || !strings.HasSuffix(got, "```\n") {
		t.Fatalf("not a fenced block:\n%.120s", got)
	}
	if strings.Count(got, "```") != 2 { // fence opener + closer only
		t.Fatalf("backticks in content escaped the fence:\n%s", got)
	}
	for _, want := range []string{"[1] First 'title'", "URL: https://a.test/1", "[2] (untitled)", "URL: https://b.test/2", "Snippet: short snippet"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	// 1200-rune clamp with ellipsis marker.
	clamped := string([]rune(long)[:snippetChars]) + "…"
	if !strings.Contains(got, clamped) {
		t.Error("snippet not clamped to 1200 runes + ellipsis")
	}
	if strings.Count(got, "é") > snippetChars {
		t.Errorf("long snippet not truncated (%d é runes)", strings.Count(got, "é"))
	}
	if ComposeContext(nil) != "" {
		t.Error("empty results should compose to empty string")
	}
}
