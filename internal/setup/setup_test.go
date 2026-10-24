package setup

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite examples/")

func actions(cs []Change) map[string]string {
	m := map[string]string{}
	for _, c := range cs {
		m[c.Path] = c.Action
	}
	return m
}

func readJSON(t *testing.T, p string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]any{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestFreshAndIdempotent(t *testing.T) {
	root := t.TempDir()
	cs, err := Plan(root)
	if err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(".claude", "settings.json")
	want := map[string]string{".mcp.json": "create", settings: "create", "CLAUDE.md": "create"}
	if got := actions(cs); !reflect.DeepEqual(got, want) {
		t.Fatalf("plan: %v", got)
	}
	if _, err := os.Stat(filepath.Join(root, ".mcp.json")); !os.IsNotExist(err) {
		t.Fatal("Plan wrote a file")
	}
	if err := Apply(root, cs); err != nil {
		t.Fatal(err)
	}
	m := readJSON(t, filepath.Join(root, ".mcp.json"))
	if _, ok := m["mcpServers"].(map[string]any)[ServerName]; !ok {
		t.Errorf(".mcp.json: %v", m)
	}
	cs, err = Plan(root)
	if err != nil {
		t.Fatal(err)
	}
	for path, a := range actions(cs) {
		if !strings.HasPrefix(a, "unchanged") {
			t.Errorf("re-run would %s %s", a, path)
		}
	}
}

func TestMergeKeepsExisting(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".claude"), 0o755)
	os.WriteFile(filepath.Join(root, ".claude", "settings.json"), []byte(`{
  "permissions": {"allow": ["Bash(make test)"]},
  "hooks": {"PostToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "lint.sh"}]}]}
}`), 0o644)
	os.WriteFile(filepath.Join(root, ".mcp.json"), []byte(`{"mcpServers": {"other": {"command": "x"}, "uxor": {"command": "/opt/uxor", "args": ["serve"]}}}`), 0o644)
	os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("# My project\nRules."), 0o644)

	cs, err := Plan(root)
	if err != nil {
		t.Fatal(err)
	}
	if a := actions(cs)[".mcp.json"]; a != "unchanged: server entry exists" {
		t.Errorf(".mcp.json: %s", a)
	}
	if err := Apply(root, cs); err != nil {
		t.Fatal(err)
	}
	m := readJSON(t, filepath.Join(root, ".claude", "settings.json"))
	if m["permissions"] == nil {
		t.Error("permissions dropped")
	}
	hooks := m["hooks"].(map[string]any)
	post := hooks["PostToolUse"].([]any)
	if len(post) != 2 || !hasCommand(post[:1], "lint.sh") || !hasCommand(post, EditedCommand) {
		t.Errorf("PostToolUse: %v", post)
	}
	if !hasCommand(hooks["UserPromptSubmit"].([]any), PromptCommand) {
		t.Errorf("UserPromptSubmit: %v", hooks["UserPromptSubmit"])
	}
	md, _ := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
	if !strings.HasPrefix(string(md), "# My project\nRules.\n\n"+SnippetStart) || strings.Count(string(md), SnippetStart) != 1 {
		t.Errorf("CLAUDE.md:\n%s", md)
	}
}

func TestInvalidJSONWritesNothing(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, ".mcp.json"), []byte(`{"mcpServers": `), 0o644)
	if _, err := Plan(root); err == nil || !strings.Contains(err.Error(), ".mcp.json") {
		t.Errorf("want a .mcp.json error, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "CLAUDE.md")); !os.IsNotExist(err) {
		t.Error("CLAUDE.md written despite the error")
	}
}

