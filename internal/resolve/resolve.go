// Package resolve turns unresolved refs (imports, calls, extends) into graph
// edges and precomputes PageRank.
//
// Invariant (D11): the non-DEFINES edges leaving a file are a pure function
// of that file's refs and the global symbol table, so only "dirty" files are
// re-resolved and only their outgoing edges replaced.
package resolve

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/gayanmadurapperuma/uxor/internal/config"
	"github.com/gayanmadurapperuma/uxor/internal/parse"
	"github.com/gayanmadurapperuma/uxor/internal/store"
)

// Version identifies the resolution rules; bumping it forces a full
// re-resolve of existing indexes. 2: Go closures are lexical (D30).
const Version = "2"

// FullFraction is the share of dirty files above which a full resolve is
// cheaper than a scoped one.
var FullFraction = 0.25

type Options struct {
	Full     bool     // re-resolve every file
	PageRank bool     // recompute PageRank (whole-project runs)
	Markers  []string // marker files from a fresh walk; nil reuses the stored list
}

type Result struct {
	Full          bool
	Files         int // files re-resolved
	Refs          int // refs examined
	Resolved      int // refs that produced at least one edge
	Edges         int // edges written
	Elapsed       time.Duration
	PageRankNodes int // 0 when PageRank was not recomputed
	PageRankTime  time.Duration
	PageRankStale bool
}

type edgeKey struct {
	src, dst int64
	typ      parse.EdgeType
}

type fileStats map[parse.EdgeType][2]int // type → {total, resolved}

// Run resolves dirty files (or everything) in one transaction.
func Run(ctx context.Context, p *config.Project, st *store.Store, opt Options) (res *Result, err error) {
	start := time.Now()
	res = &Result{}
	tx, err := st.DB().BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		if e := recover(); e != nil {
			re, ok := e.(resolveErr)
			if !ok {
				panic(e)
			}
			err = re.err
		}
		if err != nil {
			tx.Rollback()
		}
	}()

	markers := opt.Markers
	if markers == nil {
		if s, err := getMeta(ctx, tx, "resolve_markers"); err != nil {
			return nil, err
		} else if s != "" {
			json.Unmarshal([]byte(s), &markers)
		}
	} else {
		sort.Strings(markers)
		b, _ := json.Marshal(markers)
		if err := setMeta(ctx, tx, "resolve_markers", string(b)); err != nil {
			return nil, err
		}
	}
	mhash := markersHash(p, markers)
	prevHash, err := getMeta(ctx, tx, "markers_hash")
	if err != nil {
		return nil, err
	}
	prevVer, err := getMeta(ctx, tx, "resolver_version")
	if err != nil {
		return nil, err
	}
	full := opt.Full || prevHash != mhash || prevVer != Version

	t, err := newSymtab(ctx, tx)
	if err != nil {
		return nil, err
	}
	var dirty []*fileInfo
	if !full {
		ids, err := expandDirty(ctx, tx, t)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			if f := t.byID[id]; f != nil {
				dirty = append(dirty, f)
			}
		}
		full = float64(len(dirty)) > FullFraction*float64(len(t.files)) && len(dirty) > 1
	}
	if full {
		if err := t.preloadAll(); err != nil {
			return nil, err
		}
		dirty = t.files
		for _, q := range []string{`DELETE FROM edges WHERE type != 2`, `DELETE FROM resolve_stats`} {
			if _, err := tx.ExecContext(ctx, q); err != nil {
				return nil, err
			}
		}
	} else {
		for _, f := range dirty {
			if _, err := tx.ExecContext(ctx, `DELETE FROM edges WHERE type != 2 AND src_id IN (SELECT id FROM symbols WHERE file_id = ?)`, f.id); err != nil {
				return nil, err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM resolve_stats WHERE file_id = ?`, f.id); err != nil {
				return nil, err
			}
		}
	}
	res.Full, res.Files = full, len(dirty)

	insEdge, err := tx.PrepareContext(ctx, `INSERT INTO edges(src_id, dst_id, type, weight) VALUES(?, ?, ?, ?)
		ON CONFLICT(src_id, type, dst_id) DO UPDATE SET weight = max(weight, excluded.weight)`)
	if err != nil {
		return nil, err
	}
	defer insEdge.Close()
	insStat, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO resolve_stats(file_id, type, total, resolved) VALUES(?, ?, ?, ?)`)
	if err != nil {
		return nil, err
	}
	defer insStat.Close()

	r := newResolver(p, t, markers)
	for _, f := range dirty {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		edges, stats := r.resolveFile(f)
		for k, w := range edges {
			if _, err := insEdge.ExecContext(ctx, k.src, k.dst, int(k.typ), w); err != nil {
				return nil, err
			}
		}
		res.Edges += len(edges)
		for typ, c := range stats {
			res.Refs += c[0]
			res.Resolved += c[1]
			if _, err := insStat.ExecContext(ctx, f.id, int(typ), c[0], c[1]); err != nil {
				return nil, err
			}
		}
	}

	for _, q := range []string{`DELETE FROM resolve_dirty`} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return nil, err
		}
	}
	for k, v := range map[string]string{"markers_hash": mhash, "resolver_version": Version} {
		if err := setMeta(ctx, tx, k, v); err != nil {
			return nil, err
		}
	}

	stale, err := getMeta(ctx, tx, "pagerank_stale")
	if err != nil {
		return nil, err
	}
	changed := len(dirty) > 0
	switch {
	case opt.PageRank && (changed || full || stale != "0"):
		prStart := time.Now()
		if res.PageRankNodes, err = updatePageRank(ctx, tx); err != nil {
			return nil, err
		}
		res.PageRankTime = time.Since(prStart)
		err = setMeta(ctx, tx, "pagerank_stale", "0")
	case changed:
		err = setMeta(ctx, tx, "pagerank_stale", "1")
	}
	if err != nil {
		return nil, err
	}
	res.PageRankStale = !opt.PageRank && (changed || stale == "1")
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	res.Elapsed = time.Since(start)
	return res, nil
}

