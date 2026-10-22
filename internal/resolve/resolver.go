package resolve

import (
	"strings"

	"github.com/gayanmadurapperuma/codegraph/internal/config"
	"github.com/gayanmadurapperuma/codegraph/internal/parse"
)

// Confidence weights (D12). Exact resolutions through scope, bindings or a
// known receiver get 1.0; guesses get less so ranking can discount them.
const (
	wExact       = 1.0
	wReceiver1   = 0.5  // unknown receiver, one candidate method in reach
	wReceiverFew = 0.25 // unknown receiver, 2-3 candidates in reach
	wGlobal      = 0.3  // name unique in the whole repo
	wTestImport  = 0.5  // test file imports a module
	maxReceiver  = 3
	minGlobalLen = 4
	maxDepth     = 3 // re-export chains and base-class walks
)

type kindSet map[parse.Kind]bool

var (
	callKinds  = kindSet{parse.KindFunction: true, parse.KindMethod: true, parse.KindClass: true, parse.KindType: true}
	classKinds = kindSet{parse.KindClass: true, parse.KindInterface: true, parse.KindType: true}
)

type target struct {
	sym *sym
	w   float64
}

type binding struct {
	files []*fileInfo // module (or Go package) binding
	sym   *sym        // symbol binding
}

// scope is everything a file's imports make visible.
type scope struct {
	f        *fileInfo
	bind     map[string]binding
	imported []*fileInfo            // direct import targets, deduplicated
	importW  map[int64]float64      // IMPORTS edge weight per target file
	star     []*fileInfo            // star imports (Python *, Go dot imports)
	reexport map[string][]*fileInfo // ECMA `export { x } from "./y"`
	reStar   []*fileInfo            // ECMA `export * from "./y"`
	pkg      []*fileInfo            // Go: files of the same package, incl. f
	external map[string]bool        // local names bound by imports from outside the repo
	imports  int                    // IMPORTS refs seen
	resolved int                    // IMPORTS refs that resolved to a file
	building bool
}

func (s *scope) addImport(files []*fileInfo, w float64) {
	for _, f := range files {
		if f.id == s.f.id {
			continue
		}
		if _, ok := s.importW[f.id]; !ok {
			s.imported = append(s.imported, f)
		}
		s.importW[f.id] = max(s.importW[f.id], w)
	}
}

type resolver struct {
	proj   *config.Project
	t      *symtab
	scopes map[int64]*scope
	bases  map[int64][]*sym
	params map[int64]map[string]*sym // per function: typed parameter → resolved type (nil: external)
	gor    *goResolver
	py     *pyResolver
	ecma   *ecmaResolver
}

func newResolver(p *config.Project, t *symtab, markers []string) *resolver {
	r := &resolver{proj: p, t: t, scopes: map[int64]*scope{}, bases: map[int64][]*sym{},
		params: map[int64]map[string]*sym{}}
	r.gor = newGoResolver(p, t, markers)
	r.py = newPyResolver(p, t, markers)
	r.ecma = newEcmaResolver(p, t, markers)
	return r
}

// scopeOf builds (and memoizes) a file's import scope. Import cycles see a
// partially built scope, which only makes resolution more conservative.
func (r *resolver) scopeOf(f *fileInfo) *scope {
	if s, ok := r.scopes[f.id]; ok {
		return s
	}
	s := &scope{f: f, bind: map[string]binding{}, importW: map[int64]float64{},
		reexport: map[string][]*fileInfo{}, external: map[string]bool{}, building: true}
	r.scopes[f.id] = s
	for _, rf := range r.t.refsOf(f.id) {
		if rf.typ != parse.EdgeImports {
			continue
		}
		s.imports++
		var ok bool
		switch f.family {
		case "go":
			ok = r.gor.bindImport(r, s, rf)
		case "python":
			ok = r.py.bindImport(r, s, rf)
		default:
			ok = r.ecma.bindImport(r, s, rf)
		}
		if ok {
			s.resolved++
		} else if q := rf.qualifier; q != "" && q != "_" && q != "." {
			// stdlib / third-party: calls through it must not be guessed into the repo
			s.external[q] = true
			if rf.member == "" {
				s.external[firstSegment(q)] = true // import os.path → os.getcwd()
			}
		}
	}
	if f.family == "go" {
		s.pkg = r.gor.packageFiles(f)
	}
	s.building = false
	return s
}

