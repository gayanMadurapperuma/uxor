package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/gayanmadurapperuma/uxor/internal/index"
	"github.com/gayanmadurapperuma/uxor/internal/setup"
)

func newInitCmd() *cobra.Command {
	var dryRun, noIndex bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Set up Claude Code for this project: .mcp.json, hooks, CLAUDE.md, first index",
		Long: "Merge the uxor MCP server into .mcp.json, the prompt and edit hooks into\n" +
			".claude/settings.json, and a short usage note into CLAUDE.md, then index the\n" +
			"project. Existing settings are kept; re-running changes nothing. Claude Code asks\n" +
			"once to approve the project's MCP server.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := openMigrated(cmd, dryRun)
			if err != nil {
				return err
			}
			changes, err := setup.Plan(p.Root)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			for _, c := range changes {
				fmt.Fprintf(out, "%-22s %s\n", c.Path, c.Action)
			}
			if dryRun {
				fmt.Fprintln(out, "(dry run: nothing written)")
				return nil
			}
			if err := setup.Apply(p.Root, changes); err != nil {
				return err
			}
			if !noIndex {
				if err := runIndex(cmd, p, index.Options{}); err != nil {
					return err
				}
			}
			fmt.Fprintln(out, "\nnext: open `claude` here and approve the uxor MCP server (asked once)")
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show the planned changes without writing")
	cmd.Flags().BoolVar(&noIndex, "no-index", false, "skip the first index")
	return cmd
}
