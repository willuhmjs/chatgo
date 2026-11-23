package kb

import (
	"encoding/binary"
	"errors"
	"math"
	"sort"
)

// Vector storage is float32 little-endian BLOB + brute-scan cosine
// (FEATURES-V2 §RAM discipline — no vector library, personal scale).

// encodeVec renders a float32 vector as its little-endian BLOB form.
func encodeVec(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(f))
	}
	return b
}

// decodeVec parses an encodeVec BLOB back to float32s.
func decodeVec(b []byte) ([]float32, error) {
	if len(b)%4 != 0 {
		return nil, errors.New("kb: corrupt embedding blob")
	}
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v, nil
}

// cosine returns the cosine similarity of a and b (0 for mismatched lengths,
// zero or empty vectors — a zero embedding is treated as "matches nothing").
func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// chunkHit is one scored search result (also feeds chat enrichment).
type chunkHit struct {
	ChunkID  string  `json:"chunk_id"`
	FileID   string  `json:"file_id"`
	FileName string  `json:"file_name"`
	Ordinal  int     `json:"ordinal"`
	Content  string  `json:"content"`
	Score    float64 `json:"score"`
}

// pickTopK returns the k highest-scoring hits (desc by score, ties keep
// input order). Pure function.
func pickTopK(hits []chunkHit, k int) []chunkHit {
	if k <= 0 || len(hits) == 0 {
		return nil
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if k > len(hits) {
		k = len(hits)
	}
	return hits[:k]
}
