package resolve

import (
	"path"
	"sort"
	"strings"

	"github.com/gayanmadurapperuma/uxor/internal/config"
)

type pyResolver struct {
	t     *symtab
	roots []string // source roots, longest first; "" is the project root
	mods  map[string][]*fileInfo
}

func newPyResolver(p *config.Project, t *symtab, markers []string) *pyResolver {
	seen := map[string]bool{"": true}
	roots := []string{""}
	add := func(r string) {
		r = strings.Trim(path.Clean(strings.TrimPrefix(r, "./")), "/")
		if r == "." {
			r = ""
		}
		if !seen[r] {
			seen[r] = true
			roots = append(roots, r)
		}
	}
	hasDir := func(dir string) bool {
		for _, f := range t.files {
			if f.family == "python" && strings.HasPrefix(f.path, dir+"/") {
				return true
			}
		}
		return false
	}
	if hasDir("src") {
		add("src")
	}
	for _, m := range markers {
		switch path.Base(m) {
		case "pyproject.toml", "setup.py", "setup.cfg":
			d := path.Dir(m)
			add(d)
			if hasDir(path.Join(d, "src")) {
				add(path.Join(d, "src"))
			}
		}
	}
	for _, r := range p.Config.Resolve.PythonRoots {
		add(r)
	}
	sort.Slice(roots, func(i, j int) bool { return len(roots[i]) > len(roots[j]) })
	py := &pyResolver{t: t, roots: roots, mods: map[string][]*fileInfo{}}
	for _, f := range t.files {
		if f.family != "python" {
			continue
		}
		for _, r := range roots {
			if mod, ok := py.dotted(f.path, r); ok {
				py.mods[mod] = append(py.mods[mod], f)
			}
		}
	}
	return py
}

// dotted returns the module name of a file relative to a source root.
func (py *pyResolver) dotted(p, root string) (string, bool) {
	rel := p
	if root != "" {
		var ok bool
		if rel, ok = strings.CutPrefix(p, root+"/"); !ok {
			return "", false
		}
	}
	rel = strings.TrimSuffix(strings.TrimSuffix(rel, ".pyi"), ".py")
	if rel == "__init__" {
		return "", false
	}
	rel = strings.TrimSuffix(rel, "/__init__")
	return strings.ReplaceAll(rel, "/", "."), true
}

// moduleOf is f's dotted name under its innermost source root.
func (py *pyResolver) moduleOf(f *fileInfo) string {
	for _, r := range py.roots {
		if m, ok := py.dotted(f.path, r); ok {
			return m
		}
	}
	return ""
}

// absolute turns a possibly relative import ("..models") into a dotted
// module name, or "" if it climbs above the source root.
func (py *pyResolver) absolute(f *fileInfo, mod string) string {
	dots := len(mod) - len(strings.TrimLeft(mod, "."))
	if dots == 0 {
		return mod
	}
	pkg := py.moduleOf(f)
	if !strings.HasSuffix(f.path, "__init__.py") {
		pkg = parentModule(pkg)
	}
	for i := 1; i < dots; i++ {
		if pkg == "" {
			return ""
		}
		pkg = parentModule(pkg)
	}
	rest := mod[dots:]
	switch {
	case pkg == "":
		return rest
	case rest == "":
		return pkg
	default:
		return pkg + "." + rest
	}
}

func parentModule(m string) string {
	if i := strings.LastIndex(m, "."); i >= 0 {
		return m[:i]
	}
	return ""
}

func (py *pyResolver) bindImport(r *resolver, s *scope, rf ref) bool {
	mod := py.absolute(s.f, rf.name)
	if mod == "" {
		return false
	}
	switch rf.member {
	case "": // import a.b [as x]
		files := py.mods[mod]
		if len(files) == 0 {
			return false
		}
		s.addImport(files, wExact)
		s.bind[rf.qualifier] = binding{files: files}
	case "*":
		files := py.mods[mod]
		if len(files) == 0 {
			return false
		}
		s.addImport(files, wExact)
		s.star = append(s.star, files...)
	default: // from mod import member [as qualifier]
		if sub := py.mods[mod+"."+rf.member]; len(sub) > 0 {
			s.addImport(sub, wExact)
			s.bind[rf.qualifier] = binding{files: sub}
			return true
		}
		files := py.mods[mod]
		if len(files) == 0 {
			return false
		}
		s.addImport(files, wExact)
		if m := r.member(files, rf.member, maxDepth); m != nil {
			s.bind[rf.qualifier] = binding{sym: m}
		}
	}
	return true
}
