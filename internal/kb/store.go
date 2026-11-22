package kb

// Own data access for kb tables (runs on store.DB — the store package owns
// no kb repos and must not gain any). All SQL is parameterized.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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

// insertFileChunks atomically records one ingested file plus all of its
// embedded chunks. Vectors must already correspond 1:1 to chunks.
func (d *Deps) insertFileChunks(ctx context.Context, collectionID, name, mime string, textBytes int64,
	chunks []string, vectors [][]float32) (*kbFile, error) {
	if len(chunks) != len(vectors) {
		return nil, fmt.Errorf("kb: %d chunks but %d vectors", len(chunks), len(vectors))
	}
	tx, err := d.Store.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck // best-effort after commit

	f := &kbFile{ID: uuid.NewString(), CollectionID: collectionID, Name: name, Mime: mime,
		Bytes: textBytes, CreatedAt: nowUnix(), ChunkCount: len(chunks)}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO kb_files (id, collection_id, name, mime, bytes, created_at) VALUES (?1, ?2, ?3, ?4, ?5, ?6)`,
		f.ID, f.CollectionID, f.Name, f.Mime, f.Bytes, f.CreatedAt); err != nil {
		return nil, err
	}
	for i, content := range chunks {
		blob := encodeVec(vectors[i])
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO kb_chunks (id, file_id, ordinal, content, embedding, dim) VALUES (?1, ?2, ?3, ?4, ?5, ?6)`,
			uuid.NewString(), f.ID, i, content, blob, len(vectors[i])); err != nil {
			return nil, err
		}
	}
	return f, tx.Commit()
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

// chunkRow is one loaded chunk row pending the cosine scan.
type chunkRow struct {
	chunkID, fileID, fileName string
	ordinal                   int
	content                   string
	embedding                 []byte
	dim                       int
}

// loadChunks loads the embeddings of the given (already ownership-filtered)
// collections, capped at maxScanChunks rows (RAM discipline: load only what
// the selected collections hold, scan, release).
func (d *Deps) loadChunks(ctx context.Context, userID string, collectionIDs []string) ([]chunkRow, error) {
	if len(collectionIDs) == 0 {
		return nil, nil
	}
	args := make([]any, 0, len(collectionIDs)+1)
	args = append(args, userID)
	ph := make([]string, 0, len(collectionIDs))
	for _, id := range collectionIDs {
		args = append(args, id)
		ph = append(ph, fmt.Sprintf("?%d", len(args)))
	}
	args = append(args, maxScanChunks)
	rows, err := d.Store.DB.QueryContext(ctx,
		`SELECT ch.id, ch.file_id, f.name, ch.ordinal, ch.content, ch.embedding, ch.dim
		 FROM kb_chunks ch
		 JOIN kb_files f ON f.id = ch.file_id
		 JOIN kb_collections c ON c.id = f.collection_id
		 WHERE c.user_id = ?1 AND f.collection_id IN (`+strings.Join(ph, ",")+`)
		 ORDER BY f.created_at ASC, ch.ordinal ASC LIMIT ?`+fmt.Sprint(len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []chunkRow
	for rows.Next() {
		var r chunkRow
		if err := rows.Scan(&r.chunkID, &r.fileID, &r.fileName, &r.ordinal, &r.content, &r.embedding, &r.dim); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// scanChunks brute-force scores rows against the query vector and returns
// the top-k hits. Rows whose blob fails to decode or whose dim disagrees
// with the blob are skipped (corrupt data must never fail a whole search).
func scanChunks(rows []chunkRow, qvec []float32, k int) []chunkHit {
	hits := make([]chunkHit, 0, len(rows))
	for _, r := range rows {
		v, err := decodeVec(r.embedding)
		if err != nil || len(v) == 0 {
			continue
		}
		hits = append(hits, chunkHit{
			ChunkID:  r.chunkID,
			FileID:   r.fileID,
			FileName: r.fileName,
			Ordinal:  r.ordinal,
			Content:  r.content,
			Score:    cosine(qvec, v),
		})
	}
	return pickTopK(hits, k)
}

// searchChunks embeds q and brute-scans the selected collections' chunks.
func (d *Deps) searchChunks(ctx context.Context, userID string, collectionIDs []string, q string, k int) ([]chunkHit, error) {
	target, err := d.pickEmbedModel(ctx)
	if err != nil {
		return nil, err
	}
	qvecs, err := d.embedTexts(ctx, target, []string{q})
	if err != nil {
		return nil, err
	}
	rows, err := d.loadChunks(ctx, userID, collectionIDs)
	if err != nil {
		return nil, err
	}
	return scanChunks(rows, qvecs[0], k), nil
}
