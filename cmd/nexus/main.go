package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/rdkal/nexus/internal/daemon"
	"github.com/rdkal/nexus/internal/db"
	"github.com/rdkal/nexus/internal/git"
	"github.com/rdkal/nexus/internal/home"
	"github.com/rdkal/nexus/internal/spec"
	"github.com/rdkal/nexus/internal/supervisor"
)

// version is the release version, injected at build time via
// -ldflags "-X main.version=vX.Y.Z". Empty in local/dev builds, where
// resolveVersion falls back to the Go build info.
var version = ""

// resolveVersion returns the best available version string: the injected release
// tag, else the module version or VCS revision from the build info, else "dev".
func resolveVersion() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		if v := bi.Main.Version; v != "" && v != "(devel)" {
			return v
		}
		var rev string
		var dirty bool
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value
			case "vcs.modified":
				dirty = s.Value == "true"
			}
		}
		if rev != "" {
			if len(rev) > 12 {
				rev = rev[:12]
			}
			if dirty {
				rev += "-dirty"
			}
			return rev
		}
	}
	return "dev"
}

// notifyDaemon asks a running daemon to reconcile projects from the DB (start
// newly-added, stop removed) over its Unix socket. Best-effort: if the daemon is
// not running, the change still takes effect the next time it starts.
func notifyDaemon(homeFlag string) {
	homeDir, err := resolveHome(homeFlag)
	if err != nil {
		return
	}
	sock := home.NewPaths(homeDir).Socket
	client := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", sock)
			},
		},
	}
	resp, err := client.Post("http://nexus/projects", "", nil)
	if err != nil {
		return
	}
	_ = resp.Body.Close()
}

func main() {
	if err := rootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	var homeFlag string

	root := &cobra.Command{
		Use:     "nexus",
		Short:   "Git-native process manager",
		Version: resolveVersion(),
	}
	root.SetVersionTemplate("nexus {{.Version}}\n")
	root.PersistentFlags().StringVar(&homeFlag, "home", "", "override NEXUS_HOME")

	root.AddCommand(daemonCmd(&homeFlag))
	root.AddCommand(projectCmd(&homeFlag))
	root.AddCommand(serviceCmd(&homeFlag))
	root.AddCommand(taskCmd(&homeFlag))
	root.AddCommand(runCmd(&homeFlag))
	root.AddCommand(versionCmd())
	return root
}

// daemonDo makes a request to the running daemon over its Unix socket.
func daemonDo(homeFlag, method, path string) (int, []byte, error) {
	return daemonDoBody(homeFlag, method, path, nil)
}

// daemonDoBody is daemonDo with an optional JSON request body.
func daemonDoBody(homeFlag, method, path string, body io.Reader) (int, []byte, error) {
	homeDir, err := resolveHome(homeFlag)
	if err != nil {
		return 0, nil, err
	}
	sock := home.NewPaths(homeDir).Socket
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", sock)
			},
		},
	}
	req, err := http.NewRequest(method, "http://nexus"+path, body)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("daemon not reachable at %s: %w", sock, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, respBody, nil
}

func taskCmd(homeFlag *string) *cobra.Command {
	cmd := &cobra.Command{Use: "task", Short: "Trigger and inspect tasks"}
	cmd.AddCommand(taskRunCmd(homeFlag))
	cmd.AddCommand(taskListCmd(homeFlag))
	return cmd
}

func taskRunCmd(homeFlag *string) *cobra.Command {
	return &cobra.Command{
		Use:   "run <project>/<task>",
		Short: "Trigger a task now (cascades to its after: dependents)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			i := strings.LastIndex(args[0], "/")
			if i < 0 {
				return fmt.Errorf("expected <project>/<task>, got %q", args[0])
			}
			addr, task := args[0][:i], args[0][i+1:]
			code, body, err := daemonDo(*homeFlag, "POST", "/projects/"+addr+"/tasks/"+task+"/run")
			if err != nil {
				return err
			}
			if code != http.StatusOK {
				return fmt.Errorf("%s", strings.TrimSpace(string(body)))
			}
			fmt.Printf("triggered %s/%s\n", addr, task)
			return nil
		},
	}
}

