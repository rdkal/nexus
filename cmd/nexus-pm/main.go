// nexus-pm is the nexus process manager. It is the single systemd target for the
// nexus stack: it supervises the nexus runtime binary as a hardcoded service and
// exposes nexus-pm.sock so the runtime can spawn/stop/query user services.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/rdkal/nexus/internal/home"
	"github.com/rdkal/nexus/internal/supervisor"
)

func main() {
	homeDir, err := home.Dir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexus-pm: %v\n", err)
		os.Exit(1)
	}
	paths := home.NewPaths(homeDir)
	if err := home.Setup(homeDir); err != nil {
		fmt.Fprintf(os.Stderr, "nexus-pm: setup: %v\n", err)
		os.Exit(1)
	}

	sup := &supervisor.Supervisor{}

	// The nexus runtime is a hardcoded supervised service. nexus-pm starts it at
	// boot and restarts it on crash — exactly like any user service. When the
	// nexus binary is updated, the runtime calls POST /runtime/restart which
	// causes nexus-pm to swap in the new binary via a clean stop+start cycle.
	runtimeSpec := supervisor.ServiceSpec{
		Command: filepath.Join(paths.Bin, "nexus") + " daemon",
		WorkDir: paths.Home,
		Env:     append(os.Environ(), "NEXUS_HOME="+paths.Home),
		LogFile: paths.ServiceLog("nexus-runtime"),
	}

	srv := &pmServer{sup: sup, runtimeKey: "nexus-runtime", runtimeSpec: runtimeSpec}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	slog.Info("nexus-pm started", "home", homeDir, "socket", paths.PMSocket)

	if err := srv.serve(ctx, paths); err != nil {
		fmt.Fprintf(os.Stderr, "nexus-pm: %v\n", err)
		os.Exit(1)
	}
}

// pmServer holds the supervisor and the nexus runtime spec for restart operations.
type pmServer struct {
	sup         *supervisor.Supervisor
	runtimeKey  string
	runtimeSpec supervisor.ServiceSpec
}

func (s *pmServer) serve(ctx context.Context, paths home.Paths) error {
	if err := home.CheckSocketPath(paths.PMSocket); err != nil {
		return err
	}
	_ = os.Remove(paths.PMSocket)
	ln, err := net.Listen("unix", paths.PMSocket)
	if err != nil {
		return fmt.Errorf("listen %s: %w", paths.PMSocket, err)
	}

	// Spawn nexus runtime AFTER the socket is ready so nexus can reach nexus-pm.sock
	// immediately on startup without connection-refused errors.
	s.sup.Spawn(s.runtimeKey, s.runtimeSpec)

	mux := http.NewServeMux()
	// {key...} matches service keys that contain slashes (e.g. "my-system/api").
	mux.HandleFunc("POST /services/{key...}", s.handleSpawn)
	mux.HandleFunc("DELETE /services/{key...}", s.handleStop)
	mux.HandleFunc("GET /services/{key...}", s.handleStatus)
	mux.HandleFunc("POST /run/{id}", s.handleRunStart)
	mux.HandleFunc("GET /run/{id}", s.handleRunPoll)
	mux.HandleFunc("DELETE /run/{id}", s.handleRunAck)
	mux.HandleFunc("POST /runtime/restart", s.handleRuntimeRestart)

	httpSrv := &http.Server{Handler: mux}
	go func() {
		<-ctx.Done()
		_ = httpSrv.Close()
		s.sup.StopAll()
	}()

	if err := httpSrv.Serve(ln); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

func (s *pmServer) handleSpawn(w http.ResponseWriter, r *http.Request) {
	var spec supervisor.ServiceSpec
	if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.sup.Spawn(r.PathValue("key"), spec)
	w.WriteHeader(http.StatusAccepted)
}

func (s *pmServer) handleStop(w http.ResponseWriter, r *http.Request) {
	s.sup.Stop(r.PathValue("key"))
	w.WriteHeader(http.StatusNoContent)
}

func (s *pmServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	st, ok := s.sup.Status(r.PathValue("key"))
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(st)
}

// handleRunStart starts a one-shot task run keyed by id and returns immediately.
// The process is a child of nexus-pm — not the nexus runtime — so it survives a
// runtime restart; the runtime learns the outcome by polling GET /run/{id}.
func (s *pmServer) handleRunStart(w http.ResponseWriter, r *http.Request) {
	var spec supervisor.ServiceSpec
	if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.sup.StartRun(r.PathValue("id"), spec); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// handleRunPoll reports a run's state: 404 if unknown, else {status, exit_code, error}.
func (s *pmServer) handleRunPoll(w http.ResponseWriter, r *http.Request) {
	state, known, _ := s.sup.PollRun(r.PathValue("id"))
	if !known {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}
	status := "running"
	if state.Done {
		status = "done"
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Status   string `json:"status"`
		ExitCode int    `json:"exit_code"`
		Error    string `json:"error,omitempty"`
	}{Status: status, ExitCode: state.ExitCode, Error: state.Err})
}

// handleRunAck drops a run record once the runtime has recorded its outcome.
func (s *pmServer) handleRunAck(w http.ResponseWriter, r *http.Request) {
	s.sup.AckRun(r.PathValue("id"))
	w.WriteHeader(http.StatusNoContent)
}

// handleRuntimeRestart stops the nexus runtime and immediately re-spawns it.
// The nexus binary at $NEXUS_HOME/bin/nexus has already been atomically replaced
// by the build script before this endpoint is called.
func (s *pmServer) handleRuntimeRestart(w http.ResponseWriter, r *http.Request) {
	s.sup.Stop(s.runtimeKey)
	s.sup.Spawn(s.runtimeKey, s.runtimeSpec)
	w.WriteHeader(http.StatusAccepted)
}
