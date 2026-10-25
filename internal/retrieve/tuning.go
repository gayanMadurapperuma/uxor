package retrieve

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	"github.com/gayanmadurapperuma/uxor/internal/parse"
)

// Tuning holds the ranking constants, so the eval can sweep them without a
// rebuild. It is internal: not part of .uxor.yaml. DefaultTuning is
// what every caller uses unless Options.Tuning says otherwise.
type Tuning struct {
	// BM25 holds the column weights for symbols_fts(name, qualified_name,
	// signature, path).
	BM25 [4]float64 `json:"bm25"`
	// ExactBoost is added to a seed whose name equals an identifier in the
	// query.
	ExactBoost float64 `json:"exact_boost"`
	// PrefixMinLen is the shortest term that also matches as a prefix.
	PrefixMinLen int `json:"prefix_min_len"`
	// WholePrefix also prefix-matches terms against whole identifiers,
	// across their word boundaries: the pre-D31 behaviour, for comparison.
	WholePrefix bool `json:"whole_prefix"`
	// Compounds and Synonyms switch the D32 query expansions.
	Compounds bool `json:"compounds"`
	Synonyms  bool `json:"synonyms"`

	// HopDecay multiplies a path score once per hop.
	HopDecay float64 `json:"hop_decay"`
	// Edges holds the weight per edge type name (parse.EdgeType.String),
	// as [outgoing, incoming] relative to the frontier node.
	Edges map[string][2]float64 `json:"edges"`

	// Relevance is multiplied by 1 + PagerankWeight·ln(1+pagerank), so rank
	// orders relevant symbols but cannot lift an unrelated hub; pagerank has
	// mean 1 (D15).
	PagerankWeight float64 `json:"pagerank_weight"`
	TestedBonus    float64 `json:"tested_bonus"` // a symbol some test covers
	TestPenalty    float64 `json:"test_penalty"` // × for test code unless the query mentions tests
	TestBoost      float64 `json:"test_boost"`   // × for test code when it does

	// A file scores its best symbol plus RestWeight × its next RestCount
	// symbols, so several hits lift a file but many weak ones cannot outrank
	// one strong one.
	RestWeight float64 `json:"rest_weight"`
	RestCount  int     `json:"rest_count"`
	// MinRelScore drops candidates scoring below this fraction of the best:
	// the budget is a ceiling, not a target, and weak tails only cost tokens.
	MinRelScore float64 `json:"min_rel_score"`
	// RelatedPerFile caps the lines per file that were reached only through
	// the graph (not matches), keeping the best; -1 means no cap (D38).
	RelatedPerFile int `json:"related_per_file"`
	// SeedsPerName keeps at most this many seeds per identical declaration
	// (same name and signature), so a constant copied into many files can't
	// take every seed slot. An interface method and its implementation
	// differ in signature and both stay; 0 means no cap (D39).
	SeedsPerName int `json:"seeds_per_name"`
	// SeedsPerTerm adds seeds for long prompts: retrieve.seeds plus this
	// many per search term beyond 5, at most maxSeeds; 0 means off (D39).
	SeedsPerTerm int `json:"seeds_per_term"`
	// CollapseDuplicates packs identical declarations (same name, kind and
	// signature) found in several files once, noting the other files (D39).
	CollapseDuplicates bool `json:"collapse_duplicates"`
}

// DefaultTuning returns the shipped constants.
func DefaultTuning() *Tuning {
	return &Tuning{
		BM25:         [4]float64{10, 5, 2, 3}, // path 3, not 1: D28
		ExactBoost:   0.5,
		PrefixMinLen: 4,
		Compounds:    false, // hurt vite MRR 0.459 → 0.408 (D35); synonyms cover sign-in
		Synonyms:     true,
		HopDecay:     0.5,
		Edges: map[string][2]float64{
			parse.EdgeCalls.String():      {1.0, 0.8},
			parse.EdgeExtends.String():    {0.7, 0.7},
			parse.EdgeImplements.String(): {0.7, 0.7},
			parse.EdgeDefines.String():    {0.6, 0.5},
			parse.EdgeTests.String():      {0.5, 0.5},
			parse.EdgeImports.String():    {0.3, 0.3},
		},
		PagerankWeight:     0.1,
		TestedBonus:        0.05,
		TestPenalty:        1.0, // no penalty by default since D28
		TestBoost:          1.5,
		RestWeight:         0.25,
		RestCount:          3,
		MinRelScore:        0.25,
		RelatedPerFile:     1,    // −7% hook / −12% query tokens at equal hit rates (D38)
		SeedsPerName:       1,    // +1.0 hook / +1.6 query hit points, same tokens (D39)
		CollapseDuplicates: true, // identical declarations in many files: one line (D39)
	}
}

// LoadTuning reads a JSON file over DefaultTuning: fields it omits keep
// their defaults, and an "edges" object replaces only the types it names.
func LoadTuning(path string) (*Tuning, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	t := DefaultTuning()
	// Unknown fields are errors: a typo, or a binary older than the file,
	// would otherwise silently measure the defaults.
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(t); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return t, nil
}

// orDefault lets nil mean DefaultTuning.
func (t *Tuning) orDefault() *Tuning {
	if t == nil {
		return DefaultTuning()
	}
	return t
}

// ftsRank is the bm25 expression for these column weights. The weights are
// numbers formatted by us, never user text.
func (t *Tuning) ftsRank() string {
	w := t.BM25
	return fmt.Sprintf("bm25(symbols_fts, %g, %g, %g, %g)", w[0], w[1], w[2], w[3])
}
