package resolve

import (
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gayanmadurapperuma/uxor/internal/config"
	"github.com/gayanmadurapperuma/uxor/internal/parse"
)

var ecmaExts = []string{".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs"}

// tsConfig is the merged, root-relative view of a tsconfig/jsconfig.
type tsConfig struct {
	baseURL   string // root-relative; valid when hasBase
	hasBase   bool
	paths     []tsPath
	pathsBase string // directory path targets are relative to
}

type tsPath struct {
	prefix, suffix string // pattern split at "*"; wildcard false for exact patterns
	wildcard       bool
	targets        []string
}

type workspacePkg struct {
	name, dir string
	entries   []string // candidate entry files from package.json fields
}

type ecmaResolver struct {
	p        *config.Project
	t        *symtab
	markers  map[string]bool
	configs  map[string]*tsConfig // by config path
	nearest  map[string]*tsConfig // by directory
	packages []workspacePkg       // longest name first
}

func newEcmaResolver(p *config.Project, t *symtab, markers []string) *ecmaResolver {
	e := &ecmaResolver{p: p, t: t, markers: map[string]bool{},
		configs: map[string]*tsConfig{}, nearest: map[string]*tsConfig{}}
	for _, m := range markers {
		e.markers[m] = true
		if path.Base(m) == "package.json" {
			if pkg, ok := e.readPackage(m); ok {
				e.packages = append(e.packages, pkg)
			}
		}
	}
	sort.Slice(e.packages, func(i, j int) bool { return len(e.packages[i].name) > len(e.packages[j].name) })
	return e
}

func (e *ecmaResolver) read(rel string) ([]byte, bool) {
	if _, err := e.p.Confine(rel); err != nil {
		return nil, false
	}
	b, err := os.ReadFile(filepath.Join(e.p.Root, filepath.FromSlash(rel)))
	return b, err == nil
}

func (e *ecmaResolver) readPackage(rel string) (workspacePkg, bool) {
	b, ok := e.read(rel)
	if !ok {
		return workspacePkg{}, false
	}
	var pj struct {
		Name   string `json:"name"`
		Main   string `json:"main"`
		Module string `json:"module"`
		Types  string `json:"types"`
		Source string `json:"source"`
	}
	if json.Unmarshal(stripJSONC(b), &pj) != nil || pj.Name == "" {
		return workspacePkg{}, false
	}
	pkg := workspacePkg{name: pj.Name, dir: path.Dir(rel)}
	for _, ent := range []string{pj.Source, pj.Types, pj.Module, pj.Main} {
		if ent == "" {
			continue
		}
		ent = strings.TrimPrefix(path.Clean(ent), "./")
		pkg.entries = append(pkg.entries, ent)
		for _, out := range []string{"dist/", "lib/", "build/", "out/"} {
			if rest, ok := strings.CutPrefix(ent, out); ok {
				pkg.entries = append(pkg.entries, "src/"+rest) // built output usually mirrors src/
			}
		}
	}
	pkg.entries = append(pkg.entries, "src/index", "index")
	return pkg, true
}

// config loads a tsconfig and its relative `extends` chain.
func (e *ecmaResolver) config(rel string, depth int) *tsConfig {
	if c, ok := e.configs[rel]; ok {
		return c
	}
	e.configs[rel] = nil // cycle guard
	b, ok := e.read(rel)
	if !ok || depth > 5 {
		return nil
	}
	var raw struct {
		Extends         json.RawMessage `json:"extends"`
		CompilerOptions struct {
			BaseURL *string             `json:"baseUrl"`
			Paths   map[string][]string `json:"paths"`
		} `json:"compilerOptions"`
	}
	if json.Unmarshal(stripJSONC(b), &raw) != nil {
		return nil
	}
	dir := path.Dir(rel)
	merged := &tsConfig{}
	var parents []string
	var one string
	if json.Unmarshal(raw.Extends, &one) == nil && one != "" {
		parents = []string{one}
	} else {
		json.Unmarshal(raw.Extends, &parents)
	}
	for _, ext := range parents {
		if !strings.HasPrefix(ext, ".") {
			continue // package-provided base configs live in node_modules
		}
		pr := path.Join(dir, ext)
		if !strings.HasSuffix(pr, ".json") {
			pr += ".json"
		}
		if pc := e.config(pr, depth+1); pc != nil {
			*merged = *pc
		}
	}
	if raw.CompilerOptions.BaseURL != nil {
		merged.baseURL, merged.hasBase = path.Join(dir, *raw.CompilerOptions.BaseURL), true
	}
	pathsDir := merged.pathsBase
	if raw.CompilerOptions.Paths != nil {
		merged.paths = nil
		for pat, targets := range raw.CompilerOptions.Paths {
			tp := tsPath{targets: targets}
			if pre, suf, ok := strings.Cut(pat, "*"); ok {
				tp.prefix, tp.suffix, tp.wildcard = pre, suf, true
			} else {
				tp.prefix = pat
			}
			merged.paths = append(merged.paths, tp)
		}
		// TypeScript prefers the longest matching prefix.
		sort.Slice(merged.paths, func(i, j int) bool { return len(merged.paths[i].prefix) > len(merged.paths[j].prefix) })
		pathsDir = dir
	}
	if merged.hasBase {
		merged.pathsBase = merged.baseURL
	} else {
		merged.pathsBase = pathsDir
	}
	e.configs[rel] = merged
	return merged
}

