package retrieve

import (
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/gayanmadurapperuma/uxor/internal/parse"
)

// maxTerms caps the FTS expression for very long prompts.
const maxTerms = 24

// Query is a tokenized prompt.
type Query struct {
	Raw string
	// Terms are lowercase word parts, stopwords removed, deduplicated, in
	// prompt order. They also match as prefixes.
	Terms []string
	// Whole are multi-word camelCase identifiers, lowercased (formatuser),
	// matched exactly against the marked whole-identifier tokens (D31).
	Whole []string
	// Phrases are word sequences that must be adjacent: hyphenated
	// compounds (sign-in) and multi-word synonyms (log in) (D32).
	Phrases [][]string
	// Idents are identifier-looking words kept as written (formatUser,
	// get_user_by_id, and each part of store.NewMemRepo), for the exact-name
	// boost.
	Idents []string
	// WantsTests is set when the prompt mentions tests, which lifts the
	// test-file penalty.
	WantsTests bool
}

// Empty reports whether nothing searchable is left.
func (q Query) Empty() bool {
	return len(q.Terms) == 0 && len(q.Whole) == 0 && len(q.Phrases) == 0 && len(q.Idents) == 0
}

// stopwords are English function words plus words that are common in
// prompts but say nothing about where code lives. File-extension words are
// here because the FTS path column holds every file's extension. Words
// that often appear inside identifiers (get, set, add, update, delete,
// create, error) are deliberately not here.
var stopwords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`
		a an and any are as at be been being but by can could did do does doing done
		for from had has have having he her here how i if in into is it its it's
		just let me more most my no not now of on once only or other our out over
		own same she should so some such than that the their them then there these
		they this those through to too under until up very was we were what when
		where which while who whom why will with would you your
		also again all always both each few many much must need needs never
		please thanks thank hi hello hey ok okay
		fix fixes fixed fixing bug bugs issue issues problem problems broken
		code codes function functions func method methods class classes file files
		implement implemented implementing implementation make makes making
		want wants wanted like look looks looking see seems seem work works working
		where's what's how's there's doesn't don't isn't can't won't
		something anything everything thing things way ways able
		currently correctly properly right wrong instead sure ensure without within via
		go py js ts tsx jsx`) {
		m[w] = true
	}
	return m
}()

// testWords signal intent (lifting the test penalty) and are not searched:
// "test*" matches most symbols of a test-heavy repo through the path
// column, which floods bm25 (35k of 50k symbols in django).
var testWords = map[string]bool{"test": true, "tests": true, "testing": true, "spec": true, "specs": true, "unittest": true}

// synonyms are groups of words or word sequences that name the same thing
// but often differ between prompts and code ("login" vs signIn). A prompt
// containing any member searches all of them. Deliberately small: broad
// synonyms (delete/remove) would add noise (D32).
var synonyms = [][]string{
	{"login", "log in", "sign in", "signin", "logon"},
	{"logout", "log out", "sign out", "signout", "logoff"},
	{"signup", "sign up"},
}

// compound finds hyphenated words: sign-in, real-time.
var compound = regexp.MustCompile(`\pL+(?:-\pL+)+`)

// Tokenize turns a prompt into search terms with the default expansions.
func Tokenize(s string) Query {
	t := DefaultTuning()
	return tokenize(s, t.Compounds, t.Synonyms)
}

