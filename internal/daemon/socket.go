package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"

	"github.com/rdkal/nexus/internal/config"
)

// newMux builds the HTTP request multiplexer for the daemon API.
func (d *Daemon) newMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /projects", d.handleListProjects)
	mux.HandleFunc("GET /projects/{name}", d.handleGetProject)
	mux.HandleFunc("GET /projects/{name}/history", d.handleGetHistory)
	mux.HandleFunc("POST /projects/{name}/redeploy", d.handleRedeploy)
	mux.HandleFunc("GET /projects/{name}/services", d.handleListServices)
	mux.HandleFunc("GET /projects/{name}/services/{svc}/log", d.handleGetLog)
	mux.HandleFunc("POST /projects/{name}/services/{svc}/restart", d.handleRestartService)
	return mux
}

// ServeHTTP implements http.Handler so the daemon can be used directly in tests.
func (d *Daemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.newMux().ServeHTTP(w, r)
}

// serve removes any stale socket, listens on the Unix socket path,
// and serves the HTTP API until ctx is cancelled.
func (d *Daemon) serve(ctx context.Context) error {
	_ = os.Remove(d.Paths.Socket)
	ln, err := net.Listen("unix", d.Paths.Socket)
	if err != nil {
		return fmt.Errorf("listen %s: %w", d.Paths.Socket, err)
	}

	srv := &http.Server{Handler: d.newMux()}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()

	if err := srv.Serve(ln); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

// --- JSON response types ---

type projectSummary struct {
	Name       string `json:"name"`
	SpecPath   string `json:"spec_path"`
	Ref        string `json:"ref"`
	CurrentSHA string `json:"current_sha,omitempty"`
	Health     string `json:"health"`
}

type serviceSummary struct {
	Name     string `json:"name"`
	Key      string `json:"key"`
	Running  bool   `json:"running"`
	Degraded bool   `json:"degraded"`
	Restarts int    `json:"restarts"`
	PID      string `json:"pid,omitempty"`
}

type deploymentRecord struct {
	ID         int64  `json:"id"`
	SHA        string `json:"sha"`
	Status     string `json:"status"`
	StartedAt  int64  `json:"started_at"`
	FinishedAt *int64 `json:"finished_at,omitempty"`
}

// --- helper ---

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (d *Daemon) projectHealth(name string, cfg *config.ProjectFile) string {
	if cfg == nil {
		return "not_deployed"
	}
	if len(cfg.Services) == 0 {
		return "no_services"
	}
	for svcName := range cfg.Services {
		st, ok := d.Sup.Status(serviceKey(name, svcName))
		if !ok || !st.Running || st.Degraded {
			return "degraded"
		}
	}
	return "healthy"
}

// --- handlers ---

func (d *Daemon) handleListProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := d.DB.ListProjects()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	d.mu.RLock()
	defer d.mu.RUnlock()

	out := make([]projectSummary, 0, len(projects))
	for _, p := range projects {
		var cfg *config.ProjectFile
		if ps, ok := d.projects[p.Name]; ok {
			ps.mu.RLock()
			cfg = ps.cfg
			ps.mu.RUnlock()
		}
		out = append(out, projectSummary{
			Name:       p.Name,
			SpecPath:   p.SpecPath,
			Ref:        p.Ref,
			CurrentSHA: p.CurrentSHA,
			Health:     d.projectHealth(p.Name, cfg),
		})
	}
	writeJSON(w, out)
}

func (d *Daemon) handleGetProject(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	p, err := d.DB.GetProject(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	d.mu.RLock()
	ps := d.projects[name]
	d.mu.RUnlock()

	var cfg *config.ProjectFile
	if ps != nil {
		ps.mu.RLock()
		cfg = ps.cfg
		ps.mu.RUnlock()
	}

	writeJSON(w, projectSummary{
		Name:       p.Name,
		SpecPath:   p.SpecPath,
		Ref:        p.Ref,
		CurrentSHA: p.CurrentSHA,
		Health:     d.projectHealth(name, cfg),
	})
}

func (d *Daemon) handleGetHistory(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	deployments, err := d.DB.ListDeployments(name, 50)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	out := make([]deploymentRecord, 0, len(deployments))
	for _, dep := range deployments {
		rec := deploymentRecord{
			ID:        dep.ID,
			SHA:       dep.SHA,
			Status:    dep.Status,
			StartedAt: dep.StartedAt.Unix(),
		}
		if dep.FinishedAt != nil {
			t := dep.FinishedAt.Unix()
			rec.FinishedAt = &t
		}
		out = append(out, rec)
	}
	writeJSON(w, out)
}

func (d *Daemon) handleRedeploy(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	d.mu.RLock()
	ps := d.projects[name]
	d.mu.RUnlock()

	if ps == nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}

	ps.mu.RLock()
	sha := ps.sha
	ps.mu.RUnlock()

	if sha == "" {
		http.Error(w, "project not yet deployed", http.StatusConflict)
		return
	}

	ps.queue.Push(sha)
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, map[string]string{"queued": sha})
}

func (d *Daemon) handleListServices(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	d.mu.RLock()
	ps := d.projects[name]
	d.mu.RUnlock()

	if ps == nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}

	ps.mu.RLock()
	cfg := ps.cfg
	ps.mu.RUnlock()

	if cfg == nil {
		writeJSON(w, []serviceSummary{})
		return
	}

	out := make([]serviceSummary, 0, len(cfg.Services))
	for svcName := range cfg.Services {
		key := serviceKey(name, svcName)
		st, _ := d.Sup.Status(key)
		out = append(out, serviceSummary{
			Name:     svcName,
			Key:      key,
			Running:  st.Running,
			Degraded: st.Degraded,
			Restarts: st.Restarts,
			PID:      st.PID,
		})
	}
	writeJSON(w, out)
}

func (d *Daemon) handleGetLog(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	svc := r.PathValue("svc")
	key := serviceKey(name, svc)

	logPath := d.Paths.ServiceLog(key)
	f, err := os.Open(logPath)
	if err != nil {
		http.Error(w, "log not found", http.StatusNotFound)
		return
	}
	defer f.Close()

	// Seek to the last 64 KiB if the file is large.
	const tail = 64 * 1024
	if info, err := f.Stat(); err == nil && info.Size() > tail {
		_, _ = f.Seek(-tail, io.SeekEnd)
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.Copy(w, f)
}

func (d *Daemon) handleRestartService(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	svc := r.PathValue("svc")

	d.mu.RLock()
	ps := d.projects[name]
	d.mu.RUnlock()

	if ps == nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}

	ps.mu.RLock()
	spec, ok := ps.svcSpecs[svc]
	ps.mu.RUnlock()

	if !ok {
		http.Error(w, "service not found", http.StatusNotFound)
		return
	}

	key := serviceKey(name, svc)
	d.Sup.Stop(key)
	d.Sup.Spawn(key, spec)

	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, map[string]string{"restarted": key})
}
