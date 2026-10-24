package hook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/gayanmadurapperuma/uxor/internal/config"
)

// LogFile is the hook log in the project's data directory (D33).
const LogFile = "hook.log"

const (
	maxLogBytes = 1 << 20 // rotate to hook.log.1 beyond this
	maxExcerpt  = 300     // prompt characters kept
)

// logEntry is one line of hook.log.
type logEntry struct {
	Time   string   `json:"time"`
	Event  string   `json:"event"` // prompt | edited
	Prompt string   `json:"prompt,omitempty"`
	Files  []string `json:"files,omitempty"`
	Tokens int      `json:"tokens,omitempty"`
	MS     float64  `json:"ms"`
	Result string   `json:"result"` // injected | indexed | skipped: … | error: …
}

// appendLog writes one line. A hook must never fail or slow down because of
// its log, so every error here is ignored.
func appendLog(p *config.Project, e logEntry, err error) {
	e.Time = time.Now().Format(time.RFC3339)
	switch {
	case err == nil && e.Event == "prompt":
		e.Result = "injected"
	case err == nil:
		e.Result = "indexed"
	case Skipped(err):
		e.Result = err.Error()
	default:
		e.Result = "error: " + err.Error()
	}
	line, mErr := json.Marshal(e)
	if mErr != nil {
		return
	}
	path := filepath.Join(p.DataDir, LogFile)
	if fi, sErr := os.Stat(path); sErr == nil && fi.Size() > maxLogBytes {
		os.Rename(path, path+".1")
	}
	f, oErr := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if oErr != nil {
		return
	}
	f.Write(append(line, '\n'))
	f.Close()
}

// excerpt keeps the start of a prompt.
func excerpt(s string) string {
	if r := []rune(s); len(r) > maxExcerpt {
		return string(r[:maxExcerpt]) + "…"
	}
	return s
}

func since(t time.Time) float64 { return float64(time.Since(t).Microseconds()) / 1000 }
