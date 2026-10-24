package main

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"

	"github.com/gayanmadurapperuma/uxor/internal/eval"
	"github.com/gayanmadurapperuma/uxor/internal/retrieve"
)

func newEvalCmd() *cobra.Command {
	var (
		opt     eval.Options
		tunings []string
		asJSON  bool
		verbose bool
	)
	cmd := &cobra.Command{
		Use:   "eval",
		Short: "Measure retrieval on this repository's history (commit subject → changed files)",
		Long: "Measure retrieval on recent non-merge commits: each subject is a query and the\n" +
			"indexed files it changed are the answer. By default the existing index (at HEAD)\n" +
			"is used, which already contains the answers. --checkout-parent indexes each\n" +
			"commit's parent in a temporary git worktree instead: slower, but honest.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := openProject()
			if err != nil {
				return err
			}
			for i, path := range tunings {
				t, err := retrieve.LoadTuning(path)
				if err != nil {
					return err
				}
				if i == 0 {
					opt.Tuning = t
				} else {
					opt.Variants = append(opt.Variants, eval.Variant{Name: filepath.Base(path), Tuning: t})
				}
			}
			if verbose {
				opt.Progress = cmd.ErrOrStderr()
			}
			rep, err := eval.Run(cmd.Context(), p, opt)
			if err != nil {
				return err
			}
			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(rep)
			}
			printReport(cmd.OutOrStdout(), rep)
			return nil
		},
	}
	f := cmd.Flags()
	f.IntVar(&opt.Commits, "commits", 100, "commits to evaluate")
	f.IntVar(&opt.MaxChanged, "max-files", 20, "skip commits changing more indexable files")
	f.BoolVar(&opt.CheckoutParent, "checkout-parent", false, "index each commit's parent in a temporary worktree (honest, slower)")
	f.BoolVar(&opt.Savings, "savings", false, "estimate token savings (needs --checkout-parent)")
	f.BoolVar(&opt.Body, "body", false, "add the commit body to the query")
	f.StringArrayVar(&tunings, "tuning", nil, "JSON file overriding ranking constants (internal/retrieve/tuning.go); repeat to compare\nvariants against the same indexes, the first one being the main run")
	f.BoolVar(&asJSON, "json", false, "print the full report, with every task, as JSON")
	f.BoolVarP(&verbose, "verbose", "v", false, "print one line per commit on stderr")
	return cmd
}

func printReport(w io.Writer, r *eval.Report) {
	fmt.Fprintf(w, "%s  (%s; %d commits considered, %d evaluated)\n", r.Root, r.Mode, r.Considered, r.All.Tasks)
	keys := make([]string, 0, len(r.Skipped))
	for k := range r.Skipped {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(w, "  skipped %d: %s\n", r.Skipped[k], k)
	}
	fmt.Fprintf(w, "\n%-8s %5s %7s %7s %6s %8s %9s %11s %12s\n", "split", "tasks", "r@5", "r@10", "mrr", "hook hit", "query hit", "hook tokens", "hook p95 ms")
	printRows(w, "", r.All, r.Tune, r.Holdout)
	for _, v := range r.Variants {
		printRows(w, v.Name, v.All, v.Tune, v.Holdout)
	}
	if sv := r.Savings; sv != nil && sv.Tasks > 0 {
		fmt.Fprintf(w, "\ntoken savings over %d tasks (mean tokens per task)\n", sv.Tasks)
		fmt.Fprintf(w, "  A2 grep + read ranges %8.0f\n  B  perfect search     %8.0f\n  C  uxor best     %8.0f   saving A2→C %5.1f%%\n  D  uxor real     %8.0f   saving A2→D %5.1f%%, B→D %5.1f%%\n",
			sv.MeanA2, sv.MeanB, sv.MeanC, 100*sv.SavingA2C, sv.MeanD, 100*sv.SavingA2D, 100*sv.SavingBD)
		fmt.Fprintf(w, "  (A, the same visits reading whole files: %.0f; an upper bound, not a baseline)\n", sv.MeanA)
		fmt.Fprintf(w, "  a changed file in the top 5: %.1f%% (all of them: %.1f%%); tasks saving >50%% vs A2: %.1f%%; costing more than A2: %.1f%%\n",
			100*sv.HitRate, 100*sv.AllFoundRate, 100*sv.Over50, 100*sv.Costlier)
		fmt.Fprintln(w, "  examples (worst → best):")
		for _, e := range sv.Examples {
			fmt.Fprintf(w, "    %s  A2 %6d  B %6d  C %6d  D %6d  %s\n", e.SHA, e.A2, e.B, e.C, e.D, e.Subject)
		}
	}
	fmt.Fprintln(w)
	for _, c := range r.Caveats {
		fmt.Fprintln(w, "note:", c)
	}
}

// printRows prints the all/tune/holdout rows, labelled for a variant.
func printRows(w io.Writer, label string, all, tune, holdout eval.Summary) {
	for _, row := range []struct {
		name string
		s    eval.Summary
	}{{"all", all}, {"tune", tune}, {"holdout", holdout}} {
		s := row.s
		fmt.Fprintf(w, "%-8s %5d %7.3f %7.3f %6.3f %8.3f %9.3f %11.0f %12.1f  %s\n",
			row.name, s.Tasks, s.Recall5, s.Recall10, s.MRR, s.HookHit, s.QueryHit, s.HookTokens, s.HookP95MS, label)
	}
}
