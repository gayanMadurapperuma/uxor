// Package index walks a project, parses changed files in parallel and
// writes the results in batched transactions.
package index

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/gayanmadurapperuma/codegraph/internal/config"
	"github.com/gayanmadurapperuma/codegraph/internal/parse"
	"github.com/gayanmadurapperuma/codegraph/internal/resolve"
	"github.com/gayanmadurapperuma/codegraph/internal/store"
)

// BatchSize is the number of file operations per write transaction.
const BatchSize = 500

type Options struct {
	Full    bool     // drop everything and re-index
	Workers int      // parse workers; default GOMAXPROCS
	Paths   []string // restrict to these paths (absolute or root-relative); no removal scan
}

type Result struct {
	Scanned   int // candidate files considered
	Parsed    int // files (re)parsed and written
	Unchanged int // skipped by size+mtime or content hash
	Skipped   int // too large or generated
	Removed   int // deleted from the index
	Failed    int // read or parse errors
	Errors    []string
	Elapsed   time.Duration
	Resolve   *resolve.Result
}

func (r *Result) Changed() bool { return r.Parsed+r.Removed > 0 }

type job struct {
	entry   Entry
	state   store.FileState
	existed bool
}

type outcome int

const (
	outParsed outcome = iota
	outTouched
	outSkipped
	outFailed
)

type result struct {
	kind   outcome
	job    job
	meta   store.FileMeta
	parsed *parse.FileResult
	err    error
}

// Run indexes the project incrementally (or fully with opt.Full).
func Run(ctx context.Context, p *config.Project, st *store.Store, opt Options) (*Result, error) {
	start := time.Now()
	res := &Result{}
	forget, err := p.ForgetGlobs()
	if err != nil {
		return nil, err
	}
	m := NewMatcher(p.Config, forget)

	if opt.Full {
		if err := st.Reset(ctx); err != nil {
			return nil, err
		}
	}
	states, err := st.FileStates(ctx)
	if err != nil {
		return nil, err
	}

	var entries []Entry
	var removed, markers []string
	if len(opt.Paths) > 0 {
		for _, raw := range opt.Paths {
			rel, err := p.Confine(raw)
			if err != nil {
				continue // outside this project: never touch its index
			}
			if e, ok := StatEntry(p, m, rel); ok {
				entries = append(entries, e)
			} else if _, ok := states[rel]; ok {
				removed = append(removed, rel) // deleted or now excluded
			}
		}
	} else {
		listing, err := Walk(ctx, p, m)
		if err != nil {
			return nil, err
		}
		entries, markers = listing.Entries, listing.Markers
		if markers == nil {
			markers = []string{} // a fresh walk found none (nil would reuse the stored list)
		}
		seen := make(map[string]bool, len(entries))
		for _, e := range entries {
			seen[e.Path] = true
		}
		for path := range states {
			if !seen[path] {
				removed = append(removed, path)
			}
		}
	}
	res.Scanned = len(entries)

	var jobs []job
	for _, e := range entries {
		s, existed := states[e.Path]
		if existed && s.Size == e.Size && s.MTime == e.MTime {
			res.Unchanged++
			continue
		}
		jobs = append(jobs, job{entry: e, state: s, existed: existed})
	}

	// Small changes get dirty tracking and a scoped resolve; large ones (or
	// first runs) skip the tracking overhead and resolve everything.
	changes := len(jobs) + len(removed)
	track := !opt.Full && len(states) > 0 && float64(changes) <= resolve.FullFraction*float64(len(states))
	w := &writer{st: st, res: res, track: track}
	if err := parseAll(ctx, p, jobs, opt.Workers, w.handle); err != nil {
		w.abort()
		return nil, err
	}
	for _, path := range removed {
		if err := w.op(ctx, func(b *store.Batch) error { return b.DeleteFile(ctx, path) }); err != nil {
			w.abort()
			return nil, err
		}
		res.Removed++
	}
	if err := w.commit(); err != nil {
		return nil, err
	}

	if res.Resolve, err = resolve.Run(ctx, p, st, resolve.Options{
		Full:     !track && (res.Changed() || opt.Full || len(states) == 0),
		PageRank: len(opt.Paths) == 0,
		Markers:  markers,
	}); err != nil {
		return nil, fmt.Errorf("resolve: %w", err)
	}

	if res.Changed() || opt.Full {
		if err := st.SetMeta(ctx, "last_indexed", time.Now().UTC().Format(time.RFC3339)); err != nil {
			return nil, err
		}
	}
	if res.Changed() || opt.Full || res.Resolve.Files > 0 {
		if err := st.Optimize(ctx); err != nil {
			return nil, err
		}
	}
	res.Elapsed = time.Since(start)
	return res, nil
}

