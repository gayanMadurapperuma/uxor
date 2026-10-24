// Package mcpserver exposes retrieval and graph lookups as MCP tools for
// one project. The project root is fixed when the server starts.
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/gayanmadurapperuma/uxor/internal/config"
	"github.com/gayanmadurapperuma/uxor/internal/parse"
	"github.com/gayanmadurapperuma/uxor/internal/retrieve"
	"github.com/gayanmadurapperuma/uxor/internal/store"
)

// DisabledNotice is returned by every tool while the project is disabled.
const DisabledNotice = "uxor is disabled for this project (`uxor enable` to resume); search the code directly."

const (
	symbolLimit   = 10 // get_symbol matches, and candidates for an ambiguous name
	neighborLimit = 50 // default for callers_of / callees_of
	impactLimit   = 30 // per relation in impact_of
)

// Server serves one project.
type Server struct {
	p   *config.Project
	srv *mcp.Server

	mu sync.Mutex
	st *store.Store // opened on first use, then kept for the server's life
}

// New builds the MCP server and registers its tools.
func New(p *config.Project, version string) *Server {
	s := &Server{p: p}
	s.srv = mcp.NewServer(&mcp.Implementation{Name: "uxor", Version: version}, &mcp.ServerOptions{
		Instructions: "uxor indexes this repository's symbols and call graph. Call find_relevant before " +
			"broad grep/glob searches; call get_symbol to locate a definition, and callers_of / callees_of / " +
			"impact_of before refactoring. Results are file:line ranges to read, never code.",
	})
	mcp.AddTool(s.srv, &mcp.Tool{
		Name: "find_relevant",
		Description: "Find the files and line ranges most relevant to a task or question, ranked and packed " +
			"into a token budget. Use it before broad searches; then read only the listed ranges.",
	}, s.findRelevant)
	mcp.AddTool(s.srv, &mcp.Tool{
		Name:        "get_symbol",
		Description: "Locate a function, method, class or type by name or qualified name (pkg.Type.Method): path, line range, signature.",
	}, s.getSymbol)
	mcp.AddTool(s.srv, &mcp.Tool{
		Name:        "callers_of",
		Description: "List the callers of a symbol, best-resolved first. Weights below 1 are guesses. Use before changing a signature or behaviour.",
	}, s.neighbors(true))
	mcp.AddTool(s.srv, &mcp.Tool{
		Name:        "callees_of",
		Description: "List what a symbol calls, best-resolved first. Weights below 1 are guesses.",
	}, s.neighbors(false))
	mcp.AddTool(s.srv, &mcp.Tool{
		Name: "impact_of",
		Description: "List what depends on a file: importers, callers of its symbols, subclasses/implementers and tests. " +
			"Use before refactoring or deleting a file. The path is relative to the project root.",
	}, s.impactOf)
	return s
}

// MCP returns the underlying server, for transports and tests.
func (s *Server) MCP() *mcp.Server { return s.srv }

// Run serves until the transport closes or ctx ends.
func (s *Server) Run(ctx context.Context, t mcp.Transport) error {
	defer s.Close()
	return s.srv.Run(ctx, t)
}

// Close releases the database.
func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.st == nil {
		return nil
	}
	err := s.st.Close()
	s.st = nil
	return err
}

// errDisabled makes a tool answer with DisabledNotice instead of results.
var errDisabled = errors.New("disabled")

