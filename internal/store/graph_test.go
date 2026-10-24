package store_test

import (
	"context"
	"testing"

	"github.com/gayanmadurapperuma/uxor/internal/parse"
	"github.com/gayanmadurapperuma/uxor/internal/store"
	"github.com/gayanmadurapperuma/uxor/internal/testfix"
)

func TestGraphQueries(t *testing.T) {
	ctx := context.Background()
	st := testfix.ReadOnly(t, testfix.Indexed(t, "go"))

	for name, want := range map[string]int{
		"store.memRepo.GetUserByID": 1, // qualified name
		"GetUserByID":               2, // interface method and implementation
		"memRepo.GetUserByID":       1, // qualified-name suffix
		"Repo%":                     0, // LIKE metacharacters are literal
		"store":                     0, // module symbols are never returned
		"":                          0,
	} {
		refs, err := st.LookupSymbols(ctx, name, 10)
		if err != nil || len(refs) != want {
			t.Errorf("LookupSymbols(%q) = %v, %v; want %d", name, refs, err, want)
		}
	}

	refs, _ := st.LookupSymbols(ctx, "store.NewMemRepo", 1)
	callers, total, err := st.Neighbors(ctx, refs[0].ID, true, []parse.EdgeType{parse.EdgeCalls}, 10)
	if err != nil || total != 2 || len(callers) != 2 {
		t.Fatalf("callers of NewMemRepo: %v %d %v", callers, total, err)
	}
	got := map[string]bool{}
	for _, c := range callers {
		got[c.QualifiedName] = true
	}
	if !got["api.NewHandler"] || !got["store.TestGetUserByID"] {
		t.Errorf("callers: %v", got)
	}
	if _, total, _ := st.Neighbors(ctx, refs[0].ID, true, []parse.EdgeType{parse.EdgeCalls}, 1); total != 2 {
		t.Errorf("total should ignore the limit, got %d", total)
	}

	imp, err := st.FileImpact(ctx, "store/user.go", 10)
	if err != nil {
		t.Fatal(err)
	}
	if imp.Totals["importers"] != 1 || imp.Importers[0].Path != "api/handler.go" {
		t.Errorf("importers: %+v", imp.Importers)
	}
	if imp.Totals["subtypes"] != 1 || imp.Subtypes[0].QualifiedName != "api.Handler" || imp.Subtypes[0].Target != "store.Repository" {
		t.Errorf("subtypes: %+v", imp.Subtypes)
	}
	if imp.Totals["tests"] == 0 || imp.Totals["callers"] == 0 {
		t.Errorf("totals: %v", imp.Totals)
	}
	for _, c := range imp.Callers {
		if c.Path == "store/user.go" {
			t.Errorf("same-file caller listed: %+v", c)
		}
	}
	if _, err := st.FileImpact(ctx, "nope.go", 10); err != store.ErrNotIndexed {
		t.Errorf("missing file: %v", err)
	}
}
