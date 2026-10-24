package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/gayanmadurapperuma/uxor/internal/store"
)

func newStatsCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Show index counts, size, state and top hubs",
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := openProject()
			if err != nil {
				return err
			}
			st, err := store.Open(cmd.Context(), p.DBPath, store.Options{ReadOnly: true})
			if err != nil {
				return err
			}
			defer st.Close()
			s, err := st.Stats(cmd.Context())
			if err != nil {
				return err
			}
			forget, err := p.ForgetGlobs()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(struct {
					Root     string   `json:"root"`
					DB       string   `json:"db"`
					Disabled bool     `json:"disabled"`
					Forget   []string `json:"forget"`
					*store.Stats
				}{p.Root, p.DBPath, p.Disabled(), forget, s})
			}
			state := "enabled"
			if p.Disabled() {
				state = "DISABLED (hooks and MCP tools return nothing; `uxor enable` to resume)"
			}
			fmt.Fprintf(out, "root:      %s\n", p.Root)
			fmt.Fprintf(out, "db:        %s (%s)\n", p.DBPath, humanBytes(s.DBBytes))
			fmt.Fprintf(out, "state:     %s\n", state)
			fmt.Fprintf(out, "indexed:   %s\n", orDash(s.LastIndexed))
			if len(forget) > 0 {
				fmt.Fprintf(out, "forgotten: %s\n", strings.Join(forget, ", "))
			}
			fmt.Fprintf(out, "\nfiles %d · symbols %d · edges %d · refs %d\n", s.Files, s.Symbols, s.Edges, s.Refs)
			for _, l := range s.ByLang {
				fmt.Fprintf(out, "  %-11s %6d files %8d loc\n", l.Lang, l.Files, l.LOC)
			}
			fmt.Fprintf(out, "symbols by kind: %s\n", formatCounts(s.ByKind))
			fmt.Fprintf(out, "edges by type:   %s\n", formatCounts(s.ByEdge))
			if len(s.Resolution) > 0 {
				parts := make([]string, len(s.Resolution))
				for i, r := range s.Resolution {
					parts[i] = fmt.Sprintf("%s %d/%d (%s)", r.Type, r.Resolved, r.Total, percent(r.Resolved, r.Total))
				}
				fmt.Fprintf(out, "refs resolved:   %s\n", strings.Join(parts, ", "))
			}
			if len(s.Hubs) > 0 {
				if s.HubsResolved {
					fmt.Fprintln(out, "\ntop hubs (callers + subclasses):")
				} else {
					fmt.Fprintln(out, "\nmost-called names (unresolved until call resolution):")
				}
				for _, h := range s.Hubs {
					loc := ""
					if h.Path != "" {
						loc = "  " + h.Path
					}
					fmt.Fprintf(out, "  %6d  %s%s\n", h.FanIn, h.Name, loc)
				}
			}
			if len(s.TopPageRank) > 0 {
				stale := ""
				if s.PageRankStale {
					stale = " (stale; next `uxor index` recomputes)"
				}
				fmt.Fprintf(out, "\ntop PageRank%s:\n", stale)
				for _, h := range s.TopPageRank {
					fmt.Fprintf(out, "  %6.1f  %s  %s\n", h.Score, h.Name, h.Path)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "output JSON")
	return cmd
}

func formatCounts(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return m[keys[i]] > m[keys[j]] || m[keys[i]] == m[keys[j]] && keys[i] < keys[j] })
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s %d", k, m[k])
	}
	return orDash(strings.Join(parts, ", "))
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGT"[exp])
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
