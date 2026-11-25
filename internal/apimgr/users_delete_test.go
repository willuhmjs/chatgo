package apimgr

// Tests for account deletion (Open WebUI parity): DELETE /api/users/{id}
// (admin) and DELETE /api/me (self). Guards: no self-delete via the admin
// endpoint, never the last admin, role gates, cross-plane cleanup hook runs
// before the row goes away, and the user's me.<id>.* settings die with them.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"chatgo/internal/store"
)

//  1. Admin deletes a regular user: 204, row gone, hook fired with the id,
//     me-settings gone.
func TestAdminDeleteUser(t *testing.T) {
	d, _ := testDeps(t)
	ctx := context.Background()
	admin := mustUser(t, d.Store, "ad@e.com", store.RoleAdmin)
	victim := mustUser(t, d.Store, "vic@e.com", store.RoleUser)
	if err := d.Store.Settings.Set(ctx, "me."+victim.ID+".theme", "dark"); err != nil {
		t.Fatal(err)
	}

	var hooked []string
	SetUserDeleteHook(func(_ context.Context, id string) error {
		hooked = append(hooked, id)
		return nil
	})
	t.Cleanup(func() { SetUserDeleteHook(nil) })

	mux := http.NewServeMux()
	registerUsers(mux, d)
	r := httptest.NewRequest("DELETE", "/api/users/"+victim.ID, nil)
	rec := httptest.NewRecorder()
	d.Auth.Attach(mux).ServeHTTP(rec, sessionRequest(t, d, r, admin))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204, body %s", rec.Code, rec.Body)
	}
	if len(hooked) == 1 && hooked[0] == victim.ID {
		// ok — hook ran first
	} else {
		t.Fatalf("hook = %v, want [%s]", hooked, victim.ID)
	}
	if _, err := d.Store.Users.GetByID(ctx, victim.ID); err == nil {
		t.Fatal("victim still exists")
	}
	if v, err := d.Store.Settings.Get(ctx, "me."+victim.ID+".theme"); err != nil || v != nil {
		t.Fatalf("me-setting survived: %v (err %v)", v, err)
	}
	if _, err := d.Store.Users.GetByID(ctx, admin.ID); err != nil {
		t.Fatalf("admin vanished too: %v", err)
	}
}

// 2. Admin must not delete themselves through the admin endpoint → 400.
func TestAdminSelfDeleteViaAdminEndpointRejected(t *testing.T) {
	d, _ := testDeps(t)
	admin := mustUser(t, d.Store, "ad2@e.com", store.RoleAdmin)
	mux := http.NewServeMux()
	registerUsers(mux, d)
	r := httptest.NewRequest("DELETE", "/api/users/"+admin.ID, nil)
	rec := httptest.NewRecorder()
	d.Auth.Attach(mux).ServeHTTP(rec, sessionRequest(t, d, r, admin))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body %s", rec.Code, rec.Body)
	}
}

//  3. The last admin cannot be deleted (neither by /api/me nor anyone else);
//     with two admins one may go.
func TestLastAdminUndeletable(t *testing.T) {
	d, _ := testDeps(t)
	only := mustUser(t, d.Store, "only@e.com", store.RoleAdmin)
	mux := http.NewServeMux()
	registerUsers(mux, d)

	r := httptest.NewRequest("DELETE", "/api/me", nil)
	rec := httptest.NewRecorder()
	d.Auth.Attach(mux).ServeHTTP(rec, sessionRequest(t, d, r, only))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("last admin /api/me: status = %d, want 400, body %s", rec.Code, rec.Body)
	}

	second := mustUser(t, d.Store, "second@e.com", store.RoleAdmin)
	r = httptest.NewRequest("DELETE", "/api/me", nil)
	rec = httptest.NewRecorder()
	d.Auth.Attach(mux).ServeHTTP(rec, sessionRequest(t, d, r, second))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("second admin /api/me: status = %d, want 204, body %s", rec.Code, rec.Body)
	}
	if _, err := d.Store.Users.GetByID(context.Background(), second.ID); err == nil {
		t.Fatal("second admin still exists")
	}
}

//  4. Plain user self-delete → 204, row gone; non-admin hitting the admin
//     endpoint → 403 (handler-level RequireAdmin; the policy layer would
//     already 403 but the handler guard is tested standalone).
func TestMeDeleteAndRoleGate(t *testing.T) {
	d, _ := testDeps(t)
	SetUserDeleteHook(nil)
	u := mustUser(t, d.Store, "me@e.com", store.RoleUser)
	mux := http.NewServeMux()
	registerUsers(mux, d)

	r := httptest.NewRequest("DELETE", "/api/me", nil)
	rec := httptest.NewRecorder()
	d.Auth.Attach(mux).ServeHTTP(rec, sessionRequest(t, d, r, u))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("me delete: status = %d, want 204, body %s", rec.Code, rec.Body)
	}
	if _, err := d.Store.Users.GetByID(context.Background(), u.ID); err == nil {
		t.Fatal("user still exists")
	}

	victim := mustUser(t, d.Store, "v2@e.com", store.RoleUser)
	r = httptest.NewRequest("DELETE", "/api/users/"+victim.ID, nil)
	rec = httptest.NewRecorder()
	d.Auth.Attach(mux).ServeHTTP(rec, sessionRequest(t, d, r, victim))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin delete: status = %d, want 403", rec.Code)
	}
}
