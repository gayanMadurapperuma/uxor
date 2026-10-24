package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/gayanmadurapperuma/uxor/internal/index"
	"github.com/gayanmadurapperuma/uxor/internal/store"
)

// Commands that remove or pause context for the current project only.

func newCleanCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "clean",
		Short: "Delete this project's index and local state (.uxor/)",
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := openProject()
			if err != nil {
				return err
			}
			if _, err := os.Stat(p.DataDir); errors.Is(err, os.ErrNotExist) {
				fmt.Fprintf(cmd.OutOrStdout(), "nothing to clean: %s does not exist\n", p.DataDir)
				return nil
			}
			if !yes {
				fmt.Fprintf(cmd.ErrOrStderr(), "Delete %s (index, WAL files, forget list, disabled marker)? [y/N] ", p.DataDir)
				line, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
				if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
					return errors.New("aborted")
				}
			}
			if err := p.Clean(); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed %s\n", p.DataDir)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}

func newDisableCmd(disable bool) *cobra.Command {
	use, short, msg := "enable", "Resume context injection for this project", "enabled"
	if disable {
		use, short, msg = "disable", "Pause context injection for this project (keeps the index)", "disabled"
	}
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := openProject()
			if err != nil {
				return err
			}
			if err := p.SetDisabled(disable); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "uxor %s for %s\n", msg, p.Root)
			return nil
		},
	}
}

func newForgetCmd() *cobra.Command {
	var list bool
	cmd := &cobra.Command{
		Use:   "forget <glob>...",
		Short: "Remove matching paths from the index and keep them out (local, not committed)",
		Long: "Remove files matching the globs from this project's index and add the globs to\n" +
			".uxor/forget.txt so re-indexing never brings them back. Globs are\n" +
			"root-relative doublestar patterns; a directory name matches everything below it.\n" +
			"To share an exclude with your team, add it to .uxor.yaml instead.",
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := openProject()
			if err != nil {
				return err
			}
			globs, err := p.ForgetGlobs()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if list || len(args) == 0 {
				if len(globs) == 0 {
					fmt.Fprintln(out, "forget list is empty")
				}
				for _, g := range globs {
					fmt.Fprintln(out, g)
				}
				return nil
			}
			if err := p.SetForgetGlobs(append(globs, args...)); err != nil {
				return err
			}
			n, err := withWriteStore(cmd, p.DBPath, func(st *store.Store) (int, error) {
				return index.Forget(cmd.Context(), p, st, args)
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "forgot %d indexed files; added %s to .uxor/forget.txt\n", n, strings.Join(args, ", "))
			return nil
		},
	}
	cmd.Flags().BoolVar(&list, "list", false, "list forgotten globs")
	return cmd
}

func newUnforgetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unforget <glob>...",
		Short: "Remove globs from the forget list (run `uxor index` to re-add files)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := openProject()
			if err != nil {
				return err
			}
			globs, err := p.ForgetGlobs()
			if err != nil {
				return err
			}
			kept := slices.DeleteFunc(globs, func(g string) bool { return slices.Contains(args, g) })
			if err := p.SetForgetGlobs(kept); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "updated forget list; run `uxor index` to re-index those paths")
			return nil
		},
	}
}

// withWriteStore opens the index for writing if it exists.
func withWriteStore(cmd *cobra.Command, dbPath string, fn func(*store.Store) (int, error)) (int, error) {
	if _, err := os.Stat(dbPath); errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	st, err := store.Open(cmd.Context(), dbPath, store.Options{})
	if err != nil {
		return 0, err
	}
	defer st.Close()
	return fn(st)
}
