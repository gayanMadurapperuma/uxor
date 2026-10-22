package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

const (
	DataDirName  = ".codegraph"
	dbFileName   = "index.db"
	disabledFile = "disabled"
	forgetFile   = "forget.txt"
)

// ErrOutsideRoot is returned when a path escapes the project root.
var ErrOutsideRoot = errors.New("path is outside the project root")

// Project is one isolated codegraph project.
type Project struct {
	Root    string // absolute, symlinks resolved
	DataDir string // <root>/.codegraph
	DBPath  string
	Config  *Config
}

// Open resolves the project containing start (a file or directory) and
// loads its config. It does not create anything on disk.
func Open(start string) (*Project, error) {
	root, err := FindRoot(start)
	if err != nil {
		return nil, err
	}
	cfg, err := Load(root)
	if err != nil {
		return nil, err
	}
	p := &Project{Root: root, DataDir: filepath.Join(root, DataDirName), Config: cfg}
	p.DBPath = filepath.Join(p.DataDir, dbFileName)
	if cfg.DBPath != "" {
		db, err := p.Confine(cfg.DBPath)
		if err != nil {
			return nil, fmt.Errorf("db_path: %w", err)
		}
		p.DBPath = filepath.Join(root, filepath.FromSlash(db))
	}
	return p, nil
}

// FindRoot walks up from start to the nearest directory containing
// .codegraph.yaml or .git. If none is found, start itself is the root.
func FindRoot(start string) (string, error) {
	abs, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	if fi, err := os.Stat(abs); err == nil && !fi.IsDir() {
		abs = filepath.Dir(abs)
	}
	for dir := abs; ; {
		if IsProjectMarker(dir) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return abs, nil
		}
		dir = parent
	}
}

// IsProjectMarker reports whether dir is a project root on its own.
func IsProjectMarker(dir string) bool {
	for _, m := range []string{FileName, ".git"} {
		if _, err := os.Lstat(filepath.Join(dir, m)); err == nil {
			return true
		}
	}
	return false
}

// Confine resolves p (absolute or relative to the root) and returns it as a
// root-relative, slash-separated path. Symlinks are resolved where they
// exist, so a link pointing outside the root is rejected.
func (p *Project) Confine(path string) (string, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(p.Root, path)
	}
	path = filepath.Clean(path)
	resolved, err := evalExisting(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(p.Root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", ErrOutsideRoot
	}
	return filepath.ToSlash(rel), nil
}

// evalExisting resolves symlinks in the longest existing prefix of path, so
// paths of deleted or not-yet-created files can still be confined.
func evalExisting(path string) (string, error) {
	var tail []string
	cur := path
	for {
		real, err := filepath.EvalSymlinks(cur)
		if err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				real = filepath.Join(real, tail[i])
			}
			return real, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return path, nil
		}
		tail = append(tail, filepath.Base(cur))
		cur = parent
	}
}

// EnsureDataDir creates the private data directory.
func (p *Project) EnsureDataDir() error {
	if err := os.MkdirAll(p.DataDir, 0o700); err != nil {
		return err
	}
	return os.Chmod(p.DataDir, 0o700)
}

// Disabled reports whether context injection is paused for this project.
// It is a single stat so hooks can check it before opening the database.
func (p *Project) Disabled() bool {
	_, err := os.Stat(filepath.Join(p.DataDir, disabledFile))
	return err == nil
}

// SetDisabled creates or removes the disabled marker.
func (p *Project) SetDisabled(disabled bool) error {
	marker := filepath.Join(p.DataDir, disabledFile)
	if !disabled {
		err := os.Remove(marker)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := p.EnsureDataDir(); err != nil {
		return err
	}
	return os.WriteFile(marker, []byte("codegraph context injection is disabled for this project\n"), 0o600)
}

// ForgetGlobs returns the local forget list (one glob per line).
func (p *Project) ForgetGlobs() ([]string, error) {
	f, err := os.Open(filepath.Join(p.DataDir, forgetFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" && !strings.HasPrefix(line, "#") {
			out = append(out, line)
		}
	}
	return out, sc.Err()
}

// SetForgetGlobs replaces the forget list.
func (p *Project) SetForgetGlobs(globs []string) error {
	for _, g := range globs {
		if !doublestar.ValidatePattern(g) {
			return fmt.Errorf("invalid glob %q", g)
		}
	}
	if err := p.EnsureDataDir(); err != nil {
		return err
	}
	globs = dedupSorted(globs)
	var b strings.Builder
	b.WriteString("# Local codegraph excludes (not committed). Managed by `codegraph forget`.\n")
	for _, g := range globs {
		b.WriteString(g)
		b.WriteByte('\n')
	}
	return os.WriteFile(filepath.Join(p.DataDir, forgetFile), []byte(b.String()), 0o600)
}

func dedupSorted(xs []string) []string {
	sort.Strings(xs)
	out := xs[:0]
	for i, x := range xs {
		if i == 0 || x != xs[i-1] {
			out = append(out, x)
		}
	}
	return out
}

// Clean deletes the project's data directory (database, WAL files, local
// state). The config file at the root is left alone.
func (p *Project) Clean() error {
	if filepath.Base(p.DataDir) != DataDirName || filepath.Dir(p.DataDir) != p.Root {
		return fmt.Errorf("refusing to delete unexpected data dir %s", p.DataDir)
	}
	return os.RemoveAll(p.DataDir)
}

// EnsureGitignore appends .codegraph/ to <root>/.gitignore in git repos when
// no rule for it exists yet. It reports whether the file was changed.
func (p *Project) EnsureGitignore() (bool, error) {
	if _, err := os.Stat(filepath.Join(p.Root, ".git")); err != nil {
		return false, nil
	}
	gi := filepath.Join(p.Root, ".gitignore")
	b, err := os.ReadFile(gi)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		switch strings.TrimSpace(line) {
		case ".codegraph", ".codegraph/", "/.codegraph", "/.codegraph/":
			return false, nil
		}
	}
	add := ".codegraph/\n"
	if len(b) > 0 && !strings.HasSuffix(string(b), "\n") {
		add = "\n" + add
	}
	f, err := os.OpenFile(gi, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return false, err
	}
	defer f.Close()
	_, err = f.WriteString(add)
	return err == nil, err
}
