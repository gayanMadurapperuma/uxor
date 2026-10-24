package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/gayanmadurapperuma/uxor/internal/retrieve"
	"github.com/gayanmadurapperuma/uxor/internal/store"
)

func newQueryCmd() *cobra.Command {
	var (
		budget int
		asJSON bool
		why    bool
	)
	cmd := &cobra.Command{
		Use:   `query "<text>"`,
		Short: "Show the files and line ranges most relevant to a prompt",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := openProject()
			if err != nil {
				return err
			}
			if p.Disabled() {
				fmt.Fprintln(cmd.ErrOrStderr(), "note: uxor is disabled for this project; hooks and MCP tools return nothing (`uxor enable` to resume)")
			}
			st, err := store.Open(cmd.Context(), p.DBPath, store.Options{ReadOnly: true})
			if err != nil {
				return err
			}
			defer st.Close()
			res, err := retrieve.FindRelevant(cmd.Context(), st, p.Config, strings.Join(args, " "), retrieve.Options{Budget: budget})
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(res)
			}
			if len(res.Files) == 0 {
				fmt.Fprintln(out, "no matches")
				return nil
			}
			for _, f := range res.Files {
				for _, s := range f.Symbols {
					fmt.Fprintln(out, retrieve.Render(f.Path, s, why))
				}
			}
			if why {
				fmt.Fprintf(out, "\n%d/%d tokens · %.1f ms\n", res.TokensUsed, res.Budget, res.Timings["total"])
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&budget, "budget", 0, "token budget for the output (default budgets.query_tokens)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON with scores, reasons and timings")
	cmd.Flags().BoolVar(&why, "why", false, "append the reason for each line and a summary")
	return cmd
}
