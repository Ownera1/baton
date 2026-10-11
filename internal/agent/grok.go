package agent

import (
	"encoding/json"
	"fmt"
	"strings"
)

func grokArgv(o Options) []string {
	a := []string{"-p", o.Prompt, "--output-format", "streaming-json", "--cwd", o.Dir, "--permission-mode", "plan"}
	if o.Mode == "write" {
		// Headless acceptEdits cancels every write (PermissionCancelled, grok 1.0.41).
		// auto approves inside the workspace sandbox, which blocks writes outside --cwd.
		a = append(a[:len(a)-1], "auto", "--sandbox", "workspace")
	} else {
		// In plan mode a terminal command cancels the entire turn.
		a = append(a, "--tools", "read_file,list_dir,grep")
	}
	if o.Model != "" {
		a = append(a, "-m", o.Model)
	}
	if o.Effort != "" {
		a = append(a, "--effort", o.Effort)
	}
	if o.SessionID != "" {
		a = append(a, "-r", o.SessionID)
	}
	return a
}

func parseGrok(b []byte) (Result, error) {
	var r Result
	var text strings.Builder
	done := false
	err := jsonLines(b, func(raw json.RawMessage) error {
		var e struct {
			Type, Data, SessionID, StopReason string
			Usage                             json.RawMessage
			TotalCostUSD                      *float64 `json:"total_cost_usd"`
		}
		if err := json.Unmarshal(raw, &e); err != nil {
			return err
		}
		switch e.Type {
		case "tool_call", "tool_call_update":
			text.Reset()
		case "text":
			text.WriteString(e.Data)
		case "end":
			r.SessionID, r.Usage, r.CostUSD = e.SessionID, e.Usage, e.TotalCostUSD
			done = true
			r.Error = ""
			if e.StopReason != "end_turn" && e.StopReason != "stop" {
				r.Error = fmt.Sprintf("grok stopReason=%s", e.StopReason)
			}
		}
		return nil
	})
	r.FinalText = text.String()
	if err != nil {
		return r, err
	}
	if !done {
		return r, fmt.Errorf("missing end")
	}
	if r.Error != "" {
		return r, nil
	}
	return r, complete(r)
}
