package retrieve

import (
	"context"
	"database/sql"
	"sort"

	"github.com/gayanmadurapperuma/uxor/internal/parse"
)

// direction of an edge relative to the frontier node being expanded.
type direction uint8

const (
	outgoing direction = iota // frontier is src: frontier → neighbor
	incoming                  // frontier is dst: neighbor → frontier
)

// relation is how a neighbor relates to the frontier node it was reached
// from: the edge-type weight and the label used in `why`.
type relation struct {
	weight float64
	label  string
}

// labels describe the neighbor reached over each followed edge type:
// following CALLS outwards from F reaches a callee, which is "called-by F".
// Edge types without labels are not followed; weights come from Tuning.Edges.
var labels = map[parse.EdgeType][2]string{
	parse.EdgeCalls:      {outgoing: "called-by", incoming: "calls"},
	parse.EdgeExtends:    {outgoing: "extended-by", incoming: "extends"},
	parse.EdgeImplements: {outgoing: "implemented-by", incoming: "implements"},
	parse.EdgeDefines:    {outgoing: "defined-in", incoming: "defines"},
	parse.EdgeTests:      {outgoing: "tested-by", incoming: "tests"},
	parse.EdgeImports:    {outgoing: "imported-by", incoming: "imports"},
}

// relations pairs the labels with t's weights; a type t gives no weight is
// not followed.
func relations(t *Tuning) map[parse.EdgeType][2]relation {
	out := make(map[parse.EdgeType][2]relation, len(labels))
	for typ, l := range labels {
		w, ok := t.Edges[typ.String()]
		if !ok {
			continue
		}
		out[typ] = [2]relation{outgoing: {w[outgoing], l[outgoing]}, incoming: {w[incoming], l[incoming]}}
	}
	return out
}

// node is a candidate symbol: a seed, reached by expansion, or both.
type node struct {
	seed    float64 // seed score, 0 if not a seed
	seedWhy string
	exp     float64 // best path score through the graph, 0 if never reached
	from    int64   // the node the best path came from
	label   string  // relation to from
	hop     int
	hub     bool
}

// path is the score this node passes on to its neighbors.
func (n *node) path() float64 { return max(n.seed, n.exp) }

// ExpandOptions bound the walk.
type ExpandOptions struct {
	Depth     int     // hops
	MaxPerHop int     // frontier nodes expanded per hop
	HubFanIn  int     // nodes with more incoming non-DEFINES edges are not expanded
	Tuning    *Tuning // edge weights and hop decay; nil = defaults
}

// expand walks the graph from the seeds in both directions, one batched
// query per direction per hop, and keeps the best path to every node.
// Hubs are scored when reached but not expanded through.
func expand(ctx context.Context, db *sql.DB, seeds []Seed, opt ExpandOptions) (map[int64]*node, error) {
	t := opt.Tuning.orDefault()
	rels := relations(t)
	nodes := make(map[int64]*node, len(seeds))
	frontier := make([]int64, 0, len(seeds))
	for _, s := range seeds {
		nodes[s.ID] = &node{seed: s.Score, seedWhy: s.Why}
		frontier = append(frontier, s.ID)
	}
	for hop := 1; hop <= opt.Depth && len(frontier) > 0; hop++ {
		sort.Slice(frontier, func(i, j int) bool {
			a, b := nodes[frontier[i]].path(), nodes[frontier[j]].path()
			if a != b {
				return a > b
			}
			return frontier[i] < frontier[j]
		})
		if len(frontier) > opt.MaxPerHop {
			frontier = frontier[:opt.MaxPerHop]
		}
		hubs, err := hubSet(ctx, db, frontier, opt.HubFanIn)
		if err != nil {
			return nil, err
		}
		ids := frontier[:0:0]
		for _, id := range frontier {
			if hubs[id] {
				nodes[id].hub = true
				continue
			}
			ids = append(ids, id)
		}
		if len(ids) == 0 {
			break
		}

		// Scores passed on this hop are fixed up front, so the result does
		// not depend on row order when frontier nodes reach each other.
		base := make(map[int64]float64, len(ids))
		for _, id := range ids {
			base[id] = nodes[id].path()
		}
		improved := map[int64]bool{}
		visit := func(from, to int64, typ parse.EdgeType, w float64, dir direction) {
			r, ok := rels[typ]
			if !ok || from == to {
				return
			}
			rel := r[dir]
			sc := base[from] * rel.weight * w * t.HopDecay
			n := nodes[to]
			if n == nil {
				n = &node{}
				nodes[to] = n
			}
			if sc <= n.exp {
				return
			}
			before := n.path()
			n.exp, n.from, n.label, n.hop = sc, from, rel.label, hop
			if n.path() > before {
				improved[to] = true
			}
		}
		for _, dir := range []direction{outgoing, incoming} {
			q := `SELECT src_id, dst_id, type, weight FROM edges WHERE src_id IN (` + placeholders(len(ids)) + `)`
			if dir == incoming {
				q = `SELECT src_id, dst_id, type, weight FROM edges WHERE dst_id IN (` + placeholders(len(ids)) + `)`
			}
			rows, err := db.QueryContext(ctx, q, idArgs(ids)...)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var src, dst int64
				var typ int
				var w float64
				if err := rows.Scan(&src, &dst, &typ, &w); err != nil {
					rows.Close()
					return nil, err
				}
				if dir == outgoing {
					visit(src, dst, parse.EdgeType(typ), w, dir)
				} else {
					visit(dst, src, parse.EdgeType(typ), w, dir)
				}
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return nil, err
			}
		}
		frontier = frontier[:0]
		for id := range improved {
			frontier = append(frontier, id)
		}
	}
	return nodes, nil
}

// hubSet returns the ids among ids with more than limit incoming
// non-DEFINES edges, counted with the (dst_id, type, src_id) index.
func hubSet(ctx context.Context, db *sql.DB, ids []int64, limit int) (map[int64]bool, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT dst_id FROM edges WHERE dst_id IN (`+placeholders(len(ids))+`) AND type != ?
		GROUP BY dst_id HAVING count(*) > ?`, idArgs(ids, int(parse.EdgeDefines), limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	hubs := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		hubs[id] = true
	}
	return hubs, rows.Err()
}
