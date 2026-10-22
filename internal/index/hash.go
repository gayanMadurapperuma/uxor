package index

import (
	"bytes"
	"crypto/sha256"
	"regexp"
)

// contentHash is a truncated sha256: collision-safe for change detection and
// half the storage of the full digest.
func contentHash(src []byte) []byte {
	sum := sha256.Sum256(src)
	return sum[:16]
}

// generatedHeader matches the conventional markers as whole comment lines:
// Go's "// Code generated <tool>. DO NOT EDIT." and "@generated".
var generatedHeader = regexp.MustCompile(`(?m)^\s*(//|#|/?\*+)\s*(Code generated .*DO NOT EDIT|@generated\b)`)

// isGenerated detects generated or minified sources that would only add
// noise: a generated-code header near the top, or a very long average line.
func isGenerated(src []byte) bool {
	if generatedHeader.Match(src[:min(len(src), 2048)]) {
		return true
	}
	if len(src) > 4096 {
		lines := bytes.Count(src, []byte{'\n'}) + 1
		if len(src)/lines > 300 {
			return true
		}
	}
	return false
}
