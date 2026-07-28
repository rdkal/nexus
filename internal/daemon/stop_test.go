package daemon

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/rdkal/nexus/internal/config"
	"github.com/rdkal/nexus/internal/db"
	"github.com/rdkal/nexus/internal/home"
	"github.com/rdkal/nexus/internal/poller"
	"github.com/rdkal/nexus/internal/supervisor"
)

// recordingSup is a SupervisorAPI that records every Spawn/Stop call, for
// asserting on pause/resume behavior without a real process.
type recordingSup struct {
	mu      sync.Mutex
	spawned map[string]bool // last action per key was Spawn
}

func (s *recordingSup) Spawn(name string, _ supervisor.ServiceSpec) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.spawned == nil {
		s.spawned = map[string]bool{}
	}
	s.spawned[name] = true
}
func (s *recordingSup) Stop(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.spawned == nil {
		s.spawned = map[string]bool{}
	}
	s.spawned[name] = false
}
func (s *recordingSup) Status(string) (supervisor.Status, bool) { return supervisor.Status{}, false }
func (s *recordingSup) isRunning(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.spawned[name]
}

func testDB(t *testing.T) *db.DB {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "nexus.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

// TestReconcileChildren_ExcludesPausedSubProject verifies that a sub-project
// paused via `nexus project stop <nested-address>` is treated like a removed
// one: reconcileChildren stops it and drops it from the children map, even
// though it is still declared in the parent's nexus.yaml. The child is seeded
// as already-running (rather than started via a real reconcile pass) so the
// test never touches git — starting a fresh child clones its repo over the
// network via startChild.
func TestReconcileChildren_ExcludesPausedSubProject(t *testing.T) {
	database := testDB(t)
	sup := &recordingSup{}
	d := New(database, sup, home.NewPaths(t.TempDir()))
	d.ctx = context.Background()

	child := &projectState{
		address: "retu/ingest",
		ref:     "main",
		src:     "github.com/rdkal/retu/ingest",
		queue:   &poller.Queue{},
	}
	parent := &projectState{
		address:  "retu",
		queue:    &poller.Queue{},
		children: map[string]*projectState{"ingest": child},
	}
	d.projects["retu/ingest"] = child

	cfg := &config.ProjectFile{
		Projects: map[string]config.SubProject{
			"ingest": {Src: "github.com/rdkal/retu/ingest", Ref: "main"},
		},
	}

	// Not paused yet: reconcile is a no-op, the seeded child stays live.
	d.reconcileChildren(context.Background(), parent, cfg)
	if _, ok := d.projects["retu/ingest"]; !ok {
		t.Fatalf("expected retu/ingest to remain live before pausing")
	}

	// Pause it, then reconcile again: it must stop, exactly like a removal —
	// this is the mechanism behind `nexus project stop retu/ingest`.
	if err := database.SetProjectPaused("retu/ingest", true); err != nil {
		t.Fatal(err)
	}
	d.reconcileChildren(context.Background(), parent, cfg)
	if _, ok := d.projects["retu/ingest"]; ok {
		t.Errorf("expected retu/ingest to be stopped while paused, still live: %v", d.projects)
	}
	if _, ok := parent.children["ingest"]; ok {
		t.Error("expected ingest removed from parent.children while paused")
	}
}

// TestReconcileStopped_PausesAndResumesIndividualService verifies that a single
// service can be paused/resumed without touching its sibling services, and
// without needing a redeploy for the change to take effect.
func TestReconcileStopped_PausesAndResumesIndividualService(t *testing.T) {
	database := testDB(t)
	sup := &recordingSup{}
	d := New(database, sup, home.NewPaths(t.TempDir()))
	d.ctx = context.Background()

	ps := &projectState{
		address: "retu/traefik",
		queue:   &poller.Queue{},
		svcSpecs: map[string]supervisor.ServiceSpec{
			"traefik": {Command: "run-traefik"},
		},
	}
	d.projects["retu/traefik"] = ps

	// Baseline reconcile with nothing paused: the service should be running.
	d.reconcileStopped()
	if !sup.isRunning("retu/traefik/traefik") {
		t.Fatal("expected service to be running before any pause")
	}

	// Pause it: reconcileStopped (what a CLI stop ultimately triggers via
	// notifyDaemon) must stop it immediately, without any new deploy.
	if err := database.SetServicePaused("retu/traefik/traefik", true); err != nil {
		t.Fatal(err)
	}
	d.reconcileStopped()
	if sup.isRunning("retu/traefik/traefik") {
		t.Error("expected service to be stopped while paused")
	}

	// Resume it: should spawn again from the cached spec, no redeploy needed.
	if err := database.SetServicePaused("retu/traefik/traefik", false); err != nil {
		t.Fatal(err)
	}
	d.reconcileStopped()
	if !sup.isRunning("retu/traefik/traefik") {
		t.Error("expected service to run again after resume")
	}
}
