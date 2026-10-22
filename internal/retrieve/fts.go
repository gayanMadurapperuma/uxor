package retrieve

import (
	"context"
	"database/sql"
	"sort"
	"strings"

	"github.com/gayanmadurapperuma/codegraph/internal/parse"
)

// Column weights for bm25 over symbols_fts(name, qualified_name, signature, path).
const ftsRank = `bm25(symbols_fts, 10, 5, 2, 1)`

// exactBoost is added to a seed whose name equals an identifier in the query.
const exactBoost = 0.5

// prefixMinLen is the shortest term that also matches as a prefix.
const prefixMinLen = 4

// FTS seeds from the full-text index of identifier-split names,
// qualified names, signatures and paths.
type FTS struct{ DB *sql.DB }

// MatchExpr builds a MATCH expression in which every term is a quoted
// string, so nothing in the prompt can become FTS5 syntax (NEAR, AND, -,
// column filters, …). Terms of four or more characters also match as
// prefixes ("user"* finds users, username).
func MatchExpr(terms []string) string {
	parts := make([]string, 0, len(terms))
	for _, t := range terms {
		if t == "" {
			continue
		}
		p := `"` + strings.ReplaceAll(t, `"`, `""`) + `"`
		if len([]rune(t)) >= prefixMinLen {
			p += "*"
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, " OR ")
}

// Seeds ranks symbols by bm25, normalized so the best match scores 1, then
// adds exactBoost for exact identifier matches. Identifiers are also looked
// up by name directly, so an exact hit is never lost to the bm25 cut.
func (f *FTS) Seeds(ctx context.Context, q Query, n int) ([]Seed, error) {
	exact := map[string]bool{}
	for _, id := range q.Idents {
		exact[id] = true
	}
	byID := map[int64]*Seed{}

	if expr := MatchExpr(q.Terms); expr != "" {
		rows, err := f.DB.QueryContext(ctx, `
			SELECT s.id, s.name, `+ftsRank+` AS r
			FROM symbols_fts JOIN symbols s ON s.id = symbols_fts.rowid
			WHERE symbols_fts MATCH ? ORDER BY r LIMIT ?`, expr, 2*n)
		if err != nil {
			return nil, err
		}
		best := 0.0
		for rows.Next() {
			var id int64
			var name string
			var r float64
			if err := rows.Scan(&id, &name, &r); err != nil {
				rows.Close()
				return nil, err
			}
			if best == 0 {
				best = r // bm25 is negative; the first row is the best
			}
			s := &Seed{ID: id, Why: "match"}
			if best != 0 {
				s.Score = r / best
			}
			if exact[name] {
				s.Score += exactBoost
				s.Why = "match: exact name"
			}
			byID[id] = s
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}

	if len(q.Idents) > 0 {
		args := make([]any, 0, len(q.Idents)+2)
		for _, id := range q.Idents {
			args = append(args, id)
		}
		args = append(args, int(parse.KindModule), n)
		rows, err := f.DB.QueryContext(ctx, `SELECT id FROM symbols WHERE name IN (`+placeholders(len(q.Idents))+`) AND kind != ? LIMIT ?`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			if byID[id] == nil {
				byID[id] = &Seed{ID: id, Score: exactBoost, Why: "match: exact name"}
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}

	out := make([]Seed, 0, len(byID))
	for _, s := range byID {
		out = append(out, *s)
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
	return out, nil
}

func placeholders(n int) string {
	if n == 0 {
		return ""
	}
	return strings.Repeat("?,", n-1) + "?"
}

func idArgs(ids []int64, extra ...any) []any {
	args := make([]any, 0, len(ids)+len(extra))
	for _, id := range ids {
		args = append(args, id)
	}
	return append(args, extra...)
}
