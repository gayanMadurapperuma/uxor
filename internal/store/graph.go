package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/gayanmadurapperuma/codegraph/internal/parse"
)

// ErrNotIndexed is returned for a path the index has no file for.
var ErrNotIndexed = errors.New("file is not indexed")

// SymbolRef locates one symbol: what MCP tools return. Never source code.
type SymbolRef struct {
	ID            int64   `json:"-"`
	Name          string  `json:"name"`
	QualifiedName string  `json:"qualified_name"`
	Kind          string  `json:"kind"`
	Path          string  `json:"path"`
	StartLine     int     `json:"start_line"`
	EndLine       int     `json:"end_line"`
	Signature     string  `json:"signature"`
	PageRank      float64 `json:"-"`
}

// Neighbor is a symbol joined by one edge to the symbol asked about.
type Neighbor struct {
	SymbolRef
	Type   string  `json:"edge"`
	Weight float64 `json:"weight"`           // resolution confidence (D12)
	Target string  `json:"target,omitempty"` // the symbol it connects to, for FileImpact
}

const symbolCols = `s.id, s.kind, s.name, s.qualified_name, s.signature, s.start_line, s.end_line, s.pagerank, f.path`

func scanRef(sc interface{ Scan(...any) error }, r *SymbolRef, extra ...any) error {
	var kind int
	if err := sc.Scan(append([]any{&r.ID, &kind, &r.Name, &r.QualifiedName, &r.Signature, &r.StartLine, &r.EndLine, &r.PageRank, &r.Path}, extra...)...); err != nil {
		return err
	}
	r.Kind = parse.Kind(kind).String()
	return nil
}

// LookupSymbols finds symbols by exact qualified name, else by exact name,
// else by qualified-name suffix (memRepo.GetUserByID), best PageRank first.
// Module symbols are never returned.
func (s *Store) LookupSymbols(ctx context.Context, name string, limit int) ([]SymbolRef, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return []SymbolRef{}, nil
	}
	for _, where := range []string{`s.qualified_name = ?1`, `s.name = ?1`, `s.qualified_name LIKE '%.' || ?1 ESCAPE '\'`} {
		arg := name
		if strings.Contains(where, "LIKE") {
			if !strings.Contains(name, ".") {
				break // a bare name was already tried exactly
			}
			arg = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(name)
		}
		rows, err := s.db.QueryContext(ctx, `SELECT `+symbolCols+`
			FROM symbols s JOIN files f ON f.id = s.file_id
			WHERE `+where+` AND s.kind != ?2
			ORDER BY s.pagerank DESC, f.path, s.start_line LIMIT ?3`, arg, int(parse.KindModule), limit)
		if err != nil {
			return nil, err
		}
		out, err := collectRefs(rows)
		if err != nil || len(out) > 0 {
			return out, err
		}
	}
	return []SymbolRef{}, nil
}

func collectRefs(rows *sql.Rows) ([]SymbolRef, error) {
	defer rows.Close()
	out := []SymbolRef{}
	for rows.Next() {
		var r SymbolRef
		if err := scanRef(rows, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Neighbors returns symbols joined to id by edges of the given types:
// callers etc. when incoming, callees etc. otherwise. Best weight first,
// then PageRank; total counts all of them.
func (s *Store) Neighbors(ctx context.Context, id int64, incoming bool, types []parse.EdgeType, limit int) (out []Neighbor, total int, err error) {
	self, other := "e.src_id", "e.dst_id"
	if incoming {
		self, other = "e.dst_id", "e.src_id"
	}
	args := []any{id}
	for _, t := range types {
		args = append(args, int(t))
	}
	in := strings.TrimSuffix(strings.Repeat("?,", len(types)), ",")
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM edges e WHERE `+self+` = ? AND e.type IN (`+in+`)`, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+symbolCols+`, e.type, e.weight
		FROM edges e JOIN symbols s ON s.id = `+other+` JOIN files f ON f.id = s.file_id
		WHERE `+self+` = ? AND e.type IN (`+in+`)
		ORDER BY e.weight DESC, s.pagerank DESC, f.path, s.start_line LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out = []Neighbor{}
	for rows.Next() {
		var n Neighbor
		var typ int
		if err := scanRef(rows, &n.SymbolRef, &typ, &n.Weight); err != nil {
			return nil, 0, err
		}
		n.Type = parse.EdgeType(typ).String()
		out = append(out, n)
	}
	return out, total, rows.Err()
}

// Impact lists what depends on one file, per relation.
type Impact struct {
	Path      string         `json:"path"`
	Totals    map[string]int `json:"totals"` // per relation, before the limit
	Importers []Neighbor     `json:"importers"`
	Callers   []Neighbor     `json:"callers"`
	Subtypes  []Neighbor     `json:"subtypes"`
	Tests     []Neighbor     `json:"tests"`
}

// FileImpact returns the dependents of path from other files: importers,
// callers of its symbols, subclasses and implementers, and tests. Each
// list holds at most limit entries, best weight first.
func (s *Store) FileImpact(ctx context.Context, path string, limit int) (*Impact, error) {
	var fid int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM files WHERE path = ?`, path).Scan(&fid); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotIndexed
		}
		return nil, err
	}
	imp := &Impact{Path: path, Totals: map[string]int{}}
	for _, rel := range []struct {
		name  string
		dst   *[]Neighbor
		types []parse.EdgeType
	}{
		{"importers", &imp.Importers, []parse.EdgeType{parse.EdgeImports}},
		{"callers", &imp.Callers, []parse.EdgeType{parse.EdgeCalls}},
		{"subtypes", &imp.Subtypes, []parse.EdgeType{parse.EdgeExtends, parse.EdgeImplements}},
		{"tests", &imp.Tests, []parse.EdgeType{parse.EdgeTests}},
	} {
		args := []any{fid, fid}
		for _, t := range rel.types {
			args = append(args, int(t))
		}
		in := strings.TrimSuffix(strings.Repeat("?,", len(rel.types)), ",")
		from := `FROM symbols d JOIN edges e ON e.dst_id = d.id JOIN symbols s ON s.id = e.src_id JOIN files f ON f.id = s.file_id
			WHERE d.file_id = ? AND s.file_id != ? AND e.type IN (` + in + `)`
		var n int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) `+from, args...).Scan(&n); err != nil {
			return nil, err
		}
		imp.Totals[rel.name] = n
		rows, err := s.db.QueryContext(ctx, `SELECT `+symbolCols+`, e.type, e.weight,
			CASE WHEN d.kind = ? THEN '' ELSE d.qualified_name END `+from+`
			ORDER BY e.weight DESC, s.pagerank DESC, f.path, s.start_line LIMIT ?`,
			append(append([]any{int(parse.KindModule)}, args...), limit)...)
		if err != nil {
			return nil, err
		}
		list := []Neighbor{}
		for rows.Next() {
			var nb Neighbor
			var typ int
			if err := scanRef(rows, &nb.SymbolRef, &typ, &nb.Weight, &nb.Target); err != nil {
				rows.Close()
				return nil, err
			}
			nb.Type = parse.EdgeType(typ).String()
			list = append(list, nb)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		*rel.dst = list
	}
	return imp, nil
}
