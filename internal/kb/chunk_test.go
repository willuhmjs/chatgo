package kb

import (
	"fmt"
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
