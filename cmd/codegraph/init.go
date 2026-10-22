package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/gayanmadurapperuma/codegraph/internal/index"
	"github.com/gayanmadurapperuma/codegraph/internal/setup"
)

func newInitCmd() *cobra.Command {
	var dryRun, noIndex bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Set up Claude Code for this project: .mcp.json, hooks, CLAUDE.md, first index",
		Long: "Merge the codegraph MCP server into .mcp.json, the prompt and edit hooks into\n" +
			".claude/settings.json, and a short usage note into CLAUDE.md, then index the\n" +
			"project. Existing settings are kept; re-running changes nothing. Claude Code asks\n" +
			"once to approve the project's MCP server.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := openProject()
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
			if noIndex {
				return nil
			}
			return runIndex(cmd, p, index.Options{})
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show the planned changes without writing")
	cmd.Flags().BoolVar(&noIndex, "no-index", false, "skip the first index")
	return cmd
}
