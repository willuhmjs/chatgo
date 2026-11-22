package kb

// wordSpans records the byte span of every whitespace-delimited word so
// chunks can keep the ORIGINAL text (newlines/indentation included) instead
// of a normalized join — important for code files. UTF-8 continuation bytes
// never collide with ASCII spaces, so byte-level scanning is safe.
type wordSpan struct{ start, end int }

func wordSpans(text string) []wordSpan {
	isSpace := func(b byte) bool {
		switch b {
		case ' ', '\t', '\n', '\r', '\v', '\f':
			return true
		}
		return false
	}
	var spans []wordSpan
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
		spans = append(spans, wordSpan{i, j})
		i = j
	}
	return spans
}

// chunkText splits text into ~chunkWords-word windows advancing by
// (chunkWords − chunkOverlap) words, so consecutive chunks share ~80 words
// of context. Empty/whitespace-only input yields no chunks. Each emitted
// chunk is hard-capped at maxChunkBytes (a pathological file of one gigantic
// "word" cannot blow the per-chunk memory budget). Pure function; tests pin
// the bounds.
func chunkText(text string) []string {
	spans := wordSpans(text)
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

// capBytes splits s into ≤max-byte pieces, cutting at a rune boundary.
func capBytes(s string) []string {
	if len(s) <= maxChunkBytes {
		return []string{s}
	}
	var out []string
	for len(s) > 0 {
		if len(s) <= maxChunkBytes {
			out = append(out, s)
			break
		}
		cut := maxChunkBytes
		for cut > 0 && !isRuneStart(s[cut]) {
			cut--
		}
		if cut == 0 { // single rune wider than the cap: impossible in practice
			cut = maxChunkBytes
		}
		out = append(out, s[:cut])
		s = s[cut:]
	}
	return out
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }
