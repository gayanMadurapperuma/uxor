package retrieve_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/gayanmadurapperuma/uxor/internal/retrieve"
)

func TestRankFilesCoversPacked(t *testing.T) {
	for lang, queries := range goldenQueries {
		p, st := fixture(t, lang)
		for _, q := range queries {
			ranked, err := retrieve.RankFiles(context.Background(), st, p.Config, q, retrieve.Options{})
			if err != nil {
				t.Fatalf("%s %q: %v", lang, q, err)
			}
			pos := map[string]int{}
			for i, f := range ranked {
				if i > 0 && f.Score > ranked[i-1].Score {
					t.Errorf("%s %q: not sorted at %d: %v", lang, q, i, ranked)
				}
				pos[f.Path] = i
			}
			for _, f := range find(t, p, st, q, retrieve.Options{}).Files {
				if _, ok := pos[f.Path]; !ok {
					t.Errorf("%s %q: packed file %s missing from RankFiles %v", lang, q, f.Path, ranked)
				}
			}
		}
	}
}

func TestTuningChangesRanking(t *testing.T) {
	p, st := fixture(t, "go")
	paths := func(opt retrieve.Options) []string {
		var out []string
		for _, f := range find(t, p, st, "Render", opt).Files {
			out = append(out, f.Path)
		}
		return out
	}
	// By default the callee GetUserByID is reached through CALLS (see
	// testdata/go.query); with no edge weights nothing is expanded.
	if got := paths(retrieve.Options{}); len(got) != 2 {
		t.Fatalf("default: %v", got)
	}
	noEdges := retrieve.DefaultTuning()
	noEdges.Edges = map[string][2]float64{}
	if got := paths(retrieve.Options{Tuning: noEdges}); len(got) != 1 || got[0] != "api/handler.go" {
		t.Fatalf("no edges: %v", got)
	}
}

func TestLoadTuning(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.json")
	os.WriteFile(path, []byte(`{"hop_decay": 0.4, "edges": {"CALLS": [0.9, 0.7]}}`), 0o644)
	got, err := retrieve.LoadTuning(path)
	if err != nil {
		t.Fatal(err)
	}
	def := retrieve.DefaultTuning()
	if got.HopDecay != 0.4 || got.Edges["CALLS"] != [2]float64{0.9, 0.7} {
		t.Errorf("overrides not applied: %+v", got)
	}
	if got.Edges["IMPORTS"] != def.Edges["IMPORTS"] || got.BM25 != def.BM25 || got.MinRelScore != def.MinRelScore {
		t.Errorf("defaults not kept: %+v", got)
	}
	os.WriteFile(path, []byte(`{"hop_decay": "x"}`), 0o644)
	if _, err := retrieve.LoadTuning(path); err == nil {
		t.Error("bad JSON accepted")
	}
	os.WriteFile(path, []byte(`{"hop_decya": 0.4}`), 0o644)
	if _, err := retrieve.LoadTuning(path); err == nil {
		t.Error("unknown field accepted")
	}
}
