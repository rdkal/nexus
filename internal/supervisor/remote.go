package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
)

// RemoteSupervisor implements Spawn/Stop/Status by forwarding calls to nexus-pm
// over its Unix socket. Drop-in replacement for the in-process *Supervisor in the
// nexus runtime — the nexus runtime holds no OS process handles of its own.
type RemoteSupervisor struct {
	SocketPath string
	client     *http.Client
}

// NewRemoteSupervisor creates a client that talks to nexus-pm at socketPath.
func NewRemoteSupervisor(socketPath string) *RemoteSupervisor {
	return &RemoteSupervisor{
		SocketPath: socketPath,
		client: &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
				},
			},
		},
	}
}

// Spawn asks nexus-pm to start the named service. Fire-and-forget; logs on failure.
// No-op if the service is already running — nexus-pm enforces this on its side.
func (r *RemoteSupervisor) Spawn(name string, spec ServiceSpec) {
	body, _ := json.Marshal(spec)
	resp, err := r.client.Post("http://nexus-pm/services/"+name, "application/json", bytes.NewReader(body))
	if err != nil {
		slog.Warn("remote-supervisor: spawn failed", "service", name, "err", err)
		return
	}
	resp.Body.Close()
}

// Stop asks nexus-pm to stop the named service and blocks until it exits.
func (r *RemoteSupervisor) Stop(name string) {
	req, _ := http.NewRequest(http.MethodDelete, "http://nexus-pm/services/"+name, nil)
	resp, err := r.client.Do(req)
	if err != nil {
		slog.Warn("remote-supervisor: stop failed", "service", name, "err", err)
		return
	}
	resp.Body.Close()
}

// Status returns the current status of the named service from nexus-pm.
func (r *RemoteSupervisor) Status(name string) (Status, bool) {
	resp, err := r.client.Get("http://nexus-pm/services/" + name)
	if err != nil {
		slog.Warn("remote-supervisor: status failed", "service", name, "err", err)
		return Status{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return Status{}, false
	}
	var st Status
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return Status{}, false
	}
	return st, true
}

// StartRun asks nexus-pm to start a one-shot task run keyed by id. Non-blocking:
// nexus-pm owns the process, so it survives a runtime restart. Poll for the outcome.
func (r *RemoteSupervisor) StartRun(id string, spec ServiceSpec) error {
	body, _ := json.Marshal(spec)
	resp, err := r.client.Post("http://nexus-pm/run/"+id, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("start run %s: %w", id, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("start run %s: %s", id, strings.TrimSpace(string(b)))
	}
	return nil
}

// PollRun queries a run's state from nexus-pm. known is false only on a definitive
// 404 (the run is gone); a transport error returns a non-nil err so the caller
// retries instead of treating a blip as a lost run.
func (r *RemoteSupervisor) PollRun(id string) (state RunState, known bool, err error) {
	resp, err := r.client.Get("http://nexus-pm/run/" + id)
	if err != nil {
		return RunState{}, true, err // transient: keep waiting
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return RunState{}, false, nil
	}
	var out struct {
		Status   string `json:"status"`
		ExitCode int    `json:"exit_code"`
		Error    string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return RunState{}, true, err
	}
	return RunState{Done: out.Status == "done", ExitCode: out.ExitCode, Err: out.Error}, true, nil
}

// StopRun asks nexus-pm to signal a running one-shot run to terminate.
func (r *RemoteSupervisor) StopRun(id string) {
	resp, err := r.client.Post("http://nexus-pm/run/"+id+"/stop", "", nil)
	if err == nil {
		resp.Body.Close()
	}
}

// AckRun tells nexus-pm the run outcome has been recorded and can be dropped.
func (r *RemoteSupervisor) AckRun(id string) {
	req, _ := http.NewRequest(http.MethodDelete, "http://nexus-pm/run/"+id, nil)
	if resp, err := r.client.Do(req); err == nil {
		resp.Body.Close()
	}
}

// RestartRuntime asks nexus-pm to stop and restart the nexus runtime binary.
// Called after a successful self-build deployment so nexus-pm loads the new binary.
func (r *RemoteSupervisor) RestartRuntime() error {
	resp, err := r.client.Post("http://nexus-pm/runtime/restart", "", nil)
	if err != nil {
		return fmt.Errorf("runtime restart: %w", err)
	}
	resp.Body.Close()
	return nil
}
