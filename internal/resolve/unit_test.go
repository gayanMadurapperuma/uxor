package resolve

import (
	"encoding/json"
	"math"
	"testing"
)

func TestPageRank(t *testing.T) {
	// 0→1, 2→1, 1→3; 3 is dangling. 3 collects everything flowing through 1.
	g := graph{n: 4, src: []int32{0, 2, 1}, dst: []int32{1, 1, 3}, w: []float32{1, 1, 1}}
	pr := pagerank(g)
	sum := 0.0
	for _, v := range pr {
		sum += v
	}
	if math.Abs(sum-4) > 1e-3 {
		t.Errorf("scores sum to %f, want ≈ n=4", sum)
	}
	if !(pr[3] > pr[1] && pr[1] > pr[0] && math.Abs(pr[0]-pr[2]) < 1e-9) {
		t.Errorf("unexpected order: %v", pr)
	}
	// Weights matter: a heavier edge passes more rank.
	w := pagerank(graph{n: 3, src: []int32{0, 0}, dst: []int32{1, 2}, w: []float32{0.9, 0.1}})
	if w[1] <= w[2] {
		t.Errorf("weighted: %v", w)
	}
	if pagerank(graph{}) != nil {
		t.Error("empty graph should give nil")
	}
}

func TestStripJSONC(t *testing.T) {
	in := `{
  // line comment with "quote"
  "a": "http://x/*not a comment*/", /* block */
  "b": [1, 2,],
  "c": {"d": "e,}",},
}`
	var v map[string]any
	if err := json.Unmarshal(stripJSONC([]byte(in)), &v); err != nil {
		t.Fatalf("%v\n%s", err, stripJSONC([]byte(in)))
	}
	if v["a"] != "http://x/*not a comment*/" || v["c"].(map[string]any)["d"] != "e,}" || len(v["b"].([]any)) != 2 {
		t.Errorf("parsed %v", v)
	}
}

func TestParseModulePath(t *testing.T) {
	cases := map[string]string{
		"module example.com/shop\n\ngo 1.22\n":            "example.com/shop",
		"// comment\nmodule \"quoted/mod\" // trailing\n": "quoted/mod",
		"go 1.22\n":     "",
		"modulex foo\n": "",
	}
	for in, want := range cases {
		if got := parseModulePath([]byte(in)); got != want {
			t.Errorf("parseModulePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPythonAbsolute(t *testing.T) {
	py := &pyResolver{roots: []string{"src", ""}}
	mod := &fileInfo{path: "src/app/services/users.py"}
	pkg := &fileInfo{path: "src/app/services/__init__.py"}
	cases := []struct {
		f        *fileInfo
		in, want string
	}{
		{mod, "os.path", "os.path"},
		{mod, ".", "app.services"},
		{mod, ".helpers", "app.services.helpers"},
		{mod, "..models", "app.models"},
		{mod, "...", ""},
		{mod, "....x", ""},
		{pkg, ".", "app.services"},
		{pkg, "..models", "app.models"},
	}
	for _, c := range cases {
		if got := py.absolute(c.f, c.in); got != c.want {
			t.Errorf("absolute(%s, %q) = %q, want %q", c.f.path, c.in, got, c.want)
		}
	}
}

func TestParseParams(t *testing.T) {
	cases := []struct {
		fam, sig string
		want     map[string]string
	}{
		{"go", "func (h *Handler) Show(id int, repo store.Repository) string",
			map[string]string{"h": "*Handler", "id": "int", "repo": "store.Repository"}},
		{"go", "func Merge[T any](a, b []T, opts ...Option) []T",
			map[string]string{"a": "[]T", "b": "[]T", "opts": "Option"}},
		{"go", "func TestX(t *testing.T)", map[string]string{"t": "*testing.T"}},
		{"python", "def get(self, user_id: int, repo: Optional[Repo] = None, *args) -> User",
			map[string]string{"user_id": "int", "repo": "Optional[Repo]"}},
		{"ecma", "constructor(private readonly repo: Repo, cb?: (x: number) => void)",
			map[string]string{"repo": "Repo", "cb": "(x: number) => void"}},
		{"ecma", "const formatUser = (u: User): string", map[string]string{"u": "User"}},
		{"ecma", "function f(a, b)", map[string]string{}},
	}
	for _, c := range cases {
		got := parseParams(c.fam, c.sig)
		if len(got) != len(c.want) {
			t.Errorf("%s: got %v, want %v", c.sig, got, c.want)
			continue
		}
		for k, v := range c.want {
			if got[k] != v {
				t.Errorf("%s: %s = %q, want %q", c.sig, k, got[k], v)
			}
		}
	}
	for in, want := range map[string][2]string{
		"*store.User": {"User", "store"}, "Optional[Repo]": {"Repo", ""}, "User | undefined": {"User", ""},
		"Map<string, User>": {"Map", ""}, "[]T": {"T", ""}, "{ name: string }": {"", ""},
	} {
		if b, q := typeName(in); b != want[0] || q != want[1] {
			t.Errorf("typeName(%q) = %q, %q", in, b, q)
		}
	}
}
