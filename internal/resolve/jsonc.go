package resolve

// stripJSONC removes // and /* */ comments and trailing commas so
// tsconfig/jsconfig/package.json files parse with encoding/json. String
// contents are left untouched.
func stripJSONC(b []byte) []byte {
	return dropTrailingCommas(dropComments(b))
}

// skipString returns the index of the closing quote of the string that
// starts at b[i] == '"'.
func skipString(b []byte, i int) int {
	for j := i + 1; j < len(b); j++ {
		switch b[j] {
		case '\\':
			j++
		case '"':
			return j
		}
	}
	return len(b) - 1
}

func dropComments(b []byte) []byte {
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		switch {
		case b[i] == '"':
			j := skipString(b, i)
			out = append(out, b[i:j+1]...)
			i = j
		case b[i] == '/' && i+1 < len(b) && b[i+1] == '/':
			for i < len(b) && b[i] != '\n' {
				i++
			}
			if i < len(b) {
				out = append(out, '\n')
			}
		case b[i] == '/' && i+1 < len(b) && b[i+1] == '*':
			i += 2
			for i+1 < len(b) && !(b[i] == '*' && b[i+1] == '/') {
				i++
			}
			i++
		default:
			out = append(out, b[i])
		}
	}
	return out
}

func dropTrailingCommas(b []byte) []byte {
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		switch b[i] {
		case '"':
			j := skipString(b, i)
			out = append(out, b[i:j+1]...)
			i = j
		case ',':
			j := i + 1
			for j < len(b) && (b[j] == ' ' || b[j] == '\t' || b[j] == '\n' || b[j] == '\r') {
				j++
			}
			if j < len(b) && (b[j] == '}' || b[j] == ']') {
				continue
			}
			out = append(out, ',')
		default:
			out = append(out, b[i])
		}
	}
	return out
}
