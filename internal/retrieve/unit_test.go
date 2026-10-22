package retrieve

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gayanmadurapperuma/codegraph/internal/parse"
	"github.com/gayanmadurapperuma/codegraph/internal/store"
)

func TestTokenize(t *testing.T) {
	cases := []struct {
		in     string
		terms  []string
		idents []string
		tests  bool
	}{
		{"get user by id", []string{"get", "user", "id"}, nil, false},
		{"Please fix the bug in formatUser", []string{"formatuser", "format", "user"}, []string{"formatUser"}, false},
		{"why does get_user_by_id fail?", []string{"get", "user", "id", "fail"}, []string{"get_user_by_id"}, false},
		{"store.NewMemRepo returns nil", []string{"store", "newmemrepo", "new", "mem", "repo", "returns", "nil"}, []string{"NewMemRepo"}, false},
		{"add tests for HTTPServer timeouts", []string{"add", "httpserver", "http", "server", "timeouts"}, []string{"HTTPServer"}, true},
		{"Render", []string{"render"}, []string{"Render"}, false},
		{"fix test_release_memory", []string{"release", "memory"}, []string{"test_release_memory"}, true},
		{"Add retry to Cart", []string{"add", "retry", "cart"}, []string{"Cart"}, false},
		{"user user USER", []string{"user"}, []string{"USER"}, false},
		{"x 42 a", nil, nil, false},
		{"the code", nil, nil, false},
		{`NEAR("a" -b) * col:x`, []string{"near", "col"}, nil, false},
	}
	for _, c := range cases {
		q := Tokenize(c.in)
		if !reflect.DeepEqual(q.Terms, c.terms) || !reflect.DeepEqual(q.Idents, c.idents) || q.WantsTests != c.tests {
			t.Errorf("Tokenize(%q) = terms %q idents %q tests %v; want %q %q %v",
				c.in, q.Terms, q.Idents, q.WantsTests, c.terms, c.idents, c.tests)
		}
	}
}

func TestMatchExpr(t *testing.T) {
	if got := MatchExpr([]string{"get", "user", `a"b`}); got != `"get" OR "user"* OR "a""b"` {
		t.Errorf("got %s", got)
	}
}

