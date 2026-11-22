package providers

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strings"
)

// maxSSELine caps the size of a single SSE line so a broken upstream cannot
// make us allocate without bound.
const maxSSELine = 4 << 20 // 4 MiB

// sseDone is the sentinel OpenAI-compatible upstreams send as the final
// data frame ("data: [DONE]").
const sseDone = "[DONE]"

// sseEvent is one dispatched SSE frame. Event is "" for anonymous data
// frames, which is what OpenAI-compatible upstreams send.
type sseEvent struct {
	Event string
	Data  string
}

// parseSSE reads an SSE byte stream and calls fn once per frame until EOF,
// until fn returns an error (returned as-is), or on read failure (wrapped).
//
// Robustness (BE-providers task item 4):
//   - line terminators \n, \r\n and bare \r are all accepted;
//   - reads may split a line — or a multi-byte UTF-8 rune — at any byte
//     boundary: the splitter only scans raw bytes for CR/LF (UTF-8
//     continuation bytes never collide with 0x0A/0x0D) and bufio buffers
//     partial lines across reads;
//   - comment/keepalive lines ("...: ping") and id/retry/unknown fields are
//     ignored;
//   - a frame still pending when EOF arrives is dispatched anyway (tolerant
//     of gateways that forget the final blank line).
func parseSSE(r io.Reader, fn func(sseEvent) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxSSELine)
	sc.Split(scanSSELine)

	var (
		eventName string
		data      []string
	)
	flush := func() error {
		if len(data) == 0 {
			eventName = ""
			return nil
		}
		ev := sseEvent{Event: eventName, Data: strings.Join(data, "\n")}
		eventName, data = "", data[:0]
		return fn(ev)
	}

	for sc.Scan() {
		line := sc.Text()
		if line == "" { // blank line dispatches the pending frame
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		if line[0] == ':' { // comment / keepalive
			continue
		}
		field, value := line, ""
		if i := strings.IndexByte(line, ':'); i >= 0 {
			field, value = line[:i], strings.TrimPrefix(line[i+1:], " ")
		}
		switch field {
		case "event":
			eventName = value
		case "data":
			data = append(data, value)
		default: // id, retry, unknown → ignore
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("providers: sse read failed: %w", err)
	}
	return flush() // tolerate a final frame without its blank line
}

// scanSSELine is a bufio.SplitFunc splitting on \n, \r\n or bare \r. When a
// \r ends the available buffer it waits for one more byte so "\r\n" is
// consumed as a single terminator instead of an extra blank line.
func scanSSELine(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		if data[i] == '\n' {
			return i + 1, data[:i], nil
		}
		if i+1 < len(data) {
			adv := i + 1
			if data[i+1] == '\n' {
				adv++
			}
			return adv, data[:i], nil
		}
		if atEOF {
			return i + 1, data[:i], nil
		}
		return 0, nil, nil // need one more byte to tell \r from \r\n
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil // request more data
}
