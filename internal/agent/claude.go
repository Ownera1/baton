package agent

import (
	"encoding/json"
	"fmt"
)

func claudeArgv(o Options) []string {
	mode := "plan"
	if o.Mode == "write" {
		mode = "acceptEdits"
	}
	a := []string{"-p", "--output-format", "stream-json", "--verbose", "--permission-mode", mode}
	if o.Model != "" {
		a = append(a, "--model", o.Model)
	}
	if o.Effort != "" {
		a = append(a, "--effort", o.Effort)
	}
	if o.SessionID != "" {
		a = append(a, "--resume", o.SessionID)
	}
	return append(a, o.Prompt)
}

func parseClaude(b []byte) (Result, error) {
	var result json.RawMessage
	err := jsonLines(b, func(raw json.RawMessage) error {
		var e struct{ Type string }
		if err := json.Unmarshal(raw, &e); err != nil {
			return err
		}
		if e.Type == "result" {
			result = raw
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	if result == nil {
		return Result{}, fmt.Errorf("missing result")
	}
	return parseClaudeResult(result)
}

func parseClaudeResult(b []byte) (Result, error) {
	var e struct {
		Result            string
		SessionID         string `json:"session_id"`
		IsError           bool   `json:"is_error"`
		Subtype           string
		TotalCostUSD      *float64 `json:"total_cost_usd"`
		Usage             json.RawMessage
		PermissionDenials []json.RawMessage `json:"permission_denials"`
	}
	if err := json.Unmarshal(b, &e); err != nil {
		return Result{}, err
	}
	r := Result{SessionID: e.SessionID, FinalText: e.Result, Usage: e.Usage, CostUSD: e.TotalCostUSD}
	if len(e.PermissionDenials) > 0 {
		r.Warnings = append(r.Warnings, fmt.Sprintf("claude permission_denials=%s", e.PermissionDenials))
	}
	if e.IsError || (e.Subtype != "" && e.Subtype != "success") {
		r.Error = fmt.Sprintf("claude subtype=%s is_error=%v", e.Subtype, e.IsError)
		return r, nil
	}
	return r, complete(r)
}
