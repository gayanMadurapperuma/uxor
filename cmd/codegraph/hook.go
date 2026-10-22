package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/gayanmadurapperuma/codegraph/internal/hook"
)

func newHookCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hook",
		Short: "Claude Code hooks (read the hook JSON on stdin; always exit 0)",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "prompt",
		Short: "UserPromptSubmit: add likely relevant locations to the prompt's context",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			debug(cmd, hook.Prompt(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout()))
			return nil
		},
	}, &cobra.Command{
		Use:   "edited",
		Short: "PostToolUse (Edit|Write): re-index the edited file",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			n, err := hook.Edited(cmd.Context(), cmd.InOrStdin())
			if err == nil {
				debugf(cmd, "re-indexed %d path(s)", n)
			}
			debug(cmd, err)
			return nil
		},
	})
	return cmd
}

// debug reports a hook outcome on stderr, only with CODEGRAPH_DEBUG set:
// hooks never fail and never print anything else.
func debug(cmd *cobra.Command, err error) {
	if err != nil {
		debugf(cmd, "%v", err)
	}
}

func debugf(cmd *cobra.Command, format string, args ...any) {
	if os.Getenv("CODEGRAPH_DEBUG") != "" {
		fmt.Fprintf(cmd.ErrOrStderr(), "codegraph hook: "+format+"\n", args...)
	}
}
