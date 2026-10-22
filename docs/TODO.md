# codegraph: roadmap and TODO

The single place to resume work from. A new conversation should read, in order:
1. this file;
2. `docs/DECISIONS.md` (why things are the way they are);
3. `docs/TESTING.md` (how to check it by hand).

Update the checkboxes and the **Status** line at the end of every session.

**Status (2026-10-22):** Phases 1–4 done. Phase 4 (MCP server, hooks, `init`) awaits the user's review, including one interactive check: approve the server in `claude` (TESTING §7). **Next: Phase 5**, the eval harness, constant tuning and the token-savings estimate. Nothing is committed yet; the repo has no commits.

---

## 0. Project brief (from the original spec, still binding)

**Goal.** A Go CLI and MCP server that indexes a codebase into a SQLite code
graph. Claude Code then asks it for the relevant files and line ranges for a
prompt, instead of grepping and reading whole files.

**Hard requirements**
- Languages: TS/JS, Python and Go first. Adding a language must be easy.
- One static binary, fully local, no external services.
- Large repos (1M+ LOC): under 100 MB RAM at query time; query latency
  under 200 ms; the prompt hook under 300 ms (or it prints nothing).
- **Never store source code in the DB**: only IDs, paths, line ranges,
  signatures and hashes. Code is read from disk when needed.

**Stack.** Do not substitute without asking the user.
- Go (latest stable; currently 1.26 because the SQLite driver requires it)
  with `cobra`.
- `github.com/tree-sitter/go-tree-sitter` plus the official grammars (Go,
  Python, JavaScript, TypeScript/TSX).
- SQLite via `github.com/ncruces/go-sqlite3`, with FTS5 (`ext/fts5`).
- The official MCP Go SDK `github.com/modelcontextprotocol/go-sdk`, over stdio.
- An `errgroup` worker pool.
- `git` is invoked by shelling out.
- Extra dependencies were approved after justification: `gopkg.in/yaml.v3`
  and `github.com/bmatcuk/doublestar/v4`.
- No embeddings yet. The retrieval interface must allow a vector source
  later.

