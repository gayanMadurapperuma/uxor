// Command codegraph indexes a codebase into a local code graph so Claude
// Code can find relevant code without reading whole files.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/spf13/cobra"

	"github.com/gayanmadurapperuma/codegraph/internal/config"
)

var rootFlag string

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := newRootCmd().ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "codegraph:", err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "codegraph",
		Short:         "Local code graph for token-efficient code retrieval",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&rootFlag, "root", "", "project directory (default: nearest .codegraph.yaml or .git above the current directory)")
	root.AddCommand(newIndexCmd(), newStatsCmd(), newCleanCmd(), newDisableCmd(true), newDisableCmd(false),
		newForgetCmd(), newUnforgetCmd(), newQueryCmd(), newServeCmd(), newHookCmd(), newInitCmd())
	return root
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
