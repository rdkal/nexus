package lifecycle_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/rdkal/nexus/internal/config"
	"github.com/rdkal/nexus/internal/db"
	"github.com/rdkal/nexus/internal/home"
	"github.com/rdkal/nexus/internal/lifecycle"
	"github.com/rdkal/nexus/internal/supervisor"
)

// --- helpers ---

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

// mockSupervisor records all Spawn and Stop calls and serves preset statuses.
type mockSupervisor struct {
	mu       sync.Mutex
	spawned  []spawnedSvc
	stopped  []string
	statuses map[string]supervisor.Status
}

type spawnedSvc struct {
	name string
	spec supervisor.ServiceSpec
}

func (m *mockSupervisor) Spawn(name string, spec supervisor.ServiceSpec) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.spawned = append(m.spawned, spawnedSvc{name, spec})
}

func (m *mockSupervisor) Stop(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopped = append(m.stopped, name)
}

func (m *mockSupervisor) Status(name string) (supervisor.Status, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.statuses == nil {
		return supervisor.Status{Running: true}, true
	}
	st, ok := m.statuses[name]
	return st, ok
}

func (m *mockSupervisor) spawnNames() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var names []string
	for _, s := range m.spawned {
		names = append(names, s.name)
	}
	return names
}

// noopGit returns no-op git operations.
func noopGit() (fetch, add, remove func(...string) error) {
	noop := func(args ...string) error { return nil }
	return noop, noop, noop
}

// staticConfig returns a LoadConfig that always returns cfg.
func staticConfig(cfg *config.ProjectFile) func(string) (*config.ProjectFile, error) {
	return func(_ string) (*config.ProjectFile, error) { return cfg, nil }
}

// --- deployer constructor ---

func newDeployer(
	t *testing.T,
	sup lifecycle.Supervisor,
	cfg *config.ProjectFile,
	buildErr error,
) *lifecycle.Deployer {
	t.Helper()
	d := &lifecycle.Deployer{
		DB:    openDB(t),
		Sup:   sup,
		Paths: testPaths(t),

		Fetch:          func(_ string) error { return nil },
		WorktreeAdd:    func(_, _, _ string) error { return nil },
		WorktreeRemove: func(_, _ string) error { return nil },
		LoadConfig:     staticConfig(cfg),
		RunBuild: func(_, _ string, _ []string, _ string) error {
			return buildErr
		},

		VerifyWindow:    50 * time.Millisecond,
		VerifyTickEvery: 5 * time.Millisecond,
	}
	// Seed the project in DB so SetCurrentSHA works.
	_ = d.DB.(*db.DB) // assert it implements the interface via concrete type
	return d
}

// --- tests ---

func TestDeploy_Success(t *testing.T) {
	cfg := &config.ProjectFile{
		Services: map[string]config.Service{
			"api":    {Run: "python api.py"},
			"worker": {Run: "python worker.py"},
		},
	}
	sup := &mockSupervisor{
		statuses: map[string]supervisor.Status{
			"my-system/api":    {Running: true},
			"my-system/worker": {Running: true},
		},
	}

	dep := newDeployer(t, sup, cfg, nil)

	// Seed the project so SetCurrentSHA finds it.
	must(t, dep.DB.(*db.DB).AddProject(db.Project{
		Name: "my-system", SpecPath: "github.com/myorg/my-system", Ref: "@main",
	}))

	req := lifecycle.Request{
		Name:         "my-system",
		Address:      "my-system",
		Ref:          "@main",
		SpecPath:     "github.com/myorg/my-system",
		RootSpecPath: "github.com/myorg/my-system",
		NewSHA:       "abc123",
	}

	if err := dep.Deploy(context.Background(), req); err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	// Both services should have been spawned.
	names := sup.spawnNames()
	if len(names) != 2 {
		t.Errorf("expected 2 spawns, got %d: %v", len(names), names)
	}

	// No services should have been stopped (first deployment, PrevConfig nil).
	sup.mu.Lock()
	if len(sup.stopped) != 0 {
		t.Errorf("expected 0 stops, got %d", len(sup.stopped))
	}
	sup.mu.Unlock()

	// SHA should be recorded in DB.
	projects, _ := dep.DB.(*db.DB).ListProjects()
	if len(projects) == 0 || projects[0].CurrentSHA != "abc123" {
		t.Errorf("CurrentSHA not recorded; projects = %v", projects)
	}
}

