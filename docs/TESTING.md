# Testing codegraph by hand

Everything here works with what exists today: indexing, resolution,
PageRank, and the project commands. `codegraph query` and the MCP tools come
in Phases 3–4. Until then, inspect the graph with `stats` and `sqlite3`,
which ships with macOS.

## 1. Build and run the test suite

```bash
make test    # go vet + go test ./... (expect every package "ok")
make build   # writes bin/codegraph
```

The Makefile handles an x86_64 Go running on an Apple Silicon Mac by building
native arm64 binaries. Installing the darwin-arm64 Go from go.dev/dl makes
that unnecessary.

To avoid typing the full path below:

```bash
alias codegraph="$PWD/bin/codegraph"
```

## 2. Index a repository

Use any repo with Go, Python, JS or TS:

```bash
cd ~/path/to/repo
codegraph index        # first run: full index + full resolve
codegraph index        # second run: "0 parsed", well under a second
codegraph stats
```

What to look for in `stats`:
- **files / symbols / edges**: per-language file and line counts should
  match the repo.
- **refs resolved**: the share of imports and calls that point *inside* the
  repo. References to stdlib and third-party code stay unresolved by
  design, so 30–60% for calls is normal.
- **top hubs / top PageRank**: these should be names you recognise as
  central to the codebase.

Or run `make bench REPO=~/path/to/repo` for timings and peak memory.

## 3. Incremental behaviour

Edit a file, then re-index only that file. The edit hook will do the same.

| Try this | Expect |
|---|---|
| Change a function body | `resolve (scoped): 1 files`, about 0.1 s |
| Add a new function | still a handful of files |
| Rename a function other files call | callers re-resolved; their edge to the old name is gone (query 4b) |
| Add an import to a widely used module | importers re-resolved; slower, but correct |
| Delete a file, then run `codegraph index` | `1 removed`; edges into it are gone |
| Edit `go.mod` / `tsconfig.json` / `package.json`, then run `codegraph index` | `resolve (full)` |

```bash
codegraph index path/to/edited_file.py
```

## 4. Inspect the graph

```bash
sqlite3 -header -column .codegraph/index.db
```

Edge types: 1 IMPORTS · 2 DEFINES · 3 CALLS · 4 EXTENDS · 5 IMPLEMENTS · 7 TESTS.

Edge weight is confidence:
- **1.0**: exact (scope, import binding, `self`/`this`, declared parameter type);
- **0.5**: the only method with that name in reach;
- **0.25**: one of 2–3 candidates;
- **0.3**: the name is unique across the repo (Python/JS/TS only).

Replace `YourFunction` and the paths below. Many symbols can share a name
(django has four `reverse`s). To pin one, swap `dst.name = 'reverse'` for
`dst.qualified_name = 'django.urls.base.reverse'`, using a qualified name
from query (a).

**a) Find a symbol**
```sql
SELECT s.qualified_name, f.path, s.start_line, s.end_line, s.signature
FROM symbols s JOIN files f ON f.id = s.file_id
WHERE s.name = 'YourFunction';
```

**b) Callers**
```sql
SELECT src.qualified_name AS caller, sf.path, e.weight
FROM edges e
JOIN symbols src ON src.id = e.src_id JOIN files sf ON sf.id = src.file_id
JOIN symbols dst ON dst.id = e.dst_id
WHERE e.type = 3 AND dst.name = 'YourFunction'
ORDER BY e.weight DESC;
```

**c) Callees**
```sql
SELECT dst.qualified_name AS callee, df.path, e.weight
FROM edges e
JOIN symbols src ON src.id = e.src_id
JOIN symbols dst ON dst.id = e.dst_id JOIN files df ON df.id = dst.file_id
WHERE e.type = 3 AND src.name = 'YourFunction'
ORDER BY e.weight DESC;
```

**d) Files that import a file** (what breaks if it changes)
```sql
SELECT DISTINCT sf.path AS importer
FROM edges e
JOIN symbols s ON s.id = e.src_id JOIN files sf ON sf.id = s.file_id
JOIN symbols d ON d.id = e.dst_id JOIN files df ON df.id = d.file_id
WHERE e.type = 1 AND df.path = 'path/to/file.py';
```

**e) Tests that exercise a symbol**
```sql
SELECT t.qualified_name AS test, tf.path, e.weight
FROM edges e
JOIN symbols t ON t.id = e.src_id JOIN files tf ON tf.id = t.file_id
JOIN symbols d ON d.id = e.dst_id
WHERE e.type = 7 AND d.name = 'YourFunction';
```

**f) Subclasses / implementations**
```sql
SELECT src.qualified_name AS subclass, sf.path
FROM edges e
JOIN symbols src ON src.id = e.src_id JOIN files sf ON sf.id = src.file_id
JOIN symbols dst ON dst.id = e.dst_id
WHERE e.type IN (4, 5) AND dst.name = 'YourClass';
```

**g) Most important symbols** (PageRank; mean is 1.0)
```sql
SELECT s.qualified_name, f.path, round(s.pagerank, 1) AS pagerank
FROM symbols s JOIN files f ON f.id = s.file_id
WHERE s.kind != 7 ORDER BY s.pagerank DESC LIMIT 20;
```

**h) What stayed unresolved in a file** (useful when a call edge you
expected is missing)
```sql
SELECT r.line, r.qualifier, r.name, rs.qualified_name AS in_symbol
FROM refs r JOIN files f ON f.id = r.file_id JOIN symbols rs ON rs.id = r.src_id
WHERE f.path = 'path/to/file.py' AND r.type = 3
  AND NOT EXISTS (SELECT 1 FROM edges e JOIN symbols d ON d.id = e.dst_id
                  WHERE e.src_id = r.src_id AND e.type = 3 AND d.name = r.name)
ORDER BY r.line;
```

