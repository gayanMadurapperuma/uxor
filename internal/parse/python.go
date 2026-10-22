package parse

import (
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

type pyHooks struct{}

func (pyHooks) module(f *file) (string, string, string) {
	f.isTest = IsTestPath(f.path)
	mod := strings.TrimSuffix(strings.TrimSuffix(f.path, ".pyi"), ".py")
	mod = strings.TrimSuffix(strings.TrimSuffix(mod, "/__init__"), "__init__")
	mod = strings.ReplaceAll(mod, "/", ".")
	if mod == "" {
		mod = "__init__"
	}
	name := mod
	if i := strings.LastIndex(mod, "."); i >= 0 {
		name = mod[i+1:]
	}
	return name, mod, mod + "."
}

func (pyHooks) symbol(f *file, d *rawDef, parent *rawDef) bool {
	if d.kind == KindVar && d.name == "__all__" {
		return false
	}
	public := !strings.HasPrefix(d.name, "_") || (strings.HasPrefix(d.name, "__") && strings.HasSuffix(d.name, "__"))
	if d.parent == 0 && len(f.exported) > 0 {
		public = f.exported[d.name]
	}
	d.exported = public && parent.exported
	if f.isTest {
		switch {
		case d.kind == KindClass && strings.HasPrefix(d.name, "Test"):
			d.kind = KindTest
		case (d.kind == KindFunction || d.kind == KindMethod) && strings.HasPrefix(d.name, "test"):
			d.kind = KindTest
		}
	}
	return true
}

func (pyHooks) imports(f *file, n *sitter.Node) []Ref {
	var out []Ref
	cur := n.Walk()
	defer cur.Close()
	switch n.Kind() {
	case "import_statement":
		for _, c := range n.ChildrenByFieldName("name", cur) {
			mod, alias := pyAliased(f, &c)
			if alias == "" {
				alias = mod
			}
			out = append(out, Ref{Type: EdgeImports, Name: mod, Qualifier: alias})
		}
	case "import_from_statement":
		mn := n.ChildByFieldName("module_name")
		if mn == nil {
			return nil
		}
		mod := f.text(mn)
		if childByKind(n, "wildcard_import") != nil {
			return []Ref{{Type: EdgeImports, Name: mod, Member: "*"}}
		}
		for _, c := range n.ChildrenByFieldName("name", cur) {
			member, alias := pyAliased(f, &c)
			if alias == "" {
				alias = member
			}
			out = append(out, Ref{Type: EdgeImports, Name: mod, Member: member, Qualifier: alias})
		}
	}
	return out
}

func pyAliased(f *file, n *sitter.Node) (name, alias string) {
	if n.Kind() == "aliased_import" {
		if nn := n.ChildByFieldName("name"); nn != nil {
			name = f.text(nn)
		}
		if a := n.ChildByFieldName("alias"); a != nil {
			alias = f.text(a)
		}
		return name, alias
	}
	return f.text(n), ""
}

var pySkip = setOf("print", "len", "range", "str", "int", "float", "bool", "list", "dict", "set",
	"tuple", "isinstance", "issubclass", "super", "type", "repr", "enumerate", "zip", "map",
	"filter", "sorted", "reversed", "min", "max", "sum", "any", "all", "abs", "getattr",
	"setattr", "hasattr", "iter", "next", "id", "hash", "open", "format", "vars", "callable")

func (pyHooks) skipCall(name string) bool { return pySkip[name] }
