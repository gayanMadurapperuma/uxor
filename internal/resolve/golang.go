package resolve

import (
	"bufio"
	"bytes"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gayanmadurapperuma/codegraph/internal/config"
)

type goModule struct {
	dir     string // root-relative directory of go.mod ("." for the root)
	modPath string
}

type goResolver struct {
	t       *symtab
	modules []goModule // longest module path first
}

func newGoResolver(p *config.Project, t *symtab, markers []string) *goResolver {
	g := &goResolver{t: t}
	for _, m := range markers {
		if path.Base(m) != "go.mod" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(p.Root, filepath.FromSlash(m)))
		if err != nil {
			continue
		}
		if mp := parseModulePath(b); mp != "" {
			g.modules = append(g.modules, goModule{dir: path.Dir(m), modPath: mp})
		}
	}
	sort.Slice(g.modules, func(i, j int) bool { return len(g.modules[i].modPath) > len(g.modules[j].modPath) })
	return g
}

// parseModulePath extracts the module path from go.mod content.
func parseModulePath(b []byte) string {
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if i := strings.Index(line, "//"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if rest, ok := strings.CutPrefix(line, "module"); ok && (rest == "" || rest[0] == ' ' || rest[0] == '\t') {
			return strings.Trim(strings.TrimSpace(rest), `"`+"`")
		}
	}
	return ""
}

// packageDir maps an import path to a root-relative directory inside one of
// the repo's modules.
func (g *goResolver) packageDir(importPath string) (string, bool) {
	for _, m := range g.modules {
		if importPath == m.modPath || strings.HasPrefix(importPath, m.modPath+"/") {
			return path.Clean(path.Join(m.dir, strings.TrimPrefix(importPath, m.modPath))), true
		}
	}
	return "", false
}

// importable returns the non-test Go files of a package directory.
func (g *goResolver) importable(dir string) []*fileInfo {
	var out []*fileInfo
	for _, f := range g.t.byDir[dir] {
		if f.family == "go" && !f.isTest {
			out = append(out, f)
		}
	}
	return out
}

// packageFiles are the files sharing f's package (same dir and package
// name; external foo_test packages are their own package).
func (g *goResolver) packageFiles(f *fileInfo) []*fileInfo {
	var out []*fileInfo
	for _, o := range g.t.byDir[f.dir] {
		if o.family == "go" && o.modName == f.modName {
			out = append(out, o)
		}
	}
	return out
}

func (g *goResolver) bindImport(r *resolver, s *scope, rf ref) bool {
	dir, ok := g.packageDir(rf.name)
	if !ok {
		return false
	}
	files := g.importable(dir)
	if len(files) == 0 {
		return false
	}
	// An n-file package spreads one import over n edges so PageRank and
	// impact counts aren't inflated by package size.
	s.addImport(files, 1/float64(len(files)))
	switch alias := rf.qualifier; alias {
	case "_":
	case ".":
		s.star = append(s.star, files...)
	default:
		s.bind[alias] = binding{files: files}
		if pkg := files[0].modName; pkg != alias && alias == path.Base(rf.name) {
			s.bind[pkg] = binding{files: files} // e.g. ".../v2" whose package is named differently
		}
	}
	return true
}
