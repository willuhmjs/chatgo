package apimgr

import (
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
