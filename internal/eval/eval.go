// Package eval measures retrieval against a repository's own history: each
// commit's subject is a query, and the indexed files it changed are the
// answer. In checkout-parent mode every query runs against an index of the
// commit's parent, built in a temporary worktree, so the answer cannot leak
// in through the index.
package eval

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gayanmadurapperuma/uxor/internal/config"
	"github.com/gayanmadurapperuma/uxor/internal/index"
	"github.com/gayanmadurapperuma/uxor/internal/retrieve"
	"github.com/gayanmadurapperuma/uxor/internal/store"
)

// Options configures a run.
type Options struct {
	Commits        int  // commits to evaluate; default 100
	MaxChanged     int  // skip commits changing more indexable files; default 20
	CheckoutParent bool // index each commit's parent in a temporary worktree
	Savings        bool // also estimate token savings; needs CheckoutParent
	Body           bool // add the commit body to the query
	Tuning         *retrieve.Tuning
	Variants       []Variant // more tunings, queried against the same indexes
	Progress       io.Writer // one line per evaluated commit; nil = quiet
}

// Variant is a named tuning evaluated alongside Options.Tuning. Indexing
// dominates a checkout-parent run, so a sweep shares one.
type Variant struct {
	Name   string
	Tuning *retrieve.Tuning
}

// VariantReport summarizes one Variant over the same tasks.
type VariantReport struct {
	Name    string  `json:"name"`
	All     Summary `json:"all"`
	Tune    Summary `json:"tune"`
	Holdout Summary `json:"holdout"`
	tasks   []Task
}

// scanFactor bounds how many commits are examined per commit wanted.
const scanFactor = 10

// tuneShare of the evaluated commits, the oldest, form the tune split; the
// newest rest is the holdout.
const tuneShare = 2.0 / 3

// Task is one evaluated commit.
type Task struct {
	SHA         string       `json:"sha"`
	Subject     string       `json:"subject"`
	Split       string       `json:"split"`
	Truth       []string     `json:"truth"`
	Ranks       []int        `json:"ranks"` // per truth file in the full ranking; 0 = not ranked
	Recall5     float64      `json:"recall_at_5"`
	Recall10    float64      `json:"recall_at_10"`
	RR          float64      `json:"reciprocal_rank"`
	HookHit     bool         `json:"hook_hit"`
	HookTokens  int          `json:"hook_tokens"`
	HookMS      float64      `json:"hook_ms"`
	QueryHit    bool         `json:"query_hit"`
	QueryTokens int          `json:"query_tokens"`
	QueryMS     float64      `json:"query_ms"`
	Savings     *TaskSavings `json:"savings,omitempty"`
	queryTop    []string     // query-budget answer files, best first
}

// Summary aggregates tasks.
type Summary struct {
	Tasks       int     `json:"tasks"`
	Recall5     float64 `json:"recall_at_5"`
	Recall10    float64 `json:"recall_at_10"`
	MRR         float64 `json:"mrr"`
	HookHit     float64 `json:"hook_hit_rate"`
	QueryHit    float64 `json:"query_hit_rate"`
	HookTokens  float64 `json:"hook_tokens_mean"`
	QueryTokens float64 `json:"query_tokens_mean"`
	HookP50MS   float64 `json:"hook_p50_ms"`
	HookP95MS   float64 `json:"hook_p95_ms"`
	QueryP50MS  float64 `json:"query_p50_ms"`
	QueryP95MS  float64 `json:"query_p95_ms"`
}

// Report is the result of a run.
type Report struct {
	Root       string          `json:"root"`
	Mode       string          `json:"mode"` // "head" or "checkout-parent"
	Considered int             `json:"commits_considered"`
	Skipped    map[string]int  `json:"skipped"`
	All        Summary         `json:"all"`
	Tune       Summary         `json:"tune"`
	Holdout    Summary         `json:"holdout"`
	Savings    *SavingsSummary `json:"savings,omitempty"`
	Variants   []VariantReport `json:"variants,omitempty"`
	Caveats    []string        `json:"caveats"`
	Tasks      []Task          `json:"tasks"`
}

// picked is a commit chosen for evaluation, with its changes.
type picked struct {
	Commit
	changes []Change
}

