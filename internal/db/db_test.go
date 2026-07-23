package db_test

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/rdkal/nexus/internal/db"
)

func openDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "nexus.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestAddAndListProjects(t *testing.T) {
	d := openDB(t)

	projects := []db.Project{
		{Name: "my-system", SpecPath: "github.com/myorg/my-system", Ref: "@main"},
		{Name: "postgres", SpecPath: "github.com/nexus-community/postgres", Ref: "@v15"},
	}
	for _, p := range projects {
		if err := d.AddProject(p); err != nil {
			t.Fatalf("AddProject(%q): %v", p.Name, err)
		}
	}

	list, err := d.ListProjects()
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("got %d projects, want 2", len(list))
	}
	if list[0].Name != "my-system" || list[1].Name != "postgres" {
		t.Errorf("unexpected order: %v", list)
	}
}

func TestDuplicateProjectName(t *testing.T) {
	d := openDB(t)
	p := db.Project{Name: "api", SpecPath: "github.com/myorg/api", Ref: "@main"}
	if err := d.AddProject(p); err != nil {
		t.Fatalf("first AddProject: %v", err)
	}
	if err := d.AddProject(p); err == nil {
		t.Error("expected error for duplicate project name")
	}
}

func TestRemoveProject(t *testing.T) {
	d := openDB(t)
	p := db.Project{Name: "api", SpecPath: "github.com/myorg/api", Ref: "@main"}
	if err := d.AddProject(p); err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	if err := d.RemoveProject("api"); err != nil {
		t.Fatalf("RemoveProject: %v", err)
	}
	list, _ := d.ListProjects()
	if len(list) != 0 {
		t.Errorf("expected 0 projects after remove, got %d", len(list))
	}
}

func TestRemoveNonexistent(t *testing.T) {
	d := openDB(t)
	if err := d.RemoveProject("ghost"); err == nil {
		t.Error("expected error removing nonexistent project")
	}
}

func TestSetCurrentSHA(t *testing.T) {
	d := openDB(t)
	p := db.Project{Name: "api", SpecPath: "github.com/myorg/api", Ref: "@main"}
	if err := d.AddProject(p); err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	if err := d.SetCurrentSHA("api", "deadbeef"); err != nil {
		t.Fatalf("SetCurrentSHA: %v", err)
	}
	list, _ := d.ListProjects()
	if list[0].CurrentSHA != "deadbeef" {
		t.Errorf("CurrentSHA = %q, want %q", list[0].CurrentSHA, "deadbeef")
	}
}

func TestSetCurrentSHA_NoRootRowIsNoop(t *testing.T) {
	d := openDB(t)
	// A nested project has no projects-table row; SetCurrentSHA must not error.
	if err := d.SetCurrentSHA("my-system/db", "abc123"); err != nil {
		t.Errorf("SetCurrentSHA for non-root address should be a no-op, got: %v", err)
	}
}

func TestCurrentSHA_FromDeployments(t *testing.T) {
	d := openDB(t)

	// No deployments yet → empty.
	sha, err := d.CurrentSHA("my-system/db")
	if err != nil {
		t.Fatalf("CurrentSHA: %v", err)
	}
	if sha != "" {
		t.Errorf("expected empty sha, got %q", sha)
	}

	finish := func(id int64, status string, at int64) {
		t.Helper()
		if err := d.FinishDeployment(id, status, time.Unix(at, 0)); err != nil {
			t.Fatalf("FinishDeployment(%d, %s): %v", id, status, err)
		}
	}

	// A failed deployment must not count as current.
	id1, _ := d.AddDeployment("my-system/db", "bad111", time.Unix(100, 0))
	finish(id1, "failed", 101)

	// An active deployment becomes current.
	id2, _ := d.AddDeployment("my-system/db", "good222", time.Unix(200, 0))
	finish(id2, "active", 201)

	sha, err = d.CurrentSHA("my-system/db")
	if err != nil {
		t.Fatalf("CurrentSHA: %v", err)
	}
	if sha != "good222" {
		t.Errorf("CurrentSHA = %q, want good222", sha)
	}

	// A newer active deployment supersedes the previous one.
	id3, _ := d.AddDeployment("my-system/db", "good333", time.Unix(300, 0))
	finish(id3, "active", 301)

	sha, _ = d.CurrentSHA("my-system/db")
	if sha != "good333" {
		t.Errorf("CurrentSHA = %q, want good333 (newest active)", sha)
	}
}

func TestPersistenceAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nexus.db")

	d1, err := db.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := d1.AddProject(db.Project{Name: "api", SpecPath: "github.com/myorg/api", Ref: "@main"}); err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	d1.Close()

	d2, err := db.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer d2.Close()
	list, err := d2.ListProjects()
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(list) != 1 || list[0].Name != "api" {
		t.Errorf("expected project 'api' to persist, got %v", list)
	}
}

func TestAddAndFinishDeployment(t *testing.T) {
	d := openDB(t)

	id, err := d.AddDeployment("my-system", "abc123", time.Now())
	if err != nil {
		t.Fatalf("AddDeployment: %v", err)
	}
	if id <= 0 {
		t.Errorf("expected positive id, got %d", id)
	}

	if err := d.FinishDeployment(id, "active", time.Now()); err != nil {
		t.Fatalf("FinishDeployment: %v", err)
	}

	// A second deployment for same address can be added independently.
	id2, err := d.AddDeployment("my-system", "def456", time.Now())
	if err != nil {
		t.Fatalf("second AddDeployment: %v", err)
	}
	if id2 <= id {
		t.Errorf("expected auto-increment id > %d, got %d", id, id2)
	}
	if err := d.FinishDeployment(id2, "rolled_back", time.Now()); err != nil {
		t.Fatalf("FinishDeployment rolled_back: %v", err)
	}
}

// TestMigrate_AddsSubdirColumn verifies a database created before the subdir
// column upgrades cleanly on Open, defaulting existing rows to "".
func TestMigrate_AddsSubdirColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")

	// Simulate a pre-subdir database.
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	_, err = raw.Exec(`
		CREATE TABLE projects (
		    name TEXT PRIMARY KEY, spec_path TEXT NOT NULL,
		    ref TEXT NOT NULL DEFAULT '@main', current_sha TEXT
		);
		INSERT INTO projects (name, spec_path, ref) VALUES ('api', 'github.com/x/y', '@main');
	`)
	if err != nil {
		t.Fatalf("seed old schema: %v", err)
	}
	raw.Close()

	// Open through db.Open, which runs the migration.
	d, err := db.Open(path)
	if err != nil {
		t.Fatalf("Open (migrate): %v", err)
	}
	defer d.Close()

	got, err := d.GetProject("api")
	if err != nil {
		t.Fatalf("GetProject after migrate: %v", err)
	}
	if got.Subdir != "" {
		t.Errorf("migrated Subdir = %q, want empty", got.Subdir)
	}

	// The new column is usable.
	if err := d.AddProject(db.Project{
		Name: "api2", SpecPath: "github.com/x/y", Ref: "@main", Subdir: "services/api",
	}); err != nil {
		t.Fatalf("AddProject with subdir: %v", err)
	}
	got2, _ := d.GetProject("api2")
	if got2.Subdir != "services/api" {
		t.Errorf("Subdir = %q, want services/api", got2.Subdir)
	}
}

func TestSetStopped(t *testing.T) {
	d := openDB(t)
	p := db.Project{Name: "app", SpecPath: "github.com/x/app", Ref: "main"}
	if err := d.AddProject(p); err != nil {
		t.Fatalf("AddProject: %v", err)
	}

	// Fresh projects are not stopped.
	got, err := d.GetProject("app")
	if err != nil {
		t.Fatal(err)
	}
	if got.Stopped {
		t.Error("new project should not be stopped")
	}

	if err := d.SetStopped("app", true); err != nil {
		t.Fatalf("SetStopped(true): %v", err)
	}
	got, _ = d.GetProject("app")
	if !got.Stopped {
		t.Error("project should be stopped after SetStopped(true)")
	}
	// ListProjects reflects it too.
	list, _ := d.ListProjects()
	if len(list) != 1 || !list[0].Stopped {
		t.Errorf("ListProjects stopped flag not set: %+v", list)
	}

	if err := d.SetStopped("app", false); err != nil {
		t.Fatalf("SetStopped(false): %v", err)
	}
	got, _ = d.GetProject("app")
	if got.Stopped {
		t.Error("project should be resumed after SetStopped(false)")
	}
}

func TestSetStoppedNonexistent(t *testing.T) {
	d := openDB(t)
	if err := d.SetStopped("nope", true); err == nil {
		t.Error("expected error for unknown project")
	}
}
