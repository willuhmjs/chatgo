package kb

// DOCX ingest (.docx uploads): a .docx is an OPC zip; the document body is
// word/document.xml. Extraction is stdlib-only — archive/zip to reach the
// entry, encoding/xml to stream text runs (entities unescape as part of XML
// decoding). Everything else in the archive (styles, media, rels) is skipped
// without being decompressed.
//
// Zip-bomb guard: the entry is streamed through io.LimitReader against a
// total-uncompressed budget (maxDocxBytes) that is decremented per entry, so
// a 5 MiB upload whose XML inflates 1000× is refused before its bytes are
// ever held in memory. The extracted text then flows through the same
// chunk→embed pipeline as txt/md files.

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"strings"
)

const (
	// docxMime is the stored display mime for .docx files.
	docxMime = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	// docxEntry is the only zip member extraction reads.
	docxEntry = "word/document.xml"
	// maxDocxUncompressed bounds total unzipped bytes per ingest (zip bomb).
	maxDocxUncompressed int64 = 10 << 20
)

// docxBudget enforces the total-uncompressed cap per entry AND across the
// archive: each read is wrapped in io.LimitReader(budget+1), so exceeding
// the cap is observable (the extra byte proves overflow) without ever
// buffering more than cap+1 bytes per entry.
type docxBudget struct{ left int64 }

// errDocxTooBig distinguishes a zip-bomb overflow from plain corrupt-entry
// I/O failures inside readAll.
var errDocxTooBig = errors.New("kb: docx uncompressed budget spent")

func (b *docxBudget) readAll(rc io.Reader) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(rc, b.left+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > b.left {
		return nil, errDocxTooBig
	}
	b.left -= int64(len(raw))
	return raw, nil
}

// extractDocxText returns the plain text of a .docx payload: w:t runs are
// concatenated, w:tab becomes a tab, w:br/w:cr and the end of every w:p
// paragraph become newlines. Malformed archives/missing entries/over-budget
// expansion answer httpError statuses the handler maps directly (415/413).
func extractDocxText(data []byte) (string, error) {
	reject := httpErr(415, "not a valid .docx file (could not read the zip archive)")
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", reject
	}
	budget := docxBudget{left: maxDocxUncompressed}
	for _, f := range zr.File {
		if f.Name != docxEntry {
			continue // styles/media/rels are never decompressed
		}
		rc, err := f.Open()
		if err != nil {
			return "", reject
		}
		raw, rerr := budget.readAll(rc)
		_ = rc.Close()
		if errors.Is(rerr, errDocxTooBig) {
			return "", httpErr(413, "docx expands beyond the 10 MiB unzip limit")
		}
		if rerr != nil {
			return "", reject
		}
		return docxXMLText(raw)
	}
	return "", reject
}

// docxXMLText streams document.xml into plain text. Namespace prefixes are
// irrelevant (WordprocessingML, DrawingML text and OMML math all use the
// same local names for their text runs, which is what we want); matching on
// Local keeps the decoder independent of the document's xmlns declarations.
// Entity decoding (&amp;, &#233;, …) is part of XML tokenization.
func docxXMLText(raw []byte) (string, error) {
	reject := httpErr(415, "could not parse the docx document body")
	dec := xml.NewDecoder(bytes.NewReader(raw))
	var sb strings.Builder
	inT := false // inside a <w:t> text run
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", reject
		}
		switch v := tok.(type) {
		case xml.StartElement:
			switch v.Name.Local {
			case "t":
				inT = true
			case "tab":
				sb.WriteByte('\t')
			case "br", "cr":
				sb.WriteByte('\n')
			}
		case xml.EndElement:
			switch v.Name.Local {
			case "t":
				inT = false
			case "p":
				sb.WriteByte('\n')
			}
		case xml.CharData:
			if inT {
				sb.Write(v)
			}
		}
	}
	return normalizeDocxText(sb.String()), nil
}

// normalizeDocxText unifies line endings, collapses paragraph runs and
// trims, WITHOUT touching horizontal whitespace (xml:space="preserve" runs
// keep their intra-paragraph spacing across run joins).
func normalizeDocxText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(s)
}
