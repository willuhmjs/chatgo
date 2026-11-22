package kb

import (
	"math"
	"testing"
)

func TestEncodeDecodeVecRoundTrip(t *testing.T) {
	in := []float32{0, -1.5, 3.25, 1e-8, math.MaxFloat32}
	blob := encodeVec(in)
	if len(blob) != 4*len(in) {
		t.Fatalf("blob length = %d, want %d", len(blob), 4*len(in))
	}
	out, err := decodeVec(blob)
	if err != nil {
		t.Fatal(err)
	}
	for i := range in {
		if in[i] != out[i] {
			t.Fatalf("element %d: got %v want %v", i, out[i], in[i])
		}
	}
	// Little-endian check: 1.0f32 = 0x3F800000 → bytes 00 00 80 3F.
	if got := encodeVec([]float32{1.0}); string(got) != "\x00\x00\x80\x3f" {
		t.Fatalf("LE encoding wrong: % x", got)
	}
	if _, err := decodeVec([]byte{1, 2, 3}); err == nil {
		t.Fatal("ragged blob must fail to decode")
	}
}

// TestCosineHandComputed pins cosine against values computed by hand:
//
//	a=[1,2,3], b=[4,5,6]
//	dot  = 4+10+18                     = 32
//	|a|  = sqrt(1+4+9)                 = sqrt(14)
//	|b|  = sqrt(16+25+36)              = sqrt(77)
//	cos  = 32/sqrt(14*77) = 32/sqrt(1078) ≈ 0.97463185
func TestCosineHandComputed(t *testing.T) {
	a := []float32{1, 2, 3}
	b := []float32{4, 5, 6}
	const want = 0.97463185
	if got := cosine(a, b); math.Abs(got-want) > 1e-6 {
		t.Fatalf("cosine([1,2,3],[4,5,6]) = %.8f, want %.8f", got, want)
	}
	// Orthogonal: [1,0]·[0,1] → 0.
	if got := cosine([]float32{1, 0}, []float32{0, 1}); got != 0 {
		t.Fatalf("orthogonal cosine = %v, want 0", got)
	}
	// Identical → 1.
	if got := cosine(b, b); math.Abs(got-1) > 1e-6 {
		t.Fatalf("self cosine = %v, want 1", got)
	}
	// Opposite → -1.
	if got := cosine(a, []float32{-1, -2, -3}); math.Abs(got+1) > 1e-6 {
		t.Fatalf("opposite cosine = %v, want -1", got)
	}
	// Degenerate cases all score 0.
	if got := cosine([]float32{0, 0}, []float32{1, 1}); got != 0 {
		t.Fatalf("zero-vector cosine = %v, want 0", got)
	}
	if got := cosine([]float32{1}, []float32{1, 2}); got != 0 {
		t.Fatalf("dim-mismatch cosine = %v, want 0", got)
	}
	if got := cosine(nil, nil); got != 0 {
		t.Fatalf("empty cosine = %v, want 0", got)
	}
}

func TestPickTopK(t *testing.T) {
	hits := []chunkHit{
		{ChunkID: "a", Score: 0.2},
		{ChunkID: "b", Score: 0.9},
		{ChunkID: "c", Score: 0.5},
		{ChunkID: "d", Score: 0.9},
	}
	got := pickTopK(hits, 3)
	want := []string{"b", "d", "c"} // score desc, ties keep input order
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	for i := range want {
		if got[i].ChunkID != want[i] {
			t.Fatalf("rank %d = %s, want %s", i, got[i].ChunkID, want[i])
		}
	}
	if pickTopK(hits, 0) != nil {
		t.Fatal("k=0 must return nil")
	}
	if k := pickTopK(nil, 5); k != nil {
		t.Fatal("empty input must return nil")
	}
	if got := pickTopK(hits, 99); len(got) != 4 {
		t.Fatalf("k>n should clamp to %d, got %d", 4, len(got))
	}
}

func TestScanChunksSkipsCorrupt(t *testing.T) {
	good := encodeVec([]float32{1, 0})
	rows := []chunkRow{
		{chunkID: "ok", fileID: "f", content: "x", embedding: good, dim: 2},
		{chunkID: "ragged", fileID: "f", content: "x", embedding: []byte{1, 2, 3}, dim: 2},
	}
	got := scanChunks(rows, []float32{1, 0}, 5)
	if len(got) != 1 || got[0].ChunkID != "ok" {
		t.Fatalf("scanChunks = %+v, want only the intact row", got)
	}
}
