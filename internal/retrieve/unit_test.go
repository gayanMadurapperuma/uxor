package retrieve

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/gayanmadurapperuma/uxor/internal/parse"
	"github.com/gayanmadurapperuma/uxor/internal/store"
)

func TestTokenize(t *testing.T) {
	cases := []struct {
		in      string
		terms   []string
		whole   []string
		idents  []string
		tests   bool
		phrases [][]string
	}{
		{"get user by id", []string{"get", "user", "id"}, nil, nil, false, nil},
		{"Please fix the bug in formatUser", []string{"format", "user"}, []string{"formatuser"}, []string{"formatUser"}, false, nil},
		{"why does get_user_by_id fail?", []string{"get", "user", "id", "fail"}, nil, []string{"get_user_by_id"}, false, nil},
		{"store.NewMemRepo returns nil", []string{"store", "new", "mem", "repo", "returns", "nil"}, []string{"newmemrepo"}, []string{"NewMemRepo"}, false, nil},
		{"add tests for HTTPServer timeouts", []string{"add", "http", "server", "timeouts"}, []string{"httpserver"}, []string{"HTTPServer"}, true, nil},
		{"Render", []string{"render"}, nil, []string{"Render"}, false, nil},
		{"fix test_release_memory", []string{"release", "memory"}, nil, []string{"test_release_memory"}, true, nil},
		{"Add retry to Cart", []string{"add", "retry", "cart"}, nil, []string{"Cart"}, false, nil},
		{"user user USER", []string{"user"}, nil, []string{"USER"}, false, nil},
		{"x 42 a", nil, nil, nil, false, nil},
		{"the code", nil, nil, nil, false, nil},
		{`NEAR("a" -b) * col:x`, []string{"near", "col"}, nil, nil, false, nil},
		// A hyphenated compound is also searched as a phrase and joined.
		{"fix the real-time feed", []string{"real", "time", "feed", "realtime"}, []string{"realtime"}, nil, false,
			[][]string{{"real", "time"}}},
		// Synonym groups: "sign-in" reaches login, log in, logon (D32).
		{"where sign-in is handled", []string{"sign", "handled", "signin", "login", "logon"},
			[]string{"signin", "login", "logon"}, nil, false, [][]string{{"sign", "in"}, {"log", "in"}}},
		{"where is the login function", []string{"login", "signin", "logon"},
			[]string{"login", "signin", "logon"}, nil, false, [][]string{{"log", "in"}, {"sign", "in"}}},
	}
	for _, c := range cases {
		q := tokenize(c.in, true, true) // every expansion; defaults are checked below
		if !reflect.DeepEqual(q.Terms, c.terms) || !reflect.DeepEqual(q.Whole, c.whole) ||
			!reflect.DeepEqual(q.Idents, c.idents) || q.WantsTests != c.tests || !reflect.DeepEqual(q.Phrases, c.phrases) {
			t.Errorf("Tokenize(%q) = terms %q whole %q idents %q tests %v phrases %q; want %q %q %q %v %q",
				c.in, q.Terms, q.Whole, q.Idents, q.WantsTests, q.Phrases, c.terms, c.whole, c.idents, c.tests, c.phrases)
		}
	}
}

// TestTokenizeDefaults: compounds are off by default (D35), synonyms on.
func TestTokenizeDefaults(t *testing.T) {
	q := Tokenize("fix the real-time feed")
	if len(q.Phrases) != 0 || slices.Contains(q.Terms, "realtime") {
		t.Errorf("compound expanded by default: %+v", q)
	}
	q = Tokenize("where sign-in is handled")
	if !slices.Contains(q.Terms, "login") || !slices.ContainsFunc(q.Phrases, func(p []string) bool {
		return slices.Equal(p, []string{"sign", "in"})
	}) {
		t.Errorf("sign-in should still reach the login synonyms: %+v", q)
	}
}

