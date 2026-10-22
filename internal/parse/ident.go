package parse

import (
	"strings"
	"unicode"
)

// Split breaks an identifier (or any text) into lowercase word parts on
// non-alphanumerics, camelCase, acronym and letter/digit boundaries:
// "HTTPServerError" → [http server error], "get_user_by_id" → [get user by id].
func Split(s string) []string {
	var parts []string
	rs := []rune(s)
	start := -1
	flush := func(end int) {
		if start >= 0 && end > start {
			parts = append(parts, strings.ToLower(string(rs[start:end])))
		}
		start = -1
	}
	for i, r := range rs {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			flush(i)
			continue
		}
		if start < 0 {
			start = i
			continue
		}
		prev := rs[i-1]
		switch {
		case unicode.IsLower(prev) && unicode.IsUpper(r):
			flush(i)
			start = i
		case unicode.IsUpper(prev) && unicode.IsUpper(r) && i+1 < len(rs) && unicode.IsLower(rs[i+1]):
			flush(i)
			start = i
		case unicode.IsDigit(prev) != unicode.IsDigit(r):
			flush(i)
			start = i
		}
	}
	flush(len(rs))
	return parts
}

// SplitText returns s followed by its split parts, for full-text indexing,
// so both the exact identifier and its words match.
func SplitText(s string) string {
	parts := Split(s)
	if len(parts) <= 1 {
		return s
	}
	return s + " " + strings.Join(parts, " ")
}
