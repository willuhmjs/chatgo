package apichat

import (
	"testing"
)

// Conversations exported from ChatGPT arrive as {"conversations": [ … ]};
// a bare conversation object (mapping at top level) must work too.
func TestParseImportChatGPTEnvelope(t *testing.T) {
	conv := `{
		"title": "Branch chat", "current_node": "c", "mapping": {
		"r": {"parent": null, "children": ["a","b"], "message": null},
		"a": {"parent":"r","children":[],"message":{"author":{"role":"user"},"content":{"content_type":"text","parts":["discarded branch"]},"create_time":1}},
		"b": {"parent":"r","children":["c"],"message":{"author":{"role":"user"},"content":{"content_type":"text","parts":["kept branch"]},"create_time":2}},
		"c": {"parent":"b","children":[],"message":{"author":{"role":"assistant"},"model":"gpt-4o","content":{"content_type":"text","parts":["kept reply"]},"create_time":3}}
	}}`

	for name, raw := range map[string]string{
		"envelope":    `{"conversations": [` + conv + `]}`,
		"bare object": `[` + conv + `]`,
		"bare single": conv,
	} {
		convs := parseImport([]byte(raw))
		if len(convs) != 1 {
			t.Fatalf("%s: got %d conversations", name, len(convs))
		}
		cv := convs[0]
		if cv.title != "Branch chat" {
			t.Errorf("%s: title=%q", name, cv.title)
		}
		if len(cv.turns) != 2 {
			t.Fatalf("%s: turns=%d (%+v)", name, len(cv.turns), cv.turns)
		}
		if cv.turns[0].content != "kept branch" || cv.turns[1].content != "kept reply" {
			t.Errorf("%s: active branch wrong: %+v", name, cv.turns)
		}
		if cv.turns[1].model != "gpt-4o" {
			t.Errorf("%s: model=%q", name, cv.turns[1].model)
		}
	}
}

// Open WebUI exports use history.messages with parentId links; a top-level
// array of such exports imports all of them.
func TestParseImportOWUIArray(t *testing.T) {
	raw := `[
		{"title":"One","chat":{"history":{"currentId":"2","messages":{
			"1":{"role":"user","content":"q1","parentId":null},
			"2":{"role":"assistant","content":"a1","parentId":"1","models":["llama3"]}}}}},
		{"title":"Two","chat":{"history":{"currentId":"1","messages":{
			"1":{"role":"user","content":"q2","parentId":null}}}}}
	]`
	convs := parseImport([]byte(raw))
	if len(convs) != 2 {
		t.Fatalf("got %d", len(convs))
	}
	if convs[0].title != "One" || len(convs[0].turns) != 2 {
		t.Errorf("one: %+v", convs[0])
	}
	if convs[1].title != "Two" || len(convs[1].turns) != 1 {
		t.Errorf("two: %+v", convs[1])
	}
}

func TestParseImportGarbage(t *testing.T) {
	for _, raw := range []string{`{}`, `[]`, `null`, `{"foo":1}`, `not json`} {
		if got := parseImport([]byte(raw)); len(got) != 0 {
			t.Errorf("%s: parsed %d", raw, len(got))
		}
	}
}
