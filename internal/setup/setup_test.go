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
	os.WriteFile(filepath.Join(root, ".mcp.json"), []byte(`{"mcpServers": {"other": {"command": "x"}, "codegraph": {"command": "/opt/codegraph", "args": ["serve"]}}}`), 0o644)
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