// topLevel returns the symbol name defined at module level of f.
func (r *resolver) topLevel(f *fileInfo, name string, kinds kindSet) *sym {
	for _, s := range r.t.byName(name) {
		if s.file == f.id && s.parent == f.module && (kinds == nil || kinds[s.kind]) {
			return s
		}
	}
	return nil
}

// member finds name exported by any of the module files, following
// re-exports up to maxDepth.
func (r *resolver) member(files []*fileInfo, name string, depth int) *sym {
	if depth <= 0 {
		return nil
	}
	for _, f := range files {
		if f.family == "go" {
			if s := r.topLevel(f, name, nil); s != nil && s.kind != parse.KindMethod && s.exported {
				return s
			}
			continue
		}
		if s := r.topLevel(f, name, nil); s != nil {
			return s
		}
		sc := r.scopeOf(f)
		if b, ok := sc.bind[name]; ok && b.sym != nil {
			return b.sym
		}
		if s := r.member(sc.reexport[name], name, depth-1); s != nil {
			return s
		}
		if s := r.member(sc.reStar, name, depth-1); s != nil {
			return s
		}
		if f.family == "python" {
			if s := r.member(sc.star, name, depth-1); s != nil {
				return s
			}
		}
	}
	return nil
}

// methodIn finds method name on class c or its bases.
func (r *resolver) methodIn(c *sym, name string, depth int) *sym {
	if c == nil || depth <= 0 {
		return nil
	}
	cf := r.t.byID[c.file]
	for _, s := range r.t.byName(name) {
		if s.kind != parse.KindMethod || s.qname != c.qname+"."+name {
			continue
		}
		if s.file == c.file || (cf.family == "go" && r.t.byID[s.file] != nil && r.t.byID[s.file].dir == cf.dir) {
			return s
		}
	}
	for _, b := range r.basesOf(c) {
		if m := r.methodIn(b, name, depth-1); m != nil {
			return m
		}
	}
	return nil
}

// basesOf resolves a class's EXTENDS/IMPLEMENTS refs.
func (r *resolver) basesOf(c *sym) []*sym {
	if bs, ok := r.bases[c.id]; ok {
		return bs
	}
	r.bases[c.id] = nil // cycle guard
	f := r.t.byID[c.file]
	var out []*sym
	for _, rf := range r.t.refsOf(c.file) {
		if rf.src != c.id || (rf.typ != parse.EdgeExtends && rf.typ != parse.EdgeImplements) {
			continue
		}
		for _, t := range r.resolveName(r.scopeOf(f), c, rf, classKinds) {
			if t.w == wExact && t.sym.id != c.id {
				out = append(out, t.sym)
			}
		}
	}
	r.bases[c.id] = out
	return out
}

// enclosingClass is the class (or Go receiver type) a symbol belongs to.
func (r *resolver) enclosingClass(sc *scope, s *sym) *sym {
	if sc.f.family == "go" {
		if s.kind != parse.KindMethod || len(s.qname) <= len(s.name)+1 {
			return nil
		}
		tq := s.qname[:len(s.qname)-len(s.name)-1]
		tn := tq
		if i := lastDot(tq); i >= 0 {
			tn = tq[i+1:]
		}
		for _, c := range r.t.byName(tn) {
			if c.qname == tq && c.kind != parse.KindMethod && inFiles(c.file, sc.pkg) {
				return c
			}
		}
		return nil
	}
	r.t.symbolsOf(s.file)
	for p := s; p != nil; p = r.t.sym(p.parent) {
		if p.kind == parse.KindClass || p.kind == parse.KindInterface {
			return p
		}
	}
	return nil
}

