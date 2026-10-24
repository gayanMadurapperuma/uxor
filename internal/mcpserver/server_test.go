package mcpserver_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/gayanmadurapperuma/uxor/internal/config"
	"github.com/gayanmadurapperuma/uxor/internal/mcpserver"
	"github.com/gayanmadurapperuma/uxor/internal/testfix"
)

// connect serves p over in-memory transports and returns a client session.
func connect(t *testing.T, p *config.Project) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	s := mcpserver.New(p, "test")
	t.Cleanup(func() { s.Close() })
	ct, st := mcp.NewInMemoryTransports()
	if _, err := s.MCP().Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

type call struct {
	text    string
	isError bool
	data    map[string]any
}

func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) call {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var c call
	c.isError = res.IsError
	for _, ct := range res.Content {
		if tc, ok := ct.(*mcp.TextContent); ok {
			c.text += tc.Text
		}
	}
	if res.StructuredContent != nil {
		b, _ := json.Marshal(res.StructuredContent)
		json.Unmarshal(b, &c.data)
	}
	return c
}

func TestTools(t *testing.T) {
	p := testfix.Indexed(t, "ts")
	cs := connect(t, p)

	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range tools.Tools {
		names = append(names, tl.Name)
	}
	if got := strings.Join(names, ","); got != "callees_of,callers_of,find_relevant,get_symbol,impact_of" {
		t.Errorf("tools: %s", got)
	}

	for _, c := range []struct {
		tool     string
		args     map[string]any
		contains []string
		isError  bool
	}{
		{"find_relevant", map[string]any{"query": "get user by id"},
			[]string{"src/services/userService.ts:19-23  method async getUserById"}, false},
		{"find_relevant", map[string]any{"query": "the code please"}, []string{"no matches"}, false},
		{"get_symbol", map[string]any{"name": "getUserById"},
			[]string{"2 symbols match", "[UserLookup.getUserById]", "[UserService.getUserById]"}, false},
		{"get_symbol", map[string]any{"name": "Repo.findById"}, []string{"src/repo.ts:7-9  method findById"}, false},
		{"get_symbol", map[string]any{"name": "noSuchThing"}, []string{"no symbol named"}, true},
		{"callers_of", map[string]any{"symbol": "getUserById"}, []string{"is ambiguous", "UserService.getUserById"}, false},
		{"callers_of", map[string]any{"symbol": "UserService.getUserById"},
			[]string{"callers of UserService.getUserById", `test it("returns a user")  (guess 0.25)`}, false},
		{"callees_of", map[string]any{"symbol": "UserService.getUserById"},
			[]string{"function debug(", "method protected log(", "method findById(id: number): Promise<User | undefined>  (guess 0.5)"}, false},
		{"callees_of", map[string]any{"symbol": "UserService.getUserById", "limit": 1}, []string{"… 2 more"}, false},
		{"impact_of", map[string]any{"path": "src/repo.ts"},
			[]string{"importers: 1", "src/services/userService.ts\n", "callers: 1", "→ Repo.findById  (guess 0.5)"}, false},
		{"impact_of", map[string]any{"path": "src/services/userService.ts"},
			[]string{"tests: ", "src/services/userService.test.ts"}, false},
		{"impact_of", map[string]any{"path": "../../etc/passwd"}, []string{"outside this project"}, true},
		{"impact_of", map[string]any{"path": "src/missing.ts"}, []string{"is not indexed"}, true},
	} {
		got := callTool(t, cs, c.tool, c.args)
		if got.isError != c.isError {
			t.Errorf("%s %v: isError %v, want %v: %s", c.tool, c.args, got.isError, c.isError, got.text)
		}
		for _, want := range c.contains {
			if !strings.Contains(got.text, want) {
				t.Errorf("%s %v: missing %q in:\n%s", c.tool, c.args, want, got.text)
			}
		}
	}

	// Structured content carries the same data.
	got := callTool(t, cs, "find_relevant", map[string]any{"query": "formatUser", "budget_tokens": 50})
	if got.data["budget"] != 50.0 || got.data["results"] == nil || got.data["timings_ms"] == nil {
		t.Errorf("structured find_relevant: %v", got.data)
	}
	// The it() block and App.tsx's handleSignIn (both 0.25 guesses).
	got = callTool(t, cs, "callers_of", map[string]any{"symbol": "UserService.getUserById"})
	if got.data["total"] != 2.0 {
		t.Errorf("structured callers_of: %v", got.data)
	}
}

func TestDisabledAndMissingIndex(t *testing.T) {
	root := testfix.Copy(t, "go")
	p, err := config.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	cs := connect(t, p)

	// No index yet: a tool error that points at `uxor index`.
	got := callTool(t, cs, "get_symbol", map[string]any{"name": "Render"})
	if !got.isError || !strings.Contains(got.text, "uxor index") {
		t.Errorf("missing index: %+v", got)
	}
	// Indexed while the server runs: the next call opens it.
	testfix.Index(t, root)
	if got := callTool(t, cs, "get_symbol", map[string]any{"name": "Render"}); got.isError || !strings.Contains(got.text, "api/handler.go:32-35") {
		t.Errorf("after indexing: %+v", got)
	}

	if err := p.SetDisabled(true); err != nil {
		t.Fatal(err)
	}
	for tool, args := range map[string]map[string]any{
		"find_relevant": {"query": "render"},
		"get_symbol":    {"name": "Render"},
		"callers_of":    {"symbol": "Render"},
		"callees_of":    {"symbol": "Render"},
		"impact_of":     {"path": "api/handler.go"},
	} {
		if got := callTool(t, cs, tool, args); got.isError || got.text != mcpserver.DisabledNotice {
			t.Errorf("%s while disabled: %+v", tool, got)
		}
	}
}
