package daemon_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rdkal/nexus/internal/config"
	"github.com/rdkal/nexus/internal/daemon"
	"github.com/rdkal/nexus/internal/db"
	"github.com/rdkal/nexus/internal/home"
	"github.com/rdkal/nexus/internal/supervisor"
)

// --- test helpers ---

func openDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "nexus.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func testPaths(t *testing.T) home.Paths {
	t.Helper()
	return home.NewPaths(t.TempDir())
}

// stubSupervisor returns preset statuses and records Spawn/Stop calls.
type stubSupervisor struct {
	mu       sync.Mutex
	statuses map[string]supervisor.Status
	spawned  []string
	stopped  []string
}

func (s *stubSupervisor) Spawn(name string, spec supervisor.ServiceSpec) {
	s.mu.Lock()
	s.spawned = append(s.spawned, name)
	s.mu.Unlock()
}
func (s *stubSupervisor) Stop(name string) {
	s.mu.Lock()
	s.stopped = append(s.stopped, name)
	s.mu.Unlock()
}
func (s *stubSupervisor) Status(name string) (supervisor.Status, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.statuses[name]
	return st, ok
}

// newTestDaemon creates a Daemon wired to a temp DB and stub supervisor.
// The caller can pre-populate projects in the DB and/or d.ExportProjects().
func newTestDaemon(t *testing.T, sup *stubSupervisor) *daemon.Daemon {
	t.Helper()
	return daemon.New(openDB(t), sup, testPaths(t))
}

// --- tests ---

func TestHandleListProjects_Empty(t *testing.T) {
	d := newTestDaemon(t, &stubSupervisor{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/projects", nil)
	d.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var out []map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("expected empty list, got %d items", len(out))
	}
}

func TestHandleListProjects_WithProject(t *testing.T) {
	database := openDB(t)
	sup := &stubSupervisor{
		statuses: map[string]supervisor.Status{
			"my-system/api": {Running: true},
		},
	}
	d := daemon.New(database, sup, testPaths(t))

	// Add a project to the DB.
	must(t, database.AddProject(db.Project{
		Name: "my-system", SpecPath: "github.com/myorg/my-system", Ref: "@main",
	}))
	must(t, database.SetCurrentSHA("my-system", "abc123"))

	// Inject a projectState so the handler can find the cfg.
	cfg := &config.ProjectFile{
		Services: map[string]config.Service{"api": {Run: "python api.py"}},
	}
	d.InjectProject("my-system", cfg, "abc123")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/projects", nil)
	d.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var out []map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 project, got %d", len(out))
	}
	if out[0]["name"] != "my-system" {
		t.Errorf("name = %v", out[0]["name"])
	}
	if out[0]["health"] != "healthy" {
		t.Errorf("health = %v", out[0]["health"])
	}
}

func TestHandleListProjects_IncludesNestedAddresses(t *testing.T) {
	sup := &stubSupervisor{
		statuses: map[string]supervisor.Status{
			"root/db/store": {Running: true},
		},
	}
	d := newTestDaemon(t, sup)

	// A root project and a discovered external sub-project addressed "root/db".
	d.InjectProject("root", &config.ProjectFile{}, "rootsha")
	d.InjectProject("root/db", &config.ProjectFile{
		Services: map[string]config.Service{"store": {Run: "sleep 1"}},
	}, "dbsha")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/projects", nil)
	d.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var out []map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 projects (root + root/db), got %d: %v", len(out), out)
	}
	// Sorted by address: "root" then "root/db".
	if out[0]["name"] != "root" || out[1]["name"] != "root/db" {
		t.Errorf("unexpected addresses/order: %v, %v", out[0]["name"], out[1]["name"])
	}
	if out[1]["health"] != "healthy" {
		t.Errorf("sub-project health = %v, want healthy", out[1]["health"])
	}
}

func TestHandleGetProject_NotFound(t *testing.T) {
	d := newTestDaemon(t, &stubSupervisor{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/projects/ghost", nil)
	d.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestHandleGetHistory(t *testing.T) {
	database := openDB(t)
	d := daemon.New(database, &stubSupervisor{}, testPaths(t))

	must(t, database.AddProject(db.Project{Name: "api", SpecPath: "github.com/myorg/api", Ref: "@main"}))
	id, err := database.AddDeployment("api", "abc123", time.Now())
	must(t, err)
	must(t, database.FinishDeployment(id, "active", time.Now()))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/projects/api/history", nil)
	d.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var out []map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 deployment, got %d", len(out))
	}
	if out[0]["sha"] != "abc123" {
		t.Errorf("sha = %v", out[0]["sha"])
	}
	if out[0]["status"] != "active" {
		t.Errorf("status = %v", out[0]["status"])
	}
}

func TestHandleListServices(t *testing.T) {
	sup := &stubSupervisor{
		statuses: map[string]supervisor.Status{
			"my-system/api":    {Running: true},
			"my-system/worker": {Running: false, Restarts: 3},
		},
	}
	d := newTestDaemon(t, sup)

	cfg := &config.ProjectFile{
		Services: map[string]config.Service{
			"api":    {Run: "python api.py"},
			"worker": {Run: "python worker.py"},
		},
	}
	d.InjectProject("my-system", cfg, "abc123")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/projects/my-system/services", nil)
	d.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var out []map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 services, got %d", len(out))
	}
}

func TestHandleRedeploy_NotDeployed(t *testing.T) {
	d := newTestDaemon(t, &stubSupervisor{})
	d.InjectProject("my-system", &config.ProjectFile{}, "")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/projects/my-system/redeploy", nil)
	d.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Errorf("expected 409, got %d", rec.Code)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHandleGetBuildLog(t *testing.T) {
	d := newTestDaemon(t, &stubSupervisor{})

	path := d.Paths.BuildLog("app", "abc123")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("BUILD_OUTPUT_LINE\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	d.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/projects/app/builds/abc123/log", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "BUILD_OUTPUT_LINE") {
		t.Errorf("body = %q", rec.Body.String())
	}

	// A nested address build log also routes correctly.
	np := d.Paths.BuildLog("root/db", "deadbeef")
	_ = os.MkdirAll(filepath.Dir(np), 0o755)
	_ = os.WriteFile(np, []byte("NESTED_BUILD\n"), 0o644)
	rec = httptest.NewRecorder()
	d.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/projects/root/db/builds/deadbeef/log", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "NESTED_BUILD") {
		t.Errorf("nested build log: code=%d body=%q", rec.Code, rec.Body.String())
	}

	// Missing build log → 404.
	rec = httptest.NewRecorder()
	d.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/projects/app/builds/nosha/log", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing build log should 404, got %d", rec.Code)
	}
}
