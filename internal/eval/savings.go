package eval

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/gayanmadurapperuma/uxor/internal/parse"
	"github.com/gayanmadurapperuma/uxor/internal/retrieve"
	"github.com/gayanmadurapperuma/uxor/internal/store"
)

// Savings scenarios, in tokens (chars/4) of discovery and reading only.
// Both grep agents search only indexed files and visit them in order of
// matching lines, stopping once every changed file is reached or after
// grepReadCap files:
//
//	A2 a typical agent, as Claude Code works: the grep file listing, then
//	   each matching line ±contextLines of every file visited (merged with
//	   the hunks' enclosing ranges in changed files, which it must read to
//	   edit), at most readLineCap lines per file; a changed file never
//	   reached is read in full, up to readLineCap lines;
//	A  the same visits reading whole files: an upper bound, kept for
//	   reference (D27 says why A2 replaced it);
//	B  perfect search: read only the changed files, in full;
//	C  uxor best case: the answer block, then each hunk's enclosing
//	   symbol ±contextLines;
//	D  uxor realistic, from the real answer's top answerTop files: on a
//	   hit (any changed file there), the block, the C ranges of the changed
//	   files found and a full read of those missed; on a miss, the block
//	   wasted plus A2.
const (
	grepReadCap  = 15
	grepListCap  = 100
	readLineCap  = 2000
	contextLines = 10
	answerTop    = 5
	// pathChunk bounds the pathspecs per git grep, well under ARG_MAX.
	pathChunk = 1000
)

// TaskSavings is one commit's scenario costs.
type TaskSavings struct {
	A2        int  `json:"a2_grep_ranges"`
	A         int  `json:"a_whole_files"`
	B         int  `json:"b_perfect"`
	C         int  `json:"c_best"`
	D         int  `json:"d_realistic"`
	GrepList  int  `json:"grep_list_tokens"` // the listing, part of A2
	GrepFiles int  `json:"grep_files_read"`
	Hit       bool `json:"hit"`       // a changed file in the answer's top 5
	AllFound  bool `json:"all_found"` // every changed file there
}

// Example is a worked example for the report.
type Example struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
	TaskSavings
}

// SavingsSummary aggregates TaskSavings.
type SavingsSummary struct {
	Tasks        int       `json:"tasks"`
	MeanA2       float64   `json:"mean_a2"`
	MeanA        float64   `json:"mean_a_whole_files"`
	MeanB        float64   `json:"mean_b"`
	MeanC        float64   `json:"mean_c"`
	MeanD        float64   `json:"mean_d"`
	SavingA2C    float64   `json:"saving_a2_to_c"` // 1 − ΣC/ΣA2
	SavingA2D    float64   `json:"saving_a2_to_d"` // 1 − ΣD/ΣA2
	SavingBD     float64   `json:"saving_b_to_d"`  // 1 − ΣD/ΣB: against perfect search
	HitRate      float64   `json:"hit_rate"`
	AllFoundRate float64   `json:"all_found_rate"`
	Over50       float64   `json:"share_saving_over_50pct"` // D ≤ A2/2
	Costlier     float64   `json:"share_costing_more"`      // D > A2
	Examples     []Example `json:"examples"`
}

// saver prices tasks against a worktree checked out at the parent.
type saver struct {
	root    string
	st      *store.Store
	indexed map[string]bool
	files   map[string][][]byte // per task: parent content as lines, read lazily
}

func (s *saver) task(ctx context.Context, c Commit, query string, truth, answer []string, blockTokens int) (*TaskSavings, error) {
	s.files = map[string][][]byte{}
	hunks, err := Hunks(ctx, s.root, c)
	if err != nil {
		return nil, err
	}
	encl := make(map[string][]LineRange, len(truth))
	for _, p := range truth {
		if encl[p], err = s.enclosing(ctx, p, hunks[p]); err != nil {
			return nil, err
		}
	}
	matches, err := s.grepLines(ctx, retrieve.Tokenize(query).Terms)
	if err != nil {
		return nil, err
	}
	order := hitOrder(matches)

	ts := &TaskSavings{}
	ts.A2, ts.GrepList, ts.GrepFiles = priceGrepAgent(order, matches, encl, s.lines)
	ts.A = priceWholeFiles(order, truth, s.lines)
	ranges := make(map[string]int, len(truth))
	ts.C = blockTokens
	for _, p := range truth {
		ts.B += linesTokens(s.lines(p), []LineRange{{1, len(s.lines(p))}}, 0)
		ranges[p] = linesTokens(s.lines(p), encl[p], 0)
		ts.C += ranges[p]
	}

	top := map[string]bool{}
	for _, p := range answer[:min(len(answer), answerTop)] {
		top[p] = true
	}
	ts.D, ts.AllFound = blockTokens, true
	missed := 0
	for _, p := range truth {
		if top[p] {
			ts.Hit = true
			ts.D += ranges[p]
		} else {
			ts.AllFound = false
			missed += fullRead(s.lines(p))
		}
	}
	if ts.Hit {
		ts.D += missed
	} else {
		ts.D = blockTokens + ts.A2
	}
	return ts, nil
}

