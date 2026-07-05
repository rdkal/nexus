package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rdkal/nexus/internal/config"
	"github.com/rdkal/nexus/internal/supervisor"
)

func TestSplitRoute(t *testing.T) {
	cases := []struct {
		rest, action, addr, svc string
	}{
		// detail
		{"my-system", "detail", "my-system", ""},
		{"root/db", "detail", "root/db", ""},
		{"root/db/metrics", "detail", "root/db/metrics", ""},
		// history
		{"my-system/history", "history", "my-system", ""},
		{"root/db/history", "history", "root/db", ""},
		// redeploy
		{"my-system/redeploy", "redeploy", "my-system", ""},
		{"root/db/redeploy", "redeploy", "root/db", ""},
		// services collection
		{"my-system/services", "services", "my-system", ""},
		{"root/db/services", "services", "root/db", ""},
		// service log — top-level, nested project, inline service
		{"app/services/api/log", "log", "app", "api"},
		{"root/db/services/store/log", "log", "root/db", "store"},
		{"app/services/metrics/exporter/log", "log", "app", "metrics/exporter"},
		// service restart
		{"app/services/api/restart", "restart", "app", "api"},
		{"app/services/metrics/exporter/restart", "restart", "app", "metrics/exporter"},
	}
	for _, c := range cases {
		action, addr, svc := splitRoute(c.rest)
		if action != c.action || addr != c.addr || svc != c.svc {
			t.Errorf("splitRoute(%q) = (%q,%q,%q), want (%q,%q,%q)",
				c.rest, action, addr, svc, c.action, c.addr, c.svc)
		}
	}
}

// recSup records Stop/Spawn calls and serves preset statuses.
type recSup struct {
	statuses         map[string]supervisor.Status
	stopped, spawned []string
}

func (s *recSup) Spawn(name string, _ supervisor.ServiceSpec) { s.spawned = append(s.spawned, name) }
func (s *recSup) Stop(name string)                           { s.stopped = append(s.stopped, name) }
func (s *recSup) Status(name string) (supervisor.Status, bool) {
	st, ok := s.statuses[name]
	return st, ok
}

func TestServeHTTP_NestedDetailAndServices(t *testing.T) {
	sup := &recSup{statuses: map[string]supervisor.Status{
		"root/db/store": {Running: true, PID: "123"},
	}}
	d := &Daemon{Sup: sup, projects: map[string]*projectState{}}
	d.projects["root/db"] = &projectState{
		address:  "root/db",
		specPath: "github.com/community/postgres",
		ref:      "@v15",
		sha:      "abc123",
		cfg: &config.ProjectFile{
			Services: map[string]config.Service{"store": {Run: "postgres"}},
		},
	}

	// Detail of a nested address must route through {rest...}.
	rec := httptest.NewRecorder()
	d.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/projects/root/db", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("detail status %d", rec.Code)
	}
	var detail map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&detail)
	if detail["name"] != "root/db" || detail["health"] != "healthy" {
		t.Errorf("detail = %v", detail)
	}

	// Services of a nested address.
	rec = httptest.NewRecorder()
	d.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/projects/root/db/services", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("services status %d", rec.Code)
	}
	var svcs []map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&svcs)
	if len(svcs) != 1 || svcs[0]["key"] != "root/db/store" {
		t.Errorf("services = %v", svcs)
	}
}

func TestServeHTTP_RestartInlineService(t *testing.T) {
	sup := &recSup{statuses: map[string]supervisor.Status{}}
	d := &Daemon{Sup: sup, projects: map[string]*projectState{}}
	d.projects["app"] = &projectState{
		address: "app",
		svcSpecs: map[string]supervisor.ServiceSpec{
			"metrics/exporter": {Command: "./exporter"},
		},
	}

	// Restart an inline service addressed app/metrics/exporter.
	rec := httptest.NewRecorder()
	d.ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost, "/projects/app/services/metrics/exporter/restart", nil))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("restart status %d", rec.Code)
	}
	if len(sup.stopped) != 1 || sup.stopped[0] != "app/metrics/exporter" {
		t.Errorf("stopped = %v, want [app/metrics/exporter]", sup.stopped)
	}
	if len(sup.spawned) != 1 || sup.spawned[0] != "app/metrics/exporter" {
		t.Errorf("spawned = %v, want [app/metrics/exporter]", sup.spawned)
	}
}
