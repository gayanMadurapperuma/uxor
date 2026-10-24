package eval

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Commit is one non-merge commit with its first parent.
type Commit struct {
	SHA     string `json:"sha"`
	Parent  string `json:"-"`
	Subject string `json:"subject"`
	Body    string `json:"-"`
}

// Change is one changed path of a commit. Old is the path at the parent
// ("" when added), New the path at the commit ("" when deleted).
type Change struct {
	Status byte // A, M, D, R, C or T
	Old    string
	New    string
}

// LineRange is an inclusive, 1-based line range.
type LineRange struct{ Start, End int }

// git runs git -C dir with quoting off, so paths come back as written.
func git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir, "-c", "core.quotePath=false"}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// Commits returns up to limit non-merge commits reachable from HEAD,
// newest first. Root commits are skipped: they have no parent to search.
func Commits(ctx context.Context, root string, limit int) ([]Commit, error) {
	out, err := git(ctx, root, "log", "--no-merges", "-n", strconv.Itoa(limit), "--format=%H%x00%P%x00%s%x00%b%x1e")
	if err != nil {
		return nil, err
	}
	var cs []Commit
	for _, rec := range bytes.Split(out, []byte{0x1e}) {
		f := strings.SplitN(strings.TrimLeft(string(rec), "\n"), "\x00", 4)
		if len(f) < 4 || f[0] == "" {
			continue
		}
		parents := strings.Fields(f[1])
		if len(parents) == 0 {
			continue
		}
		cs = append(cs, Commit{SHA: f[0], Parent: parents[0], Subject: f[2], Body: strings.TrimSpace(f[3])})
	}
	return cs, nil
}

// Changed lists the paths c changed relative to its parent, with renames
// detected.
func Changed(ctx context.Context, root string, c Commit) ([]Change, error) {
	out, err := git(ctx, root, "diff", "--no-ext-diff", "--name-status", "-M", "-z", c.Parent, c.SHA)
	if err != nil {
		return nil, err
	}
	return parseNameStatus(out)
}

// parseNameStatus parses `git diff --name-status -z`: a status field, then
// one path, or two for renames and copies.
func parseNameStatus(out []byte) ([]Change, error) {
	f := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	var cs []Change
	for i := 0; i < len(f); {
		st := f[i]
		if st == "" {
			i++
			continue
		}
		switch st[0] {
		case 'R', 'C':
			if i+2 >= len(f) {
				return nil, fmt.Errorf("truncated name-status after %q", st)
			}
			cs = append(cs, Change{Status: st[0], Old: f[i+1], New: f[i+2]})
			i += 3
		case 'A', 'M', 'D', 'T':
			if i+1 >= len(f) {
				return nil, fmt.Errorf("truncated name-status after %q", st)
			}
			c := Change{Status: st[0], Old: f[i+1], New: f[i+1]}
			if st[0] == 'A' {
				c.Old = ""
			}
			if st[0] == 'D' {
				c.New = ""
			}
			cs = append(cs, c)
			i += 2
		default:
			i += 2 // U (unmerged) or X: not in a commit diff; skip its path
		}
	}
	return cs, nil
}

// Hunks returns, per parent-side path, the line ranges c touches at the
// parent. A pure insertion after line n is recorded as line max(n, 1), the
// place an agent would read.
func Hunks(ctx context.Context, root string, c Commit) (map[string][]LineRange, error) {
	out, err := git(ctx, root, "diff", "--no-ext-diff", "--no-color", "-U0", "-M", c.Parent, c.SHA)
	if err != nil {
		return nil, err
	}
	return parseHunks(out), nil
}

func parseHunks(out []byte) map[string][]LineRange {
	hunks := map[string][]LineRange{}
	cur := ""
	header := false // between "diff --git" and the first hunk, where "---" names the file
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			cur, header = "", true
		case header && strings.HasPrefix(line, "--- "):
			// git appends a tab to names containing spaces.
			cur = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(line, "--- "), "a/"), "\t")
			if cur == "/dev/null" {
				cur = ""
			}
		case strings.HasPrefix(line, "@@ "):
			header = false
			if r, ok := oldRange(line); ok && cur != "" {
				hunks[cur] = append(hunks[cur], r)
			}
		}
	}
	return hunks
}

// oldRange parses the parent side of "@@ -a,b +c,d @@".
func oldRange(header string) (LineRange, bool) {
	f := strings.Fields(header)
	if len(f) < 2 || !strings.HasPrefix(f[1], "-") {
		return LineRange{}, false
	}
	startS, countS, hasCount := strings.Cut(f[1][1:], ",")
	start, err := strconv.Atoi(startS)
	if err != nil {
		return LineRange{}, false
	}
	count := 1
	if hasCount {
		if count, err = strconv.Atoi(countS); err != nil {
			return LineRange{}, false
		}
	}
	if count == 0 {
		start = max(start, 1)
		return LineRange{start, start}, true
	}
	return LineRange{start, start + count - 1}, true
}
