// Folders repo (BE-apichat / BE4 per docs/CONTRACTS.md §Store): per-user
// folder CRUD with nullable parent and cycle-safe moves.
//
// CONTRACT-DEVIATION: the task brief mentioned a `color` on folders, but the
// frozen schema.sql / store.Folder struct carry no folder color (color lives
// on tags). Folders therefore expose name/parent/sort exactly as scaffolded.

package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/google/uuid"
)

// MaxFolderNameLen is the contract cap for folder names.
const MaxFolderNameLen = 120

// ErrInvalidName is returned for empty/oversized folder or tag names.
var ErrInvalidName = errors.New("store: invalid name")

// ErrParentCycle is returned when a folder would become its own ancestor.
var ErrParentCycle = errors.New("store: folder parent cycle")

const folderCols = "id, user_id, name, parent_id, sort, created_at"

func scanFolder(scan func(...any) error) (*Folder, error) {
	f := &Folder{}
	var parent sql.NullString
	if err := scan(&f.ID, &f.UserID, &f.Name, &parent, &f.Sort, &f.CreatedAt); err != nil {
		return nil, err
	}
	if parent.Valid {
		p := parent.String
		f.ParentID = &p
	}
	return f, nil
}

// Create inserts a folder for its owner. Name must be non-empty and
// ≤ MaxFolderNameLen runes; when ParentID is set the parent must exist,
// belong to the same user and not create a cycle.
func (r *FoldersRepo) Create(ctx context.Context, f *Folder) error {
	name := strings.TrimSpace(f.Name)
	if name == "" || len([]rune(name)) > MaxFolderNameLen {
		return ErrInvalidName
	}
	f.Name = name
	if f.ID == "" {
		f.ID = uuid.NewString()
	}
	if f.CreatedAt == 0 {
		f.CreatedAt = nowUnix()
	}
	if err := r.checkParent(ctx, f.UserID, f.ID, f.ParentID); err != nil {
		return err
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO folders (id, user_id, name, parent_id, sort, created_at)
		 VALUES (?1, ?2, ?3, ?4, ?5, ?6)`,
		f.ID, f.UserID, f.Name, nullStr(f.ParentID), f.Sort, f.CreatedAt)
	return err
}

// Get fetches a folder by id (ownership checked by caller or the
// user-scoped Update/Delete methods).
func (r *FoldersRepo) Get(ctx context.Context, id string) (*Folder, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+folderCols+` FROM folders WHERE id = ?1`, id)
	f, err := scanFolder(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return f, err
}

// List returns all folders of a user (sort, name, created order).
func (r *FoldersRepo) List(ctx context.Context, userID string) ([]*Folder, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+folderCols+` FROM folders WHERE user_id = ?1
		 ORDER BY sort ASC, name ASC, created_at ASC, id ASC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Folder
	for rows.Next() {
		f, err := scanFolder(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// Update writes name/parent/sort, scoped to the owner row (foreign owner →
// ErrNotFound so handlers can answer 404 without an existence leak).
func (r *FoldersRepo) Update(ctx context.Context, f *Folder) error {
	name := strings.TrimSpace(f.Name)
	if name == "" || len([]rune(name)) > MaxFolderNameLen {
		return ErrInvalidName
	}
	f.Name = name
	if err := r.checkParent(ctx, f.UserID, f.ID, f.ParentID); err != nil {
		return err
	}
	res, err := r.db.ExecContext(ctx,
		`UPDATE folders SET name = ?2, parent_id = ?3, sort = ?4
		 WHERE id = ?1 AND user_id = ?5`,
		f.ID, f.Name, nullStr(f.ParentID), f.Sort, f.UserID)
	return affected(res, err)
}

// Delete removes an own folder; chats.folder_id and child folders' parent_id
// are nulled by ON DELETE SET NULL foreign keys.
func (r *FoldersRepo) Delete(ctx context.Context, id, userID string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM folders WHERE id = ?1 AND user_id = ?2`, id, userID)
	return affected(res, err)
}

// checkParent validates a (possibly provisional) parent link: the parent
// must exist, belong to userID, never equal the folder itself and never sit
// below it (cycle). Foreign-owned parents surface as ErrNotFound.
func (r *FoldersRepo) checkParent(ctx context.Context, userID, id string, parentID *string) error {
	if parentID == nil || *parentID == "" {
		return nil
	}
	seen := map[string]bool{}
	for cur := *parentID; cur != ""; {
		if cur == id || seen[cur] {
			return ErrParentCycle
		}
		seen[cur] = true
		p, err := r.Get(ctx, cur)
		if err != nil {
			return err
		}
		if p.UserID != userID {
			return ErrNotFound
		}
		if p.ParentID != nil {
			cur = *p.ParentID
		} else {
			cur = ""
		}
	}
	return nil
}
