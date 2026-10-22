package retrieve_test

import (
	"context"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gayanmadurapperuma/codegraph/internal/config"
	"github.com/gayanmadurapperuma/codegraph/internal/index"
	"github.com/gayanmadurapperuma/codegraph/internal/retrieve"
	"github.com/gayanmadurapperuma/codegraph/internal/store"
)

var update = flag.Bool("update", false, "rewrite golden files")

// fixture copies testdata/fixtures/<name> into a temp project, indexes it
// and returns the project with a read-only store.
func fixture(t testing.TB, name string) (*config.Project, *store.Store) {
	t.Helper()
	src := filepath.Join("..", "..", "testdata", "fixtures", name)
	dst, _ := filepath.EvalSymlinks(t.TempDir())
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dst, config.FileName), nil, 0o644)
	p, err := config.Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	w, err := store.Open(ctx, p.DBPath, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := index.Run(ctx, p, w, index.Options{}); err != nil {
		t.Fatal(err)
	}
	w.Close()
	st, err := store.Open(ctx, p.DBPath, store.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return p, st
}

func find(t testing.TB, p *config.Project, st *store.Store, q string, opt retrieve.Options) *retrieve.Result {
	t.Helper()
	res, err := retrieve.FindRelevant(context.Background(), st, p.Config, q, opt)
	if err != nil {
		t.Fatalf("%q: %v", q, err)
	}
	return res
}

var goldenQueries = map[string][]string{
	"go":     {"get user by id", "Render", "handler show user", "tests for GetUserByID"},
	"python": {"get user by id", "save user model", "display name", "tests for user service"},
	"ts":     {"get user by id", "formatUser", "load config"},
	"js":     {"cart total", "formatTotal"},
}

// TestQueryGolden pins the packed output, with reasons, of fixed queries.
func TestQueryGolden(t *testing.T) {
	for _, fx := range []string{"go", "python", "ts", "js"} {
		t.Run(fx, func(t *testing.T) {
			p, st := fixture(t, fx)
			var b strings.Builder
			for _, q := range goldenQueries[fx] {
				res := find(t, p, st, q, retrieve.Options{})
				fmt.Fprintf(&b, "## %s  (%d tokens)\n", q, res.TokensUsed)
				for _, f := range res.Files {
					for _, s := range f.Symbols {
						b.WriteString(retrieve.Render(f.Path, s, true) + "\n")
					}
				}
				b.WriteString("\n")
			}
			got := b.String()
			golden := filepath.Join("testdata", fx+".query")
			if *update {
				os.MkdirAll("testdata", 0o755)
				if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("%v (run go test ./internal/retrieve -update)", err)
			}
			if got != string(want) {
				t.Errorf("output differs from %s; got:\n%s", golden, got)
			}
		})
	}
}

// TestQueryExpectations states what the goldens should show, so an update
// cannot silently accept a ranking regression.
func TestQueryExpectations(t *testing.T) {
	type want struct {
		fixture, query string
		firstFile      string // the top file, or (prefixed "best:") the best symbol's file
		contains       string // a line that must be present (substring)
		why            string // its reason prefix
	}
	for _, w := range []want{
		{"go", "get user by id", "store/user.go", "GetUserByID(id int) (*User, error)", "match"},
		{"go", "Render", "api/handler.go", "func Render(", "match: exact name"},
		{"python", "save user model", "app/models.py", "def save(self)", "match"},
		{"python", "display name", "app/models.py", "def save(self)", "called-by app.models.User.display_name"},
		{"python", "tests for user service", "best:tests/test_users.py", "test def test_get_user_by_id", "match"},
		{"ts", "formatUser", "src/services/userService.ts", "const formatUser", "match: exact name"},
		{"ts", "get user by id", "src/services/userService.ts", "function debug(", "called-by UserService.getUserById"},
		{"js", "formatTotal", "lib/cart.js", "const formatTotal", "match: exact name"},
		{"js", "cart total", "lib/cart.js", "function sum(xs)", "called-by Cart.total"},
	} {
		p, st := fixture(t, w.fixture)
		res := find(t, p, st, w.query, retrieve.Options{})
		first := ""
		if bestFile, ok := strings.CutPrefix(w.firstFile, "best:"); ok {
			best := -1.0
			for _, f := range res.Files {
				for _, s := range f.Symbols {
					if s.Score > best {
						best, first = s.Score, f.Path
					}
				}
			}
			w.firstFile = bestFile
		} else if len(res.Files) > 0 {
			first = res.Files[0].Path
		}
		if first != w.firstFile {
			t.Errorf("%s %q: first file %s, want %s:\n%s", w.fixture, w.query, first, w.firstFile, res.Text)
			continue
		}
		found := false
		for _, f := range res.Files {
			for _, s := range f.Symbols {
				if strings.Contains(retrieve.Render(f.Path, s, false), w.contains) {
					found = true
					if !strings.HasPrefix(s.Why, w.why) {
						t.Errorf("%s %q: %s why %q, want %q", w.fixture, w.query, w.contains, s.Why, w.why)
					}
				}
			}
		}
		if !found {
			t.Errorf("%s %q: no line containing %q:\n%s", w.fixture, w.query, w.contains, res.Text)
		}
	}
}

func TestFindRelevantLimits(t *testing.T) {
	p, st := fixture(t, "ts")
	for _, budget := range []int{1, 20, 60, 1000} {
		res := find(t, p, st, "get user by id", retrieve.Options{Budget: budget})
		if res.Text == "" {
			t.Fatalf("budget %d: empty", budget)
		}
		if strings.Count(res.Text, "\n") > 1 && res.TokensUsed > budget {
			t.Errorf("budget %d: used %d", budget, res.TokensUsed)
		}
		if res.TokensUsed != retrieve.Tokens(res.Text) {
			t.Errorf("tokens_used %d != Tokens(text) %d", res.TokensUsed, retrieve.Tokens(res.Text))
		}
	}
	if res := find(t, p, st, "get user by id", retrieve.Options{MaxFiles: 2}); len(res.Files) != 2 {
		t.Errorf("max files: %d", len(res.Files))
	}
	for _, q := range []string{"", "the code please", `"*" - NEAR ( )`, "zzzqqq"} {
		if res := find(t, p, st, q, retrieve.Options{}); len(res.Files) != 0 || res.Text != "" {
			t.Errorf("%q: expected no results, got %s", q, res.Text)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := retrieve.FindRelevant(ctx, st, p.Config, "get user by id", retrieve.Options{}); err == nil {
		t.Error("a cancelled context should abort retrieval")
	}
}

// BenchmarkFindRelevant runs on the ts fixture, or on an indexed project
// given by CODEGRAPH_BENCH_ROOT (e.g. a django checkout).
func BenchmarkFindRelevant(b *testing.B) {
	var p *config.Project
	var st *store.Store
	queries := []string{"get user by id", "formatUser", "load config"}
	if root := os.Getenv("CODEGRAPH_BENCH_ROOT"); root != "" {
		var err error
		if p, err = config.Open(root); err != nil {
			b.Fatal(err)
		}
		if st, err = store.Open(context.Background(), p.DBPath, store.Options{ReadOnly: true}); err != nil {
			b.Fatal(err)
		}
		defer st.Close()
		queries = []string{"queryset bulk create ignore conflicts", "csrf token rotation middleware",
			"remote write retry on 429", "tsdb head compaction appender"}
	} else {
		p, st = fixture(b, "ts")
	}
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		find(b, p, st, queries[i%len(queries)], retrieve.Options{})
	}
}