// Run evaluates p's recent history. The project root must be the git
// top level, so that git paths and index paths agree.
func Run(ctx context.Context, p *config.Project, opt Options) (*Report, error) {
	if opt.Commits <= 0 {
		opt.Commits = 100
	}
	if opt.MaxChanged <= 0 {
		opt.MaxChanged = 20
	}
	if opt.Savings && !opt.CheckoutParent {
		return nil, errors.New("savings need --checkout-parent: they read each file as it was before the commit")
	}
	top, err := git(ctx, p.Root, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, err
	}
	if t, _ := filepath.EvalSymlinks(strings.TrimSpace(string(top))); t != p.Root {
		return nil, fmt.Errorf("eval needs the project root (%s) to be the git top level (%s)", p.Root, t)
	}
	forget, err := p.ForgetGlobs()
	if err != nil {
		return nil, err
	}
	m := index.NewMatcher(p.Config, forget)

	rep := &Report{Root: p.Root, Mode: "head", Skipped: map[string]int{}, Tasks: []Task{}}
	if opt.CheckoutParent {
		rep.Mode = "checkout-parent"
	}
	log, err := Commits(ctx, p.Root, opt.Commits*scanFactor)
	if err != nil {
		return nil, err
	}
	var picks []picked
	for _, c := range log {
		if len(picks) == opt.Commits {
			break
		}
		rep.Considered++
		changes, err := Changed(ctx, p.Root, c)
		if err != nil {
			return nil, err
		}
		switch n := len(truthPaths(changes, true, m)); {
		case n == 0:
			rep.Skipped["no indexable files changed"]++
		case n > opt.MaxChanged:
			rep.Skipped[fmt.Sprintf("more than %d files", opt.MaxChanged)]++
		default:
			picks = append(picks, picked{c, changes})
		}
	}
	// Oldest first: the tune split is the older history, and in
	// checkout-parent mode consecutive parents differ little, so each
	// re-index stays incremental.
	for i, j := 0, len(picks)-1; i < j; i, j = i+1, j-1 {
		picks[i], picks[j] = picks[j], picks[i]
	}

	for _, v := range opt.Variants {
		rep.Variants = append(rep.Variants, VariantReport{Name: v.Name})
	}
	e := &evaluator{opt: opt, m: m, rep: rep, total: len(picks)}
	if opt.CheckoutParent {
		err = e.runParents(ctx, p, forget, picks)
	} else {
		err = e.runHead(ctx, p, picks)
	}
	if err != nil {
		return nil, err
	}
	e.summarize()
	return rep, nil
}

type evaluator struct {
	opt   Options
	m     *index.Matcher
	rep   *Report
	total int
	done  int
}

// runHead queries the existing index at HEAD for every commit.
func (e *evaluator) runHead(ctx context.Context, p *config.Project, picks []picked) error {
	if _, err := os.Stat(p.DBPath); err != nil {
		return fmt.Errorf("no index at %s: run `uxor index` first, or use --checkout-parent", p.DBPath)
	}
	st, err := store.Open(ctx, p.DBPath, store.Options{ReadOnly: true})
	if err != nil {
		return err
	}
	defer st.Close()
	indexed, err := pathSet(ctx, st)
	if err != nil {
		return err
	}
	for _, pk := range picks {
		if err := e.task(ctx, st, p.Config, pk, indexed, false, nil); err != nil {
			return err
		}
	}
	return nil
}

// runParents checks out each commit's parent in one temporary worktree,
// re-indexes it incrementally and queries that index.
func (e *evaluator) runParents(ctx context.Context, p *config.Project, forget []string, picks []picked) error {
	if len(picks) == 0 {
		return nil
	}
	wt, remove, err := addWorktree(ctx, p.Root, picks[0].Parent)
	if err != nil {
		return err
	}
	defer remove()
	// Untracked local settings apply to the worktree too.
	if b, err := os.ReadFile(filepath.Join(p.Root, config.FileName)); err == nil {
		if _, err := os.Stat(filepath.Join(wt, config.FileName)); errors.Is(err, os.ErrNotExist) {
			if err := os.WriteFile(filepath.Join(wt, config.FileName), b, 0o644); err != nil {
				return err
			}
		}
	}
	wp, err := config.Open(wt)
	if err != nil {
		return err
	}
	if err := wp.EnsureDataDir(); err != nil {
		return err
	}
	if len(forget) > 0 {
		if err := wp.SetForgetGlobs(forget); err != nil {
			return err
		}
	}
	st, err := store.Open(ctx, wp.DBPath, store.Options{})
	if err != nil {
		return err
	}
	defer st.Close()

	for _, pk := range picks {
		if _, err := git(ctx, wt, "checkout", "--quiet", "--force", "--detach", pk.Parent); err != nil {
			return err
		}
		if _, err := index.Run(ctx, wp, st, index.Options{}); err != nil {
			return fmt.Errorf("index %s: %w", short(pk.Parent), err)
		}
		indexed, err := pathSet(ctx, st)
		if err != nil {
			return err
		}
		var sv *saver
		if e.opt.Savings {
			sv = &saver{root: wt, st: st, indexed: indexed}
		}
		if err := e.task(ctx, st, wp.Config, pk, indexed, true, sv); err != nil {
			return err
		}
	}
	return nil
}

