# Design decisions

Each entry: the decision, and why. Newest phases append at the end.

## Phase 1: scaffold + indexer

### D1. Go 1.26 toolchain
`github.com/ncruces/go-sqlite3` v0.35 requires Go ≥ 1.26, so `go.mod` declares
`go 1.26.0`. Older local toolchains auto-download it (`GOTOOLCHAIN=auto`).

### D2. SQLite driver: ncruces/go-sqlite3 (wasm2go), FTS5 registered per connection
Current ncruces releases translate SQLite's Wasm build to plain Go (wasm2go).
There is no wazero runtime and no JIT compile at startup. Measured process
startup is about 10 ms, which leaves the 300 ms hook budget almost entirely for the
query. The plan expected a wazero compilation cache to be needed; it isn't.
FTS5 is a separate module (`ext/fts5`) registered through `driver.Open`'s
connection hook. SQLite version 3.53 supports `contentless_delete=1`.

### D3. Dependencies beyond the spec
- `gopkg.in/yaml.v3`: parses `.uxor.yaml`.
- `github.com/bmatcuk/doublestar/v4`: `**` globs for include/exclude/forget.
  It is tiny and has no dependencies of its own.
- No gitignore library: in git repos we run `git ls-files -co --exclude-standard -z`,
  so git applies its own ignore rules exactly (nested `.gitignore` files,
  `info/exclude`, global excludes). Outside git we walk the filesystem and apply
  only the default excludes.

### D4. Schema
- Every file has a `kind=module` symbol, so all edges are symbol→symbol.
  IMPORTS is module→module, DEFINES is parent→child (module→class→method).
- `refs` stores every unresolved reference by name (calls, imports,
  extends/implements) and is the durable source of truth. Resolved edges are
  derived from refs (Phase 2). When a file changes, its own rows are replaced
  and edges pointing into it are dropped. The resolver rebuilds those edges
  from the other files' refs, which never changed.
- `refs.member` was added to the planned schema to hold the imported member
  of `from x import y` / `import { y as z } from "x"`. That makes a ref
  (module `x`, member `y`, local alias `z`) without encoding tricks.
- `symbols_fts` is **contentless** (`content=''`, `contentless_delete=1`).
  Its columns hold identifier-split tokens (`getUserById get user by id`),
  never source text. Signatures and qualified names are split word by word.
- Enum columns are small integers (`parse.Kind`, `parse.EdgeType`). They are
  append-only so existing indexes remain valid.
- Migrations are an ordered list keyed by `PRAGMA user_version`. A newer
  index than the binary is refused with a hint to run `uxor clean`.

### D5. Extraction: shared query conventions + small per-language hooks
Each language has one embedded `.scm` query using shared capture names
(`@def.<kind>`, `@name`, `@body`, `@call`, `@call.name`, `@call.qualifier`,
`@import`, `@extends`, `@implements`, `@export.name`). A generic walker then:
1. sorts definitions and assigns **containment parents**. Qualified names and
   function→method promotion come from nesting, not per-language code;
2. attributes each call to the innermost enclosing symbol with a sweep,
   O(n log n), and dedups refs per (owner, name, qualifier);
3. delegates quirks to a hook: Go receivers, Python `__all__`, TS/JS
   `export`/accessibility, test detection, import parsing, and builtin call
   filtering.

Adding a language means one `.scm` file, one hooks file, and one registry
line. Notes per language:
- **Go**: structs and aliases are `type`; interfaces are `interface`, and their
  `method_elem`s are methods. Qualified names use the package name
  (`store.memRepo.GetUserByID`). The module symbol's qualified name is the
  package directory. `TestXxx`/`BenchmarkXxx`/`FuzzXxx`/`ExampleXxx` in
  `_test.go` are `test`. Embedded fields become EXTENDS refs.
- **Python**: the qualified name is the dotted module path from the root. If
  `__all__` exists it defines what is exported at top level. In test files
  (`test_*.py`, `*_test.py`, `conftest.py`) `test*` functions and `Test*`
  classes are `test`.
- **TS/JS**: arrow and function-expression consts are functions. `require()`
  and `import()` bindings become import refs, not symbols. `describe`/`it`/`test`
  blocks in test files are `test` symbols named after their title, so calls
  inside a test are attributed to that test (for TESTS edges in Phase 2).
  `module.exports`/`exports.x` mark exports. `.d.ts` files are skipped.
- Signatures are the declaration header up to the body, with whitespace
  collapsed, capped at 200 characters.
- Builtins (`len`, `print`, `require`, `expect`, …) are not stored as call refs.
  They could never resolve and only inflate `refs`.

### D6. Indexing pipeline
- File discovery, then a cheap **size+mtime** check against `files`. Only files
  that look changed are read. Those are hashed (sha256, first 16 bytes), and
  re-parsed only if the hash differs. Otherwise just size and mtime are
  updated, so the next run skips them without reading.
- N workers (`errgroup`, default GOMAXPROCS) each own a `parse.Parser`, which
  holds one tree-sitter parser per language plus a query cursor. Every tree is
  closed right after extraction, and parsers are closed when the worker exits.
  Compiled queries are shared, immutable, and live for the process.
- A single writer goroutine applies results in transactions of 500 file
  operations using prepared statements. After a run with changes:
  `PRAGMA optimize`.
- Workers run under a cancellable child context. The writer uses the caller's
  context, because a `database/sql` transaction is rolled back when its
  context ends.
- Skipped: files over `max_file_bytes` (default 1 MiB), generated files (a
  comment line matching `Code generated … DO NOT EDIT` or `@generated` in the
  first 2 KiB), and minified files (average line length over 300 for files
  over 4 KiB). A previously indexed file that becomes skipped is removed.

### D7. Connection settings
WAL, `synchronous=NORMAL`, `mmap_size=256MiB`, `temp_store=MEMORY`,
`cache_size=16MiB`, `busy_timeout=5000`. Writers use `_txlock=immediate`
(`BEGIN IMMEDIATE`) so concurrent writers wait on the busy timeout instead of
failing on lock upgrade. Write handles use one connection. Read-only
handles (`stats`, and later query/hook) open with `mode=ro`.