// expandDirty widens the dirty set recorded by the store batches (D11)
// with rules that need the whole picture, and returns it.
func expandDirty(ctx context.Context, tx *sql.Tx, t *symtab) ([]int64, error) {
	// 1. Changed Go files affect unqualified lookups across their package.
	var changedGo []*fileInfo
	rows, err := tx.QueryContext(ctx, `SELECT file_id FROM resolve_dirty WHERE changed & ?`, store.ChangedSymbols)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		if f := t.byID[id]; f != nil && f.family == "go" {
			changedGo = append(changedGo, f)
		}
	}
	rows.Close()
	for _, f := range changedGo {
		for _, o := range t.byDir[f.dir] {
			if o.family == "go" {
				if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO resolve_dirty(file_id) VALUES(?)`, o.id); err != nil {
					return nil, err
				}
			}
		}
	}

	// 2. When a class's bases changed, method lookups in its subclasses (the
	//    store marked direct ones) and their subclasses may change too.
	for range 2 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO resolve_dirty(file_id, changed)
			SELECT DISTINCT s.file_id, ?1 FROM edges e
			JOIN symbols s ON s.id = e.src_id JOIN symbols d ON d.id = e.dst_id
			WHERE e.type IN (4, 5) AND d.file_id IN (SELECT file_id FROM resolve_dirty WHERE changed & ?1)
			ON CONFLICT(file_id) DO UPDATE SET changed = changed | ?1`, store.ChangedRefs); err != nil {
			return nil, err
		}
	}

	rows, err = tx.QueryContext(ctx, `SELECT file_id FROM resolve_dirty ORDER BY file_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// resolveFile computes all non-DEFINES edges leaving f.
func (r *resolver) resolveFile(f *fileInfo) (map[edgeKey]float64, fileStats) {
	edges := map[edgeKey]float64{}
	add := func(src, dst int64, typ parse.EdgeType, w float64) {
		if src == dst || src == 0 || dst == 0 {
			return
		}
		k := edgeKey{src, dst, typ}
		edges[k] = max(edges[k], w)
	}
	stats := fileStats{}
	count := func(typ parse.EdgeType, ok bool) {
		c := stats[typ]
		c[0]++
		if ok {
			c[1]++
		}
		stats[typ] = c
	}

	sc := r.scopeOf(f)
	for _, g := range sc.imported {
		add(f.module, g.module, parse.EdgeImports, sc.importW[g.id])
	}
	if sc.imports > 0 {
		stats[parse.EdgeImports] = [2]int{sc.imports, sc.resolved}
	}

	r.t.symbolsOf(f.id)
	for _, rf := range r.t.refsOf(f.id) {
		var kinds kindSet
		switch rf.typ {
		case parse.EdgeCalls:
			kinds = callKinds
		case parse.EdgeExtends, parse.EdgeImplements:
			kinds = classKinds
		default:
			continue
		}
		src := r.t.sym(rf.src)
		if src == nil {
			continue
		}
		ts := r.resolveName(sc, src, rf, kinds)
		count(rf.typ, len(ts) > 0)
		for _, tg := range ts {
			add(src.id, tg.sym.id, rf.typ, tg.w)
			if rf.typ == parse.EdgeCalls && src.kind == parse.KindTest && tg.sym.kind != parse.KindTest {
				if tf := r.t.byID[tg.sym.file]; tf != nil && !tf.isTest {
					add(src.id, tg.sym.id, parse.EdgeTests, tg.w)
				}
			}
		}
	}

	if f.isTest {
		for _, g := range r.testedFiles(f) {
			add(f.module, g.module, parse.EdgeTests, wExact)
		}
		for _, g := range sc.imported {
			if !g.isTest {
				add(f.module, g.module, parse.EdgeTests, wTestImport)
			}
		}
	}
	return edges, stats
}

// markersHash fingerprints the marker list and contents so edits to
// go.mod / tsconfig / package.json trigger a full re-resolve.
func markersHash(p *config.Project, markers []string) string {
	h := sha256.New()
	for _, m := range markers {
		fmt.Fprintf(h, "%s\x00", m)
		if b, err := os.ReadFile(filepath.Join(p.Root, filepath.FromSlash(m))); err == nil {
			h.Write(b)
		}
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)[:12])
}

func getMeta(ctx context.Context, q querier, key string) (string, error) {
	var v string
	err := q.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func setMeta(ctx context.Context, q querier, key, value string) error {
	_, err := q.ExecContext(ctx, `INSERT INTO meta(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}