func taskListCmd(homeFlag *string) *cobra.Command {
	return &cobra.Command{
		Use:   "list <project>",
		Short: "Show recent task runs for a project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			code, body, err := daemonDo(*homeFlag, "GET", "/projects/"+args[0]+"/tasks")
			if err != nil {
				return err
			}
			if code != http.StatusOK {
				return fmt.Errorf("%s", strings.TrimSpace(string(body)))
			}
			var runs []struct {
				Task      string `json:"task"`
				Reason    string `json:"reason"`
				Status    string `json:"status"`
				ExitCode  *int   `json:"exit_code"`
				StartedAt int64  `json:"started_at"`
			}
			if err := json.Unmarshal(body, &runs); err != nil {
				return err
			}
			if len(runs) == 0 {
				fmt.Println("no task runs")
				return nil
			}
			for _, r := range runs {
				when := time.Unix(r.StartedAt, 0).Format("2006-01-02 15:04:05")
				exit := ""
				if r.ExitCode != nil {
					exit = fmt.Sprintf(" exit=%d", *r.ExitCode)
				}
				fmt.Printf("%-19s  %-16s  %-8s  %s%s\n", when, r.Task, r.Status, r.Reason, exit)
			}
			return nil
		},
	}
}

// runCmd is `nexus run` — start a durable, run-once host operation — plus its
// list/logs/rm subcommands. The owning project is inferred from the working
// directory server-side.
func runCmd(homeFlag *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run <name> -- <command>...",
		Short: "Start a durable, run-once host operation",
		Long: "Start a long-running one-shot operation on the host, supervised and logged.\n" +
			"It survives a nexus runtime restart and its outcome is recorded. The owning\n" +
			"project is inferred from the current directory (where its nexus.yaml lives).\n\n" +
			"Example:\n  nexus run backfill-2024 -- ./scripts/backfill.sh --year 2024",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dash := cmd.ArgsLenAtDash()
			if dash != 1 {
				return fmt.Errorf("usage: nexus run <name> -- <command>")
			}
			name := args[0]
			command := strings.Join(args[dash:], " ")
			if strings.TrimSpace(command) == "" {
				return fmt.Errorf("usage: nexus run <name> -- <command>")
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			payload, _ := json.Marshal(map[string]string{"name": name, "command": command, "cwd": cwd})
			code, body, err := daemonDoBody(*homeFlag, "POST", "/runs", bytes.NewReader(payload))
			if err != nil {
				return err
			}
			if code != http.StatusCreated {
				return fmt.Errorf("%s", strings.TrimSpace(string(body)))
			}
			fmt.Printf("started run %q\n", name)
			return nil
		},
	}
	cmd.AddCommand(runListCmd(homeFlag), runLogsCmd(homeFlag), runStopCmd(homeFlag), runRmCmd(homeFlag))
	return cmd
}

func runStopCmd(homeFlag *string) *cobra.Command {
	return &cobra.Command{
		Use:   "stop <name>",
		Short: "Signal a running managed run to stop (recorded as failed)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			code, body, err := daemonDo(*homeFlag, "POST", "/runs/"+args[0]+"/stop")
			if err != nil {
				return err
			}
			if code != http.StatusAccepted {
				return fmt.Errorf("%s", strings.TrimSpace(string(body)))
			}
			fmt.Printf("stopping run %q\n", args[0])
			return nil
		},
	}
}

func runListCmd(homeFlag *string) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List managed runs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			code, body, err := daemonDo(*homeFlag, "GET", "/runs")
			if err != nil {
				return err
			}
			if code != http.StatusOK {
				return fmt.Errorf("%s", strings.TrimSpace(string(body)))
			}
			var runs []struct {
				Name      string `json:"name"`
				Address   string `json:"address"`
				Status    string `json:"status"`
				ExitCode  *int   `json:"exit_code"`
				StartedAt int64  `json:"started_at"`
			}
			if err := json.Unmarshal(body, &runs); err != nil {
				return err
			}
			if len(runs) == 0 {
				fmt.Println("no runs")
				return nil
			}
			for _, r := range runs {
				when := time.Unix(r.StartedAt, 0).Format("2006-01-02 15:04:05")
				scope := r.Address
				if scope == "" {
					scope = "-"
				}
				exit := ""
				if r.ExitCode != nil {
					exit = fmt.Sprintf(" exit=%d", *r.ExitCode)
				}
				fmt.Printf("%-19s  %-20s  %-16s  %-8s%s\n", when, r.Name, scope, r.Status, exit)
			}
			return nil
		},
	}
}

func runLogsCmd(homeFlag *string) *cobra.Command {
	return &cobra.Command{
		Use:   "logs <name>",
		Short: "Show a managed run's captured output",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			code, body, err := daemonDo(*homeFlag, "GET", "/runs/"+args[0]+"/log")
			if err != nil {
				return err
			}
			if code != http.StatusOK {
				return fmt.Errorf("%s", strings.TrimSpace(string(body)))
			}
			os.Stdout.Write(body)
			return nil
		},
	}
}

