// Package hook implements the Claude Code hooks. Both read the hook's JSON
// payload from stdin and resolve the project from its cwd, never from the
// process's working directory. Callers always exit 0: a hook must never
// block a prompt or fail a tool call, so errors are only for debug logs.
package hook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/gayanmadurapperuma/uxor/internal/config"
	"github.com/gayanmadurapperuma/uxor/internal/index"
	"github.com/gayanmadurapperuma/uxor/internal/retrieve"
	"github.com/gayanmadurapperuma/uxor/internal/store"
)

// PromptHeader introduces the injected locations.
const PromptHeader = "Likely relevant locations (uxor; read these line ranges before searching broadly):\n"

// EditTimeout bounds the re-index after an edit.
const EditTimeout = 2 * time.Second

// errSkip marks a deliberate no-op (disabled, no index, …); it is not logged
// as a failure.
var errSkip = errors.New("skipped")

// Skipped reports whether err is a deliberate no-op rather than a failure.
func Skipped(err error) bool { return errors.Is(err, errSkip) }

func skip(why string) error { return fmt.Errorf("%w: %s", errSkip, why) }

// project resolves the project for a hook payload's cwd and checks the
// disabled marker and the index with a stat each, before any database work.
func project(cwd string) (*config.Project, error) {
	if cwd == "" {
		return nil, errors.New("payload has no cwd")
	}
	p, err := config.Open(cwd)
	if err != nil {
		return nil, err
	}
	if p.Disabled() {
		return nil, skip("disabled")
	}
	if _, err := os.Stat(p.DBPath); err != nil {
		return nil, skip("no index")
	}
	return p, nil
}

type promptPayload struct {
	Prompt string `json:"prompt"`
	Cwd    string `json:"cwd"`
}

type promptOutput struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// Prompt is the UserPromptSubmit hook. It writes nothing unless it found
// locations within budgets.hook_timeout_ms; then it writes one JSON object
// whose additionalContext Claude Code adds next to the prompt.
func Prompt(ctx context.Context, stdin io.Reader, stdout io.Writer) error {
	start := time.Now()
	var in promptPayload
	if err := json.NewDecoder(stdin).Decode(&in); err != nil {
		return err
	}
	text := strings.TrimSpace(in.Prompt)
	if text == "" || strings.HasPrefix(text, "/") {
		return skip("empty prompt or slash command")
	}
	p, err := project(in.Cwd)
	if err != nil {
		return err
	}
	res, err := prompt(ctx, p, text, stdout)
	if p.Config.HookLog {
		e := logEntry{Event: "prompt", Prompt: excerpt(text), MS: since(start)}
		if res != nil {
			e.Tokens = res.TokensUsed
			for _, f := range res.Files {
				e.Files = append(e.Files, f.Path)
			}
		}
		appendLog(p, e, err)
	}
	return err
}

// prompt runs retrieval for a resolved project and writes the block. The
// result is returned for the log even when it is not written.
func prompt(ctx context.Context, p *config.Project, text string, stdout io.Writer) (*retrieve.Result, error) {
	b := p.Config.Budgets
	ctx, cancel := context.WithTimeout(ctx, time.Duration(b.HookTimeoutMS)*time.Millisecond)
	defer cancel()
	st, err := store.Open(ctx, p.DBPath, store.Options{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer st.Close()
	res, err := retrieve.FindRelevant(ctx, st, p.Config, text, retrieve.Options{Budget: b.HookTokens, MaxFiles: b.HookTop})
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return res, ctx.Err() // late: print nothing rather than a stale block
	}
	if res.Text == "" {
		return res, skip("no matches")
	}
	var out promptOutput
	out.HookSpecificOutput.HookEventName = "UserPromptSubmit"
	out.HookSpecificOutput.AdditionalContext = PromptHeader + res.Text
	return res, json.NewEncoder(stdout).Encode(out)
}

type editPayload struct {
	Cwd       string         `json:"cwd"`
	ToolInput map[string]any `json:"tool_input"`
}

// pathKeys are the tool_input fields that name an edited file.
var pathKeys = []string{"file_path", "notebook_path"}

// Edited is the PostToolUse hook for file-editing tools: it re-indexes the
// edited files of this project (scoped resolve; PageRank marked stale) and
// returns how many paths it passed on. Paths outside the project are
// ignored.
func Edited(ctx context.Context, stdin io.Reader) (int, error) {
	start := time.Now()
	var in editPayload
	if err := json.NewDecoder(stdin).Decode(&in); err != nil {
		return 0, err
	}
	p, err := project(in.Cwd)
	if err != nil {
		return 0, err
	}
	var paths []string
	for _, k := range pathKeys {
		s, _ := in.ToolInput[k].(string)
		if s == "" {
			continue
		}
		if rel, err := p.Confine(s); err == nil {
			paths = append(paths, rel)
		}
	}
	if len(paths) == 0 {
		return 0, skip("no paths in this project")
	}
	err = reindex(ctx, p, paths)
	if p.Config.HookLog {
		appendLog(p, logEntry{Event: "edited", Files: paths, MS: since(start)}, err)
	}
	if err != nil {
		return 0, err
	}
	return len(paths), nil
}

// reindex runs a scoped index of paths under EditTimeout.
func reindex(ctx context.Context, p *config.Project, paths []string) error {
	ctx, cancel := context.WithTimeout(ctx, EditTimeout)
	defer cancel()
	st, err := store.Open(ctx, p.DBPath, store.Options{BusyTimeoutMS: int(EditTimeout / time.Millisecond)})
	if err != nil {
		return err
	}
	defer st.Close()
	_, err = index.Run(ctx, p, st, index.Options{Paths: paths})
	return err
}
