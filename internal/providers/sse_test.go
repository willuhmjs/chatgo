package providers

import (
	"io"
	"strings"
	"testing"
)

// oneByteReader forces every Read to return a single byte, so the parser is
// exercised with lines AND multi-byte runes split at every byte boundary.
type oneByteReader struct{ r io.Reader }

func (o oneByteReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return o.r.Read(p[:1])
}

func parseAll(t *testing.T, src io.Reader) []sseEvent {
	t.Helper()
	var got []sseEvent
	if err := parseSSE(src, func(ev sseEvent) error {
		got = append(got, ev)
		return nil
	}); err != nil {
		t.Fatalf("parseSSE: %v", err)
	}
	return got
}

func TestParseSSEBasics(t *testing.T) {
	got := parseAll(t, strings.NewReader(
		"event: meta\ndata: one\ndata: two\n\n"+
			": keepalive ping\n\n"+
			"retry: 3000\nid: 9\ndata: three\n\n"+
			"data: tail-no-blank-line"))
	want := []sseEvent{
		{Event: "meta", Data: "one\ntwo"},
		{Data: "three"},
		{Data: "tail-no-blank-line"}, // dispatched at EOF (tolerant)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d events (%v), want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestParseSSETerminators(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"LF", "data: a\ndata: b\n\n", "a\nb"},
		{"CRLF", "data: a\r\ndata: b\r\n\r\n", "a\nb"},
		{"bareCR", "data: a\rdata: b\r\r", "a\nb"},
		{"mixed", "data: a\r\ndata: b\rdata: c\n\n", "a\nb\nc"},
	} {
		got := parseAll(t, oneByteReader{strings.NewReader(tc.in)})
		if len(got) != 1 || got[0].Data != tc.want {
			t.Errorf("%s: got %+v, want one event data=%q", tc.name, got, tc.want)
		}
	}
}

func TestParseSSEByteSplit(t *testing.T) {
	// Covers CRLF pairs split across reads and a 4-byte rune split at any
	// boundary; every prefix length i is a valid split point.
	in := ": ping\r\ndata: {\"c\":\"héllo 🎈\"}\r\n\r\ndata: last\n"
	for i := 0; i < len(in); i++ {
		got := parseAll(t, io.MultiReader(
			oneByteReader{strings.NewReader(in[:i])},
			strings.NewReader(in[i:])))
		if len(got) != 2 || got[0].Data != `{"c":"héllo 🎈"}` || got[1].Data != "last" {
			t.Fatalf("split at %d: got %+v", i, got)
		}
	}
}

func TestParseSSEEmptyAndCommentsOnly(t *testing.T) {
	got := parseAll(t, oneByteReader{strings.NewReader("\n\n: hi\n\n\r\n")})
	if len(got) != 0 {
		t.Fatalf("got %+v, want none", got)
	}
}

func TestScanSSELineCRLFAtBufferEdge(t *testing.T) {
	// A lone trailing \r without EOF must request more data (so a following
	// \n is not mistaken for a blank line).
	if adv, tok, err := scanSSELine([]byte("ab\r"), false); adv != 0 || tok != nil || err != nil {
		t.Fatalf(`"ab\r" !atEOF: got adv=%d tok=%q err=%v, want need-more`, adv, tok, err)
	}
	if adv, tok, err := scanSSELine([]byte("ab\r"), true); adv != 3 || string(tok) != "ab" || err != nil {
		t.Fatalf(`"ab\r" atEOF: got adv=%d tok=%q err=%v`, adv, tok, err)
	}
	if adv, tok, err := scanSSELine([]byte("ab\r\nx"), false); adv != 4 || string(tok) != "ab" || err != nil {
		t.Fatalf(`"ab\r\nx": got adv=%d tok=%q err=%v`, adv, tok, err)
	}
}
