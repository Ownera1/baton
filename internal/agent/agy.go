package agent

import (
	"encoding/json"
	"fmt"
	"strings"
)

func agyArgv(o Options) []string {
	mode := "plan"
	if o.Mode == "write" {
		mode = "accept-edits"
	}
	a := []string{"-p", o.Prompt, "--output-format", "stream-json", "--mode", mode}
	if o.Model != "" {
		a = append(a, "--model", o.Model)
	}
	if o.Effort != "" {
		a = append(a, "--effort", o.Effort)
	}
	if o.SessionID != "" {
		a = append(a, "--conversation", o.SessionID)
	}
	return a
}

func parseAgy(b []byte) (Result, error) {
	var result json.RawMessage
	err := jsonLines(b, func(raw json.RawMessage) error {
		var e struct {
			Event  string
			Result json.RawMessage
		}
		if err := json.Unmarshal(raw, &e); err != nil {
			return err
		}
		if e.Event == "result" {
			result = e.Result
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	if result == nil {
		return Result{}, fmt.Errorf("missing result")
	}
	return parseAgyResult(result)
}

func parseAgyResult(b []byte) (Result, error) {
	var e struct {
		ConversationID   string `json:"conversation_id"`
		Status, Response string
		Usage            json.RawMessage
	}
	if err := json.Unmarshal(b, &e); err != nil {
		return Result{}, err
	}
	r := Result{SessionID: e.ConversationID, FinalText: e.Response, Usage: e.Usage}
	if !strings.EqualFold(e.Status, "success") {
		r.Error = fmt.Sprintf("agy status=%s", e.Status)
		return r, nil
	}
	return r, complete(r)
}
