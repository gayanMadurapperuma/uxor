package parse

import (
	"path"
	"strconv"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

// ecmaHooks serve JavaScript, TypeScript and TSX.
type ecmaHooks struct{}

func (ecmaHooks) module(f *file) (string, string, string) {
	f.isTest = IsTestPath(f.path)
	mod := strings.TrimSuffix(f.path, path.Ext(f.path))
	name := path.Base(mod)
	if name == "index" && path.Dir(mod) != "." {
		name = path.Base(path.Dir(mod))
	}
	return name, mod, ""
}

func (ecmaHooks) symbol(f *file, d *rawDef, parent *rawDef) bool {
	switch d.kind {
	case KindTest:
		if !f.isTest {
			return false
		}
		d.exported = false
		if fn := d.node.ChildByFieldName("function"); fn != nil {
			d.sig = f.text(fn) + "(" + strconv.Quote(d.name) + ")"
		}
		return true
	case KindVar:
		if isRequireBinding(f, &d.node) {
			return false // imports are recorded as refs, not symbols
		}
	case KindMethod:
		d.exported = parent.exported && !strings.HasPrefix(d.name, "#")
		if m := childByKind(&d.node, "accessibility_modifier"); m != nil {
			if t := f.text(m); t == "private" || t == "protected" {
				d.exported = false
			}
		}
		return true
	}
	if d.parent == 0 {
		p := d.node.Parent()
		d.exported = (p != nil && p.Kind() == "export_statement") || f.exported[d.name]
	}
	return true
}

func (ecmaHooks) imports(f *file, n *sitter.Node) []Ref {
	switch n.Kind() {
	case "import_statement":
		return ecmaImportStatement(f, n)
	case "export_statement": // re-export: export { a } from "./x" / export * from "./x"
		src := n.ChildByFieldName("source")
		if src == nil {
			return nil
		}
		spec := stringValue(f, src)
		clause := childByKind(n, "export_clause")
		if clause == nil {
			return []Ref{{Type: EdgeImports, Name: spec, Member: "*"}}
		}
		var out []Ref
		for i := uint(0); i < clause.NamedChildCount(); i++ {
			s := clause.NamedChild(i)
			if nn := s.ChildByFieldName("name"); nn != nil {
				out = append(out, Ref{Type: EdgeImports, Name: spec, Member: f.text(nn)})
			}
		}
		return out
	case "call_expression": // require("x") or import("x")
		args := n.ChildByFieldName("arguments")
		if args == nil || args.NamedChildCount() == 0 {
			return nil
		}
		spec := stringValue(f, args.NamedChild(0))
		decl := n.Parent()
		if decl == nil || decl.Kind() != "variable_declarator" {
			return []Ref{{Type: EdgeImports, Name: spec}}
		}
		bind := decl.ChildByFieldName("name")
		switch {
		case bind == nil:
			return []Ref{{Type: EdgeImports, Name: spec}}
		case bind.Kind() == "identifier":
			return []Ref{{Type: EdgeImports, Name: spec, Member: "*", Qualifier: f.text(bind)}}
		case bind.Kind() == "object_pattern":
			var out []Ref
			for i := uint(0); i < bind.NamedChildCount(); i++ {
				c := bind.NamedChild(i)
				switch c.Kind() {
				case "shorthand_property_identifier_pattern":
					out = append(out, Ref{Type: EdgeImports, Name: spec, Member: f.text(c), Qualifier: f.text(c)})
				case "pair_pattern":
					k, v := c.ChildByFieldName("key"), c.ChildByFieldName("value")
					if k != nil && v != nil {
						out = append(out, Ref{Type: EdgeImports, Name: spec, Member: f.text(k), Qualifier: f.text(v)})
					}
				}
			}
			return out
		}
		return []Ref{{Type: EdgeImports, Name: spec}}
	}
	return nil
}

func ecmaImportStatement(f *file, n *sitter.Node) []Ref {
	src := n.ChildByFieldName("source")
	if src == nil {
		return nil
	}
	spec := stringValue(f, src)
	clause := childByKind(n, "import_clause")
	if clause == nil {
		return []Ref{{Type: EdgeImports, Name: spec}} // side-effect import
	}
	var out []Ref
	for i := uint(0); i < clause.NamedChildCount(); i++ {
		c := clause.NamedChild(i)
		switch c.Kind() {
		case "identifier":
			out = append(out, Ref{Type: EdgeImports, Name: spec, Member: "default", Qualifier: f.text(c)})
		case "namespace_import":
			if id := childByKind(c, "identifier"); id != nil {
				out = append(out, Ref{Type: EdgeImports, Name: spec, Member: "*", Qualifier: f.text(id)})
			}
		case "named_imports":
			for j := uint(0); j < c.NamedChildCount(); j++ {
				s := c.NamedChild(j)
				if s.Kind() != "import_specifier" {
					continue
				}
				nn := s.ChildByFieldName("name")
				if nn == nil {
					continue
				}
				r := Ref{Type: EdgeImports, Name: spec, Member: f.text(nn), Qualifier: f.text(nn)}
				if a := s.ChildByFieldName("alias"); a != nil {
					r.Qualifier = f.text(a)
				}
				out = append(out, r)
			}
		}
	}
	return out
}

// isRequireBinding reports declarations like `const x = require("x")`.
func isRequireBinding(f *file, decl *sitter.Node) bool {
	v := childByKind(decl, "variable_declarator")
	if v == nil {
		return false
	}
	val := v.ChildByFieldName("value")
	if val == nil {
		return false
	}
	if val.Kind() == "await_expression" && val.NamedChildCount() > 0 {
		val = val.NamedChild(0)
	}
	if val.Kind() != "call_expression" {
		return false
	}
	fn := val.ChildByFieldName("function")
	return fn != nil && (fn.Kind() == "import" || f.text(fn) == "require")
}

func stringValue(f *file, n *sitter.Node) string {
	if frag := childByKind(n, "string_fragment"); frag != nil {
		return f.text(frag)
	}
	return unquote(f.text(n))
}

var ecmaSkip = setOf("require", "describe", "it", "test", "expect", "beforeEach", "afterEach",
	"beforeAll", "afterAll", "parseInt", "parseFloat", "String", "Number", "Boolean", "Array",
	"Object", "Promise", "Error", "Map", "Set", "Date", "RegExp", "Symbol", "setTimeout",
	"clearTimeout", "setInterval", "clearInterval")

func (ecmaHooks) skipCall(name string) bool { return ecmaSkip[name] }