### D8. Multi-project isolation: one database per project, by construction
- **Root resolution.** Every command resolves exactly one root: the nearest
  `.uxor.yaml` or `.git` above the start directory. Symlinks are
  resolved. The start directory is `--root`, else the current directory.
  Hooks (Phase 4) will start from the `cwd` in the hook payload, never the
  process's working directory.
- **Storage.** All state lives in `<root>/.uxor/`, created `0700`, with
  `index.db` `0600`. The WAL/SHM side files are created by the Go VFS with
  default permissions, so the `0700` directory is the real access boundary.
  Nothing is shared across projects, and there is no global registry.
- **Path confinement.** `Project.Confine` cleans the path and resolves
  symlinks in its longest existing prefix, so paths of deleted or new files
  work too. It returns a root-relative path or `ErrOutsideRoot`. Paths given
  to `index <paths>` (and later hooks/MCP) that fall outside the root are
  ignored, so an edit in another repo never touches this project's index. The
  walker never follows a symlink out of the root.
- **Nested projects.** A subdirectory with its own `.uxor.yaml`,
  `.uxor/` or `.git` (for example a submodule or vendored repo) belongs to
  that project and is skipped. `include_nested: true` opts monorepos into one
  index.
- `.uxor/` is appended to `.gitignore` on the first `index` in a git repo.

### D9. Removing or pausing context for one project
- `uxor clean [-y]` deletes `<root>/.uxor/` after confirmation. It
  refuses any directory that isn't exactly `<root>/.uxor`.
  `.uxor.yaml` is kept.
- `uxor disable` / `enable` toggle the marker `.uxor/disabled`. Hooks
  and MCP tools check it with one `stat` before opening the database
  (enforced in Phase 4). The index itself is kept and still updated by
  `index`.
- `uxor forget <glob>…` deletes matching files from the index in batched
  transactions and adds the globs to `.uxor/forget.txt`. The matcher
  treats that file as excludes, so neither full nor single-file re-indexing
  brings them back. The list is **local by design**: forgetting is often
  personal (scratch dirs, secrets). Team-wide excludes belong in
  `.uxor.yaml`. `unforget` + `index` restores the files.
- Globs are root-relative doublestar patterns. A bare directory (`legacy` or
  `legacy/`) also matches everything below it.

### D10. `stats` hubs before resolution
Phase 1 has only DEFINES edges (fan-in is always 1), so `stats` shows the
most-called *names* from `refs` and labels them as unresolved. Once
non-DEFINES edges exist it switches to resolved symbols ranked by fan-in.

## Phase 2: resolution, call edges, PageRank

### D11. Incremental resolution: edges are owned by their source file
The non-DEFINES edges leaving file X are a pure function of X's refs plus what
other files expose: symbol names, qualified names, kinds, parents, exported
flags, and their import/extends refs. Resolution never reads edges. So only
"dirty" files are re-resolved, and only their outgoing edges are replaced.
`TestScopedEqualsFull` checks after scripted edits that a scoped resolve
leaves exactly the edges a full resolve produces. Each dirty-marking rule
below has a test step that fails when that rule is disabled (mutation-checked).

- **Stable symbol IDs.** In tracked (incremental) batches, `replaceTracked`
  matches old and new symbols by (kind, name, qualified name, parent,
  exported) and keeps their IDs. Edges from other files therefore stay
  valid. A body-only edit dirties just the file itself: about 50–100 ms
  end-to-end on django and prometheus, versus 300–500 ms when every
  dependent was re-resolved.