func tokenize(s string, compounds, synonymsOn bool) Query {
	q := Query{Raw: s}
	seenTerm, seenIdent := map[string]bool{}, map[string]bool{}
	addTerm := func(t string) {
		if testWords[t] {
			q.WantsTests = true // also from parts: test_release_memory
			return
		}
		if len(q.Terms) >= maxTerms || seenTerm[t] || stopwords[t] || !useful(t) {
			return
		}
		seenTerm[t] = true
		q.Terms = append(q.Terms, t)
	}
	ws := words(s)
	for i, word := range ws {
		if testWords[strings.ToLower(word)] {
			q.WantsTests = true
			continue
		}
		// A capitalized word names a type or exported symbol (Render, Cart),
		// except at the start of a sentence-like prompt.
		capOK := i > 0 || len(ws) == 1
		// A dotted path (store.NewMemRepo, os.path.join) contributes each part.
		for _, part := range strings.Split(word, ".") {
			if part == "" {
				continue
			}
			ident := isIdent(part) || capOK && isCapitalized(part) && !stopwords[strings.ToLower(part)]
			if ident && !seenIdent[part] {
				seenIdent[part] = true
				q.Idents = append(q.Idents, part)
				// unicode61 keeps camelCase words whole; underscores split.
				// A one-word identifier is just its term.
				if !strings.Contains(part, "_") && len(parse.Split(part)) > 1 && len(q.Whole) < maxTerms {
					q.Whole = append(q.Whole, strings.ToLower(part))
				}
			}
			for _, t := range parse.Split(part) {
				addTerm(t)
			}
		}
	}

	seenWhole := map[string]bool{}
	for _, w := range q.Whole {
		seenWhole[w] = true
	}
	addWhole := func(w string) {
		if !seenWhole[w] && len(q.Whole) < maxTerms {
			seenWhole[w] = true
			q.Whole = append(q.Whole, w)
		}
	}
	seenPhrase := map[string]bool{}
	addPhrase := func(ws []string) {
		if k := strings.Join(ws, " "); !seenPhrase[k] {
			seenPhrase[k] = true
			q.Phrases = append(q.Phrases, ws)
		}
	}
	// One word or several: words are searched as terms and whole
	// identifiers, sequences as phrases.
	addForm := func(ws []string) {
		if len(ws) == 1 {
			addTerm(ws[0])
			addWhole(ws[0])
		} else {
			addPhrase(ws)
		}
	}
	// A compound is a phrase and a joined word (sign-in → "sign in", signin).
	lower := strings.ToLower(s)
	if !compounds {
		lower = ""
	}
	for _, c := range compound.FindAllString(lower, -1) {
		parts := strings.Split(c, "-")
		addPhrase(parts)
		joined := strings.Join(parts, "")
		addTerm(joined)
		addWhole(joined)
	}
	seq := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	for _, group := range synonyms {
		if !synonymsOn {
			break
		}
		if !mentions(seq, group) {
			continue
		}
		for _, m := range group {
			addForm(strings.Fields(m))
		}
	}
	return q
}

// mentions reports whether the word sequence contains any member of group,
// a multi-word member as consecutive words.
func mentions(seq, group []string) bool {
	for _, m := range group {
		ws := strings.Fields(m)
		for i := 0; i+len(ws) <= len(seq); i++ {
			if slices.Equal(seq[i:i+len(ws)], ws) {
				return true
			}
		}
	}
	return false
}

// words splits on anything that cannot be part of an identifier or a
// dotted path.
func words(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return !(r == '_' || r == '.' || unicode.IsLetter(r) || unicode.IsDigit(r))
	})
}

// isIdent reports whether w looks like a code identifier rather than a
// plain word: snake_case, an inner lower→upper case change, or an acronym
// run followed by a word (HTTPServer).
func isIdent(w string) bool {
	w = strings.Trim(w, "_")
	if w == "" {
		return false
	}
	if strings.Contains(w, "_") {
		return true
	}
	rs := []rune(w)
	for i := 1; i < len(rs); i++ {
		if unicode.IsLower(rs[i-1]) && unicode.IsUpper(rs[i]) ||
			unicode.IsUpper(rs[i-1]) && unicode.IsUpper(rs[i]) && i+1 < len(rs) && unicode.IsLower(rs[i+1]) {
			return true
		}
	}
	return false
}

// isCapitalized reports an uppercase first letter followed by at least two
// more letters or digits.
func isCapitalized(w string) bool {
	rs := []rune(w)
	return len(rs) >= 3 && unicode.IsUpper(rs[0])
}

// useful drops one-character terms and bare numbers, which match too much.
func useful(t string) bool {
	if len([]rune(t)) < 2 {
		return false
	}
	for _, r := range t {
		if !unicode.IsDigit(r) {
			return true
		}
	}
	return false
}
