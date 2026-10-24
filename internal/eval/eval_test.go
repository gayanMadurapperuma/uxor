package eval

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gayanmadurapperuma/uxor/internal/config"
	"github.com/gayanmadurapperuma/uxor/internal/retrieve"
	"github.com/gayanmadurapperuma/uxor/internal/testfix"
)

func TestParseNameStatus(t *testing.T) {
	out := "M\x00a.go\x00R087\x00old.go\x00new.go\x00A\x00added.go\x00D\x00gone.go\x00C100\x00src.go\x00copy.go\x00"
	got, err := parseNameStatus([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	want := []Change{
		{'M', "a.go", "a.go"}, {'R', "old.go", "new.go"}, {'A', "", "added.go"},
		{'D', "gone.go", ""}, {'C', "src.go", "copy.go"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
	if _, err := parseNameStatus([]byte("R100\x00only-old.go\x00")); err == nil {
		t.Error("truncated rename accepted")
	}
}

func TestParseHunks(t *testing.T) {
	diff := strings.Join([]string{
		"diff --git a/x.go b/x.go",
		"--- a/x.go",
		"+++ b/x.go",
		"@@ -3,2 +3,2 @@ func f()",
		"--- removed line that looks like a header",
		"+++ added line that looks like a header",
		"@@ -10 +10 @@",
		"-a",
		"+b",
		"@@ -0,0 +1 @@",
		"+inserted at the top",
		"diff --git a/new.go b/new.go",
		"--- /dev/null",
		"+++ b/new.go",
		"@@ -0,0 +1,3 @@",
		"+x",
		"diff --git a/has space.go b/has space.go",
		"--- a/has space.go\t",
		"+++ b/has space.go\t",
		"@@ -7,0 +8 @@",
		"+appended after line 7",
	}, "\n")
	got := parseHunks([]byte(diff))
	want := map[string][]LineRange{
		"x.go":         {{3, 4}, {10, 10}, {1, 1}},
		"has space.go": {{7, 7}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
}

func TestMetrics(t *testing.T) {
	rs := ranks([]string{"a", "b", "c", "d", "e", "f"}, []string{"c", "z", "f"})
	if !reflect.DeepEqual(rs, []int{3, 0, 6}) {
		t.Fatalf("ranks %v", rs)
	}
	if r := recallAt(rs, 5); r != 1.0/3 {
		t.Errorf("recall@5 %v", r)
	}
	if r := recallAt(rs, 10); r != 2.0/3 {
		t.Errorf("recall@10 %v", r)
	}
	if rr := reciprocalRank(rs); rr != 1.0/3 {
		t.Errorf("rr %v", rr)
	}
	if rr := reciprocalRank([]int{0, 0}); rr != 0 {
		t.Errorf("rr none %v", rr)
	}
	if !hit([]string{"x", "f"}, []string{"f"}) || hit([]string{"x"}, []string{"f"}) {
		t.Error("hit")
	}
	xs := []float64{5, 1, 4, 2, 3}
	if p := percentile(xs, 50); p != 3 {
		t.Errorf("p50 %v", p)
	}
	if p := percentile(xs, 95); p != 5 {
		t.Errorf("p95 %v", p)
	}
	if m := merge([]LineRange{{10, 12}, {1, 3}, {4, 5}, {11, 20}}); !reflect.DeepEqual(m, []LineRange{{1, 5}, {10, 20}}) {
		t.Errorf("merge %v", m)
	}
}

func TestPriceGrepAgent(t *testing.T) {
	files := map[string][][]byte{}
	file := func(name, line string, n int) {
		for range n {
			files[name] = append(files[name], []byte(line))
		}
	}
	file("w.go", "x\n", 50)   // a wrong file
	file("t.go", "ab\n", 100) // changed, reached
	file("u.go", "y\n", 3000) // changed, never matched
	lines := func(p string) [][]byte { return files[p] }
	matches := map[string][]int{"w.go": {5, 40}, "t.go": {10}}
	truth := map[string][]LineRange{"t.go": {{50, 60}}, "u.go": {{1, 1}}}

	order := hitOrder(matches)
	if !reflect.DeepEqual(order, []string{"w.go", "t.go"}) {
		t.Fatalf("order %v", order)
	}
	cost, list, read := priceGrepAgent(order, matches, truth, lines)
	// listing "w.go\nt.go\n" = 10 chars → 3; w.go lines 1–15 and 30–50 =
	// 36×2 chars → 18; t.go lines 1–20 and 50–60 = 31×3 → 24 (rounded up);
	// u.go never reached: a full read capped at 2,000 lines ×2 → 1,000.
	if list != 3 || read != 2 || cost != 3+18+24+1000 {
		t.Errorf("cost %d list %d read %d", cost, list, read)
	}
	if a := priceWholeFiles(order, []string{"t.go", "u.go"}, lines); a != 25+75+1500 {
		t.Errorf("whole files %d", a)
	}

	// The read cap: 16 one-match wrong files, and the changed one unmatched.
	matches = map[string][]int{}
	for i := range 16 {
		p := fmt.Sprintf("f%02d.go", i)
		file(p, "z\n", 1)
		matches[p] = []int{1}
	}
	cost, list, read = priceGrepAgent(hitOrder(matches), matches, map[string][]LineRange{"u.go": {{1, 1}}}, lines)
	if read != grepReadCap || list != 28 || cost != 28+15+1000 {
		t.Errorf("capped: cost %d list %d read %d", cost, list, read)
	}
}

// calcGo has Add at lines 19–21 between one-line fillers, so the C range
// of an edit inside it is lines 9–31.
func calcGo(addBody string, extra string) string {
	var b strings.Builder
	b.WriteString("package calc\n\n")
	for i := 1; i <= 15; i++ {
		fmt.Fprintf(&b, "func f%d() int { return %d }\n", i, i)
	}
	b.WriteString("\nfunc Add(a, b int) int {\n\treturn " + addBody + "\n}\n")
	for i := 16; i <= 30; i++ {
		fmt.Fprintf(&b, "func f%d() int { return %d }\n", i, i)
	}
	return b.String() + extra
}

type repo struct {
	t    *testing.T
	root string
	n    int
}

func (r *repo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", r.root}, args...)...)
	date := time.Date(2026, 1, 1, 0, 0, r.n, 0, time.UTC).Format(time.RFC3339)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		"GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (r *repo) write(rel, content string) {
	r.t.Helper()
	p := filepath.Join(r.root, rel)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *repo) commit(subject string) string {
	r.t.Helper()
	r.n++
	r.git("add", "-A", ".")
	r.git("commit", "--quiet", "-m", subject)
	return r.git("rev-parse", "HEAD")
}

func (r *repo) read(rel string) string {
	b, _ := os.ReadFile(filepath.Join(r.root, rel))
	return string(b)
}

type history struct {
	root                        string
	calcFix, rename, foo, merge string
}

// newHistory builds a repository from the go fixture with one commit of
// each kind the eval must handle. .uxor.yaml stays untracked, as in a
// real checkout; .uxor/ is ignored.
func newHistory(t *testing.T) *history {
	r := &repo{t: t, root: testfix.Copy(t, "go")}
	r.git("init", "--quiet", "-b", "main")
	r.write(".gitignore", ".uxor/\n"+config.FileName+"\n")
	r.write("calc/calc.go", calcGo("a + b", ""))
	r.commit("initial") // root commit: never evaluated
	h := &history{root: r.root}
	r.write("calc/calc.go", calcGo("b + a", ""))
	h.calcFix = r.commit("fix calc Add operand order")
	r.git("mv", "api/handler.go", "api/handlers.go")
	r.write("api/handlers.go", r.read("api/handlers.go")+"\n// moved\n")
	h.rename = r.commit("rename handler file")
	r.write("store/user.go", r.read("store/user.go")+"\nfunc FooBarBaz() {}\n")
	h.foo = r.commit("introduce FooBarBaz")
	r.write("README.md", "docs\n")
	r.commit("write docs") // no indexable file
	r.write("store/user.go", r.read("store/user.go")+"\n// two\n")
	r.write("calc/calc.go", r.read("calc/calc.go")+"\n// two\n")
	r.commit("touch two files") // over MaxChanged 1
	r.git("checkout", "--quiet", "-b", "side")
	r.write("api/handlers.go", r.read("api/handlers.go")+"\n// side\n")
	r.commit("side tweak handler")
	r.git("checkout", "--quiet", "main")
	r.write("calc/calc.go", r.read("calc/calc.go")+"\n// main\n")
	r.commit("main tweak calc")
	r.n++
	r.git("merge", "--quiet", "--no-ff", "side", "-m", "merge side")
	h.merge = r.git("rev-parse", "HEAD")
	return h
}

func worktrees(t *testing.T, root string) int {
	out, err := exec.Command("git", "-C", root, "worktree", "list", "--porcelain").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(out), "worktree ")
}

func taskBySHA(t *testing.T, rep *Report, sha string) Task {
	t.Helper()
	for _, x := range rep.Tasks {
		if x.SHA == sha {
			return x
		}
	}
	t.Fatalf("no task for %s in %+v", sha, rep.Tasks)
	return Task{}
}

func TestRunCheckoutParent(t *testing.T) {
	h := newHistory(t)
	p, err := config.Open(h.root)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := Run(context.Background(), p, Options{MaxChanged: 1, CheckoutParent: true, Savings: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Considered != 7 || len(rep.Tasks) != 5 {
		t.Fatalf("considered %d, tasks %d: %+v", rep.Considered, len(rep.Tasks), rep.Tasks)
	}
	wantSkipped := map[string]int{"no indexable files changed": 1, "more than 1 files": 1}
	if !reflect.DeepEqual(rep.Skipped, wantSkipped) {
		t.Errorf("skipped %v", rep.Skipped)
	}
	for _, x := range rep.Tasks {
		if x.SHA == h.merge {
			t.Error("merge commit evaluated")
		}
	}
	if rep.Tune.Tasks != 3 || rep.Holdout.Tasks != 2 || rep.Tasks[0].Split != "tune" || rep.Tasks[4].Split != "holdout" {
		t.Errorf("split: tune %d holdout %d", rep.Tune.Tasks, rep.Holdout.Tasks)
	}
	if got := taskBySHA(t, rep, h.rename).Truth; !reflect.DeepEqual(got, []string{"api/handler.go"}) {
		t.Errorf("rename truth %v, want the parent-side path", got)
	}
	// The parent has no FooBarBaz: the answer cannot leak in.
	if got := taskBySHA(t, rep, h.foo).Ranks; !reflect.DeepEqual(got, []int{0}) {
		t.Errorf("FooBarBaz found at the parent: ranks %v", got)
	}
	if n := worktrees(t, h.root); n != 1 {
		t.Errorf("%d worktrees left", n)
	}

	// Savings for the calc fix, priced on the parent's calc.go.
	fix := taskBySHA(t, rep, h.calcFix)
	parent := calcGo("a + b", "")
	lines := strings.SplitAfter(parent, "\n")
	window := strings.Join(lines[8:31], "") // lines 9–31: Add (19–21) ±10
	sv := fix.Savings
	if sv == nil {
		t.Fatal("no savings")
	}
	if sv.B != retrieve.Tokens(parent) {
		t.Errorf("B %d, want %d", sv.B, retrieve.Tokens(parent))
	}
	if want := fix.QueryTokens + (len(window)+3)/4; sv.C != want {
		t.Errorf("C %d, want %d", sv.C, want)
	}
	for _, x := range rep.Tasks {
		s := x.Savings
		if s.A < s.B {
			t.Errorf("%s: A %d < B %d", x.Subject, s.A, s.B)
		}
		if s.A2 > s.A+s.GrepList || s.A2 < s.GrepList {
			t.Errorf("%s: A2 %d outside [list %d, A %d + list]", x.Subject, s.A2, s.GrepList, s.A)
		}
		if s.AllFound && s.D != s.C || !s.Hit && s.D != x.QueryTokens+s.A2 || s.Hit && s.D > s.C+s.B {
			t.Errorf("%s: D %d inconsistent (C %d, A2 %d, hit %v)", x.Subject, s.D, s.C, s.A2, s.Hit)
		}
	}
	if rep.Savings == nil || rep.Savings.Tasks != 5 || len(rep.Savings.Examples) == 0 {
		t.Errorf("savings summary %+v", rep.Savings)
	}
}

func TestRunHead(t *testing.T) {
	h := newHistory(t)
	p := testfix.Index(t, h.root)
	if _, err := Run(context.Background(), p, Options{Savings: true}); err == nil {
		t.Error("savings without checkout-parent accepted")
	}
	noEdges := retrieve.DefaultTuning()
	noEdges.Edges = map[string][2]float64{}
	rep, err := Run(context.Background(), p, Options{MaxChanged: 1, Variants: []Variant{
		{"same", retrieve.DefaultTuning()}, {"no-edges", noEdges},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Variants) != 2 || rep.Variants[1].All.Tasks != 5 {
		t.Fatalf("variants %+v", rep.Variants)
	}
	same, main := rep.Variants[0].All, rep.All
	same.HookP50MS, same.HookP95MS, same.QueryP50MS, same.QueryP95MS = 0, 0, 0, 0
	main.HookP50MS, main.HookP95MS, main.QueryP50MS, main.QueryP95MS = 0, 0, 0, 0
	if same != main {
		t.Errorf("default variant %+v differs from the main run %+v", same, main)
	}
	if rep.Mode != "head" || len(rep.Tasks) != 5 || len(rep.Caveats) == 0 || !strings.Contains(rep.Caveats[0], "optimistic") {
		t.Fatalf("report %+v", rep)
	}
	if got := taskBySHA(t, rep, h.rename).Truth; !reflect.DeepEqual(got, []string{"api/handlers.go"}) {
		t.Errorf("rename truth %v, want the HEAD path", got)
	}
	// At HEAD the index already holds FooBarBaz: the leak checkout-parent avoids.
	if got := taskBySHA(t, rep, h.foo).Ranks; !reflect.DeepEqual(got, []int{1}) {
		t.Errorf("FooBarBaz ranks at HEAD %v, want [1]", got)
	}
}

func TestRunCancelRemovesWorktree(t *testing.T) {
	h := newHistory(t)
	p, err := config.Open(h.root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err = Run(ctx, p, Options{CheckoutParent: true, Progress: cancelWriter(cancel)})
	if err == nil {
		t.Fatal("run survived cancellation")
	}
	if n := worktrees(t, h.root); n != 1 {
		t.Errorf("%d worktrees left after cancel", n)
	}
}

func TestRunNeedsGitTopLevel(t *testing.T) {
	h := newHistory(t)
	sub := filepath.Join(h.root, "store")
	os.WriteFile(filepath.Join(sub, config.FileName), nil, 0o644)
	p, err := config.Open(sub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), p, Options{}); err == nil || !strings.Contains(err.Error(), "top level") {
		t.Errorf("err %v", err)
	}
}

// cancelWriter cancels on the first progress line, after the first task.
type cancelWriter context.CancelFunc

func (c cancelWriter) Write(b []byte) (int, error) {
	c()
	return len(b), nil
}