// hitOrder sorts files by matching lines, most first, ties by path.
func hitOrder(matches map[string][]int) []string {
	order := make([]string, 0, len(matches))
	for p := range matches {
		order = append(order, p)
	}
	sort.Slice(order, func(i, j int) bool {
		if len(matches[order[i]]) != len(matches[order[j]]) {
			return len(matches[order[i]]) > len(matches[order[j]])
		}
		return order[i] < order[j]
	})
	return order
}

// priceGrepAgent prices scenario A2: the listing of the first grepListCap
// matching paths, then the files in order, each read as its matching lines
// ±contextLines merged with its truth ranges (truth maps every changed file
// to its hunks' enclosing ranges), until every changed file is reached or
// grepReadCap files are read; changed files never reached are read in
// full. It returns the total, the listing's share and the files read.
func priceGrepAgent(order []string, matches map[string][]int, truth map[string][]LineRange, lines func(string) [][]byte) (cost, list, read int) {
	chars := 0
	for _, p := range order[:min(len(order), grepListCap)] {
		chars += len(p) + 1
	}
	list = (chars + 3) / 4
	cost = list
	unreached := make(map[string]bool, len(truth))
	for p := range truth {
		unreached[p] = true
	}
	for _, p := range order {
		if len(unreached) == 0 || read == grepReadCap {
			break
		}
		want := append([]LineRange(nil), truth[p]...)
		for _, l := range matches[p] {
			want = append(want, LineRange{l - contextLines, l + contextLines})
		}
		cost += linesTokens(lines(p), want, readLineCap)
		read++
		delete(unreached, p)
	}
	for p := range unreached {
		cost += fullRead(lines(p))
	}
	return cost, list, read
}

// priceWholeFiles prices scenario A: the same visits as A2, each reading
// the whole file; changed files never reached are read whole as well.
func priceWholeFiles(order, truth []string, lines func(string) [][]byte) int {
	unreached := make(map[string]bool, len(truth))
	for _, p := range truth {
		unreached[p] = true
	}
	cost, read := 0, 0
	for _, p := range order {
		if len(unreached) == 0 || read == grepReadCap {
			break
		}
		cost += linesTokens(lines(p), []LineRange{{1, len(lines(p))}}, 0)
		read++
		delete(unreached, p)
	}
	for p := range unreached {
		cost += linesTokens(lines(p), []LineRange{{1, len(lines(p))}}, 0)
	}
	return cost
}

// fullRead is one Read of a whole file: at most readLineCap lines.
func fullRead(lines [][]byte) int {
	return linesTokens(lines, []LineRange{{1, readLineCap}}, readLineCap)
}

// linesTokens prices the merged ranges of lines, clamped to the file and to
// at most capLines lines (0 = no cap), in chars/4 rounded up.
func linesTokens(lines [][]byte, ranges []LineRange, capLines int) int {
	chars, n := 0, 0
	for _, r := range merge(ranges) {
		for l := max(r.Start, 1); l <= min(r.End, len(lines)); l++ {
			if capLines > 0 && n == capLines {
				return (chars + 3) / 4
			}
			chars += len(lines[l-1])
			n++
		}
	}
	return (chars + 3) / 4
}

// grepLines returns the matching line numbers per indexed file for any of
// terms, ignoring case. Only indexed files are searched: whole-repository
// greps can take minutes on large data files.
func (s *saver) grepLines(ctx context.Context, terms []string) (map[string][]int, error) {
	matches := map[string][]int{}
	if len(terms) == 0 || len(s.indexed) == 0 {
		return matches, nil
	}
	paths := make([]string, 0, len(s.indexed))
	for p := range s.indexed {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	base := []string{"-C", s.root, "-c", "core.quotePath=false", "grep", "-n", "-i", "-F", "--no-color"}
	for _, t := range terms {
		base = append(base, "-e", t)
	}
	for start := 0; start < len(paths); start += pathChunk {
		args := append(append(append([]string(nil), base...), "--"), paths[start:min(start+pathChunk, len(paths))]...)
		out, err := exec.CommandContext(ctx, "git", args...).Output()
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			continue // no matches in this chunk
		}
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(string(out), "\n") {
			if p, n, ok := s.grepLine(line); ok {
				matches[p] = append(matches[p], n)
			}
		}
	}
	return matches, nil
}

