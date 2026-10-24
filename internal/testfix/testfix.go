// Package testfix gives tests an indexed copy of a fixture project from
// testdata/fixtures. It is imported only by tests.
package testfix

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/gayanmadurapperuma/uxor/internal/config"
	"github.com/gayanmadurapperuma/uxor/internal/index"
	"github.com/gayanmadurapperuma/uxor/internal/store"
)

// Dir returns testdata/fixtures/<name> in this repository.
func Dir(name string) string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "testdata", "fixtures", name)
}

// Copy copies a fixture into a fresh project directory (with a
// .uxor.yaml marker) and returns its root, symlinks resolved.
func Copy(t testing.TB, name string) string {
	t.Helper()
	src := Dir(name)
	dst, _ := filepath.EvalSymlinks(t.TempDir())
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, config.FileName), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return dst
}

// Index indexes the project at root and returns it.
func Index(t testing.TB, root string) *config.Project {
	t.Helper()
	p, err := config.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	st, err := store.Open(ctx, p.DBPath, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := index.Run(ctx, p, st, index.Options{}); err != nil {
		t.Fatal(err)
	}
	return p
}

// Indexed copies and indexes a fixture.
func Indexed(t testing.TB, name string) *config.Project {
	t.Helper()
	return Index(t, Copy(t, name))
}

// ReadOnly opens the project's index read-only, closed at cleanup.
func ReadOnly(t testing.TB, p *config.Project) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), p.DBPath, store.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}
