package index

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/gayanmadurapperuma/codegraph/internal/config"
	"github.com/gayanmadurapperuma/codegraph/internal/parse"
)

// Entry is a candidate source file.
type Entry struct {
	Path  string // root-relative, slash-separated
	Lang  string
	Size  int64
	MTime int64
}

// Matcher decides which root-relative paths are indexed.
type Matcher struct {
	cfg     *config.Config
	exclude []string
	forget  []string
}

func NewMatcher(cfg *config.Config, forget []string) *Matcher {
	ex := append(append([]string{}, config.DefaultExcludes...), cfg.Exclude...)
	return &Matcher{cfg: cfg, exclude: ex, forget: forget}
}

// Lang returns the file's language and whether the path should be indexed.
func (m *Matcher) Lang(rel string) (string, bool) {
	lang, ok := parse.Detect(rel)
	if !ok || !m.cfg.LanguageEnabled(lang) {
		return "", false
	}
	if !matchAny(m.cfg.Include, rel) || matchAny(m.exclude, rel) || matchAny(m.forget, rel) {
		return "", false
	}
	return lang, true
}

// Excluded reports whether rel is excluded by default, config or forget
// globs, regardless of language.
func (m *Matcher) Excluded(rel string) bool {
	return matchAny(m.exclude, rel) || matchAny(m.forget, rel)
}

// matchAny matches doublestar globs; a glob naming a directory ("legacy" or
// "legacy/") also matches everything beneath it.
func matchAny(globs []string, rel string) bool {
	for _, g := range globs {
		if ok, _ := doublestar.Match(g, rel); ok {
			return true
		}
		if ok, _ := doublestar.Match(strings.TrimSuffix(g, "/")+"/**", rel); ok {
			return true
		}
	}
	return false
}

// MatchAny is matchAny for callers outside the package (forget).
func MatchAny(globs []string, rel string) bool { return matchAny(globs, rel) }

// skipDirs are pruned during filesystem walks without glob matching.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "dist": true, "build": true,
	".venv": true, "venv": true, "__pycache__": true, config.DataDirName: true, ".next": true,
	".tox": true, ".mypy_cache": true, "coverage": true,
}

// Listing is the result of a project walk.
type Listing struct {
	Entries []Entry
	// Markers are build/config files the resolver reads (go.mod,
	// tsconfig*.json, package.json, pyproject.toml, ...), root-relative.
	Markers []string
}

// IsMarker reports whether a file name is a resolver marker file.
func IsMarker(rel string) bool {
	switch base := path.Base(rel); base {
	case "go.mod", "package.json", "jsconfig.json", "pyproject.toml", "setup.py", "setup.cfg":
		return true
	default:
		return strings.HasPrefix(base, "tsconfig") && strings.HasSuffix(base, ".json")
	}
}

// Walk lists indexable files and marker files under the project root. In git
// repos it uses git's own ignore handling; otherwise it walks the filesystem.
func Walk(ctx context.Context, p *config.Project, m *Matcher) (*Listing, error) {
	rels, err := gitFiles(ctx, p.Root)
	if err != nil {
		rels, err = fsFiles(p.Root, p.Config.IncludeNested)
		if err != nil {
			return nil, err
		}
	}
	nested := nestedChecker(p)
	out := &Listing{}
	for _, rel := range rels {
		if IsMarker(rel) && !m.Excluded(rel) && !nested(rel) {
			out.Markers = append(out.Markers, rel)
		}
		lang, ok := m.Lang(rel)
		if !ok || nested(rel) {
			continue
		}
		e, ok := stat(p, rel, lang)
		if ok {
			out.Entries = append(out.Entries, e)
		}
	}
	return out, nil
}

// StatEntry builds an Entry for one path, applying the matcher. ok is false
// when the file is missing, excluded, or escapes the root.
func StatEntry(p *config.Project, m *Matcher, rel string) (Entry, bool) {
	lang, ok := m.Lang(rel)
	if !ok || nestedChecker(p)(rel) {
		return Entry{}, false
	}
	return stat(p, rel, lang)
}

func stat(p *config.Project, rel, lang string) (Entry, bool) {
	abs := filepath.Join(p.Root, filepath.FromSlash(rel))
	fi, err := os.Lstat(abs)
	if err != nil {
		return Entry{}, false
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		if _, err := p.Confine(abs); err != nil {
			return Entry{}, false // never follow links out of the root
		}
		if fi, err = os.Stat(abs); err != nil {
			return Entry{}, false
		}
	}
	if !fi.Mode().IsRegular() {
		return Entry{}, false
	}
	return Entry{Path: rel, Lang: lang, Size: fi.Size(), MTime: fi.ModTime().UnixNano()}, true
}

func gitFiles(ctx context.Context, root string) ([]string, error) {
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "git", "-C", root, "ls-files", "-co", "--exclude-standard", "-z")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var rels []string
	for _, b := range bytes.Split(out, []byte{0}) {
		if len(b) > 0 && b[len(b)-1] != '/' {
			rels = append(rels, string(b))
		}
	}
	return rels, nil
}

func fsFiles(root string, includeNested bool) ([]string, error) {
	var rels []string
	err := filepath.WalkDir(root, func(abs string, d fs.DirEntry, err error) error {
		if err != nil {
			if abs == root {
				return err
			}
			return nil // unreadable entries are skipped, not fatal
		}
		if d.IsDir() {
			if abs != root && (skipDirs[d.Name()] || !includeNested && config.IsProjectMarker(abs)) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, abs)
		if err != nil {
			return nil
		}
		rels = append(rels, filepath.ToSlash(rel))
		return nil
	})
	return rels, err
}

// nestedChecker reports whether rel lives inside a nested project (a
// subdirectory with its own .codegraph.yaml, .codegraph/ or .git), unless
// include_nested is set. Results are cached per directory.
func nestedChecker(p *config.Project) func(rel string) bool {
	if p.Config.IncludeNested {
		return func(string) bool { return false }
	}
	cache := map[string]bool{".": false}
	var isNested func(dir string) bool
	isNested = func(dir string) bool {
		if v, ok := cache[dir]; ok {
			return v
		}
		abs := filepath.Join(p.Root, filepath.FromSlash(dir))
		v := config.IsProjectMarker(abs)
		if !v {
			if fi, err := os.Stat(filepath.Join(abs, config.DataDirName)); err == nil && fi.IsDir() {
				v = true
			}
		}
		if !v {
			v = isNested(path.Dir(dir))
		}
		cache[dir] = v
		return v
	}
	return func(rel string) bool { return isNested(path.Dir(rel)) }
}
