package resolve

import (
	"context"
	"math"

	"github.com/gayanmadurapperuma/uxor/internal/parse"
)

const (
	damping     = 0.85
	maxIter     = 30
	convergence = 1e-6
)

// graph is a weighted edge list over dense node indexes.
type graph struct {
	n        int
	src, dst []int32
	w        []float32
}

// pagerank runs weighted PageRank and returns scores scaled so the mean is
// 1.0 (so they're comparable across repos of different size). Mass from
// nodes without out-edges is spread uniformly.
func pagerank(g graph) []float64 {
	if g.n == 0 {
		return nil
	}
	out := make([]float64, g.n)
	for i, s := range g.src {
		out[s] += float64(g.w[i])
	}
	n := float64(g.n)
	pr := make([]float64, g.n)
	next := make([]float64, g.n)
	for i := range pr {
		pr[i] = 1 / n
	}
	for range maxIter {
		dangling := 0.0
		for i, o := range out {
			if o == 0 {
				dangling += pr[i]
			}
		}
		base := (1-damping)/n + damping*dangling/n
		for i := range next {
			next[i] = base
		}
		for i, s := range g.src {
			next[g.dst[i]] += damping * pr[s] * float64(g.w[i]) / out[s]
		}
		delta := 0.0
		for i := range pr {
			delta += math.Abs(next[i] - pr[i])
		}
		pr, next = next, pr
		if delta < convergence {
			break
		}
	}
	for i := range pr {
		pr[i] *= n
	}
	return pr
}

// rankEdges are the edge types that confer importance.
var rankEdges = []parse.EdgeType{parse.EdgeImports, parse.EdgeCalls, parse.EdgeExtends, parse.EdgeImplements}

// updatePageRank recomputes PageRank for the whole graph and writes scores
// that changed. It returns the node count.
func updatePageRank(ctx context.Context, q querier) (int, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, pagerank FROM symbols ORDER BY id`)
	if err != nil {
		return 0, err
	}
	var ids []int64
	var old []float64
	idx := map[int64]int32{}
	for rows.Next() {
		var id int64
		var pr float64
		if err := rows.Scan(&id, &pr); err != nil {
			rows.Close()
			return 0, err
		}
		idx[id] = int32(len(ids))
		ids = append(ids, id)
		old = append(old, pr)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	g := graph{n: len(ids)}
	rows, err = q.QueryContext(ctx, `SELECT src_id, dst_id, weight FROM edges WHERE type IN (?, ?, ?, ?)`,
		int(rankEdges[0]), int(rankEdges[1]), int(rankEdges[2]), int(rankEdges[3]))
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var s, d int64
		var w float64
		if err := rows.Scan(&s, &d, &w); err != nil {
			rows.Close()
			return 0, err
		}
		si, ok1 := idx[s]
		di, ok2 := idx[d]
		if ok1 && ok2 && w > 0 {
			g.src, g.dst, g.w = append(g.src, si), append(g.dst, di), append(g.w, float32(w))
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	pr := pagerank(g)
	upd, err := q.PrepareContext(ctx, `UPDATE symbols SET pagerank = ? WHERE id = ?`)
	if err != nil {
		return 0, err
	}
	defer upd.Close()
	for i, v := range pr {
		if math.Abs(v-old[i]) < 1e-6 {
			continue
		}
		if _, err := upd.ExecContext(ctx, v, ids[i]); err != nil {
			return 0, err
		}
	}
	return g.n, nil
}