func TestMatchExpr(t *testing.T) {
	got := MatchExpr(Query{Terms: []string{"get", "user", `a"b`}, Whole: []string{"formatuser"},
		Phrases: [][]string{{"sign", "in"}}})
	if want := `"get" OR "user"* OR "a""b" OR "0formatuser" OR "formatuser" OR "sign in"`; got != want {
		t.Errorf("got %s, want %s", got, want)
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
		{ID: 2, Kind: parse.KindMethod, Name: "m", Signature: "def m(self)", Path: "a.py", StartLine: 3, EndLine: 4, Parent: 1, Score: 3, Why: "match"},
		{ID: 3, Kind: parse.KindFunction, Name: "f", Signature: "def f()", Path: "b.py", StartLine: 1, EndLine: 2, Score: 2.5, Why: "match"},
		{ID: 1, Kind: parse.KindClass, Name: "C", Signature: "class C", Path: "a.py", StartLine: 1, EndLine: 9, Score: 2, Why: "match"},
		{ID: 4, Kind: parse.KindMethod, Name: "find", Signature: "find(id: number)", Path: "c.ts", StartLine: 5, EndLine: 6, Score: 1, Why: "match"},
		{ID: 5, Kind: parse.KindFunction, Name: "weak", Signature: "def weak()", Path: "d.py", StartLine: 1, EndLine: 2, Score: 0.7, Why: "match"},
	}
	files, text := pack(cands, 1000, 0, DefaultTuning())
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
		_, text := pack(cands, budget, 0, DefaultTuning())
		n := strings.Count(text, "\n")
		if n == 0 {
			t.Fatalf("budget %d: no lines", budget)
		}
		if n > 1 && Tokens(text) > budget {
			t.Errorf("budget %d: used %d tokens:\n%s", budget, Tokens(text), text)
		}
	}
	if _, text := pack(cands, 1, 0, DefaultTuning()); text != "a.py:3-4  def m(self)\n" {
		t.Errorf("tiny budget: %q", text)
	}
	if files, _ := pack(cands, 1000, 1, DefaultTuning()); len(files) != 1 {
		t.Errorf("max files 1: %+v", files)
	}
}

// TestPackRelatedPerFile: lines reached only through the graph are capped
// per file, keeping the best; matches never are (D38).
func TestPackRelatedPerFile(t *testing.T) {
	cands := []candidate{ // sorted by score
		{ID: 1, Name: "signIn", Signature: "signIn()", Path: "repo.ts", StartLine: 10, EndLine: 20, Score: 1.2, Why: "match"},
		{ID: 2, Name: "signOut", Signature: "signOut()", Path: "repo.ts", StartLine: 22, EndLine: 25, Score: 1.1, Why: "match"},
		{ID: 3, Name: "mapError", Signature: "mapError()", Path: "repo.ts", StartLine: 40, EndLine: 50, Score: 0.6, Why: "called-by signIn"},
		{ID: 4, Name: "mapUser", Signature: "mapUser()", Path: "repo.ts", StartLine: 30, EndLine: 38, Score: 0.5, Why: "called-by signIn"},
		{ID: 5, Name: "useAuth", Signature: "useAuth()", Path: "ctx.ts", StartLine: 5, EndLine: 9, Score: 0.55, Why: "called-by LoginClient"},
	}
	names := func(limit int) []string {
		tu := DefaultTuning()
		tu.RelatedPerFile = limit
		files, _ := pack(cands, 1000, 0, tu)
		var out []string
		for _, f := range files {
			for _, s := range f.Symbols {
				out = append(out, s.Name)
			}
		}
		return out
	}
	if got := names(-1); len(got) != 5 {
		t.Errorf("no cap: %v", got)
	}
	if got := names(1); !reflect.DeepEqual(got, []string{"signIn", "signOut", "mapError", "useAuth"}) {
		t.Errorf("cap 1: %v", got)
	}
	if got := names(0); !reflect.DeepEqual(got, []string{"signIn", "signOut"}) {
		t.Errorf("cap 0: %v", got)
	}
}

