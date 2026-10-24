// Command uxor indexes a codebase into a local code graph so Claude
// Code can find relevant code without reading whole files.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/gayanmadurapperuma/uxor/internal/config"
)

var rootFlag string

func main() {
	// SIGTERM too, so a killed eval still removes its temporary worktree.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := newRootCmd().ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "uxor:", err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "uxor",
		Short:         "Local code graph for token-efficient code retrieval",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&rootFlag, "root", "", "project directory (default: nearest .uxor.yaml or .git above the current directory)")
	root.AddCommand(newIndexCmd(), newStatsCmd(), newCleanCmd(), newDisableCmd(true), newDisableCmd(false),
		newForgetCmd(), newUnforgetCmd(), newQueryCmd(), newServeCmd(), newHookCmd(), newInitCmd(), newEvalCmd())
	return root
}

// openMigrated resolves the project after moving any pre-rename state
// (.codegraph/, .codegraph.yaml) to the uxor names, printing what moved.
// With dry set it only reports (D36).
func openMigrated(cmd *cobra.Command, dry bool) (*config.Project, error) {
	p, err := openProject()
	if err != nil {
		return nil, err
	}
	notes, err := config.MigrateLegacy(p.Root, dry)
	for _, n := range notes {
		verb := "migrated"
		if dry {
			verb = "would migrate"
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "%s %s (renamed from codegraph)\n", verb, n)
	}
	if err != nil || len(notes) == 0 || dry {
		return p, err
	}
	return config.Open(p.Root)
}

// openProject resolves the single project this invocation operates on.
func openProject() (*config.Project, error) {
	start := rootFlag
	if start == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		start = wd
	}
	return config.Open(start)
}
