package db

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS projects (
    name        TEXT PRIMARY KEY,
    spec_path   TEXT NOT NULL,
    ref         TEXT NOT NULL DEFAULT '@main',
    current_sha TEXT
);

CREATE TABLE IF NOT EXISTS deployments (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    address     TEXT NOT NULL,
    sha         TEXT NOT NULL,
    status      TEXT NOT NULL CHECK(status IN ('building','active','failed','rolled_back')),
    started_at  INTEGER NOT NULL,
    finished_at INTEGER
);

CREATE TABLE IF NOT EXISTS services (
    address         TEXT PRIMARY KEY,
    status          TEXT NOT NULL CHECK(status IN ('starting','running','stopped','degraded')),
    pid             INTEGER,
    restart_count   INTEGER NOT NULL DEFAULT 0,
    last_exit_code  INTEGER,
    last_exit_at    INTEGER
);
`

// DB wraps a SQLite connection with nexus-specific operations.
type DB struct {
	conn *sql.DB
}

// Open opens (or creates) the nexus SQLite database at path and applies the schema.
func Open(path string) (*DB, error) {
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	if _, err := conn.Exec(schema); err != nil {
		conn.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &DB{conn: conn}, nil
}

// Close closes the database connection.
func (d *DB) Close() error { return d.conn.Close() }

// Project is a root-level project tracked by nexus.
type Project struct {
	Name       string
	SpecPath   string
	Ref        string
	CurrentSHA string // empty until first successful deployment
}

// AddProject inserts a new project. Returns an error if the name is already in use.
func (d *DB) AddProject(p Project) error {
	_, err := d.conn.Exec(
		`INSERT INTO projects (name, spec_path, ref) VALUES (?, ?, ?)`,
		p.Name, p.SpecPath, p.Ref,
	)
	if err != nil {
		return fmt.Errorf("add project %q: %w", p.Name, err)
	}
	return nil
}

// RemoveProject deletes a project by name. Returns an error if not found.
func (d *DB) RemoveProject(name string) error {
	res, err := d.conn.Exec(`DELETE FROM projects WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("remove project %q: %w", name, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("project %q not found", name)
	}
	return nil
}

// ListProjects returns all tracked root projects ordered by name.
func (d *DB) ListProjects() ([]Project, error) {
	rows, err := d.conn.Query(
		`SELECT name, spec_path, ref, COALESCE(current_sha, '') FROM projects ORDER BY name`,
	)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()

	var out []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.Name, &p.SpecPath, &p.Ref, &p.CurrentSHA); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// AddDeployment inserts a new deployment record with status "building" and returns its ID.
func (d *DB) AddDeployment(address, sha string, startedAt time.Time) (int64, error) {
	res, err := d.conn.Exec(
		`INSERT INTO deployments (address, sha, status, started_at) VALUES (?, ?, 'building', ?)`,
		address, sha, startedAt.Unix(),
	)
	if err != nil {
		return 0, fmt.Errorf("add deployment: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("last insert id: %w", err)
	}
	return id, nil
}

// FinishDeployment updates the status and finished_at of a deployment record.
// status must be one of: active, failed, rolled_back.
func (d *DB) FinishDeployment(id int64, status string, finishedAt time.Time) error {
	_, err := d.conn.Exec(
		`UPDATE deployments SET status = ?, finished_at = ? WHERE id = ?`,
		status, finishedAt.Unix(), id,
	)
	if err != nil {
		return fmt.Errorf("finish deployment %d: %w", id, err)
	}
	return nil
}

// SetCurrentSHA records the active SHA for a project after a successful deployment.
func (d *DB) SetCurrentSHA(name, sha string) error {
	res, err := d.conn.Exec(`UPDATE projects SET current_sha = ? WHERE name = ?`, sha, name)
	if err != nil {
		return fmt.Errorf("set sha for %q: %w", name, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("project %q not found", name)
	}
	return nil
}
