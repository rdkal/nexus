package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/rdkal/nexus/internal/config"
)

// newMux builds the HTTP request multiplexer for the daemon API.
//
// Project addresses and inline service names contain slashes (e.g. "root/db",
// "metrics/exporter"), which Go 1.22's routing can only capture with a trailing
// {rest...} wildcard. So everything under /projects/ is caught by a single
// wildcard route per method and dispatched by splitRoute, which classifies the
// path by its structural suffix (history, redeploy, services, .../log,
// .../restart). The segments "history", "redeploy" and "services" are therefore
// reserved as the last path segment on this internal socket.
func (d *Daemon) newMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /projects", d.handleListProjects)
	mux.HandleFunc("GET /projects/{rest...}", d.handleProjectGet)
	mux.HandleFunc("POST /projects/{rest...}", d.handleProjectPost)
	return mux
}

// splitRoute classifies a path under /projects/ into an action and the addresses
// it targets. addr is the project's resource address; svc (when present) is the
// service's address relative to that project. Both may contain slashes.
func splitRoute(rest string) (action, addr, svc string) {
	segs := strings.Split(rest, "/")
	n := len(segs)
	last := segs[n-1]

	// Service sub-resource: <addr>/services/<svc...>/{log|restart}.
	if last == "log" || last == "restart" {
		for i := 1; i <= n-3; i++ {
			if segs[i] == "services" {
				return last, strings.Join(segs[:i], "/"), strings.Join(segs[i+1:n-1], "/")
			}
		}
	}

	// Project sub-resource or collection.
	switch last {
	case "history", "redeploy", "services":
		if n >= 2 {
			return last, strings.Join(segs[:n-1], "/"), ""
		}
	}

	return "detail", rest, ""
}

// handleProjectGet dispatches GET requests under /projects/.
func (d *Daemon) handleProjectGet(w http.ResponseWriter, r *http.Request) {
	action, addr, svc := splitRoute(r.PathValue("rest"))
	switch action {
	case "detail":
		d.getProject(w, addr)
	case "history":
		d.getHistory(w, addr)
	case "services":
		d.listServices(w, addr)
	case "log":
		d.getLog(w, addr, svc)
	default:
		http.NotFound(w, r)
	}
}

// handleProjectPost dispatches POST requests under /projects/.
func (d *Daemon) handleProjectPost(w http.ResponseWriter, r *http.Request) {
	action, addr, svc := splitRoute(r.PathValue("rest"))
	switch action {
	case "redeploy":
		d.redeploy(w, addr)
	case "restart":
		d.restartService(w, addr, svc)
	default:
		http.NotFound(w, r)
	}
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

func (d *Daemon) projectHealth(address string, cfg *config.ProjectFile) string {
	if cfg == nil {
		return "not_deployed"
	}
	// Health spans the project and its inline sub-projects, whose services deploy
	// together with it under nested addresses.
	units, _ := cfg.Flatten()
	total := 0
	for _, u := range units {
		total += len(u.Services)
	}
	if total == 0 {
		return "no_services"
	}
	for _, u := range units {
		uAddr := subAddress(address, u.RelPath)
		for svcName := range u.Services {
			st, ok := d.Sup.Status(serviceKey(uAddr, svcName))
			if !ok || !st.Running || st.Degraded {
				return "degraded"
			}
		}
	}
	return "healthy"
}

// subAddress joins a base address with a unit's relative alias chain.
func subAddress(base string, rel []string) string {
	if len(rel) == 0 {
		return base
	}
	return base + "/" + strings.Join(rel, "/")
}

// --- handlers ---

func (d *Daemon) handleListProjects(w http.ResponseWriter, r *http.Request) {
	// List every live project keyed by address — root projects and the external
	// sub-projects discovered from their configs — so the tree is fully observable.
	d.mu.RLock()
	addresses := make([]string, 0, len(d.projects))
	states := make(map[string]*projectState, len(d.projects))
	for addr, ps := range d.projects {
		addresses = append(addresses, addr)
		states[addr] = ps
	}
	d.mu.RUnlock()

	sort.Strings(addresses)

	out := make([]projectSummary, 0, len(addresses))
	for _, addr := range addresses {
		ps := states[addr]
		ps.mu.RLock()
		cfg := ps.cfg
		summary := projectSummary{
			Name:       ps.address,
			SpecPath:   ps.specPath,
			Ref:        ps.ref,
			CurrentSHA: ps.sha,
		}
		ps.mu.RUnlock()
		summary.Health = d.projectHealth(addr, cfg)
		out = append(out, summary)
	}
	writeJSON(w, out)
}

func (d *Daemon) getProject(w http.ResponseWriter, address string) {
	d.mu.RLock()
	ps := d.projects[address]
	d.mu.RUnlock()

	if ps == nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}

	ps.mu.RLock()
	summary := projectSummary{
		Name:       ps.address,
		SpecPath:   ps.specPath,
		Ref:        ps.ref,
		CurrentSHA: ps.sha,
	}
	cfg := ps.cfg
	ps.mu.RUnlock()

	summary.Health = d.projectHealth(address, cfg)
	writeJSON(w, summary)
}

func (d *Daemon) getHistory(w http.ResponseWriter, address string) {
	deployments, err := d.DB.ListDeployments(address, 50)
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

func (d *Daemon) redeploy(w http.ResponseWriter, address string) {
	d.mu.RLock()
	ps := d.projects[address]
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

func (d *Daemon) listServices(w http.ResponseWriter, address string) {
	d.mu.RLock()
	ps := d.projects[address]
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

	// Include the project's own services and those of its inline sub-projects.
	// An inline service's Name is its address relative to this project (e.g.
	// "metrics/exporter"); Key is the full supervisor key.
	units, _ := cfg.Flatten()
	out := make([]serviceSummary, 0)
	for _, u := range units {
		uAddr := subAddress(address, u.RelPath)
		relPrefix := strings.Join(u.RelPath, "/")
		for svcName := range u.Services {
			displayName := svcName
			if relPrefix != "" {
				displayName = relPrefix + "/" + svcName
			}
			key := serviceKey(uAddr, svcName)
			st, _ := d.Sup.Status(key)
			out = append(out, serviceSummary{
				Name:     displayName,
				Key:      key,
				Running:  st.Running,
				Degraded: st.Degraded,
				Restarts: st.Restarts,
				PID:      st.PID,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, out)
}

func (d *Daemon) getLog(w http.ResponseWriter, address, svc string) {
	key := serviceKey(address, svc)

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

func (d *Daemon) restartService(w http.ResponseWriter, address, svc string) {
	d.mu.RLock()
	ps := d.projects[address]
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

	key := serviceKey(address, svc)
	d.Sup.Stop(key)
	d.Sup.Spawn(key, spec)

	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, map[string]string{"restarted": key})
}
