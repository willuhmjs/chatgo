// RES-5 ingest-pipeline tests: embed→insert now runs one ≤64-text batch at a
// time (live vectors stay one batch deep). These pin what that contract must
// keep from the old whole-file single-tx write:
//
//	a) batches commit incrementally (batch i's arrival sees batches <i
//	   already in the DB — the pipeline really streams);
//	b) multi-batch files persist complete with contiguous ordinals and
//	   every vector paired with its OWN chunk text across batch edges;
//	c) a mid-ingest failure (after earlier batches committed) leaves ZERO
//	   rows — the compensating delete restores the legacy all-or-nothing
//	   end state;
//	d) cross-batch dimension raggedness fails as 502 with nothing behind;
//	e) empty files still take the degenerate single-tx file-row path and
//	   never reach the provider.
package kb

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// res5Stub is a fixed-width embeddings stub with optional failure after
// n-1 batches (failAt) and optional dim narrowing from batch badDimAt on.
type res5Stub struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	dim      int
	n        int // batches handled so far
	failAt   int // 0 = never; n ⇒ batch n replies 500
	badDimAt int // 0 = never; n ⇒ batches ≥ n reply width-1
}

func newRes5Stub(t *testing.T, dim, failAt, badDimAt int) *res5Stub {
	s := &res5Stub{t: t, dim: dim, failAt: failAt, badDimAt: badDimAt}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *res5Stub) handle(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	var in struct {
		Input []string `json:"input"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.n++
	n := s.n
	fail := s.failAt > 0 && n == s.failAt
	dim := s.dim
	if s.badDimAt > 0 && n >= s.badDimAt {
		dim-- // ragged: narrower vectors from this batch on
	}
	s.mu.Unlock()

	if fail {
		http.Error(w, "upstream blew up", http.StatusInternalServerError)
		return
	}
	data := res5Vectors(in.Input, dim)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

// res5Vectors renders deterministic position-tagged vectors: the vector for
// input i has v[j] == float32((i+j)%dimWidth…) — actually v[j] = (i+j)%7, so
// a vector can only have arrived with its own input index, which is what
// pins vector↔row pairing across batch boundaries.
func res5Vectors(inputs []string, width int) []map[string]any {
	data := make([]map[string]any, 0, len(inputs))
	for i := range inputs {
		v := make([]float32, width)
		for j := range v {
			v[j] = float32((i + j) % 7)
		}
		data = append(data, map[string]any{"index": i, "embedding": v})
	}
	return data
}

// count returns the number of batches handled so far.
func (s *res5Stub) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

// pointRes5Provider repoints the env's embed provider at a custom stub.
func pointRes5Provider(t *testing.T, e *testEnv, url string) {
	t.Helper()
	ctx := context.Background()
	row, err := e.Store.Providers.Get(ctx, "p-embed")
	if err != nil {
		t.Fatal(err)
	}
	row.BaseURL = url + "/v1"
	if err := e.Store.Providers.Update(ctx, row); err != nil {
		t.Fatal(err)
	}
}

// res5Chunks is file text (distinct words) that the chunker cuts into
// exactly c windows — same formula as TestEmbedBatchesCappedAt64.
func res5Chunks(c int) string {
	out := make([]byte, 0, c*chunkWords*8)
	for i := 0; i < c*chunkWords-(c-1)*chunkOverlap; i++ {
		out = fmt.Appendf(out, "w%d ", i)
	}
	return string(out)
}

// res5FullRows pulls (ordinal, content, embedding) triples for one file.
func res5FullRows(t *testing.T, e *testEnv, fileID string) []struct {
	ord  int
	text string
	blob []byte
} {
	t.Helper()
	rows, err := e.Store.DB.Query(
		`SELECT ordinal, content, embedding FROM kb_chunks WHERE file_id = ?1 ORDER BY ordinal ASC`, fileID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []struct {
		ord  int
		text string
		blob []byte
	}
	for rows.Next() {
		var r struct {
			ord  int
			text string
			blob []byte
		}
		if err := rows.Scan(&r.ord, &r.text, &r.blob); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func res5Counts(t *testing.T, e *testEnv) (files, chunks int) {
	t.Helper()
	if err := e.Store.DB.QueryRow(`SELECT COUNT(*) FROM kb_files`).Scan(&files); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.DB.QueryRow(`SELECT COUNT(*) FROM kb_chunks`).Scan(&chunks); err != nil {
		t.Fatal(err)
	}
	return
}

// TestRes5PipelineStreamsAndPairs pins (a)+(b): three embed batches
// [64,64,22]; each batch arrival must see the PREVIOUS batches' rows already
// committed (batch 0 uncommitted during its own embed), and the finished
// file must be complete with every embedding value-stamped by its own
// in-batch index — a transposed vector across a batch edge cannot survive.
func TestRes5PipelineStreamsAndPairs(t *testing.T) {
	e := newTestEnv(t)
	id := e.createCollection(e.tokA, "Pipe")

	var mu sync.Mutex
	var arrivals []int64
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		var in struct {
			Input []string `json:"input"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		var n int64 // rows VISIBLE (uncommitted tx invisible) at this arrival
		if err := e.Store.DB.QueryRow(`SELECT COUNT(*) FROM kb_chunks`).Scan(&n); err != nil {
			t.Error(err)
		}
		mu.Lock()
		arrivals = append(arrivals, n)
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"data": res5Vectors(in.Input, 4)})
	}))
	t.Cleanup(stub.Close)
	pointRes5Provider(t, e, stub.URL)

	const chunks = 150 // ⇒ batches [64, 64, 22]
	st, m, raw := e.upload(e.tokA, id, [2]string{"pipe.txt", res5Chunks(chunks)})
	if st != http.StatusCreated {
		t.Fatalf("upload: %d %q", st, raw)
	}
	fileID := m["items"].([]any)[0].(map[string]any)["id"].(string)

	mu.Lock()
	got := append([]int64(nil), arrivals...)
	mu.Unlock()
	want := []int64{0, 64, 128}
	if len(got) != len(want) {
		t.Fatalf("embed arrivals = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("visible rows at batch %d arrival = %d, want %d (per-batch commits?)", i, got[i], want[i])
		}
	}

	rows := res5FullRows(t, e, fileID)
	if len(rows) != chunks {
		t.Fatalf("persisted rows = %d, want %d", len(rows), chunks)
	}
	for i, row := range rows {
		if row.ord != i {
			t.Fatalf("row %d ordinal = %d", i, row.ord)
		}
		vec, err := decodeVec(row.blob)
		if err != nil {
			t.Fatalf("row %d: %v", i, err)
		}
		inBatch := i % embedBatchMax
		for j, v := range vec {
			if w := float32((inBatch + j) % 7); v != w {
				t.Fatalf("row %d vec[%d] = %v, want %v (vector paired to the wrong chunk?)", i, j, v, w)
			}
		}
	}
}

