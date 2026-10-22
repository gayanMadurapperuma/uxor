<!-- codegraph:start -->
## Code navigation (codegraph)
- Call the `find_relevant` MCP tool before broad Grep/Glob searches, then read only the
  listed line ranges. A prompt hook may already list likely locations.
- Use `get_symbol` to locate a definition by name.
- Before changing a signature, behaviour or file, check `callers_of` and `impact_of`.
- Weights below 1 in results are resolution guesses; confirm by reading.
<!-- codegraph:end -->
