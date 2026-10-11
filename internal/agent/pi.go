package agent

import (
	"encoding/json"
	"fmt"
	"strings"
)

func piArgv(o Options) []string {
	a := []string{"-p", "-ne", "--mode", "json"}
	if o.Mode == "read" {
		a = append(a, "--tools", "read,grep,find,ls")
	}
	if o.Model != "" {
		a = append(a, "--model", o.Model)
	}
	if o.Effort != "" {
		a = append(a, "--thinking", o.Effort)
	}
	if o.SessionID != "" {
		a = append(a, "--session", o.SessionID)
	}
	return append(a, o.Prompt)
}

type piMessage struct {
	Role                     string
	Content                  []struct{ Type, Text string }
	Usage                    json.RawMessage
	StopReason, ErrorMessage string
}

func parsePi(b []byte) (Result, error) {
	var r Result
	var messages []piMessage
	ended, retrying := false, false
	err := jsonLines(b, func(raw json.RawMessage) error {
		var e struct {
			Type, ID  string
			Message   json.RawMessage
			Messages  []json.RawMessage
			WillRetry bool
		}
		if err := json.Unmarshal(raw, &e); err != nil {
			return err
		}
		switch e.Type {
		case "session":
			r.SessionID = e.ID
		case "message_end":
			m, err := assistant(e.Message)
			if err != nil {
				return err
			}
			if m != nil {
				messages = append(messages, *m)
			}
		case "agent_end":
			ended, retrying = true, e.WillRetry
			if len(messages) == 0 {
				for _, raw := range e.Messages {
					m, err := assistant(raw)
					if err != nil {
						return err
					}
					if m != nil {
						messages = append(messages, *m)
					}
				}
			}
		case "agent_settled":
			ended, retrying = true, false
		}
		return nil
	})
	if err != nil {
		return r, err
	}
	if !ended || retrying {
		return r, fmt.Errorf("missing final agent completion")
	}
	if len(messages) == 0 {
		return r, fmt.Errorf("missing assistant message")
	}
	m := messages[len(messages)-1]
	var texts []string
	for _, c := range m.Content {
		if c.Type == "text" {
			texts = append(texts, c.Text)
		}
	}
	r.FinalText = strings.Join(texts, "\n")
	// Count message_end records once; agent_end repeats the same messages.
	totals := map[string]float64{}
	var cost float64
	hasCost := false
	for _, m := range messages {
		var u map[string]json.RawMessage
		if err := json.Unmarshal(m.Usage, &u); err != nil {
			continue
		}
		for k, v := range u {
			var n float64
			if json.Unmarshal(v, &n) == nil {
				totals[k] += n
			}
		}
		var c struct{ Total *float64 }
		if json.Unmarshal(u["cost"], &c) == nil && c.Total != nil {
			cost += *c.Total
			hasCost = true
		}
	}
	if len(totals) > 0 {
		r.Usage, _ = json.Marshal(totals)
	}
	if hasCost {
		r.CostUSD = &cost
	}
	if m.StopReason == "error" || m.StopReason == "aborted" || m.StopReason == "length" || m.ErrorMessage != "" {
		r.Error = fmt.Sprintf("pi stopReason=%s: %s", m.StopReason, m.ErrorMessage)
		return r, nil
	}
	return r, complete(r)
}

func assistant(raw json.RawMessage) (*piMessage, error) {
	var header struct{ Role string }
	if err := json.Unmarshal(raw, &header); err != nil {
		return nil, err
	}
	if header.Role != "assistant" {
		return nil, nil
	}
	var m piMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return &m, nil
}
