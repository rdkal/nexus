package db_test

import (
	"path/filepath"
	"testing"

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
