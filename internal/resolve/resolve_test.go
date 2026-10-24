package resolve_test

import (
	"context"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/gayanmadurapperuma/uxor/internal/config"
	"github.com/gayanmadurapperuma/uxor/internal/index"
	"github.com/gayanmadurapperuma/uxor/internal/resolve"
	"github.com/gayanmadurapperuma/uxor/internal/store"
)

// copyFixture copies testdata/fixtures/<name> into a fresh project dir.
func copyFixture(t *testing.T, name string) string {
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
	return dst
}

type env struct {
	t  *testing.T
	p  *config.Project
	st *store.Store
}

func setup(t *testing.T, fixture string) *env {
	t.Helper()
	root := copyFixture(t, fixture)
	p, err := config.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), p.DBPath, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e := &env{t, p, st}
	e.index()
	return e
}

func (e *env) index(paths ...string) *index.Result {
	e.t.Helper()
	res, err := index.Run(context.Background(), e.p, e.st, index.Options{Paths: paths})
	if err != nil {
		e.t.Fatal(err)
	}
	return res
}

func (e *env) write(rel, content string) {
	e.t.Helper()
	p := filepath.Join(e.p.Root, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

// edges dumps resolved edges as "src -TYPE-> dst (dst path) weight", with
// module symbols shown as file:<path>.
func (e *env) edges() []string {
	e.t.Helper()
	rows, err := e.st.DB().Query(`
		SELECT CASE WHEN s.kind = 7 THEN 'file:' || sf.path ELSE s.qualified_name END,
		       e.type,
		       CASE WHEN d.kind = 7 THEN 'file:' || df.path ELSE d.qualified_name || ' (' || df.path || ')' END,
		       e.weight
		FROM edges e
		JOIN symbols s ON s.id = e.src_id JOIN files sf ON sf.id = s.file_id
		JOIN symbols d ON d.id = e.dst_id JOIN files df ON df.id = d.file_id
		WHERE e.type != 2`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	names := map[int]string{1: "IMPORTS", 3: "CALLS", 4: "EXTENDS", 5: "IMPLEMENTS", 6: "REFERENCES", 7: "TESTS"}
	var out []string
	for rows.Next() {
		var s, d string
		var typ int
		var w float64
		if err := rows.Scan(&s, &typ, &d, &w); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%s -%s-> %s %.2f", s, names[typ], d, w))
	}
	sort.Strings(out)
	return out
}

var update = flag.Bool("update", false, "rewrite golden files")

// TestEdgesGolden pins the full resolved edge set of every fixture. The
// goldens were reviewed by hand: they contain the expected exact edges, the
// low-weight guesses, and none of the stdlib/external/ambiguous targets.
func TestEdgesGolden(t *testing.T) {
	for _, fx := range []string{"go", "python", "ts", "js"} {
		t.Run(fx, func(t *testing.T) {
			got := strings.Join(setup(t, fx).edges(), "\n") + "\n"
			golden := filepath.Join("testdata", fx+".edges")
			if *update {
				os.MkdirAll("testdata", 0o755)
				if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("%v (run go test ./internal/resolve -update)", err)
			}
			if got != string(want) {
				t.Errorf("edges differ from %s:\n%s", golden, diff(string(want), got))
			}
		})
	}
}

func diff(want, got string) string {
	w, g := map[string]bool{}, map[string]bool{}
	for _, l := range strings.Split(want, "\n") {
		w[l] = true
	}
	for _, l := range strings.Split(got, "\n") {
		g[l] = true
	}
	var b strings.Builder
	for l := range w {
		if !g[l] {
			b.WriteString("- " + l + "\n")
		}
	}
	for l := range g {
		if !w[l] {
			b.WriteString("+ " + l + "\n")
		}
	}
	return b.String()
}

// step is one scripted edit followed by a single-file re-index.
type step struct {
	name    string
	path    string
	content string // "" deletes the file
	full    bool   // the change is expected to force a full resolve
	expect  string // an edge that must exist afterwards ("!" prefix: must not)
}

// TestScopedEqualsFull checks the D11 invariant: after every edit, the
// scoped (dirty-files-only) resolve leaves exactly the edges a full resolve
// produces.
func TestScopedEqualsFull(t *testing.T) {
	old := resolve.FullFraction
	resolve.FullFraction = 100 // never fall back to full on size; we want to exercise scoping
	t.Cleanup(func() { resolve.FullFraction = old })

	cases := map[string][]step{
		"python": {
			{name: "body-only edit keeps the interface", path: "app/services/helpers.py",
				content: "def clean(user):\n    if not user:\n        return None\n    user.save()\n    return user\n",
				expect:  "app.services.users.UserService.get_user_by_id -CALLS-> app.services.helpers.clean (app/services/helpers.py) 1.00"},
			{name: "rename callee", path: "app/services/helpers.py",
				content: "def sanitize(user):\n    user.save()\n    return user\n"},
			{name: "same-name method makes fallback ambiguous", path: "app/services/helpers.py",
				content: "class Other:\n    def save(self):\n        pass\n\n\ndef clean(user):\n    user.save()\n    return user\n"},
			{name: "delete imported module", path: "app/services/helpers.py"},
			{name: "re-add module satisfying a dangling import", path: "app/services/helpers.py",
				content: "def clean(user):\n    user.save()\n    return user\n",
				expect:  "app.services.users.UserService.get_user_by_id -CALLS-> app.services.helpers.clean (app/services/helpers.py) 1.00"},
			{name: "new base-class method reachable via import", path: "app/models.py",
				content: "class Base:\n    def save(self):\n        return True\n\n    def find(self, x):\n        return x\n\n\nclass User(Base):\n    def __init__(self, name):\n        self.name = name\n\n    def display_name(self):\n        self.save()\n        return self.name.title()\n"},
			{name: "new file makes a global name ambiguous", path: "app/extra.py",
				content: "class Badge:\n    def display_name(self):\n        return 'b'\n"},
			{name: "remove the duplicate again", path: "app/extra.py"},
		},
		"ts": {
			{name: "body-only edit", path: "src/base.ts",
				content: "export class BaseService {\n  protected log(msg: string): string {\n    return msg + \"!\";\n  }\n}\n",
				expect:  "UserService.getUserById -CALLS-> BaseService.log (src/base.ts) 1.00"},
			{name: "rename base method", path: "src/base.ts",
				content: "export class BaseService {\n  protected write(msg: string): string {\n    return msg;\n  }\n}\n"},
			{name: "change default export", path: "src/services/config.ts",
				content: "export default function make() {\n  return { scale: 2 };\n}\n"},
			{name: "delete re-exported module", path: "src/logging/index.ts"},
			{name: "new module under path alias", path: "src/logging/index.ts",
				content: "export function debug(msg: string): void {}\n"},
			{name: "add impl a", path: "src/logging/a.ts", content: "export function debug(msg: string): void {}\n"},
			{name: "add impl b", path: "src/logging/b.ts", content: "export function debug(msg: string): void {}\n"},
			{name: "re-export from a", path: "src/logging/index.ts", content: "export { debug } from \"./a\";\n",
				expect: "UserService.getUserById -CALLS-> debug (src/logging/a.ts)"},
			{name: "retarget the re-export (refs-only change)", path: "src/logging/index.ts",
				content: "export { debug } from \"./b\";\n",
				expect:  "UserService.getUserById -CALLS-> debug (src/logging/b.ts)"},
			{name: "restore default-exported class", path: "src/services/config.ts",
				content: "export default class Config {\n  static load(): Config {\n    return new Config();\n  }\n}\n",
				expect:  "internalHelper -CALLS-> Config.load"},
			{name: "second export breaks the default-export guess", path: "src/services/config.ts",
				content: "export default class Config {\n  static load(): Config {\n    return new Config();\n  }\n}\n\nexport const VERSION = 1;\n",
				expect:  "!internalHelper -CALLS-> Config.load"},
		},
		"js": {
			// Browser-style globals: no import, resolved only by repo-wide uniqueness.
			{name: "call an un-imported global", path: "lib/cart.js",
				content: "function createCart() {\n  return formatPrice(1);\n}\n"},
			{name: "define it", path: "lib/format.js", content: "function formatPrice(x) {\n  return x;\n}\n",
				expect: "createCart -CALLS-> formatPrice (lib/format.js) 0.30"},
			{name: "a second definition makes it ambiguous", path: "lib/other.js",
				content: "function formatPrice(y) {\n  return y;\n}\n", expect: "!createCart -CALLS-> formatPrice"},
			{name: "remove the duplicate", path: "lib/other.js", expect: "createCart -CALLS-> formatPrice (lib/format.js) 0.30"},
		},
		"go": {
			{name: "body-only edit with new calls", path: "api/handler.go",
				content: "package api\n\nimport \"example.com/shop/store\"\n\ntype Handler struct {\n\tstore.Repository\n\trepo store.Repository\n}\n\nfunc NewHandler() *Handler {\n\treturn &Handler{repo: store.NewMemRepo()}\n}\n\nfunc (h *Handler) Show(id int) string {\n\treturn h.format(nil)\n}\n\nfunc (h *Handler) format(u *store.User) string {\n\treturn Render(h.repo, 1)\n}\n\n// Render looks a user up through any repository.\nfunc Render(repo store.Repository, id int) string {\n\tu, _ := repo.GetUserByID(id)\n\treturn u.Name\n}\n",
				expect:  "api.Handler.format -CALLS-> api.Render (api/handler.go) 1.00"},
			{name: "rename package function", path: "store/user.go",
				content: "package store\n\ntype User struct{ Name string }\n\ntype Repository interface {\n\tGetUserByID(id int) (*User, error)\n}\n\ntype memRepo struct{}\n\nfunc NewRepo() *memRepo { return &memRepo{} }\n\nfunc (r *memRepo) GetUserByID(id int) (*User, error) { return nil, nil }\n"},
			{name: "add a file to the package", path: "store/compat.go",
				content: "package store\n\nfunc NewMemRepo() *memRepo { return NewRepo() }\n"},
			{name: "delete the test file", path: "store/user_test.go"},
			{name: "add a plain importer", path: "cmd/main.go",
				content: "package main\n\nimport \"example.com/shop/store\"\n\nvar _ = store.NewMemRepo\n"},
			{name: "add a symbol-less package file", path: "store/doc.go", content: "// Package store keeps users.\npackage store\n",
				expect: "file:cmd/main.go -IMPORTS-> file:store/doc.go"},
			{name: "rename its package (only the module symbol changes)", path: "store/doc.go",
				content: "// Package store keeps users.\npackage storage\n",
				expect:  "file:cmd/main.go -IMPORTS-> file:store/doc.go"},
			{name: "rename the package clause (module symbol replaced)", path: "store/user.go",
				content: "package storage\n\ntype User struct{ Name string }\n\ntype Repository interface {\n\tGetUserByID(id int) (*User, error)\n}\n\ntype memRepo struct{}\n\nfunc NewRepo() *memRepo { return &memRepo{} }\n\nfunc (r *memRepo) GetUserByID(id int) (*User, error) { return nil, nil }\n",
				expect:  "file:api/handler.go -IMPORTS-> file:store/user.go"},
		},
	}
	for fx, steps := range cases {
		t.Run(fx, func(t *testing.T) {
			e := setup(t, fx)
			for _, s := range steps {
				if s.content == "" {
					os.Remove(filepath.Join(e.p.Root, filepath.FromSlash(s.path)))
				} else {
					e.write(s.path, s.content)
				}
				res := e.index(s.path)
				if res.Resolve == nil || res.Resolve.Full != s.full {
					t.Fatalf("%s: resolve = %+v, want full=%v", s.name, res.Resolve, s.full)
				}
				scoped := e.edges()
				if s.expect != "" {
					want, neg := strings.CutPrefix(s.expect, "!")
					found := false
					for _, l := range scoped {
						found = found || strings.HasPrefix(l, want)
					}
					if found == neg {
						t.Fatalf("%s: edge %q present=%v\n%s", s.name, want, found, strings.Join(scoped, "\n"))
					}
				}
				if _, err := resolve.Run(context.Background(), e.p, e.st, resolve.Options{Full: true}); err != nil {
					t.Fatal(err)
				}
				if full := e.edges(); strings.Join(full, "\n") != strings.Join(scoped, "\n") {
					t.Fatalf("%s: scoped resolve differs from full:\n%s", s.name,
						diff(strings.Join(full, "\n"), strings.Join(scoped, "\n")))
				}
			}
		})
	}
}

func TestMarkerChangeForcesFull(t *testing.T) {
	e := setup(t, "ts")
	e.write("tsconfig.base.json", `{"compilerOptions": {"baseUrl": ".", "paths": {"#app/*": ["src/*"]}}}`)
	res := e.index() // whole-project run re-reads markers
	if res.Resolve == nil || !res.Resolve.Full {
		t.Fatalf("marker edit should force a full resolve: %+v", res.Resolve)
	}
	for _, l := range e.edges() {
		if strings.Contains(l, "file:src/base.ts") || strings.Contains(l, "BaseService.log") {
			t.Errorf("edge via the removed @app alias survived: %s", l)
		}
	}
}

func TestPageRankComputed(t *testing.T) {
	e := setup(t, "python")
	var top string
	if err := e.st.DB().QueryRow(`SELECT qualified_name FROM symbols WHERE kind != 7 ORDER BY pagerank DESC LIMIT 1`).Scan(&top); err != nil {
		t.Fatal(err)
	}
	if top != "app.models.Base.save" && top != "app.models.Base" {
		t.Errorf("top PageRank symbol = %s; expected the most depended-on Base/save", top)
	}
	// A single-file run defers PageRank and marks it stale.
	e.write("app/services/helpers.py", "def clean(user):\n    return user\n")
	if res := e.index("app/services/helpers.py"); !res.Resolve.PageRankStale || res.Resolve.PageRankNodes != 0 {
		t.Errorf("single-file run: %+v", res.Resolve)
	}
	if res := e.index(); res.Resolve.PageRankNodes == 0 {
		t.Errorf("whole-project run should recompute stale PageRank: %+v", res.Resolve)
	}
}
