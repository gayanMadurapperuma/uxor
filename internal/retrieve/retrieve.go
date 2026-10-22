// Package retrieve answers "where should I look for <prompt>?" from the
// code graph: full-text seeds, a bounded graph expansion, ranking, and
// packing into a token budget. It reads only the index, never source.
package retrieve

import (
	"context"
	"time"

	"github.com/gayanmadurapperuma/codegraph/internal/config"
	"github.com/gayanmadurapperuma/codegraph/internal/store"
)

// Options for one retrieval.
type Options struct {
	Budget   int // tokens for the packed text; default budgets.query_tokens
	MaxFiles int // at most this many files; 0 = no limit
}

// Result is a packed answer.
type Result struct {
	Query      string             `json:"query"`
	Budget     int                `json:"budget"`
	TokensUsed int                `json:"tokens_used"`
	Files      []File             `json:"results"`
	Text       string             `json:"-"`
	Timings    map[string]float64 `json:"timings_ms"`
}

// FindRelevant returns the files and line ranges most relevant to query,
// packed into the budget. All database work honours ctx, so callers can
// impose a deadline. The CLI, the MCP server and the prompt hook share it.
func FindRelevant(ctx context.Context, st *store.Store, cfg *config.Config, query string, opt Options) (*Result, error) {
	if opt.Budget <= 0 {
		opt.Budget = cfg.Budgets.QueryTokens
	}
	res := &Result{Query: query, Budget: opt.Budget, Files: []File{}, Timings: map[string]float64{}}
	start := time.Now()
	lap := start
	mark := func(stage string) {
		now := time.Now()
		res.Timings[stage] = ms(now.Sub(lap))
		lap = now
	}

	q := Tokenize(query)
	mark("tokenize")
	if q.Empty() {
		res.Timings["total"] = ms(time.Since(start))
		return res, nil
	}

	db := st.DB()
	sources := []Source{&FTS{DB: db}}
	lists := make([][]Seed, 0, len(sources))
	for _, s := range sources {
		seeds, err := s.Seeds(ctx, q, cfg.Retrieve.Seeds)
		if err != nil {
			return nil, err
		}
		lists = append(lists, seeds)
	}
	seeds := Fuse(lists, cfg.Retrieve.Seeds)
	if err := adjustTestSeeds(ctx, db, seeds, q); err != nil {
		return nil, err
	}
	mark("seeds")

	nodes, err := expand(ctx, db, seeds, ExpandOptions{
		Depth: cfg.Retrieve.Depth, MaxPerHop: cfg.Retrieve.MaxPerHop, HubFanIn: cfg.Retrieve.HubFanIn,
	})
	if err != nil {
		return nil, err
	}
	mark("expand")

	cands, err := rank(ctx, db, nodes, q)
	if err != nil {
		return nil, err
	}
	mark("rank")

	res.Files, res.Text = pack(cands, opt.Budget, opt.MaxFiles)
	res.TokensUsed = Tokens(res.Text)
	mark("pack")
	res.Timings["total"] = ms(time.Since(start))
	return res, nil
}

func ms(d time.Duration) float64 { return round(float64(d.Microseconds()) / 1000) }
