package parse

import (
	"embed"
	"fmt"
	"path"
	"strings"
	"sync"

	sitter "github.com/tree-sitter/go-tree-sitter"
	tsgo "github.com/tree-sitter/tree-sitter-go/bindings/go"
	tsjs "github.com/tree-sitter/tree-sitter-javascript/bindings/go"
	tspy "github.com/tree-sitter/tree-sitter-python/bindings/go"
	tsts "github.com/tree-sitter/tree-sitter-typescript/bindings/go"
)

//go:embed queries/*.scm
var queryFS embed.FS

// hooks hold the per-language quirks the shared query walker can't express.
type hooks interface {
	// module returns the module symbol's name and qualified name, and the
	// prefix prepended to qualified names of top-level symbols.
	module(f *file) (name, qname, prefix string)
	// symbol fixes up kind, exported, container and signature. Returning
	// false drops the definition (its children are re-parented).
	symbol(f *file, d *rawDef, parent *rawDef) bool
	// imports converts an @import node into import refs.
	imports(f *file, n *sitter.Node) []Ref
	// skipCall reports call names not worth storing (builtins).
	skipCall(name string) bool
}

// language is one registered grammar. Several extensions may share a
// language name (e.g. .ts and .tsx are both "typescript").
type language struct {
	name    string
	exts    []string
	queryFn string
	grammar func() *sitter.Language
	hooks   hooks

	once  sync.Once
	lang  *sitter.Language
	query *sitter.Query
	err   error
}

// load compiles the grammar's query once. Queries are immutable and safe to
// share across goroutines; they live for the process lifetime.
func (l *language) load() error {
	l.once.Do(func() {
		l.lang = l.grammar()
		src, err := queryFS.ReadFile("queries/" + l.queryFn)
		if err != nil {
			l.err = err
			return
		}
		q, qerr := sitter.NewQuery(l.lang, string(src))
		if qerr != nil {
			l.err = fmt.Errorf("%s query: %s", l.queryFn, qerr.Error())
			return
		}
		l.query = q
	})
	return l.err
}

var languages = []*language{
	{name: "go", exts: []string{".go"}, queryFn: "go.scm", hooks: goHooks{},
		grammar: func() *sitter.Language { return sitter.NewLanguage(tsgo.Language()) }},
	{name: "python", exts: []string{".py", ".pyi"}, queryFn: "python.scm", hooks: pyHooks{},
		grammar: func() *sitter.Language { return sitter.NewLanguage(tspy.Language()) }},
	{name: "typescript", exts: []string{".ts", ".mts", ".cts"}, queryFn: "typescript.scm", hooks: ecmaHooks{},
		grammar: func() *sitter.Language { return sitter.NewLanguage(tsts.LanguageTypescript()) }},
	{name: "typescript", exts: []string{".tsx"}, queryFn: "typescript.scm", hooks: ecmaHooks{},
		grammar: func() *sitter.Language { return sitter.NewLanguage(tsts.LanguageTSX()) }},
	{name: "javascript", exts: []string{".js", ".jsx", ".mjs", ".cjs"}, queryFn: "javascript.scm", hooks: ecmaHooks{},
		grammar: func() *sitter.Language { return sitter.NewLanguage(tsjs.Language()) }},
}

var byExt = func() map[string]*language {
	m := map[string]*language{}
	for _, l := range languages {
		for _, e := range l.exts {
			m[e] = l
		}
	}
	return m
}()

func lookup(p string) *language {
	if strings.HasSuffix(p, ".d.ts") {
		return nil // declaration files are generated or vendored type stubs
	}
	return byExt[strings.ToLower(path.Ext(p))]
}

// Detect returns the language name for a path, if supported.
func Detect(p string) (string, bool) {
	if l := lookup(p); l != nil {
		return l.name, true
	}
	return "", false
}

// Languages lists the supported language names.
func Languages() []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range languages {
		if !seen[l.name] {
			seen[l.name] = true
			out = append(out, l.name)
		}
	}
	return out
}

// IsTestPath reports whether a path is a test file by its language's naming
// convention: Go *_test.go; Python test_*.py, *_test.py, conftest.py;
// JS/TS *.test.*, *.spec.* or anything under __tests__/.
func IsTestPath(p string) bool {
	lang, ok := Detect(p)
	if !ok {
		return false
	}
	base := path.Base(p)
	switch lang {
	case "go":
		return strings.HasSuffix(base, "_test.go")
	case "python":
		return strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.py") || base == "conftest.py"
	default:
		return strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") ||
			strings.Contains("/"+p, "/__tests__/")
	}
}
