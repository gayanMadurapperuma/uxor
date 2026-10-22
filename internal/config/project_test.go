package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func mkProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("exclude: [\"gen/**\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(dir)
	return real
}

func TestFindRootAndLoad(t *testing.T) {
	root := mkProject(t)
	sub := filepath.Join(root, "a", "b")
	os.MkdirAll(sub, 0o755)
	p, err := Open(sub)
	if err != nil {
		t.Fatal(err)
	}
	if p.Root != root {
		t.Errorf("root = %s, want %s", p.Root, root)
	}
	if p.DBPath != filepath.Join(root, ".codegraph", "index.db") {
		t.Errorf("db = %s", p.DBPath)
	}
	if len(p.Config.Exclude) != 1 || p.Config.Budgets.QueryTokens != 1000 {
		t.Errorf("config not merged with defaults: %+v", p.Config)
	}
}

func TestInvalidConfig(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, FileName), []byte("db_path: /etc/x.db\n"), 0o644)
	if _, err := Open(dir); err == nil {
		t.Error("absolute db_path should be rejected")
	}
	os.WriteFile(filepath.Join(dir, FileName), []byte("exclude: [\"[\"]\n"), 0o644)
	if _, err := Open(dir); err == nil {
		t.Error("invalid glob should be rejected")
	}
}

func TestConfine(t *testing.T) {
	root := mkProject(t)
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret.go"), []byte("package x"), 0o644)
	os.MkdirAll(filepath.Join(root, "src"), 0o755)
	os.WriteFile(filepath.Join(root, "src", "a.go"), []byte("package a"), 0o644)
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	p, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	ok := map[string]string{
		"src/a.go":                         "src/a.go",
		filepath.Join(root, "src", "a.go"): "src/a.go",
		"src/../src/a.go":                  "src/a.go",
		"src/not-yet-created.go":           "src/not-yet-created.go",
	}
	for in, want := range ok {
		got, err := p.Confine(in)
		if err != nil || got != want {
			t.Errorf("Confine(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"../x.go", "src/../../x.go", filepath.Join(outside, "secret.go"),
		"escape/secret.go", "escape/new.go"} {
		if _, err := p.Confine(bad); !errors.Is(err, ErrOutsideRoot) {
			t.Errorf("Confine(%q) err = %v, want ErrOutsideRoot", bad, err)
		}
	}
}

func TestDisabledForgetClean(t *testing.T) {
	root := mkProject(t)
	p, _ := Open(root)
	if p.Disabled() {
		t.Fatal("new project should be enabled")
	}
	if err := p.SetDisabled(true); err != nil || !p.Disabled() {
		t.Fatalf("disable: %v", err)
	}
	if err := p.SetDisabled(false); err != nil || p.Disabled() {
		t.Fatalf("enable: %v", err)
	}
	if err := p.SetDisabled(false); err != nil {
		t.Fatalf("enable twice: %v", err)
	}

	if err := p.SetForgetGlobs([]string{"secrets/**", "legacy", "secrets/**"}); err != nil {
		t.Fatal(err)
	}
	g, err := p.ForgetGlobs()
	if err != nil || len(g) != 2 || g[0] != "legacy" {
		t.Fatalf("forget globs = %v, %v", g, err)
	}
	if err := p.SetForgetGlobs([]string{"["}); err == nil {
		t.Error("invalid forget glob accepted")
	}

	fi, err := os.Stat(p.DataDir)
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("data dir mode = %v, %v", fi.Mode().Perm(), err)
	}
	if err := p.Clean(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p.DataDir); !errors.Is(err, os.ErrNotExist) {
		t.Error("data dir still exists after Clean")
	}
	if _, err := os.Stat(filepath.Join(root, FileName)); err != nil {
		t.Error("Clean must keep .codegraph.yaml")
	}
}

func TestEnsureGitignore(t *testing.T) {
	root := mkProject(t)
	p, _ := Open(root)
	if changed, _ := p.EnsureGitignore(); changed {
		t.Error("non-git project should not get a .gitignore")
	}
	os.Mkdir(filepath.Join(root, ".git"), 0o755)
	os.WriteFile(filepath.Join(root, ".gitignore"), []byte("node_modules"), 0o644)
	if changed, err := p.EnsureGitignore(); !changed || err != nil {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if changed, _ := p.EnsureGitignore(); changed {
		t.Error("second call should be a no-op")
	}
	b, _ := os.ReadFile(filepath.Join(root, ".gitignore"))
	if string(b) != "node_modules\n.codegraph/\n" {
		t.Errorf(".gitignore = %q", b)
	}
}
