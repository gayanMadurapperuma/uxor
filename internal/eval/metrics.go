package eval

import (
	"math"
	"sort"
)

// ranks returns the 1-based rank of each truth path in ranked, 0 when it is
// absent.
func ranks(ranked, truth []string) []int {
	pos := make(map[string]int, len(ranked))
	for i, p := range ranked {
		if _, ok := pos[p]; !ok {
			pos[p] = i + 1
		}
	}
	out := make([]int, len(truth))
	for i, t := range truth {
		out[i] = pos[t]
	}
	return out
}

// recallAt is the share of truth files ranked within the top k.
func recallAt(rs []int, k int) float64 {
	if len(rs) == 0 {
		return 0
	}
	n := 0
	for _, r := range rs {
		if r > 0 && r <= k {
			n++
		}
	}
	return float64(n) / float64(len(rs))
}

// reciprocalRank is 1/rank of the best-ranked truth file, 0 if none is
// ranked.
func reciprocalRank(rs []int) float64 {
	best := 0
	for _, r := range rs {
		if r > 0 && (best == 0 || r < best) {
			best = r
		}
	}
	if best == 0 {
		return 0
	}
	return 1 / float64(best)
}

// hit reports whether any truth path is among got.
func hit(got, truth []string) bool {
	set := make(map[string]bool, len(got))
	for _, g := range got {
		set[g] = true
	}
	for _, t := range truth {
		if set[t] {
			return true
		}
	}
	return false
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := 0.0
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

// percentile is the nearest-rank percentile (0 < p ≤ 100) of xs.
func percentile(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	i := int(math.Ceil(p/100*float64(len(s)))) - 1
	return s[min(max(i, 0), len(s)-1)]
}

func round3(f float64) float64 { return math.Round(f*1000) / 1000 }
