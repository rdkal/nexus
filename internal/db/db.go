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
    subdir      TEXT NOT NULL DEFAULT '',
    stopped     INTEGER NOT NULL DEFAULT 0
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

-- Pause state for sub-projects (nested, not root — roots use projects.stopped)
-- and for individual services. Keyed by resource address; presence means paused.
-- Kept as separate tables (rather than reusing one keyspace) because a
-- sub-project's address and a leaf service's full key share the same
-- slash-joined shape and could otherwise collide.
CREATE TABLE IF NOT EXISTS stopped_projects (
    address TEXT PRIMARY KEY
);

CREATE TABLE IF NOT EXISTS stopped_services (
    address TEXT PRIMARY KEY
);

-- One row per task fire. status starts 'running' and is finalised on exit.
CREATE TABLE IF NOT EXISTS task_runs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    address     TEXT NOT NULL,   -- project address the task belongs to
    task        TEXT NOT NULL,
    reason      TEXT NOT NULL,   -- 'schedule' | 'after:<task>' | 'manual'
    status      TEXT NOT NULL CHECK(status IN ('running','success','failed')),
    exit_code   INTEGER,
    started_at  INTEGER NOT NULL,
    finished_at INTEGER
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
		`ALTER TABLE projects ADD COLUMN stopped INTEGER NOT NULL DEFAULT 0`,
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
	Stopped    bool   // paused for maintenance: kept tracked, but not deployed/running
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
		`SELECT name, spec_path, ref, COALESCE(current_sha, ''), subdir, stopped FROM projects ORDER BY name`,
	)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()

	var out []Project
	for rows.Next() {
		var p Project
		var stopped int
		if err := rows.Scan(&p.Name, &p.SpecPath, &p.Ref, &p.CurrentSHA, &p.Subdir, &stopped); err != nil {
			return nil, err
		}
		p.Stopped = stopped != 0
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

// TaskRun is one execution of a task.
type TaskRun struct {
	ID         int64
	Address    string
	Task       string
	Reason     string
	Status     string // running | success | failed
	ExitCode   *int
	StartedAt  time.Time
	FinishedAt *time.Time
}

// AddTaskRun records the start of a task run (status 'running') and returns its ID.
func (d *DB) AddTaskRun(address, task, reason string, startedAt time.Time) (int64, error) {
	res, err := d.conn.Exec(
		`INSERT INTO task_runs (address, task, reason, status, started_at) VALUES (?, ?, ?, 'running', ?)`,
		address, task, reason, startedAt.Unix(),
	)
	if err != nil {
		return 0, fmt.Errorf("add task run: %w", err)
	}
	return res.LastInsertId()
}

// FinishTaskRun finalises a task run with its outcome and exit code.
func (d *DB) FinishTaskRun(id int64, status string, exitCode int, finishedAt time.Time) error {
	_, err := d.conn.Exec(
		`UPDATE task_runs SET status = ?, exit_code = ?, finished_at = ? WHERE id = ?`,
		status, exitCode, finishedAt.Unix(), id,
	)
	if err != nil {
		return fmt.Errorf("finish task run %d: %w", id, err)
	}
	return nil
}

// HasRunningTaskRun reports whether a task currently has a run in progress —
// used to enforce no self-overlap (survives restarts, unlike an in-memory flag).
func (d *DB) HasRunningTaskRun(address, task string) (bool, error) {
	var n int
	err := d.conn.QueryRow(
		`SELECT COUNT(*) FROM task_runs WHERE address = ? AND task = ? AND status = 'running'`,
		address, task,
	).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("has running task run: %w", err)
	}
	return n > 0, nil
}

// LastTaskRunID returns the id of a task's most recent run (any status), or 0 if
// it has never run. Used as the watermark for a fan-in join: a parent's success
// only counts toward the next join fire if it is newer than this.
func (d *DB) LastTaskRunID(address, task string) (int64, error) {
	var id sql.NullInt64
	err := d.conn.QueryRow(
		`SELECT MAX(id) FROM task_runs WHERE address = ? AND task = ?`, address, task,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("last task run id: %w", err)
	}
	if !id.Valid {
		return 0, nil
	}
	return id.Int64, nil
}

// HasSuccessfulTaskRunAfter reports whether a task has a successful run with an id
// greater than afterID — i.e. a "fresh" success not yet consumed by a join that
// last ran at afterID.
func (d *DB) HasSuccessfulTaskRunAfter(address, task string, afterID int64) (bool, error) {
	var n int
	err := d.conn.QueryRow(
		`SELECT COUNT(*) FROM task_runs WHERE address = ? AND task = ? AND status = 'success' AND id > ?`,
		address, task, afterID,
	).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("has successful task run after: %w", err)
	}
	return n > 0, nil
}

