package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gayanmadurapperuma/uxor/internal/index"
)

// frames is the spinner of the live progress line.
var frames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// redraw is how often the live line is repainted.
const redraw = 100 * time.Millisecond

// isTerminal reports whether w is an interactive terminal. Pipes, files,
// buffers and TERM=dumb get plain output.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok || os.Getenv("TERM") == "dumb" {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// liveProgress repaints one status line from index events until stop.
type liveProgress struct {
	w     io.Writer
	start time.Time
	mu    sync.Mutex
	ev    index.Event
	quit  chan struct{}
	ended chan struct{}
	once  sync.Once
}

func newLiveProgress(w io.Writer) *liveProgress {
	lp := &liveProgress{w: w, start: time.Now(), quit: make(chan struct{}), ended: make(chan struct{})}
	go lp.loop()
	return lp
}

// Event records the latest progress; it is the index.Options.Progress
// callback.
func (lp *liveProgress) Event(ev index.Event) {
	lp.mu.Lock()
	lp.ev = ev
	lp.mu.Unlock()
}

func (lp *liveProgress) loop() {
	defer close(lp.ended)
	t := time.NewTicker(redraw)
	defer t.Stop()
	for frame := 0; ; frame++ {
		lp.mu.Lock()
		line := renderLine(lp.ev, time.Since(lp.start), frame)
		lp.mu.Unlock()
		if line != "" {
			fmt.Fprint(lp.w, "\r\x1b[K"+line)
		}
		select {
		case <-lp.quit:
			return
		case <-t.C:
		}
	}
}

// stop ends the repainting and clears the line; calling it again is a
// no-op.
func (lp *liveProgress) stop() {
	lp.once.Do(func() {
		close(lp.quit)
		<-lp.ended
		fmt.Fprint(lp.w, "\r\x1b[K")
	})
}

// renderLine is the live line for an event, "" when there is nothing to
// show.
func renderLine(ev index.Event, elapsed time.Duration, frame int) string {
	spin := frames[frame%len(frames)]
	switch ev.Stage {
	case "scanning":
		return fmt.Sprintf("%s scanning files   %s", spin, seconds(elapsed))
	case "parsing":
		return fmt.Sprintf("%s parsing   %s / %s files   %s", spin, thousands(ev.Done), thousands(ev.Total), seconds(elapsed))
	case "resolving":
		return fmt.Sprintf("%s resolving references   %s", spin, seconds(elapsed))
	}
	return ""
}

// printChecks is the terminal summary of an index run: one ✓ line per stage.
func printChecks(w io.Writer, res *index.Result) {
	parse := res.Elapsed
	if rr := res.Resolve; rr != nil {
		parse -= rr.Elapsed
	}
	line := fmt.Sprintf("✓ parsed %s files", thousands(res.Parsed))
	var notes []string
	for _, n := range []struct {
		count int
		what  string
	}{{res.Unchanged, "unchanged"}, {res.Skipped, "skipped"}, {res.Removed, "removed"}, {res.Failed, "failed"}} {
		if n.count > 0 {
			notes = append(notes, thousands(n.count)+" "+n.what)
		}
	}
	if len(notes) > 0 {
		line += " (" + strings.Join(notes, ", ") + ")"
	}
	fmt.Fprintf(w, "%-48s %s\n", line, seconds(parse))
	rr := res.Resolve
	if rr == nil || rr.Files == 0 {
		return
	}
	mode := "scoped"
	if rr.Full {
		mode = "full"
	}
	fmt.Fprintf(w, "%-48s %s\n", fmt.Sprintf("✓ resolved %s edges (%s, %s refs)", thousands(rr.Edges), mode, percent(rr.Resolved, rr.Refs)),
		seconds(rr.Elapsed-rr.PageRankTime))
	if rr.PageRankNodes > 0 {
		fmt.Fprintf(w, "%-48s %s\n", fmt.Sprintf("✓ pagerank %s nodes", thousands(rr.PageRankNodes)), seconds(rr.PageRankTime))
	}
}

func seconds(d time.Duration) string { return fmt.Sprintf("%.1fs", d.Seconds()) }

// thousands formats n with comma separators: 2982 → "2,982".
func thousands(n int) string {
	s := strconv.Itoa(n)
	neg := n < 0
	if neg {
		s = s[1:]
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if neg {
		s = "-" + s
	}
	return s
}