// addWorktree creates a detached worktree of root at rev in a new temporary
// directory. remove deletes it, also after ctx is cancelled.
func addWorktree(ctx context.Context, root, rev string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "uxor-eval-")
	if err != nil {
		return "", nil, err
	}
	if dir, err = filepath.EvalSymlinks(dir); err != nil {
		return "", nil, err
	}
	wt := filepath.Join(dir, "wt")
	remove := func() {
		bg := context.Background()
		if _, err := git(bg, root, "worktree", "remove", "--force", wt); err != nil {
			// An add interrupted by cancellation leaves the entry locked
			// ("initializing"), which prune skips.
			git(bg, root, "worktree", "unlock", wt)
			os.RemoveAll(wt)
			git(bg, root, "worktree", "prune")
		}
		os.RemoveAll(dir)
	}
	if _, err := git(ctx, root, "worktree", "add", "--quiet", "--detach", wt, rev); err != nil {
		remove()
		return "", nil, err
	}
	return wt, remove, nil
}

// task evaluates one commit against st and records it.
func (e *evaluator) task(ctx context.Context, st *store.Store, cfg *config.Config, pk picked, indexed map[string]bool, parentSide bool, sv *saver) error {
	var truth []string
	for _, path := range truthPaths(pk.changes, parentSide, e.m) {
		if indexed[path] {
			truth = append(truth, path)
		}
	}
	if len(truth) == 0 {
		e.rep.Skipped["changed files not in the index"]++
		return nil
	}
	query := pk.Subject
	if e.opt.Body && pk.Body != "" {
		query += "\n\n" + pk.Body
	}
	t, err := measure(ctx, st, cfg, query, truth, e.opt.Tuning)
	if err != nil {
		return err
	}
	t.SHA, t.Subject = pk.SHA, pk.Subject
	if sv != nil {
		if t.Savings, err = sv.task(ctx, pk.Commit, query, truth, t.queryTop, t.QueryTokens); err != nil {
			return err
		}
	}
	e.rep.Tasks = append(e.rep.Tasks, t)
	for i, v := range e.opt.Variants {
		vt, err := measure(ctx, st, cfg, query, truth, v.Tuning)
		if err != nil {
			return err
		}
		e.rep.Variants[i].tasks = append(e.rep.Variants[i].tasks, vt)
	}
	e.done++
	if e.opt.Progress != nil {
		fmt.Fprintf(e.opt.Progress, "[%d/%d] %s r@5 %.2f rr %.2f  %s\n", e.done, e.total, short(t.SHA), t.Recall5, t.RR, t.Subject)
	}
	return nil
}

// measure runs the full ranking and both answer budgets for one query.
func measure(ctx context.Context, st *store.Store, cfg *config.Config, query string, truth []string, tuning *retrieve.Tuning) (Task, error) {
	t := Task{Truth: truth}
	ranked, err := retrieve.RankFiles(ctx, st, cfg, query, retrieve.Options{Tuning: tuning})
	if err != nil {
		return t, err
	}
	t.Ranks = ranks(filePaths(ranked), truth)
	t.Recall5, t.Recall10, t.RR = recallAt(t.Ranks, 5), recallAt(t.Ranks, 10), reciprocalRank(t.Ranks)

	b := cfg.Budgets
	hook, hookMS, err := timedFind(ctx, st, cfg, query, retrieve.Options{Budget: b.HookTokens, MaxFiles: b.HookTop, Tuning: tuning})
	if err != nil {
		return t, err
	}
	t.HookHit, t.HookTokens, t.HookMS = hit(filePaths(hook.Files), truth), hook.TokensUsed, hookMS
	q, queryMS, err := timedFind(ctx, st, cfg, query, retrieve.Options{Budget: b.QueryTokens, Tuning: tuning})
	if err != nil {
		return t, err
	}
	t.queryTop = filePaths(q.Files)
	t.QueryHit, t.QueryTokens, t.QueryMS = hit(t.queryTop, truth), q.TokensUsed, queryMS
	return t, nil
}

