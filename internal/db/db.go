package db

import (
	"database/sql"
	"fmt"
	"net/url"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS projects (
    name        TEXT PRIMARY KEY,
    spec_path   TEXT NOT NULL,
    ref         TEXT NOT NULL DEFAULT '@main',
    current_sha TEXT,
    subdir      TEXT NOT NULL DEFAULT ''
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
//
// The connection is tuned for the daemon's access pattern — several project
// deploy loops writing concurrently, plus brief overlap between an old and new
// nexus process during a self-update restart:
//
//   - WAL journal mode: readers never block the single writer.
//   - busy_timeout: a writer waits (instead of erroring SQLITE_BUSY) when the
//     database is momentarily locked by another connection or process.
//   - MaxOpenConns(1): serialise writes within this process, so the deploy loops
//     never contend with each other on a write lock.
func Open(path string) (*DB, error) {
	dsn := "file:" + path + "?" + url.Values{
		"_pragma": {"busy_timeout(10000)", "journal_mode(WAL)", "foreign_keys(1)"},
	}.Encode()

	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	// One connection serialises all access within the process; busy_timeout above
	// covers contention with any other process (e.g. an overlapping restart).
	conn.SetMaxOpenConns(1)

	if _, err := conn.Exec(schema); err != nil {
		conn.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if err := migrate(conn); err != nil {
		conn.Close()
		return nil, err
	}
	return &DB{conn: conn}, nil
}

// migrate applies additive schema changes to databases created by older versions.
// Each step is idempotent — a duplicate-column error means the column already
// exists (a fresh DB created it via the schema above), which is not an error.
func migrate(conn *sql.DB) error {
	steps := []string{
		`ALTER TABLE projects ADD COLUMN subdir TEXT NOT NULL DEFAULT ''`,
	}
	for _, stmt := range steps {
		if _, err := conn.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			return fmt.Errorf("migrate %q: %w", stmt, err)
		}
	}
	return nil
}

// Close closes the database connection.
func (d *DB) Close() error { return d.conn.Close() }

// Project is a root-level project tracked by nexus.
type Project struct {
	Name       string
	SpecPath   string // git repo root (cloneable); the part before any subdir
	Ref        string
	CurrentSHA string // empty until first successful deployment
	Subdir     string // in-repo path to the app's nexus.yaml ("" = repo root)
}

// AddProject inserts a new project. Returns an error if the name is already in use.
func (d *DB) AddProject(p Project) error {
	_, err := d.conn.Exec(
		`INSERT INTO projects (name, spec_path, ref, subdir) VALUES (?, ?, ?, ?)`,
		p.Name, p.SpecPath, p.Ref, p.Subdir,
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
		`SELECT name, spec_path, ref, COALESCE(current_sha, ''), subdir FROM projects ORDER BY name`,
	)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()

	var out []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.Name, &p.SpecPath, &p.Ref, &p.CurrentSHA, &p.Subdir); err != nil {
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

// GetProject returns a single project by name.
func (d *DB) GetProject(name string) (Project, error) {
	row := d.conn.QueryRow(
		`SELECT name, spec_path, ref, COALESCE(current_sha, ''), subdir FROM projects WHERE name = ?`, name,
	)
	var p Project
	if err := row.Scan(&p.Name, &p.SpecPath, &p.Ref, &p.CurrentSHA, &p.Subdir); err != nil {
		return Project{}, fmt.Errorf("get project %q: %w", name, err)
	}
	return p, nil
}

// Deployment is a record of one deploy attempt for a project.
type Deployment struct {
	ID         int64
	Address    string
	SHA        string
	Status     string
	StartedAt  time.Time
	FinishedAt *time.Time
}

// ListDeployments returns up to limit deployments for address, newest first.
func (d *DB) ListDeployments(address string, limit int) ([]Deployment, error) {
	rows, err := d.conn.Query(
		`SELECT id, address, sha, status, started_at, finished_at
		 FROM deployments WHERE address = ? ORDER BY started_at DESC LIMIT ?`,
		address, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list deployments: %w", err)
	}
	defer rows.Close()

	var out []Deployment
	for rows.Next() {
		var dep Deployment
		var startedUnix int64
		var finishedUnix *int64
		if err := rows.Scan(&dep.ID, &dep.Address, &dep.SHA, &dep.Status, &startedUnix, &finishedUnix); err != nil {
			return nil, err
		}
		dep.StartedAt = time.Unix(startedUnix, 0)
		if finishedUnix != nil {
			t := time.Unix(*finishedUnix, 0)
			dep.FinishedAt = &t
		}
		out = append(out, dep)
	}
	return out, rows.Err()
}

// SetCurrentSHA records the active SHA for a root project after a successful
// deployment. Nested (external sub-)projects have no row in the projects table —
// they are discovered from their parent's config, not tracked independently — so
// a no-match is not an error; their active SHA is derived from the deployments
// table via CurrentSHA instead.
func (d *DB) SetCurrentSHA(name, sha string) error {
	_, err := d.conn.Exec(`UPDATE projects SET current_sha = ? WHERE name = ?`, sha, name)
	if err != nil {
		return fmt.Errorf("set sha for %q: %w", name, err)
	}
	return nil
}

// CurrentSHA returns the SHA of the most recent active deployment for an address,
// or "" if the address has never had a successful deployment. This is the source
// of truth for a nested project's deployed SHA on recovery, since such projects
// are not stored in the projects table.
func (d *DB) CurrentSHA(address string) (string, error) {
	var sha string
	err := d.conn.QueryRow(
		`SELECT sha FROM deployments WHERE address = ? AND status = 'active'
		 ORDER BY started_at DESC, id DESC LIMIT 1`, address,
	).Scan(&sha)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("current sha for %q: %w", address, err)
	}
	return sha, nil
}
