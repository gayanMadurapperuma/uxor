package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func cli(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	rootFlag = ""
	cmd := newRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func project(t *testing.T, dir string) {
	t.Helper()
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, ".codegraph.yaml"), nil, 0o644)
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, "secrets"), 0o755)
	os.WriteFile(filepath.Join(dir, "secrets", "k.go"), []byte("package secrets\n"), 0o644)
}

func TestProjectScopedCommands(t *testing.T) {
	parent := t.TempDir()
	a, b := filepath.Join(parent, "a"), filepath.Join(parent, "b")
	project(t, a)
	project(t, b)
	for _, p := range []string{a, b} {
		if out, err := cli(t, "", "--root", p, "index"); err != nil {
			t.Fatalf("index %s: %v %s", p, err, out)
		}
	}

	out, err := cli(t, "", "--root", a, "forget", "secrets")
	if err != nil || !strings.Contains(out, "forgot 1 indexed files") {
		t.Fatalf("forget: %v %s", err, out)
	}
	if out, _ := cli(t, "", "--root", a, "stats"); !strings.Contains(out, "files 1 ") || !strings.Contains(out, "forgotten: secrets") {
		t.Errorf("stats a after forget:\n%s", out)
	}
	if out, _ := cli(t, "", "--root", b, "stats"); !strings.Contains(out, "files 2 ") {
		t.Errorf("forget in a affected b:\n%s", out)
	}

	if _, err := cli(t, "", "--root", a, "disable"); err != nil {
		t.Fatal(err)
	}
	if out, _ := cli(t, "", "--root", a, "stats"); !strings.Contains(out, "DISABLED") {
		t.Errorf("stats should show disabled:\n%s", out)
	}
	if out, _ := cli(t, "", "--root", b, "stats"); strings.Contains(out, "DISABLED") {
		t.Error("disable in a affected b")
	}

	if _, err := cli(t, "n\n", "--root", a, "clean"); err == nil {
		t.Error("clean without confirmation should abort")
	}
	if _, err := os.Stat(filepath.Join(a, ".codegraph")); err != nil {
		t.Fatal("aborted clean removed data")
	}
	if _, err := cli(t, "y\n", "--root", a, "clean"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(a, ".codegraph")); !os.IsNotExist(err) {
		t.Error("clean left .codegraph/ behind")
	}
	if _, err := os.Stat(filepath.Join(b, ".codegraph", "index.db")); err != nil {
		t.Error("clean in a removed b's index")
	}
}

func TestQuery(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "q")
	project(t, dir)
	if out, err := cli(t, "", "--root", dir, "query", "main"); err == nil || !strings.Contains(err.Error(), "run `codegraph index`") {
		t.Fatalf("query without an index: %v %s", err, out)
	}
	if out, err := cli(t, "", "--root", dir, "index"); err != nil {
		t.Fatal(err, out)
	}
	out, err := cli(t, "", "--root", dir, "query", "main", "function")
	if err != nil || !strings.HasPrefix(out, "main.go:3-3  func main()\n") {
		t.Fatalf("query: %v %q", err, out)
	}
	out, err = cli(t, "", "--root", dir, "query", "--json", "main")
	for _, key := range []string{`"query": "main"`, `"budget": 1000`, `"tokens_used"`, `"results"`, `"qualified_name"`, `"why": "match`, `"timings_ms"`} {
		if err != nil || !strings.Contains(out, key) {
			t.Errorf("json missing %s: %v\n%s", key, err, out)
		}
	}
	if out, _ := cli(t, "", "--root", dir, "query", "nothingmatcheshere"); out != "no matches\n" {
		t.Errorf("no matches: %q", out)
	}
	if _, err := cli(t, "", "--root", dir, "disable"); err != nil {
		t.Fatal(err)
	}
	out, err = cli(t, "", "--root", dir, "query", "main")
	if err != nil || !strings.Contains(out, "disabled for this project") || !strings.Contains(out, "main.go:3-3") {
		t.Errorf("disabled project should still answer on the CLI with a note: %v %q", err, out)
	}
}

func TestInitAndHooks(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "p")
	project(t, dir)
	out, err := cli(t, "", "--root", dir, "init", "--dry-run")
	if err != nil || !strings.Contains(out, ".mcp.json") || !strings.Contains(out, "dry run") {
		t.Fatalf("dry run: %v %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, ".mcp.json")); !os.IsNotExist(err) {
		t.Fatal("dry run wrote .mcp.json")
	}
	if out, err := cli(t, "", "--root", dir, "init"); err != nil || !strings.Contains(out, "files scanned") {
		t.Fatalf("init: %v %s", err, out)
	}
	for _, f := range []string{".mcp.json", ".claude/settings.json", "CLAUDE.md", ".codegraph/index.db"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("init did not create %s", f)
		}
	}
	if out, _ := cli(t, "", "--root", dir, "init", "--no-index"); strings.Count(out, "unchanged") != 3 {
		t.Errorf("re-run should change nothing:\n%s", out)
	}

	// The hook reads its project from the payload's cwd, not --root or the
	// working directory.
	out, err = cli(t, `{"cwd":"`+dir+`","prompt":"where is main"}`, "hook", "prompt")
	if err != nil || !strings.Contains(out, `"additionalContext"`) || !strings.Contains(out, "main.go:3-3") {
		t.Errorf("hook prompt: %v %s", err, out)
	}
	if out, err := cli(t, "garbage", "hook", "prompt"); err != nil || out != "" {
		t.Errorf("hook prompt on bad input must be silent and succeed: %v %q", err, out)
	}
	if out, err := cli(t, `{"cwd":"`+dir+`","tool_input":{"file_path":"`+filepath.Join(dir, "main.go")+`"}}`, "hook", "edited"); err != nil || out != "" {
		t.Errorf("hook edited: %v %q", err, out)
	}
}
