package kb

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

func words(n int) string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("w%d", i)
	}
	return strings.Join(out, " ")
}

func TestChunkTextEmpty(t *testing.T) {
	for _, in := range []string{"", "   \n\t\r\n "} {
		if got := chunkText(in); got != nil {
			t.Fatalf("chunkText(%q) = %v, want nil", in, got)
		}
	}
}

func TestChunkTextShort(t *testing.T) {
	got := chunkText(words(100))
	if len(got) != 1 {
		t.Fatalf("100 words → %d chunks, want 1", len(got))
	}
	if got[0] != words(100) {
		t.Fatal("short input must round-trip unchanged")
	}
}

func TestChunkTextBoundsAndOverlap(t *testing.T) {
	const total = 2000 // step 720 → windows [0,800) [720,1520) [1440,2000)
	all := strings.Fields(words(total))
	got := chunkText(strings.Join(all, "\n")) // newlines must survive

	if len(got) != 3 {
		t.Fatalf("2000 words → %d chunks, want 3", len(got))
	}
	step := chunkWords - chunkOverlap
	for i, c := range got {
		fields := strings.Fields(c)
		start := i * step
		end := min(start+chunkWords, total)
		if len(fields) > chunkWords {
			t.Fatalf("chunk %d has %d words (max %d)", i, len(fields), chunkWords)
		}
		want := all[start:end]
		if len(fields) != len(want) {
			t.Fatalf("chunk %d covers %d words, want %d", i, len(fields), len(want))
		}
		for j := range want {
			if fields[j] != want[j] {
				t.Fatalf("chunk %d word %d = %q, want %q", i, j, fields[j], want[j])
			}
		}
		if i+1 < len(got) {
			if overlap := end - (i+1)*step; overlap != chunkOverlap {
				t.Fatalf("overlap before chunk %d = %d, want %d", i+1, overlap, chunkOverlap)
			}
		}
	}
}

func TestChunkTextPreservesNewlines(t *testing.T) {
	in := "line one\nline two\n\nline four"
	got := chunkText(in)
	if len(got) != 1 || got[0] != in {
		t.Fatalf("chunkText() = %q, want original text intact", got)
	}
}

func TestChunkTextByteCap(t *testing.T) {
	// One gigantic "word" must still be split at maxChunkBytes.
	huge := strings.Repeat("é", maxChunkBytes) // 2-byte rune, single word
	got := chunkText(huge)
	if len(got) < 2 {
		t.Fatalf("giant word produced %d chunks, want split", len(got))
	}
	for i, c := range got {
		if len(c) > maxChunkBytes {
			t.Fatalf("chunk %d is %d bytes (cap %d)", i, len(c), maxChunkBytes)
		}
	}
	if strings.Join(got, "") != huge {
		t.Fatal("split lost content")
	}
}

// ---------- RES-4 streaming-chunker equivalence guards -------------------
//
// refChunkText is the pre-RES-4 whole-file-span algorithm kept VERBATIM (the
// removed wordSpans + window loop) as the golden reference: chunkBytes must
// produce byte-identical output on every input, per the boundary argument in
// chunk.go. These tests are the proof shipped with the fix.

func refWordSpans(text string) []refWordSpan {
	isSpace := func(b byte) bool {
		switch b {
		case ' ', '\t', '\n', '\r', '\v', '\f':
			return true
		}
		return false
	}
	var spans []refWordSpan
	for i := 0; i < len(text); {
		for i < len(text) && isSpace(text[i]) {
			i++
		}
		if i >= len(text) {
			break
		}
		j := i
		for j < len(text) && !isSpace(text[j]) {
			j++
		}
		spans = append(spans, refWordSpan{i, j})
		i = j
	}
	return spans
}

type refWordSpan struct{ start, end int }

func refChunkText(text string) []string {
	spans := refWordSpans(text)
	if len(spans) == 0 {
		return nil
	}
	step := chunkWords - chunkOverlap
	var out []string
	for start := 0; start < len(spans); start += step {
		end := start + chunkWords
		if end > len(spans) {
			end = len(spans)
		}
		out = append(out, capBytes(text[spans[start].start:spans[end-1].end])...)
		if end == len(spans) {
			break
		}
	}
	return out
}

