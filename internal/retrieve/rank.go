package retrieve

import (
	"context"
	"database/sql"
	"math"
	"path"
	"sort"
	"strings"

	"github.com/gayanmadurapperuma/uxor/internal/parse"
)

// maxCandidates bounds the detail lookup; nodes are pre-cut by seed +
// expansion score, which dominates the final score. The scoring constants
// are in Tuning.
const maxCandidates = 200

// candidate is a ranked symbol with what packing and output need.
type candidate struct {
	ID            int64
	Kind          parse.Kind
	Name          string
	QualifiedName string
	Signature     string
	Path          string
	StartLine     int
	EndLine       int
	Parent        int64 // DEFINES parent, 0 for module symbols
	Score         float64
	Why           string
}

// rank scores the expanded nodes and returns listable candidates (module
// symbols excluded), best first.
func rank(ctx context.Context, db *sql.DB, nodes map[int64]*node, q Query, t *Tuning) ([]candidate, error) {
	ids := make([]int64, 0, len(nodes))
	for id := range nodes {
		ids = append(ids, id)
	}
	partial := func(id int64) float64 { n := nodes[id]; return n.seed + n.exp }
	sort.Slice(ids, func(i, j int) bool {
		a, b := partial(ids[i]), partial(ids[j])
		if a != b {
			return a > b
		}
		return ids[i] < ids[j]
	})
	if len(ids) > maxCandidates {
		ids = ids[:maxCandidates]
	}
	// Also load the nodes the best paths came from, to name them in `why`.
	want := map[int64]bool{}
	lookup := make([]int64, 0, 2*len(ids))
	for _, id := range ids {
		for _, x := range []int64{id, nodes[id].from} {
			if x != 0 && !want[x] {
				want[x] = true
				lookup = append(lookup, x)
			}
		}
	}

	type detail struct {
		candidate
		pagerank float64
		tested   bool
	}
	details := make(map[int64]*detail, len(lookup))
	rows, err := db.QueryContext(ctx, `
		SELECT s.id, s.kind, s.name, s.qualified_name, s.signature, s.start_line, s.end_line,
		       s.pagerank, f.path,
		       coalesce((SELECT e.src_id FROM edges e WHERE e.dst_id = s.id AND e.type = ? LIMIT 1), 0),
		       EXISTS(SELECT 1 FROM edges e WHERE e.dst_id = s.id AND e.type = ?)
		FROM symbols s JOIN files f ON f.id = s.file_id
		WHERE s.id IN (`+placeholders(len(lookup))+`)`,
		append([]any{int(parse.EdgeDefines), int(parse.EdgeTests)}, idArgs(lookup)...)...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		d := &detail{}
		var kind int
		if err := rows.Scan(&d.ID, &kind, &d.Name, &d.QualifiedName, &d.Signature, &d.StartLine, &d.EndLine,
			&d.pagerank, &d.Path, &d.Parent, &d.tested); err != nil {
			rows.Close()
			return nil, err
		}
		d.Kind = parse.Kind(kind)
		details[d.ID] = d
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]candidate, 0, len(ids))
	for _, id := range ids {
		d := details[id]
		if d == nil || d.Kind == parse.KindModule {
			continue
		}
		n := nodes[id]
		c := d.candidate
		// Seed scores were already adjusted before expansion.
		exp := n.exp * testFactor(q, isTest(c.Kind, c.Path), t)
		c.Score = (n.seed + exp) * (1 + t.PagerankWeight*math.Log1p(max(d.pagerank, 0)))
		if d.tested {
			c.Score += t.TestedBonus
		}
		switch {
		case n.seed > 0:
			c.Why = n.seedWhy
		case details[n.from] != nil:
			c.Why = n.label + " " + displayName(&details[n.from].candidate)
		default:
			c.Why = n.label
		}
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// testDirs are path segments that mark test code beyond the per-language
// file conventions of parse.IsTestPath (Django's tests/x/tests.py,
// Go testutil packages, JS __tests__ helpers).
var testDirs = map[string]bool{
	"test": true, "tests": true, "testing": true, "__tests__": true,
	"testdata": true, "testutil": true, "e2e": true,
}

// isTest reports whether a symbol is test code for ranking purposes.
func isTest(kind parse.Kind, p string) bool {
	if kind == parse.KindTest || parse.IsTestPath(p) {
		return true
	}
	if b := path.Base(p); b == "tests.py" || strings.HasPrefix(b, "tests_") {
		return true
	}
	for _, seg := range strings.Split(path.Dir(p), "/") {
		if testDirs[seg] {
			return true
		}
	}
	return false
}

// testFactor scales test code: down normally, up when the query is
// about tests.
func testFactor(q Query, test bool, t *Tuning) float64 {
	switch {
	case !test:
		return 1
	case q.WantsTests:
		return t.TestBoost
	default:
		return t.TestPenalty
	}
}

// adjustTestSeeds applies testFactor to seeds before expansion, so a test
// matching the query does not pass its full score to every helper it
// calls (and does when tests are what the query wants).
func adjustTestSeeds(ctx context.Context, db *sql.DB, seeds []Seed, q Query, t *Tuning) error {
	if len(seeds) == 0 {
		return nil
	}
	ids := make([]int64, len(seeds))
	for i, s := range seeds {
		ids[i] = s.ID
	}
	rows, err := db.QueryContext(ctx, `
		SELECT s.id, s.kind, f.path FROM symbols s JOIN files f ON f.id = s.file_id
		WHERE s.id IN (`+placeholders(len(ids))+`)`, idArgs(ids)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	test := map[int64]bool{}
	for rows.Next() {
		var id int64
		var kind int
		var p string
		if err := rows.Scan(&id, &kind, &p); err != nil {
			return err
		}
		test[id] = isTest(parse.Kind(kind), p)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range seeds {
		seeds[i].Score *= testFactor(q, test[seeds[i].ID], t)
	}
	return nil
}

// displayName names a symbol in `why`: its qualified name, or the file for
// a module symbol.
func displayName(c *candidate) string {
	if c.Kind == parse.KindModule {
		return c.Path
	}
	return c.QualifiedName
}
