package agent

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

func codexArgv(o Options) []string {
	sandbox := "read-only"
	if o.Mode == "write" {
		sandbox = "workspace-write"
	}
	a := []string{"exec"}
	if o.SessionID != "" {
		// exec resume has no -s or -C; the local help confirms -c and -m.
		a = append(a, "resume", "-c", "sandbox_mode="+strconv.Quote(sandbox))
	} else {
		a = append(a, "-s", sandbox, "-C", o.Dir)
	}
	// never: the sandbox is a hard boundary. on-request lets the user's auto
	// reviewer approve escalations outside it; Baton runs checks itself.
	a = append(a, "-c", `approval_policy="never"`, "--json", "-o", o.LastFile)
	if o.Model != "" {
		a = append(a, "-m", o.Model)
	}
	if o.Effort != "" {
		a = append(a, "-c", "model_reasoning_effort="+strconv.Quote(o.Effort))
	}
	if o.Mode == "write" && o.Cache != "" {
		// The sandbox blocks the default Go cache, so codex would make one in
		// the worktree (.cache/go-build, hundreds of MB) for Baton to commit.
		// This also drops the user's own writable_roots: a task writes only
		// its worktree and the cache.
		a = append(a, "-c", "sandbox_workspace_write.writable_roots=["+strconv.Quote(o.Cache)+"]",
			"-c", "shell_environment_policy.set.GOCACHE="+strconv.Quote(filepath.Join(o.Cache, "go-build")))
	}
	a = append(a, "--")
	if o.SessionID != "" {
		a = append(a, o.SessionID)
	}
	return append(a, "-") // the prompt comes on stdin, see Stdin
}

func parseCodex(b []byte, last string) (Result, error) {
	var r Result
	done := false
	var transient []string
	err := jsonLines(b, func(raw json.RawMessage) error {
		var e struct {
			Type, Message string
			Thread        string `json:"thread_id"`
			Item          struct{ Type, Text string }
			Usage         json.RawMessage
			Error         json.RawMessage
		}
		if err := json.Unmarshal(raw, &e); err != nil {
			return err
		}
		switch e.Type {
		case "thread.started":
			r.SessionID = e.Thread
		case "item.completed":
			if e.Item.Type == "agent_message" {
				r.FinalText = e.Item.Text
			}
		case "turn.completed":
			r.Usage, done = e.Usage, true
		case "turn.failed":
			r.Error = fmt.Sprintf("%s: %s %s", e.Type, e.Message, e.Error)
		case "error":
			// Stream reconnects are reported as "error" while the turn goes on.
			transient = append(transient, fmt.Sprintf("codex: %s %s", e.Message, e.Error))
		}
		return nil
	})
	if err != nil {
		return r, err
	}
	if r.FinalText == "" {
		r.FinalText = last
	}
	if done {
		r.Warnings = transient
	} else if r.Error == "" && len(transient) > 0 {
		r.Error = strings.Join(transient, "; ")
	}
	if r.Error != "" {
		return r, nil
	}
	if !done {
		return r, fmt.Errorf("missing turn.completed")
	}
	return r, complete(r)
}
