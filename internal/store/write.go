package store

import (
	"context"
	"database/sql"
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/gayanmadurapperuma/codegraph/internal/parse"
)

// FileMeta is the per-file bookkeeping stored alongside parse results.
type FileMeta struct {
	Path  string
	Lang  string
	Hash  []byte
	Size  int64
	MTime int64
	LOC   int
}

// FileState is what the indexer needs to decide whether a file changed.
type FileState struct {
	ID    int64
	Hash  []byte
	Size  int64
	MTime int64
}

// FileStates loads the state of every indexed file, keyed by path.
func (s *Store) FileStates(ctx context.Context) (map[string]FileState, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, path, hash, size, mtime FROM files`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]FileState{}
	for rows.Next() {
		var st FileState
		var p string
		if err := rows.Scan(&st.ID, &p, &st.Hash, &st.Size, &st.MTime); err != nil {
			return nil, err
		}
		out[p] = st
	}
	return out, rows.Err()
}

// Batch is one write transaction. Callers group ~500 files per batch.
type Batch struct {
	tx                                        *sql.Tx
	track                                     bool
	upsertFile, touchFile, fileID             *sql.Stmt
	delFTS, delEdgesSrc, delEdgesDst, delRefs *sql.Stmt
	delSyms, delFile, delStats, delDirty      *sql.Stmt
	insSym, insFTS, insEdge, insRef           *sql.Stmt
	updSym, delSymFTS, delSymEdges, delSym    *sql.Stmt
	delDefines, oldSyms, oldRefs, fileNames   *sql.Stmt
	markChanged, markDependents               *sql.Stmt
	markName, markMember, markImporter        *sql.Stmt
	markImporters, markSubclasses             *sql.Stmt
	markDefaultImporters, markFileDependents  *sql.Stmt
}

// Flags in resolve_dirty.changed: what about the file itself changed.
const (
	ChangedSymbols = 1 // symbol set: Go package-scope lookups may change
	ChangedRefs    = 2 // import/extends refs: method lookups in subclasses may change
)

// BatchOptions configure a write batch.
type BatchOptions struct {
	// TrackDirty records which files need re-resolution (for an incremental
	// resolve after the batch) and keeps symbol IDs stable across edits.
	// Skip it when a full resolve will follow.
	TrackDirty bool
}

// Begin starts a write batch.
func (s *Store) Begin(ctx context.Context, opt BatchOptions) (*Batch, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	b := &Batch{tx: tx, track: opt.TrackDirty}
	const dirtyFrom = `INSERT OR IGNORE INTO resolve_dirty(file_id) SELECT DISTINCT s.file_id FROM edges e JOIN symbols s ON s.id = e.src_id `
	stmts := []struct {
		dst **sql.Stmt
		sql string
	}{
		{&b.upsertFile, `INSERT INTO files(path, lang, hash, size, mtime, loc) VALUES(?, ?, ?, ?, ?, ?)
			ON CONFLICT(path) DO UPDATE SET lang = excluded.lang, hash = excluded.hash,
			size = excluded.size, mtime = excluded.mtime, loc = excluded.loc RETURNING id`},
		{&b.touchFile, `UPDATE files SET size = ?, mtime = ? WHERE path = ?`},
		{&b.fileID, `SELECT id FROM files WHERE path = ?`},
		{&b.delFTS, `DELETE FROM symbols_fts WHERE rowid IN (SELECT id FROM symbols WHERE file_id = ?)`},
		{&b.delEdgesSrc, `DELETE FROM edges WHERE src_id IN (SELECT id FROM symbols WHERE file_id = ?)`},
		{&b.delEdgesDst, `DELETE FROM edges WHERE dst_id IN (SELECT id FROM symbols WHERE file_id = ?)`},
		{&b.delRefs, `DELETE FROM refs WHERE file_id = ?`},
		{&b.delSyms, `DELETE FROM symbols WHERE file_id = ?`},
		{&b.delFile, `DELETE FROM files WHERE id = ?`},
		{&b.delStats, `DELETE FROM resolve_stats WHERE file_id = ?`},
		{&b.delDirty, `DELETE FROM resolve_dirty WHERE file_id = ?`},
		{&b.insSym, `INSERT INTO symbols(file_id, kind, name, qualified_name, signature, start_line, end_line, exported)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?)`},
		{&b.insFTS, `INSERT INTO symbols_fts(rowid, name, qualified_name, signature, path) VALUES(?, ?, ?, ?, ?)`},
		{&b.insEdge, `INSERT OR IGNORE INTO edges(src_id, dst_id, type, weight) VALUES(?, ?, ?, ?)`},
		{&b.insRef, `INSERT INTO refs(src_id, file_id, type, name, qualifier, member, line) VALUES(?, ?, ?, ?, ?, ?, ?)`},
		// Stable-ID updates (tracked batches).
		{&b.updSym, `UPDATE symbols SET signature = ?, start_line = ?, end_line = ? WHERE id = ?`},
		{&b.delSymFTS, `DELETE FROM symbols_fts WHERE rowid = ?`},
		{&b.delSymEdges, `DELETE FROM edges WHERE src_id = ?1 OR dst_id = ?1`},
		{&b.delSym, `DELETE FROM symbols WHERE id = ?`},
		{&b.delDefines, `DELETE FROM edges WHERE type = 2 AND src_id IN (SELECT id FROM symbols WHERE file_id = ?)`},
		{&b.oldSyms, `SELECT s.id, s.kind, s.name, s.qualified_name, coalesce(p.qualified_name, ''), s.exported
			FROM symbols s LEFT JOIN edges e ON e.dst_id = s.id AND e.type = 2 LEFT JOIN symbols p ON p.id = e.src_id
			WHERE s.file_id = ? ORDER BY s.id`},
		{&b.oldRefs, `SELECT r.type, s.qualified_name, r.name, r.qualifier, r.member
			FROM refs r JOIN symbols s ON s.id = r.src_id WHERE r.file_id = ? AND r.type != 3`},
		{&b.fileNames, `SELECT DISTINCT name FROM symbols WHERE file_id = ? AND kind != 7`},
		// Dirty tracking (D11).
		{&b.markChanged, `INSERT INTO resolve_dirty(file_id, changed) VALUES(?1, ?2)
			ON CONFLICT(file_id) DO UPDATE SET changed = changed | ?2`},
		{&b.markDependents, dirtyFrom + `WHERE e.dst_id = ?1 AND e.type != 2 AND s.file_id != ?2`},
		{&b.markFileDependents, dirtyFrom + `WHERE e.dst_id IN (SELECT id FROM symbols WHERE file_id = ?1) AND e.type != 2 AND s.file_id != ?1`},
		{&b.markName, `INSERT OR IGNORE INTO resolve_dirty(file_id) SELECT DISTINCT file_id FROM refs WHERE name = ?`},
		{&b.markMember, `INSERT OR IGNORE INTO resolve_dirty(file_id) SELECT DISTINCT file_id FROM refs WHERE member = ? AND member != ''`},
		{&b.markImporter, `INSERT OR IGNORE INTO resolve_dirty(file_id)
			SELECT DISTINCT file_id FROM refs WHERE type = 1 AND (name = ?1 OR member = ?1
				OR name LIKE '%/' || ?1 OR name LIKE '%.' || ?1 OR name LIKE '%/' || ?1 || '.%')`},
		{&b.markImporters, dirtyFrom + `WHERE e.dst_id = ? AND e.type = 1`},
		{&b.markSubclasses, dirtyFrom + `WHERE e.type IN (4, 5) AND e.dst_id IN (SELECT id FROM symbols WHERE file_id = ?1) AND s.file_id != ?1`},
		{&b.markDefaultImporters, dirtyFrom + `WHERE e.dst_id = ? AND e.type = 1 AND EXISTS (
			SELECT 1 FROM refs r WHERE r.file_id = s.file_id AND r.type = 1 AND r.member = 'default')`},
	}
	for _, st := range stmts {
		p, err := tx.PrepareContext(ctx, st.sql)
		if err != nil {
			tx.Rollback()
			return nil, err
		}
		*st.dst = p
	}
	return b, nil
}

func (b *Batch) Commit() error   { return b.tx.Commit() }
func (b *Batch) Rollback() error { return b.tx.Rollback() }

func (b *Batch) exec(ctx context.Context, st *sql.Stmt, args ...any) error {
	_, err := st.ExecContext(ctx, args...)
	return err
}

// markNameRefs marks files whose refs mention name: adding or removing a
// definition can change how those refs resolve (bindings, receiver
// candidates, global uniqueness).
func (b *Batch) markNameRefs(ctx context.Context, name string) error {
	if err := b.exec(ctx, b.markName, name); err != nil {
		return err
	}
	return b.exec(ctx, b.markMember, name)
}

// clearFile removes everything derived from a file except its files row.
// Edges pointing into the file from elsewhere are dropped too; when
// tracking, the files that owned them are marked dirty first.
func (b *Batch) clearFile(ctx context.Context, id int64) error {
	if b.track {
		if err := b.exec(ctx, b.markFileDependents, id); err != nil {
			return err
		}
		names, err := b.names(ctx, id)
		if err != nil {
			return err
		}
		for _, n := range names {
			if err := b.markNameRefs(ctx, n); err != nil {
				return err
			}
		}
	}
	for _, st := range []*sql.Stmt{b.delFTS, b.delEdgesSrc, b.delEdgesDst, b.delRefs, b.delSyms} {
		if err := b.exec(ctx, st, id); err != nil {
			return err
		}
	}
	return nil
}

func (b *Batch) names(ctx context.Context, fileID int64) ([]string, error) {
	rows, err := b.fileNames.QueryContext(ctx, fileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ReplaceFile writes a file's parse result, replacing any previous data.
func (b *Batch) ReplaceFile(ctx context.Context, m FileMeta, r *parse.FileResult, existed bool) error {
	var fid int64
	if err := b.upsertFile.QueryRowContext(ctx, m.Path, m.Lang, m.Hash, m.Size, m.MTime, m.LOC).Scan(&fid); err != nil {
		return err
	}
	if existed && b.track {
		return b.replaceTracked(ctx, fid, m, r)
	}
	if existed {
		if err := b.clearFile(ctx, fid); err != nil {
			return err
		}
	}
	ids := make([]int64, len(r.Symbols))
	for i := range r.Symbols {
		if err := b.insertSymbol(ctx, fid, m.Path, r, i, ids); err != nil {
			return err
		}
	}
	if err := b.writeRefs(ctx, fid, r, ids); err != nil {
		return err
	}
	if !b.track {
		return nil
	}
	// A new file: its names may satisfy refs elsewhere, and it may satisfy
	// imports that dangled until now.
	if err := b.exec(ctx, b.markChanged, fid, ChangedSymbols|ChangedRefs); err != nil {
		return err
	}
	for _, s := range r.Symbols {
		if s.Kind != parse.KindModule {
			if err := b.markNameRefs(ctx, s.Name); err != nil {
				return err
			}
		}
	}
	for _, stem := range ModuleStems(m.Path) {
		if err := b.exec(ctx, b.markImporter, stem); err != nil {
			return err
		}
	}
	return nil
}

func (b *Batch) insertSymbol(ctx context.Context, fid int64, path string, r *parse.FileResult, i int, ids []int64) error {
	s := r.Symbols[i]
	res, err := b.insSym.ExecContext(ctx, fid, int(s.Kind), s.Name, s.QualifiedName, s.Signature,
		s.StartLine, s.EndLine, boolInt(s.Exported))
	if err != nil {
		return err
	}
	if ids[i], err = res.LastInsertId(); err != nil {
		return err
	}
	return b.writeFTS(ctx, path, s, ids[i])
}

func (b *Batch) writeFTS(ctx context.Context, path string, s parse.Symbol, id int64) error {
	return b.exec(ctx, b.insFTS, id, parse.SplitText(s.Name), splitAll(s.QualifiedName),
		splitAll(s.Signature), parse.SplitText(path))
}

// writeRefs inserts DEFINES edges and refs for freshly assigned ids.
func (b *Batch) writeRefs(ctx context.Context, fid int64, r *parse.FileResult, ids []int64) error {
	for i, s := range r.Symbols {
		if s.Parent >= 0 {
			if err := b.exec(ctx, b.insEdge, ids[s.Parent], ids[i], int(parse.EdgeDefines), 1.0); err != nil {
				return err
			}
		}
	}
	for _, ref := range r.Refs {
		if err := b.exec(ctx, b.insRef, ids[ref.From], fid, int(ref.Type), ref.Name,
			ref.Qualifier, ref.Member, ref.Line); err != nil {
			return err
		}
	}
	return nil
}

func symKey(kind parse.Kind, name, qname, parent string, exported bool) string {
	return fmt.Sprintf("%d\x00%s\x00%s\x00%s\x00%v", kind, name, qname, parent, exported)
}

// replaceTracked rewrites an existing file while keeping the IDs of
// symbols whose identity (kind, name, qualified name, parent, exported) is
// unchanged, so other files' edges to them stay valid. Only files that
// could resolve differently are marked dirty:
//   - removed symbols: files with edges into them;
//   - added or removed names: files whose refs mention the name;
//   - changed import/extends refs (read by importers through re-exports and
//     by subclasses through bases): importers and subclasses;
//   - a changed symbol set in JS/TS: files default-importing it.
//
// A body-only edit therefore dirties just the file itself.
func (b *Batch) replaceTracked(ctx context.Context, fid int64, m FileMeta, r *parse.FileResult) error {
	type old struct {
		id   int64
		name string
	}
	queue := map[string][]old{}
	rows, err := b.oldSyms.QueryContext(ctx, fid)
	if err != nil {
		return err
	}
	var moduleID int64
	for rows.Next() {
		var o old
		var kind, exp int
		var qname, parent string
		if err := rows.Scan(&o.id, &kind, &o.name, &qname, &parent, &exp); err != nil {
			rows.Close()
			return err
		}
		if parse.Kind(kind) == parse.KindModule {
			moduleID = o.id
		}
		k := symKey(parse.Kind(kind), o.name, qname, parent, exp == 1)
		queue[k] = append(queue[k], o)
	}
	rows.Close()
	oldRefs, err := b.interfaceRefs(ctx, fid)
	if err != nil {
		return err
	}

	ids := make([]int64, len(r.Symbols))
	var added []int
	for i, s := range r.Symbols {
		parent := ""
		if s.Parent >= 0 {
			parent = r.Symbols[s.Parent].QualifiedName
		}
		k := symKey(s.Kind, s.Name, s.QualifiedName, parent, s.Exported)
		if q := queue[k]; len(q) > 0 {
			ids[i], queue[k] = q[0].id, q[1:]
		} else {
			added = append(added, i)
		}
	}
	var removed []old
	for _, q := range queue {
		removed = append(removed, q...)
	}
	symbolsChanged := len(added)+len(removed) > 0
	refsChanged := !slices.Equal(oldRefs, newInterfaceRefs(r))

	// Mark before deleting: the edges that identify dependents go away.
	for _, o := range removed {
		if err := b.exec(ctx, b.markDependents, o.id, fid); err != nil {
			return err
		}
		if err := b.markNameRefs(ctx, o.name); err != nil {
			return err
		}
	}
	for _, i := range added {
		if err := b.markNameRefs(ctx, r.Symbols[i].Name); err != nil {
			return err
		}
	}
	if refsChanged {
		if err := b.exec(ctx, b.markImporters, moduleID); err != nil {
			return err
		}
		if err := b.exec(ctx, b.markSubclasses, fid); err != nil {
			return err
		}
	}
	if symbolsChanged && (m.Lang == "typescript" || m.Lang == "javascript") {
		if err := b.exec(ctx, b.markDefaultImporters, moduleID); err != nil {
			return err
		}
	}

	for _, o := range removed {
		for _, st := range []*sql.Stmt{b.delSymFTS, b.delSymEdges, b.delSym} {
			if err := b.exec(ctx, st, o.id); err != nil {
				return err
			}
		}
	}
	if err := b.exec(ctx, b.delDefines, fid); err != nil {
		return err
	}
	if err := b.exec(ctx, b.delRefs, fid); err != nil {
		return err
	}
	isAdded := make(map[int]bool, len(added))
	for _, i := range added {
		isAdded[i] = true
	}
	for i, s := range r.Symbols {
		if isAdded[i] {
			if err := b.insertSymbol(ctx, fid, m.Path, r, i, ids); err != nil {
				return err
			}
			continue
		}
		if err := b.exec(ctx, b.updSym, s.Signature, s.StartLine, s.EndLine, ids[i]); err != nil {
			return err
		}
		if err := b.exec(ctx, b.delSymFTS, ids[i]); err != nil {
			return err
		}
		if err := b.writeFTS(ctx, m.Path, s, ids[i]); err != nil {
			return err
		}
	}
	if err := b.writeRefs(ctx, fid, r, ids); err != nil {
		return err
	}
	var flags int
	if symbolsChanged {
		flags |= ChangedSymbols
	}
	if refsChanged {
		flags |= ChangedRefs
	}
	return b.exec(ctx, b.markChanged, fid, flags)
}

func (b *Batch) interfaceRefs(ctx context.Context, fid int64) ([]string, error) {
	rows, err := b.oldRefs.QueryContext(ctx, fid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var typ int
		var src, name, qual, member string
		if err := rows.Scan(&typ, &src, &name, &qual, &member); err != nil {
			return nil, err
		}
		out = append(out, refKey(typ, src, name, qual, member))
	}
	sort.Strings(out)
	return out, rows.Err()
}

// newInterfaceRefs are a parse result's import/extends/implements refs in
// the same form as interfaceRefs. Call refs only affect the file's own edges.
func newInterfaceRefs(r *parse.FileResult) []string {
	var out []string
	for _, ref := range r.Refs {
		if ref.Type != parse.EdgeCalls {
			out = append(out, refKey(int(ref.Type), r.Symbols[ref.From].QualifiedName, ref.Name, ref.Qualifier, ref.Member))
		}
	}
	sort.Strings(out)
	return out
}

func refKey(typ int, src, name, qual, member string) string {
	return fmt.Sprintf("%d\x00%s\x00%s\x00%s\x00%s", typ, src, name, qual, member)
}

// ModuleStems are the names an import of path could end with: the file
// name without extension, or the directory name for index/__init__ files.
func ModuleStems(p string) []string {
	base := path.Base(p)
	stem := strings.TrimSuffix(base, path.Ext(base))
	if stem == "index" || stem == "__init__" {
		if d := path.Base(path.Dir(p)); d != "." && d != "/" {
			return []string{d}
		}
		return nil
	}
	stems := []string{stem}
	if strings.HasSuffix(p, ".go") {
		if d := path.Base(path.Dir(p)); d != "." && d != stem {
			stems = append(stems, d) // Go imports name the package directory
		}
	}
	return stems
}

// TouchFile records a new size/mtime for a file whose content is unchanged.
func (b *Batch) TouchFile(ctx context.Context, path string, size, mtime int64) error {
	_, err := b.touchFile.ExecContext(ctx, size, mtime, path)
	return err
}

// DeleteFile removes a file and everything derived from it. Missing files
// are ignored.
func (b *Batch) DeleteFile(ctx context.Context, path string) error {
	var id int64
	err := b.fileID.QueryRowContext(ctx, path).Scan(&id)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if err := b.clearFile(ctx, id); err != nil {
		return err
	}
	for _, st := range []*sql.Stmt{b.delStats, b.delDirty, b.delFile} {
		if _, err := st.ExecContext(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// splitAll applies identifier splitting to every word of s so camelCase
// parts inside signatures and qualified names are searchable.
func splitAll(s string) string {
	if s == "" {
		return ""
	}
	words := strings.FieldsFunc(s, func(r rune) bool {
		return !(r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r > 127)
	})
	var b strings.Builder
	for _, w := range words {
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(parse.SplitText(w))
	}
	return b.String()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
