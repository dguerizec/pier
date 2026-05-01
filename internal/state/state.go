// Package state persists workload metadata in a tiny SQLite file
// (DESIGN §5.3). The DB is a cache rebuildable from docker/git inspection;
// truth still lives in the runtime, not here.
package state

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

const Schema = `
CREATE TABLE IF NOT EXISTS workloads (
    project       TEXT NOT NULL,
    slug          TEXT NOT NULL,
    worktree_path TEXT NOT NULL,
    branch        TEXT NOT NULL,
    kind          TEXT NOT NULL,
    container_id  TEXT,
    pid           INTEGER,
    port          INTEGER,
    started_at    INTEGER NOT NULL,
    PRIMARY KEY (project, slug)
);

CREATE TABLE IF NOT EXISTS projects (
    name          TEXT PRIMARY KEY,
    path          TEXT NOT NULL,
    base_domain   TEXT NOT NULL,
    stack_file    TEXT NOT NULL,
    stack_service TEXT,
    last_seen     INTEGER NOT NULL
);
`

// ErrNotFound is returned when a (project, slug) pair has no row.
var ErrNotFound = errors.New("state: workload not found")

// Workload mirrors one row in the workloads table.
type Workload struct {
	Project      string
	Slug         string
	WorktreePath string
	Branch       string
	Kind         string // compose | process | dockerfile
	ContainerID  string
	PID          int64 // reserved; legacy field for the dropped process adapter
	Port         int   // reserved; same.
	StartedAt    time.Time
}

// Store wraps the SQLite handle.
type Store struct {
	db *sql.DB
}

// Open opens the file at path (creating it if missing) and ensures the
// schema is in place. Pass ":memory:" for tests.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("state: open %s: %w", path, err)
	}
	if _, err := db.Exec(Schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("state: init schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Upsert inserts or replaces a workload row.
func (s *Store) Upsert(w *Workload) error {
	if w.Project == "" || w.Slug == "" {
		return errors.New("state: project and slug are required")
	}
	if w.StartedAt.IsZero() {
		w.StartedAt = time.Now()
	}
	_, err := s.db.Exec(`
		INSERT INTO workloads
			(project, slug, worktree_path, branch, kind, container_id, pid, port, started_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project, slug) DO UPDATE SET
			worktree_path = excluded.worktree_path,
			branch        = excluded.branch,
			kind          = excluded.kind,
			container_id  = excluded.container_id,
			pid           = excluded.pid,
			port          = excluded.port,
			started_at    = excluded.started_at
	`,
		w.Project, w.Slug, w.WorktreePath, w.Branch, w.Kind,
		nullableString(w.ContainerID), nullableInt(w.PID), nullableInt(int64(w.Port)),
		w.StartedAt.Unix(),
	)
	return err
}

// Get fetches one workload. Returns ErrNotFound if the row is missing.
func (s *Store) Get(project, slug string) (*Workload, error) {
	row := s.db.QueryRow(`
		SELECT project, slug, worktree_path, branch, kind,
		       container_id, pid, port, started_at
		FROM workloads
		WHERE project = ? AND slug = ?
	`, project, slug)
	w, err := scanWorkload(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return w, err
}

// List returns every workload, ordered by (project, slug).
func (s *Store) List() ([]*Workload, error) {
	rows, err := s.db.Query(`
		SELECT project, slug, worktree_path, branch, kind,
		       container_id, pid, port, started_at
		FROM workloads
		ORDER BY project, slug
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Workload
	for rows.Next() {
		w, err := scanWorkload(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// Delete removes the row for (project, slug). No-op if absent.
func (s *Store) Delete(project, slug string) error {
	_, err := s.db.Exec(`DELETE FROM workloads WHERE project = ? AND slug = ?`, project, slug)
	return err
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanWorkload(r rowScanner) (*Workload, error) {
	var (
		w         Workload
		container sql.NullString
		pid       sql.NullInt64
		port      sql.NullInt64
		startedAt int64
	)
	if err := r.Scan(
		&w.Project, &w.Slug, &w.WorktreePath, &w.Branch, &w.Kind,
		&container, &pid, &port, &startedAt,
	); err != nil {
		return nil, err
	}
	if container.Valid {
		w.ContainerID = container.String
	}
	if pid.Valid {
		w.PID = pid.Int64
	}
	if port.Valid {
		w.Port = int(port.Int64)
	}
	w.StartedAt = time.Unix(startedAt, 0)
	return &w, nil
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableInt(n int64) any {
	if n == 0 {
		return nil
	}
	return n
}
