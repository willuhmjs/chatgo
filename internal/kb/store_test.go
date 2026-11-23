// Two-pass scan tests (KB RAM finding): pass 1 (scanCollections/scanRows)
// must never touch content TEXT and must stream into a bounded top-k whose
// selection + tie order is identical to the old score-all-rows path; the
// row and byte caps stop-and-ignore with identical semantics; pass 2
// (attachContents) fetches content for survivors only and stays
// owner-scoped. The streaming scorer is golden-pinned bit-for-bit against
// the old decodeVec+cosine arithmetic.
package kb

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"testing"

	"github.com/google/uuid"
)

// --- test-side seeding (synthetic rows without the HTTP ingest path) ----

func (e *testEnv) seedScanFile(collID, name string, createdAt int64) string {
	e.t.Helper()
	id := uuid.NewString()
	if _, err := e.Store.DB.Exec(
		`INSERT INTO kb_files (id, collection_id, name, mime, bytes, created_at) VALUES (?1, ?2, ?3, 'text/plain', 0, ?4)`,
		id, collID, name, createdAt); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *testEnv) seedScanChunk(fileID string, ordinal int, content string, vec []float32) string {
	e.t.Helper()
	id := uuid.NewString()
	if _, err := e.Store.DB.Exec(
		`INSERT INTO kb_chunks (id, file_id, ordinal, content, embedding, dim) VALUES (?1, ?2, ?3, ?4, ?5, ?6)`,
		id, fileID, ordinal, content, encodeVec(vec), len(vec)); err != nil {
		e.t.Fatal(err)
	}
	return id
}

// --- two-pass contract ----------------------------------------------------

