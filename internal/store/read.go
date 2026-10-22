package store

import (
	"context"
	"os"

	"github.com/gayanmadurapperuma/codegraph/internal/parse"
)

type LangCount struct {
	Lang  string
	Files int
	LOC   int
}

type Hub struct {
	Name  string // qualified name, or the bare ref name when unresolved
	Path  string // empty for unresolved names
	FanIn int
	Score float64 `json:",omitempty"`
}

type Stats struct {
	Files, Symbols, Edges, Refs int
	ByLang                      []LangCount
	ByKind                      map[string]int
	ByEdge                      map[string]int
	DBBytes                     int64
	// Hubs are resolved symbols with the most incoming non-DEFINES edges.
	// Until call resolution exists they are the most-referenced call names.
	Hubs         []Hub
	HubsResolved bool
	// Resolution per ref type (IMPORTS, CALLS, EXTENDS, IMPLEMENTS).
	Resolution    []Resolution
	TopPageRank   []Hub // FanIn unused; Score holds the PageRank
	PageRankStale bool
	LastIndexed   string
}

type Resolution struct {
	Type     string
	Total    int
	Resolved int
}

// Stats summarises the index.
func (s *Store) Stats(ctx context.Context) (*Stats, error) {
	st := &Stats{ByKind: map[string]int{}, ByEdge: map[string]int{}}
	for _, c := range []struct {
		dst *int
		sql string
	}{
		{&st.Files, `SELECT count(*) FROM files`},
		{&st.Symbols, `SELECT count(*) FROM symbols`},
		{&st.Edges, `SELECT count(*) FROM edges`},
		{&st.Refs, `SELECT count(*) FROM refs`},
	} {
		if err := s.db.QueryRowContext(ctx, c.sql).Scan(c.dst); err != nil {
			return nil, err
		}
	}

	rows, err := s.db.QueryContext(ctx, `SELECT lang, count(*), sum(loc) FROM files GROUP BY lang ORDER BY 2 DESC`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var lc LangCount
		if err := rows.Scan(&lc.Lang, &lc.Files, &lc.LOC); err != nil {
			rows.Close()
			return nil, err
		}
		st.ByLang = append(st.ByLang, lc)
	}
	rows.Close()

	if err := s.countBy(ctx, `SELECT kind, count(*) FROM symbols GROUP BY kind`, func(k, n int) {
		st.ByKind[parse.Kind(k).String()] = n
	}); err != nil {
		return nil, err
	}
	if err := s.countBy(ctx, `SELECT type, count(*) FROM edges GROUP BY type`, func(k, n int) {
		st.ByEdge[parse.EdgeType(k).String()] = n
	}); err != nil {
		return nil, err
	}

	if st.Edges > st.ByEdge[parse.EdgeDefines.String()] {
		st.HubsResolved = true
		err = s.hubs(ctx, st, `
			SELECT s.qualified_name, f.path, h.n FROM (
				SELECT dst_id, count(*) AS n FROM edges WHERE type IN (?, ?, ?) GROUP BY dst_id ORDER BY n DESC LIMIT 10
			) h JOIN symbols s ON s.id = h.dst_id JOIN files f ON f.id = s.file_id ORDER BY h.n DESC`,
			int(parse.EdgeCalls), int(parse.EdgeExtends), int(parse.EdgeImplements))
	} else {
		err = s.hubs(ctx, st, `
			SELECT name, '', count(*) AS n FROM refs WHERE type = ? GROUP BY name ORDER BY n DESC LIMIT 10`,
			int(parse.EdgeCalls))
	}
	if err != nil {
		return nil, err
	}

	rows, err = s.db.QueryContext(ctx, `SELECT type, sum(total), sum(resolved) FROM resolve_stats GROUP BY type ORDER BY type`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var typ int
		var r Resolution
		if err := rows.Scan(&typ, &r.Total, &r.Resolved); err != nil {
			rows.Close()
			return nil, err
		}
		r.Type = parse.EdgeType(typ).String()
		st.Resolution = append(st.Resolution, r)
	}
	rows.Close()

	rows, err = s.db.QueryContext(ctx, `SELECT s.qualified_name, f.path, s.pagerank FROM symbols s
		JOIN files f ON f.id = s.file_id WHERE s.kind != ? AND s.pagerank > 0
		ORDER BY s.pagerank DESC LIMIT 10`, int(parse.KindModule))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var h Hub
		if err := rows.Scan(&h.Name, &h.Path, &h.Score); err != nil {
			rows.Close()
			return nil, err
		}
		st.TopPageRank = append(st.TopPageRank, h)
	}
	rows.Close()
	stale, err := s.Meta(ctx, "pagerank_stale")
	if err != nil {
		return nil, err
	}
	st.PageRankStale = stale == "1"

	for _, suffix := range []string{"", "-wal", "-shm"} {
		if fi, err := os.Stat(s.path + suffix); err == nil {
			st.DBBytes += fi.Size()
		}
	}
	st.LastIndexed, err = s.Meta(ctx, "last_indexed")
	return st, err
}

func (s *Store) countBy(ctx context.Context, q string, fn func(k, n int)) error {
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var k, n int
		if err := rows.Scan(&k, &n); err != nil {
			return err
		}
		fn(k, n)
	}
	return rows.Err()
}

func (s *Store) hubs(ctx context.Context, st *Stats, q string, args ...any) error {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var h Hub
		if err := rows.Scan(&h.Name, &h.Path, &h.FanIn); err != nil {
			return err
		}
		st.Hubs = append(st.Hubs, h)
	}
	return rows.Err()
}

// Paths returns all indexed file paths.
func (s *Store) Paths(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path FROM files ORDER BY path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SymbolsInFile returns the symbols of one file in line order (for tests and
// debugging).
func (s *Store) SymbolsInFile(ctx context.Context, path string) ([]parse.Symbol, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT s.kind, s.name, s.qualified_name, s.signature, s.start_line, s.end_line, s.exported
		FROM symbols s JOIN files f ON f.id = s.file_id WHERE f.path = ? ORDER BY s.start_line, s.id`, path)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []parse.Symbol
	for rows.Next() {
		var sym parse.Symbol
		var kind, exp int
		if err := rows.Scan(&kind, &sym.Name, &sym.QualifiedName, &sym.Signature, &sym.StartLine, &sym.EndLine, &exp); err != nil {
			return nil, err
		}
		sym.Kind, sym.Exported = parse.Kind(kind), exp == 1
		out = append(out, sym)
	}
	return out, rows.Err()
}
