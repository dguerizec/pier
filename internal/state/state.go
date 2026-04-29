// Package state persists pier metadata in a tiny SQLite file (DESIGN
// §5.3). Two tables: `workloads` is a cache rebuildable from docker/git
// inspection (the runtime is the truth); `projects` is the authoritative
// registry of repos pier knows about — populated by `pier init` and the
// REST API, not derivable from the runtime.
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
    repo_path     TEXT NOT NULL UNIQUE,
    registered_at INTEGER NOT NULL
);
`

// ErrNotFound is returned when a (project, slug) pair has no row.
var ErrNotFound = errors.New("state: workload not found")

// ErrProjectNotFound is returned when no projects row matches the lookup.
var ErrProjectNotFound = errors.New("state: project not found")

// ErrProjectExists is returned when registering a project would clash on
// either `name` or `repo_path` (both UNIQUE). The caller decides whether
// to surface it (POST /projects) or merge silently (CLI re-init).
var ErrProjectExists = errors.New("state: project already registered")

// Project mirrors one row of the projects table.
type Project struct {
	Name         string
	RepoPath     string
	RegisteredAt time.Time
}

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

// RegisterProject inserts a (name, repo_path) row. If the project name
// already exists with the SAME repo_path, this is a no-op (returns the
// existing row). Conflicts on either column with a different mapping
// return ErrProjectExists so the caller can decide how to react.
func (s *Store) RegisterProject(name, repoPath string) (*Project, error) {
	if name == "" || repoPath == "" {
		return nil, errors.New("state: name and repo_path are required")
	}
	if existing, err := s.GetProject(name); err == nil {
		if existing.RepoPath == repoPath {
			return existing, nil
		}
		return nil, fmt.Errorf("%w: name %q already maps to %s", ErrProjectExists, name, existing.RepoPath)
	} else if !errors.Is(err, ErrProjectNotFound) {
		return nil, err
	}
	if existing, err := s.GetProjectByRepo(repoPath); err == nil {
		return nil, fmt.Errorf("%w: repo %s already registered as %q", ErrProjectExists, repoPath, existing.Name)
	} else if !errors.Is(err, ErrProjectNotFound) {
		return nil, err
	}
	now := time.Now().Unix()
	if _, err := s.db.Exec(
		`INSERT INTO projects (name, repo_path, registered_at) VALUES (?, ?, ?)`,
		name, repoPath, now,
	); err != nil {
		return nil, err
	}
	return &Project{Name: name, RepoPath: repoPath, RegisteredAt: time.Unix(now, 0)}, nil
}

// GetProject fetches one project by name. Returns ErrProjectNotFound when
// nothing matches.
func (s *Store) GetProject(name string) (*Project, error) {
	row := s.db.QueryRow(
		`SELECT name, repo_path, registered_at FROM projects WHERE name = ?`,
		name,
	)
	return scanProject(row)
}

// GetProjectByRepo fetches a project by absolute repo path. Same error
// surface as GetProject.
func (s *Store) GetProjectByRepo(repoPath string) (*Project, error) {
	row := s.db.QueryRow(
		`SELECT name, repo_path, registered_at FROM projects WHERE repo_path = ?`,
		repoPath,
	)
	return scanProject(row)
}

// ListProjects returns every registered project, ordered by name.
func (s *Store) ListProjects() ([]*Project, error) {
	rows, err := s.db.Query(
		`SELECT name, repo_path, registered_at FROM projects ORDER BY name`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UnregisterProject deletes the row for `name`. No-op if absent. Does
// NOT cascade to workloads — those keep their `project` text and remain
// listable; the registry just forgets where the repo lives.
func (s *Store) UnregisterProject(name string) error {
	_, err := s.db.Exec(`DELETE FROM projects WHERE name = ?`, name)
	return err
}

func scanProject(r rowScanner) (*Project, error) {
	var (
		p            Project
		registeredAt int64
	)
	if err := r.Scan(&p.Name, &p.RepoPath, &registeredAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrProjectNotFound
		}
		return nil, err
	}
	p.RegisteredAt = time.Unix(registeredAt, 0)
	return &p, nil
}
