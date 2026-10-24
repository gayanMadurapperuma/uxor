// Package retrieve answers "where should I look for <prompt>?" from the
// code graph: full-text seeds, a bounded graph expansion, ranking, and
// packing into a token budget. It reads only the index, never source.
package retrieve

import (
	"context"
	"time"

	"github.com/gayanmadurapperuma/uxor/internal/config"
	"github.com/gayanmadurapperuma/uxor/internal/store"
)

// Options for one retrieval.
type Options struct {
	Budget   int     // tokens for the packed text; default budgets.query_tokens
	MaxFiles int     // at most this many files; 0 = no limit
	Tuning   *Tuning // ranking constants; nil = DefaultTuning (the eval sweeps them)
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
	t := opt.Tuning.orDefault()
	res := &Result{Query: query, Budget: opt.Budget, Files: []File{}, Timings: map[string]float64{}}
	start := time.Now()
	cands, err := candidates(ctx, st, cfg, query, t, res.Timings)
	if err != nil {
		return nil, err
	}
	if len(cands) > 0 {
		lap := time.Now()
		res.Files, res.Text = pack(cands, opt.Budget, opt.MaxFiles, t)
		res.TokensUsed = Tokens(res.Text)
		res.Timings["pack"] = ms(time.Since(lap))
	}
	res.Timings["total"] = ms(time.Since(start))
	return res, nil
}

// RankFiles returns every candidate file best first, scored as packing
// scores them but with no budget, file limit or relative cutoff, so the
// eval can measure recall beyond what one answer shows. Files carry no
// symbols.
func RankFiles(ctx context.Context, st *store.Store, cfg *config.Config, query string, opt Options) ([]File, error) {
	t := opt.Tuning.orDefault()
	cands, err := candidates(ctx, st, cfg, query, t, map[string]float64{})
	if err != nil {
		return nil, err
	}
	byFile := map[string][]*candidate{}
	for i := range cands {
		c := &cands[i]
		byFile[c.Path] = append(byFile[c.Path], c)
	}
	out := make([]File, 0, len(byFile))
	for path, cs := range byFile {
		out = append(out, File{Path: path, Score: fileScore(cs, t)})
	}
	sortFiles(out)
	return out, nil
}

// candidates runs tokenize, seeds, expansion and ranking, recording each
// stage's time in timings. No query terms means no candidates.
func candidates(ctx context.Context, st *store.Store, cfg *config.Config, query string, t *Tuning, timings map[string]float64) ([]candidate, error) {
	lap := time.Now()
	mark := func(stage string) {
		now := time.Now()
		timings[stage] = ms(now.Sub(lap))
		lap = now
	}

	q := tokenize(query, t.Compounds, t.Synonyms)
	mark("tokenize")
	if q.Empty() {
		return nil, nil
	}

	db := st.DB()
	sources := []Source{&FTS{DB: db, Tuning: t}}
	lists := make([][]Seed, 0, len(sources))
	for _, s := range sources {
		seeds, err := s.Seeds(ctx, q, cfg.Retrieve.Seeds)
		if err != nil {
			return nil, err
		}
		lists = append(lists, seeds)
	}
	seeds := Fuse(lists, cfg.Retrieve.Seeds)
	if err := adjustTestSeeds(ctx, db, seeds, q, t); err != nil {
		return nil, err
	}
	mark("seeds")

	nodes, err := expand(ctx, db, seeds, ExpandOptions{
		Depth: cfg.Retrieve.Depth, MaxPerHop: cfg.Retrieve.MaxPerHop, HubFanIn: cfg.Retrieve.HubFanIn, Tuning: t,
	})
	if err != nil {
		return nil, err
	}
	mark("expand")

	cands, err := rank(ctx, db, nodes, q, t)
	if err != nil {
		return nil, err
	}
	mark("rank")
	return cands, nil
}

func ms(d time.Duration) float64 { return round(float64(d.Microseconds()) / 1000) }
