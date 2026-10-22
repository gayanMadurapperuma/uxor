package parse

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"unicode"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

const maxSignature = 200

// Parser parses files. It owns cgo resources and is not safe for concurrent
// use; create one per worker and Close it when done.
type Parser struct {
	parsers map[*language]*sitter.Parser
	cursor  *sitter.QueryCursor
}

func NewParser() *Parser {
	return &Parser{parsers: map[*language]*sitter.Parser{}, cursor: sitter.NewQueryCursor()}
}

// Close releases all tree-sitter parsers and the query cursor.
func (p *Parser) Close() {
	for _, tp := range p.parsers {
		tp.Close()
	}
	p.parsers = nil
	p.cursor.Close()
}

// file is the per-parse context handed to hooks.
type file struct {
	path     string
	src      []byte
	root     *sitter.Node
	isTest   bool
	exported map[string]bool // names from @export.name captures
}

func (f *file) text(n *sitter.Node) string { return n.Utf8Text(f.src) }

type rawDef struct {
	kind       Kind
	name       string
	node       sitter.Node
	start, end uint
	bodyStart  uint // 0 when there is no body
	sig        string
	container  string // extra qualifier, e.g. a Go receiver type
	exported   bool
	parent     int // index into kept defs; 0 is the module
}

type rawRef struct {
	pos uint
	ref Ref
}

// Parse extracts symbols and refs from src. path must be repo-relative.
func (p *Parser) Parse(path string, src []byte) (*FileResult, error) {
	l := lookup(path)
	if l == nil {
		return nil, fmt.Errorf("unsupported file type: %s", path)
	}
	if err := l.load(); err != nil {
		return nil, err
	}
	tp := p.parsers[l]
	if tp == nil {
		tp = sitter.NewParser()
		if err := tp.SetLanguage(l.lang); err != nil {
			tp.Close()
			return nil, err
		}
		p.parsers[l] = tp
	}
	tree := tp.Parse(src, nil)
	if tree == nil {
		return nil, fmt.Errorf("parse failed: %s", path)
	}
	defer tree.Close()
	root := tree.RootNode()
	f := &file{path: path, src: src, root: root, exported: map[string]bool{}}
	return p.extract(l, f), nil
}

func (p *Parser) extract(l *language, f *file) *FileResult {
	h := l.hooks
	names := l.query.CaptureNames()
	var defs []rawDef
	var refs []rawRef
	var extends []rawRef

	matches := p.cursor.Matches(l.query, f.root, f.src)
	for m := matches.Next(); m != nil; m = matches.Next() {
		var (
			main             string
			mainNode         sitter.Node
			nameNodes        []sitter.Node
			body, qual, call *sitter.Node
		)
		for _, c := range m.Captures {
			cn := names[c.Index]
			node := c.Node
			switch {
			case strings.HasPrefix(cn, "_"):
			case cn == "name":
				nameNodes = append(nameNodes, node)
			case cn == "body":
				body = &node
			case cn == "call.name":
				call = &node
			case cn == "call.qualifier":
				qual = &node
			case cn == "export.name":
				f.exported[unquote(f.text(&node))] = true
			case cn == "extends" || cn == "implements":
				t := EdgeExtends
				if cn == "implements" {
					t = EdgeImplements
				}
				extends = append(extends, rawRef{pos: node.StartByte(), ref: Ref{
					Type: t, Name: baseName(f.text(&node)), Qualifier: qualifierOf(f.text(&node)),
					Line: int(node.StartPosition().Row) + 1,
				}})
			default:
				main, mainNode = cn, node
			}
		}
		switch {
		case strings.HasPrefix(main, "def."):
			kind := kindFromCapture(main[4:])
			for _, nn := range nameNodes {
				d := rawDef{kind: kind, name: f.text(&nn), node: mainNode,
					start: mainNode.StartByte(), end: mainNode.EndByte()}
				if body != nil {
					d.bodyStart = body.StartByte()
				}
				defs = append(defs, d)
			}
		case main == "call" && call != nil:
			name := f.text(call)
			if h.skipCall(name) {
				continue
			}
			r := Ref{Type: EdgeCalls, Name: name, Line: int(mainNode.StartPosition().Row) + 1}
			if qual != nil {
				r.Qualifier = cleanQualifier(f.text(qual))
			}
			refs = append(refs, rawRef{pos: mainNode.StartByte(), ref: r})
		case main == "import":
			for _, r := range h.imports(f, &mainNode) {
				r.Line = int(mainNode.StartPosition().Row) + 1
				refs = append(refs, rawRef{pos: 0, ref: r}) // imports belong to the module
			}
		}
	}

	modName, modQName, prefix := h.module(f)
	loc := countLines(f.src)
	res := &FileResult{Path: f.path, Lang: l.name, LOC: loc}
	res.Symbols = append(res.Symbols, Symbol{
		Kind: KindModule, Name: modName, QualifiedName: modQName,
		StartLine: 1, EndLine: max(loc, 1), Exported: true, Parent: -1,
	})

	kept := buildSymbols(f, h, defs, prefix, res)

	refs = append(refs, extends...)
	attributeRefs(refs, kept, res)
	return res
}