// mres4Corpus is a broad fixture set: the golden shapes above plus seams
// that land exactly on window edges (multiples of step=720 and chunkWords=800
// words), multi-byte content, giant words, BOM, and pure whitespace.
func mres4Corpus() []string {
	corpus := []string{
		"", " ", "   \n\t\r\n ",
		"one", "line one\nline two\n\nline four",
		words(100), words(719), words(720), words(721), words(799),
		words(800), words(801), words(1439), words(1440), words(1441),
		words(1520), words(1521), words(1600), words(1601), words(5000),
		strings.Join(strings.Fields(words(2000)), "\n"),
		strings.Join(strings.Fields(words(2000)), "\r\n"),
		strings.Join(strings.Fields(words(2000)), strings.Repeat(" ", 40)),
		"\ufeffbom start\nsecond line",
		"Grünstraße — über 日本語テキスト mixed ✓ words " + words(1000),
		strings.Repeat("é", maxChunkBytes),                     // giant single word
		strings.Repeat("é", maxChunkBytes*2+7) + " tail",       // two giant words
		strings.Repeat("x", 5000) + strings.Repeat(" ", 10000), // one word + whitespace tail
		strings.Repeat(" \n\t", 5000) + "late word",            // whitespace head
		words(2000) + "\n\n\n" + words(900),                    // mixed seam
		strings.Repeat("a", 30*1024) + " bbb " + strings.Repeat("é", 40*1024) + " c",
		words(800) + "  ", " " + words(801),
	}
	// 1 word per line, 1-char words: forces every window seam mid-newline-
	// run on a multi-window scale (≈ 4 full span-table compactions).
	onePerLine := strings.Join(strings.Fields(words(6000)), "\n")
	corpus = append(corpus, onePerLine)
	return corpus
}

func TestChunkStreamingByteIdentity(t *testing.T) {
	for i, in := range mres4Corpus() {
		want := refChunkText(in)
		got := chunkBytes([]byte(in))
		if len(got) != len(want) {
			t.Fatalf("corpus[%d] (%d bytes): %d chunks, want %d", i, len(in), len(got), len(want))
		}
		for j := range want {
			if got[j] != want[j] {
				t.Fatalf("corpus[%d] chunk %d differs\n got: %q\nwant: %q", i, j,
					got[j][:min(len(got[j]), 80)], want[j][:min(len(want[j]), 80)])
			}
		}
	}
}

// TestChunkStreamingFuzzEquivalence randomized-differential fuzzes the
// streaming chunker against the whole-file reference (word-length seam
// torture: 1–3-byte words, random whitespace runs, multi-byte tails).
func TestChunkStreamingFuzzEquivalence(t *testing.T) {
	rng := rand.New(rand.NewSource(20261122))
	piece := []string{"a", "bb", "ccc", "dd", "é", "日", "ok!", "x"}
	for iter := 0; iter < 300; iter++ {
		var sb strings.Builder
		n := rng.Intn(2600)
		for i := 0; i < n; i++ {
			sb.WriteString(piece[rng.Intn(len(piece))])
			switch rng.Intn(6) {
			case 0:
				sb.WriteString("\n")
			case 1:
				sb.WriteString(strings.Repeat(" ", 1+rng.Intn(3)))
			case 2:
				sb.WriteString("\t \r\n")
			}
		}
		in := sb.String()
		want := refChunkText(in)
		got := chunkBytes([]byte(in))
		if len(got) != len(want) {
			t.Fatalf("iter %d (%d words, %d bytes): %d chunks, want %d", iter, n, len(in), len(got), len(want))
		}
		for j := range want {
			if got[j] != want[j] {
				t.Fatalf("iter %d chunk %d differs (input %d bytes)", iter, j, len(in))
			}
		}
	}
}

// TestChunkStreamingMultiWindowWordPerLine is the RES-4 worst-case fixture:
// 2 MiB of 1-char words, one per line (the max-chunk-count shape that drove
// the old 42 MiB span table). Byte identity against the reference, every
// chunk within the byte cap, and full content coverage are asserted.
func TestChunkStreamingMultiWindowWordPerLine(t *testing.T) {
	line := "w\n"
	in := strings.Repeat(line, (2<<20)/len(line))
	want := refChunkText(in)
	got := chunkBytes([]byte(in))
	if len(got) != len(want) {
		t.Fatalf("%d chunks, want %d", len(got), len(want))
	}
	if len(got) < 1000 { // multi-window on ~1.1M words ⇒ ~1500 chunks
		t.Fatalf("fixture too small to span many windows: %d chunks", len(got))
	}
	for j := range want {
		if got[j] != want[j] {
			t.Fatalf("chunk %d differs", j)
		}
		if len(got[j]) > maxChunkBytes {
			t.Fatalf("chunk %d over byte cap", j)
		}
	}
}

func TestParseEmbeddingsResponseOrderingAndErrors(t *testing.T) {
	raw := []byte(`{"data":[{"index":1,"embedding":[0.5,0.5]},{"index":0,"embedding":[1,0]}]}`)
	got, err := parseEmbeddingsResponse(raw, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got[0][0] != 1 || got[1][0] != 0.5 {
		t.Fatalf("vectors not reordered by index: %v", got)
	}
	for _, bad := range []string{
		`{"data":[]}`, // wrong count
		`nonsense`,    // malformed
		`{"data":[{"index":0,"embedding":[]},{"index":1,"embedding":[1]}]}`,  // empty vector
		`{"data":[{"index":0,"embedding":[1]},{"index":0,"embedding":[1]}]}`, // duplicate index
	} {
		if _, err := parseEmbeddingsResponse([]byte(bad), 2); err == nil {
			t.Fatalf("parseEmbeddingsResponse(%q) should fail", bad)
		}
	}
}