func runRmCmd(homeFlag *string) *cobra.Command {
	return &cobra.Command{
		Use:   "rm <name>",
		Short: "Remove a finished managed run's record and log",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			code, body, err := daemonDo(*homeFlag, "DELETE", "/runs/"+args[0])
			if err != nil {
				return err
			}
			if code != http.StatusNoContent {
				return fmt.Errorf("%s", strings.TrimSpace(string(body)))
			}
			fmt.Printf("removed run %q\n", args[0])
			return nil
		},
	}
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the nexus version",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println(resolveVersion())
		},
	}
}

func daemonCmd(homeFlag *string) *cobra.Command {
	return &cobra.Command{
		Use:   "daemon",
		Short: "Start the nexus daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			homeDir, err := resolveHome(*homeFlag)
			if err != nil {
				return err
			}
			if err := home.Setup(homeDir); err != nil {
				return err
			}
			paths := home.NewPaths(homeDir)
			database, err := db.Open(paths.DB)
			if err != nil {
				return err
			}
			defer database.Close()

			fmt.Fprintf(os.Stderr, "nexus daemon starting (home=%s)\n", homeDir)

			sup := supervisor.NewRemoteSupervisor(paths.PMSocket)
			d := daemon.New(database, sup, paths)

			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			return d.Run(ctx)
		},
	}
}

func projectCmd(homeFlag *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Manage root projects",
	}
	cmd.AddCommand(projectAddCmd(homeFlag))
	cmd.AddCommand(projectRemoveCmd(homeFlag))
	cmd.AddCommand(projectStopCmd(homeFlag))
	cmd.AddCommand(projectStartCmd(homeFlag))
	cmd.AddCommand(projectListCmd(homeFlag))
	return cmd
}

// projectStopCmd pauses a project: its services (and nested sub-projects) stop.
// <name> may be a root project name (e.g. "retu") or a nested sub-project
// address (e.g. "retu/ingest") — either way it remains paused across daemon
// restarts until `project start`. Distinct from `remove`, which forgets a root
// project entirely; a nested address has no "remove" since it isn't tracked
// independently of its parent's config.
func projectStopCmd(homeFlag *string) *cobra.Command {
	return &cobra.Command{
		Use:   "stop <name|address>",
		Short: "Pause a project or nested sub-project (keeps it tracked)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return setProjectStopped(*homeFlag, args[0], true)
		},
	}
}

// projectStartCmd resumes a paused project or sub-project, which recovers from
// its last SHA.
func projectStartCmd(homeFlag *string) *cobra.Command {
	return &cobra.Command{
		Use:   "start <name|address>",
		Short: "Resume a paused project or nested sub-project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return setProjectStopped(*homeFlag, args[0], false)
		},
	}
}

// setProjectStopped pauses/resumes a project by address. A bare root project
// name uses the projects table (preserves ref/current_sha across the pause,
// exactly as before); anything else — a nested sub-project address containing
// "/" — uses the generic stopped_projects set, since sub-projects have no row
// of their own (they're discovered from their parent's config each deploy).
func setProjectStopped(homeFlag, address string, stopped bool) error {
	database, err := openDB(homeFlag)
	if err != nil {
		return err
	}
	defer database.Close()

	if _, gerr := database.GetProject(address); gerr == nil {
		if err := database.SetStopped(address, stopped); err != nil {
			return err
		}
	} else {
		if err := database.SetProjectPaused(address, stopped); err != nil {
			return err
		}
	}

	if stopped {
		fmt.Printf("stopped %q\n", address)
	} else {
		fmt.Printf("started %q\n", address)
	}
	notifyDaemon(homeFlag) // reconcile: stop or resume it now
	return nil
}

// serviceCmd groups start/stop for individual services — a single named process
// within a project's (or nested sub-project's) services: map. Unlike a project,
// a service has no independent recovery state: pausing it just stops the
// process and keeps it out of the spawn set until resumed; its spec is
// recovered from the owning project's cached config, no rebuild needed.
func serviceCmd(homeFlag *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Start/stop an individual service",
	}
	cmd.AddCommand(serviceStopCmd(homeFlag))
	cmd.AddCommand(serviceStartCmd(homeFlag))
	return cmd
}