func TestDeploy_BuildFailure(t *testing.T) {
	cfg := &config.ProjectFile{
		Build:    "make build",
		Services: map[string]config.Service{"api": {Run: "python api.py"}},
	}
	sup := &mockSupervisor{}
	dep := newDeployer(t, sup, cfg, errors.New("build failed"))

	must(t, dep.DB.(*db.DB).AddProject(db.Project{
		Name: "my-system", SpecPath: "github.com/myorg/my-system", Ref: "@main",
	}))

	var worktreeRemoved bool
	dep.WorktreeRemove = func(_, _ string) error { worktreeRemoved = true; return nil }

	err := dep.Deploy(context.Background(), lifecycle.Request{
		Name:         "my-system",
		Address:      "my-system",
		Ref:          "@main",
		SpecPath:     "github.com/myorg/my-system",
		RootSpecPath: "github.com/myorg/my-system",
		NewSHA:       "abc123",
	})

	if err == nil {
		t.Fatal("expected error on build failure")
	}

	// No services should have been spawned.
	if n := len(sup.spawnNames()); n != 0 {
		t.Errorf("expected 0 spawns after build failure, got %d", n)
	}

	// Failed worktree should be cleaned up.
	if !worktreeRemoved {
		t.Error("expected worktree to be removed after build failure")
	}

	// SHA should NOT be recorded.
	projects, _ := dep.DB.(*db.DB).ListProjects()
	if len(projects) > 0 && projects[0].CurrentSHA != "" {
		t.Errorf("SHA should not be recorded after build failure, got %q", projects[0].CurrentSHA)
	}
}

func TestDeploy_VerifyFailure_Rollback(t *testing.T) {
	newCfg := &config.ProjectFile{
		Services: map[string]config.Service{"api": {Run: "python api.py"}},
	}
	prevCfg := &config.ProjectFile{
		Services: map[string]config.Service{"api": {Run: "python api_old.py"}},
	}

	sup := &mockSupervisor{
		// Simulate the new "api" service crashing immediately.
		statuses: map[string]supervisor.Status{
			"my-system/api": {Running: false, Restarts: 1},
		},
	}

	dep := newDeployer(t, sup, newCfg, nil)

	must(t, dep.DB.(*db.DB).AddProject(db.Project{
		Name: "my-system", SpecPath: "github.com/myorg/my-system", Ref: "@main",
	}))
	must(t, dep.DB.(*db.DB).SetCurrentSHA("my-system", "prev123"))

	var removedWorktrees []string
	dep.WorktreeRemove = func(_, wt string) error {
		removedWorktrees = append(removedWorktrees, wt)
		return nil
	}

	err := dep.Deploy(context.Background(), lifecycle.Request{
		Name:         "my-system",
		Address:      "my-system",
		Ref:          "@main",
		SpecPath:     "github.com/myorg/my-system",
		RootSpecPath: "github.com/myorg/my-system",
		NewSHA:       "abc123",
		PrevSHA:      "prev123",
		PrevConfig:   prevCfg,
	})

	if err == nil {
		t.Fatal("expected error on verify failure")
	}

	// The new service should have been spawned then stopped.
	spawns := sup.spawnNames()
	sup.mu.Lock()
	stops := append([]string(nil), sup.stopped...)
	sup.mu.Unlock()

	// SHUTDOWN (prev): stop "my-system/api" (from PrevConfig)
	// STARTUP (new):   spawn "my-system/api"
	// ROLLBACK stop:   stop "my-system/api" again
	// ROLLBACK spawn:  spawn "my-system/api" again (from PrevConfig)
	if len(spawns) != 2 {
		t.Errorf("expected 2 spawns (new + rollback restore), got %d: %v", len(spawns), spawns)
	}
	if len(stops) != 2 {
		t.Errorf("expected 2 stops (shutdown + rollback), got %d: %v", len(stops), stops)
	}

	// Failed worktree should be removed.
	if len(removedWorktrees) == 0 {
		t.Error("expected failed worktree to be removed during rollback")
	}

	// CurrentSHA should remain at the previous value.
	projects, _ := dep.DB.(*db.DB).ListProjects()
	if len(projects) > 0 && projects[0].CurrentSHA != "prev123" {
		t.Errorf("SHA should not be promoted on rollback, got %q", projects[0].CurrentSHA)
	}
}

