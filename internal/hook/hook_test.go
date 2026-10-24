package hook_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gayanmadurapperuma/uxor/internal/config"
	"github.com/gayanmadurapperuma/uxor/internal/hook"
	"github.com/gayanmadurapperuma/uxor/internal/testfix"
)

func payload(fields map[string]any) string {
	b, _ := json.Marshal(fields)
	return string(b)
}

func prompt(t *testing.T, ctx context.Context, in string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := hook.Prompt(ctx, strings.NewReader(in), &out)
	return out.String(), err
}

func TestPrompt(t *testing.T) {
	p := testfix.Indexed(t, "ts")
	ctx := context.Background()

	// A cwd below the root resolves the same project.
	start := time.Now()
	out, err := prompt(t, ctx, payload(map[string]any{
		"session_id": "s", "hook_event_name": "UserPromptSubmit",
		"cwd": filepath.Join(p.Root, "src", "services"), "prompt": "why does getUserById return undefined?",
	}))
	if elapsed := time.Since(start); elapsed > 300*time.Millisecond {
		t.Errorf("prompt hook took %s", elapsed)
	}
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not one JSON object: %v\n%s", err, out)
	}
	h := got.HookSpecificOutput
	if h.HookEventName != "UserPromptSubmit" || !strings.HasPrefix(h.AdditionalContext, hook.PromptHeader) ||
		!strings.Contains(h.AdditionalContext, "src/services/userService.ts:19-23") {
		t.Errorf("unexpected output: %+v", h)
	}
	// hook_tokens (500) and hook_top (5) bound the block.
	files := map[string]bool{}
	for _, l := range strings.Split(strings.TrimPrefix(h.AdditionalContext, hook.PromptHeader), "\n") {
		if path, _, ok := strings.Cut(strings.TrimSpace(l), ":"); ok {
			files[path] = true
		}
	}
	if len(files) > p.Config.Budgets.HookTop {
		t.Errorf("%d files, hook_top is %d", len(files), p.Config.Budgets.HookTop)
	}

	for name, c := range map[string]struct {
		in   string
		ctx  context.Context
		skip bool
	}{
		"slash command": {payload(map[string]any{"cwd": p.Root, "prompt": "/compact"}), ctx, true},
		"empty prompt":  {payload(map[string]any{"cwd": p.Root, "prompt": "  "}), ctx, true},
		"no matches":    {payload(map[string]any{"cwd": p.Root, "prompt": "the code please"}), ctx, true},
		"foreign cwd":   {payload(map[string]any{"cwd": t.TempDir(), "prompt": "get user by id"}), ctx, true},
		"no cwd":        {payload(map[string]any{"prompt": "get user by id"}), ctx, false},
		"malformed":     {"not json", ctx, false},
		"deadline":      {payload(map[string]any{"cwd": p.Root, "prompt": "get user by id"}), cancelled(), false},
	} {
		out, err := prompt(t, c.ctx, c.in)
		if out != "" {
			t.Errorf("%s: printed %q", name, out)
		}
		if err == nil || c.skip != hook.Skipped(err) {
			t.Errorf("%s: err %v, want skipped=%v", name, err, c.skip)
		}
	}

	if err := p.SetDisabled(true); err != nil {
		t.Fatal(err)
	}
	if out, err := prompt(t, ctx, payload(map[string]any{"cwd": p.Root, "prompt": "get user by id"})); out != "" || !hook.Skipped(err) {
		t.Errorf("disabled: %q %v", out, err)
	}
}

