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
