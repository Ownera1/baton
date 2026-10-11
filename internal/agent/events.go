package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

type Event struct {
	Kind     string // message, tool, or error
	Text     string
	Category string // read, search, edit, command, message, or error
	Right    string // exit code and/or duration, when supplied by the agent
}

// Events summarizes a live stdout snapshot. Bad records and partial lines are
// ignored independently so they cannot hide the rest of the stream.
func Events(name string, stdout []byte) []Event {
	switch name {
	case "codex":
		return codexEvents(stdout)
	case "claude":
		return claudeEvents(stdout)
	case "grok":
		return grokEvents(stdout)
	case "agy":
		return agyEvents(stdout)
	case "pi":
		return piEvents(stdout)
	}
	return nil
}

func eventLines(b []byte, visit func([]byte)) {
	for _, line := range bytes.Split(b, []byte{'\n'}) {
		if json.Valid(line) {
			visit(line)
		}
	}
}

func addEvent(events *[]Event, kind, text string) {
	text = strings.Join(strings.Fields(text), " ")
	if text != "" {
		category := kind
		if kind == "tool" {
			category = toolCategory(text)
		}
		*events = append(*events, Event{Kind: kind, Text: text, Category: category})
	}
}

func toolCategory(text string) string {
	words := strings.Fields(strings.ToLower(strings.TrimPrefix(text, "$ ")))
	if len(words) == 0 {
		return "command"
	}
	name := filepath.Base(words[0])
	if name == "sh" || name == "bash" || name == "zsh" {
		for i, word := range words {
			if (word == "-c" || word == "-lc") && i+1 < len(words) {
				return toolCategory(strings.Trim(strings.Join(words[i+1:], " "), "'\""))
			}
		}
	}
	switch {
	case strings.Contains(name, "edit"), strings.Contains(name, "write"), strings.Contains(name, "patch"), name == "update", name == "add", name == "delete":
		return "edit"
	case strings.Contains(name, "grep"), strings.Contains(name, "search"), strings.Contains(name, "find"), name == "rg", name == "glob":
		return "search"
	case strings.Contains(name, "read"), strings.Contains(name, "view_file"), strings.Contains(name, "list"), name == "cat", name == "ls", name == "head", name == "tail", name == "sed":
		return "read"
	default:
		return "command"
	}
}

func toolRight(raw json.RawMessage) string {
	var meta struct {
		Exit      *int     `json:"exit_code"`
		CamelExit *int     `json:"exitCode"`
		MS        *float64 `json:"duration_ms"`
		CamelMS   *float64 `json:"durationMs"`
		Seconds   *float64 `json:"duration_seconds"`
	}
	if json.Unmarshal(raw, &meta) != nil {
		return ""
	}
	var parts []string
	if meta.Exit == nil {
		meta.Exit = meta.CamelExit
	}
	if meta.Exit != nil {
		parts = append(parts, fmt.Sprintf("exit %d", *meta.Exit))
	}
	if meta.MS == nil {
		meta.MS = meta.CamelMS
	}
	if meta.MS != nil {
		parts = append(parts, fmt.Sprintf("%.1fs", *meta.MS/1000))
	} else if meta.Seconds != nil {
		parts = append(parts, fmt.Sprintf("%.1fs", *meta.Seconds))
	}
	return strings.Join(parts, " · ")
}

func toolText(name string, input json.RawMessage) string {
	if name == "" {
		return ""
	}
	var args map[string]json.RawMessage
	_ = json.Unmarshal(input, &args)
	parts := []string{name}
	for _, key := range []string{"command", "cmd", "CommandLine", "file_path", "target_file", "AbsolutePath", "path", "target_directory", "directory", "DirectoryPath", "SearchDirectory", "pattern", "SearchPattern", "Pattern", "glob", "query"} {
		var value string
		if json.Unmarshal(args[key], &value) == nil && value != "" {
			parts = append(parts, value)
		}
	}
	return strings.Join(parts, " ")
}

func codexEvents(b []byte) []Event {
	var events []Event
	items := map[string]int{}
	eventLines(b, func(raw []byte) {
		var e struct {
			Type  string
			Error struct{ Message string }
			Item  struct {
				ID, Type, Command, Text string
				ExitCode                *int `json:"exit_code"`
				Changes                 []struct{ Kind, Path string }
			}
		}
		if json.Unmarshal(raw, &e) != nil {
			return
		}
		if e.Type == "error" || e.Type == "turn.failed" {
			addEvent(&events, "error", e.Error.Message)
			return
		}
		if e.Type != "item.started" && e.Type != "item.completed" {
			return
		}
		switch e.Item.Type {
		case "command_execution":
			if e.Item.Command == "" {
				return
			}
			addEvent(&events, "tool", "$ "+e.Item.Command)
			var record struct{ Item json.RawMessage }
			_ = json.Unmarshal(raw, &record)
			events[len(events)-1].Right = toolRight(record.Item)
			if e.Item.ID != "" {
				if i, ok := items[e.Item.ID]; ok {
					events[i] = events[len(events)-1]
					events = events[:len(events)-1]
				} else {
					items[e.Item.ID] = len(events) - 1
				}
			}
		case "file_change":
			for _, c := range e.Item.Changes {
				if strings.TrimSpace(c.Kind+c.Path) == "" {
					continue
				}
				addEvent(&events, "tool", c.Kind+" "+c.Path)
				events[len(events)-1].Category = "edit"
			}
		case "agent_message":
			addEvent(&events, "message", e.Item.Text)
		}
	})
	return events
}

