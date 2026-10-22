package resolve

import (
	"context"
	"database/sql"
	"path"
	"regexp"

	"github.com/gayanmadurapperuma/codegraph/internal/parse"
)

// querier is satisfied by *sql.Tx; all resolver I/O runs inside one
// transaction (the write connection is single, so reads must share it).
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	PrepareContext(ctx context.Context, query string) (*sql.Stmt, error)
}

type fileInfo struct {
	id      int64
	path    string
	dir     string
	lang    string
	family  string // "go", "python" or "ecma" (JS and TS resolve against each other)
	module  int64  // module symbol id
	modName string // module symbol name (the package name for Go)
	isTest  bool
}

type sym struct {
	id, file, parent int64
	kind             parse.Kind
	name, qname      string
	recv             string // Go method receiver variable, e.g. "r" in (r *T)
	sig              string // functions, methods and tests only (for parameter types)
	exported         bool
}

type ref struct {
	src                     int64
	typ                     parse.EdgeType
	name, qualifier, member string
}

// symtab loads symbols lazily (scoped runs) or all at once (full runs).
type symtab struct {
	ctx context.Context
	q   querier

	files  []*fileInfo
	byID   map[int64]*fileInfo
	byPath map[string]*fileInfo
	byDir  map[string][]*fileInfo
	byBase map[string][]*fileInfo

	syms       map[int64]*sym
	fileSyms   map[int64][]*sym // complete per loaded file
	children   map[int64][]*sym // by parent id, for loaded files
	named      map[string][]*sym
	allLoaded  bool
	refs       map[int64][]ref
	refsLoaded bool
}

func family(lang string) string {
	switch lang {
	case "go", "python":
		return lang
	default:
		return "ecma"
	}
}

func newSymtab(ctx context.Context, q querier) (*symtab, error) {
	t := &symtab{ctx: ctx, q: q,
		byID: map[int64]*fileInfo{}, byPath: map[string]*fileInfo{}, byDir: map[string][]*fileInfo{},
		byBase: map[string][]*fileInfo{}, syms: map[int64]*sym{}, fileSyms: map[int64][]*sym{},
		children: map[int64][]*sym{}, named: map[string][]*sym{}, refs: map[int64][]ref{}}
	rows, err := q.QueryContext(ctx, `SELECT f.id, f.path, f.lang, s.id, s.name
		FROM files f JOIN symbols s ON s.file_id = f.id AND s.kind = ?`, int(parse.KindModule))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		f := &fileInfo{}
		if err := rows.Scan(&f.id, &f.path, &f.lang, &f.module, &f.modName); err != nil {
			return nil, err
		}
		f.dir, f.family, f.isTest = path.Dir(f.path), family(f.lang), parse.IsTestPath(f.path)
		t.files = append(t.files, f)
		t.byID[f.id] = f
		t.byPath[f.path] = f
		t.byDir[f.dir] = append(t.byDir[f.dir], f)
		t.byBase[path.Base(f.path)] = append(t.byBase[path.Base(f.path)], f)
	}
	return t, rows.Err()
}

const symColumns = `SELECT s.id, s.file_id, s.kind, s.name, s.qualified_name, s.exported,
	CASE WHEN s.kind IN (1, 2, 8) THEN s.signature ELSE '' END, coalesce(e.src_id, 0)
	FROM symbols s LEFT JOIN edges e ON e.dst_id = s.id AND e.type = 2`

var goRecv = regexp.MustCompile(`^func \(\s*(\w+)\s`)

func (t *symtab) scanSyms(query string, args ...any) ([]*sym, error) {
	rows, err := t.q.QueryContext(t.ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*sym
	for rows.Next() {
		s := &sym{}
		var kind, exp int
		var sig string
		if err := rows.Scan(&s.id, &s.file, &kind, &s.name, &s.qname, &exp, &sig, &s.parent); err != nil {
			return nil, err
		}
		s.kind, s.exported = parse.Kind(kind), exp == 1
		s.sig = sig
		if m := goRecv.FindStringSubmatch(sig); m != nil {
			s.recv = m[1]
		}
		if old, ok := t.syms[s.id]; ok {
			s = old
		} else {
			t.syms[s.id] = s
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// preloadAll loads every symbol and ref; used by full resolves.
func (t *symtab) preloadAll() error {
	all, err := t.scanSyms(symColumns)
	if err != nil {
		return err
	}
	for _, s := range all {
		t.fileSyms[s.file] = append(t.fileSyms[s.file], s)
		t.children[s.parent] = append(t.children[s.parent], s)
		t.named[s.name] = append(t.named[s.name], s)
	}
	for _, f := range t.files {
		if _, ok := t.fileSyms[f.id]; !ok {
			t.fileSyms[f.id] = nil
		}
	}
	t.allLoaded = true
	rows, err := t.q.QueryContext(t.ctx, `SELECT file_id, src_id, type, name, qualifier, member FROM refs`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var fid int64
		var r ref
		var typ int
		if err := rows.Scan(&fid, &r.src, &typ, &r.name, &r.qualifier, &r.member); err != nil {
			return err
		}
		r.typ = parse.EdgeType(typ)
		t.refs[fid] = append(t.refs[fid], r)
	}
	t.refsLoaded = true
	return rows.Err()
}

// symbolsOf returns all symbols of a file.
func (t *symtab) symbolsOf(fileID int64) []*sym {
	if ss, ok := t.fileSyms[fileID]; ok || t.allLoaded {
		return ss
	}
	ss, err := t.scanSyms(symColumns+` WHERE s.file_id = ?`, fileID)
	if err != nil {
		panic(resolveErr{err})
	}
	t.fileSyms[fileID] = ss
	for _, s := range ss {
		t.children[s.parent] = append(t.children[s.parent], s)
	}
	return ss
}

// childrenOf returns the direct children of a symbol in a file.
func (t *symtab) childrenOf(s *sym) []*sym {
	t.symbolsOf(s.file)
	return t.children[s.id]
}

// byName returns all symbols with the given name.
func (t *symtab) byName(name string) []*sym {
	if ss, ok := t.named[name]; ok || t.allLoaded {
		return ss
	}
	ss, err := t.scanSyms(symColumns+` WHERE s.name = ?`, name)
	if err != nil {
		panic(resolveErr{err})
	}
	t.named[name] = ss
	return ss
}

func (t *symtab) sym(id int64) *sym { return t.syms[id] }

// refsOf returns a file's unresolved refs.
func (t *symtab) refsOf(fileID int64) []ref {
	if rs, ok := t.refs[fileID]; ok || t.refsLoaded {
		return rs
	}
	rows, err := t.q.QueryContext(t.ctx, `SELECT src_id, type, name, qualifier, member FROM refs WHERE file_id = ?`, fileID)
	if err != nil {
		panic(resolveErr{err})
	}
	defer rows.Close()
	var out []ref
	for rows.Next() {
		var r ref
		var typ int
		if err := rows.Scan(&r.src, &typ, &r.name, &r.qualifier, &r.member); err != nil {
			panic(resolveErr{err})
		}
		r.typ = parse.EdgeType(typ)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		panic(resolveErr{err})
	}
	t.refs[fileID] = out
	return out
}

// resolveErr carries database errors out of deep lookup recursion; Run
// recovers it and returns the error.
type resolveErr struct{ err error }
