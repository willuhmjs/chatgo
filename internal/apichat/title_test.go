// Regenerate-title endpoint tests. Reuses the suggest env harness (same
// task-model wiring), swaps the route. Prefix: TestAPI.

package apichat

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestAPI_RegenerateTitle(t *testing.T) {
	prov := &suggestTestProv{text: "\"Vector DB Basics.\"\nextra lines ignored"}
	d, alice, bob, c := suggestEnv(t, prov, "p1:tm1", true)
	url := "/api/chats/" + c.ID + "/title"

	rec := apiServe(t, d, alice, "POST", url, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("title regen: %d %s", rec.Code, rec.Body.String())
	}
	got := decodeJSON[map[string]string](t, rec)["title"]
	if got != "Vector DB Basics" { // quotes stripped, first line kept, period trimmed
		t.Fatalf("title = %q, want %q", got, "Vector DB Basics")
	}
	// Persisted on the chat row (not just echoed).
	ctx := t.Context()
	fresh, err := d.Store.Chats.Get(ctx, c.ID)
	if err != nil || fresh.Title != got {
		t.Fatalf("stored title = %q (err %v), want %q", fresh.Title, err, got)
	}

	// Foreign chat → 404 (no existence leak).
	if rec := apiServe(t, d, bob, "POST", url, nil); rec.Code != http.StatusNotFound {
		t.Errorf("foreign: %d", rec.Code)
	}
	// Anonymous → 401.
	if rec := apiServe(t, d, nil, "POST", url, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("anon: %d", rec.Code)
	}
	// Oversized body → 400 (decodeEmptyBody caps at suggestBodyCap).
	if rec := apiServe(t, d, alice, "POST", url, `{"x":"`+strings.Repeat("y", 4096)+`"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("big body: %d", rec.Code)
	}
}

func TestAPI_RegenerateTitleTaskModelFailures503(t *testing.T) {
	prov := &suggestTestProv{err: errors.New("upstream down")}
	d, alice, _, c := suggestEnv(t, prov, "p1:tm1", true)
	url := "/api/chats/" + c.ID + "/title"
	if rec := apiServe(t, d, alice, "POST", url, nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("upstream error: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAPI_RegenerateTitleNoTaskModel503(t *testing.T) {
	// No task model configured at all → 503 (never the truncation fallback).
	d, alice, _, c := suggestEnv(t, &suggestTestProv{text: "x"}, "", true)
	url := "/api/chats/" + c.ID + "/title"
	if rec := apiServe(t, d, alice, "POST", url, nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no task model: %d", rec.Code)
	}
}