**Working rules** (the user's)
- Plan first and get approval before each phase. **Stop after each phase**
  and show results.
- Keep dependencies minimal; justify any new one.
- Every phase needs `go test ./...` and `go vet` passing (`make test`).
- Record design decisions in `docs/DECISIONS.md` (next free number:
  **D25**).
- Commit or push only when the user asks.

**User-added requirements**
- **Multi-project isolation.** One DB per project at
  `<root>/.codegraph/index.db`, with no shared state. Path confinement;
  nested projects are skipped.
- **Per-project context removal**:
  - `clean` deletes the index;
  - `disable`/`enable` pause injection;
  - `forget`/`unforget <glob>` keep paths out, using a local list.

---

## 1. Environment notes (read before building)

- The machine is Apple Silicon, but `/usr/local/go` is an **x86_64 Go**
  running under Rosetta. cgo (tree-sitter) fails with plain `go build`.
- **Use `make build` / `make test`.** The Makefile detects this and builds
  native arm64 with an explicit clang and SDK. To do it by hand:
  ```bash
  export GOARCH=arm64 CGO_ENABLED=1 CC="/Library/Developer/CommandLineTools/usr/bin/clang -isysroot /Library/Developer/CommandLineTools/SDKs/MacOSX.sdk"
  ```
- The permanent fix (user action, recommended) is to install the
  darwin-arm64 Go from go.dev/dl.
- Benchmark repos: shallow clones of `prometheus/prometheus` and
  `django/django`. They lived in a session scratchpad, which may be gone, so
  re-clone with `git clone --depth 1` into a temp dir. The eval in Phase 5
  needs history, so it needs `--deepen`.
- Module path: `github.com/gayanmadurapperuma/codegraph`. This is a
  **placeholder**; confirm with the user before the release (Phase 6).

---

## 2. Code map (as of Phase 4)

```
cmd/codegraph/          main.go (root, --root flag), index.go (runIndex), stats.go, query.go,
                        serve.go (version), hook.go (prompt/edited, CODEGRAPH_DEBUG), init.go,
                        context.go (clean/disable/enable/forget/unforget), main_test.go
internal/config/        config.go (.codegraph.yaml, defaults), project.go (root finding,
                        Confine, disabled marker, forget list, Clean, EnsureGitignore)
internal/parse/         extract.go (generic tree-sitter query walker), lang.go (registry,
                        IsTestPath), golang.go / python.go / ecma.go (hooks),
                        ident.go (Split/SplitText), queries/*.scm, testdata/*.golden
internal/store/         schema.sql (v1), schema_v2.sql (resolver bookkeeping), store.go
                        (Open, migrations, PRAGMAs), write.go (Batch: stable-ID replace +
                        dirty marking), read.go (Stats, Paths, SymbolsInFile), graph.go
                        (LookupSymbols, Neighbors, FileImpact)
internal/index/         walker.go (git ls-files / fs walk, Matcher, markers), indexer.go
                        (pipeline, Forget), hash.go (sha256, generated detection)
internal/resolve/       resolve.go (Run, dirty expansion), resolver.go (resolveName rules,
                        weights), symtab.go, golang.go / python.go / ecma.go (import
                        resolution), params.go (typed params), tests.go, pagerank.go,
                        jsonc.go, testdata/*.edges
internal/retrieve/      retrieve.go (FindRelevant, Options, Result), tokenize.go (Query,
                        stopwords, test intent), source.go (Source, Fuse/RRF), fts.go
                        (MatchExpr, bm25 seeds, exact names), expand.go (BFS, edge
                        weights, hubs), rank.go (scoring, test factor), pack.go (budget,
                        grouping, Render), testdata/*.query (goldens)
internal/mcpserver/     server.go (5 tools, lazy read-only store, disabled notice)
internal/hook/          hook.go (Prompt → additionalContext JSON, Edited → scoped index)
internal/setup/         setup.go (MCP entry, hooks, CLAUDE.md snippet; Plan/Apply merge;
                        Examples → examples/)
internal/testfix/       test-only: copy + index fixtures
examples/               .mcp.json, settings.json, CLAUDE.snippet.md (generated)
testdata/fixtures/      go / python / ts / js mini projects (resolvable)
docs/                   DECISIONS.md (D1–D24), TESTING.md, TODO.md
Makefile                build / test / golden (+examples) / bench REPO=
```

---

## 3. Phases

### Phase 1: Scaffold and indexer ✅ DONE
- [x] Config (`.codegraph.yaml`): include/exclude globs, languages, max
  file size, budgets, retrieve settings, `include_nested`, `db_path`
- [x] Schema v1: `files`, `symbols`, `edges` (WITHOUT ROWID with
  (src,type,dst) PK and a (dst,type,src) index), `refs`, contentless FTS5
  `symbols_fts`, `meta`; migrations via `user_version`
- [x] Walker: `git ls-files -co --exclude-standard`, or a filesystem walk
  - default excludes and generated/minified detection;
  - symlinks confined to the root;
  - nested projects skipped.
- [x] Worker pool: one parser per worker, trees and parsers closed, one
  writer committing 500 files per transaction, WAL, mmap,
  `PRAGMA optimize`
- [x] Extraction for Go, Python, JS, TS and TSX
  - symbols: kinds, qualified names, signatures, exported flag, tests;
  - imports, calls and extends/implements stored as refs.
- [x] Incremental: size+mtime check first, then content hash; deleted
  files removed
- [x] CLI: `index [--full] [paths...]`, `stats [--json]`
- [x] Isolation and context removal: `clean`, `disable`/`enable`,
  `forget`/`unforget`; `--root`; path confinement; tests
- [x] Tests: goldens per language, store, incremental, walker, isolation, CLI

### Phase 2: Resolution, call edges, PageRank ✅ DONE
- [x] Import resolution:
  - Go: go.mod modules;
  - Python: source roots, relative imports, re-exports;
  - TS/JS: relative probing, tsconfig `paths`/`baseUrl` with `extends`,
    workspace packages, require/import().
- [x] Edges: CALLS, IMPORTS, EXTENDS, IMPLEMENTS and TESTS, each with a
  confidence weight (D12)
- [x] Typed-parameter resolution (Go, Python hints, TS annotations); no
  guessing through external imports
- [x] Incremental resolution with stable symbol IDs and dirty marking
  (D11); a scoped resolve is proven equal to a full one by
  `TestScopedEqualsFull`, and each rule is mutation-checked
- [x] PageRank over weighted edges, recomputed on whole-project runs (D15)
- [x] `stats`: resolution rate per ref type, top hubs, top PageRank
- [x] Measured results:
  - prometheus: resolve 0.38 s;
  - django: resolve 0.70 s;
  - single-file body edit: 50–110 ms;
  - CALLS precision by weight: 1.0 → 20/20, 0.5 → 7/10, 0.25 → ~6/10,
    0.3 → 6/8.
- [x] `Makefile`, `docs/TESTING.md`

### Phase 3: Retrieval and CLI query ✅ DONE (awaiting review)
- [x] `internal/retrieve`: tokenize, `Source` + RRF fusion, FTS seeds
  (injection-safe MATCH, bm25 10/5/2/1, exact-name boost and lookup),
  bounded bidirectional expansion with hubs, ranking, budget packing.
  `FindRelevant(ctx, st, cfg, query, Options{Budget, MaxFiles})` honours
  ctx and returns per-stage timings (D16–D19).
- [x] `codegraph query "<text>" [--budget N] [--json] [--why]`. A missing
  index gives a clear error; a disabled project answers with a stderr note.
- [x] Tests: tokenizer, FTS injection against a real FTS table,
  expansion (depth, hubs, best path, max_per_hop), fusion, packing
  (budget, ≥1 line, grouping, indent, cutoff), fixture expectations,
  query goldens (`make golden`), limits and cancellation, CLI, and
  `BenchmarkFindRelevant` (`CODEGRAPH_BENCH_ROOT` for real repos).
- [x] Parser fix: comment-only lines dropped from signatures (D20).
- [x] Measured, 20 commit subjects per repo (details in DECISIONS):
  - django: p50 16 ms, p95 38 ms end to end; 23 MB peak RSS; hit@5 65%;
    about 575 tokens;
  - prometheus: p50 21 ms, p95 25 ms; 19 MB; hit@5 75%; about 790
    tokens;
  - in-process: 5–6 ms.
- [x] Deviations from the plan, all recorded: PageRank is a multiplier,
  not additive; test words are intent, not terms; test code ×1.5 when
  asked for; 25% relative cutoff; file score caps the rest at 3.
- [x] DECISIONS D16–D20; TESTING.md §6.
- The token-savings estimate moved to Phase 5 (the user's call, 2026-10-22).

### Phase 4: MCP server and Claude Code hooks ✅ DONE (awaiting review)
- [x] Hook schema checked against the current Claude Code docs (D21).
- [x] `codegraph serve`: official go-sdk v1.8.0 over stdio. Tools:
  `find_relevant`, `get_symbol`, `callers_of`, `callees_of`, `impact_of`,
  each with text plus structured content. The read-only store opens lazily
  (a missing index is retried); every call checks disabled; `impact_of`
  confines paths (D22).
- [x] `codegraph hook prompt`: root from the payload `cwd`, silent no-ops,
  a 250 ms deadline, a JSON `additionalContext` block. End to end p95:
  40 ms on django, 47 ms on prometheus.
- [x] `codegraph hook edited`: confined paths → scoped re-index, about
  30 ms; always exits 0; `CODEGRAPH_DEBUG` logging.
- [x] `codegraph init [--dry-run] [--no-index]`: merges `.mcp.json`,
  `.claude/settings.json` and the CLAUDE.md snippet; idempotent (D23).
  `examples/` is generated from the same source.
- [x] Tests: MCP round-trip over in-memory transports (every tool,
  ambiguity, outside root, not indexed, missing index then indexed,
  disabled); hooks (output, subdirectory cwd, slash, empty, no matches,
  foreign cwd, no cwd, malformed, deadline, disabled, no index; edited
  indexes, foreign path ignored, bad payloads); setup merge/idempotence/
  invalid JSON/examples in sync; store graph queries; CLI init + hooks.
- [x] Verified with Claude Code: `claude mcp list` sees the server from
  `.mcp.json` ("Pending approval").
- [ ] **User check**: approve the server in an interactive `claude`
  session and confirm the hook block and tools (TESTING §7).

### Phase 5: Eval harness
- [ ] `codegraph eval [--commits N] [--json]` in `internal/eval/`
  - Last N non-merge commits via `git log --no-merges --format=%H%x00%s`.
    Skip commits touching no indexed files, or more than 20 files.
  - Query = message subject (optionally the body). Ground truth = changed
    files that are indexed (`git show --name-only`), with renames and
    deletes handled.
  - Metrics: recall@5, recall@10 and MRR at file level; also mean tokens
    returned and latency.
  - The leakage caveat goes in the output: the index is HEAD, so this is
    optimistic. An optional `--checkout-parent` mode re-indexes a temp
    worktree at each parent; it is slow but honest.
- [ ] Run it on this repo, django and prometheus; also on one TS repo
  (suggest one to the user, e.g. a mid-size OSS TS project).
- [ ] Use the results to tune the Phase 3 constants (seed weights, edge
  weights, hop decay, hub threshold). Record before and after in DECISIONS.
- [ ] **Token-savings estimate** (deferred from Phase 3), run on the
  tuned ranking:
  - Data: about 100 recent non-merge commits per repo (needs
    `git fetch --deepen=400`). Query = commit subject; ground truth =
    changed files and hunks; content taken at that commit; tokens = chars/4.
  - Scenarios:
    - **A. Typical agent today**: `git grep` keywords, then read whole files
      in hit order until every changed file is read (cap 15).
    - **B. Perfect search**: read only the changed files, in full.
    - **C. codegraph best case**: the 1,000-token block plus only the
      enclosing symbol ranges ±10 lines.
    - **D. codegraph realistic**: the real `FindRelevant`; a hit in the
      top 5 files costs C, a miss costs A + 1,000.
  - Report: savings A→C and A→D (% and tokens per task); the hit rate; the
    share of tasks saving more than 50% and the share costing more; 5
    worked examples; caveats (simulated agent, chars/4, discovery and
    reading tokens only). Optionally save as `docs/BENCHMARKS.md`.
- [ ] **Stop and report** the numbers.

### Phase 6: Release
- [ ] Confirm the module path and repo name with the user.
- [ ] goreleaser config. **cgo is required** (tree-sitter), so either:
  - (preferred) a GitHub Actions matrix on native runners (ubuntu x64/arm64,
    macos arm64/x64, windows x64/arm64), each running
    `goreleaser build --single-target`, with a final job assembling the
    release. This avoids goreleaser Pro's split/merge; or
  - goreleaser-cross / `zig cc`. Decide, and record it in DECISIONS.
- [ ] CI: `go vet`, `go test ./...` and the golden checks on PRs; build
  matrix on tags.
- [ ] README:
  - what it is and why (token savings);
  - install (release binaries, `go install` with cgo notes);
  - setup (index, `.mcp.json`, hooks, CLAUDE.md snippet);
  - commands;
  - config reference;
  - per-project controls;
  - benchmarks (Phase 2/3/5 numbers);
  - privacy (no source stored, local only);
  - limitations.
- [ ] Final `make test`; **stop and report**.

### Later (not now; listed in the spec)
- [ ] LSP-based precise resolution (gopls, pyright, tsserver) as an
  optional edge source
- [ ] Framework edge plugins: AWS CDK, Express routes, Django URLs/models,
  Spring
- [ ] Optional embeddings via Ollama and sqlite-vec, as a second `Source`
  fused with FTS
- [ ] More languages: one `.scm`, one hooks file and one registry line each
  (see D5)

---

## 4. Known issues and tech debt
- Adding or changing an **import** in a widely imported module re-resolves
  all its importers (~0.4 s for `django/utils/text.py`). It's correct but
  slow. An idea: only re-resolve importers whose refs could resolve through
  the changed re-exports.
- Peak RSS **at index time** is 130–160 MB on django/prometheus, mostly the
  full-resolve symbol table. Query time is unaffected: 19–23 MB peak,
  measured in Phase 3.
- Not modelled: Python/TS **class fields** (`self.repo: Repo`, TS
  constructor parameter properties). So `self.repo.find()` is guessed
  (0.5/0.25), not exact. Go struct field types are also not used.
- No REFERENCES edges: type usages in annotations or variables aren't
  captured.
- Go build-tagged duplicate files (e.g. `labels_slicelabels.go`,
  `labels_stringlabels.go`) produce duplicate symbols; ranking may show
  either.
- The `.codegraph/` WAL/SHM files are created 0644. The 0700 directory is
  the real access boundary (D8).
- Global (0.3) guesses between sibling JS apps in one repo can cross
  app boundaries (seen in prometheus `web/ui/*`). A possible fix is
  restricting by nearest `package.json`.

- Retrieval (Phase 3), for Phase 5 tuning:
  - The FTS path column holds each file's whole split path, so a word
    such as "user" matches every symbol in `user.go` (weight 1 of 10).
    That's useful for file-level hits but adds noise on small files.
  - bm25 terms are OR'ed with no document-frequency cut. Very common words
    (e.g. "add", "get") rely on bm25's IDF. An `fts5vocab` table (schema
    v3) would allow dropping terms that match most of the corpus.
  - Commit-subject recall is optimistic (index at HEAD). Measure honestly
    with the Phase 5 `--checkout-parent` mode.
  - Existing indexes keep pre-D20 signatures until files are re-parsed;
    `index --full` refreshes them.

- Hooks and MCP (Phase 4):
  - `codegraph` must be on PATH for the generated hook and server
    commands; Phase 6 install docs must say so (or init could write an
    absolute path).
  - Whether `MultiEdit`/`NotebookEdit` still exist was unverified in the
    docs; they stay in the matcher (harmless).
  - `init` rewrites merged JSON with sorted keys.

## 5. Open questions for the user
- The final module path or repo URL (before Phase 6).
- When to make the first commit. Nothing is committed yet; ask before
  committing, and branch off the default branch first.

## 6. How to resume in a new conversation
1. Tell Claude: "Read `docs/TODO.md` and continue with the next phase."
2. Claude should then:
   - run `make test` to confirm a green baseline;
   - plan the phase and get approval;
   - implement, test, update DECISIONS/TESTING/TODO;
   - stop and report.
