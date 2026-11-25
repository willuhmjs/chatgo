package apimgr

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"chatgo/internal/api/httpx"
	"chatgo/internal/audit"
	"chatgo/internal/store"
)

// User admin surface: GET /api/users, POST /api/users/{id}/approve,
// PUT /api/users/{id}/role, POST /api/users/{id}/reset-2fa. Password hashes
// are NEVER serialized out — the sole exception is the one-time recovery
// password from reset-2fa, which exists only in that response body.

type usersH struct{ d *Deps }

func registerUsers(mux *http.ServeMux, d *Deps) {
	h := &usersH{d: d}
	mux.HandleFunc("GET /api/users", h.handleList)
	mux.HandleFunc("POST /api/users/{id}/approve", h.handleApprove)
	mux.HandleFunc("PUT /api/users/{id}/role", h.handleRole)
	mux.HandleFunc("POST /api/users/{id}/reset-2fa", h.handleReset2FA)
	mux.HandleFunc("DELETE /api/users/{id}", h.handleDelete)
	mux.HandleFunc("DELETE /api/me", h.handleMeDelete)
}

// userDeleteHook lets the integrator plug cross-plane cleanup into account
// deletion (kb collections, memories, on-disk file blobs — tables that
// feature planes own without an FK to users, so the store cascade cannot
// reach them). Runs BEFORE the user row goes away; a failure aborts the
// delete (nothing removed yet, retryable). Same integrator-binding pattern
// as chat.RegisterEnricher / the memory enricher in server.go.
var userDeleteHook func(ctx context.Context, userID string) error

// SetUserDeleteHook installs that cleanup (nil clears it). Called once at
// boot; not for runtime use.
func SetUserDeleteHook(fn func(ctx context.Context, userID string) error) {
	userDeleteHook = fn
}

// deleteUser runs the full removal for an already-validated target:
// cross-plane cleanup hook → row delete (FK cascade takes the core tables,
// Users.Delete also clears me.<id>.* settings) → audit trail entry.
func (h *usersH) deleteUser(r *http.Request, target, actor *store.User) error {
	ctx := r.Context()
	if userDeleteHook != nil {
		if err := userDeleteHook(ctx, target.ID); err != nil {
			return err
		}
	}
	if err := h.d.Store.Users.Delete(ctx, target.ID); err != nil {
		return err
	}
	audit.Record(ctx, h.d.Store.DB, actor, "user.delete",
		"user_id="+target.ID+" email="+target.Email, h.d.Auth.RequestIP(r))
	return nil
}