- **Marking rules** (in the same write transaction, `internal/store/write.go`):
  - removed symbols → files with edges into them;
  - added or removed names → files whose refs mention the name, as call
    name or imported member (`refs_name`, `refs_member` indexes). This
    covers bindings, receiver candidates and global-uniqueness changes;
  - changed import/extends refs → importers (they may resolve through this
    file's re-exports) and direct subclasses (base lookups). The resolver
    extends this two more subclass levels;
  - a changed symbol set in JS/TS → files default-importing the module
    (the default-export guess may change);
  - a changed symbol set in Go → every file of the package
    (package-scope lookups);
  - new files → files with IMPORTS refs ending in the file's module stem
    (dangling imports may now resolve).
- `resolve_dirty.changed` holds bits (`store.ChangedSymbols`,
  `store.ChangedRefs`) so each expansion starts only from files where the
  relevant thing changed.
- **Full re-resolve instead**: when dirty files exceed 25% of all files
  (`resolve.FullFraction`); on `--full`, first runs, or large change sets
  (the indexer then skips dirty tracking entirely); when marker files
  (go.mod, tsconfig*, package.json, pyproject/setup.*) change, detected by a
  content hash; and when `resolve.Version` changes.
- Known cost: adding an import to a module many files import re-resolves
  those importers (~0.4 s for `django/utils/text.py`, 414 importers). This is
  correct but slower than a body edit.

### D12. Resolution order and confidence weights
Each CALLS/EXTENDS/IMPLEMENTS ref goes through `resolveName`
(`internal/resolve/resolver.go`):
1. **Declared parameter type** (`repo store.Repository`, `user: User`,
   `svc: UserLookup`), parsed from the stored signature. It is
   authoritative: the method is found on that type, or nothing is
   resolved. An external type such as `t *testing.T` resolves nothing.
   Weight 1.0.
2. **self/this/cls/super, or a Go receiver variable**: a method of the
   enclosing class, then its bases (depth ≤ 3). Weight 1.0.
3. **Import binding** (`pkg.F`, `ns.f`, an alias, a from-import), with
   re-exports followed up to depth 3. A binding to a known module that
   lacks the member resolves nothing. Weight 1.0.
4. **Unqualified name**: lexical scope (enclosing functions, then the
   module; Go: the package), then bindings, then star/dot imports.
   Weight 1.0.
5. **Unknown receiver** (`obj.m()`): methods named `m` in the file, its Go
   package and the files it imports. One candidate gives 0.5; two or
   three give 0.25 each; more are dropped.
6. **Repo-wide unique name** (Python/JS/TS only, unqualified calls only,
   name ≥ 4 chars). Weight 0.3.

Guards that buy precision:
- **Names bound by external imports** (stdlib, third-party) are never
  guessed. `errors.Is` and `from datetime import date` stay unresolved
  instead of matching a repo symbol.
- **No repo-wide fallback for method calls.** Method names collide with
  stdlib APIs too often (`Lock`, `Run`, `get`). Before this guard,
  prometheus showed `DirLocker.Lock` with 389 bogus callers.
- **Go never uses rule 6.** An unqualified Go name is package-scoped by
  language rules.
- **Rule 5 skips common builtin method names** per language (`commonMethods`)
  and dunders.
- **The parser marks unusable receivers** (`", ".join(x)`, call chains) as
  `?`, so they are treated as unknown receivers rather than unqualified calls.

Measured precision from hand-checked random samples on prometheus and django:
- weight 1.0: 20/20
- weight 0.5: 7/10
- weight 0.25: about 6/10 (counting the right method being among the candidates)
- weight 0.3: 6/8

Phase 3 ranking multiplies by these weights.

### D13. Per-language rules
- **Go.** Import paths map through every `go.mod` in the repo (longest
  module path wins). A package import binds its alias, plus the real package
  name when that differs from the path's last element. IMPORTS edges go to
  each non-test file of the package at weight 1/n. External test packages
  (`foo_test`) are a separate scope. Embedded fields become EXTENDS.
  Composite literals `T{}` count as instantiation calls.
- **Python.** Source roots are the repo root, `src/`, directories with
  pyproject.toml/setup.py/setup.cfg, and `resolve.python_roots`.
  `from pkg import name` prefers the submodule `pkg.name` when one exists.
  Relative imports climb from the file's package. `super().m()` resolves
  through the bases.
- **TS/JS.**
  - Specifiers are probed the way bundler resolution does: extension
    probing, `index.*`, and `./x.js` → `x.ts`.
  - The nearest `tsconfig.json`/`jsconfig.json` is used, with relative
    `extends` followed (JSONC is stripped without a dependency). `paths`
    uses the longest prefix, relative to `baseUrl`, or else to the config
    that defines `paths`.
  - Workspace packages (`package.json` `name`) map to their directory.
    Entries are tried from source/types/module/main, `dist/`→`src/`, and
    `src/index`.
  - Default imports bind a top-level symbol with the same name as the local
    binding, else the module's only exported top-level symbol.

### D14. TESTS edges
- **Symbol-level**: a test symbol → each non-test symbol it calls (weight of
  the call).
- **File-level, by convention**, weight 1.0: `x_test.go`→`x.go`;
  `test_x.py`/`x_test.py`→ the unique closest `x.py`;
  `x.test.ts`/`x.spec.ts`/`__tests__/x.ts`→`x.*`.
- **File-level, by import**: test file → each non-test module it imports,
  at 0.5.

### D15. PageRank
- **Graph**: weighted over IMPORTS, CALLS, EXTENDS and IMPLEMENTS.
  DEFINES and TESTS are excluded, so test volume doesn't inflate rank.
- **Algorithm**: damping 0.85, at most 30 iterations or an L1 change below
  1e-6, with dangling mass spread uniformly. It runs on int32 edge arrays.
- **Scaling**: scores are ×N (mean 1.0) so they're comparable across repos.
  Only changed rows are written.
- **When it runs**: whole-project runs recompute it (~50 ms prometheus,
  ~130 ms django). Single-file runs skip it and set `pagerank_stale`, which
  the next whole-project run clears.
- **Peak memory at index time**: 110–143 MB on these repos, up from about
  100 MB, mostly the full-resolve symbol table. The < 100 MB target applies
  at query time (Phase 3).

## Phase 3: retrieval and `uxor query`

### D16. Tokenization (`internal/retrieve/tokenize.go`)
- Prompt words are split with `parse.Split`, the same splitter used to build
  the FTS columns, so "getUserById" and "get user by id" produce the same
  terms. Terms are lowercased and deduplicated in prompt order; one-character
  terms and bare numbers are dropped; at most 24 are kept.
- **Identifiers** are kept whole as well, for the exact-name boost:
  snake_case, camelCase, acronym runs (`HTTPServer`), each part of a dotted
  path, and capitalized words of 3+ characters (`Render`, `Cart`, `CSRF`).
  The first word of a multi-word prompt doesn't count as capitalized,
  because sentences start with a capital. A whole camelCase identifier is
  also searched lowercased (`formatuser`), because unicode61 indexes it
  whole; snake_case is not, because underscores split it.
- **Stopwords**: English function words plus prompt noise (fix, bug,
  please, function, code, file, implement, …). Words that are often part
  of identifiers (get, set, add, update, delete, create, error) are kept.
  File-extension words (go, py, js, ts, tsx, jsx) are stopwords, because
  the path column holds every file's extension: "go" matched 16k
  prometheus symbols.
- **Test words** (test, tests, testing, spec, specs, unittest), whole or
  as identifier parts (`test_release_memory`), are *intent*, not search
  terms. They set `WantsTests`. Searched, `test*` matched 35k of 50k django
  symbols through the path column, and the seeds stage took 85–250 ms on
  one query instead of about 5 ms.

### D17. Seeds and scoring
- **Seeds** come from a `Source` interface (`Seeds(ctx, q, n)`). FTS is
  the only source today. Several sources are fused by reciprocal rank
  (k = 60, rescaled to best = 1). A single source passes through
  unchanged, keeping its scores. This is where embeddings plug in later.
- **FTS**: every term is a quoted FTS5 string, with `*` for 4+ characters,
  and the terms are OR'ed. Prompt text can therefore never become syntax
  (NEAR, AND, `-`, column filters); `TestFTSInjection` runs hostile input
  through a real FTS table. The ranking is
  `bm25(symbols_fts, 10, 5, 2, 1)` (name > qualified name > signature >
  path), normalized so the best row scores 1. A row whose name equals a
  query identifier gets +0.5. Identifiers are also looked up by
  `symbols.name` directly, so the bm25 cut can't lose an exact hit.
  Module symbols can be seeds (a path match) but are never listed.
- **Final score** = (seed + best expansion path) × (1 + 0.1·ln(1+pagerank)),
  plus 0.05 if any TESTS edge points at the symbol. **This deviates from the
  Phase 3 plan** (`+ 0.1·ln(1+pagerank)`): additive PageRank let hubs such as
  `labels.FromStrings` (prometheus) outrank real matches on rank alone. As
  a multiplier it orders relevant symbols and can't lift unrelated ones.
- **Test code** (kind `test`, `parse.IsTestPath`, plus `tests.py`/`tests_*`
  files and directories named test/tests/testing/`__tests__`/testdata/
  testutil/e2e) is scaled by 0.5, or by 1.5 when the query wants tests.
  *(Phase 5: the 0.5 is now 1.0, and the path bm25 weight is 3; see D28.)*
  This is applied to seeds *before* expansion: otherwise a test that matches
  the prompt gives its full score to every helper it calls, which is how
  `newQueueManagerMetrics` came first for "remote write retry". The broader
  test detection is retrieval-only; resolution (D14) keeps its stricter
  per-language conventions.
- All constants live in `rank.go`, `expand.go` and `pack.go`. Phase 5
  tunes them against the eval harness.

### D18. Expansion and hubs (`internal/retrieve/expand.go`)
- BFS from the seeds in both edge directions, with one batched query per
  direction per hop (`src_id IN (…)` uses the edges PK, `dst_id IN (…)`
  uses `edges_dst`). Depth ≤ `retrieve.depth` (2). Each hop expands the
  `max_per_hop` (50) best frontier nodes.
- Path score = parent path × edge-type weight × edge confidence (D12) ×
  0.5 per hop. Each node keeps only its best path, plus where the path came
  from and the relation. The scores a hop passes on are fixed at the start
  of that hop, so results don't depend on row order.
- Edge-type weights, as (outgoing, incoming) from the frontier:
  CALLS 1.0 / 0.8, EXTENDS and IMPLEMENTS 0.7 / 0.7, DEFINES 0.6 / 0.5,
  TESTS 0.5 / 0.5, IMPORTS 0.3 / 0.3.
- **Hubs**: a frontier node with more than `hub_fanin` (100) incoming
  non-DEFINES edges is scored but not expanded through. All edge types
  count, not just CALLS, because a widely imported module is a hub too. One
  grouped count query per hop.
- `why` names the relation of the listed symbol to the node it was reached
  from: `called-by X` (X calls it), `calls X`, `extends X`/`extended-by X`,
  `implements X`/`implemented-by X`, `defines X` (it is X's parent),
  `defined-in X`, `tests X`/`tested-by X`, `imports X`/`imported-by X`. For
  seeds it is `match` or `match: exact name`. A module X is shown as its
  path.
- Only the top 200 nodes by seed + expansion are ranked in detail (one
  query: symbol, file, DEFINES parent, tested flag).

### D19. Packing format (`internal/retrieve/pack.go`)
- Candidates are taken in score order while the text fits the budget
  (tokens = chars/4, rounded up). A line that doesn't fit is skipped, so a
  shorter one can still fit. The best line is always included, even past
  the budget. `Options.MaxFiles` caps the number of files (for the
  Phase 4 hook's `hook_top`).
- Candidates below **25% of the best score** are dropped: the budget is a
  ceiling, not a target. Without this, weak two-hop neighbours filled
  every 1,000-token answer. With it, answers averaged 575 tokens (django)
  and 790 (prometheus).
- Files are ordered by best symbol + 0.25 × the next three. The plan said
  "the rest", but uncapped, a file with many weak matches (django's
  `tests.py`) outranked one with a single strong match. Symbols are listed
  by line, enclosing ones first.
- Each line is `path:start-end  [kind] signature`, with two spaces of
  indent per listed ancestor (methods under their class). The path is
  repeated on every line so each line can be passed straight to Read. The
  kind word is omitted when the signature already starts with a
  declaration keyword (`func`, `def`, `class`, `type`, `interface`, `var`,
  `const`, `let`, `function`, `enum`), which saves about 2 tokens a line. It
  is always shown for tests.
- An empty signature shows as the bare name. A prompt with nothing searchable
  left returns an empty result, not an error.

### D20. Signatures drop comment-only lines
Python headers included the comment lines between `def …:` and the first
statement. Django's `atomic` got a 150-character signature of prose. Such
lines (`#` or `//` after trimming) are now dropped before whitespace is
collapsed. This is a parser change, so the goldens were rechecked
(unchanged) and `TestSignatureSkipsComments` was added. Existing indexes pick it
up on the next re-parse of a file, or immediately with `index --full`.

### Phase 3 measurements (2026-10-22, Apple Silicon, warm OS cache)
The method: 20 most recent non-merge commits per repo touching 1–20
indexed files; query = subject; ground truth = changed indexed files. The
index is at HEAD, so recall is optimistic (Phase 5 adds a parent-checkout
mode).

| | django (2,982 files) | prometheus (997 files) |
|---|---|---|
| end-to-end p50 / p95 / max | 16 / 38 / 40 ms | 21 / 25 / 83 ms |
| in-process (`BenchmarkFindRelevant`) | 4.9 ms | 6.0 ms |
| peak RSS | 23 MB | 19 MB |
| hit@5 (any changed file in top 5) | 65% | 75% |
| file recall@5 | 42% | 47% |
| mean tokens returned | 575 | 790 |

End-to-end is a full process run (`/usr/bin/time`), excluding the first
run of a freshly built binary (macOS adds about 0.5 s once). The misses are
mostly subjects with no code words ("Release 3.15.1", "Fix
merge issues", "Doc'd Python 3.15 compatibility").

## Phase 4: MCP server, hooks, init

### D21. Hook contract (`internal/hook`, `uxor hook prompt|edited`)
Checked against the Claude Code hooks docs (code.claude.com/docs/en/hooks)
on 2026-10-22.
- **Input**: the hook JSON on stdin. The project comes from the payload's
  `cwd` through `config.Open`, never from the process's working directory
  (D8). Prompt reads `prompt`; edited reads `tool_input.file_path` (absolute
  for Edit/Write) and `notebook_path`.
- **Failure policy**: both always exit 0 with empty stderr. Exit 2 on
  UserPromptSubmit would *block the prompt*, and other non-zero codes
  show a "hook error" notice. Reasons go to stderr only with
  `UXOR_DEBUG` set. Deliberate no-ops (disabled, no index, slash
  command, no matches, foreign path) are "skipped", not errors.
- **Prompt output**: one JSON object,
  `{"hookSpecificOutput":{"hookEventName":"UserPromptSubmit","additionalContext":…}}`.
  Plain stdout also works for this event; JSON was chosen because it's
  explicit, and because PostToolUse ignores plain stdout, so the two hooks
  shouldn't rely on different conventions. The block is a header plus the
  Phase 3 lines, within `budgets.hook_tokens` (500) and `hook_top` (5)
  files: 1.4–1.7k characters on average, 2.1k at most, well under the
  documented 10k cap.
- **Deadline**: `hook_timeout_ms` (250) covers opening the store and
  retrieval. A late result prints nothing. Measured end to end
  (process start included), with 20 commit-subject prompts: django p50 15 ms /
  p95 40 ms; prometheus 18 / 47 ms. The settings `timeout` (5 s) is only
  a backstop.
- **Edited**: confine each path to the root (foreign paths ignored), then
  `index.Run(Paths)` under a 2 s context and busy timeout: a scoped
  resolve, with PageRank marked stale (D15). About 30 ms for a django
  file. Skipped while disabled: the index is still updated by an explicit
  `uxor index`.
- **Matcher**: `Edit|Write|MultiEdit|NotebookEdit`. The current docs list
  only Edit and Write among file-editing tools. The other names are kept
  because a matcher naming an absent tool is harmless, and they cover older
  or newer versions.

### D22. MCP server shape (`internal/mcpserver`, `uxor serve`)
- Official go-sdk v1.8.0 (spec stack). It adds these modules: google/jsonschema-go,
  segmentio/encoding (+asm), yosida95/uritemplate, x/oauth2, x/time.
  The binary grew to about 29 MB. These are the SDK's own imports; no
  alternative fits the "official SDK" requirement.
- The root is fixed at start (`--root`, else the cwd). `.mcp.json` passes
  `--root ${CLAUDE_PROJECT_DIR:-.}`. The read-only store opens **lazily**
  on the first call and stays open (WAL readers see the hook's writes). A
  missing index is a tool error ("run `uxor index`") that the next
  call retries, so starting a session before the first index works.
- Every call stats `.uxor/disabled` first and answers
  `DisabledNotice` as normal text, not an error, so the model falls back to
  normal search. The SDK validates structured output even then, so
  disabled answers carry empty but schema-valid values.
- Tools take typed inputs (schema from struct tags; unknown arguments are
  rejected). Each returns compact **text** for the model, in the Phase 3
  line format, plus **structured content** with the same data:
  - `find_relevant(query, budget_tokens?)` → `retrieve.Result`.
  - `get_symbol(name)`: exact qualified name, else exact name, else
    qualified-name suffix (`memRepo.GetUserByID`, LIKE-escaped). Best
    PageRank first, at most 10. Module symbols are never returned.
  - `callers_of` / `callees_of(symbol, limit?)`: CALLS edges, best weight
    first, default 50, with a total and "… N more". An ambiguous name returns
    the candidates and no guess. Weights below 1 are shown as `(guess w)`.
  - `impact_of(path)`: `Project.Confine` first, so a path outside the root
    is a tool error. Then importers, callers, subtypes and tests from
    *other* files, 30 per relation, with totals. IMPORTS weights are not
    flagged as guesses: Go spreads one import over the package's files
    (1/n, D13), so the weight isn't a confidence.
- Tool errors use `IsError` results (the model sees and can correct
  them), never protocol errors.

### D23. `uxor init`
- It merges into `.mcp.json` (a `uxor` server entry) and
  `.claude/settings.json` (both hooks), appends a marked snippet to
  `CLAUDE.md` (`<!-- uxor:start/end -->`), then runs the first index
  (`--no-index` to skip; `--dry-run` to only print the plan).
- Merge rules:
  - existing keys are kept;
  - an existing `uxor` server entry, or a hook whose command starts
    with `uxor hook prompt|edited`, counts as present and is never
    replaced, so local edits (e.g. an absolute binary path) survive;
  - re-running changes nothing;
  - invalid JSON aborts before anything is written.
  - Known cost: rewritten JSON files get sorted keys (encoding/json).
- `examples/` (`.mcp.json`, `settings.json`, `CLAUDE.snippet.md`) is
  generated from the same Go values. `TestExamples` fails when the files
  are stale; `make golden` regenerates them.
- Claude Code asks the user once to approve a project's `.mcp.json`
  server (`claude mcp list` shows "Pending approval" until then). A repo
  can't pre-approve itself.

### D24. `internal/testfix`
A test-only helper package (copy a fixture, index it, open it read-only),
used by the Phase 4 packages. The older per-package copies in resolve and
retrieve tests were left as they are.

### D25. The eval: commit history as queries, and the leakage fix
- `uxor eval` (`internal/eval`) replays recent non-merge commits:
  - the subject (with `--body`, also the body) is the query;
  - the changed files that are indexed are the answer.
- Commit selection:
  - root commits are skipped;
  - so are commits with no indexable changed files or more than
    `--max-files` (20) of them;
  - added and copied files are never part of the answer, because nothing
    can find a file before it exists;
  - renames count under the parent-side (old) path.
- **Leakage.** An index at HEAD already contains every evaluated change, so
  it scores too well. `--checkout-parent` builds one temporary
  `git worktree` and checks out each commit's parent there.
  - Commits are processed oldest first, so each re-index is incremental.
  - The project's untracked `.uxor.yaml` and forget list are copied
    into the worktree.
  - Measured on django (100 commits): MRR 0.61 at HEAD against 0.56 at the
    parent, and hook hit 70% against 65%. The honest numbers are the ones
    quoted.
- The project root must be the git top level, so git paths and index paths
  agree.
- Cleanup: the worktree is removed on success, error, Ctrl-C and SIGTERM.
  The CLI cancels its context on both signals. An add interrupted by
  cancellation leaves a *locked* worktree entry, which `prune` skips, so
  cleanup unlocks it first. Checked by killing a run during the add and
  again mid-run.
- Metrics, at file level:
  - recall@5/@10 and MRR are computed over the **full ranking**
    (`retrieve.RankFiles`: no budget, no file limit, no relative cutoff);
  - hook hit and query hit use the **real answers** at the hook budget
    (500 tokens, 5 files) and the query budget (1,000);
  - latencies are in-process.

### D26. Tuning without a rebuild: `retrieve.Tuning`, splits, variants
- All ranking constants live in `retrieve.Tuning` (`DefaultTuning()`):
  bm25 column weights, exact boost, prefix length, hop decay, edge weights,
  PageRank weight, test factors, the file-score rest weight and count, and
  the relative cutoff.
  - `Options.Tuning` overrides them; nil means the defaults.
  - They are not part of `.uxor.yaml`, because they are not a user
    setting.
  - Moving the constants into the struct changed no golden file.
- `eval --tuning a.json` loads a partial JSON file over the defaults; an
  `edges` object replaces only the types it names.
  - Repeating `--tuning` evaluates the extra files as **variants** against
    the same per-parent indexes.
  - Indexing dominates a checkout-parent run, so a sweep of 24 variants
    costs about as much as one run.
- Overfitting guard:
  - the oldest two thirds of the evaluated commits are the `tune` split,
    the newest third the `holdout`;
  - changes are chosen on tune-split MRR, averaged over the repos, then
    confirmed on the holdout;
  - a change is adopted only if no repo regresses by more than about 2
    points.

### D27. The token-savings model (scenarios A2, A, B, C, D)
- Tokens are chars/4 of discovery and reading only; reasoning and edits are
  not counted. Content is read from the worktree at the parent, the code
  the agent would have seen. Nothing is written to the DB.
- **B, perfect search:** read the changed files in full.
- **C, uxor best case:** the answer block, plus each hunk's smallest
  enclosing symbol ±10 lines, merged per file.
- **D, uxor realistic**, from the real answer's top 5 files:
  - on a hit (a changed file is there): the block, plus the C ranges of the
    files found, plus a full read of the changed files missed;
  - on a miss: the wasted block plus A2.
  - The first version charged a miss whenever *any* changed file was
    missing. With 2.7 changed files per commit that made 90% of tasks
    "cost more" by construction, so a hit now means any changed file.
- **A2, a typical agent**, which replaced A as the baseline:
  - it greps the query terms (`git grep -n -i -F`, **indexed files only**)
    and sees the listing of up to 100 paths;
  - it then visits files in order of matching lines, reading each matching
    line ±10 lines (merged with the hunks' enclosing ranges in changed
    files), at most 2,000 lines per file, Claude Code's Read default;
  - it stops once every changed file is reached or after 15 files;
  - changed files never reached cost a full read, capped at 2,000 lines.
- **A, the same visits reading whole files**, is kept in the JSON as an
  upper bound only.
  - On django it hit the 15-file cap in 87 of 100 tasks, about 377k tokens
    per task, which inflated every saving against it.
  - Its whole-repository grep also stalled on prometheus, with more than 5
    minutes for one query; hence the indexed-files-only pathspecs.
- A2 is still a simulation. Short, common terms ("ui", "test") match
  thousands of lines and make it read large windows. **B→D is the
  conservative comparison** and is always reported next to A2→D.

### D28. Tuned constants: no test penalty, path weight 3
- **Sweep 1:** 24 one-at-a-time variants on django, prometheus and vite,
  100 commits each, checkout-parent (D25, D26). Only two helped on average
  without costing any repo more than 2 points of tune MRR:
  - `test_penalty` 0.8 (from 0.5): +0.017 tune / +0.021 holdout average,
    and every repo better;
  - bm25 path weight 3 (from 1): +0.009 / +0.007.
  - Harmful: dropping the path column (−0.05, vite −0.09) or the signature
    column (−0.02).
  - No effect on the ranking: the IMPORTS weight (it rarely decides between
    files) and `min_rel_score` (it only trims packing).
- **Sweep 2**, combining the winners and stepping further:

  | variant | Δ tune MRR (avg, worst repo) | Δ holdout MRR (avg, worst) | Δ hook hit |
  |---|---|---|---|
  | test 1.0 + path 3 | **+0.024** (+0.005) | **+0.025** (+0.011) | **+3.7 pts** |
  | test 1.0 | +0.023 (+0.004) | +0.021 (+0.004) | +2.0 |
  | test 0.8 + path 3 | +0.013 (+0.009) | +0.029 (+0.015) | +2.7 |
  | test 0.8 | +0.016 (+0.012) | +0.021 (+0.001) | +1.3 |

- **Adopted: `test_penalty` 1.0 and bm25 weights (10, 5, 2, 3).** Both
  splits improved on every repo, so it isn't overfitting the tune split.
  - Commits often change a test together with its code, so scoring tests
    down cost recall. That is why test code now gets no penalty by default.
  - Kept: the ×1.5 boost when the query asks for tests, and the knob in
    `Tuning`.
- **The D17 case re-checked:** "remote write retry" on prometheus.
  `newQueueManagerMetrics` is no longer first: `storage/remote/client.go`
  and `queue_manager.go` lead. Tests passing their score to the helpers
  they call remains visible in `why` ("called-by remote.TestClient…").
  Here that lands in the right files; it is a known trade-off.
- Golden changes, reviewed:
  - `go.query`: the test ranks above the api handlers, and `NewMemRepo`
    enters through its path;
  - `python.query`: `helpers.py` moves up one place.
- Not tuned: the config values `depth`, `max_per_hop` and `hub_fanin`.
  Variants share one config, so these would need separate runs.

### D29. Progress output: a live line, not an animation library
- `index` and `init` were silent until a final summary: about 3 s on
  django, but plausibly a minute on the 1M-LOC target, which looks like a
  hang.
- `index.Options.Progress` receives `Event{Stage, Done, Total}`:
  "scanning", then "parsing" once per handled file (from the single writer
  goroutine), then "resolving", then "done". Nil means silent, so the edit
  hook, the eval and the tests are unaffected.
- The CLI (`cmd/uxor/progress.go`) draws it only when **both stdout
  and stderr are terminals** (`os.ModeCharDevice`, not `TERM=dumb`):
  - a spinner line is redrawn on stderr every 100 ms;
  - when the run ends it is cleared, and `✓ parsed / resolved / pagerank`
    lines go to stdout.
- Otherwise the plain summary is printed byte for byte as before, because
  scripts, CI logs, tests and TESTING.md quote it.
- No new dependency: terminal detection is the standard library's
  `os.ModeCharDevice` check, not `golang.org/x/term`. A TUI library
  (bubbletea and the like) would add dependencies for a few seconds of
  output.
- `init` ends with a next step, approving the MCP server in `claude`, which
  is the one thing it can't do itself (D23).

## Phase 5.5: fixes from the first real-project test

The first test on a real project (a React/TypeScript monorepo, 2026-10-24)
asked "where is the login function". The answer, `handleSignIn` inside an
`AuthProvider` component, was found by Claude's own grep: uxor's hook
block pointed at the login page but not at the handler, and `find_relevant`
ranked an unrelated `handleDelete` first for "where sign-in is handled".
Each cause below is reproduced by a fixture test that fails without the fix.

### D30. Nested named functions are symbols
- **JS/TS:** `const x = () => …` / `const x = function …` (and `var`)
  were symbols only at top level. Inside a function body, which is where
  React components keep their handlers, they were invisible. They now
  match in any `statement_block`. Class fields holding a function
  (`handleClick = () => {…}`) are methods.
- **Go:** named closures `keep := func(…) {…}` are functions. Only
  short variable declarations are matched; they only occur inside
  functions, and `var x = func…` would collide with the package-variable
  pattern at top level.
- **Python:** nested `def` was already matched anywhere.
- Containment gives the qualified names (`AuthProvider.handleSignIn`,
  `store.ValidNames.keep`). Calls inside the nested function are now
  attributed to it, not to the enclosing one.
- Not symbols: local non-function variables, and anonymous callbacks
  (`onClick={() => …}`). Their calls stay with the enclosing symbol.
- **Resolver:** Go's unqualified lookup was package-scope only, which is
  right for Go except for local closures. It now checks the closures of
  the enclosing functions first. JS/Python already walked enclosing scopes.
  `TestScopedEqualsFull` still holds.
- Measured alone: vite MRR 0.442 → 0.463 (tune and holdout both +0.02),
  hook hit 55% → 57%; django unchanged; prometheus −0.004 MRR, within
  noise.

### D31. Prefix matches stay inside identifier words (schema v3)
- The FTS columns held each identifier whole as well as split
  (`handleDelete handle delete`), and terms of 4+ characters are prefix
  queries. So `"handled"*` matched the whole token `handledelete`:
  prompt words matched across word boundaries of longer identifiers.
- The whole-identifier token is now indexed with a marker,
  `parse.WholeMarker` (`0handleDelete`). A word prefix can't reach it.
  Exact identifier searches use the marked form: multi-word camelCase
  prompt identifiers go into `Query.Whole` and are searched as
  `"0formatuser"`, with no prefix. One-word identifiers are just their
  word, unchanged.
- Kept over a separate column: the four columns and their bm25 weights
  (D28) stay as they are. The cost is that a partial identifier
  (`formatus`) no longer prefix-matches `formatUser`; its words still do.
- **Migration v3** rebuilds every FTS row from `symbols` and `files`,
  without re-parsing. Migrations can now carry Go code, not just SQL. It
  runs on the first write open (`index`, `init`, an edit hook). Read-only
  opens (query, hook, MCP) keep working on a v2 index in the old format
  until then. `TestMigrateV3RebuildsFTS` turns an index back into v2,
  confirms the cross-word match, and checks the rewrite.
- On the fixtures, "save user model" lost `UserService(ModelBase)` and
  `clean`, which had only matched through `userservice`/`modelbase`, and
  now answers with `Base.save` and `User` (38 tokens instead of 52).

### D32. Hyphenated compounds and a small synonym list
- "sign-in" was searched as `sign` alone ("in" is a stopword), which matched
  every `signOut`. A hyphenated compound is now also a **phrase**
  (`"sign in"`: adjacent words, which match `handleSignIn`'s split words
  but not `signOut`) and a **joined word** (`signin`, as a term and as a
  whole identifier). The individual words still count.
- **Synonyms**: three groups that often differ between prompts and code.
  If the prompt mentions any member (multi-word members as consecutive
  words), all members are searched:
  - login / log in / sign in / signin / logon;
  - logout / log out / sign out / signout / logoff;
  - signup / sign up.

  Broad synonyms (delete/remove, get/fetch) are deliberately not included:
  they would add noise to many prompts for a rare gain.
- `TestLoginFindsSignIn`: with `handleSignIn` inside `AuthProvider` (the
  real case), "where is the login function" finds nothing without the
  synonyms and ranks `handleSignIn` best with them (mutation-checked).

### D33. Hook log; init refreshes its CLAUDE.md block
- **Hook log** (`hook_log: true` in `.uxor.yaml`; off by default
  because it stores prompt text): one JSON line per hook run in
  `.uxor/hook.log` (0600). Each line has the time, the event
  (prompt/edited), a prompt excerpt of at most 300 characters, the files,
  tokens, ms, and the result (`injected`, `indexed`, `skipped: …`,
  `error: …`). It is written after the output; write errors are ignored, so
  the log can never fail or block a hook; it rotates to `hook.log.1` past
  1 MiB. It answers "what did uxor inject for that prompt?" without
  replaying the hook.
- **Stronger snippet**: in that test, Claude grepped instead of calling
  `find_relevant` with MCP connected. "Before broad Grep/Glob searches"
  didn't cover a targeted grep, and the model used that gap. The snippet
  now says: for any "where is / how does / what calls X" question, call
  `find_relevant` first, before Grep, Glob or even a targeted grep.
- **Refresh**: the block between the markers belongs to uxor. `init`
  replaces it when it differs ("update: snippet refreshed"), so existing
  projects pick up new wording by re-running `init`. Text outside the
  markers is never touched. A start marker without an end marker is left
  alone. This narrows D23's "never replaced" to the JSON entries.

### D34. Extraction-rule changes re-parse existing indexes
- Found on the real project: after upgrading, `uxor init` reported
  `parsed 0 files (326 unchanged)`. Incremental indexing skips files
  whose size and mtime are unchanged, so new extraction rules (D30) never
  reached an existing index; the nested handlers were still missing.
- `parse.Version` identifies the extraction rules (now "2"). The index
  stores `parser_version` in `meta`. A **whole-project** run (`index`,
  `init`) that finds a different version on a non-empty index re-parses
  every file once (a full run) and says so on stderr. **Path-scoped runs**
  (the edit hook, under a 2 s deadline) never do; the next whole run
  catches up. Bump `parse.Version` whenever the same source would now yield
  different symbols or refs (D20 should have done this too).
- `resolve.Version` → "2" for the Go closure scoping (D30), forcing a
  full re-resolve the same way.
- `TestParserVersionReparses` covers the first run, an unchanged run, a
  path-scoped run with an old version, and the full re-parse.
- **Installing safely**: on macOS, `cp` of a new binary over the installed
  one keeps a stale code signature, and the new binary is killed on start
  (`zsh: killed`, exit 137). `make install` uses install(1), which
  replaces the file, so updates are safe (TESTING §1).

### D35. Phase 5.5 measured: compounds off, whole words also plain, path weight kept
- **Attribution** (variants against the same indexes, D26). The switches
  `whole_prefix`, `compounds` and `synonyms` in `retrieve.Tuning` exist
  for this, and `LoadTuning` now rejects unknown fields: a sweep once ran a
  stale binary that silently ignored them.
  - **Old cross-word prefix matching** (`whole_prefix: true`) is worse
    everywhere, by 0.03 to 0.13 MRR. D31 stays.
  - **Compounds** (D32) cost vite 0.459 → 0.408 MRR: its subjects are full
    of hyphenated words ("pre-transform", "non-blocking"). They're now
    **off by default**. "sign-in" still reaches the login synonyms, because
    "sign in" is a group member.
  - **Synonyms**: no effect on commit subjects, and they fix the real
    "login" case. On.
- **Two D31 bugs found by per-commit diffs** (prometheus):
  - Multi-word values (paths, test titles) were marked only on their first
    word, and snake_case got a marker on half a word. Every column now goes
    through `splitAll`, which splits underscores too (migrations v4, v5).
  - Whole camelCase prompt identifiers were searched only in marked form,
    so "PromQL" stopped matching the directory `promql/`. They're now also
    searched as an exact plain word.
- **Path weight re-tuned** on the new format: 2/3/5/6 are within noise; 3
  is kept.
- **Net, before → after Phase 5.5** (100 commits, checkout-parent), MRR:
  - django 0.613 → 0.614 (holdout 0.582 → 0.606);
  - prometheus 0.678 → 0.663 (its "storage/remote: …" subjects leaned on
    the old path format);
  - vite 0.442 → 0.448 (holdout 0.332 → 0.349);
  - averaged: tune −0.010, holdout +0.008, hook hit +0.3 points.

  The benchmark is about neutral. The gains are in what the benchmark
  can't see: nested handlers, "login" finding `signIn`, no cross-word
  false matches, re-parsing after upgrades.
- **Still open** (seen on the real project): `AuthProvider.handleSignIn`
  isn't in the top 5 files for "where is the login function" (methods named
  exactly `signIn` rank higher), and helpers reached by "called-by" add
  noise to answers. Candidates for the next ranking work.

### D36. Renamed codegraph → uxor
- Why: a mature project with the same name exists
  (github.com/colbymchenry/codegraph). The name was checked on the registries
  directly (crates.io, npm, PyPI, Homebrew, GitHub); `loci` and `hansa` were
  dropped for collisions. "uxor" is Latin for "wife".
- What changed: module `github.com/gayanmadurapperuma/uxor`, `cmd/uxor`,
  `.uxor/`, `.uxor.yaml`, MCP server `uxor`, `uxor hook …`, markers
  `<!-- uxor:start/end -->`, `UXOR_*` variables.
- Migration (`uxor init` / `uxor index`, `config.MigrateLegacy`):
  `.codegraph/` → `.uxor/`, `.codegraph.yaml` → `.uxor.yaml` and the
  `.gitignore` entry move, never overwriting. Init replaces only *our* old
  MCP entry (exact args), hooks (`codegraph hook …`) and CLAUDE.md block (our
  heading), so another tool's `codegraph` setup is untouched. An
  un-migrated project's config is still read. Verified on this repo and on
  openverse.