func serviceStopCmd(homeFlag *string) *cobra.Command {
	return &cobra.Command{
		Use:   "stop <address> <service>",
		Short: "Pause one service (e.g. `nexus service stop retu/ingest ingest`)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return setServiceStopped(*homeFlag, args[0], args[1], true)
		},
	}
}

func serviceStartCmd(homeFlag *string) *cobra.Command {
	return &cobra.Command{
		Use:   "start <address> <service>",
		Short: "Resume a paused service",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return setServiceStopped(*homeFlag, args[0], args[1], false)
		},
	}
}

func setServiceStopped(homeFlag, address, service string, stopped bool) error {
	database, err := openDB(homeFlag)
	if err != nil {
		return err
	}
	defer database.Close()

	key := address + "/" + service
	if err := database.SetServicePaused(key, stopped); err != nil {
		return err
	}
	if stopped {
		fmt.Printf("stopped service %q\n", key)
	} else {
		fmt.Printf("started service %q\n", key)
	}
	notifyDaemon(homeFlag) // reconcile: stop or (re-)spawn it now
	return nil
}

func projectAddCmd(homeFlag *string) *cobra.Command {
	var refFlag string
	cmd := &cobra.Command{
		Use:   "add <spec-path[:name]>",
		Short: "Add a root project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			specPath, argRef, name, err := spec.ParseAddArg(args[0])
			if err != nil {
				return err
			}
			// A ref in the arg (spec@ref) wins over the --ref flag.
			ref := refFlag
			if argRef != "" {
				ref = argRef
			}

			// Discover the git repo within the spec path by walking up (Go-style):
			// a monorepo app can be given as github.com/org/repo/services/api, which
			// resolves to repo root github.com/org/repo + subdir services/api, and
			// the transport (https/ssh) is resolved here too. Reject an unresolvable
			// spec now rather than storing a project that can never deploy.
			root, subdir, rerr := git.ResolveRepoRoot(string(specPath))
			if rerr != nil {
				return fmt.Errorf("could not find a git repository for %q: %w\n"+
					"check the spec path, your network, and git credentials (e.g. an SSH key or a token in git's credential helper)", specPath, rerr)
			}

			database, err := openDB(*homeFlag)
			if err != nil {
				return err
			}
			defer database.Close()

			if err := database.AddProject(db.Project{
				Name:     name,
				SpecPath: root,
				Ref:      ref,
				Subdir:   subdir,
			}); err != nil {
				return err
			}
			if subdir != "" {
				fmt.Printf("added project %q  repo=%s  subdir=%s  ref=%s\n", name, root, subdir, ref)
			} else {
				fmt.Printf("added project %q  src=%s  ref=%s\n", name, root, ref)
			}
			notifyDaemon(*homeFlag) // ask a running daemon to start it now
			return nil
		},
	}
	cmd.Flags().StringVar(&refFlag, "ref", "main", "ref to track: a branch (main), tag (v15), latest, or a tag glob (web-v*)")
	return cmd
}

func projectRemoveCmd(homeFlag *string) *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a root project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			database, err := openDB(*homeFlag)
			if err != nil {
				return err
			}
			defer database.Close()

			if err := database.RemoveProject(args[0]); err != nil {
				return err
			}
			fmt.Printf("removed project %q\n", args[0])
			notifyDaemon(*homeFlag) // ask a running daemon to stop it now
			return nil
		},
	}
}

func projectListCmd(homeFlag *string) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List root projects",
		RunE: func(cmd *cobra.Command, args []string) error {
			database, err := openDB(*homeFlag)
			if err != nil {
				return err
			}
			defer database.Close()

			projects, err := database.ListProjects()
			if err != nil {
				return err
			}
			if len(projects) == 0 {
				fmt.Println("no projects")
				return nil
			}
			for _, p := range projects {
				sha := p.CurrentSHA
				if sha == "" {
					sha = "(not deployed)"
				}
				if p.Stopped {
					sha += "  (stopped)"
				}
				fmt.Printf("%-20s  %-45s  %-10s  %s\n", p.Name, p.SpecPath, p.Ref, sha)
			}
			return nil
		},
	}
}

func openDB(homeFlag string) (*db.DB, error) {
	homeDir, err := resolveHome(homeFlag)
	if err != nil {
		return nil, err
	}
	if err := home.Setup(homeDir); err != nil {
		return nil, err
	}
	return db.Open(home.NewPaths(homeDir).DB)
}

func resolveHome(flag string) (string, error) {
	if flag != "" {
		return flag, nil
	}
	return home.Dir()
}
