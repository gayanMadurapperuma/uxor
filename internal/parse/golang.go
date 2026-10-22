package parse

import (
	"path"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

type goHooks struct{}

func (goHooks) module(f *file) (string, string, string) {
	f.isTest = IsTestPath(f.path)
	pkg := ""
	if pc := childByKind(f.root, "package_clause"); pc != nil {
		if id := childByKind(pc, "package_identifier"); id != nil {
			pkg = f.text(id)
		}
	}
	dir := path.Dir(f.path)
	if pkg == "" {
		pkg = path.Base(dir)
	}
	return pkg, dir, pkg + "."
}

func (goHooks) symbol(f *file, d *rawDef, parent *rawDef) bool {
	d.exported = isUpperFirst(d.name)
	switch d.kind {
	case KindMethod:
		if d.node.Kind() == "method_declaration" {
			d.container = goReceiverType(f, &d.node)
		} else { // interface method_elem
			d.exported = d.exported && parent.exported
		}
	case KindFunction:
		if f.isTest && isGoTestName(d.name) {
			d.kind = KindTest
		}
	case KindInterface, KindType:
		d.sig = "type " + signature(f.src, d.start, 0, d.end)
		if t := d.node.ChildByFieldName("type"); t != nil {
			switch t.Kind() {
			case "struct_type":
				d.sig = "type " + d.name + " struct"
			case "interface_type":
				d.sig = "type " + d.name + " interface"
			}
		}
	case KindVar:
		kw := "var"
		for p := d.node.Parent(); p != nil; p = p.Parent() {
			if p.Kind() == "const_declaration" {
				kw = "const"
				break
			}
			if p.Kind() == "var_declaration" {
				break
			}
		}
		d.sig = kw + " " + signature(f.src, d.start, 0, d.end)
	}
	return true
}

func isGoTestName(name string) bool {
	for _, p := range []string{"Test", "Benchmark", "Fuzz", "Example"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// goReceiverType extracts "T" from receivers like (t T), (t *T), (t *T[K]).
func goReceiverType(f *file, m *sitter.Node) string {
	recv := m.ChildByFieldName("receiver")
	if recv == nil {
		return ""
	}
	pd := childByKind(recv, "parameter_declaration")
	if pd == nil {
		return ""
	}
	t := pd.ChildByFieldName("type")
	for t != nil {
		switch t.Kind() {
		case "type_identifier":
			return f.text(t)
		case "pointer_type":
			t = t.NamedChild(0)
		case "generic_type":
			t = t.ChildByFieldName("type")
		default:
			return baseName(f.text(t))
		}
	}
	return ""
}

func (goHooks) imports(f *file, n *sitter.Node) []Ref {
	p := n.ChildByFieldName("path")
	if p == nil {
		return nil
	}
	r := Ref{Type: EdgeImports, Name: unquote(f.text(p))}
	if a := n.ChildByFieldName("name"); a != nil {
		r.Qualifier = f.text(a)
	} else {
		r.Qualifier = path.Base(r.Name)
	}
	return []Ref{r}
}

var goSkip = setOf("append", "cap", "clear", "close", "complex", "copy", "delete", "imag", "len",
	"make", "max", "min", "new", "panic", "print", "println", "real", "recover",
	"bool", "byte", "rune", "string", "error", "any", "int", "int8", "int16", "int32", "int64",
	"uint", "uint8", "uint16", "uint32", "uint64", "uintptr", "float32", "float64")

func (goHooks) skipCall(name string) bool { return goSkip[name] }

func setOf(xs ...string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}