// TestSeedsPerName: a constant declared in many files can't take every seed
// slot; with the cap, other names get the freed slots (D39).
func TestSeedsPerName(t *testing.T) {
	st := openTemp(t)
	for i := 1; i <= 8; i++ { // COVER_BUCKET in 8 files: the strongest match
		exec(t, st, fmt.Sprintf(`INSERT INTO symbols(id, file_id, kind, name, qualified_name, start_line, end_line) VALUES (%d, %d, 6, 'COVER_BUCKET', 'COVER_BUCKET', 1, 1)`, i, i))
		exec(t, st, fmt.Sprintf(`INSERT INTO symbols_fts(rowid, name, qualified_name, signature, path) VALUES (%d, 'cover bucket', 'cover bucket', '', 'f%d.ts')`, i, i))
	}
	exec(t, st, `INSERT INTO symbols(id, file_id, kind, name, qualified_name, start_line, end_line) VALUES (9, 9, 3, 'CoverUploadController', 'CoverUploadController', 1, 9)`)
	exec(t, st, `INSERT INTO symbols_fts(rowid, name, qualified_name, signature, path) VALUES (9, 'cover upload controller', 'cover upload controller', 'class CoverUploadController', 'upload.ts')`)
	q := Query{Terms: []string{"cover", "bucket"}}
	ids := func(perName int) []int64 {
		tu := DefaultTuning()
		tu.SeedsPerName = perName
		seeds, err := (&FTS{DB: st.DB(), Tuning: tu}).Seeds(context.Background(), q, 3)
		if err != nil {
			t.Fatal(err)
		}
		var out []int64
		for _, s := range seeds {
			out = append(out, s.ID)
		}
		return out
	}
	if got := ids(0); slices.Contains(got, 9) {
		t.Errorf("without the cap the 8 copies should fill all 3 slots: %v", got)
	}
	if got := ids(2); len(got) != 3 || !slices.Contains(got, 9) {
		t.Errorf("with 2 per name the controller should take a slot: %v", got)
	}
	// Same name, different signature (interface method and implementation):
	// not identical declarations, so the cap keeps both.
	exec(t, st, `UPDATE symbols SET signature = 'const COVER_BUCKET: string' WHERE id = 1`)
	if got := ids(1); !slices.Contains(got, 1) || !slices.Contains(got, 2) {
		t.Errorf("same name, different signatures must both stay: %v", got)
	}
}

func TestSeedCount(t *testing.T) {
	tu := DefaultTuning()
	if n := seedCount(20, 12, tu); n != 20 {
		t.Errorf("off: %d", n)
	}
	tu.SeedsPerTerm = 2
	for terms, want := range map[int]int{3: 20, 5: 20, 10: 30, 40: 60} {
		if n := seedCount(20, terms, tu); n != want {
			t.Errorf("%d terms: %d, want %d", terms, n, want)
		}
	}
	if n := seedCount(80, 3, tu); n != 80 {
		t.Errorf("a configured count above the cap is kept: %d", n)
	}
}

// TestPackCollapseDuplicates: an identical declaration in many files is
// packed once, noting the others; its files don't take answer slots (D39).
func TestPackCollapseDuplicates(t *testing.T) {
	var cands []candidate
	for i := 1; i <= 8; i++ {
		cands = append(cands, candidate{ID: int64(i), Kind: parse.KindVar, Name: "COVER_BUCKET",
			Signature: "const COVER_BUCKET = process.env.COVER_BUCKET!", Path: fmt.Sprintf("lambda/f%d/index.ts", i),
			StartLine: 3, EndLine: 3, Score: 1.0 - float64(i)/100, Why: "match"})
	}
	cands = append(cands, candidate{ID: 9, Kind: parse.KindClass, Name: "Uploader", Signature: "class Uploader",
		Path: "upload.ts", StartLine: 1, EndLine: 9, Score: 0.8, Why: "match"})
	tu := DefaultTuning()
	tu.CollapseDuplicates = true
	files, text := pack(cands, 1000, 2, tu) // 2 files, as the hook caps them
	if strings.Count(text, "const COVER_BUCKET =") != 1 || !strings.Contains(text, "(+7 more files)") {
		t.Errorf("not collapsed:\n%s", text)
	}
	if len(files) != 2 || !strings.Contains(text, "upload.ts") {
		t.Errorf("the copies took the file slots:\n%s", text)
	}
	if got := files[0].Symbols[0].AlsoIn; len(got) != 7 {
		t.Errorf("also_in: %v", got)
	}
	tu.CollapseDuplicates = false
	if _, text := pack(cands, 1000, 2, tu); strings.Contains(text, "more files") {
		t.Errorf("collapsed while off:\n%s", text)
	}
}
