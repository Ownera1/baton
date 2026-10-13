package agent

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

type Options struct {
	Mode, Dir, Prompt, Model, Effort, SessionID, LastFile string
	Path                                                  string // executable; empty means name looked up in PATH
	// Cache is a directory a write task may keep build caches in, shared by tasks.
	Cache string
}

type Result struct {
	SessionID string          `json:"session_id"`
	FinalText string          `json:"final_text"`
	Usage     json.RawMessage `json:"usage,omitempty"`
	CostUSD   *float64        `json:"cost_usd,omitempty"`
	Error     string          `json:"error,omitempty"`
	Warnings  []string        `json:"warnings,omitempty"`
}

// Argv is the executable, from o.Path or PATH, followed by Args.
func Argv(name string, o Options) ([]string, error) {
	args, err := Args(name, o)
	if err != nil {
		return nil, err
	}
	path, err := exec.LookPath(cmp.Or(o.Path, name)) // Direct exec deliberately ignores shell aliases.
	if err != nil {
		return nil, err
	}
	return append([]string{path}, args...), nil
}

// PromptOnStdin reports whether the agent reads Options.Prompt from stdin
// instead of argv, where Linux caps one argument at 128 KiB.
func PromptOnStdin(name string) bool { return name == "codex" || name == "claude" }

// Args builds the agent's arguments without looking up its executable.
func Args(name string, o Options) ([]string, error) {
	var args []string
	switch name {
	case "codex":
		args = codexArgv(o)
	case "claude":
		args = claudeArgv(o)
	case "grok":
		args = grokArgv(o)
	case "agy":
		args = agyArgv(o)
	case "pi":
		args = piArgv(o)
	default:
		return nil, fmt.Errorf("unsupported agent %q", name)
	}
	return args, nil
}

func Parse(name string, stdout []byte, last string, exitCode int) Result {
	var r Result
	var err error
	switch name {
	case "codex":
		r, err = parseCodex(stdout, last)
	case "claude":
		r, err = parseClaude(stdout)
	case "grok":
		r, err = parseGrok(stdout)
	case "agy":
		r, err = parseAgy(stdout)
	case "pi":
		r, err = parsePi(stdout)
	default:
		err = fmt.Errorf("unsupported agent %q", name)
	}
	if err != nil {
		r.FinalText = ""
		r.Error = "parse " + name + " output: " + err.Error()
	}
	if exitCode != 0 {
		r.Error = strings.TrimSpace(fmt.Sprintf("%s; agent exited with code %d", r.Error, exitCode))
		r.Error = strings.TrimPrefix(r.Error, "; ")
	}
	r.FinalText = strings.TrimSpace(r.FinalText)
	return r
}

func jsonLines(b []byte, visit func(json.RawMessage) error) error {
	d := json.NewDecoder(bytes.NewReader(b))
	count := 0
	for {
		var raw json.RawMessage
		err := d.Decode(&raw)
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if err := visit(raw); err != nil {
			return err
		}
		count++
	}
	if count == 0 {
		return fmt.Errorf("empty output")
	}
	return nil
}

func complete(r Result) error {
	if r.SessionID == "" || strings.TrimSpace(r.FinalText) == "" {
		return fmt.Errorf("missing session id or final reply")
	}
	return nil
}
