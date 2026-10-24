// Package parse turns source files into symbols and unresolved references
// using tree-sitter. Results are plain Go values; no cgo memory escapes.
package parse

// Version identifies the extraction rules. Bump it whenever the same
// source would now yield different symbols or refs: the next whole-project
// index then re-parses every file (D34). 2: nested named functions (D30).
const Version = "2"

// Kind is a symbol kind. Values are persisted; append only.
type Kind uint8

const (
	KindFunction Kind = iota + 1
	KindMethod
	KindClass
	KindInterface
	KindType
	KindVar
	KindModule
	KindTest
)

var kindNames = [...]string{"", "function", "method", "class", "interface", "type", "var", "module", "test"}

func (k Kind) String() string {
	if int(k) < len(kindNames) {
		return kindNames[k]
	}
	return "unknown"
}

// EdgeType is a graph edge type. Values are persisted; append only.
type EdgeType uint8

const (
	EdgeImports EdgeType = iota + 1
	EdgeDefines
	EdgeCalls
	EdgeExtends
	EdgeImplements
	EdgeReferences
	EdgeTests
)

var edgeNames = [...]string{"", "IMPORTS", "DEFINES", "CALLS", "EXTENDS", "IMPLEMENTS", "REFERENCES", "TESTS"}

func (e EdgeType) String() string {
	if int(e) < len(edgeNames) {
		return edgeNames[e]
	}
	return "UNKNOWN"
}

// Symbol is a definition. Symbols[0] of a FileResult is always the file's
// module symbol.
type Symbol struct {
	Kind          Kind
	Name          string
	QualifiedName string
	Signature     string
	StartLine     int // 1-based, inclusive
	EndLine       int // 1-based, inclusive
	Exported      bool
	Parent        int // index into FileResult.Symbols; -1 for the module symbol
}

// Ref is an unresolved reference from a symbol to something by name.
type Ref struct {
	From      int // index into FileResult.Symbols
	Type      EdgeType
	Name      string // callee / base type / import specifier
	Qualifier string // receiver or local alias, e.g. "fmt" in fmt.Println
	Member    string // imported member for from-imports / named imports
	Line      int
}

// FileResult is everything extracted from one file.
type FileResult struct {
	Path    string // repo-relative, forward slashes
	Lang    string
	LOC     int
	Symbols []Symbol
	Refs    []Ref
}
