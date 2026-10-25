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

// WholeMarker prefixes the whole-identifier token in the full-text index
// (getUserById → "0getUserById"), so a prefix query on a word ("handled"*)
// can never match across word boundaries of a longer identifier
// ("handleDelete"). Exact identifier searches use WholeTerm (D31).
const WholeMarker = "0"

// maxPartWords bounds the word-aligned parts SplitText adds: up to this
// many words every contiguous run of 2+ words, beyond it runs of 2-3 only.
const maxPartWords = 6

// SplitText returns, for full-text indexing, the marked whole identifier,
// the marked word-aligned parts of 2+ words (handleSignIn → handlesign,
// signin), then its split words. So the exact identifier, any run of its
// words (D38) and each word match, and no prefix ever spans a word (D31).
// A one-word identifier is just itself.
func SplitText(s string) string {
	parts := Split(s)
	if len(parts) <= 1 {
		return s
	}
	out := []string{WholeMarker + s}
	longest := len(parts) - 1 // the whole is already there
	if len(parts) > maxPartWords {
		longest = 3
	}
	for i := range parts {
		for n := 2; n <= longest && i+n <= len(parts); n++ {
			out = append(out, WholeMarker+strings.Join(parts[i:i+n], ""))
		}
	}
	return strings.Join(append(out, parts...), " ")
}

// WholeTerm is the search term for a whole multi-word identifier, matching
// what SplitText indexes.
func WholeTerm(s string) string { return WholeMarker + strings.ToLower(s) }
