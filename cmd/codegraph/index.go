package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/gayanmadurapperuma/codegraph/internal/config"
	"github.com/gayanmadurapperuma/codegraph/internal/index"
	"github.com/gayanmadurapperuma/codegraph/internal/store"
)

func newIndexCmd() *cobra.Command {
	var opt index.Options
	cmd := &cobra.Command{
		Use:   "index [paths...]",
		Short: "Index the project (incremental by content hash)",
		Long: "Index the project. Without paths, the whole project is scanned and files that\n" +
			"disappeared are removed. With paths, only those files are (re)indexed.",
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := openProject()
			if err != nil {
				return err
			}
			opt.Paths = args
			return runIndex(cmd, p, opt)
		},
	}
	cmd.Flags().BoolVar(&opt.Full, "full", false, "drop the index and rebuild from scratch")
	cmd.Flags().IntVar(&opt.Workers, "workers", 0, "parse workers (default: number of CPUs)")
	return cmd
}

// runIndex indexes p and prints a summary; shared by index and init.
func runIndex(cmd *cobra.Command, p *config.Project, opt index.Options) error {
	if err := p.EnsureDataDir(); err != nil {
		return err
	}
	if changed, err := p.EnsureGitignore(); err != nil {
		return err
	} else if changed {
		fmt.Fprintln(cmd.ErrOrStderr(), "added .codegraph/ to .gitignore")
	}
	st, err := store.Open(cmd.Context(), p.DBPath, store.Options{})
	if err != nil {
		return err
	}
	defer st.Close()
	res, err := index.Run(cmd.Context(), p, st, opt)
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(),
		"%s: %d files scanned, %d parsed, %d unchanged, %d skipped, %d removed, %d failed in %s\n",
		p.Root, res.Scanned, res.Parsed, res.Unchanged, res.Skipped, res.Removed, res.Failed,
		res.Elapsed.Round(1e6))
	if rr := res.Resolve; rr != nil && rr.Files > 0 {
		mode := "scoped"
		if rr.Full {
			mode = "full"
		}
		fmt.Fprintf(cmd.OutOrStdout(), "resolve (%s): %d files, %d/%d refs resolved (%s) → %d edges in %s",
			mode, rr.Files, rr.Resolved, rr.Refs, percent(rr.Resolved, rr.Refs), rr.Edges, rr.Elapsed.Round(1e6))
		if rr.PageRankNodes > 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "; pagerank %d nodes in %s", rr.PageRankNodes, rr.PageRankTime.Round(1e6))
		}
		fmt.Fprintln(cmd.OutOrStdout())
	}
	for _, e := range res.Errors {
		fmt.Fprintln(cmd.ErrOrStderr(), "  error:", e)
	}
	return nil
}

func percent(n, d int) string {
	if d == 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f%%", 100*float64(n)/float64(d))
}
