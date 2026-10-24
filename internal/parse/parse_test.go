package parse

import (
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

// render prints a FileResult in a stable, reviewable text form.
func render(r *FileResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "== %s (%s, %d loc)\n", r.Path, r.Lang, r.LOC)
	for _, s := range r.Symbols {
		fmt.Fprintf(&b, "  %-9s %s %d-%d exported=%v parent=%d | %s\n",
			s.Kind, s.QualifiedName, s.StartLine, s.EndLine, s.Exported, s.Parent, s.Signature)
	}
	for _, x := range r.Refs {
		fmt.Fprintf(&b, "  %s %s name=%q qual=%q member=%q L%d\n",
			r.Symbols[x.From].QualifiedName, x.Type, x.Name, x.Qualifier, x.Member, x.Line)
	}
	return b.String()
}

func parseFixture(t *testing.T, lang string) map[string]*FileResult {
	t.Helper()
	root := filepath.Join("..", "..", "testdata", "fixtures", lang)
	p := NewParser()
	defer p.Close()
	out := map[string]*FileResult{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if _, ok := Detect(rel); !ok {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		r, err := p.Parse(rel, src)
		if err != nil {
			return err
		}
		out[rel] = r
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestGolden(t *testing.T) {
	for _, lang := range []string{"go", "python", "ts", "js"} {
		t.Run(lang, func(t *testing.T) {
			results := parseFixture(t, lang)
			var paths []string
			for p := range results {
				paths = append(paths, p)
			}
			sort.Strings(paths)
			var b strings.Builder
			for _, p := range paths {
				b.WriteString(render(results[p]))
			}
			golden := filepath.Join("testdata", lang+".golden")
			if *update {
				if err := os.MkdirAll("testdata", 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(golden, []byte(b.String()), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("%v (run go test ./internal/parse -update)", err)
			}
			if got := b.String(); got != string(want) {
				t.Errorf("golden mismatch for %s; got:\n%s", lang, got)
			}
		})
	}
}

type symWant struct {
	qname      string
	kind       Kind
	start, end int
	exported   bool
}

func findSym(r *FileResult, qname string) *Symbol {
	for i := range r.Symbols {
		if r.Symbols[i].QualifiedName == qname {
			return &r.Symbols[i]
		}
	}
	return nil
}

func checkSyms(t *testing.T, r *FileResult, wants []symWant) {
	t.Helper()
	for _, w := range wants {
		s := findSym(r, w.qname)
		if s == nil {
			t.Errorf("%s: missing symbol %s", r.Path, w.qname)
			continue
		}
		if s.Kind != w.kind || s.StartLine != w.start || s.EndLine != w.end || s.Exported != w.exported {
			t.Errorf("%s: %s = {%s %d-%d exported=%v}, want {%s %d-%d exported=%v}", r.Path, w.qname,
				s.Kind, s.StartLine, s.EndLine, s.Exported, w.kind, w.start, w.end, w.exported)
		}
	}
}

func hasRef(r *FileResult, from string, typ EdgeType, name, qual, member string) bool {
	for _, x := range r.Refs {
		if r.Symbols[x.From].QualifiedName == from && x.Type == typ && x.Name == name &&
			x.Qualifier == qual && x.Member == member {
			return true
		}
	}
	return false
}

func TestGoExtraction(t *testing.T) {
	rs := parseFixture(t, "go")
	r := rs["store/user.go"]
	checkSyms(t, r, []symWant{
		{"store", KindModule, 1, 62, true},
		{"store.ErrNotFound", KindVar, 9, 9, true},
		{"store.defaultTTL", KindVar, 13, 13, false},
		{"store.User", KindType, 17, 20, true},
		{"store.Repository", KindInterface, 23, 25, true},
		{"store.Repository.GetUserByID", KindMethod, 24, 24, true},
		{"store.memRepo.GetUserByID", KindMethod, 38, 44, true},
		{"store.normalize", KindFunction, 46, 48, false},
		{"store.ValidNames", KindFunction, 51, 62, true},
		// A named closure is a function under its enclosing function.
		{"store.ValidNames.keep", KindFunction, 52, 54, false},
	})
	if findSym(r, "store.ValidNames.out") != nil {
		t.Error("local var out became a symbol")
	}
	if !hasRef(r, "store.ValidNames.keep", EdgeCalls, "normalize", "", "") ||
		!hasRef(r, "store.ValidNames", EdgeCalls, "keep", "", "") {
		t.Error("closure calls not attributed: keep→normalize, ValidNames→keep")
	}
	if !hasRef(r, "store", EdgeImports, "strings", "str", "") {
		t.Error("missing aliased import of strings")
	}
	if !hasRef(r, "store.normalize", EdgeCalls, "ToLower", "str", "") {
		t.Error("missing call str.ToLower attributed to normalize")
	}
	tr := rs["store/user_test.go"]
	checkSyms(t, tr, []symWant{{"store.TestGetUserByID", KindTest, 5, 10, true}})
	if !hasRef(tr, "store.TestGetUserByID", EdgeCalls, "NewMemRepo", "", "") {
		t.Error("missing test call to NewMemRepo")
	}
}

func TestPythonExtraction(t *testing.T) {
	rs := parseFixture(t, "python")
	r := rs["app/services/users.py"]
	checkSyms(t, r, []symWant{
		{"app.services.users", KindModule, 1, 38, true},
		{"app.services.users.UserService", KindClass, 13, 24, true},
		{"app.services.users.UserService.get_user_by_id", KindMethod, 19, 21, true},
		{"app.services.users.UserService._cache_key", KindMethod, 23, 24, false},
		{"app.services.users.load_users", KindFunction, 27, 28, true},
		{"app.services.users.DEFAULT_LIMIT", KindVar, 10, 10, false}, // not in __all__
		{"app.services.users._private_helper.inner", KindFunction, 32, 33, false},
	})
	if findSym(r, "app.services.users.__all__") != nil {
		t.Error("__all__ should not be a symbol")
	}
	for _, c := range []struct{ name, qual, member string }{
		{"os.path", "osp", ""}, {"..models", "ModelBase", "Base"}, {".", "helpers", "helpers"},
	} {
		if !hasRef(r, "app.services.users", EdgeImports, c.name, c.qual, c.member) {
			t.Errorf("missing import %+v", c)
		}
	}
	if !hasRef(r, "app.services.users.UserService", EdgeExtends, "ModelBase", "", "") {
		t.Error("missing EXTENDS ModelBase")
	}
	tr := rs["tests/test_users.py"]
	checkSyms(t, tr, []symWant{
		{"tests.test_users.TestUserService", KindTest, 5, 8, true},
		{"tests.test_users.TestUserService.test_get_user_by_id", KindTest, 6, 8, true},
		{"tests.test_users.test_load_users", KindTest, 11, 13, true},
	})
}

// TestSignatureSkipsComments: comment lines between a Python header and
// its first statement are not part of the signature.
func TestSignatureSkipsComments(t *testing.T) {
	p := NewParser()
	defer p.Close()
	src := "def atomic(using=None,\n           durable=False):\n    # Bare decorator: @atomic\n    # more notes\n    return using\n"
	r, err := p.Parse("m.py", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if s := findSym(r, "m.atomic"); s == nil || s.Signature != "def atomic(using=None, durable=False)" {
		t.Errorf("signature: %+v", s)
	}
}

func TestTypeScriptExtraction(t *testing.T) {
	rs := parseFixture(t, "ts")
	r := rs["src/services/userService.ts"]
	checkSyms(t, r, []symWant{
		{"UserLookup", KindInterface, 6, 8, true},
		{"UserLookup.getUserById", KindMethod, 7, 7, true},
		{"UserId", KindType, 10, 10, true},
		{"UserService", KindClass, 12, 28, true},
		{"UserService.getUserById", KindMethod, 19, 23, true},
		{"UserService.cacheKey", KindMethod, 25, 27, false},
		{"formatUser", KindFunction, 30, 32, true},
		{"internalHelper", KindFunction, 34, 36, false},
	})
	if s := findSym(r, "formatUser"); s.Signature != "const formatUser = (u: User): string" {
		t.Errorf("formatUser signature = %q", s.Signature)
	}
	if !hasRef(r, "src/services/userService", EdgeImports, "@app/logging", "log", "*") {
		t.Error("missing namespace import")
	}
	if !hasRef(r, "UserService", EdgeImplements, "UserLookup", "", "") ||
		!hasRef(r, "UserService", EdgeExtends, "BaseService", "", "") {
		t.Error("missing heritage refs")
	}
	tr := rs["src/services/userService.test.ts"]
	checkSyms(t, tr, []symWant{
		{"UserService", KindTest, 3, 8, false},
		{"UserService.returns a user", KindTest, 4, 7, false},
	})
	if !hasRef(tr, "UserService.returns a user", EdgeCalls, "getUserById", "svc", "") {
		t.Error("test call not attributed to the it() block")
	}
	app := rs["src/App.tsx"]
	checkSyms(t, app, []symWant{
		{"App", KindFunction, 4, 6, true},
		{"AuthProvider", KindFunction, 8, 18, true},
		// Named function-valued consts inside a function (React handlers)
		// are symbols under their enclosing function.
		{"AuthProvider.handleSignIn", KindFunction, 9, 12, false},
		{"AuthProvider.handleReset", KindFunction, 13, 15, false},
		// A class field holding an arrow function is a method.
		{"Banner.handleDismiss", KindMethod, 21, 23, true}, // public member of an exported class
	})
	// Local non-function consts and anonymous callbacks are not symbols.
	for _, q := range []string{"AuthProvider.label", "AuthProvider.user"} {
		if findSym(app, q) != nil {
			t.Errorf("unexpected symbol %s", q)
		}
	}
	// Calls inside a handler belong to it; the inline onClick callback is
	// anonymous, so its call belongs to the component.
	if !hasRef(app, "AuthProvider.handleSignIn", EdgeCalls, "formatUser", "", "") {
		t.Error("call in handleSignIn not attributed to it")
	}
	if !hasRef(app, "AuthProvider", EdgeCalls, "handleSignIn", "", "") {
		t.Error("onClick call to handleSignIn not attributed to AuthProvider")
	}
}

func TestJavaScriptExtraction(t *testing.T) {
	rs := parseFixture(t, "js")
	r := rs["lib/cart.js"]
	checkSyms(t, r, []symWant{
		{"Cart", KindClass, 4, 16, true}, // via module.exports
		{"Cart.addItem", KindMethod, 9, 11, true},
		{"createCart", KindFunction, 18, 20, true},
		{"formatTotal", KindFunction, 22, 24, true},
	})
	if findSym(r, "util") != nil {
		t.Error("require binding should not be a symbol")
	}
	if !hasRef(r, "lib/cart", EdgeImports, "./math", "sum", "sum") {
		t.Error("missing destructured require")
	}
	if !hasRef(r, "Cart.total", EdgeCalls, "sum", "", "") {
		t.Error("missing call to sum from Cart.total")
	}
}

func TestUnsupported(t *testing.T) {
	p := NewParser()
	defer p.Close()
	if _, err := p.Parse("README.md", nil); err == nil {
		t.Error("expected error for unsupported file")
	}
	if _, ok := Detect("types/index.d.ts"); ok {
		t.Error(".d.ts should be skipped")
	}
}
