package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
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
	root.AddCommand(versionCmd())
	return root
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

// projectStopCmd pauses a project: its services (and nested sub-projects) stop,
// but its row and current SHA stay in the DB, so it remains stopped across daemon
// restarts until `project start`. Distinct from `remove`, which forgets it.
func projectStopCmd(homeFlag *string) *cobra.Command {
	return &cobra.Command{
		Use:   "stop <name>",
		Short: "Pause a project for maintenance (keeps it tracked)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return setStopped(*homeFlag, args[0], true)
		},
	}
}

// projectStartCmd resumes a paused project, which recovers from its last SHA.
func projectStartCmd(homeFlag *string) *cobra.Command {
	return &cobra.Command{
		Use:   "start <name>",
		Short: "Resume a paused project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return setStopped(*homeFlag, args[0], false)
		},
	}
}

func setStopped(homeFlag, name string, stopped bool) error {
	database, err := openDB(homeFlag)
	if err != nil {
		return err
	}
	defer database.Close()

	if err := database.SetStopped(name, stopped); err != nil {
		return err
	}
	if stopped {
		fmt.Printf("stopped project %q\n", name)
	} else {
		fmt.Printf("started project %q\n", name)
	}
	notifyDaemon(homeFlag) // reconcile: stop or resume it now
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
