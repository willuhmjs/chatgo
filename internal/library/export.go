// Export: GET /api/chats/{id}/export?format=md|json and GET /api/me/export
// (streamed zip archive). Ownership rule as everywhere: foreign chat → 404.
//
// RAM discipline (FEATURES-V2 §RAM): nothing is buffered whole. The md and
// json writers walk `messages` in rowid order straight from the driver into
// the ResponseWriter; the zip writer sits ON the ResponseWriter, so at most
// one message row is resident at a time (plus the chat-id list — ids only,
// not content — needed because the single-writer SQLite pool cannot serve a
// nested query while an outer result set is open on :memory: databases).

package library

import (
	"archive/zip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"

	"chatgo/internal/api/httpx"
	"chatgo/internal/store"
)

const meSettingPrefix = "me."

func registerExport(mux *http.ServeMux, d *Deps) {
	mux.HandleFunc("GET /api/chats/{id}/export", d.handleExportChat)
	mux.HandleFunc("GET /api/me/export", d.handleExportMe)
}

// GET /api/chats/{id}/export?format=md|json (default md) → single-chat
// transcript download. format=anything-else → 400.
func (d *Deps) handleExportChat(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	c, err := d.ownChat(r.Context(), u.ID, r.PathValue("id"))
	if err != nil {
		d.fail(w, r, err)
		return
	}
	format := strings.ToLower(r.URL.Query().Get("format"))
	if format == "" {
		format = "md"
	}
	switch format {
	case "md":
		setDownload(w, "text/markdown; charset=utf-8", safeFileStem(c.Title)+"-"+shortID(c.ID)+".md")
		if err := d.writeChatMarkdown(w, r.Context(), c); err != nil {
			d.fail(w, r, err) // only fires before the first byte in practice
			return
		}
	case "json":
		setDownload(w, "application/json; charset=utf-8", safeFileStem(c.Title)+"-"+shortID(c.ID)+".json")
		if err := d.writeChatJSON(w, r.Context(), c); err != nil {
			d.fail(w, r, err)
			return
		}
	default:
		httpx.ErrorJSON(w, http.StatusBadRequest, `format must be "md" or "json"`)
	}
}

// GET /api/me/export → streaming archive/zip:
//
//	chats/<chat-id>.json   one per live chat (same shape as export?format=json)
//	me-settings.json       caller prefs, "me.<uid>." prefix stripped,
//	                       secret-looking keys excluded entirely
//
// Once the zip starts streaming the status is already 200 — a mid-stream DB
// failure truncates the response (logged, never rendered into the archive).
func (d *Deps) handleExportMe(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}

	// Everything that can still answer a clean JSON error happens BEFORE the
	// first zip byte leaves.
	ids, err := d.userChatIDs(r.Context(), u.ID)
	if err != nil {
		d.fail(w, r, err)
		return
	}
	prefs, err := d.meSettings(r.Context(), u.ID)
	if err != nil {
		d.fail(w, r, err)
		return
	}

	setDownload(w, "application/zip", fmt.Sprintf("chatgo-export-%d.zip", time.Now().Unix()))
	zw := zip.NewWriter(w)
	flush := func() {
		zw.Flush() //nolint:errcheck // best-effort stream pump
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
	for _, id := range ids {
		c, err := d.Store.Chats.Get(r.Context(), id)
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) { // chat deleted mid-export: skip
				d.fail(w, r, err)
				return
			}
			continue
		}
		ew, err := zw.Create("chats/" + c.ID + ".json")
		if err != nil {
			d.fail(w, r, err)
			return
		}
		if err := d.writeChatJSON(ew, r.Context(), c); err != nil {
			d.fail(w, r, err)
			return
		}
		flush()
	}
	sw, err := zw.Create("me-settings.json")
	if err != nil {
		d.fail(w, r, err)
		return
	}
	if err := json.NewEncoder(sw).Encode(prefs); err != nil {
		d.fail(w, r, err)
		return
	}
	if err := zw.Close(); err != nil {
		d.fail(w, r, err) // already streaming; logs only
		return
	}
	flush()
}

// --- writers ---------------------------------------------------------------

// writeChatMarkdown renders a human transcript: title header, model line,
// then `## role · RFC3339` blocks with sources under each message.
func (d *Deps) writeChatMarkdown(w io.Writer, ctx context.Context, c *store.Chat) error {
	if _, err := fmt.Fprintf(w, "# %s\n\n", c.Title); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "- Chat: `%s`\n", c.ID); err != nil {
		return err
	}
	if c.ProviderID != "" || c.ModelID != "" {
		model := strings.Trim(c.ProviderID+"/"+c.ModelID, "/")
		if _, err := fmt.Fprintf(w, "- Model: `%s`\n", model); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "- Exported: %s\n\n", time.Now().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	return d.streamMessages(ctx, c.ID, func(m *store.Message) error {
		if _, err := fmt.Fprintf(w, "## %s · %s\n\n", m.Role,
			time.Unix(m.CreatedAt, 0).UTC().Format(time.RFC3339)); err != nil {
			return err
		}
		if _, err := io.WriteString(w, m.Content); err != nil {
			return err
		}
		if _, err := io.WriteString(w, "\n"); err != nil {
			return err
		}
		if m.Error != nil && *m.Error != "" {
			if _, err := fmt.Fprintf(w, "\n> error: %s\n", strings.ReplaceAll(*m.Error, "\n", " ")); err != nil {
				return err
			}
		}
		for _, s := range toMessageJSON(m).Sources {
			if _, err := fmt.Fprintf(w, "\nSource: [%s](%s)\n", s.Title, s.URL); err != nil {
				return err
			}
		}
		_, err := io.WriteString(w, "\n")
		return err
	})
}