func (r *resolver) isSelf(sc *scope, src *sym, q string) bool {
	switch sc.f.family {
	case "go":
		return src.recv != "" && q == src.recv
	case "python":
		return q == "self" || q == "cls" || q == "super"
	default:
		return q == "this" || q == "super"
	}
}

// lexical finds an unqualified name in the file's own scopes.
func (r *resolver) lexical(sc *scope, src *sym, name string, kinds kindSet) *sym {
	if sc.f.family == "go" {
		for _, s := range r.t.byName(name) {
			if !kinds[s.kind] || s.kind == parse.KindMethod {
				continue
			}
			if f := r.t.byID[s.file]; f != nil && s.parent == f.module && inFiles(s.file, sc.pkg) {
				return s
			}
		}
		return nil
	}
	r.t.symbolsOf(sc.f.id)
	for a := src; a != nil; a = r.t.sym(a.parent) {
		if a.kind == parse.KindClass && a.id != src.id {
			continue // class bodies are not enclosing scopes for their methods
		}
		for _, c := range r.t.childrenOf(a) {
			if c.name == name && kinds[c.kind] {
				return c
			}
		}
		if a.parent == 0 {
			break
		}
	}
	return nil
}

// resolveName resolves a CALLS/EXTENDS/IMPLEMENTS ref following D12.
func (r *resolver) resolveName(sc *scope, src *sym, rf ref, kinds kindSet) []target {
	q, n := rf.qualifier, rf.name
	if q == "" {
		if s := r.lexical(sc, src, n, kinds); s != nil {
			return []target{{s, wExact}}
		}
		if b, ok := sc.bind[n]; ok && b.sym != nil && kinds[b.sym.kind] {
			return []target{{b.sym, wExact}}
		}
		if sc.external[n] {
			return nil
		}
		if s := r.member(sc.star, n, maxDepth); s != nil && kinds[s.kind] {
			return []target{{s, wExact}}
		}
		return r.global(sc, n, kinds)
	}
	if r.isSelf(sc, src, q) {
		if c := r.enclosingClass(sc, src); c != nil {
			var m *sym
			if q == "super" {
				for _, b := range r.basesOf(c) {
					if m = r.methodIn(b, n, maxDepth); m != nil {
						break
					}
				}
			} else {
				m = r.methodIn(c, n, maxDepth)
			}
			if m != nil {
				return []target{{m, wExact}}
			}
		}
		return r.receiverGuess(sc, n, kinds)
	}
	if typ, typed := r.paramType(sc, src, q); typed {
		// A declared parameter type is authoritative: resolve on it or not at all.
		if typ != nil {
			if m := r.methodIn(typ, n, maxDepth); m != nil && kinds[m.kind] {
				return []target{{m, wExact}}
			}
		}
		return nil
	}
	if b, ok := sc.bind[q]; ok {
		switch {
		case len(b.files) > 0:
			if s := r.member(b.files, n, maxDepth); s != nil && kinds[s.kind] {
				return []target{{s, wExact}}
			}
		case b.sym != nil:
			if m := r.methodIn(b.sym, n, maxDepth); m != nil && kinds[m.kind] {
				return []target{{m, wExact}}
			}
		}
		return nil // bound to a known module or symbol that lacks the member: don't guess
	}
	if sc.external[q] || sc.external[firstSegment(q)] {
		return nil
	}
	return r.receiverGuess(sc, n, kinds)
}

