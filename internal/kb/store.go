package kb

// Own data access for kb tables (runs on store.DB — the store package owns
// no kb repos and must not gain any). All SQL is parameterized.

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
)

func nowUnix() int64 { return time.Now().Unix() }

type collection struct {
	ID        string
	UserID    string
	Name      string
	CreatedAt int64
}

type kbFile struct {
	ID           string
	CollectionID string
	Name         string
	Mime         string
	Bytes        int64
	CreatedAt    int64
	ChunkCount   int
}

// collectionRow is one GET /api/kb list entry.
type collectionRow struct {
	Collection collection
	FileCount  int
	ChunkCount int
}

// createCollection inserts a new collection owned by userID.
func (d *Deps) createCollection(ctx context.Context, userID, name string) (*collection, error) {
	c := &collection{ID: uuid.NewString(), UserID: userID, Name: name, CreatedAt: nowUnix()}
	_, err := d.Store.DB.ExecContext(ctx,
		`INSERT INTO kb_collections (id, user_id, name, created_at) VALUES (?1, ?2, ?3, ?4)`,
		c.ID, c.UserID, c.Name, c.CreatedAt)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// getCollection fetches a collection the caller owns; foreign or missing
// rows both return ErrNotFound (no existence leak).
func (d *Deps) getCollection(ctx context.Context, userID, id string) (*collection, error) {
	row := d.Store.DB.QueryRowContext(ctx,
		`SELECT id, user_id, name, created_at FROM kb_collections WHERE id = ?1 AND user_id = ?2`, id, userID)
	c := &collection{}
	if err := row.Scan(&c.ID, &c.UserID, &c.Name, &c.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return c, nil
}

// listCollections pages the caller's collections, newest first, with
// file/chunk tallies.
func (d *Deps) listCollections(ctx context.Context, userID string, page, per int) ([]collectionRow, int, error) {
	if page < 1 {
		page = 1
	}
	if per <= 0 {
		per = 30
	}
	if per > 100 {
		per = 100
	}
	var total int
	if err := d.Store.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM kb_collections WHERE user_id = ?1`, userID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := d.Store.DB.QueryContext(ctx,
		`SELECT c.id, c.user_id, c.name, c.created_at,
			(SELECT COUNT(*) FROM kb_files f WHERE f.collection_id = c.id),
			(SELECT COUNT(*) FROM kb_chunks ch JOIN kb_files f2 ON f2.id = ch.file_id WHERE f2.collection_id = c.id)
		 FROM kb_collections c WHERE c.user_id = ?1
		 ORDER BY c.created_at DESC, c.id DESC LIMIT ?2 OFFSET ?3`,
		userID, per, (page-1)*per)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []collectionRow
	for rows.Next() {
		var cr collectionRow
		if err := rows.Scan(&cr.Collection.ID, &cr.Collection.UserID, &cr.Collection.Name,
			&cr.Collection.CreatedAt, &cr.FileCount, &cr.ChunkCount); err != nil {
			return nil, 0, err
		}
		out = append(out, cr)
	}
	return out, total, rows.Err()
}

// deleteCollection removes chunks → files → collection in one tx (explicit,
// so removal never depends on the connection's foreign_keys pragma).
func (d *Deps) deleteCollection(ctx context.Context, userID, id string) error {
	c, err := d.getCollection(ctx, userID, id)
	if err != nil {
		return err
	}
	tx, err := d.Store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // best-effort after commit
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM kb_chunks WHERE file_id IN (SELECT id FROM kb_files WHERE collection_id = ?1)`, c.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM kb_files WHERE collection_id = ?1`, c.ID); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM kb_collections WHERE id = ?1`, c.ID)
	if err != nil {
		return err
	}
	if err := mustAffect(res); err != nil {
		return err
	}
	return tx.Commit()
}

// listFiles lists a collection's files (caller must have verified ownership).
func (d *Deps) listFiles(ctx context.Context, collectionID string) ([]kbFile, error) {
	rows, err := d.Store.DB.QueryContext(ctx,
		`SELECT f.id, f.collection_id, f.name, f.mime, f.bytes, f.created_at,
			(SELECT COUNT(*) FROM kb_chunks ch WHERE ch.file_id = f.id)
		 FROM kb_files f WHERE f.collection_id = ?1 ORDER BY f.created_at ASC, f.id ASC`, collectionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []kbFile
	for rows.Next() {
		var f kbFile
		if err := rows.Scan(&f.ID, &f.CollectionID, &f.Name, &f.Mime, &f.Bytes, &f.CreatedAt, &f.ChunkCount); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// getFile fetches one file that must belong to collectionID.
func (d *Deps) getFile(ctx context.Context, collectionID, fileID string) (*kbFile, error) {
	row := d.Store.DB.QueryRowContext(ctx,
		`SELECT f.id, f.collection_id, f.name, f.mime, f.bytes, f.created_at,
			(SELECT COUNT(*) FROM kb_chunks ch WHERE ch.file_id = f.id)
		 FROM kb_files f WHERE f.id = ?1 AND f.collection_id = ?2`, fileID, collectionID)
	f := &kbFile{}
	if err := row.Scan(&f.ID, &f.CollectionID, &f.Name, &f.Mime, &f.Bytes, &f.CreatedAt, &f.ChunkCount); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return f, nil
}

// insertFileChunksStream is the RES-5 ingest pipeline: it embeds chunks one
// embedBatchMax batch at a time and commits each batch's rows IMMEDIATELY,
// so the file's vectors never accumulate — live embedding memory is a single
// batch (64 × 1536 × 4B ≈ 400 KiB at realistic dims) instead of every vector
// for the file (~22 MiB for a 5 MiB 1-word/line upload). Consumed chunk
// strings are blanked as their rows commit (release-after-use on the text
// side too).
//
// Pipeline shape: strictly sequential embed→insert per batch — a bounded
// staging queue at depth 1, the tightest bound the ≤2-batch queue design
// allows. A concurrent embedder feeding a ≤2-deep queue would double live
// vectors to overlap embed latency with DB commits; commits here are
// millisecond-scale against 25 ms–second embed round-trips, so the overlap
// would buy <10% wall time while violating this package's zero-goroutine
// RAM discipline.
//
// Atomicity (per file, vs the old single-tx write): FK enforcement is ON
// (store DSN pragma), so chunks cannot be staged parentless and revealed by
// a final file-row insert, and ONE tx held open across all embed HTTP calls
// would pin the SQLite writer for the whole ingest — both rejected.
// Instead the file row commits inside the FIRST batch's tx (which satisfies
// kb_chunks' FK) and every later batch is its own tx; any request-visible
// failure (embed error, ragged dimensions, DB error, ctx cancel) runs
// discardIngest, so the END STATE of every failure is byte-identical to the
// old all-or-nothing write: zero rows for the file. Batch-0 failures need no
// compensation at all — nothing ever committed, so the file was never
// visible. Two windows remain inherently traded for the memory bound:
// (a) a concurrent reader can observe a partially ingested file during the
// (seconds-long) ingest window, and (b) a PROCESS CRASH mid-ingest leaves a
// partial file where the old code left nothing. Both stay fully valid rows
// (FK intact, ordinals contiguous); the retry path is the documented one —
// delete the file and re-upload it (same as today's multi-file partial
// success semantics).
//
// target may be nil iff len(chunks)==0 (empty files never reach the
// provider). The dim-consistency invariant embedTexts enforced across
// batches is enforced here too.
func (d *Deps) insertFileChunksStream(ctx context.Context, target *embedTarget, collectionID, name, mime string,
	textBytes int64, chunks []string) (*kbFile, error) {
	if len(chunks) > 0 && target == nil {
		return nil, errors.New("kb: embed target required for non-empty ingest")
	}
	f := &kbFile{ID: uuid.NewString(), CollectionID: collectionID, Name: name, Mime: mime,
		Bytes: textBytes, CreatedAt: nowUnix(), ChunkCount: len(chunks)}

	dim := 0
	committed := false
	for start := 0; start < len(chunks); start += embedBatchMax {
		end := min(start+embedBatchMax, len(chunks))
		vecs, err := d.embedBatch(ctx, target, chunks[start:end])
		if err != nil {
			d.discardIngest(ctx, f.ID, committed)
			return nil, err
		}
		if dim == 0 {
			dim = len(vecs[0])
		}
		for _, v := range vecs {
			if len(v) != dim {
				d.discardIngest(ctx, f.ID, committed)
				return nil, fmt.Errorf("%w: inconsistent embedding dimensions", ErrEmbedUpstream)
			}
		}
		if err := d.insertChunkBatch(ctx, f, start, chunks[start:end], vecs); err != nil {
			d.discardIngest(ctx, f.ID, committed)
			return nil, err
		}
		committed = true
		// Release after use: the batch's vectors die at its commit and its
		// chunk strings are never read again (the caller's slice headers stay
		// reachable, so blank the strings to actually free the bytes).
		clear(vecs)
		for i := start; i < end; i++ {
			chunks[i] = ""
		}
	}
	if !committed {
		// Empty file: the legacy single-tx file-row write, nothing to stream.
		tx, err := d.Store.DB.BeginTx(ctx, nil)
		if err != nil {
			return nil, err
		}
		defer tx.Rollback() //nolint:errcheck // best-effort after commit
		if err := insertFileRow(ctx, tx, f); err != nil {
			return nil, err
		}
		return f, tx.Commit()
	}
	return f, nil
}

// insertChunkBatch commits one embed batch's chunk rows; the FIRST batch's tx
// also carries the file row (FK parent). Ordinals stay file-contiguous:
// batch starting at index `start` writes ordinals start..start+len-1, exactly
// the numbering the old whole-file loop produced.
func (d *Deps) insertChunkBatch(ctx context.Context, f *kbFile, start int, texts []string, vecs [][]float32) error {
	tx, err := d.Store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // best-effort after commit
	if start == 0 {
		if err := insertFileRow(ctx, tx, f); err != nil {
			return err
		}
	}
	for i, content := range texts {
		blob := encodeVec(vecs[i])
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO kb_chunks (id, file_id, ordinal, content, embedding, dim) VALUES (?1, ?2, ?3, ?4, ?5, ?6)`,
			uuid.NewString(), f.ID, start+i, content, blob, len(vecs[i])); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// insertFileRow writes the kb_files row inside an open tx.
func insertFileRow(ctx context.Context, tx *sql.Tx, f *kbFile) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO kb_files (id, collection_id, name, mime, bytes, created_at) VALUES (?1, ?2, ?3, ?4, ?5, ?6)`,
		f.ID, f.CollectionID, f.Name, f.Mime, f.Bytes, f.CreatedAt)
	return err
}

// discardIngest is the compensating delete restoring per-file all-or-nothing
// after a mid-ingest failure: it removes every row insertFileChunksStream may
// have committed for fileID. committed=false means the file was never
// visible (batch 0 failed) and there is nothing to undo. It must run even
// when ctx is already dead (canceled/timed-out requests are the common
// failure cause), hence WithoutCancel + its own deadline; a failed rollback
// leaves only a stale partial file a reader can see, logged for hygiene.
func (d *Deps) discardIngest(ctx context.Context, fileID string, committed bool) {
	if !committed {
		return
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	tx, err := d.Store.DB.BeginTx(cctx, nil)
	if err != nil {
		slog.Warn("kb: ingest rollback failed (could not open tx)", "file_id", fileID, "err", err)
		return
	}
	defer tx.Rollback() //nolint:errcheck // best-effort after commit
	// Chunks first: foreign_keys is ON.
	if _, err := tx.ExecContext(cctx, `DELETE FROM kb_chunks WHERE file_id = ?1`, fileID); err != nil {
		slog.Warn("kb: ingest rollback failed (chunks)", "file_id", fileID, "err", err)
		return
	}
	if _, err := tx.ExecContext(cctx, `DELETE FROM kb_files WHERE id = ?1`, fileID); err != nil {
		slog.Warn("kb: ingest rollback failed (file row)", "file_id", fileID, "err", err)
		return
	}
	if err := tx.Commit(); err != nil {
		slog.Warn("kb: ingest rollback failed (commit)", "file_id", fileID, "err", err)
	}
}

// deleteFile removes one file's chunks and row (ownership checked by caller).
func (d *Deps) deleteFile(ctx context.Context, fileID string) error {
	tx, err := d.Store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // best-effort after commit
	if _, err := tx.ExecContext(ctx, `DELETE FROM kb_chunks WHERE file_id = ?1`, fileID); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM kb_files WHERE id = ?1`, fileID)
	if err != nil {
		return err
	}
	if err := mustAffect(res); err != nil {
		return err
	}
	return tx.Commit()
}

func mustAffect(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ownedCollectionIDs filters ids down to collections the user actually owns
// (a chat's params_json is user data: never trust the ids it references).
func (d *Deps) ownedCollectionIDs(ctx context.Context, userID string, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, 0, len(ids)+1)
	args = append(args, userID)
	ph := make([]string, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
		ph = append(ph, fmt.Sprintf("?%d", len(args)))
	}
	rows, err := d.Store.DB.QueryContext(ctx,
		`SELECT id FROM kb_collections WHERE user_id = ?1 AND id IN (`+strings.Join(ph, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// scanRow is one pass-1 chunk row: everything the cosine scan needs.
// It deliberately carries NO content text — the KB RAM finding was that
// the old pass loaded content for EVERY scanned row (20k rows ≈ 100 MiB
// per search, riding every enriched chat turn). Content is re-fetched in
// pass 2 for the surviving k rows only (attachContents).
type scanRow struct {
	chunkID, fileID, fileName string
	ordinal                   int
	embedding                 []byte
}

// scanCollections is pass 1 of the two-pass search: it streams the
// (already ownership-filtered) collections' embeddings — never their
// content — and folds each row straight into the running top-k set, so
// live memory is O(k + one row), not O(rows). The scan stops at maxRows
// rows or maxBytes of cumulative embedding BLOBs, whichever comes first;
// rows past either cap are ignored — the same documented semantics the
// original maxScanChunks row cap always had. Caps are parameters so
// tests can drive them; production callers pass the kb.go constants.
func (d *Deps) scanCollections(ctx context.Context, userID string, collectionIDs []string,
	qvec []float32, k, maxRows int, maxBytes int64) ([]chunkHit, error) {
	if len(collectionIDs) == 0 || k <= 0 {
		return nil, nil
	}
	args := make([]any, 0, len(collectionIDs)+2)
	args = append(args, userID)
	ph := make([]string, 0, len(collectionIDs))
	for _, id := range collectionIDs {
		args = append(args, id)
		ph = append(ph, fmt.Sprintf("?%d", len(args)))
	}
	args = append(args, maxRows)
	rows, err := d.Store.DB.QueryContext(ctx,
		`SELECT ch.id, ch.file_id, f.name, ch.ordinal, ch.embedding
		 FROM kb_chunks ch
		 JOIN kb_files f ON f.id = ch.file_id
		 JOIN kb_collections c ON c.id = f.collection_id
		 WHERE c.user_id = ?1 AND f.collection_id IN (`+strings.Join(ph, ",")+`)
		 ORDER BY f.created_at ASC, ch.ordinal ASC LIMIT ?`+fmt.Sprint(len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cands []chunkHit
	var scanned int64
	for rows.Next() {
		var r scanRow
		if err := rows.Scan(&r.chunkID, &r.fileID, &r.fileName, &r.ordinal, &r.embedding); err != nil {
			return nil, err
		}
		if scanned+int64(len(r.embedding)) > maxBytes {
			break // byte budget spent; the remaining rows are ignored (see maxScanBytes)
		}
		scanned += int64(len(r.embedding))
		cands = addScored(cands, r, qvec, k)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return pickTopK(cands, k), nil // final stable rank (window may never have overflowed)
}

// scanRows scores rows that are already in memory (pure helper; the
// SQL-streamed scanCollections funnels through the same addScored core, so
// this and the live path cannot drift apart). Content stays empty.
func scanRows(rows []scanRow, qvec []float32, k int) []chunkHit {
	var cands []chunkHit
	for _, r := range rows {
		cands = addScored(cands, r, qvec, k)
	}
	return pickTopK(cands, k)
}

// addScored folds one row into the running top-k. Corrupt rows (empty or
// ragged BLOB) are skipped and a BLOB whose length disagrees with qvec
// scores 0 — both exactly as the original scanChunks behaved. Re-running
// pickTopK over the k+1 window keeps the same selection AND stable tie
// order as ranking all rows at once: stable sort preserves the tie order
// established so far, and a newer row always sorts in behind the equals
// that came before it. Callers apply one final pickTopK for the case
// where the window never overflowed.
func addScored(cands []chunkHit, r scanRow, qvec []float32, k int) []chunkHit {
	score, ok := scoreBlob(qvec, r.embedding)
	if !ok {
		return cands
	}
	cands = append(cands, chunkHit{
		ChunkID:  r.chunkID,
		FileID:   r.fileID,
		FileName: r.fileName,
		Ordinal:  r.ordinal,
		Score:    score,
	})
	if len(cands) > k {
		cands = pickTopK(cands, k)
	}
	return cands
}

// scoreBlob cosines qvec against a little-endian float32 BLOB directly on
// its bytes — no decodeVec, so zero per-row float32 allocations (the old
// decode churned one slice per row per search on pure GC waste). The
// arithmetic is bit-identical to cosine(qvec, decodeVec(blob)): same
// float32→float64 widening, same accumulation order (golden-pinned by
// TestScoreBlobBitIdenticalToDecode). ok=false ⇒ corrupt BLOB (ragged or
// empty). A decodable BLOB whose length disagrees with qvec scores 0,
// matching cosine's length-mismatch contract.
func scoreBlob(qvec []float32, blob []byte) (float64, bool) {
	if len(blob) == 0 || len(blob)%4 != 0 {
		return 0, false
	}
	var dot, na, nb float64
	if n := len(blob) / 4; n == len(qvec) {
		for i := range qvec {
			x := float64(qvec[i])
			y := float64(math.Float32frombits(binary.LittleEndian.Uint32(blob[i*4:])))
			dot += x * y
			na += x * x
			nb += y * y
		}
	}
	if na == 0 || nb == 0 {
		return 0, true
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb)), true
}

// attachContents is pass 2 of the two-pass search: ONE indexed query
// pulls the content text for the final k hit rows only (k ≤ searchMaxK).
// The user join re-checks ownership defensively even though the ids came
// out of pass 1. A row deleted between the passes keeps empty Content
// (stale but harmless; the hit's ids/scores stay truthful).
func (d *Deps) attachContents(ctx context.Context, userID string, hits []chunkHit) error {
	if len(hits) == 0 {
		return nil
	}
	args := make([]any, 0, len(hits)+1)
	args = append(args, userID)
	ph := make([]string, 0, len(hits))
	for _, h := range hits {
		args = append(args, h.ChunkID)
		ph = append(ph, fmt.Sprintf("?%d", len(args)))
	}
	rows, err := d.Store.DB.QueryContext(ctx,
		`SELECT ch.id, ch.content FROM kb_chunks ch
		 JOIN kb_files f ON f.id = ch.file_id
		 JOIN kb_collections c ON c.id = f.collection_id
		 WHERE c.user_id = ?1 AND ch.id IN (`+strings.Join(ph, ",")+`)`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	content := make(map[string]string, len(hits))
	for rows.Next() {
		var id, text string
		if err := rows.Scan(&id, &text); err != nil {
			return err
		}
		content[id] = text
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range hits {
		hits[i].Content = content[hits[i].ChunkID]
	}
	return nil
}

// searchChunks embeds q and runs the two-pass scan: pass 1 streams the
// selected collections' embeddings into a top-k window, pass 2 loads
// content for the survivors. See scanCollections for the caps.
func (d *Deps) searchChunks(ctx context.Context, userID string, collectionIDs []string, q string, k int) ([]chunkHit, error) {
	target, err := d.pickEmbedModel(ctx)
	if err != nil {
		return nil, err
	}
	qvecs, err := d.embedTexts(ctx, target, []string{q})
	if err != nil {
		return nil, err
	}
	hits, err := d.scanCollections(ctx, userID, collectionIDs, qvecs[0], k, maxScanChunks, maxScanBytes)
	if err != nil {
		return nil, err
	}
	if err := d.attachContents(ctx, userID, hits); err != nil {
		return nil, err
	}
	return hits, nil
}