// RunningTaskRuns returns every task run still marked 'running' — the runtime
// polls nexus-pm for each on startup to finalise runs it kicked off before a restart.
func (d *DB) RunningTaskRuns() ([]TaskRun, error) {
	rows, err := d.conn.Query(
		`SELECT id, address, task, reason, status, exit_code, started_at, finished_at
		 FROM task_runs WHERE status = 'running' ORDER BY id`,
	)
	if err != nil {
		return nil, fmt.Errorf("running task runs: %w", err)
	}
	defer rows.Close()
	return scanTaskRuns(rows)
}

// ListTaskRuns returns up to limit runs for a project, newest first.
func (d *DB) ListTaskRuns(address string, limit int) ([]TaskRun, error) {
	rows, err := d.conn.Query(
		`SELECT id, address, task, reason, status, exit_code, started_at, finished_at
		 FROM task_runs WHERE address = ? ORDER BY started_at DESC, id DESC LIMIT ?`,
		address, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list task runs: %w", err)
	}
	defer rows.Close()
	return scanTaskRuns(rows)
}

func scanTaskRuns(rows *sql.Rows) ([]TaskRun, error) {
	var out []TaskRun
	for rows.Next() {
		var r TaskRun
		var exit *int64
		var started int64
		var finished *int64
		if err := rows.Scan(&r.ID, &r.Address, &r.Task, &r.Reason, &r.Status, &exit, &started, &finished); err != nil {
			return nil, err
		}
		r.StartedAt = time.Unix(started, 0)
		if exit != nil {
			e := int(*exit)
			r.ExitCode = &e
		}
		if finished != nil {
			t := time.Unix(*finished, 0)
			r.FinishedAt = &t
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetProject returns a single project by name.
func (d *DB) GetProject(name string) (Project, error) {
	row := d.conn.QueryRow(
		`SELECT name, spec_path, ref, COALESCE(current_sha, ''), subdir, stopped FROM projects WHERE name = ?`, name,
	)
	var p Project
	var stopped int
	if err := row.Scan(&p.Name, &p.SpecPath, &p.Ref, &p.CurrentSHA, &p.Subdir, &stopped); err != nil {
		return Project{}, fmt.Errorf("get project %q: %w", name, err)
	}
	p.Stopped = stopped != 0
	return p, nil
}

// SetStopped marks a project paused (stopped=true) or resumed (stopped=false).
// The project's row and current SHA are untouched, so a paused project stays down
// across daemon restarts until resumed, then recovers from its last SHA.
func (d *DB) SetStopped(name string, stopped bool) error {
	res, err := d.conn.Exec(`UPDATE projects SET stopped = ? WHERE name = ?`, boolToInt(stopped), name)
	if err != nil {
		return fmt.Errorf("set stopped for %q: %w", name, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("project %q not found", name)
	}
	return nil
}

// SetProjectPaused pauses (or resumes) a nested sub-project by address. Unlike
// SetStopped, this table has no per-project row of its own — a sub-project is
// discovered from its parent's config, not tracked independently — so pausing
// is purely "is this address in the set", with no SHA or ref to preserve.
func (d *DB) SetProjectPaused(address string, paused bool) error {
	if paused {
		_, err := d.conn.Exec(`INSERT OR IGNORE INTO stopped_projects (address) VALUES (?)`, address)
		if err != nil {
			return fmt.Errorf("pause sub-project %q: %w", address, err)
		}
		return nil
	}
	_, err := d.conn.Exec(`DELETE FROM stopped_projects WHERE address = ?`, address)
	if err != nil {
		return fmt.Errorf("resume sub-project %q: %w", address, err)
	}
	return nil
}

// PausedProjects returns the set of currently-paused sub-project addresses.
func (d *DB) PausedProjects() (map[string]bool, error) {
	return d.addressSet(`SELECT address FROM stopped_projects`)
}

// SetServicePaused pauses (or resumes) an individual service by its full
// supervisor key (address/service). The service's spec is not stored here —
// it is recovered from the owning project's cached svcSpecs — so pausing is
// purely "is this key in the set".
func (d *DB) SetServicePaused(key string, paused bool) error {
	if paused {
		_, err := d.conn.Exec(`INSERT OR IGNORE INTO stopped_services (address) VALUES (?)`, key)
		if err != nil {
			return fmt.Errorf("pause service %q: %w", key, err)
		}
		return nil
	}
	_, err := d.conn.Exec(`DELETE FROM stopped_services WHERE address = ?`, key)
	if err != nil {
		return fmt.Errorf("resume service %q: %w", key, err)
	}
	return nil
}

// PausedServices returns the set of currently-paused service keys.
func (d *DB) PausedServices() (map[string]bool, error) {
	return d.addressSet(`SELECT address FROM stopped_services`)
}

func (d *DB) addressSet(query string) (map[string]bool, error) {
	rows, err := d.conn.Query(query)
	if err != nil {
		return nil, fmt.Errorf("query address set: %w", err)
	}
	defer rows.Close()

	out := make(map[string]bool)
	for rows.Next() {
		var addr string
		if err := rows.Scan(&addr); err != nil {
			return nil, err
		}
		out[addr] = true
	}
	return out, rows.Err()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
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
