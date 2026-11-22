package audit_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"chatgo/internal/audit"
	"chatgo/internal/store"
)

var testSecret = []byte("abcdef0123456789abcdef0123456789")

func openDB(t *testing.T) *store.Store {
	t.Helper()
	name := "file:mem_audit_" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()) +
		"?mode=memory&cache=shared"
	st, err := store.Open(name, testSecret)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := audit.Init(st.DB); err != nil {
		t.Fatalf("audit.Init: %v", err)
	}
	return st
}

// fakeResolver stands in for *auth.Auth (audit must not import auth).
type fakeResolver struct {
	u  *store.User
	ok bool
}

func (f fakeResolver) UserFrom(context.Context) (*store.User, bool) { return f.u, f.ok }

func TestInitIdempotent(t *testing.T) {
	st := openDB(t)
	if err := audit.Init(st.DB); err != nil { // CREATE IF NOT EXISTS
		t.Fatalf("second Init: %v", err)
	}
}

func TestRecordWritesRows(t *testing.T) {
	st := openDB(t)
	ctx := context.Background()
	u := &store.User{ID: "u1", Email: "alice@example.com"}

	audit.Record(ctx, st.DB, u, audit.ActionLoginSuccess, "method=password", "10.0.0.1")
	audit.Record(ctx, st.DB, nil, audit.ActionLogout, "", "10.0.0.2") // nil user must not panic
	audit.RecordIdentity(ctx, st.DB, "", "victim@x.io", audit.ActionLoginFail, "invalid credentials", "10.0.0.3")

	rows, err := st.DB.Query(`SELECT user_id, email, action, detail, ip FROM audit_log ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type rec struct{ uid, email, action, detail, ip string }
	var got []rec
	for rows.Next() {
		var r rec
		if err := rows.Scan(&r.uid, &r.email, &r.action, &r.detail, &r.ip); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	want := []rec{
		{"u1", "alice@example.com", "login.success", "method=password", "10.0.0.1"},
		{"", "", "logout", "", "10.0.0.2"},
		{"", "victim@x.io", "login.fail", "invalid credentials", "10.0.0.3"},
	}
	if len(got) != len(want) {
		t.Fatalf("rows = %d, want %d (%+v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestSelfTrim: with a shrunk retain window (the parametric variant), inserts
// past the budget prune the oldest rows — retain+1 rows survive.
func TestSelfTrim(t *testing.T) {
	st := openDB(t)
	ctx := context.Background()
	const retain = 3
	for i := range 10 {
		audit.RecordWithRetain(ctx, st.DB, "", "", fmt.Sprintf("act-%d", i), "", "", retain)
	}
	var n int
	var minID, maxID int64
	if err := st.DB.QueryRow(`SELECT COUNT(*), MIN(id), MAX(id) FROM audit_log`).Scan(&n, &minID, &maxID); err != nil {
		t.Fatal(err)
	}
	// max id is 10 after ten inserts; the DELETE keeps id ≥ 10-3.
	if n != retain+1 || minID != 7 || maxID != 10 {
		t.Fatalf("trim kept %d rows [%d..%d], want %d rows [7..10]", n, minID, maxID, retain+1)
	}
}

// TestRecordDetachedContext: the row lands even when the caller's context is
// already cancelled (fire-and-safe survives response completion).
func TestRecordDetachedContext(t *testing.T) {
	st := openDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	audit.Record(ctx, st.DB, &store.User{ID: "u9", Email: "gone@x.io"}, "logout", "", "ip")
	var n int
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'logout'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("rows after cancelled-ctx Record = %d, want 1", n)
	}
}

type listPage struct {
	Items []struct {
		ID     int64  `json:"id"`
		Action string `json:"action"`
		Email  string `json:"email"`
	} `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

func TestGETAudit(t *testing.T) {
	st := openDB(t)
	ctx := context.Background()
	for i := range 5 {
		audit.Record(ctx, st.DB, &store.User{ID: "u1", Email: "a@x.io"}, fmt.Sprintf("act-%d", i), "", "ip")
	}

	admin := &store.User{ID: "u1", Email: "a@x.io", Role: store.RoleAdmin}
	user := &store.User{ID: "u2", Email: "u@x.io", Role: store.RoleUser}

	// full-body fetch helper
	fetch := func(t *testing.T, resolver audit.Resolver, target string) (int, listPage) {
		t.Helper()
		mux := http.NewServeMux()
		audit.Register(mux, &audit.Deps{DB: st.DB, Auth: resolver})
		srv := httptest.NewServer(mux)
		t.Cleanup(srv.Close)
		resp, err := http.Get(srv.URL + target)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var page listPage
		dec := json.NewDecoder(resp.Body)
		if resp.StatusCode == 200 {
			if err := dec.Decode(&page); err != nil {
				t.Fatalf("decode: %v", err)
			}
		}
		return resp.StatusCode, page
	}

	if code, _ := fetch(t, fakeResolver{}, "/api/audit"); code != http.StatusUnauthorized {
		t.Errorf("anonymous: status = %d, want 401", code)
	}
	if code, _ := fetch(t, fakeResolver{u: user, ok: true}, "/api/audit"); code != http.StatusForbidden {
		t.Errorf("non-admin: status = %d, want 403", code)
	}

	code, page := fetch(t, fakeResolver{u: admin, ok: true}, "/api/audit")
	if code != http.StatusOK {
		t.Fatalf("admin: status = %d", code)
	}
	if len(page.Items) != 5 {
		t.Fatalf("items = %d, want 5", len(page.Items))
	}
	for i := 1; i < len(page.Items); i++ {
		if page.Items[i-1].ID <= page.Items[i].ID {
			t.Fatalf("not newest-first: %d before %d", page.Items[i-1].ID, page.Items[i].ID)
		}
	}
	if page.NextCursor != nil {
		t.Errorf("short page should have null next_cursor, got %v", *page.NextCursor)
	}

	// Cursor pagination: first page (2 rows) + next_cursor, then tail.
	code, p1 := fetch(t, fakeResolver{u: admin, ok: true}, "/api/audit?limit=2")
	if code != http.StatusOK || len(p1.Items) != 2 || p1.NextCursor == nil {
		t.Fatalf("page1 = %d, %d items, next=%v", code, len(p1.Items), p1.NextCursor)
	}
	_, p2 := fetch(t, fakeResolver{u: admin, ok: true},
		fmt.Sprintf("/api/audit?limit=2&cursor=%s", *p1.NextCursor))
	if len(p2.Items) != 2 {
		t.Fatalf("page2 items = %d, want 2", len(p2.Items))
	}
	if p2.Items[0].ID >= p1.Items[len(p1.Items)-1].ID {
		t.Errorf("cursor did not advance the window")
	}
	if code, _ := fetch(t, fakeResolver{u: admin, ok: true}, "/api/audit?limit=101"); code != http.StatusBadRequest {
		t.Errorf("limit=101: status = %d, want 400", code)
	}
	if code, _ := fetch(t, fakeResolver{u: admin, ok: true}, "/api/audit?cursor=abc"); code != http.StatusBadRequest {
		t.Errorf("bad cursor: status = %d, want 400", code)
	}
}
