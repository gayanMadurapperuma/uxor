package retrieve

import (
	"context"
	"sort"
)

// Seed is a symbol a source considers directly relevant to the query.
type Seed struct {
	ID    int64
	Score float64 // 0..1 within its source; an exact-name hit may add more
	Why   string
}

// Source produces ranked seeds for a query. FTS is the only source today;
// an embeddings source can be added later and fused with it.
type Source interface {
	Seeds(ctx context.Context, q Query, n int) ([]Seed, error)
}

// rrfK is the usual reciprocal-rank-fusion constant.
const rrfK = 60

// Fuse merges per-source seed lists. One list is returned as is, keeping
// its scores. Several lists are combined by reciprocal rank, rescaled so
// the best seed scores 1; each seed keeps the Why of its best-ranked source.
func Fuse(lists [][]Seed, n int) []Seed {
	var nonEmpty [][]Seed
	for _, l := range lists {
		if len(l) > 0 {
			nonEmpty = append(nonEmpty, l)
		}
	}
	switch len(nonEmpty) {
	case 0:
		return nil
	case 1:
		l := nonEmpty[0]
		if len(l) > n {
			l = l[:n]
		}
		return l
	}
	type acc struct {
		score   float64
		why     string
		bestPos int
	}
	m := map[int64]*acc{}
	for _, l := range nonEmpty {
		for pos, s := range l {
			a := m[s.ID]
			if a == nil {
				a = &acc{bestPos: pos, why: s.Why}
				m[s.ID] = a
			} else if pos < a.bestPos {
				a.bestPos, a.why = pos, s.Why
			}
			a.score += 1.0 / float64(rrfK+pos+1)
		}
	}
	out := make([]Seed, 0, len(m))
	best := 0.0
	for id, a := range m {
		out = append(out, Seed{ID: id, Score: a.score, Why: a.why})
		best = max(best, a.score)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].ID < out[j].ID
	})
	if len(out) > n {
		out = out[:n]
	}
	for i := range out {
		out[i].Score /= best
	}
	return out
}
