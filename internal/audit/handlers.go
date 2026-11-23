package audit

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"strconv"

	"chatgo/internal/api/httpx"
	"chatgo/internal/store"
)

// Resolver is the minimal slice of *auth.Auth this package needs to identify
// the caller attached by auth middleware (kept as an interface so audit does
// not import internal/auth, which imports audit).
type Resolver interface {
	UserFrom(ctx context.Context) (*store.User, bool)
}

// Deps is the wiring struct the integrator passes at startup.
type Deps struct {
	DB   *sql.DB
	Auth Resolver
}

// Register mounts the audit surface on the shared mux and ensures the table
// exists. Routes land under /api/audit, which api.AuthPolicy already gates
// behind Require; the admin check below is the (defense-in-depth) 403 layer —
// per the coordination law, admin policy inside a feature = read the user
// from context, 403 unless role==admin.
func Register(mux *http.ServeMux, d *Deps) {
	if err := Init(d.DB); err != nil {
		// Non-fatal by design (fire-and-safe everywhere); Record keeps
		// warning and GET answers 500 until the DB recovers.
		slog.Error("audit: table init failed", "err", err)
	}
	h := &handler{db: d.DB, auth: d.Auth}
	mux.HandleFunc("GET /api/audit", h.handleList)
}

type handler struct {
	db   *sql.DB
	auth Resolver
}

const (
	defaultLimit = 50
	maxLimit     = 100
)

// GET /api/audit?cursor=&limit=50 — admin only, newest-first.
// cursor is the last id of the previous page (exclusive); next_cursor is the
// id to pass next, or null at the end.
func (h *handler) handleList(w http.ResponseWriter, r *http.Request) {
	u, ok := h.auth.UserFrom(r.Context())
	if !ok {
		httpx.ErrorJSON(w, http.StatusUnauthorized, "authentication required")
		return
	}
	if !u.IsAdmin() {
		httpx.ErrorJSON(w, http.StatusForbidden, "admin access required")
		return
	}

	cursor := int64(0)
	if c := r.URL.Query().Get("cursor"); c != "" {
		v, err := strconv.ParseInt(c, 10, 64)
		if err != nil || v < 0 {
			httpx.ErrorJSON(w, http.StatusBadRequest, "cursor must be a non-negative integer")
			return
		}
		cursor = v
	}
	limit := defaultLimit
	if l := r.URL.Query().Get("limit"); l != "" {
		v, err := strconv.Atoi(l)
		if err != nil || v < 1 || v > maxLimit {
			httpx.ErrorJSON(w, http.StatusBadRequest, "limit must be between 1 and 100")
			return
		}
		limit = v
	}

	// Parameterized (contract: no string-built SQL). cursor 0 = start at the
	// newest row; rows paginate on the integer PK descending.
	var (
		rows *sql.Rows
		err  error
	)
	if cursor == 0 {
		rows, err = h.db.QueryContext(r.Context(),
			`SELECT id, ts, user_id, email, action, detail, ip FROM audit_log ORDER BY id DESC LIMIT ?1`, limit)
	} else {
		rows, err = h.db.QueryContext(r.Context(),
			`SELECT id, ts, user_id, email, action, detail, ip FROM audit_log WHERE id < ?1 ORDER BY id DESC LIMIT ?2`,
			cursor, limit)
	}
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	defer rows.Close()

	items := make([]Entry, 0, limit)
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.Ts, &e.UserID, &e.Email, &e.Action, &e.Detail, &e.IP); err != nil {
			httpx.InternalError(w, r, err)
			return
		}
		items = append(items, e)
	}
	if err := rows.Err(); err != nil {
		httpx.InternalError(w, r, err)
		return
	}

	out := map[string]any{"items": items}
	if len(items) == limit {
		// Decimal STRING (web/src/lib/admin/audit-api.ts types next_cursor as
		// string|null); the cursor param parser accepts the same back.
		out["next_cursor"] = strconv.FormatInt(items[len(items)-1].ID, 10)
	} else {
		out["next_cursor"] = nil
	}
	httpx.JSON(w, http.StatusOK, out)
}
