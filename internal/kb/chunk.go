package kb

// Chunking (RES-4 RAM remediation): this used to materialize a []wordSpan
// for EVERY word of the file — 2.62 M spans ≈ 42 MiB of live memory for a
// 5 MiB 1-word/line worst case — plus a whole-file bytes→string copy. The
// scan is now a single streaming pass: at most chunkWords+1 word spans (and
// one chunk's worth of emitted bytes) are ever live (~13 KiB of spans), and
// chunks are emitted as soon as they are provably complete.
//
// The output is byte-identical to the old whole-file algorithm on every
// input: the old loop emits a full window [base, base+chunkWords) exactly
// when word base+chunkWords exists (i.e. pending ≥ chunkWords+1), then one
// final window [base_last, T) where base_last is the first multiple of
// chunkWords−chunkOverlap ≥ T−chunkWords. chunkBytes reproduces exactly that
// sequence (see the pending ≥ chunkWords+1 rule below), so the window
// boundaries and text ranges match by construction. chunk_test.go keeps the
// old implementation verbatim as refChunkText and proves equivalence over
// the golden fixtures, a multi-window 1-word/line file, and randomized
// differential fuzzing.
//
// (Design deviation on record: the finding suggested ~512 KiB byte windows
// with partial-span carry. Because the upload path already holds the whole
// body in one buffer — io.ReadAll under the 5 MiB cap, and classifyText
// needs whole-payload UTF-8 — a single continuous scan with a bounded span
// window is strictly simpler and seam-free: window-edge word splits are
// identical to the whole-file scan by definition, not by carry logic.)

// wordSpan records the byte span of one whitespace-delimited word so chunks
// can keep the ORIGINAL text (newlines/indentation included) instead of a
// normalized join — important for code files. UTF-8 continuation bytes never
// collide with ASCII spaces, so byte-level scanning is safe.
type wordSpan struct{ start, end int }

func isSpaceByte(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r', '\v', '\f':
		return true
	}
	return false
}

// chunkBytes splits data into ~chunkWords-word windows advancing by
// (chunkWords − chunkOverlap) words, so consecutive chunks share ~80 words
// of context. Empty/whitespace-only input yields no chunks. Each emitted
// chunk is hard-capped at maxChunkBytes (a pathological file of one gigantic
// "word" cannot blow the per-chunk memory budget). Streaming: live memory is
// O(chunkWords) spans + one chunk, independent of input size.
func chunkBytes(data []byte) []string {
	const step = chunkWords - chunkOverlap
	var out []string

	// Live word spans spans[head:]; spans[head] has absolute word index
	// base. The window still being built starts at base.
	var spans []wordSpan
	head, base, total := 0, 0, 0

	// emit words [base, base+count) as one (possibly cap-split) chunk.
	emit := func(count int) {
		s, e := spans[head].start, spans[head+count-1].end
		out = append(out, capBytes(string(data[s:e]))...)
	}

	for i := 0; i < len(data); {
		for i < len(data) && isSpaceByte(data[i]) {
			i++
		}
		if i >= len(data) {
			break
		}
		j := i
		for j < len(data) && !isSpaceByte(data[j]) {
			j++
		}
		spans = append(spans, wordSpan{i, j})
		total++
		i = j
		// A full window is provably complete (and NOT the final one) once
		// the word AFTER it exists: total−base ≥ chunkWords+1. Emit it and
		// slide by step — the trailing chunkOverlap words stay live and
		// open the next window, exactly like the old whole-file loop.
		for total-base >= chunkWords+1 {
			emit(chunkWords)
			base += step
			head += step
		}
		if head >= 2*chunkWords { // keep the slice small; spans[head:] ≤ 801
			n := copy(spans, spans[head:])
			spans = spans[:n]
			head = 0
		}
	}
	if total > base { // final (clamped) window: words [base, total)
		emit(total - base)
	}
	return out
}

// chunkText is the string-facing form of chunkBytes, kept for tests and any
// caller already holding a string (it copies on conversion; upload paths
// should pass the buffer to chunkBytes directly and avoid the copy).
func chunkText(text string) []string { return chunkBytes([]byte(text)) }

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