// grepLine parses "path:line:text". The path may itself contain colons, so
// it is the shortest prefix ending at a colon that names an indexed file
// and is followed by a line number.
func (s *saver) grepLine(line string) (string, int, bool) {
	for i := 0; i < len(line); i++ {
		if line[i] != ':' || !s.indexed[line[:i]] {
			continue
		}
		rest := line[i+1:]
		j := strings.IndexByte(rest, ':')
		if j < 0 {
			return "", 0, false
		}
		if n, err := strconv.Atoi(rest[:j]); err == nil {
			return line[:i], n, true
		}
	}
	return "", 0, false
}

// enclosing returns each hunk's smallest enclosing symbol, or the hunk
// itself outside any symbol, ±contextLines. A file with no hunks (a pure
// rename) is read for its first lines.
func (s *saver) enclosing(ctx context.Context, path string, hunks []LineRange) ([]LineRange, error) {
	syms, err := s.st.SymbolsInFile(ctx, path)
	if err != nil {
		return nil, err
	}
	if len(hunks) == 0 {
		hunks = []LineRange{{1, 1}}
	}
	var want []LineRange
	for _, h := range hunks {
		r := h
		best := -1
		for i, sym := range syms {
			if sym.Kind == parse.KindModule || sym.StartLine > h.Start || sym.EndLine < h.End {
				continue
			}
			if best < 0 || sym.EndLine-sym.StartLine < syms[best].EndLine-syms[best].StartLine {
				best = i
			}
		}
		if best >= 0 {
			r = LineRange{syms[best].StartLine, syms[best].EndLine}
		}
		want = append(want, LineRange{r.Start - contextLines, r.End + contextLines})
	}
	return want, nil
}

// merge sorts ranges and joins overlapping or adjacent ones.
func merge(rs []LineRange) []LineRange {
	rs = append([]LineRange(nil), rs...)
	sort.Slice(rs, func(i, j int) bool { return rs[i].Start < rs[j].Start })
	var out []LineRange
	for _, r := range rs {
		if n := len(out); n > 0 && r.Start <= out[n-1].End+1 {
			out[n-1].End = max(out[n-1].End, r.End)
			continue
		}
		out = append(out, r)
	}
	return out
}

// lines returns a file's content at the parent, split after each newline.
func (s *saver) lines(path string) [][]byte {
	ls, ok := s.files[path]
	if !ok {
		b, _ := os.ReadFile(filepath.Join(s.root, filepath.FromSlash(path)))
		if len(b) > 0 {
			ls = bytes.SplitAfter(b, []byte("\n"))
			if len(ls[len(ls)-1]) == 0 {
				ls = ls[:len(ls)-1] // the empty piece after a final newline
			}
		}
		s.files[path] = ls
	}
	return ls
}

func summarizeSavings(tasks []Task) *SavingsSummary {
	sum := &SavingsSummary{Examples: []Example{}}
	var a2, a, b, c, d, hits, all, over, more float64
	var priced []Task
	for _, t := range tasks {
		ts := t.Savings
		if ts == nil {
			continue
		}
		priced = append(priced, t)
		a2, a, b = a2+float64(ts.A2), a+float64(ts.A), b+float64(ts.B)
		c, d = c+float64(ts.C), d+float64(ts.D)
		hits, all = hits+b2f(ts.Hit), all+b2f(ts.AllFound)
		over += b2f(2*ts.D <= ts.A2)
		more += b2f(ts.D > ts.A2)
	}
	n := float64(len(priced))
	sum.Tasks = len(priced)
	if n == 0 {
		return sum
	}
	sum.MeanA2, sum.MeanA, sum.MeanB = round3(a2/n), round3(a/n), round3(b/n)
	sum.MeanC, sum.MeanD = round3(c/n), round3(d/n)
	if a2 > 0 {
		sum.SavingA2C, sum.SavingA2D = round3(1-c/a2), round3(1-d/a2)
	}
	if b > 0 {
		sum.SavingBD = round3(1 - d/b)
	}
	sum.HitRate, sum.AllFoundRate = round3(hits/n), round3(all/n)
	sum.Over50, sum.Costlier = round3(over/n), round3(more/n)

	// Worked examples across the spread: worst, quartiles, median, best A2→D.
	saving := func(t Task) float64 {
		if t.Savings.A2 == 0 {
			return 0
		}
		return 1 - float64(t.Savings.D)/float64(t.Savings.A2)
	}
	sort.SliceStable(priced, func(i, j int) bool { return saving(priced[i]) < saving(priced[j]) })
	seen := map[int]bool{}
	for _, q := range []float64{0, 0.25, 0.5, 0.75, 1} {
		i := int(q * float64(len(priced)-1))
		if seen[i] {
			continue
		}
		seen[i] = true
		t := priced[i]
		sum.Examples = append(sum.Examples, Example{SHA: short(t.SHA), Subject: t.Subject, TaskSavings: *t.Savings})
	}
	return sum
}
