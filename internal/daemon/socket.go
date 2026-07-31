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
	"github.com/rdkal/nexus/internal/db"
	"github.com/rdkal/nexus/internal/home"
)

// newMux builds the HTTP request multiplexer for the daemon API.
//
// Project addresses and inline service names contain slashes (e.g. "root/db",
// "metrics/exporter"), which Go 1.22's routing can only capture with a trailing
// {rest...} wildcard. So everything under /projects/ is caught by a single
// wildcard route per method and dispatched by splitRoute, which classifies the
// path by its structural suffix (history, redeploy, services, .../services/<svc>/log,
// .../services/<svc>/restart, .../builds/<sha>/log). The segments "history",
// "redeploy" and "services" are therefore reserved as the last path segment on
// this internal socket.
func (d *Daemon) newMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /projects", d.handleListProjects)
	mux.HandleFunc("POST /projects", d.handleReconcile)
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

	// Build log: <addr>/builds/<sha>/log. A SHA has no slashes, so "builds" is
	// always the third-from-last segment — distinct from a service log's
	// "services" marker. svc carries the SHA.
	if last == "log" && n >= 4 && segs[n-3] == "builds" {
		return "buildlog", strings.Join(segs[:n-3], "/"), segs[n-2]
	}

	// Task manual run: <addr>/tasks/<task>/run. Task names have no slashes, so
	// "tasks" is always the third-from-last segment; svc carries the task name.
	if last == "run" && n >= 4 && segs[n-3] == "tasks" {
		return "taskrun", strings.Join(segs[:n-3], "/"), segs[n-2]
	}

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
	case "history", "redeploy", "services", "tasks":
		if n >= 2 {
			return last, strings.Join(segs[:n-1], "/"), ""
		}
	}

	return "detail", rest, ""
}

// handleReconcile re-syncs root projects from the DB (start added, stop removed).
// Called by `nexus project add`/`remove`. Runs in the background so the CLI gets a
// fast reply; the actual clone/start happens under the daemon's own context.
func (d *Daemon) handleReconcile(w http.ResponseWriter, r *http.Request) {
	go func() {
		d.reconcileRoots()
		d.reconcileStopped()
	}()
	w.WriteHeader(http.StatusAccepted)
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
	case "tasks":
		d.listTaskRuns(w, addr)
	case "log":
		d.getLog(w, addr, svc)
	case "buildlog":
		d.getBuildLog(w, addr, svc) // svc carries the SHA
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
	case "taskrun":
		d.handleTaskRun(w, addr, svc) // svc carries the task name
	default:
		http.NotFound(w, r)
	}
}

// handleTaskRun manually fires a task (and cascades to its after: dependents).
func (d *Daemon) handleTaskRun(w http.ResponseWriter, address, task string) {
	if err := d.triggerTask(address, task); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]string{"triggered": address + "/" + task})
}

type taskRunRecord struct {
	ID         int64  `json:"id"`
	Task       string `json:"task"`
	Reason     string `json:"reason"`
	Status     string `json:"status"`
	ExitCode   *int   `json:"exit_code,omitempty"`
	StartedAt  int64  `json:"started_at"`
	FinishedAt *int64 `json:"finished_at,omitempty"`
}

// listTaskRuns returns a project's recent task runs, newest first.
func (d *Daemon) listTaskRuns(w http.ResponseWriter, address string) {
	runs, err := d.DB.ListTaskRuns(address, 50)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]taskRunRecord, 0, len(runs))
	for _, r := range runs {
		rec := taskRunRecord{
			ID:        r.ID,
			Task:      r.Task,
			Reason:    r.Reason,
			Status:    r.Status,
			ExitCode:  r.ExitCode,
			StartedAt: r.StartedAt.Unix(),
		}
		if r.FinishedAt != nil {
			f := r.FinishedAt.Unix()
			rec.FinishedAt = &f
		}
		out = append(out, rec)
	}
	writeJSON(w, out)
}

// ServeHTTP implements http.Handler so the daemon can be used directly in tests.
func (d *Daemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.newMux().ServeHTTP(w, r)
}

// serve removes any stale socket, listens on the Unix socket path,
// and serves the HTTP API until ctx is cancelled.
func (d *Daemon) serve(ctx context.Context) error {
	if err := home.CheckSocketPath(d.Paths.Socket); err != nil {
		return err
	}
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
	Name       string        `json:"name"`
	SpecPath   string        `json:"spec_path"`
	Ref        string        `json:"ref"`
	CurrentSHA string        `json:"current_sha,omitempty"`
	Health     string        `json:"health"`
	Tasks      []taskSummary `json:"tasks,omitempty"` // populated on project detail only
}

type taskSummary struct {
	Name       string `json:"name"`
	Schedule   string `json:"schedule,omitempty"`
	After      string `json:"after,omitempty"`
	LastStatus string `json:"last_status,omitempty"` // running|success|failed; "" = never run
	LastReason string `json:"last_reason,omitempty"`
	LastAt     int64  `json:"last_at,omitempty"`
	LastExit   *int   `json:"last_exit,omitempty"`
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
	summary.Tasks = d.taskSummaries(address, cfg)
	writeJSON(w, summary)
}

// taskSummaries lists a project's task definitions with the status of each
// task's most recent run, so the web UI can show tasks and offer a retry.
func (d *Daemon) taskSummaries(address string, cfg *config.ProjectFile) []taskSummary {
	if cfg == nil || len(cfg.Tasks) == 0 {
		return nil
	}

	// Latest run per task: ListTaskRuns is newest-first, so the first row we see
	// for a task is its most recent run.
	last := map[string]db.TaskRun{}
	if runs, err := d.DB.ListTaskRuns(address, 200); err == nil {
		for _, r := range runs {
			if _, seen := last[r.Task]; !seen {
				last[r.Task] = r
			}
		}
	}

	names := make([]string, 0, len(cfg.Tasks))
	for name := range cfg.Tasks {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]taskSummary, 0, len(names))
	for _, name := range names {
		t := cfg.Tasks[name]
		ts := taskSummary{
			Name:     name,
			Schedule: t.Schedule,
			After:    t.After,
		}
		if r, ok := last[name]; ok {
			ts.LastStatus = r.Status
			ts.LastReason = r.Reason
			ts.LastAt = r.StartedAt.Unix()
			ts.LastExit = r.ExitCode
		}
		out = append(out, ts)
	}
	return out
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
	serveLogFile(w, d.Paths.ServiceLog(serviceKey(address, svc)))
}

// getBuildLog serves the build log for a deployment SHA. svc is the SHA.
func (d *Daemon) getBuildLog(w http.ResponseWriter, address, sha string) {
	serveLogFile(w, d.Paths.BuildLog(address, sha))
}

// serveLogFile streams a log file as text/plain, tailing the last 64 KiB of a
// large file. A missing file is a 404 (e.g. a deployment with no build step).
func serveLogFile(w http.ResponseWriter, path string) {
	f, err := os.Open(path)
	if err != nil {
		http.Error(w, "log not found", http.StatusNotFound)
		return
	}
	defer f.Close()

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
