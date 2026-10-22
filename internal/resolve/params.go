package resolve

import (
	"strings"
)

// paramType reports whether q is a parameter of src with a declared type,
// and that type's symbol (nil when the type is external or builtin, e.g.
// *testing.T or str).
func (r *resolver) paramType(sc *scope, src *sym, q string) (*sym, bool) {
	if src.sig == "" || strings.ContainsAny(q, ".[(") {
		return nil, false
	}
	m, ok := r.params[src.id]
	if !ok {
		m = map[string]*sym{}
		r.params[src.id] = m // guard: resolving a type name may consult src's params again
		for name, typ := range parseParams(sc.f.family, src.sig) {
			base, qual := typeName(typ)
			if base == "" {
				continue
			}
			var t *sym
			for _, tg := range r.resolveName(sc, src, ref{name: base, qualifier: qual}, classKinds) {
				if tg.w == wExact {
					t = tg.sym
				}
			}
			m[name] = t
		}
	}
	t, typed := m[q]
	return t, typed
}

// parseParams extracts typed parameters (name → type text) from a
// signature. Untyped parameters are omitted.
func parseParams(fam, sig string) map[string]string {
	out := map[string]string{}
	switch fam {
	case "go":
		s, ok := strings.CutPrefix(sig, "func ")
		if !ok {
			return out
		}
		var groups []string
		if strings.HasPrefix(s, "(") { // receiver
			var recv string
			recv, s = balanced(s, '(', ')')
			groups = append(groups, recv)
		}
		s = strings.TrimLeft(strings.TrimSpace(s), "_abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")
		if strings.HasPrefix(s, "[") { // type parameters
			_, s = balanced(s, '[', ']')
		}
		if strings.HasPrefix(s, "(") {
			params, _ := balanced(s, '(', ')')
			groups = append(groups, params)
		}
		for _, g := range groups {
			var pending []string
			for _, part := range splitTop(g) {
				f := strings.Fields(part)
				switch len(f) {
				case 0:
				case 1:
					pending = append(pending, f[0]) // "a, b int": a takes b's type
				default:
					typ := strings.TrimPrefix(strings.Join(f[1:], " "), "...")
					out[f[0]] = typ
					for _, p := range pending {
						out[p] = typ
					}
					pending = nil
				}
			}
		}
	case "python", "ecma":
		i := strings.IndexByte(sig, '(')
		if i < 0 {
			return out
		}
		inner, _ := balanced(sig[i:], '(', ')')
		for _, part := range splitTop(inner) {
			name, typ, ok := strings.Cut(part, ":")
			if !ok {
				continue
			}
			if j := indexTop(typ, '='); j >= 0 {
				typ = typ[:j]
			}
			f := strings.Fields(name)
			if len(f) == 0 {
				continue
			}
			n := strings.TrimRight(strings.TrimLeft(f[len(f)-1], "*."), "?") // drop modifiers, */**/..., optional marker
			out[n] = strings.TrimSpace(typ)
		}
	}
	return out
}

// typeName reduces a type expression to its base name and qualifier:
// "*store.User" → (User, store); "Optional[User]" → (User, ""); "User |
// undefined" → (User, ""). Builtin-looking or structural types give "".
func typeName(t string) (base, qual string) {
	t = strings.TrimSpace(t)
	if strings.HasPrefix(t, "Optional[") && strings.HasSuffix(t, "]") {
		t = t[len("Optional[") : len(t)-1]
	}
	if parts := strings.Split(t, "|"); len(parts) > 1 {
		t = ""
		for _, p := range parts {
			if p = strings.TrimSpace(p); p != "undefined" && p != "null" && p != "None" {
				t = p
				break
			}
		}
	}
	t = strings.TrimLeft(t, "*&[]")
	if i := strings.IndexAny(t, "<[({ "); i >= 0 {
		t = t[:i]
	}
	t = strings.Trim(t, `"'`)
	if t == "" {
		return "", ""
	}
	if i := strings.LastIndexByte(t, '.'); i >= 0 {
		return t[i+1:], t[:i]
	}
	return t, ""
}

// balanced returns the content between s[0]==open and its matching close,
// and the remainder after it.
func balanced(s string, open, close byte) (string, string) {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return s[1:i], s[i+1:]
			}
		}
	}
	return s[1:], ""
}

// splitTop splits on commas outside brackets ("=>" is not a bracket).
func splitTop(s string) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(', '[', '{', '<':
			depth++
		case ')', ']', '}', '>':
			if s[i] == '>' && i > 0 && s[i-1] == '=' {
				continue
			}
			depth--
		case ',':
			if depth == 0 {
				out = append(out, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	if rest := strings.TrimSpace(s[start:]); rest != "" {
		out = append(out, rest)
	}
	return out
}

func indexTop(s string, c byte) int {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(', '[', '{', '<':
			depth++
		case ')', ']', '}', '>':
			if s[i] == '>' && i > 0 && s[i-1] == '=' {
				continue
			}
			depth--
		case c:
			if depth == 0 && !(c == '=' && i+1 < len(s) && s[i+1] == '>') {
				return i
			}
		}
	}
	return -1
}
