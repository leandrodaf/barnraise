package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/leandrodaf/barnraise/internal/ports"
)

var _ ports.FileReadLog = (*Store)(nil)

// NoteFiles records files an item's sessions read or changed. A file noted
// again keeps its first time; once changed it stays marked as changed.
func (s *Store) NoteFiles(ctx context.Context, project, itemID string, files []ports.FileTouch) error {
	if len(files) == 0 {
		return nil
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		for _, f := range files {
			at := f.At
			if at.IsZero() {
				at = time.Now()
			}
			w := 0
			if f.Write {
				w = 1
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO file_reads(project, item, path, write, time) VALUES(?, ?, ?, ?, ?)
				ON CONFLICT(project, item, path) DO UPDATE SET write = MAX(write, excluded.write)`,
				project, itemID, f.Path, w, at.UTC().UnixMilli()); err != nil {
				return err
			}
		}
		return nil
	})
}

// TouchedFiles lists the files noted for an item, by path.
func (s *Store) TouchedFiles(ctx context.Context, project, itemID string) ([]ports.FileTouch, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path, write, time FROM file_reads WHERE project = ? AND item = ? ORDER BY path`, project, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ports.FileTouch
	for rows.Next() {
		var f ports.FileTouch
		var w int
		var ms int64
		if err := rows.Scan(&f.Path, &w, &ms); err != nil {
			return nil, err
		}
		f.Write, f.At = w == 1, time.UnixMilli(ms).UTC()
		out = append(out, f)
	}
	return out, rows.Err()
}

// ReadItems lists the items of a project with files noted.
func (s *Store) ReadItems(ctx context.Context, project string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT item FROM file_reads WHERE project = ? ORDER BY item`, project)
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

// ForgetFiles drops what was noted for an item (it is finished).
func (s *Store) ForgetFiles(ctx context.Context, project, itemID string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM file_reads WHERE project = ? AND item = ?`, project, itemID)
		return err
	})
}
