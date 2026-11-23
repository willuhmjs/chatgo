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
	"net/url"
	"sort"
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
//	                       secret-NAMED keys excluded
//	redacted-settings.json only when keys were excluded: names + reason, so
//	                       nothing vanishes silently (L2-I1)
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
	prefs, redacted, err := d.meSettings(r.Context(), u.ID)
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
	if len(redacted) > 0 {
		rw, err := zw.Create("redacted-settings.json")
		if err != nil {
			d.fail(w, r, err)
			return
		}
		manifest := struct {
			Redacted []string `json:"redacted"`
			Reason   string   `json:"reason"`
		}{redacted, "keys whose NAME looks secret-bearing (key/secret/token/password) are excluded from exports"}
		if err := json.NewEncoder(rw).Encode(manifest); err != nil {
			d.fail(w, r, err)
			return
		}
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
	if _, err := fmt.Fprintf(w, "# %s\n\n", flatMDLine(c.Title)); err != nil {
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
	return d.streamMessages(ctx, c.ID, c.LeafID, func(m *store.Message) error {
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
			if _, err := io.WriteString(w, sourceMD(s)); err != nil {
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
	cj, err := json.Marshal(exportChatShape(c))
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
	err = d.streamMessages(ctx, c.ID, c.LeafID, func(m *store.Message) error {
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

// streamMessages walks one chat's messages in display order — the active
// branch root→leaf when leafID is set, rowid-linear otherwise (and linear
// again when the leaf row no longer exists) — scanning straight from the
// driver, one row resident.
func (d *Deps) streamMessages(ctx context.Context, chatID, leafID string, fn func(*store.Message) error) error {
	if leafID != "" {
		rows, err := d.Store.DB.QueryContext(ctx,
			`WITH RECURSIVE chain(id, parent, depth) AS (
			   SELECT id, parent_id, 0 FROM messages WHERE chat_id = ?1 AND id = ?2
			   UNION ALL
			   SELECT m.id, m.parent_id, c.depth+1 FROM messages m
			     JOIN chain c ON m.chat_id = ?1 AND m.id = c.parent
			  WHERE c.depth < 100000
			 )
			 SELECT m.id, m.chat_id, m.role, m.content, m.sources_json, m.error, m.tokens_in, m.tokens_out, m.created_at
			   FROM messages m JOIN chain c ON m.id = c.id ORDER BY c.depth DESC`,
			chatID, leafID)
		if err != nil {
			return err
		}
		empty := true
		err = d.scanMessages(rows, func(m *store.Message) error {
			empty = false
			return fn(m)
		})
		rows.Close()
		if err != nil {
			return err
		}
		if !empty {
			return nil
		}
		// leaf gone → fall through to the linear walk below.
	}
	rows, err := d.Store.DB.QueryContext(ctx,
		`SELECT id, chat_id, role, content, sources_json, error, tokens_in, tokens_out, created_at
		 FROM messages WHERE chat_id = ?1 ORDER BY rowid`, chatID)
	if err != nil {
		return err
	}
	defer rows.Close()
	return d.scanMessages(rows, fn)
}

// scanMessages drains a message rowset, one row at a time, into fn.
func (d *Deps) scanMessages(rows *sql.Rows, fn func(*store.Message) error) error {
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

// exportChatShape is the chat object embedded in EVERY export serialization
// that carries one (?format=json document + zip member files; the md export
// has no chat object). Identical to toChatJSON minus share_token: a share
// token is bearer-equivalent (possession = public read of the chat) and an
// exported file leaves the server's trust boundary, so the field is scrubbed
// at this single choke point (L8-L2). The chat API and fork responses keep
// the full mirrored shape — toChatJSON itself is untouched.
func exportChatShape(c *store.Chat) *chatJSON {
	j := toChatJSON(c)
	j.ShareToken = nil
	return j
}

// flatMDLine collapses every whitespace run (spaces, \r, \n, tabs) into a
// single space and trims, so a chat title or source name containing newlines
// cannot fabricate extra block structure (# heading, "## role · ts" turn) in
// the md export (L3-L1). No length cap exists on titles, so none is added.
func flatMDLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// escMD backslash-escapes the markdown punctuation that could break out of a
// link label or turn plain exported text into markup: brackets (fake/early
// links), backticks (code spans swallowing the label end), braces (autolinks)
// and angle brackets (raw HTML) (L3-L2).
func escMD(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\\', '[', ']', '`', '{', '}', '<', '>':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// sourceMD renders one citation as a md line (L3-L2). http(s) URLs become a
// link with an escaped label + hardened destination; anything else — the
// chatgo-internal kb:// scheme, javascript:, a malformed URL — is an
// unresolvable pointer inside a downloaded file, so it degrades to a
// plain-text source name (title, else the flattened URL itself).
func sourceMD(s sourceJSON) string {
	name := escMD(flatMDLine(s.Title))
	if dest, ok := mdLinkDest(s.URL); ok {
		return "\nSource: [" + name + "](" + dest + ")\n"
	}
	if name == "" {
		name = escMD(flatMDLine(s.URL))
	}
	if name == "" {
		return ""
	}
	return "\nSource: " + name + "\n"
}

// mdLinkDest turns a raw citation URL into a CommonMark link destination:
// newline/CR/tab/NUL bytes dropped, then only http(s) URLs accepted, wrapped
// in <> and with the remaining structure-breaking bytes (spaces, parens,
// angle brackets, backslash, control and non-ASCII bytes) percent-encoded —
// ") ", "[", "<" or a newline inside a URL can no longer close the link
// early or inject markup, yet the target round-trips percent-decode intact.
func mdLinkDest(raw string) (string, bool) {
	u := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' || r == 0 {
			return -1
		}
		return r
	}, raw)
	u = strings.TrimSpace(u)
	p, err := url.Parse(u)
	if err != nil {
		return "", false
	}
	if s := strings.ToLower(p.Scheme); s != "http" && s != "https" {
		return "", false
	}
	var b strings.Builder
	for i := 0; i < len(u); i++ {
		c := u[i]
		if c <= 0x20 || c >= 0x7f || c == '(' || c == ')' || c == '<' || c == '>' || c == '\\' {
			fmt.Fprintf(&b, "%%%02X", c)
			continue
		}
		b.WriteByte(c)
	}
	return "<" + b.String() + ">", true
}

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
// prefix stripped, plus the stripped names of rows excluded because the KEY
// itself looks secret-bearing.
//
// Redaction verdict (L2-I1): provider/API keys can NEVER enter an export.
// The me.<uid>. namespace is writable only via PUT /api/me/settings, which
// enforces a fixed allowlist (theme/setup_done/accent); provider keys live
// under apimgr-owned global keys the prefix gate filters out, and dotted-key
// spoofing into someone else's namespace is rejected. So no mask/last-4
// machinery is warranted — a regression test pins the invariant instead.
// The old filter additionally dropped rows whose VALUE merely contained
// key/secret/token/password, which silently deleted benign prefs from the
// user's own export ("mood":"all good no secrets" vanished). Values are
// user-authored, shipped only to their author, and word matches are noise —
// redaction is now key-signature only, and excluded keys are named in the
// zip's redacted-settings.json manifest so nothing disappears silently.
func (d *Deps) meSettings(ctx context.Context, userID string) (map[string]string, []string, error) {
	all, err := d.Store.Settings.All(ctx)
	if err != nil {
		return nil, nil, err
	}
	out := map[string]string{}
	var redacted []string
	prefix := meSettingPrefix + userID + "."
	for k, v := range all {
		rest, ok := strings.CutPrefix(k, prefix)
		if !ok {
			continue
		}
		if looksSecret(rest) {
			redacted = append(redacted, rest)
			continue
		}
		out[rest] = v
	}
	sort.Strings(redacted)
	return out, redacted, nil
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
