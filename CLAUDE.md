# codegraph (development)

A Go CLI and MCP server that indexes a codebase into a SQLite code graph for
token-efficient retrieval in Claude Code.

- **Start every session by reading `docs/TODO.md`.** It holds the roadmap,
  the current status and the next phase. `docs/DECISIONS.md` records why
  things are the way they are (D1…); add new decisions there.
  `docs/TESTING.md` is the manual test guide.
- **Build and test with `make build` and `make test`.** Plain `go build`
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