func TestDeploy_Swap_StopsOldServices(t *testing.T) {
	newCfg := &config.ProjectFile{
		Services: map[string]config.Service{
			"api": {Run: "python api_v2.py"},
		},
	}
	prevCfg := &config.ProjectFile{
		Services: map[string]config.Service{
			"api":    {Run: "python api.py"},
			"worker": {Run: "python worker.py"},
		},
	}

	sup := &mockSupervisor{
		statuses: map[string]supervisor.Status{
			"my-system/api": {Running: true},
		},
	}
	dep := newDeployer(t, sup, newCfg, nil)

	must(t, dep.DB.(*db.DB).AddProject(db.Project{
		Name: "my-system", SpecPath: "github.com/myorg/my-system", Ref: "@main",
	}))
	must(t, dep.DB.(*db.DB).SetCurrentSHA("my-system", "prev123"))

	err := dep.Deploy(context.Background(), lifecycle.Request{
		Name:         "my-system",
		Address:      "my-system",
		Ref:          "@main",
		SpecPath:     "github.com/myorg/my-system",
		RootSpecPath: "github.com/myorg/my-system",
		NewSHA:       "abc123",
		PrevSHA:      "prev123",
		PrevConfig:   prevCfg,
	})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	// Both old services should have been stopped.
	sup.mu.Lock()
	stops := append([]string(nil), sup.stopped...)
	sup.mu.Unlock()
	if len(stops) != 2 {
		t.Errorf("expected 2 old services stopped, got %d: %v", len(stops), stops)
	}

	// Only the new service should have been spawned.
	if n := len(sup.spawnNames()); n != 1 {
		t.Errorf("expected 1 spawn, got %d", n)
	}
}

func TestDeploy_VolumeEnvInjection(t *testing.T) {
	cfg := &config.ProjectFile{
		Services: map[string]config.Service{"api": {Run: "python api.py"}},
		Volumes:  map[string]struct{}{"my-data": {}},
	}
	sup := &mockSupervisor{
		statuses: map[string]supervisor.Status{
			"my-system/api": {Running: true},
		},
	}
	dep := newDeployer(t, sup, cfg, nil)

	must(t, dep.DB.(*db.DB).AddProject(db.Project{
		Name: "my-system", SpecPath: "github.com/myorg/my-system", Ref: "@main",
	}))

	if err := dep.Deploy(context.Background(), lifecycle.Request{
		Name:         "my-system",
		Address:      "my-system",
		Ref:          "@main",
		SpecPath:     "github.com/myorg/my-system",
		RootSpecPath: "github.com/myorg/my-system",
		NewSHA:       "abc123",
	}); err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	sup.mu.Lock()
	defer sup.mu.Unlock()
	if len(sup.spawned) == 0 {
		t.Fatal("expected api to be spawned")
	}
	env := sup.spawned[0].spec.Env
	found := false
	for _, e := range env {
		if len(e) > 15 && e[:15] == "NEXUS_VOLUME_MY" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("NEXUS_VOLUME_MY_DATA not found in service env; env = %v", env)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