// receiverGuess handles obj.method() with an unknown receiver type: methods
// of that name in the file, its package and the files it imports.
//
// There is deliberately no repo-wide fallback here: method names collide
// with stdlib and third-party APIs (Lock, Run, get, ...) far too often.
func (r *resolver) receiverGuess(sc *scope, name string, kinds kindSet) []target {
	if !kinds[parse.KindMethod] || commonMethods[sc.f.family][name] ||
		(strings.HasPrefix(name, "__") && strings.HasSuffix(name, "__")) {
		return nil
	}
	reach := map[int64]bool{sc.f.id: true}
	for _, f := range sc.imported {
		reach[f.id] = true
	}
	for _, f := range sc.pkg {
		reach[f.id] = true
	}
	var cands []*sym
	for _, s := range r.t.byName(name) {
		if s.kind == parse.KindMethod && reach[s.file] {
			cands = append(cands, s)
		}
	}
	switch {
	case len(cands) == 1:
		return []target{{cands[0], wReceiver1}}
	case len(cands) > 1 && len(cands) <= maxReceiver:
		out := make([]target, len(cands))
		for i, c := range cands {
			out[i] = target{c, wReceiverFew}
		}
		return out
	}
	return nil
}

// global resolves a name that is unique across the repo (same language
// family), as a last resort.
//
// Go never uses it: an unqualified Go name can only be package-scoped (or a
// local/builtin), so a match in another package is always wrong.
func (r *resolver) global(sc *scope, name string, kinds kindSet) []target {
	if len(name) < minGlobalLen || sc.f.family == "go" {
		return nil
	}
	var found *sym
	for _, s := range r.t.byName(name) {
		f := r.t.byID[s.file]
		if f == nil || f.family != sc.f.family || !kinds[s.kind] || s.kind == parse.KindMethod {
			continue
		}
		if found != nil {
			return nil
		}
		found = s
	}
	if found == nil {
		return nil
	}
	return []target{{found, wGlobal}}
}

func inFiles(id int64, files []*fileInfo) bool {
	for _, f := range files {
		if f.id == id {
			return true
		}
	}
	return false
}

func firstSegment(q string) string {
	if i := strings.IndexByte(q, '.'); i >= 0 {
		return q[:i]
	}
	return q
}

func lastDot(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '.' {
			return i
		}
	}
	return -1
}

// commonMethods are method names so common on builtin/stdlib types (dict.get,
// Array.push, Mutex.Lock, ...) that guessing a repo method for an unknown
// receiver would mostly be wrong. Exact resolutions are unaffected.
var commonMethods = map[string]map[string]bool{
	"go": setOf("Close", "Error", "String", "Len", "Write", "Read", "Lock", "Unlock", "RLock", "RUnlock",
		"Add", "Done", "Wait", "Run", "Get", "Set", "Load", "Store", "Delete", "Reset", "Next", "Err",
		"Value", "Key", "Bytes", "Fatal", "Fatalf", "Errorf", "Log", "Logf", "Helper", "Cleanup",
		"Context", "Seek", "Sync", "Stat", "Name", "Format", "Sum", "Equal", "Contains"),
	"python": setOf("get", "set", "items", "keys", "values", "append", "extend", "pop", "update", "add",
		"remove", "discard", "join", "split", "strip", "lstrip", "rstrip", "format", "replace", "startswith",
		"endswith", "lower", "upper", "copy", "clear", "insert", "index", "count", "sort", "read", "write",
		"close", "open", "encode", "decode", "setdefault", "group", "match", "search", "sub", "exists",
		"filter", "exclude", "all", "first", "last", "create", "save", "delete", "assertEqual", "assertTrue",
		"assertFalse", "assertIn", "assertIs", "assertIsNone", "assertRaises", "isoformat", "title"),
	"ecma": setOf("push", "pop", "shift", "unshift", "map", "filter", "reduce", "forEach", "find", "some",
		"every", "includes", "indexOf", "join", "split", "slice", "splice", "concat", "sort", "keys", "values",
		"entries", "get", "set", "has", "delete", "add", "clear", "then", "catch", "finally", "toString",
		"trim", "replace", "match", "test", "startsWith", "endsWith", "toLowerCase", "toUpperCase", "log",
		"error", "warn", "info", "debug", "emit", "on", "off", "call", "apply", "bind", "json", "send"),
}

func setOf(xs ...string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}
