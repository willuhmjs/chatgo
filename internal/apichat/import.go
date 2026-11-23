// Chat import: POST /api/chats/import accepts the JSON you get from
// "export all conversations" (ChatGPT conversations.json), an Open WebUI
// chat export (single object or array), or our own /api/chats/{id}/export
// json — and lands them as regular chats. Only the ACTIVE branch of each
// conversation is imported (current_node/currentId when present, else the
// first-child chain), flattened to a linear parent chain so every imported
// chat behaves like a normal conversation (branching resumes naturally).

package apichat

import (
	"encoding/json"
	"net/http"
	"strings"

	"chatgo/internal/api/httpx"
	"chatgo/internal/store"

	"github.com/google/uuid"
)

const (
	maxImportChats = 200  // per request
	maxImportMsgs  = 2000 // per chat
	maxImportChars = 512 << 10
)

// importedConv is one parsed conversation.

// --- envelope sniffing ------------------------------------------------------
//
// Everything below is deliberately tolerant: imports are best-effort one-shot
// conversions of foreign formats, unknown keys are ignored, garbage is
// skipped rather than rejected mid-file.

type anyJSON = map[string]any

func str(v any) string {
	s, _ := v.(string)
	return s
}

func fnum(v any) float64 {
	f, _ := v.(float64)
	return f
}

// importedTurn is one normalized message of an imported conversation.
type importedTurn struct {
	role    string
	content string
	model   string
	at      int64
}

type importedConv struct {
	title string
	turns []importedTurn
}