// buildSymbols orders definitions, assigns containment parents, applies
// hooks, and appends the surviving symbols to res. It returns the kept defs
// aligned with res.Symbols (index 0 is a placeholder for the module).
func buildSymbols(f *file, h hooks, defs []rawDef, prefix string, res *FileResult) []rawDef {
	sort.SliceStable(defs, func(i, j int) bool {
		if defs[i].start != defs[j].start {
			return defs[i].start < defs[j].start
		}
		return defs[i].end > defs[j].end
	})
	defs = dedupDefs(defs)

	kept := []rawDef{{start: 0, end: ^uint(0), exported: true}}
	var stack []int // indices into kept
	for i := range defs {
		d := defs[i]
		for len(stack) > 0 && kept[stack[len(stack)-1]].end <= d.start {
			stack = stack[:len(stack)-1]
		}
		d.parent = 0
		if len(stack) > 0 {
			d.parent = stack[len(stack)-1]
		}
		par := &kept[d.parent]
		if d.kind == KindFunction && d.parent != 0 && (par.kind == KindClass || par.kind == KindInterface) {
			d.kind = KindMethod
		}
		if !h.symbol(f, &d, par) {
			continue
		}
		if d.sig == "" {
			d.sig = signature(f.src, d.start, d.bodyStart, d.end)
		}
		var qn string
		switch {
		case d.parent == 0:
			qn = prefix
		default:
			qn = res.Symbols[d.parent].QualifiedName + "."
		}
		if d.container != "" {
			qn += d.container + "."
		}
		qn += d.name
		res.Symbols = append(res.Symbols, Symbol{
			Kind: d.kind, Name: d.name, QualifiedName: qn, Signature: d.sig,
			StartLine: int(d.node.StartPosition().Row) + 1,
			EndLine:   int(d.node.EndPosition().Row) + 1,
			Exported:  d.exported, Parent: d.parent,
		})
		kept = append(kept, d)
		stack = append(stack, len(kept)-1)
	}
	return kept
}

// dedupDefs drops repeated captures of the same node and name (e.g. a Go
// interface also matching the generic type pattern), keeping the most
// specific kind. Input must be sorted by start.
func dedupDefs(defs []rawDef) []rawDef {
	out := defs[:0]
	for _, d := range defs {
		dup := false
		for j := len(out) - 1; j >= 0 && out[j].start == d.start; j-- {
			if out[j].end == d.end && out[j].name == d.name {
				if specificity(d.kind) > specificity(out[j].kind) {
					out[j] = d
				}
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, d)
		}
	}
	return out
}

func specificity(k Kind) int {
	switch k {
	case KindVar:
		return 0
	case KindType:
		return 1
	default:
		return 2
	}
}