func claudeEvents(b []byte) []Event {
	var events []Event
	tools := map[string]int{}
	eventLines(b, func(raw []byte) {
		var e struct {
			Type          string
			ToolUseResult json.RawMessage `json:"tool_use_result"`
			Message       struct {
				Content []struct {
					ID, Type, Name, Text string
					ToolUseID            string `json:"tool_use_id"`
					IsError              bool   `json:"is_error"`
					Content              json.RawMessage
					Input                json.RawMessage
				}
			}
		}
		if json.Unmarshal(raw, &e) != nil || (e.Type != "assistant" && e.Type != "user") {
			return
		}
		for _, c := range e.Message.Content {
			switch c.Type {
			case "tool_use":
				before := len(events)
				addEvent(&events, "tool", toolText(c.Name, c.Input))
				if len(events) > before {
					tools[c.ID] = before
				}
			case "tool_result":
				if i, ok := tools[c.ToolUseID]; ok {
					events[i].Right = toolRight(e.ToolUseResult)
					if c.IsError {
						addEvent(&events, "error", events[i].Text+" failed: "+string(c.Content))
					}
				}
			case "text":
				addEvent(&events, "message", c.Text)
			}
		}
	})
	return events
}

func grokEvents(b []byte) []Event {
	var events []Event
	var text strings.Builder
	tools := map[string]string{}
	flush := func() {
		addEvent(&events, "message", text.String())
		text.Reset()
	}
	eventLines(b, func(raw []byte) {
		var e struct {
			Type, Data, ToolName, ToolCallID, Status string
			RawInput, RawOutput                      json.RawMessage
		}
		if json.Unmarshal(raw, &e) != nil {
			return
		}
		if e.Type == "text" {
			text.WriteString(e.Data)
			return
		}
		flush()
		switch e.Type {
		case "tool_call":
			summary := toolText(e.ToolName, e.RawInput)
			tools[e.ToolCallID] = summary
			addEvent(&events, "tool", summary)
		case "tool_call_update":
			if right := toolRight(e.RawOutput); right != "" {
				for i := len(events) - 1; i >= 0; i-- {
					if events[i].Text == tools[e.ToolCallID] {
						events[i].Right = right
						break
					}
				}
			}
			if e.Status == "failed" {
				summary := tools[e.ToolCallID]
				if summary == "" {
					summary = e.ToolName
				}
				addEvent(&events, "error", summary+" failed: "+string(e.RawOutput))
			}
		}
	})
	flush()
	return events
}

func agyEvents(b []byte) []Event {
	var events []Event
	var text strings.Builder
	flush := func() {
		addEvent(&events, "message", text.String())
		text.Reset()
	}
	eventLines(b, func(raw []byte) {
		var e struct {
			Event string
			Step  struct {
				Type      string `json:"step_type"`
				State     string
				ToolName  string `json:"tool_name"`
				TextDelta string `json:"text_delta"`
				Text      string
				ToolInfo  struct {
					Parameters json.RawMessage
					Error      json.RawMessage
				} `json:"tool_info"`
			} `json:"step_update"`
		}
		if json.Unmarshal(raw, &e) != nil {
			return
		}
		s := e.Step
		if e.Event == "step_update" && s.Type == "agent_response" {
			text.WriteString(s.TextDelta)
			text.WriteString(s.Text)
			return
		}
		flush()
		if e.Event == "step_update" && s.Type == "tool" {
			summary := toolText(s.ToolName, s.ToolInfo.Parameters)
			before := len(events)
			switch s.State {
			case "DONE":
				addEvent(&events, "tool", summary)
			case "ERROR":
				addEvent(&events, "error", summary+" failed: "+string(s.ToolInfo.Error))
			}
			if len(events) > before {
				var record struct {
					Step json.RawMessage `json:"step_update"`
				}
				_ = json.Unmarshal(raw, &record)
				events[len(events)-1].Right = toolRight(record.Step)
			}
		}
	})
	flush()
	return events
}

func piEvents(b []byte) []Event {
	var events []Event
	tools := map[string]int{}
	eventLines(b, func(raw []byte) {
		var e struct {
			Type, ToolName, ToolCallID string
			Args                       json.RawMessage
			Message                    json.RawMessage
			Result                     json.RawMessage
			IsError                    bool
		}
		if json.Unmarshal(raw, &e) != nil {
			return
		}
		switch e.Type {
		case "tool_execution_start":
			before := len(events)
			addEvent(&events, "tool", toolText(e.ToolName, e.Args))
			if len(events) > before {
				tools[e.ToolCallID] = before
			}
		case "tool_execution_end":
			if i, ok := tools[e.ToolCallID]; ok {
				events[i].Right = toolRight(e.Result)
				if events[i].Right == "" {
					events[i].Right = toolRight(raw)
				}
			}
			if e.IsError {
				addEvent(&events, "error", e.ToolName+" failed: "+string(e.Result))
			}
		case "message_end":
			m, err := assistant(e.Message)
			if err != nil || m == nil {
				return
			}
			for _, c := range m.Content {
				if c.Type == "text" {
					addEvent(&events, "message", c.Text)
				}
			}
		}
	})
	return events
}