// nearestConfig finds the closest tsconfig.json / jsconfig.json above dir.
func (e *ecmaResolver) nearestConfig(dir string) *tsConfig {
	if c, ok := e.nearest[dir]; ok {
		return c
	}
	var c *tsConfig
	for _, name := range []string{"tsconfig.json", "jsconfig.json"} {
		if p := path.Join(dir, name); e.markers[p] {
			if c = e.config(p, 0); c != nil {
				break
			}
		}
	}
	if c == nil && dir != "." && dir != "/" {
		c = e.nearestConfig(path.Dir(dir))
	}
	e.nearest[dir] = c
	return c
}

// probe finds the indexed file an extensionless or JS-extension path
// refers to, the way TypeScript's bundler resolution does.
func (e *ecmaResolver) probe(p string) *fileInfo {
	p = path.Clean(p)
	if p == ".." || strings.HasPrefix(p, "../") {
		return nil
	}
	ok := func(c string) *fileInfo {
		if f := e.t.byPath[c]; f != nil && f.family == "ecma" {
			return f
		}
		return nil
	}
	if f := ok(p); f != nil {
		return f
	}
	if ext := path.Ext(p); ext == ".js" || ext == ".jsx" || ext == ".mjs" || ext == ".cjs" {
		stem := strings.TrimSuffix(p, ext)
		for _, x := range []string{".ts", ".tsx", ".mts", ".cts"} {
			if f := ok(stem + x); f != nil {
				return f
			}
		}
	}
	for _, x := range ecmaExts {
		if f := ok(p + x); f != nil {
			return f
		}
	}
	for _, x := range ecmaExts {
		if f := ok(p + "/index" + x); f != nil {
			return f
		}
	}
	return nil
}

// resolveSpec maps an import specifier to a file in the repo.
func (e *ecmaResolver) resolveSpec(from *fileInfo, spec string) *fileInfo {
	if spec == "." || spec == ".." || strings.HasPrefix(spec, "./") || strings.HasPrefix(spec, "../") {
		return e.probe(path.Join(from.dir, spec))
	}
	if strings.HasPrefix(spec, "/") {
		return nil
	}
	if c := e.nearestConfig(from.dir); c != nil {
		for _, tp := range c.paths {
			var star string
			switch {
			case tp.wildcard && strings.HasPrefix(spec, tp.prefix) && strings.HasSuffix(spec[len(tp.prefix):], tp.suffix):
				star = strings.TrimSuffix(spec[len(tp.prefix):], tp.suffix)
			case !tp.wildcard && spec == tp.prefix:
			default:
				continue
			}
			for _, tgt := range tp.targets {
				if f := e.probe(path.Join(c.pathsBase, strings.Replace(tgt, "*", star, 1))); f != nil {
					return f
				}
			}
			break
		}
		if c.hasBase {
			if f := e.probe(path.Join(c.baseURL, spec)); f != nil {
				return f
			}
		}
	}
	for _, pkg := range e.packages {
		if spec != pkg.name && !strings.HasPrefix(spec, pkg.name+"/") {
			continue
		}
		if sub := strings.TrimPrefix(strings.TrimPrefix(spec, pkg.name), "/"); sub != "" {
			for _, c := range []string{path.Join(pkg.dir, sub), path.Join(pkg.dir, "src", sub)} {
				if f := e.probe(c); f != nil {
					return f
				}
			}
			return nil
		}
		for _, ent := range pkg.entries {
			cand := path.Join(pkg.dir, ent)
			if f := e.probe(strings.TrimSuffix(cand, path.Ext(cand))); f != nil && path.Ext(ent) != "" {
				return f
			}
			if f := e.probe(cand); f != nil {
				return f
			}
		}
		return nil
	}
	return nil
}

// defaultExport guesses a module's default export: a top-level symbol
// named like the local binding, else its only exported top-level symbol.
func (r *resolver) defaultExport(f *fileInfo, local string) *sym {
	if s := r.topLevel(f, local, nil); s != nil {
		return s
	}
	var only *sym
	for _, s := range r.t.symbolsOf(f.id) {
		if s.parent != f.module || !s.exported || s.kind == parse.KindModule {
			continue
		}
		if only != nil {
			return nil
		}
		only = s
	}
	return only
}

func (e *ecmaResolver) bindImport(r *resolver, s *scope, rf ref) bool {
	f := e.resolveSpec(s.f, rf.name)
	if f == nil {
		return false
	}
	files := []*fileInfo{f}
	s.addImport(files, wExact)
	q, m := rf.qualifier, rf.member
	switch {
	case q == "" && m == "": // side-effect import
	case q == "" && m == "*":
		s.reStar = append(s.reStar, f)
	case q == "":
		s.reexport[m] = append(s.reexport[m], f)
	case m == "*":
		s.bind[q] = binding{files: files}
	case m == "default":
		if d := r.defaultExport(f, q); d != nil {
			s.bind[q] = binding{sym: d}
		} else {
			s.bind[q] = binding{files: files}
		}
	default:
		if sym := r.member(files, m, maxDepth); sym != nil {
			s.bind[q] = binding{sym: sym}
		}
	}
	return true
}