The database stores no source code, only names, paths, line ranges,
signatures and hashes. `SELECT * FROM symbols LIMIT 5;` shows exactly what
is kept.

## 5. Per-project controls and isolation

```bash
codegraph forget secrets/      # drop matching files from the index and keep them out
codegraph forget --list
codegraph stats                # shows "forgotten: secrets/"
codegraph unforget secrets/ && codegraph index

codegraph disable              # pause context injection (index kept)
codegraph stats                # "state: DISABLED"
codegraph enable

codegraph clean                # asks, then deletes only this repo's .codegraph/
```

To check isolation:
1. Index two repos, A and B.
2. Run `codegraph --root A clean`. B's `.codegraph/index.db` must still
   exist.
3. Run `codegraph --root A index ../B/some_file.go`. It processes nothing,
   because paths outside the project root are ignored.

## 6. Try `codegraph query`

```bash
codegraph query "get user by id"              # packed lines, ≤ 1,000 tokens
codegraph query --why "csrf token rotation"   # + the reason for each line, tokens, ms
codegraph query --budget 300 "remote write retry"
codegraph query --json "formatUser" | jq .     # scores, why, per-stage timings_ms
```

Each line is `path:start-end  [kind] signature`; methods are indented
under their class when both are listed. The kind word is left out when the
signature already says it (`func`, `def`, `class`, …).

Things to check:
- **An exact identifier** (`formatUser`, `get_user_by_id`, `Render`) is
  listed with `why: match: exact name`.
- **Graph neighbours** appear with a reason such as `called-by X`,
  `calls X`, `extends X` or `defined-in X`, where X is the symbol the path
  came from.
- **Tests** rank below the code they test, unless the prompt mentions tests
  (`test`, `tests`, `spec`, or an identifier such as `test_foo`), in which
  case they rank above it.
- **Hostile input** (`codegraph query '"*" - NEAR ( )'`) prints
  `no matches`; it is never an FTS syntax error.
- **Speed**: `--why` prints the total; `--json` shows `timings_ms` per stage
  (`seeds`, `expand`, `rank`, `pack`). The target is under 200 ms end to end.
  On macOS the first run of a freshly built binary takes about 0.5 s
  extra for the OS signature check; run it twice.
- **A disabled project** still answers on the CLI, with a note on stderr.
- **No index**: the error says to run `codegraph index`.

Benchmark on a real repo:
`CODEGRAPH_BENCH_ROOT=/path/to/indexed/repo go test -run x -bench FindRelevant ./internal/retrieve`
(under `make`'s environment on this machine; see §1).

## 7. Set up a project for Claude Code

```bash
codegraph init --dry-run   # shows: .mcp.json / .claude/settings.json / CLAUDE.md  create|update|unchanged
codegraph init             # writes them, then runs the first index
codegraph init             # again: everything "unchanged"
claude mcp list            # codegraph … ⏸ Pending approval
claude                     # approve the project server when asked; then /mcp lists its 5 tools
```

`codegraph` must be on PATH for the hooks and the server; otherwise edit
the `command` values to an absolute path. `init` won't overwrite them.
In a session, check that:
- a prompt such as "where is the user lookup?" shows the
  "Likely relevant locations (codegraph; …)" block (Ctrl-O for the
  transcript, or `claude --debug`);
- after Claude edits a file, `codegraph stats` shows the new symbol and
  "top PageRank (stale; …)";
- `codegraph disable` makes the hook print nothing and the tools answer
  "codegraph is disabled for this project".

## 8. Hooks by hand

```bash
echo '{"cwd":"'$PWD'","prompt":"get user by id"}' | codegraph hook prompt | jq -r .hookSpecificOutput.additionalContext
echo '{"cwd":"'$PWD'","prompt":"/compact"}' | CODEGRAPH_DEBUG=1 codegraph hook prompt   # codegraph hook: skipped: …
echo '{"cwd":"'$PWD'","tool_name":"Edit","tool_input":{"file_path":"'$PWD'/src/repo.ts"}}' | CODEGRAPH_DEBUG=1 codegraph hook edited
echo 'garbage' | codegraph hook prompt; echo "exit=$?"                                   # nothing, exit=0
```
Time the prompt hook with `time (echo … | codegraph hook prompt)`. The
budget is 300 ms; django measured p95 40 ms.

## 9. The MCP server by hand

```bash
( printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}' \
  '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"callers_of","arguments":{"symbol":"UserService.getUserById"}}}' \
  '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"impact_of","arguments":{"path":"../outside"}}}'; sleep 1 ) \
  | codegraph serve | jq -c '.result.content[0].text // .result'
```
Expect callers with `(guess …)` markers, and `outside this project` with
`isError: true`. Alternatively run `npx @modelcontextprotocol/inspector codegraph serve`.

## 10. When something looks wrong

- **A call edge is missing**: run query (h) to see the raw ref.
  - A qualifier of `?` means the receiver was an expression, such as a call
    chain or literal.
  - A qualifier naming a stdlib or third-party import is never guessed into
    the repo (by design).
- **A wrong edge**: check its weight. Anything below 1.0 is a guess; the
  rules are in `docs/DECISIONS.md` D12.
- **A query misses an obvious file**: run it with `--json` and look at
  the seeds' `why`. If nothing says `match`, the prompt words don't occur in
  any name, signature or path; try the identifier. The stopword list is in
  `internal/retrieve/tokenize.go`.
- **No hook block in a session**: run the hook by hand (§8) with the same
  cwd and `CODEGRAPH_DEBUG=1`. "skipped: no index" means `codegraph index`
  hasn't run there; "disabled" means `codegraph enable`.
- **Reset everything**: `codegraph index --full`, or `codegraph clean -y` to
  delete this repo's index.
