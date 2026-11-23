// Admin ratings dashboard: GET /api/admin/ratings — instance-wide 👍/👎
// aggregates (per-model rollup + totals) plus a recent-feedback page with
// message excerpts. Backs the admin console's "Evaluations" tab
// (Open WebUI's evals idea, trimmed to one bounded query set).

package library

import (
	"net/http"

	"chatgo/internal/api/httpx"
	"chatgo/internal/store"
)

// maxExcerpt caps the message preview shipped in the feedback list — the
// dashboard links back into the chat rather than shipping whole transcripts.
const maxExcerpt = 240

func registerRatingsAdmin(mux *http.ServeMux, d *Deps) {
	mux.HandleFunc("GET /api/admin/ratings", d.handleAdminRatings)
}

// requireAdminHere is library-local (AuthPolicy also gates /api/admin/ at the
// middleware; this stays defense-in-depth, same posture as apimgr).
func (d *Deps) requireAdminHere(w http.ResponseWriter, r *http.Request) (*store.User, bool) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return nil, false
	}
	if u.Role != "admin" {
		httpx.ErrorJSON(w, http.StatusForbidden, "admin access required")
		return nil, false
	}
	return u, true
}

// modelJSON is one per-model rollup row.
type modelJSON struct {
	ModelID    string `json:"model_id"`
	ProviderID string `json:"provider_id"`
	Up         int    `json:"up"`
	Down       int    `json:"down"`
}

// feedbackJSON is one recent rating with just enough context to act on it.
type feedbackJSON struct {
	ID        string `json:"id"`
	Value     int    `json:"value"`
	Comment   string `json:"comment,omitempty"`
	CreatedAt int64  `json:"created_at"`
	ChatID    string `json:"chat_id"`
	ChatTitle string `json:"chat_title"`
	MessageID string `json:"message_id"`
	Excerpt   string `json:"excerpt"`
	ModelID   string `json:"model_id"`
	UserName  string `json:"user_name"`
}

func (d *Deps) handleAdminRatings(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.requireAdminHere(w, r); !ok {
		return
	}
	ctx := r.Context()

	var totals struct {
		Up    int `json:"up"`
		Down  int `json:"down"`
		Score int `json:"-"`
	}
	if err := d.Store.DB.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(value = 1), 0), COALESCE(SUM(value = -1), 0),
		        COALESCE(SUM(value), 0) FROM ratings`).
		Scan(&totals.Up, &totals.Down, &totals.Score); err != nil {
		d.fail(w, r, err)
		return
	}

	rows, err := d.Store.DB.QueryContext(ctx,
		`SELECT c.model_id, c.provider_id,
		        COALESCE(SUM(r.value = 1), 0), COALESCE(SUM(r.value = -1), 0)
		 FROM ratings r JOIN chats c ON c.id = r.chat_id
		 GROUP BY c.provider_id, c.model_id
		 ORDER BY (SUM(r.value = 1) + SUM(r.value = -1)) DESC
		 LIMIT 100`)
	if err != nil {
		d.fail(w, r, err)
		return
	}
	defer rows.Close()
	models := []*modelJSON{}
	for rows.Next() {
		m := &modelJSON{}
		if err := rows.Scan(&m.ModelID, &m.ProviderID, &m.Up, &m.Down); err != nil {
			d.fail(w, r, err)
			return
		}
		models = append(models, m)
	}
	if err := rows.Err(); err != nil {
		d.fail(w, r, err)
		return
	}

	page, per := pageParams(r)
	fbRows, err := d.Store.DB.QueryContext(ctx,
		`SELECT r.id, r.value, r.comment, r.created_at, r.chat_id,
		        COALESCE(c.title, ''), r.message_id,
		        COALESCE(substr(m.content, 1, ?1), ''), c.model_id,
		        COALESCE(NULLIF(u.name, ''), u.email, 'deleted user')
		 FROM ratings r
		 LEFT JOIN chats c ON c.id = r.chat_id
		 LEFT JOIN messages m ON m.id = r.message_id
		 LEFT JOIN users u ON u.id = r.user_id
		 ORDER BY r.created_at DESC, r.id DESC LIMIT ?2 OFFSET ?3`,
		maxExcerpt, per, (page-1)*per)
	if err != nil {
		d.fail(w, r, err)
		return
	}
	defer fbRows.Close()
	recent := []*feedbackJSON{}
	for fbRows.Next() {
		f := &feedbackJSON{}
		if err := fbRows.Scan(&f.ID, &f.Value, &f.Comment, &f.CreatedAt, &f.ChatID,
			&f.ChatTitle, &f.MessageID, &f.Excerpt, &f.ModelID, &f.UserName); err != nil {
			d.fail(w, r, err)
			return
		}
		if len(f.Excerpt) > maxExcerpt {
			b := []byte(f.Excerpt[:maxExcerpt])
			for len(b) > 0 && b[len(b)-1]&0xC0 == 0x80 { // drop a split rune tail
				b = b[:len(b)-1]
			}
			if len(b) > 0 && b[len(b)-1] >= 0xC0 { // cut left a dangling lead byte
				b = b[:len(b)-1]
			}
			f.Excerpt = string(b) + "…"
		}
		recent = append(recent, f)
	}
	if err := fbRows.Err(); err != nil {
		d.fail(w, r, err)
		return
	}

	var total int
	if err := d.Store.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM ratings`).Scan(&total); err != nil {
		d.fail(w, r, err)
		return
	}

	httpx.JSON(w, http.StatusOK, map[string]any{
		"totals": map[string]int{"up": totals.Up, "down": totals.Down,
			"score": totals.Score},
		"models": models,
		"recent": recent,
		"total":  total,
		"page":   page,
		"per":    per,
	})
}
