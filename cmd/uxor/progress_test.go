package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gayanmadurapperuma/uxor/internal/index"
	"github.com/gayanmadurapperuma/uxor/internal/resolve"
)

func TestRenderLine(t *testing.T) {
	for _, c := range []struct {
		ev   index.Event
		want string
	}{
		{index.Event{Stage: "scanning"}, "⠙ scanning files   1.2s"},
		{index.Event{Stage: "parsing", Done: 1284, Total: 2982}, "⠙ parsing   1,284 / 2,982 files   1.2s"},
		{index.Event{Stage: "resolving"}, "⠙ resolving references   1.2s"},
		{index.Event{Stage: "done"}, ""},
		{index.Event{}, ""},
	} {
		if got := renderLine(c.ev, 1234*time.Millisecond, 11); got != c.want {
			t.Errorf("%+v: %q, want %q", c.ev, got, c.want)
		}
	}
}

func TestThousands(t *testing.T) {
	for n, want := range map[int]string{0: "0", 999: "999", 1000: "1,000", 86585: "86,585", 1234567: "1,234,567", -4200: "-4,200"} {
		if got := thousands(n); got != want {
			t.Errorf("%d: %q", n, got)
		}
	}
}

func TestIsTerminal(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "out"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if isTerminal(&bytes.Buffer{}) || isTerminal(f) {
		t.Error("a buffer or regular file counted as a terminal")
	}
}

func TestLiveProgress(t *testing.T) {
	var buf syncBuffer
	lp := newLiveProgress(&buf)
	lp.Event(index.Event{Stage: "parsing", Done: 2, Total: 5})
	time.Sleep(3 * redraw)
	lp.stop()
	lp.stop() // a second stop is a no-op
	out := buf.String()
	if !strings.Contains(out, "parsing   2 / 5 files") || !strings.HasSuffix(out, "\r\x1b[K") {
		t.Errorf("output %q", out)
	}
}

func TestPrintChecks(t *testing.T) {
	var buf bytes.Buffer
	printChecks(&buf, &index.Result{
		Parsed: 2982, Unchanged: 3, Elapsed: 3800 * time.Millisecond,
		Resolve: &resolve.Result{Full: true, Files: 2982, Refs: 200, Resolved: 100, Edges: 86585,
			Elapsed: 800 * time.Millisecond, PageRankNodes: 49844, PageRankTime: 100 * time.Millisecond},
	})
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	want := []string{
		"✓ parsed 2,982 files (3 unchanged)", "3.0s",
		"✓ resolved 86,585 edges (full, 50.0% refs)", "0.7s",
		"✓ pagerank 49,844 nodes", "0.1s",
	}
	if len(lines) != 3 {
		t.Fatalf("lines %q", lines)
	}
	for i, l := range lines {
		if !strings.HasPrefix(l, want[2*i]) || !strings.HasSuffix(l, want[2*i+1]) {
			t.Errorf("line %d %q, want %q … %q", i, l, want[2*i], want[2*i+1])
		}
	}
}

// syncBuffer is a bytes.Buffer safe for the redraw goroutine and the test.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