// parseAll fans jobs out to workers and feeds results to handle on the
// calling goroutine, so all writes happen on one goroutine.
func parseAll(ctx context.Context, p *config.Project, jobs []job, workers int, handle func(context.Context, result) error) error {
	if len(jobs) == 0 {
		return nil
	}
	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}
	workers = min(workers, len(jobs))
	// Workers run under a cancellable child context; handle gets the
	// caller's ctx because a write transaction must outlive the workers.
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	g, gctx := errgroup.WithContext(wctx)
	in := make(chan job)
	out := make(chan result, workers*2)

	g.Go(func() error {
		defer close(in)
		for _, j := range jobs {
			select {
			case in <- j:
			case <-gctx.Done():
				return gctx.Err()
			}
		}
		return nil
	})
	var parsers errgroup.Group
	for range workers {
		parsers.Go(func() error {
			ps := parse.NewParser()
			defer ps.Close()
			for j := range in {
				r := parseOne(p, ps, j)
				select {
				case out <- r:
				case <-gctx.Done():
					return gctx.Err()
				}
			}
			return nil
		})
	}
	g.Go(func() error {
		err := parsers.Wait()
		close(out)
		return err
	})

	var handleErr error
	for r := range out {
		if handleErr != nil {
			continue // drain so workers can exit
		}
		if handleErr = handle(ctx, r); handleErr != nil {
			cancel() // stop the producer and workers
		}
	}
	if err := g.Wait(); err != nil && handleErr == nil {
		return err
	}
	return handleErr
}

func parseOne(p *config.Project, ps *parse.Parser, j job) result {
	r := result{job: j}
	src, err := os.ReadFile(filepath.Join(p.Root, filepath.FromSlash(j.entry.Path)))
	if err != nil {
		r.kind, r.err = outFailed, err
		return r
	}
	if int64(len(src)) > p.Config.MaxFileBytes || isGenerated(src) {
		r.kind = outSkipped
		return r
	}
	h := contentHash(src)
	if j.existed && bytes.Equal(h, j.state.Hash) {
		r.kind = outTouched
		return r
	}
	fr, err := ps.Parse(j.entry.Path, src)
	if err != nil {
		r.kind, r.err = outFailed, err
		return r
	}
	r.kind, r.parsed = outParsed, fr
	r.meta = store.FileMeta{Path: j.entry.Path, Lang: fr.Lang, Hash: h,
		Size: int64(len(src)), MTime: j.entry.MTime, LOC: fr.LOC}
	return r
}

// writer applies results in batches of BatchSize operations.
type writer struct {
	st    *store.Store
	res   *Result
	track bool // record dirty files for an incremental resolve
	batch *store.Batch
	n     int
}

func (w *writer) op(ctx context.Context, fn func(*store.Batch) error) error {
	if w.batch == nil {
		b, err := w.st.Begin(ctx, store.BatchOptions{TrackDirty: w.track})
		if err != nil {
			return err
		}
		w.batch = b
	}
	if err := fn(w.batch); err != nil {
		return err
	}
	if w.n++; w.n >= BatchSize {
		return w.commit()
	}
	return nil
}

func (w *writer) commit() error {
	if w.batch == nil {
		return nil
	}
	err := w.batch.Commit()
	w.batch, w.n = nil, 0
	return err
}

func (w *writer) abort() {
	if w.batch != nil {
		w.batch.Rollback()
		w.batch = nil
	}
}

func (w *writer) handle(ctx context.Context, r result) error {
	e := r.job.entry
	switch r.kind {
	case outParsed:
		w.res.Parsed++
		return w.op(ctx, func(b *store.Batch) error { return b.ReplaceFile(ctx, r.meta, r.parsed, r.job.existed) })
	case outTouched:
		w.res.Unchanged++
		return w.op(ctx, func(b *store.Batch) error { return b.TouchFile(ctx, e.Path, e.Size, e.MTime) })
	case outSkipped:
		w.res.Skipped++
		if r.job.existed {
			w.res.Removed++
			return w.op(ctx, func(b *store.Batch) error { return b.DeleteFile(ctx, e.Path) })
		}
	case outFailed:
		w.res.Failed++
		if len(w.res.Errors) < 10 {
			w.res.Errors = append(w.res.Errors, fmt.Sprintf("%s: %v", e.Path, r.err))
		}
	}
	return nil
}

// Forget deletes indexed files matching globs and re-resolves the files
// that referenced them. It returns how many were removed.
func Forget(ctx context.Context, p *config.Project, st *store.Store, globs []string) (int, error) {
	paths, err := st.Paths(ctx)
	if err != nil {
		return 0, err
	}
	w := &writer{st: st, res: &Result{}, track: true}
	n := 0
	for _, path := range paths {
		if !MatchAny(globs, path) {
			continue
		}
		if err := w.op(ctx, func(b *store.Batch) error { return b.DeleteFile(ctx, path) }); err != nil {
			w.abort()
			return 0, err
		}
		n++
	}
	if err := w.commit(); err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, nil
	}
	if _, err := resolve.Run(ctx, p, st, resolve.Options{PageRank: true}); err != nil {
		return 0, fmt.Errorf("resolve: %w", err)
	}
	return n, st.Optimize(ctx)
}
