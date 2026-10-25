package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/gayanmadurapperuma/uxor/internal/parse"
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
	// Identifier splitting makes camelCase parts searchable, and the whole
	// identifier through its marked token (D31).
	for _, q := range []string{"user", `"` + parse.WholeTerm("GetUserByID") + `"`, "repo", "pkg"} {
		if n := count(t, st, `SELECT count(*) FROM symbols_fts WHERE symbols_fts MATCH ?`, q); n == 0 {
			t.Errorf("FTS query %q found nothing", q)
		}
	}
	// A prefix never spans identifier words: "getuserb" is no word's prefix.
	if n := count(t, st, `SELECT count(*) FROM symbols_fts WHERE symbols_fts MATCH ?`, `"getuserb"*`); n != 0 {
		t.Errorf("cross-word prefix matched %d rows", n)
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

// TestMigrateV3RebuildsFTS: a v2 index, whose FTS rows hold unmarked whole
// identifiers, is rewritten to the marked format when opened for writing.
func TestMigrateV3RebuildsFTS(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "d", "index.db")
	st, err := Open(ctx, path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	meta := FileMeta{Path: "pkg/user.go", Lang: "go", Hash: []byte{1}, Size: 10, MTime: 42, LOC: 20}
	write(t, st, func(b *Batch) error { return b.ReplaceFile(ctx, meta, sample(), false) })
	// Turn it back into a v2 index: old-format FTS rows, user_version 2.
	db := st.DB()
	for _, q := range []string{
		`INSERT INTO symbols_fts(symbols_fts) VALUES('delete-all')`,
		`INSERT INTO symbols_fts(rowid, name, qualified_name, signature, path)
		   SELECT id, name || ' old', qualified_name, signature, 'pkg/user.go' FROM symbols`,
		`PRAGMA user_version = 2`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if n := count(t, st, `SELECT count(*) FROM symbols_fts WHERE symbols_fts MATCH ?`, `"getuserb"*`); n == 0 {
		t.Fatal("setup: the old format should allow the cross-word prefix")
	}
	st.Close()

	st, err = Open(ctx, path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if n := count(t, st, `PRAGMA user_version`); n != SchemaVersion {
		t.Fatalf("user_version %d, want %d", n, SchemaVersion)
	}
	if n := count(t, st, `SELECT count(*) FROM symbols_fts WHERE symbols_fts MATCH ?`, `"getuserb"*`); n != 0 {
		t.Errorf("after migration a cross-word prefix still matches %d rows", n)
	}
	if n := count(t, st, `SELECT count(*) FROM symbols_fts WHERE symbols_fts MATCH ?`, `"`+parse.WholeTerm("GetUserByID")+`"`); n != 1 {
		t.Errorf("marked whole identifier matches %d rows, want 1", n)
	}
	if n := count(t, st, `SELECT count(*) FROM symbols_fts WHERE symbols_fts MATCH ?`, "old"); n != 0 {
		t.Errorf("%d old-format rows survived", n)
	}
}

// TestWordAlignedParts: a run of an identifier's words is searchable as a
// marked whole ("signIn" reaches handleSignIn), but a fragment that isn't
// word-aligned never is (D31, D38).
func TestWordAlignedParts(t *testing.T) {
	ctx := context.Background()
	st := openTemp(t)
	r := &parse.FileResult{Path: "web/auth.ts", Lang: "typescript", LOC: 9, Symbols: []parse.Symbol{
		{Kind: parse.KindModule, Name: "web/auth", QualifiedName: "web/auth", StartLine: 1, EndLine: 9, Parent: -1},
		{Kind: parse.KindFunction, Name: "handleSignIn", QualifiedName: "AuthProvider.handleSignIn", StartLine: 2, EndLine: 4},
		{Kind: parse.KindMethod, Name: "handleDelete", QualifiedName: "Repo.handleDelete", StartLine: 5, EndLine: 7},
	}}
	meta := FileMeta{Path: r.Path, Lang: r.Lang, Hash: []byte{1}, Size: 1, MTime: 1, LOC: 9}
	write(t, st, func(b *Batch) error { return b.ReplaceFile(ctx, meta, r, false) })
	q := `SELECT count(*) FROM symbols_fts WHERE symbols_fts MATCH ?`
	for term, want := range map[string]int{
		`"` + parse.WholeTerm("signIn") + `"`:       1, // a word run inside handleSignIn
		`"` + parse.WholeTerm("authProvider") + `"`: 1, // a whole qualified-name part
		`"0handled"`: 0, // "handle"+"d": not word-aligned
		`"handled"*`: 0, // word prefixes stay inside words
		// Marked tokens are only ever matched exactly; retrieval never
		// prefix-queries them (only the off-by-default whole_prefix does).
	} {
		if n := count(t, st, q, term); n != want {
			t.Errorf("%s matched %d rows, want %d", term, n, want)
		}
	}
}