// handleDelete: DELETE /api/users/{id} — admin removes an account outright
// (Open WebUI parity). Guards: no self-delete here (use /api/me — the
// profile path, which keeps the "who pulled the trigger on themselves"
// audit line honest), and never the last admin (same zero-admin guard as
// role demotion).
func (h *usersH) handleDelete(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.d.requireAdmin(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if id == actor.ID {
		httpx.ErrorJSON(w, http.StatusBadRequest, "delete your own account from your settings (DELETE /api/me)")
		return
	}
	target, err := h.d.Store.Users.GetByID(r.Context(), id)
	if err != nil {
		userLookupFail(w, r, err)
		return
	}
	if wroteErr := h.lastAdminGuard(w, r, target); wroteErr {
		return
	}
	if err := h.deleteUser(r, target, actor); err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleMeDelete: DELETE /api/me — self-service account deletion (OWUI
// "delete personal account"). Same cleanup path; the session cookie is
// cleared explicitly even though the sessions rows cascade away with the
// user (a dead-but-still-sent cookie would only confuse the browser).
func (h *usersH) handleMeDelete(w http.ResponseWriter, r *http.Request) {
	u, ok := h.d.requireUser(w, r)
	if !ok {
		return
	}
	if wroteErr := h.lastAdminGuard(w, r, u); wroteErr {
		return
	}
	if err := h.deleteUser(r, u, u); err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	h.d.Auth.ClearSessionCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

// lastAdminGuard answers 400 when removing target would leave the instance
// with zero admins. Returns true when it wrote the error response.
func (h *usersH) lastAdminGuard(w http.ResponseWriter, r *http.Request, target *store.User) bool {
	if target.Role != store.RoleAdmin {
		return false
	}
	admins, err := h.countAdmins(r)
	if err != nil {
		httpx.InternalError(w, r, err)
		return true
	}
	if admins <= 1 {
		httpx.ErrorJSON(w, http.StatusBadRequest, "cannot delete the last admin")
		return true
	}
	return false
}

// userJSON mirrors web/src/lib/api.ts User (no password_hash field exists —
// the type makes leaking impossible).
type userJSON struct {
	ID           string `json:"id"`
	Email        string `json:"email"`
	Name         string `json:"name"`
	Role         string `json:"role"`
	AvatarURL    string `json:"avatar_url,omitempty"`
	CreatedAt    int64  `json:"created_at"`
	LastActiveAt int64  `json:"last_active_at"`
}

func userView(u *store.User) userJSON {
	return userJSON{
		ID: u.ID, Email: u.Email, Name: u.Name, Role: u.Role,
		AvatarURL: u.AvatarURL, CreatedAt: u.CreatedAt, LastActiveAt: u.LastActiveAt,
	}
}

// handleList: GET /api/users — every account including role:"pending" rows,
// which is how the admin UI gets its pending list (api.ts expects a bare
// User[]; pending is just a role filter client-side).
func (h *usersH) handleList(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.d.requireAdmin(w, r); !ok {
		return
	}
	h.list(w, r)
}

func (h *usersH) list(w http.ResponseWriter, r *http.Request) {
	users, err := h.d.Store.Users.List(r.Context())
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	out := make([]userJSON, 0, len(users))
	for _, u := range users {
		out = append(out, userView(u))
	}
	httpx.JSON(w, http.StatusOK, out)
}

// handleApprove: POST /api/users/{id}/approve — pending → user.
func (h *usersH) handleApprove(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.d.requireAdmin(w, r); !ok {
		return
	}
	h.approve(w, r, r.PathValue("id"))
}

func (h *usersH) approve(w http.ResponseWriter, r *http.Request, id string) {
	u, err := h.d.Store.Users.GetByID(r.Context(), id)
	if err != nil {
		userLookupFail(w, r, err)
		return
	}
	if u.Role != store.RolePending {
		httpx.ErrorJSON(w, http.StatusConflict, "user is not pending")
		return
	}
	if err := h.d.Store.Users.UpdateRole(r.Context(), u.ID, store.RoleUser); err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	u.Role = store.RoleUser
	actor, _ := h.d.Auth.UserFrom(r.Context())
	audit.Record(r.Context(), h.d.Store.DB, actor, "user.approve", "user_id="+id, h.d.Auth.RequestIP(r))
	httpx.JSON(w, http.StatusOK, userView(u))
}

// handleRole: PUT /api/users/{id}/role {role:"admin|user|pending"}.
// Guard: the last admin can never be demoted (any role change that would
// leave zero admins — including self-demotion — is rejected 400).
func (h *usersH) handleRole(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.d.requireAdmin(w, r); !ok {
		return
	}
	var in struct {
		Role string `json:"role"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		httpx.ErrorJSON(w, http.StatusBadRequest, `body must be {"role":"admin|user|pending"}`)
		return
	}
	switch in.Role {
	case store.RoleAdmin, store.RoleUser, store.RolePending:
	default:
		httpx.ErrorJSON(w, http.StatusBadRequest, "role must be admin, user or pending")
		return
	}
	h.setRole(w, r, r.PathValue("id"), in.Role)
}

func (h *usersH) setRole(w http.ResponseWriter, r *http.Request, id, role string) {
	u, err := h.d.Store.Users.GetByID(r.Context(), id)
	if err != nil {
		userLookupFail(w, r, err)
		return
	}
	if u.Role == store.RoleAdmin && role != store.RoleAdmin {
		admins, err := h.countAdmins(r)
		if err != nil {
			httpx.InternalError(w, r, err)
			return
		}
		if admins <= 1 {
			httpx.ErrorJSON(w, http.StatusBadRequest, "cannot demote the last admin")
			return
		}
	}
	if err := h.d.Store.Users.UpdateRole(r.Context(), u.ID, role); err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	u.Role = role
	actor, _ := h.d.Auth.UserFrom(r.Context())
	audit.Record(r.Context(), h.d.Store.DB, actor, "user.role", "user_id="+id+" role="+role, h.d.Auth.RequestIP(r))
	httpx.JSON(w, http.StatusOK, userView(u))
}

// handleReset2FA: POST /api/users/{id}/reset-2fa — admin-driven 2FA lockout
// recovery (auth.AdminReset2FA does the work): the target's TOTP secret is
// cleared, ALL their sessions are revoked, and a local account gets a fresh
// one-time password returned HERE AND NOWHERE ELSE (audit detail carries
// only user_id + method — never the password). OIDC-only accounts get no
// password: new_password is simply absent from the response.
func (h *usersH) handleReset2FA(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.d.requireAdmin(w, r)
	if !ok {
		return
	}
	res, err := h.d.Auth.AdminReset2FA(r.Context(), r.PathValue("id"), actor, h.d.Auth.RequestIP(r))
	if err != nil {
		userLookupFail(w, r, err) // unknown id → 404, same as approve/role
		return
	}
	out := map[string]any{
		"ok":               true,
		"was_enabled":      res.WasEnabled,
		"sessions_revoked": res.SessionsRevoked,
	}
	if res.PasswordSet {
		out["new_password"] = res.NewPassword // shown once; never logged or stored plaintext
	}
	httpx.JSON(w, http.StatusOK, out)
}

// countAdmins counts admin rows (repo has no count method; homelab-scale
// List is fine).
func (h *usersH) countAdmins(r *http.Request) (int, error) {
	users, err := h.d.Store.Users.List(r.Context())
	if err != nil {
		return 0, err
	}
	n := 0
	for _, u := range users {
		if strings.EqualFold(u.Role, store.RoleAdmin) {
			n++
		}
	}
	return n, nil
}

func userLookupFail(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrNotFound) {
		httpx.ErrorJSON(w, http.StatusNotFound, "user not found")
		return
	}
	httpx.InternalError(w, r, err)
}
