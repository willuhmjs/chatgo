package apimgr

import (
	"errors"
	"net/http"
	"strings"

	"chatgo/internal/api/httpx"
	"chatgo/internal/store"
)

// User admin surface: GET /api/users, POST /api/users/{id}/approve,
// PUT /api/users/{id}/role. Password hashes are NEVER serialized out.

type usersH struct{ d *Deps }

func registerUsers(mux *http.ServeMux, d *Deps) {
	h := &usersH{d: d}
	mux.HandleFunc("GET /api/users", h.handleList)
	mux.HandleFunc("POST /api/users/{id}/approve", h.handleApprove)
	mux.HandleFunc("PUT /api/users/{id}/role", h.handleRole)
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
	httpx.JSON(w, http.StatusOK, userView(u))
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
