package resolve

import (
	"path"
	"strings"
)

// testedFiles maps a test file to the source file(s) it tests by naming
// convention: x_test.go → x.go; test_x.py / x_test.py → x.py;
// x.test.ts / x.spec.ts / __tests__/x.ts → x.<ext>.
func (r *resolver) testedFiles(f *fileInfo) []*fileInfo {
	base := path.Base(f.path)
	switch f.family {
	case "go":
		if g := r.t.byPath[path.Join(f.dir, strings.TrimSuffix(base, "_test.go")+".go")]; g != nil && !g.isTest {
			return []*fileInfo{g}
		}
	case "python":
		stem := strings.TrimSuffix(base, ".py")
		stem = strings.TrimSuffix(strings.TrimPrefix(stem, "test_"), "_test")
		if stem == "" || stem == "conftest" {
			return nil
		}
		return closest(f, r.t.byBase[stem+".py"])
	default:
		stem := strings.TrimSuffix(base, path.Ext(base))
		stem = strings.TrimSuffix(strings.TrimSuffix(stem, ".test"), ".spec")
		dirs := []string{f.dir}
		if path.Base(f.dir) == "__tests__" {
			dirs = append(dirs, path.Dir(f.dir))
		}
		for _, d := range dirs {
			for _, x := range ecmaExts {
				if g := r.t.byPath[path.Join(d, stem+x)]; g != nil && !g.isTest {
					return []*fileInfo{g}
				}
			}
		}
	}
	return nil
}

// closest picks the non-test candidate sharing the longest directory prefix
// with f; ties mean the convention is ambiguous and nothing is returned.
func closest(f *fileInfo, cands []*fileInfo) []*fileInfo {
	best, bestLen, tie := (*fileInfo)(nil), -1, false
	for _, c := range cands {
		if c.isTest || c.id == f.id {
			continue
		}
		n := commonDirLen(f.dir, c.dir)
		switch {
		case n > bestLen:
			best, bestLen, tie = c, n, false
		case n == bestLen:
			tie = true
		}
	}
	if best == nil || tie {
		return nil
	}
	return []*fileInfo{best}
}

func commonDirLen(a, b string) int {
	as, bs := strings.Split(a, "/"), strings.Split(b, "/")
	n := 0
	for n < len(as) && n < len(bs) && as[n] == bs[n] {
		n++
	}
	return n
}