func cancelled() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func TestEdited(t *testing.T) {
	p := testfix.Indexed(t, "ts")
	ctx := context.Background()
	file := filepath.Join(p.Root, "src", "repo.ts")
	b, _ := os.ReadFile(file)
	os.WriteFile(file, append(b, []byte("\nexport function brandNewLookup(id: number) { return id; }\n")...), 0o644)

	in := payload(map[string]any{"cwd": p.Root, "hook_event_name": "PostToolUse", "tool_name": "Edit",
		"tool_input": map[string]any{"file_path": file, "old_string": "a", "new_string": "b"}})
	if n, err := hook.Edited(ctx, strings.NewReader(in)); err != nil || n != 1 {
		t.Fatalf("edited: %d %v", n, err)
	}
	st := testfix.ReadOnly(t, p)
	if refs, err := st.LookupSymbols(ctx, "brandNewLookup", 5); err != nil || len(refs) != 1 || refs[0].Path != "src/repo.ts" {
		t.Errorf("new symbol not indexed: %v %v", refs, err)
	}

	// A path in another project is ignored, not indexed into this one.
	other := testfix.Indexed(t, "go")
	in = payload(map[string]any{"cwd": p.Root, "tool_name": "Write",
		"tool_input": map[string]any{"file_path": filepath.Join(other.Root, "api", "handler.go"), "content": "x"}})
	if n, err := hook.Edited(ctx, strings.NewReader(in)); n != 0 || !hook.Skipped(err) {
		t.Errorf("foreign path: %d %v", n, err)
	}
	if refs, _ := st.LookupSymbols(ctx, "NewHandler", 5); len(refs) != 0 {
		t.Errorf("foreign file leaked into the index: %v", refs)
	}

	for name, in := range map[string]string{
		"malformed":  "{",
		"no path":    payload(map[string]any{"cwd": p.Root, "tool_input": map[string]any{}}),
		"wrong type": payload(map[string]any{"cwd": p.Root, "tool_input": map[string]any{"file_path": 42}}),
	} {
		if n, err := hook.Edited(ctx, strings.NewReader(in)); n != 0 || err == nil {
			t.Errorf("%s: %d %v", name, n, err)
		}
	}

	if err := p.SetDisabled(true); err != nil {
		t.Fatal(err)
	}
	in = payload(map[string]any{"cwd": p.Root, "tool_input": map[string]any{"file_path": file}})
	if _, err := hook.Edited(ctx, strings.NewReader(in)); !hook.Skipped(err) {
		t.Errorf("disabled: %v", err)
	}
}

// TestPromptNoIndex: a project that was never indexed prints nothing.
func TestPromptNoIndex(t *testing.T) {
	root := testfix.Copy(t, "go")
	if _, err := config.Open(root); err != nil {
		t.Fatal(err)
	}
	out, err := prompt(t, context.Background(), payload(map[string]any{"cwd": root, "prompt": "render"}))
	if out != "" || !hook.Skipped(err) {
		t.Errorf("no index: %q %v", out, err)
	}
}

func TestHookLog(t *testing.T) {
	p := testfix.Indexed(t, "ts")
	ctx := context.Background()
	logPath := filepath.Join(p.DataDir, hook.LogFile)

	// Off by default: nothing is written.
	prompt(t, ctx, payload(map[string]any{"cwd": p.Root, "prompt": "get user by id"}))
	if _, err := os.Stat(logPath); err == nil {
		t.Fatal("hook.log written while hook_log is off")
	}

	os.WriteFile(filepath.Join(p.Root, config.FileName), []byte("hook_log: true\n"), 0o644)
	long := "get user by id " + strings.Repeat("x", 400)
	prompt(t, ctx, payload(map[string]any{"cwd": p.Root, "prompt": long}))
	prompt(t, ctx, payload(map[string]any{"cwd": p.Root, "prompt": "zzzqqq"}))
	file := filepath.Join(p.Root, "src", "repo.ts")
	hook.Edited(ctx, strings.NewReader(payload(map[string]any{"cwd": p.Root, "tool_name": "Edit",
		"tool_input": map[string]any{"file_path": file}})))

	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	var entries []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var e map[string]any
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("bad line %q: %v", line, err)
		}
		entries = append(entries, e)
	}
	if len(entries) != 3 {
		t.Fatalf("%d entries, want 3:\n%s", len(entries), b)
	}
	inj, miss, ed := entries[0], entries[1], entries[2]
	if inj["event"] != "prompt" || inj["result"] != "injected" || inj["files"] == nil || inj["tokens"] == nil {
		t.Errorf("injected entry %v", inj)
	}
	if got := []rune(inj["prompt"].(string)); len(got) > 301 {
		t.Errorf("prompt excerpt not capped: %d chars", len(got))
	}
	if miss["result"] != "skipped: no matches" || miss["files"] != nil {
		t.Errorf("miss entry %v", miss)
	}
	if ed["event"] != "edited" || ed["result"] != "indexed" || ed["files"].([]any)[0] != "src/repo.ts" {
		t.Errorf("edited entry %v", ed)
	}
	if fi, _ := os.Stat(logPath); fi.Mode().Perm() != 0o600 {
		t.Errorf("hook.log mode %v, want 0600", fi.Mode().Perm())
	}
}