// TestRes5PipelinePairsWithCannedStub runs a multi-batch upload through the
// DEFAULT env stub (deterministic testEmbed of the RECEIVED text) and
// asserts every stored blob equals encodeVec(testEmbed(its own content)).
func TestRes5PipelinePairsWithCannedStub(t *testing.T) {
	e := newTestEnv(t)
	id := e.createCollection(e.tokA, "Pair")
	st, m, raw := e.upload(e.tokA, id, [2]string{"pair.txt", res5Chunks(130)})
	if st != http.StatusCreated {
		t.Fatalf("upload: %d %q", st, raw)
	}
	fileID := m["items"].([]any)[0].(map[string]any)["id"].(string)

	rows := res5FullRows(t, e, fileID)
	if len(rows) != 130 {
		t.Fatalf("persisted rows = %d, want 130", len(rows))
	}
	for i, row := range rows {
		have, err := decodeVec(row.blob)
		if err != nil {
			t.Fatalf("row %d: %v", i, err)
		}
		if want := testEmbed(row.text); fmt.Sprint(want) != fmt.Sprint(have) {
			t.Fatalf("row %d embedding = %v, want testEmbed(content) = %v", i, have, want)
		}
	}
	batches, _ := e.Embed.snapshot()
	if len(batches) != 3 || batches[0] != 64 || batches[1] != 64 || batches[2] != 2 {
		t.Fatalf("embed batch sizes = %v, want [64 64 2]", batches)
	}
}

// TestRes5MidIngestFailureRollsBack pins (c): batch 1 commits 64 rows,
// batch 2 fails, and the compensating delete must leave NOTHING — the end
// state the legacy single-tx write produced. The retry upload then lands.
func TestRes5MidIngestFailureRollsBack(t *testing.T) {
	e := newTestEnv(t)
	id := e.createCollection(e.tokA, "Rollback")
	stub := newRes5Stub(t, 4, 2, 0)
	pointRes5Provider(t, e, stub.srv.URL)

	st, _, raw := e.upload(e.tokA, id, [2]string{"rb.txt", res5Chunks(150)})
	if st != http.StatusBadGateway {
		t.Fatalf("mid-ingest embed failure: %d %q, want 502", st, raw)
	}
	if stub.count() != 2 {
		t.Fatalf("stub saw %d batches, want 2 (batch 1 committed before the failure)", stub.count())
	}
	if files, chunks := res5Counts(t, e); files != 0 || chunks != 0 {
		t.Fatalf("failed ingest left %d files / %d chunks behind (compensating delete?)", files, chunks)
	}
	if st, _, raw := e.upload(e.tokA, id, [2]string{"rb.txt", res5Chunks(70)}); st != http.StatusCreated {
		t.Fatalf("retry upload: %d %q, want 201", st, raw)
	}
}

// TestRes5CrossBatchDimMismatch pins (d): batch 2 comes back one dimension
// narrower ⇒ 502 and zero rows (the committed batch-1 tx rolled back too).
func TestRes5CrossBatchDimMismatch(t *testing.T) {
	e := newTestEnv(t)
	id := e.createCollection(e.tokA, "Dims")
	stub := newRes5Stub(t, 4, 0, 2)
	pointRes5Provider(t, e, stub.srv.URL)

	st, m, raw := e.upload(e.tokA, id, [2]string{"dim.txt", res5Chunks(150)})
	if st != http.StatusBadGateway {
		t.Fatalf("ragged dims: %d %q, want 502", st, raw)
	}
	if msg, _ := m["error"].(string); msg == "" {
		t.Fatalf("no error envelope: %q", raw)
	}
	if files, chunks := res5Counts(t, e); files != 0 || chunks != 0 {
		t.Fatalf("ragged-dims ingest left %d files / %d chunks behind", files, chunks)
	}
}

// TestRes5EmptyFileDegenerate pins (e): zero chunks never reach the
// provider and land via the single file-row tx.
func TestRes5EmptyFileDegenerate(t *testing.T) {
	e := newTestEnv(t)
	id := e.createCollection(e.tokA, "Empty")
	before, _ := e.Embed.snapshot()
	st, _, raw := e.upload(e.tokA, id, [2]string{"empty.txt", ""})
	if st != http.StatusCreated {
		t.Fatalf("empty upload: %d %q, want 201", st, raw)
	}
	if after, _ := e.Embed.snapshot(); len(after) != len(before) {
		t.Fatal("empty-file upload reached the embeddings provider")
	}
	if _, chunks := res5Counts(t, e); chunks != 0 {
		t.Fatalf("empty file stored %d chunks", chunks)
	}
}