// attributeRefs assigns each ref to the innermost kept symbol containing it
// and drops duplicates of the same (owner, type, name, qualifier, member).
func attributeRefs(refs []rawRef, kept []rawDef, res *FileResult) {
	sort.SliceStable(refs, func(i, j int) bool { return refs[i].pos < refs[j].pos })
	type key struct {
		from              int
		t                 EdgeType
		name, qual, membr string
	}
	seen := map[key]bool{}
	var stack []int
	next := 1
	for _, rr := range refs {
		owner := 0
		if rr.ref.Type != EdgeImports {
			for next < len(kept) && kept[next].start <= rr.pos {
				for len(stack) > 0 && kept[stack[len(stack)-1]].end <= kept[next].start {
					stack = stack[:len(stack)-1]
				}
				stack = append(stack, next)
				next++
			}
			for len(stack) > 0 && kept[stack[len(stack)-1]].end <= rr.pos {
				stack = stack[:len(stack)-1]
			}
			if len(stack) > 0 {
				owner = stack[len(stack)-1]
			}
		}
		r := rr.ref
		r.From = owner
		k := key{owner, r.Type, r.Name, r.Qualifier, r.Member}
		if r.Name == "" || seen[k] {
			continue
		}
		seen[k] = true
		res.Refs = append(res.Refs, r)
	}
}

func kindFromCapture(s string) Kind {
	switch s {
	case "function":
		return KindFunction
	case "method":
		return KindMethod
	case "class":
		return KindClass
	case "interface":
		return KindInterface
	case "type":
		return KindType
	case "var":
		return KindVar
	case "test":
		return KindTest
	}
	return KindVar
}

// signature is the declaration header: text up to the body (or the first
// line when there is none), whitespace-collapsed and truncated.
func signature(src []byte, start, bodyStart, end uint) string {
	stop := end
	if bodyStart > start {
		stop = bodyStart
	}
	header := src[start:stop]
	if bodyStart <= start {
		if i := bytes.IndexByte(header, '\n'); i >= 0 {
			header = header[:i]
		}
	}
	// Comment-only lines between a header and its body (Python comments
	// before the first statement) are not part of the signature.
	lines := strings.Split(string(header), "\n")
	kept := lines[:0]
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if !strings.HasPrefix(t, "#") && !strings.HasPrefix(t, "//") {
			kept = append(kept, l)
		}
	}
	s := strings.Join(strings.Fields(strings.Join(kept, "\n")), " ")
	s = strings.TrimSuffix(s, "{")
	s = strings.TrimSuffix(s, ";")
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, ":")
	s = strings.TrimSuffix(s, "=>")
	s = strings.TrimSpace(s)
	return truncate(s, maxSignature)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// UnknownQualifier marks a call whose receiver is an expression too complex
// to resolve (a call chain, literal or index): `", ".join(x)` is a method
// call on something, not an unqualified call to join.
const UnknownQualifier = "?"

// cleanQualifier keeps short, simple receiver expressions ("fmt",
// "this.repo") and replaces call chains or literals with UnknownQualifier.
func cleanQualifier(s string) string {
	if s == "super()" {
		return "super" // Python super().method()
	}
	if len(s) > 64 || strings.ContainsAny(s, "()[]{}\"'` \n\t") {
		return UnknownQualifier
	}
	return s
}

// baseName returns the last identifier of a dotted/qualified type
// expression, without generic arguments: "pkg.Base[T]" → "Base".
func baseName(s string) string {
	if i := strings.IndexAny(s, "[<("); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimLeft(s, "*&")
	if i := strings.LastIndex(s, "."); i >= 0 {
		return s[i+1:]
	}
	return strings.TrimSpace(s)
}

func qualifierOf(s string) string {
	if i := strings.IndexAny(s, "[<("); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimLeft(s, "*&")
	if i := strings.LastIndex(s, "."); i >= 0 {
		return cleanQualifier(s[:i])
	}
	return ""
}

func unquote(s string) string { return strings.Trim(s, "\"'`") }

func countLines(src []byte) int {
	if len(src) == 0 {
		return 0
	}
	n := bytes.Count(src, []byte{'\n'})
	if src[len(src)-1] != '\n' {
		n++
	}
	return n
}

func isUpperFirst(s string) bool {
	for _, r := range s {
		return unicode.IsUpper(r)
	}
	return false
}

// childByKind returns the first direct named child of the given kind.
func childByKind(n *sitter.Node, kind string) *sitter.Node {
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c != nil && c.Kind() == kind {
			return c
		}
	}
	return nil
}
