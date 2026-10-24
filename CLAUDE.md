# uxor (development)

A Go CLI and MCP server that indexes a codebase into a SQLite code graph for
token-efficient retrieval in Claude Code. Formerly "codegraph" (renamed to
uxor, Latin for "wife", in Phase 5.5; D36).

- **Start every session by reading `docs/TODO.md`.** It holds the roadmap,
  the current status and the next phase. `docs/DECISIONS.md` records why
  things are the way they are (D1…); add new decisions there.
  `docs/TESTING.md` is the manual test guide. `docs/ARCHITECTURE.md`
  explains the whole system and its theory (update it when the design
  changes); `docs/BENCHMARKS.md` holds the measured results.
- **Build and test with `make build` and `make test`**; install with
  `make install` (never `cp` over the binary on macOS). Plain `go build`
  fails on this machine: the Go install is x86_64 on Apple Silicon, and the
  Makefile applies the cgo workaround.
- After an intended parser or resolver change, run `make golden` and review
  the golden diffs.
- Working rules:
  - plan and get approval before each phase, and stop after it with
    results;
  - keep dependencies minimal and justify new ones;
  - never store source code in the DB;
  - commit only when asked.

<!-- uxor:start -->
## Code navigation (uxor)
- For any "where is / how does / what calls X" question, call the uxor
  `find_relevant` MCP tool first, before Grep, Glob or even a targeted grep,
  then read only the listed line ranges. A prompt hook may already list likely
  locations; start there.
- Use `get_symbol` to locate a definition by name.
- Before changing a signature, behaviour or file, check `callers_of` and `impact_of`.
- Weights below 1 in results are resolution guesses; confirm by reading.
<!-- uxor:end -->
