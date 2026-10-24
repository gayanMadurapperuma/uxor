package retrieve

import (
	"fmt"
	"sort"
	"strings"
)

// charsPerToken is the token estimate used for budgets.
const charsPerToken = 4

// File is one result file and its chosen symbols in line order.
type File struct {
	Path    string   `json:"path"`
	Score   float64  `json:"score"`
	Symbols []Symbol `json:"symbols"`
}

// Symbol is one packed line.
type Symbol struct {
	Name          string  `json:"name"`
	QualifiedName string  `json:"qualified_name"`
	Kind          string  `json:"kind"`
	StartLine     int     `json:"start_line"`
	EndLine       int     `json:"end_line"`
	Signature     string  `json:"signature"`
	Score         float64 `json:"score"`
	Why           string  `json:"why"`
	Depth         int     `json:"-"` // indentation: chosen ancestors
}

// Tokens estimates the tokens of s.
func Tokens(s string) int { return (len(s) + charsPerToken - 1) / charsPerToken }

// symbol converts a candidate for output.
func symbol(c *candidate, depth int) Symbol {
	return Symbol{
		Name: c.Name, QualifiedName: c.QualifiedName, Kind: c.Kind.String(),
		StartLine: c.StartLine, EndLine: c.EndLine, Signature: c.Signature,
		Score: round(c.Score), Why: c.Why, Depth: depth,
	}
}

// pack chooses candidates in score order while the rendered text stays
// within budget tokens and at most maxFiles files (0 = no limit), down to
// t.MinRelScore of the best, then groups them by file. The best candidate is
// always included. Lines that do not fit are skipped so a shorter,
// lower-ranked one can still be used.
func pack(cands []candidate, budget, maxFiles int, t *Tuning) ([]File, string) {
	limit := budget * charsPerToken
	chosen := map[int64]*candidate{}
	files := map[string]bool{}
	used := 0
	// depth counts chosen ancestors, which indent a line by two spaces each.
	var depth func(c *candidate) int
	depth = func(c *candidate) int {
		if p := chosen[c.Parent]; p != nil && c.Parent != 0 {
			return depth(p) + 1
		}
		return 0
	}
	// shifted counts chosen descendants of id, whose indentation grows by
	// one level when id is chosen.
	var shifted func(id int64) int
	shifted = func(id int64) int {
		n := 0
		for _, c := range chosen {
			if c.Parent == id {
				n += 1 + shifted(c.ID)
			}
		}
		return n
	}
	for i := range cands {
		c := &cands[i]
		if c.Score < t.MinRelScore*cands[0].Score {
			break // cands are sorted by score
		}
		if !files[c.Path] && maxFiles > 0 && len(files) >= maxFiles {
			continue
		}
		cost := len(Render(c.Path, symbol(c, depth(c)), false)) + 1 + 2*shifted(c.ID)
		if used+cost > limit && len(chosen) > 0 {
			continue
		}
		chosen[c.ID] = c
		files[c.Path] = true
		used += cost
	}

	byFile := map[string][]*candidate{}
	for _, c := range chosen {
		byFile[c.Path] = append(byFile[c.Path], c)
	}
	out := make([]File, 0, len(byFile))
	for path, cs := range byFile {
		f := File{Path: path, Score: fileScore(cs, t)}
		sort.Slice(cs, func(i, j int) bool {
			if cs[i].StartLine != cs[j].StartLine {
				return cs[i].StartLine < cs[j].StartLine
			}
			if cs[i].EndLine != cs[j].EndLine {
				return cs[i].EndLine > cs[j].EndLine // enclosing symbol first
			}
			return cs[i].ID < cs[j].ID
		})
		for _, c := range cs {
			f.Symbols = append(f.Symbols, symbol(c, depth(c)))
		}
		out = append(out, f)
	}
	sortFiles(out)

	var b strings.Builder
	for _, f := range out {
		for _, s := range f.Symbols {
			b.WriteString(Render(f.Path, s, false))
			b.WriteByte('\n')
		}
	}
	return out, b.String()
}

// fileScore is a file's best symbol score plus t.RestWeight × its next
// t.RestCount, so several hits lift a file but many weak ones cannot
// outrank one strong one. It sorts cs by score.
func fileScore(cs []*candidate, t *Tuning) float64 {
	sort.Slice(cs, func(i, j int) bool { return cs[i].Score > cs[j].Score })
	s := cs[0].Score
	for _, c := range cs[1:min(len(cs), 1+t.RestCount)] {
		s += t.RestWeight * c.Score
	}
	return round(s)
}

// sortFiles orders files best first, ties by path.
func sortFiles(fs []File) {
	sort.Slice(fs, func(i, j int) bool {
		if fs[i].Score != fs[j].Score {
			return fs[i].Score > fs[j].Score
		}
		return fs[i].Path < fs[j].Path
	})
}

// Render formats one packed line, "path:start-end  kind signature",
// indented by depth and optionally followed by its why.
func Render(path string, s Symbol, why bool) string {
	sig := s.Signature
	if sig == "" {
		sig = s.Name
	}
	l := fmt.Sprintf("%s%s:%d-%d  ", strings.Repeat("  ", s.Depth), path, s.StartLine, s.EndLine)
	if showKind(s.Kind, sig) {
		l += s.Kind + " "
	}
	l += sig
	if why {
		l += "  # " + s.Why
	}
	return l
}

// declWords start signatures that already say what the symbol is.
var declWords = map[string]bool{
	"func": true, "def": true, "class": true, "type": true, "interface": true,
	"var": true, "const": true, "let": true, "function": true, "enum": true,
}

// showKind reports whether the kind word adds information to the
// signature: "class class Cart" would not. Tests always show it.
func showKind(kind, sig string) bool {
	if kind == "test" {
		return true
	}
	first, _, _ := strings.Cut(sig, " ")
	first, _, _ = strings.Cut(first, "(")
	return first != kind && !declWords[first]
}

func round(f float64) float64 { return float64(int64(f*1000+0.5)) / 1000 }
