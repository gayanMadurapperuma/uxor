package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/gayanmadurapperuma/codegraph/internal/parse"
)

func sample() *parse.FileResult {
	return &parse.FileResult{
		Path: "pkg/user.go", Lang: "go", LOC: 20,
		Symbols: []parse.Symbol{
			{Kind: parse.KindModule, Name: "pkg", QualifiedName: "pkg", StartLine: 1, EndLine: 20, Exported: true, Parent: -1},
			{Kind: parse.KindType, Name: "UserRepo", QualifiedName: "pkg.UserRepo", Signature: "type UserRepo struct", StartLine: 3, EndLine: 5, Exported: true},
			{Kind: parse.KindMethod, Name: "GetUserByID", QualifiedName: "pkg.UserRepo.GetUserByID", Signature: "func (r *UserRepo) GetUserByID(id int)", StartLine: 7, EndLine: 9, Exported: true},
		},
		Refs: []parse.Ref{
			{From: 0, Type: parse.EdgeImports, Name: "fmt", Qualifier: "fmt", Line: 2},
			{From: 2, Type: parse.EdgeCalls, Name: "Println", Qualifier: "fmt", Line: 8},
		},
	}
}

func openTemp(t *testing.T) *Store {
	t.Helper()
	st, err := Open(context.Background(), filepath.Join(t.TempDir(), "d", "index.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func write(t *testing.T, st *Store, fn func(*Batch) error) {
	t.Helper()
	b, err := st.Begin(context.Background(), BatchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := fn(b); err != nil {
		b.Rollback()
		t.Fatal(err)
	}
	if err := b.Commit(); err != nil {
		t.Fatal(err)
	}
}

func count(t *testing.T, st *Store, q string, args ...any) int {
	t.Helper()
	var n int
	if err := st.DB().QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := openTemp(t)
	meta := FileMeta{Path: "pkg/user.go", Lang: "go", Hash: []byte("0123456789abcdef"), Size: 100, MTime: 42, LOC: 20}
	write(t, st, func(b *Batch) error { return b.ReplaceFile(ctx, meta, sample(), false) })

	syms, err := st.SymbolsInFile(ctx, "pkg/user.go")
	if err != nil || len(syms) != 3 {
		t.Fatalf("symbols = %v, %v", syms, err)
	}
	if syms[2].QualifiedName != "pkg.UserRepo.GetUserByID" || syms[2].Kind != parse.KindMethod || !syms[2].Exported {
		t.Errorf("unexpected symbol %+v", syms[2])
	}
	if n := count(t, st, `SELECT count(*) FROM edges WHERE type = ?`, int(parse.EdgeDefines)); n != 2 {
		t.Errorf("DEFINES edges = %d, want 2", n)
	}
	if n := count(t, st, `SELECT count(*) FROM refs`); n != 2 {
		t.Errorf("refs = %d, want 2", n)
	}
	// Identifier splitting makes camelCase parts searchable.
	for _, q := range []string{"user", "GetUserByID", "repo", "pkg"} {
		if n := count(t, st, `SELECT count(*) FROM symbols_fts WHERE symbols_fts MATCH ?`, q); n == 0 {
			t.Errorf("FTS query %q found nothing", q)
		}
	}

	states, err := st.FileStates(ctx)
	if err != nil || states["pkg/user.go"].MTime != 42 {
		t.Fatalf("states = %v, %v", states, err)
	}

	// Replacing leaves no stale rows; deleting removes everything.
	write(t, st, func(b *Batch) error { return b.ReplaceFile(ctx, meta, sample(), true) })
	if n := count(t, st, `SELECT count(*) FROM symbols`); n != 3 {
		t.Errorf("after replace symbols = %d, want 3", n)
	}
	if n := count(t, st, `SELECT count(*) FROM symbols_fts WHERE symbols_fts MATCH 'user'`); n != 3 {
		t.Errorf("after replace FTS hits = %d, want 3", n)
	}
	write(t, st, func(b *Batch) error { return b.DeleteFile(ctx, "pkg/user.go") })
	for _, tbl := range []string{"files", "symbols", "edges", "refs"} {
		if n := count(t, st, "SELECT count(*) FROM "+tbl); n != 0 {
			t.Errorf("%s has %d rows after delete", tbl, n)
		}
	}
	if n := count(t, st, `SELECT count(*) FROM symbols_fts WHERE symbols_fts MATCH 'user'`); n != 0 {
		t.Errorf("FTS has %d hits after delete", n)
	}
}

func TestFilePermissionsAndReopen(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "data")
	path := filepath.Join(dir, "index.db")
	st, err := Open(ctx, path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("db mode = %v, want 0600", fi.Mode().Perm())
	}
	// Reopening must not re-run migrations.
	st, err = Open(ctx, path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var v int
	st.DB().QueryRow(`PRAGMA user_version`).Scan(&v)
	if v != SchemaVersion {
		t.Errorf("user_version = %d, want %d", v, SchemaVersion)
	}
	ro, err := Open(ctx, path, Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if _, err := ro.Stats(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestReadOnlyMissing(t *testing.T) {
	if _, err := Open(context.Background(), filepath.Join(t.TempDir(), "none.db"), Options{ReadOnly: true}); err == nil {
		t.Error("expected error opening a missing index read-only")
	}
}
