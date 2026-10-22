// Package setup holds what `codegraph init` writes into a project (the MCP
// server entry, the hooks and the CLAUDE.md snippet) and merges it into
// existing files. The examples/ directory is generated from the same values.
package setup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ServerName is the key of the MCP server entry.
const ServerName = "codegraph"

// MCPServer is the .mcp.json entry. The root is passed explicitly so the
// server indexes the project Claude Code was started in.
func MCPServer() map[string]any {
	return map[string]any{
		"type":    "stdio",
		"command": "codegraph",
		"args":    []any{"--root", "${CLAUDE_PROJECT_DIR:-.}", "serve"},
	}
}

// Hook commands; init recognises existing entries by these prefixes.
const (
	PromptCommand = "codegraph hook prompt"
	EditedCommand = "codegraph hook edited"
	// EditMatcher selects the file-editing tools. MultiEdit and NotebookEdit
	// are listed in case the Claude Code version has them; a matcher naming an
	// absent tool is harmless.
	EditMatcher = "Edit|Write|MultiEdit|NotebookEdit"
)

// Hooks is the "hooks" object for .claude/settings.json. Timeouts are in
// seconds; the prompt hook stops itself after budgets.hook_timeout_ms.
func Hooks() map[string]any {
	return map[string]any{
		"UserPromptSubmit": []any{map[string]any{
			"hooks": []any{map[string]any{"type": "command", "command": PromptCommand, "timeout": 5}},
		}},
		"PostToolUse": []any{map[string]any{
			"matcher": EditMatcher,
			"hooks":   []any{map[string]any{"type": "command", "command": EditedCommand, "timeout": 30}},
		}},
	}
}

// Snippet markers make the CLAUDE.md addition idempotent and removable.
const (
	SnippetStart = "<!-- codegraph:start -->"
	SnippetEnd   = "<!-- codegraph:end -->"
)

// Snippet is appended to the project's CLAUDE.md.
const Snippet = SnippetStart + `
## Code navigation (codegraph)
- Call the ` + "`find_relevant`" + ` MCP tool before broad Grep/Glob searches, then read only the
  listed line ranges. A prompt hook may already list likely locations.
- Use ` + "`get_symbol`" + ` to locate a definition by name.
- Before changing a signature, behaviour or file, check ` + "`callers_of`" + ` and ` + "`impact_of`" + `.
- Weights below 1 in results are resolution guesses; confirm by reading.
` + SnippetEnd + "\n"

// Change is one planned or applied file change.
type Change struct {
	Path   string // relative to the project root
	Action string // "create", "update" or "unchanged: <why>"
	data   []byte
}

// Plan computes the changes for a project root without writing anything.
// Existing JSON must parse; existing entries of ours are kept, not
// replaced, so local edits survive re-running init.
func Plan(root string) ([]Change, error) {
	var out []Change
	c, err := planJSON(root, ".mcp.json", func(m map[string]any) (bool, string) {
		servers, _ := m["mcpServers"].(map[string]any)
		if servers == nil {
			servers = map[string]any{}
			m["mcpServers"] = servers
		}
		if _, ok := servers[ServerName]; ok {
			return false, "server entry exists"
		}
		servers[ServerName] = MCPServer()
		return true, ""
	})
	if err != nil {
		return nil, err
	}
	out = append(out, c)

	c, err = planJSON(root, filepath.Join(".claude", "settings.json"), func(m map[string]any) (bool, string) {
		hooks, _ := m["hooks"].(map[string]any)
		if hooks == nil {
			hooks = map[string]any{}
			m["hooks"] = hooks
		}
		changed := false
		for event, groups := range Hooks() {
			cmd := PromptCommand
			if event == "PostToolUse" {
				cmd = EditedCommand
			}
			existing, _ := hooks[event].([]any)
			if hasCommand(existing, cmd) {
				continue
			}
			hooks[event] = append(existing, groups.([]any)...)
			changed = true
		}
		return changed, "hooks exist"
	})
	if err != nil {
		return nil, err
	}
	out = append(out, c)

	c, err = planSnippet(root)
	if err != nil {
		return nil, err
	}
	return append(out, c), nil
}

// Apply writes planned changes.
func Apply(root string, changes []Change) error {
	for _, c := range changes {
		if c.data == nil {
			continue
		}
		p := filepath.Join(root, c.Path)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, c.data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func planJSON(root, rel string, merge func(map[string]any) (bool, string)) (Change, error) {
	m := map[string]any{}
	action := "create"
	b, err := os.ReadFile(filepath.Join(root, rel))
	switch {
	case err == nil:
		action = "update"
		if len(bytes.TrimSpace(b)) > 0 {
			if err := json.Unmarshal(b, &m); err != nil {
				return Change{}, fmt.Errorf("%s: %w (fix it or remove it; nothing was written)", rel, err)
			}
		}
	case !errors.Is(err, os.ErrNotExist):
		return Change{}, err
	}
	changed, why := merge(m)
	if !changed {
		return Change{Path: rel, Action: "unchanged: " + why}, nil
	}
	data, err := Marshal(m)
	if err != nil {
		return Change{}, err
	}
	return Change{Path: rel, Action: action, data: data}, nil
}

func hasCommand(groups []any, prefix string) bool {
	for _, g := range groups {
		gm, _ := g.(map[string]any)
		hs, _ := gm["hooks"].([]any)
		for _, h := range hs {
			hm, _ := h.(map[string]any)
			if cmd, _ := hm["command"].(string); strings.HasPrefix(cmd, prefix) {
				return true
			}
		}
	}
	return false
}

func planSnippet(root string) (Change, error) {
	const rel = "CLAUDE.md"
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Change{}, err
	}
	if bytes.Contains(b, []byte(SnippetStart)) {
		return Change{Path: rel, Action: "unchanged: snippet present"}, nil
	}
	action := "update"
	if errors.Is(err, os.ErrNotExist) {
		action = "create"
	}
	data := append([]byte{}, b...)
	if len(data) > 0 {
		if !bytes.HasSuffix(data, []byte("\n")) {
			data = append(data, '\n')
		}
		data = append(data, '\n')
	}
	return Change{Path: rel, Action: action, data: append(data, Snippet...)}, nil
}

// Marshal formats JSON the way the files are written: two-space indent,
// no HTML escaping, trailing newline. Keys are sorted (encoding/json), so
// an updated file may reorder its keys.
func Marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Examples are the files under examples/, generated from the values above.
func Examples() (map[string][]byte, error) {
	mcp, err := Marshal(map[string]any{"mcpServers": map[string]any{ServerName: MCPServer()}})
	if err != nil {
		return nil, err
	}
	settings, err := Marshal(map[string]any{"hooks": Hooks()})
	if err != nil {
		return nil, err
	}
	return map[string][]byte{
		".mcp.json":         mcp,
		"settings.json":     settings,
		"CLAUDE.snippet.md": []byte(Snippet),
	}, nil
}