func (d *Deps) handleImportChats(w http.ResponseWriter, r *http.Request) {
	u, ok := d.requireUser(w, r)
	if !ok {
		return
	}
	var raw json.RawMessage
	if err := httpx.Decode(r, &raw); err != nil {
		httpx.ErrorJSON(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	convs := parseImport(raw)
	if len(convs) == 0 {
		httpx.ErrorJSON(w, http.StatusBadRequest,
			"no conversations found (expected a ChatGPT conversations.json array or an Open WebUI export)")
		return
	}
	if len(convs) > maxImportChats {
		convs = convs[:maxImportChats]
	}

	ids := make([]string, 0, len(convs))
	totalMsgs := 0
	for i := range convs {
		cv := &convs[i]
		if len(cv.turns) == 0 {
			continue
		}
		if len(cv.turns) > maxImportMsgs {
			cv.turns = cv.turns[:maxImportMsgs]
		}
		c := &store.Chat{UserID: u.ID, Title: cv.title}
		if err := d.Store.Chats.Create(r.Context(), c); err != nil {
			d.fail(w, r, err)
			return
		}
		prev := ""
		leaf := ""
		for _, t := range cv.turns {
			m := &store.Message{
				ID:        uuid.NewString(),
				ChatID:    c.ID,
				Role:      t.role,
				ParentID:  prev,
				Content:   t.content,
				CreatedAt: t.at,
			}
			if err := d.Store.Messages.Append(r.Context(), m); err != nil {
				d.fail(w, r, err)
				return
			}
			prev, leaf = m.ID, m.ID
			totalMsgs++
		}
		if leaf != "" {
			if err := d.Store.Chats.SetLeaf(r.Context(), c.ID, leaf); err != nil {
				d.fail(w, r, err)
				return
			}
		}
		ids = append(ids, c.ID)
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{
		"imported": len(ids),
		"messages": totalMsgs,
		"ids":      ids,
	})
}

// --- parsing ----------------------------------------------------------------

func parseImport(raw []byte) []importedConv {
	var arr []json.RawMessage
	if json.Unmarshal(raw, &arr) == nil {
		var out []importedConv
		for _, item := range arr {
			out = append(out, parseImportOne(item)...)
		}
		return out
	}
	return parseImportOne(raw)
}

// parseImportOne sniffs one JSON object: OWUI/ChatGo export vs a single
// ChatGPT conversation.
func parseImportOne(raw []byte) []importedConv {
	var obj anyJSON
	if json.Unmarshal(raw, &obj) != nil {
		return nil
	}
	// OWUI / ChatGo: history under "chat" or at top level.
	for _, key := range []string{"chat", ""} {
		holder := obj
		if key != "" {
			sub, _ := obj[key].(anyJSON)
			if sub == nil {
				continue
			}
			holder = sub
		}
		if hist, _ := holder["history"].(anyJSON); hist != nil {
			if conv := parseOWUI(obj, hist); conv != nil {
				return []importedConv{*conv}
			}
		}
	}
	if mapping, _ := obj["mapping"].(anyJSON); mapping != nil {
		if conv := parseChatGPT(obj, mapping); conv != nil {
			return []importedConv{*conv}
		}
	}
	// ChatGPT conversations.json envelope: {"conversations": [ … ]}.
	if arrRaw, _ := obj["conversations"].([]any); len(arrRaw) > 0 {
		var out []importedConv
		for _, item := range arrRaw {
			sub, _ := item.(anyJSON)
			if sub == nil {
				continue
			}
			mapping, _ := sub["mapping"].(anyJSON)
			if mapping == nil {
				continue
			}
			if conv := parseChatGPT(sub, mapping); conv != nil {
				out = append(out, *conv)
			}
			if len(out) >= maxImportChats {
				break
			}
		}
		return out
	}
	return nil
}

func importTitle(obj anyJSON) string {
	if t := strings.TrimSpace(str(obj["title"])); t != "" {
		return t
	}
	return "Imported chat"
}

// parseOWUI walks Open WebUI's history: from currentId (falling back to the
// newest leaf) up through parentId to the root, then replays root→leaf.
func parseOWUI(obj, hist anyJSON) *importedConv {
	msgs, _ := hist["messages"].(anyJSON)
	if len(msgs) == 0 {
		return nil
	}
	conv := &importedConv{title: importTitle(obj)}

	// Find the walk start: currentId → currentResponseId → last message.
	start := str(hist["currentId"])
	if start == "" {
		start = str(hist["currentResponseId"])
	}
	var chain []anyJSON
	if m, _ := msgs[start].(anyJSON); m != nil {
		chain = owuiAncestry(msgs, start)
	} else {
		// No anchor: pick the deepest leaf (longest parent chain).
		bestLen := -1
		for id := range msgs {
			ch := owuiAncestry(msgs, id)
			if len(ch) > bestLen {
				bestLen, chain = len(ch), ch
			}
		}
	}
	chars := 0
	for _, m := range chain {
		role := str(m["role"])
		if role != "user" && role != "assistant" && role != "system" {
			continue
		}
		content := str(m["content"])
		if content == "" {
			continue
		}
		if chars+len(content) > maxImportChars {
			break
		}
		chars += len(content)
		at := int64(fnum(m["timestamp"]))
		turn := importedTurn{role: role, content: content, at: at}
		if models, _ := m["models"].([]any); len(models) > 0 {
			turn.model = str(models[0])
		}
		conv.turns = append(conv.turns, turn)
	}
	if len(conv.turns) == 0 {
		return nil
	}
	return conv
}

// owuiAncestry returns the root→node chain containing nodeID (parent walk,
// cycle-guarded).
func owuiAncestry(msgs anyJSON, nodeID string) []anyJSON {
	var up []anyJSON
	seen := map[string]bool{}
	for id := nodeID; id != ""; {
		if seen[id] {
			break
		}
		seen[id] = true
		m, ok := msgs[id].(anyJSON)
		if !ok {
			break
		}
		up = append(up, m)
		next := str(m["parentId"])
		if next == "null" {
			next = ""
		}
		id = next
	}
	for i, j := 0, len(up)-1; i < j; i, j = i+1, j-1 {
		up[i], up[j] = up[j], up[i]
	}
	return up
}

// parseChatGPT walks OpenAI's conversation mapping: from current_node (or the
// first child chain from the root) with message.author.role / message.content
// .parts[] payloads.
func parseChatGPT(obj, mapping anyJSON) *importedConv {
	node := str(obj["current_node"])
	var chain []string
	if node != "" {
		chain = chatgptAncestry(mapping, node)
	} else {
		// Find the root (parent == nil) and follow children[0].
		rootID := ""
		for id, v := range mapping {
			n, _ := v.(anyJSON)
			if n == nil {
				continue
			}
			if n["parent"] == nil {
				rootID = id
				break
			}
		}
		if rootID == "" {
			return nil
		}
		for id := rootID; id != ""; {
			n, ok := mapping[id].(anyJSON)
			if !ok {
				break
			}
			if id != rootID { // skip the null root node itself
				chain = append(chain, id)
			}
			kids, _ := n["children"].([]any)
			id = ""
			if len(kids) > 0 {
				id = str(kids[0])
			}
		}
	}

	conv := &importedConv{title: importTitle(obj)}
	chars := 0
	for _, id := range chain {
		n, _ := mapping[id].(anyJSON)
		if n == nil {
			continue
		}
		msg, _ := n["message"].(anyJSON)
		if msg == nil {
			continue
		}
		author, _ := msg["author"].(anyJSON)
		role := str(author["role"])
		if role == "model" {
			role = "assistant"
		}
		if role != "user" && role != "assistant" && role != "system" {
			continue
		}
		content := chatgptContent(msg["content"])
		if strings.TrimSpace(content) == "" {
			continue
		}
		if chars+len(content) > maxImportChars {
			break
		}
		chars += len(content)
		at := int64(fnum(msg["create_time"]))
		if at == 0 {
			at = int64(fnum(msg["createTime"]))
		}
		conv.turns = append(conv.turns, importedTurn{
			role: role, content: content, at: at, model: str(msg["model"]),
		})
	}
	if len(conv.turns) == 0 {
		return nil
	}
	return conv
}

// chatgptAncestry returns the root→node node-id chain (parent walk, with a
// fallback that derives parent links from children edges when the "parent"
// key is absent).
func chatgptAncestry(mapping anyJSON, nodeID string) []string {
	parentOf := map[string]string{}
	for id, v := range mapping {
		n, _ := v.(anyJSON)
		if n == nil {
			continue
		}
		kids, _ := n["children"].([]any)
		for _, k := range kids {
			c := str(k)
			if c != "" {
				if _, seen := parentOf[c]; !seen {
					parentOf[c] = id
				}
			}
		}
	}
	var up []string
	seen := map[string]bool{}
	for id := nodeID; id != ""; {
		if seen[id] {
			break
		}
		seen[id] = true
		n, ok := mapping[id].(anyJSON)
		if !ok {
			break
		}
		up = append(up, id)
		next := str(n["parent"]) // null → ""
		if next == "" {
			next = parentOf[id]
		}
		id = next
	}
	for i, j := 0, len(up)-1; i < j; i, j = i+1, j-1 {
		up[i], up[j] = up[j], up[i]
	}
	return up
}

// chatgptContent extracts text from a ChatGPT content block
// ({content_type:"text", parts:[…]} or a bare string).
func chatgptContent(v any) string {
	switch c := v.(type) {
	case string:
		return c
	case anyJSON:
		if c["content_type"] != nil && str(c["content_type"]) != "text" {
			return "" // code interpreter / image artifacts are skipped
		}
		parts, _ := c["parts"].([]any)
		var b strings.Builder
		for _, p := range parts {
			if s, ok := p.(string); ok {
				b.WriteString(s)
			}
		}
		return b.String()
	}
	return ""
}
