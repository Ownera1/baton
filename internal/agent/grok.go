package agent

import (
	"encoding/json"
	"fmt"
)

func grokArgv(o Options) []string {
	a := []string{"-p", o.Prompt, "--output-format", "json", "--cwd", o.Dir, "--permission-mode", "plan"}
	if o.Mode == "write" {
		// Headless acceptEdits cancels every write (PermissionCancelled, grok 1.0.41).
		// auto approves inside the workspace sandbox, which blocks writes outside --cwd.
		a = append(a[:len(a)-1], "auto", "--sandbox", "workspace")
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
	var e struct {
		Text, SessionID, StopReason string
		Usage                       json.RawMessage
		TotalCostUSD                *float64 `json:"total_cost_usd"`
	}
	if err := json.Unmarshal(b, &e); err != nil {
		return Result{}, err
	}
	r := Result{SessionID: e.SessionID, FinalText: e.Text, Usage: e.Usage, CostUSD: e.TotalCostUSD}
	if e.StopReason != "end_turn" && e.StopReason != "stop" {
		r.Error = fmt.Sprintf("grok stopReason=%s", e.StopReason)
		return r, nil
	}
	return r, complete(r)
}
