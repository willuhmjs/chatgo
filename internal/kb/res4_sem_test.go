// RES-4 global upload-gate tests: the 2-slot package semaphore on the file-
// upload path must (a) answer the house 429 envelope with a message distinct
// from the per-user C9b limiter when full, (b) release slots on every
// completion path, (c) run AFTER the per-user limiter so cheap rejections
// stay cheap, and (d) actually bound concurrent ingests.
package kb

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
)

func TestUploadGateBusyEnvelopeAndRelease(t *testing.T) {
	e := newTestEnv(t)
	id := e.createCollection(e.tokA, "Gate")

	// Pin both slots from the test (in-package) to force the busy path.
	// Budget: the per-user limiter spends a token on EVERY upload attempt
	// (the C9b gate runs first), so this test stays within uploadPerMin=5.
	uploadSlots <- struct{}{}
	uploadSlots <- struct{}{}
	t.Cleanup(func() { // drain whatever pins remain (the body consumes one)
		for {
			select {
			case <-uploadSlots:
			default:
				return
			}
		}
	})

	// (1) Both full → house 429 with the distinct RES-4 message, no embed.
	batchesBefore, _ := e.Embed.snapshot()
	st, m, raw := e.upload(e.tokA, id, [2]string{"x.txt", "golang notes"})
	if st != http.StatusTooManyRequests {
		t.Fatalf("slots full: %d %q, want 429", st, raw)
	}
	msg, _ := m["error"].(string)
	if msg != "server busy ingesting, retry" {
		t.Fatalf("busy envelope = %q, want the distinct RES-4 message", msg)
	}
	if batchesAfter, _ := e.Embed.snapshot(); len(batchesAfter) != len(batchesBefore) {
		t.Fatal("429'd upload reached the embeddings provider")
	}

	// (2) A released slot admits the next upload immediately.
	<-uploadSlots
	if st, _, raw := e.upload(e.tokA, id, [2]string{"x.txt", "golang notes"}); st != http.StatusCreated {
		t.Fatalf("one free slot: %d %q, want 201", st, raw)
	}
	// (3) Handler completion releases its slot: a sequential follow-up gets
	// in without any further test-side manipulation (one slot stays pinned
	// by this test the whole time).
	if st, _, raw := e.upload(e.tokA, id, [2]string{"s.txt", "golang notes"}); st != http.StatusCreated {
		t.Fatalf("sequential upload: %d %q, want 201 (slot leak?)", st, raw)
	}
	// (4/5) Failed ingest paths release too: the 415 rejection (after the
	// gate) must not strand its slot — the very next upload succeeds.
	if st, _, _ := e.upload(e.tokA, id, [2]string{"evil.zip", "hi"}); st != http.StatusUnsupportedMediaType {
		t.Fatalf("bad ext: %d, want 415", st)
	}
	if st, _, _ := e.upload(e.tokA, id, [2]string{"y.txt", "pasta"}); st != http.StatusCreated {
		t.Fatalf("after 415 path: %d, want 201 (slot leaked by failure path)", st)
	}
}

func TestUploadGateRunsAfterPerUserLimiter(t *testing.T) {
	e := newTestEnv(t)
	id := e.createCollection(e.tokA, "Order")

	// One slot pinned, one always free: any 429 MUST come from the per-user
	// limiter (message-wise) while its budget lasts, proving the gate is
	// second in line.
	uploadSlots <- struct{}{}
	t.Cleanup(func() { <-uploadSlots })

	for i := 0; i < uploadPerMin; i++ {
		if st, _, raw := e.upload(e.tokA, id, [2]string{fmt.Sprintf("f%d.txt", i), "golang notes"}); st != http.StatusCreated {
			t.Fatalf("upload %d: %d %q, want 201", i+1, st, raw)
		}
	}
	st, m, raw := e.upload(e.tokA, id, [2]string{"late.txt", "golang notes"})
	if st != http.StatusTooManyRequests {
		t.Fatalf("over-limit: %d %q, want 429", st, raw)
	}
	if msg, _ := m["error"].(string); msg != "too many kb uploads, try again later" {
		t.Fatalf("limiter-first message = %q, want the C9b limiter envelope (gate must not preempt it)", msg)
	}
}

// TestUploadGateBoundsConcurrentIngest fires 8 uploads at once behind the
// slow (25 ms) embeddings stub and asserts the invariant: never more than
// maxConcurrentUploads ingests reach the embeddings provider at once, every
// request gets 201 or the busy 429, and at least one is admitted.
func TestUploadGateBoundsConcurrentIngest(t *testing.T) {
	e := mres4Run(t, "127.0.0.1:0")
	body := []byte(fmt.Sprintf("%s\n", mres4RepeatWord("golang", 500)))

	var wg sync.WaitGroup
	codes := make([]int, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); codes[i] = e.upload(i, body) }(i)
	}
	wg.Wait()

	ok, busy := 0, 0
	for _, c := range codes {
		switch c {
		case http.StatusCreated:
			ok++
		case http.StatusTooManyRequests:
			busy++
		default:
			t.Fatalf("unexpected status %d", c)
		}
	}
	if ok == 0 {
		t.Fatal("nothing was admitted")
	}
	if peak := e.peakConc.Load(); peak > maxConcurrentUploads {
		t.Fatalf("peak concurrent embed calls = %d, want ≤ %d", peak, maxConcurrentUploads)
	}
	// The 429 envelope content itself is pinned by
	// TestUploadGateBusyEnvelopeAndRelease.
	t.Logf("admitted=%d 429=%d peak_concurrent_embed=%d", ok, busy, e.peakConc.Load())
}

func mres4RepeatWord(w string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		if i > 0 {
			out += " "
		}
		out += w
	}
	return out
}