// openTemp returns a writable store for raw-SQL tests.
func openTemp(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "index.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func exec(t *testing.T, st *store.Store, q string, args ...any) {
	t.Helper()
	if _, err := st.DB().Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

// TestFTSInjection runs hostile terms through a real FTS5 table: every one
// must be a valid expression that matches literally.
func TestFTSInjection(t *testing.T) {
	st := openTemp(t)
	exec(t, st, `INSERT INTO symbols(id, file_id, kind, name, qualified_name, start_line, end_line) VALUES (1, 1, 1, 'near', 'near', 1, 1)`)
	exec(t, st, `INSERT INTO symbols_fts(rowid, name, qualified_name, signature, path) VALUES (1, 'near', 'near', '', 'a.go')`)
	hostile := [][]string{
		{`"`}, {`*`}, {`-`}, {`NEAR`}, {`NEAR(a b)`}, {`AND`}, {`OR`}, {`NOT`}, {`(`}, {`)`},
		{`name:x`}, {`^a`}, {`a"b`}, {`""`}, {`{name}:x`}, {`ünïcödé`}, {`a`, `-b`, `*`},
	}
	f := &FTS{DB: st.DB()}
	for _, terms := range hostile {
		if _, err := f.Seeds(context.Background(), Query{Terms: terms}, 10); err != nil {
			t.Errorf("terms %q: %v", terms, err)
		}
		q := Tokenize(strings.Join(terms, " "))
		if _, err := f.Seeds(context.Background(), q, 10); err != nil {
			t.Errorf("prompt %q: %v", terms, err)
		}
	}
	// NEAR is searched as the word "near", not used as an operator.
	got, err := f.Seeds(context.Background(), Query{Terms: []string{"NEAR"}}, 10)
	if err != nil || len(got) != 1 {
		t.Errorf("literal NEAR: %v %v", got, err)
	}
}

func TestExpand(t *testing.T) {
	st := openTemp(t)
	edge := func(src, dst int64, typ parse.EdgeType, w float64) {
		exec(t, st, `INSERT INTO edges(src_id, dst_id, type, weight) VALUES (?, ?, ?, ?)`, src, dst, int(typ), w)
	}
	edge(1, 2, parse.EdgeCalls, 1)   // 2: callee at hop 1
	edge(2, 3, parse.EdgeCalls, 1)   // 3: hop 2
	edge(3, 4, parse.EdgeCalls, 1)   // 4: hop 3, beyond depth
	edge(1, 5, parse.EdgeCalls, 0.5) // 5: a hub
	for _, c := range []int64{6, 7, 8} {
		edge(c, 5, parse.EdgeCalls, 1) // callers of the hub stay unreached
	}
	edge(1, 9, parse.EdgeImports, 1) // 9 via IMPORTS (0.3) and CALLS (1.0)
	edge(1, 9, parse.EdgeCalls, 1)
	edge(10, 1, parse.EdgeCalls, 1) // 10: a caller

	nodes, err := expand(context.Background(), st.DB(), []Seed{{ID: 1, Score: 1, Why: "match"}},
		ExpandOptions{Depth: 2, MaxPerHop: 50, HubFanIn: 2})
	if err != nil {
		t.Fatal(err)
	}
	want := map[int64]struct {
		exp   float64
		label string
	}{
		2:  {0.5, "called-by"},
		3:  {0.25, "called-by"},
		5:  {0.25, "called-by"},
		9:  {0.5, "called-by"},
		10: {0.4, "calls"},
	}
	for id, w := range want {
		n := nodes[id]
		if n == nil || !near(n.exp, w.exp) || n.label != w.label {
			t.Errorf("node %d = %+v, want exp %.2f %s", id, n, w.exp, w.label)
		}
	}
	for _, id := range []int64{4, 6, 7, 8} {
		if nodes[id] != nil {
			t.Errorf("node %d reached: %+v", id, nodes[id])
		}
	}
	if !nodes[5].hub {
		t.Error("5 should be marked as a hub")
	}

	// MaxPerHop 1: only the best frontier node (2 or 9, tie broken by id)
	// is expanded at hop 2.
	nodes, err = expand(context.Background(), st.DB(), []Seed{{ID: 1, Score: 1}},
		ExpandOptions{Depth: 2, MaxPerHop: 1, HubFanIn: 100})
	if err != nil {
		t.Fatal(err)
	}
	if nodes[3] == nil || nodes[6] != nil {
		t.Errorf("max_per_hop: 3=%v 6=%v", nodes[3], nodes[6])
	}
}

func near(a, b float64) bool { return a-b < 1e-9 && b-a < 1e-9 }

func TestFuse(t *testing.T) {
	a := []Seed{{ID: 1, Score: 1}, {ID: 2, Score: 0.5}}
	if got := Fuse([][]Seed{a, nil}, 10); !reflect.DeepEqual(got, a) {
		t.Errorf("single source should pass through, got %v", got)
	}
	b := []Seed{{ID: 2, Score: 1, Why: "vector"}, {ID: 3, Score: 0.9}}
	got := Fuse([][]Seed{a, b}, 10)
	if len(got) != 3 || got[0].ID != 2 || got[0].Score != 1 || got[0].Why != "vector" {
		t.Errorf("fused: %+v", got)
	}
}

func TestPack(t *testing.T) {
	// Sorted by score, as rank returns them.
	cands := []candidate{
		{ID: 2, Kind: parse.KindMethod, Name: "m", Signature: "def m(self)", Path: "a.py", StartLine: 3, EndLine: 4, Parent: 1, Score: 3},
		{ID: 3, Kind: parse.KindFunction, Name: "f", Signature: "def f()", Path: "b.py", StartLine: 1, EndLine: 2, Score: 2.5},
		{ID: 1, Kind: parse.KindClass, Name: "C", Signature: "class C", Path: "a.py", StartLine: 1, EndLine: 9, Score: 2},
		{ID: 4, Kind: parse.KindMethod, Name: "find", Signature: "find(id: number)", Path: "c.ts", StartLine: 5, EndLine: 6, Score: 1},
		{ID: 5, Kind: parse.KindFunction, Name: "weak", Signature: "def weak()", Path: "d.py", StartLine: 1, EndLine: 2, Score: 0.7},
	}
	files, text := pack(cands, 1000, 0)
	want := "a.py:1-9  class C\n  a.py:3-4  def m(self)\nb.py:1-2  def f()\nc.ts:5-6  method find(id: number)\n"
	if text != want {
		t.Errorf("text:\n%s\nwant:\n%s", text, want)
	}
	// d.py scores below minRelScore of the best and is dropped.
	if len(files) != 3 || files[0].Path != "a.py" || files[0].Score != 3.5 {
		t.Errorf("files: %+v", files)
	}

	// The budget is never exceeded, except by the single best line.
	for budget := 1; budget <= 40; budget++ {
		_, text := pack(cands, budget, 0)
		n := strings.Count(text, "\n")
		if n == 0 {
			t.Fatalf("budget %d: no lines", budget)
		}
		if n > 1 && Tokens(text) > budget {
			t.Errorf("budget %d: used %d tokens:\n%s", budget, Tokens(text), text)
		}
	}
	if _, text := pack(cands, 1, 0); text != "a.py:3-4  def m(self)\n" {
		t.Errorf("tiny budget: %q", text)
	}
	if files, _ := pack(cands, 1000, 1); len(files) != 1 {
		t.Errorf("max files 1: %+v", files)
	}
}