func timedFind(ctx context.Context, st *store.Store, cfg *config.Config, query string, opt retrieve.Options) (*retrieve.Result, float64, error) {
	start := time.Now()
	res, err := retrieve.FindRelevant(ctx, st, cfg, query, opt)
	return res, float64(time.Since(start).Microseconds()) / 1000, err
}

// truthPaths are the changed paths that would be indexed: parent-side
// paths (old names of renames, deleted files) or, at HEAD, the commit-side
// paths. Added and copied files are never included: nothing can find a
// file before it exists, and a copy's source is unchanged.
func truthPaths(changes []Change, parentSide bool, m *index.Matcher) []string {
	var out []string
	for _, c := range changes {
		if c.Status == 'A' || c.Status == 'C' {
			continue
		}
		path := c.New
		if parentSide {
			path = c.Old
		}
		if path == "" {
			continue
		}
		if _, ok := m.Lang(path); ok {
			out = append(out, path)
		}
	}
	return out
}

func (e *evaluator) summarize() {
	tasks := e.rep.Tasks
	cut := int(float64(len(tasks)) * tuneShare)
	for i := range tasks {
		tasks[i].Split = "tune"
		if i >= cut {
			tasks[i].Split = "holdout"
		}
	}
	e.rep.All, e.rep.Tune, e.rep.Holdout = summary(tasks), summary(tasks[:cut]), summary(tasks[cut:])
	for i := range e.rep.Variants {
		v := &e.rep.Variants[i]
		v.All, v.Tune, v.Holdout = summary(v.tasks), summary(v.tasks[:cut]), summary(v.tasks[cut:])
	}
	if e.opt.Savings {
		e.rep.Savings = summarizeSavings(tasks)
		e.rep.Caveats = append(e.rep.Caveats,
			"savings simulate agents: A2 approximates a grep-then-read-ranges agent and B (perfect search) is the conservative comparison; tokens are chars/4, discovery and reading only, no reasoning or edits")
	}
	if e.rep.Mode == "head" {
		e.rep.Caveats = append(e.rep.Caveats,
			"head mode: the index is at HEAD and already contains every evaluated change, so these numbers are optimistic; use --checkout-parent for honest ones")
	}
	e.rep.Caveats = append(e.rep.Caveats,
		"queries are commit subjects, which are terser than prompts and sometimes name the files outright",
		"latencies are in-process; the prompt hook adds process start-up")
}

func summary(tasks []Task) Summary {
	s := Summary{Tasks: len(tasks)}
	if len(tasks) == 0 {
		return s
	}
	var r5, r10, rr, hh, qh, ht, qt, hms, qms []float64
	for _, t := range tasks {
		r5, r10, rr = append(r5, t.Recall5), append(r10, t.Recall10), append(rr, t.RR)
		hh, qh = append(hh, b2f(t.HookHit)), append(qh, b2f(t.QueryHit))
		ht, qt = append(ht, float64(t.HookTokens)), append(qt, float64(t.QueryTokens))
		hms, qms = append(hms, t.HookMS), append(qms, t.QueryMS)
	}
	s.Recall5, s.Recall10, s.MRR = round3(mean(r5)), round3(mean(r10)), round3(mean(rr))
	s.HookHit, s.QueryHit = round3(mean(hh)), round3(mean(qh))
	s.HookTokens, s.QueryTokens = round3(mean(ht)), round3(mean(qt))
	s.HookP50MS, s.HookP95MS = round3(percentile(hms, 50)), round3(percentile(hms, 95))
	s.QueryP50MS, s.QueryP95MS = round3(percentile(qms, 50)), round3(percentile(qms, 95))
	return s
}

func pathSet(ctx context.Context, st *store.Store) (map[string]bool, error) {
	paths, err := st.Paths(ctx)
	if err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(paths))
	for _, p := range paths {
		set[p] = true
	}
	return set, nil
}

func filePaths(fs []retrieve.File) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.Path
	}
	return out
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func short(sha string) string { return sha[:min(len(sha), 9)] }