// store checks the disabled marker (one stat, D9), then opens the index
// read-only once. A missing index is an error the next call retries.
func (s *Server) store(ctx context.Context) (*store.Store, error) {
	if s.p.Disabled() {
		return nil, errDisabled
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.st == nil {
		st, err := store.Open(ctx, s.p.DBPath, store.Options{ReadOnly: true})
		if err != nil {
			return nil, err
		}
		s.st = st
	}
	return s.st, nil
}

func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

// begin is the common prologue: the store, or a ready result for the
// disabled case. The SDK validates structured output even then, so
// handlers pair that result with an empty but valid output value.
func (s *Server) begin(ctx context.Context) (*store.Store, *mcp.CallToolResult, error) {
	st, err := s.store(ctx)
	if errors.Is(err, errDisabled) {
		return nil, text(DisabledNotice), nil
	}
	return st, nil, err
}

func emptyResult() *retrieve.Result {
	return &retrieve.Result{Files: []retrieve.File{}, Timings: map[string]float64{}}
}

func emptyImpact() *store.Impact {
	return &store.Impact{Totals: map[string]int{}, Importers: []store.Neighbor{}, Callers: []store.Neighbor{},
		Subtypes: []store.Neighbor{}, Tests: []store.Neighbor{}}
}

type FindRelevantIn struct {
	Query        string `json:"query" jsonschema:"the task, question or identifiers to look for"`
	BudgetTokens int    `json:"budget_tokens,omitempty" jsonschema:"token budget for the answer (default 1000)"`
}

func (s *Server) findRelevant(ctx context.Context, _ *mcp.CallToolRequest, in FindRelevantIn) (*mcp.CallToolResult, *retrieve.Result, error) {
	st, res, err := s.begin(ctx)
	if st == nil {
		return res, emptyResult(), err
	}
	r, err := retrieve.FindRelevant(ctx, st, s.p.Config, in.Query, retrieve.Options{Budget: in.BudgetTokens})
	if err != nil {
		return nil, nil, err
	}
	if r.Text == "" {
		return text("no matches; try identifiers or different words"), r, nil
	}
	return text(r.Text), r, nil
}

type SymbolIn struct {
	Name string `json:"name" jsonschema:"a symbol name (GetUserByID) or qualified name (store.memRepo.GetUserByID)"`
}

type SymbolsOut struct {
	Matches []store.SymbolRef `json:"matches"`
}

func (s *Server) getSymbol(ctx context.Context, _ *mcp.CallToolRequest, in SymbolIn) (*mcp.CallToolResult, *SymbolsOut, error) {
	st, res, err := s.begin(ctx)
	if st == nil {
		return res, &SymbolsOut{Matches: []store.SymbolRef{}}, err
	}
	refs, err := st.LookupSymbols(ctx, in.Name, symbolLimit)
	if err != nil {
		return nil, nil, err
	}
	if len(refs) == 0 {
		return nil, nil, fmt.Errorf("no symbol named %q; try find_relevant", in.Name)
	}
	var b strings.Builder
	if len(refs) > 1 {
		fmt.Fprintf(&b, "%d symbols match %q:\n", len(refs), in.Name)
	}
	for _, r := range refs {
		b.WriteString(line(r) + "  [" + r.QualifiedName + "]\n")
	}
	return text(b.String()), &SymbolsOut{Matches: refs}, nil
}

type NeighborsIn struct {
	Symbol string `json:"symbol" jsonschema:"a symbol name or qualified name"`
	Limit  int    `json:"limit,omitempty" jsonschema:"at most this many results (default 50)"`
}

type NeighborsOut struct {
	Symbol     *store.SymbolRef  `json:"symbol,omitempty"`
	Total      int               `json:"total"`
	Results    []store.Neighbor  `json:"results"`
	Candidates []store.SymbolRef `json:"candidates,omitempty"` // when the name is ambiguous
}

func (s *Server) neighbors(callers bool) mcp.ToolHandlerFor[NeighborsIn, *NeighborsOut] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in NeighborsIn) (*mcp.CallToolResult, *NeighborsOut, error) {
		st, res, err := s.begin(ctx)
		if st == nil {
			return res, &NeighborsOut{Results: []store.Neighbor{}}, err
		}
		refs, err := st.LookupSymbols(ctx, in.Symbol, symbolLimit)
		if err != nil {
			return nil, nil, err
		}
		switch len(refs) {
		case 0:
			return nil, nil, fmt.Errorf("no symbol named %q; try find_relevant", in.Symbol)
		case 1:
		default:
			var b strings.Builder
			fmt.Fprintf(&b, "%q is ambiguous; call again with one of these qualified names:\n", in.Symbol)
			for _, r := range refs {
				b.WriteString(line(r) + "  [" + r.QualifiedName + "]\n")
			}
			return text(b.String()), &NeighborsOut{Results: []store.Neighbor{}, Candidates: refs}, nil
		}
		limit := in.Limit
		if limit <= 0 {
			limit = neighborLimit
		}
		target := refs[0]
		list, total, err := st.Neighbors(ctx, target.ID, callers, []parse.EdgeType{parse.EdgeCalls}, limit)
		if err != nil {
			return nil, nil, err
		}
		verb := "calls"
		if callers {
			verb = "callers of"
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%s %s (%s:%d): %d\n", verb, target.QualifiedName, target.Path, target.StartLine, total)
		for _, n := range list {
			b.WriteString(line(n.SymbolRef) + confidence(n) + "\n")
		}
		if total > len(list) {
			fmt.Fprintf(&b, "… %d more (raise limit)\n", total-len(list))
		}
		return text(b.String()), &NeighborsOut{Symbol: &target, Total: total, Results: list}, nil
	}
}

type ImpactIn struct {
	Path string `json:"path" jsonschema:"a file path relative to the project root"`
}

func (s *Server) impactOf(ctx context.Context, _ *mcp.CallToolRequest, in ImpactIn) (*mcp.CallToolResult, *store.Impact, error) {
	st, res, err := s.begin(ctx)
	if st == nil {
		return res, emptyImpact(), err
	}
	rel, err := s.p.Confine(in.Path)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: outside this project", in.Path)
	}
	imp, err := st.FileImpact(ctx, rel, impactLimit)
	if errors.Is(err, store.ErrNotIndexed) {
		return nil, nil, fmt.Errorf("%s is not indexed", rel)
	}
	if err != nil {
		return nil, nil, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "impact of %s\n", rel)
	for _, g := range []struct {
		name string
		list []store.Neighbor
	}{{"importers", imp.Importers}, {"callers", imp.Callers}, {"subtypes", imp.Subtypes}, {"tests", imp.Tests}} {
		fmt.Fprintf(&b, "\n%s: %d\n", g.name, imp.Totals[g.name])
		for _, n := range g.list {
			l := line(n.SymbolRef)
			if n.Target != "" {
				l += "  → " + n.Target
			}
			b.WriteString(l + confidence(n) + "\n")
		}
		if more := imp.Totals[g.name] - len(g.list); more > 0 {
			fmt.Fprintf(&b, "… %d more\n", more)
		}
	}
	return text(b.String()), imp, nil
}

// line renders a symbol like retrieve's packed lines; a module symbol (an
// importer or test file) is shown as its file.
func line(r store.SymbolRef) string {
	if r.Kind == parse.KindModule.String() {
		return r.Path
	}
	return retrieve.Render(r.Path, retrieve.Symbol{
		Name: r.Name, Kind: r.Kind, StartLine: r.StartLine, EndLine: r.EndLine, Signature: r.Signature,
	}, false)
}

// confidence flags guessed edges (D12 weights below 1). IMPORTS weights
// are not confidences (Go spreads a package import over its files, D13).
func confidence(n store.Neighbor) string {
	if n.Weight >= 1 || n.Type == parse.EdgeImports.String() {
		return ""
	}
	return fmt.Sprintf("  (guess %.2g)", n.Weight)
}