// TestExamples keeps examples/ generated from this package (-update rewrites).
func TestExamples(t *testing.T) {
	ex, err := Examples()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join("..", "..", "examples")
	for name, want := range ex {
		p := filepath.Join(dir, name)
		if *update {
			os.MkdirAll(dir, 0o755)
			if err := os.WriteFile(p, want, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("%v (run go test ./internal/setup -update)", err)
		}
		if string(got) != string(want) {
			t.Errorf("%s is stale (run go test ./internal/setup -update)", p)
		}
	}
}

// TestSnippetRefresh: an outdated uxor block is replaced in place,
// text around it is kept, and a block without an end marker is left alone.
func TestSnippetRefresh(t *testing.T) {
	root := t.TempDir()
	md := filepath.Join(root, "CLAUDE.md")
	old := "# My project\n\nOwn notes.\n\n" + SnippetStart + "\n## Code navigation (uxor)\n- old wording\n" +
		SnippetEnd + "\n\n## After\nMore notes.\n"
	os.WriteFile(md, []byte(old), 0o644)
	cs, err := Plan(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := actions(cs)["CLAUDE.md"]; got != "update: snippet refreshed" {
		t.Fatalf("action %q", got)
	}
	if err := Apply(root, cs); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(md)
	want := "# My project\n\nOwn notes.\n\n" + Snippet + "\n## After\nMore notes.\n"
	if string(b) != want {
		t.Errorf("refreshed CLAUDE.md:\n%s\nwant:\n%s", b, want)
	}
	if cs, _ := Plan(root); actions(cs)["CLAUDE.md"] != "unchanged: snippet up to date" {
		t.Errorf("second run: %v", actions(cs))
	}

	broken := "notes\n" + SnippetStart + "\nno end marker\n"
	os.WriteFile(md, []byte(broken), 0o644)
	cs, _ = Plan(root)
	Apply(root, cs)
	if b, _ := os.ReadFile(md); string(b) != broken {
		t.Errorf("a block without an end marker was changed:\n%s", b)
	}
}

// TestLegacySetupReplaced: init replaces our pre-rename codegraph setup, and
// leaves another tool's "codegraph" server and CLAUDE.md section alone (D36).
func TestLegacySetupReplaced(t *testing.T) {
	root := t.TempDir()
	write := func(rel, s string) {
		os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o755)
		os.WriteFile(filepath.Join(root, rel), []byte(s), 0o644)
	}
	write(".mcp.json", `{"mcpServers": {"codegraph": {"type": "stdio", "command": "/opt/bin/codegraph",
		"args": ["--root", "${CLAUDE_PROJECT_DIR:-.}", "serve"]}, "other": {"command": "x"}}}`)
	write(".claude/settings.json", `{"hooks": {
		"UserPromptSubmit": [{"hooks": [{"type": "command", "command": "codegraph hook prompt", "timeout": 5},
		                                {"type": "command", "command": "my-own-hook"}]}],
		"PostToolUse": [{"matcher": "Edit|Write", "hooks": [{"type": "command", "command": "codegraph hook edited"}]}]}}`)
	write("CLAUDE.md", "# P\n\n<!-- codegraph:start -->\n## Code navigation (codegraph)\n- old\n<!-- codegraph:end -->\n\nEnd.\n")

	cs, err := Plan(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(root, cs); err != nil {
		t.Fatal(err)
	}
	servers := readJSON(t, filepath.Join(root, ".mcp.json"))["mcpServers"].(map[string]any)
	if _, ok := servers["codegraph"]; ok || servers[ServerName] == nil || servers["other"] == nil {
		t.Errorf("servers: %v", servers)
	}
	b, _ := os.ReadFile(filepath.Join(root, ".claude", "settings.json"))
	if strings.Contains(string(b), "codegraph hook") || !strings.Contains(string(b), "my-own-hook") ||
		!strings.Contains(string(b), PromptCommand) || !strings.Contains(string(b), EditedCommand) {
		t.Errorf("settings: %s", b)
	}
	b, _ = os.ReadFile(filepath.Join(root, "CLAUDE.md"))
	if want := "# P\n\n" + Snippet + "\nEnd.\n"; string(b) != want {
		t.Errorf("CLAUDE.md:\n%s\nwant:\n%s", b, want)
	}
	if cs, _ := Plan(root); actions(cs)[".mcp.json"] != "unchanged: server entry exists" {
		t.Errorf("not idempotent: %v", actions(cs))
	}

	// Another tool's codegraph entry and section are not ours: kept.
	other := t.TempDir()
	os.WriteFile(filepath.Join(other, ".mcp.json"), []byte(`{"mcpServers": {"codegraph": {"command": "codegraph", "args": ["serve", "--mcp"]}}}`), 0o644)
	theirs := "<!-- codegraph:start -->\n## CodeGraph\nUse codegraph_explore.\n<!-- codegraph:end -->\n"
	os.WriteFile(filepath.Join(other, "CLAUDE.md"), []byte(theirs), 0o644)
	cs, _ = Plan(other)
	Apply(other, cs)
	servers = readJSON(t, filepath.Join(other, ".mcp.json"))["mcpServers"].(map[string]any)
	if servers["codegraph"] == nil || servers[ServerName] == nil {
		t.Errorf("other tool's server removed or ours missing: %v", servers)
	}
	if b, _ := os.ReadFile(filepath.Join(other, "CLAUDE.md")); !strings.HasPrefix(string(b), theirs) {
		t.Errorf("other tool's section changed:\n%s", b)
	}
}
