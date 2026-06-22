package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/rdkal/nexus/internal/daemon"
	"github.com/rdkal/nexus/internal/db"
	"github.com/rdkal/nexus/internal/home"
	"github.com/rdkal/nexus/internal/spec"
	"github.com/rdkal/nexus/internal/supervisor"
)

func main() {
	if err := rootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	var homeFlag string

	root := &cobra.Command{
		Use:   "nexus",
		Short: "Git-native process manager",
	}
	root.PersistentFlags().StringVar(&homeFlag, "home", "", "override NEXUS_HOME")

	root.AddCommand(daemonCmd(&homeFlag))
	root.AddCommand(projectCmd(&homeFlag))
	return root
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
	cmd.AddCommand(projectListCmd(homeFlag))
	return cmd
}

func projectAddCmd(homeFlag *string) *cobra.Command {
	var refFlag string
	cmd := &cobra.Command{
		Use:   "add <spec-path[:name]>",
		Short: "Add a root project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			specPath, name, err := spec.ParseAddArg(args[0])
			if err != nil {
				return err
			}
			database, err := openDB(*homeFlag)
			if err != nil {
				return err
			}
			defer database.Close()

			if err := database.AddProject(db.Project{
				Name:     name,
				SpecPath: string(specPath),
				Ref:      refFlag,
			}); err != nil {
				return err
			}
			fmt.Printf("added project %q  src=%s  ref=%s\n", name, specPath, refFlag)
			return nil
		},
	}
	cmd.Flags().StringVar(&refFlag, "ref", "@main", "ref to track (@main, @v15, @latest)")
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
