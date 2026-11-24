// DOCX extraction tests: archives are built in-test with archive/zip (no
// fixtures, no network) and pushed through extractDocxText / the real upload
// endpoint (chunk+embed pipeline behind the canned stub).
package kb

import (
	"archive/zip"
	"bytes"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// buildDocx packs documentXML as word/document.xml (plus a couple of decoy
// entries a real Word file would carry) into a valid zip payload.
func buildDocx(t *testing.T, documentXML string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{
		"[Content_Types].xml": `<?xml version="1.0"?><Types/>`,
		docxEntry:             documentXML,
		"word/media/x.png":    "\x89PNG fake image bytes",
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// docxBody wraps paragraph/part content in the WordprocessingML envelope.
func docxBody(body string) string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">` +
		`<w:body>` + body + `</w:body></w:document>`
}

func TestExtractDocxText(t *testing.T) {
	cases := []struct {
		name string
		body string // inner <w:body> markup; "" → feed raw src instead
		src  []byte // when non-nil, used verbatim as the whole upload
		want string
		code int // non-zero → want httpError with this status
	}{
		{
			name: "two paragraphs",
			body: `<w:p><w:r><w:t>First para</w:t></w:r></w:p><w:p><w:r><w:t>Second para</w:t></w:r></w:p>`,
			want: "First para\nSecond para",
		},
		{
			name: "runs concatenate without separator",
			body: `<w:p><w:r><w:t>Foo</w:t></w:r><w:r><w:t xml:space="preserve">bar </w:t></w:r><w:r><w:t>baz</w:t></w:r></w:p>`,
			want: "Foobar baz",
		},
		{
			name: "tab run becomes a tab",
			body: `<w:p><w:r><w:t>key</w:t></w:r><w:r><w:tab/></w:r><w:r><w:t>value</w:t></w:r></w:p>`,
			want: "key\tvalue",
		},
		{
			name: "br and cr become newlines",
			body: `<w:p><w:r><w:t>a</w:t><w:br/><w:t>b</w:t><w:cr/><w:t>c</w:t></w:r></w:p>`,
			want: "a\nb\nc",
		},
		{
			name: "entities unescape",
			body: `<w:p><w:r><w:t>a &amp; b &#233; &lt;t&#62; &#x41;</w:t></w:r></w:p>`,
			want: "a & b é <t> A",
		},
		{
			name: "instrText and styles are not text",
			body: `<w:p><w:r><w:instrText>HYPERLINK "http://x"</w:instrText></w:r><w:pPr><w:rPr></w:rPr></w:pPr><w:r><w:t>real</w:t></w:r></w:p>`,
			want: "real",
		},
		{
			name: "CRLF in runs normalizes",
			body: "<w:p><w:r><w:t>a\r\nb</w:t></w:r></w:p>",
			want: "a\nb",
		},
		{
			name: "drawingml text runs count too",
			body: `<w:p><w:r><w:t>main</w:t></w:r></w:p>` +
				`<w:p><w:r><w:pict><v:shape xmlns:v="urn:schemas-microsoft-com:vml"><v:textbox><w:p><w:r><w:t>boxed</w:t></w:r></w:p></v:textbox></v:shape></w:pict></w:r></w:p>`,
			want: "main\nboxed",
		},
		{
			name: "malformed xml rejected",
			src:  buildZipWith(t, docxEntry, `<w:document><w:p><w:r><w:t>unclosed`),
			code: http.StatusUnsupportedMediaType,
		},
		{
			name: "not a zip",
			src:  []byte("this is definitely not a docx"),
			code: http.StatusUnsupportedMediaType,
		},
		{
			name: "empty payload",
			src:  []byte{},
			code: http.StatusUnsupportedMediaType,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := tc.src
			if src == nil {
				src = buildDocx(t, docxBody(tc.body))
			}
			got, err := extractDocxText(src)
			var he httpError
			if tc.code != 0 {
				if !errors.As(err, &he) || he.status != tc.code {
					t.Fatalf("err = %v, want status %d", err, tc.code)
				}
				return
			}
			if err != nil {
				t.Fatalf("extract: %v", err)
			}
			if got != tc.want {
				t.Fatalf("text = %q, want %q", got, tc.want)
			}
		})
	}
}

// buildZipWith is the single-entry variant used by the error cases.
func buildZipWith(t *testing.T, name, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestExtractDocxZipBombGuard(t *testing.T) {
	// An 11 MiB "document" compresses to a few KB — exactly the zip-bomb
	// shape the uncompressed budget must catch before the bytes materialize.
	t.Run("over budget", func(t *testing.T) {
		huge := strings.Repeat("x", int(maxDocxUncompressed)+1)
		src := buildZipWith(t, docxEntry, `<w:p><w:r><w:t>`+huge+`</w:t></w:r></w:p>`)
		_, err := extractDocxText(src)
		var he httpError
		if !errors.As(err, &he) || he.status != http.StatusRequestEntityTooLarge {
			t.Fatalf("err = %v, want 413", err)
		}
	})
	t.Run("under budget passes", func(t *testing.T) {
		body := strings.Repeat("y", 1<<20)
		src := buildZipWith(t, docxEntry, `<w:p><w:r><w:t>`+body+`</w:t></w:r></w:p>`)
		got, err := extractDocxText(src)
		if err != nil {
			t.Fatal(err)
		}
		if got != body {
			t.Fatalf("len(got) = %d, want %d", len(got), len(body))
		}
	})
}

func TestExtractDocxMissingEntry(t *testing.T) {
	src := buildZipWith(t, "word/styles.xml", `<styles/>`)
	_, err := extractDocxText(src)
	var he httpError
	if !errors.As(err, &he) || he.status != http.StatusUnsupportedMediaType {
		t.Fatalf("err = %v, want 415", err)
	}
}

// TestUploadDocxFlow drives the real endpoint: the .docx answers 201 with
// the OOXML mime, its extracted text is searchable, and invalid payloads
// surface 415 like every other unsupported upload.
func TestUploadDocxFlow(t *testing.T) {
	e := newTestEnv(t)
	id := e.createCollection(e.tokA, "Docs")

	src := buildDocx(t, docxBody(
		`<w:p><w:r><w:t>Release notes</w:t></w:r></w:p>`+
			`<w:p><w:r><w:t>Golang modules &amp; tooling </w:t></w:r><w:r><w:t>golang rocks</w:t></w:r></w:p>`))

	st, m, raw := e.upload(e.tokA, id, [2]string{"notes.docx", string(src)})
	if st != http.StatusCreated {
		t.Fatalf("docx upload: %d %q", st, raw)
	}
	item := m["items"].([]any)[0].(map[string]any)
	if item["mime"] != docxMime {
		t.Fatalf("mime = %v, want %q", item["mime"], docxMime)
	}
	if item["name"] != "notes.docx" {
		t.Fatalf("name = %v", item["name"])
	}

	// The extracted text (entity included) made it through chunk+embed.
	st, m, raw = e.json("POST", "/api/kb/"+id+"/search", e.tokA, map[string]any{"q": "golang"})
	if st != http.StatusOK {
		t.Fatalf("search: %d %q", st, raw)
	}
	hits := m["items"].([]any)
	if len(hits) != 1 {
		t.Fatalf("hits = %d, want 1: %v", len(hits), hits)
	}
	hit := hits[0].(map[string]any)
	if hit["file_name"] != "notes.docx" {
		t.Fatalf("file_name = %v", hit["file_name"])
	}
	if content, _ := hit["content"].(string); !strings.Contains(content, "Golang modules & tooling golang rocks") {
		t.Fatalf("content = %q", content)
	}

	// Garbage under a .docx name → 415, same envelope as other rejects.
	if st, _, raw := e.upload(e.tokA, id, [2]string{"evil.docx", "PK\x03\x04 not really a zip"}); st != 415 {
		t.Fatalf("garbage docx: %d %q, want 415", st, raw)
	}
	// Valid zip without the body entry → 415.
	if st, _, raw := e.upload(e.tokA, id, [2]string{"empty.docx", string(buildZipWith(t, "word/styles.xml", `<styles/>`))}); st != 415 {
		t.Fatalf("entryless docx: %d %q, want 415", st, raw)
	}
}
