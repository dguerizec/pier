package state

import (
	"database/sql"
	"errors"
	"time"
)

// ErrProjectNotFound is returned when a project name has no row.
var ErrProjectNotFound = errors.New("state: project not found")

// Project mirrors one row in the projects table. Unlike Workload, a
// Project survives `pier down`: it represents "pier knows about this
// repo", not "this repo is currently up". Populated by `pier init` and
// refreshed by `pier up` so projects that predate the registry get
// adopted on first use.
type Project struct {
	Name         string
	Path         string // primary worktree where init ran
	BaseDomain   string
	StackFile    string // relative to Path
	StackService string
	LastSeen     time.Time
}

// UpsertProject inserts or refreshes a project row. LastSeen defaults
// to now() when zero, so callers can pass a bare struct.
func (s *Store) UpsertProject(p *Project) error {
	if p.Name == "" || p.Path == "" {
		return errors.New("state: project name and path are required")
	}
	if p.LastSeen.IsZero() {
		p.LastSeen = time.Now()
	}
	_, err := s.db.Exec(`
		INSERT INTO projects
			(name, path, base_domain, stack_file, stack_service, last_seen)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			path          = excluded.path,
			base_domain   = excluded.base_domain,
			stack_file    = excluded.stack_file,
			stack_service = excluded.stack_service,
			last_seen     = excluded.last_seen
	`,
		p.Name, p.Path, p.BaseDomain, p.StackFile,
		nullableString(p.StackService), p.LastSeen.Unix(),
	)
	return err
}

// GetProject fetches one project. Returns ErrProjectNotFound if absent.
func (s *Store) GetProject(name string) (*Project, error) {
	row := s.db.QueryRow(`
		SELECT name, path, base_domain, stack_file, stack_service, last_seen
		FROM projects
		WHERE name = ?
	`, name)
	p, err := scanProject(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrProjectNotFound
	}
	return p, err
}

// ListProjects returns every project, ordered by name.
func (s *Store) ListProjects() ([]*Project, error) {
	rows, err := s.db.Query(`
		SELECT name, path, base_domain, stack_file, stack_service, last_seen
		FROM projects
		ORDER BY name
	`)
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

// DeleteProject removes the project row. No-op if absent. Does not
// touch the workloads table — orphan workloads (e.g. from a removed
// project) remain queryable.
func (s *Store) DeleteProject(name string) error {
	_, err := s.db.Exec(`DELETE FROM projects WHERE name = ?`, name)
	return err
}

func scanProject(r rowScanner) (*Project, error) {
	var (
		p        Project
		service  sql.NullString
		lastSeen int64
	)
	if err := r.Scan(
		&p.Name, &p.Path, &p.BaseDomain, &p.StackFile, &service, &lastSeen,
	); err != nil {
		return nil, err
	}
	if service.Valid {
		p.StackService = service.String
	}
	p.LastSeen = time.Unix(lastSeen, 0)
	return &p, nil
}