func TestTwoPassScanContentFlow(t *testing.T) {
	e := newTestEnv(t)
	coll := e.createCollection(e.tokA, "scan")
	f := e.seedScanFile(coll, "a.md", 1)
	idA := e.seedScanChunk(f, 0, "alpha", []float32{1, 0})
	idB := e.seedScanChunk(f, 1, "beta", []float32{0, 1})

	d := &Deps{Store: e.Store}
	ctx := context.Background()
	hits, err := d.scanCollections(ctx, e.UserA, []string{coll}, []float32{1, 0}, 2, maxScanChunks, maxScanBytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 || hits[0].ChunkID != idA || hits[1].ChunkID != idB {
		t.Fatalf("hits = %+v, want alpha(1.0) then beta(0.0)", hits)
	}
	for _, h := range hits {
		if h.Content != "" {
			t.Fatalf("pass 1 must not load content, got %q", h.Content)
		}
	}
	if err := d.attachContents(ctx, e.UserA, hits); err != nil {
		t.Fatal(err)
	}
	if hits[0].Content != "alpha" || hits[1].Content != "beta" {
		t.Fatalf("pass 2 contents = %q / %q", hits[0].Content, hits[1].Content)
	}
	if hits[0].FileName != "a.md" || hits[0].FileID != f || hits[1].Ordinal != 1 {
		t.Fatalf("pass-1 meta lost: %+v", hits)
	}
}

func TestAttachContentsOwnerScopeAndStaleRows(t *testing.T) {
	e := newTestEnv(t)
	coll := e.createCollection(e.tokA, "own")
	f := e.seedScanFile(coll, "a.md", 1)
	id := e.seedScanChunk(f, 0, "secret", []float32{1})

	d := &Deps{Store: e.Store}
	ctx := context.Background()

	// Foreign user's id cannot read content even when handed the chunk id.
	hits := []chunkHit{{ChunkID: id}}
	if err := d.attachContents(ctx, e.UserB, hits); err != nil {
		t.Fatal(err)
	}
	if hits[0].Content != "" {
		t.Fatalf("foreign attach leaked content: %q", hits[0].Content)
	}
	// Row deleted between passes (stale hit): empty content, no error.
	if _, err := e.Store.DB.Exec(`DELETE FROM kb_chunks WHERE id = ?1`, id); err != nil {
		t.Fatal(err)
	}
	hits2 := []chunkHit{{ChunkID: id}}
	if err := d.attachContents(ctx, e.UserA, hits2); err != nil {
		t.Fatalf("stale row must not error: %v", err)
	}
	if hits2[0].Content != "" {
		t.Fatalf("deleted row content = %q", hits2[0].Content)
	}
}

// --- caps: stop-and-ignore, same semantics for rows and bytes -------------

func TestScanCollectionsCaps(t *testing.T) {
	e := newTestEnv(t)
	coll := e.createCollection(e.tokA, "caps")
	f := e.seedScanFile(coll, "a.md", 1)
	// Five 2-dim rows (8-byte BLOBs) in scan order; the LAST row is the
	// only perfect match for q, so any early stop excludes it.
	ids := make([]string, 5)
	for i := range ids {
		vec := []float32{0, 1} // orthogonal to q → score 0
		if i == 4 {
			vec = []float32{1, 0} // score 1
		}
		ids[i] = e.seedScanChunk(f, i, "x", vec)
	}
	d := &Deps{Store: e.Store}
	ctx := context.Background()
	q := []float32{1, 0}

	contains := func(hits []chunkHit, id string) bool {
		for _, h := range hits {
			if h.ChunkID == id {
				return true
			}
		}
		return false
	}

	// Row cap: LIMIT 3 stops before the perfect match (row 5).
	hits, err := d.scanCollections(ctx, e.UserA, []string{coll}, q, 5, 3, maxScanBytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 3 || contains(hits, ids[4]) {
		t.Fatalf("row cap: %+v", hits)
	}
	// Byte cap: budget 16 B = exactly the first two 8-byte rows; the third
	// row would cross the budget, so the scan stops there (rows 3..5 out).
	hits, err = d.scanCollections(ctx, e.UserA, []string{coll}, q, 5, 5, 16)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 || hits[0].ChunkID != ids[0] || hits[1].ChunkID != ids[1] {
		t.Fatalf("byte cap: %+v, want first two rows only", hits)
	}
	// Generous caps: everything scans, perfect match ranks first.
	hits, err = d.scanCollections(ctx, e.UserA, []string{coll}, q, 5, 5, maxScanBytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 5 || hits[0].ChunkID != ids[4] || math.Abs(hits[0].Score-1) > 1e-9 {
		t.Fatalf("uncapped scan: %+v", hits)
	}
}

// --- pure scan parity with the legacy path --------------------------------

func TestScanRowsSkipsCorrupt(t *testing.T) {
	good := encodeVec([]float32{1, 0})
	rows := []scanRow{
		{chunkID: "ok", fileID: "f", embedding: good},
		{chunkID: "ragged", fileID: "f", embedding: []byte{1, 2, 3}},
		{chunkID: "empty", fileID: "f", embedding: []byte{}},
	}
	got := scanRows(rows, []float32{1, 0}, 5)
	if len(got) != 1 || got[0].ChunkID != "ok" {
		t.Fatalf("scanRows = %+v, want only the intact row", got)
	}
}

// TestScanRowsMatchesLegacyFullRank proves the streaming top-k window
// selects and orders hits EXACTLY like the old score-every-row-then-pickTopK
// scan. Integer components force heavy score ties, pinning the stable
// input-order tiebreak too.
func TestScanRowsMatchesLegacyFullRank(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	q := []float32{0.5, -0.25, 1, 0, 3}
	const n = 400
	rows := make([]scanRow, 0, n)
	legacy := make([]chunkHit, 0, n) // old algorithm: decodeVec + cosine, keep all
	for i := 0; i < n; i++ {
		v := make([]float32, 5)
		for j := range v {
			v[j] = float32(rng.Intn(3) - 1) // -1, 0, +1 → many identical scores
		}
		if i%17 == 0 {
			v = make([]float32, 5) // zero BLOB rows: score 0, still eligible hits
		}
		blob := encodeVec(v)
		r := scanRow{chunkID: fmt.Sprintf("c%03d", i), fileID: "f", fileName: "f.md", ordinal: i, embedding: blob}
		rows = append(rows, r)
		dec, err := decodeVec(blob)
		if err != nil {
			t.Fatal(err)
		}
		legacy = append(legacy, chunkHit{ChunkID: r.chunkID, FileID: "f", FileName: "f.md", Ordinal: i, Score: cosine(q, dec)})
	}
	want := pickTopK(legacy, 7) // in-place stable sort; legacy beyond [:7] no longer matters
	got := scanRows(rows, q, 7)
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.ChunkID != w.ChunkID || g.FileID != w.FileID || g.FileName != w.FileName ||
			g.Ordinal != w.Ordinal || math.Float64bits(g.Score) != math.Float64bits(w.Score) {
			t.Fatalf("rank %d: got %+v want %+v", i, g, w)
		}
	}
}

// --- streaming scorer bit-identity golden ---------------------------------

// TestScoreBlobBitIdenticalToDecode pins scoreBlob (streaming, allocation-
// free) against the old cosine(qvec, decodeVec(blob)) arithmetic: every
// score must match BIT-FOR-BIT, so ranking cannot drift after the rewrite.
func TestScoreBlobBitIdenticalToDecode(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for trial := 0; trial < 200; trial++ {
		n := 1 + rng.Intn(300)
		q := make([]float32, n)
		v := make([]float32, n)
		for i := range q {
			q[i] = float32(rng.NormFloat64())
			v[i] = float32(rng.NormFloat64())
		}
		switch trial % 5 {
		case 1:
			v = make([]float32, n) // zero row → 0 via the nb==0 branch
		case 2:
			v[n-1] = 1e30 // extreme component
		case 3:
			q[n-1] = -1e30
		}
		got, ok := scoreBlob(q, encodeVec(v))
		if !ok {
			t.Fatalf("trial %d: valid BLOB reported corrupt", trial)
		}
		if want := cosine(q, v); math.Float64bits(got) != math.Float64bits(want) {
			t.Fatalf("trial %d: got bits %016x want %016x", trial,
				math.Float64bits(got), math.Float64bits(want))
		}
	}
	// Corrupt BLOBs stay skippable; mismatched-but-valid BLOBs score 0 —
	// the exact old scanChunks skip-vs-hit rule.
	if _, ok := scoreBlob([]float32{1}, []byte{1, 2, 3}); ok {
		t.Fatal("ragged BLOB must be corrupt")
	}
	if _, ok := scoreBlob([]float32{1}, nil); ok {
		t.Fatal("empty BLOB must be corrupt")
	}
	if got, ok := scoreBlob([]float32{1, 2}, encodeVec([]float32{1})); !ok || got != 0 {
		t.Fatalf("dim-mismatch hit: got %v ok=%v, want score-0 hit", got, ok)
	}
}