// writeChatJSON streams {chat, messages[]} (the ?format=json document, and
// the per-chat zip entry shape); the array is assembled token by token so no
// full copy of the chat is ever held.
func (d *Deps) writeChatJSON(w io.Writer, ctx context.Context, c *store.Chat) error {
	cj, err := json.Marshal(toChatJSON(c))
	if err != nil {
		return err
	}
	if _, err := io.WriteString(w, `{"chat":`); err != nil {
		return err
	}
	if _, err := w.Write(cj); err != nil {
		return err
	}
	if _, err := io.WriteString(w, `,"messages":[`); err != nil {
		return err
	}
	first := true
	err = d.streamMessages(ctx, c.ID, func(m *store.Message) error {
		blob, err := json.Marshal(toMessageJSON(m))
		if err != nil {
			return err
		}
		sep := ","
		if first {
			sep = ""
			first = false
		}
		if _, err := io.WriteString(w, sep+string(blob)); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, "]}\n")
	return err
}

// streamMessages walks one chat's messages in rowid order (same order the
// message API uses), scanning straight from the driver — one row resident.
func (d *Deps) streamMessages(ctx context.Context, chatID string, fn func(*store.Message) error) error {
	rows, err := d.Store.DB.QueryContext(ctx,
		`SELECT id, chat_id, role, content, sources_json, error, tokens_in, tokens_out, created_at
		 FROM messages WHERE chat_id = ?1 ORDER BY rowid`, chatID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var m store.Message
		var e sql.NullString
		if err := rows.Scan(&m.ID, &m.ChatID, &m.Role, &m.Content, &m.SourcesJSON,
			&e, &m.TokensIn, &m.TokensOut, &m.CreatedAt); err != nil {
			return err
		}
		if e.Valid {
			m.Error = &e.String
		}
		if err := fn(&m); err != nil {
			return err
		}
	}
	return rows.Err()
}

// --- helpers ---------------------------------------------------------------

// userChatIDs lists live chat ids for the export manifest (ids only — see
// package comment for why content is not loaded here).
func (d *Deps) userChatIDs(ctx context.Context, userID string) ([]string, error) {
	rows, err := d.Store.DB.QueryContext(ctx,
		`SELECT id FROM chats WHERE user_id = ?1 AND deleted_at IS NULL ORDER BY updated_at DESC, id`,
		userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// meSettings collects the caller's "me.<uid>.*" rows into a bare map with the
// prefix stripped. Keys that look secret-bearing (same signatures apimgr
// masks) are DROPPED — an export leaves the trust boundary of the server, so
// nothing remotely secret-shaped ships even by accident.
func (d *Deps) meSettings(ctx context.Context, userID string) (map[string]string, error) {
	all, err := d.Store.Settings.All(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	prefix := meSettingPrefix + userID + "."
	for k, v := range all {
		rest, ok := strings.CutPrefix(k, prefix)
		if !ok {
			continue
		}
		if looksSecret(rest) || looksSecret(v) {
			continue
		}
		out[rest] = v
	}
	return out, nil
}

func looksSecret(s string) bool {
	k := strings.ToLower(s)
	for _, sig := range []string{"key", "secret", "token", "password"} {
		if strings.Contains(k, sig) {
			return true
		}
	}
	return false
}

// setDownload sets content-type + attachment disposition (filename is
// sanitized by safeFileStem callers, so quoting here is safe).
func setDownload(w http.ResponseWriter, contentType, filename string) {
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Content-Disposition", `attachment; filename="`+filename+`"`)
}

// shortID keeps exports filenames readable: first 8 chars of the uuid.
func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// safeFileStem turns a chat title into a filename-safe stem: letters/digits
// kept, everything else collapses to a single '-', ≤40 runes, trimmed, with a
// "chat" fallback.
func safeFileStem(title string) string {
	var out []rune
	for _, r := range title {
		if len(out) >= 40 {
			break
		}
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			out = append(out, r)
		} else if len(out) > 0 && out[len(out)-1] != '-' {
			out = append(out, '-')
		}
	}
	stem := strings.TrimRight(string(out), "-")
	if stem == "" {
		return "chat"
	}
	return stem
}
