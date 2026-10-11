package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

type Event struct {
	Kind string // message, tool, or error
	Text string
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
		*events = append(*events, Event{Kind: kind, Text: text})
	}
}

func toolText(name string, input json.RawMessage) string {
	if name == "" {
		return ""
	}
	var args map[string]json.RawMessage
	_ = json.Unmarshal(input, &args)
	parts := []string{name}
	for _, key := range []string{"command", "CommandLine", "file_path", "target_file", "AbsolutePath", "path", "target_directory", "directory", "DirectoryPath", "SearchDirectory", "pattern", "SearchPattern", "Pattern", "glob", "query"} {
		var value string
		if json.Unmarshal(args[key], &value) == nil && value != "" {
			parts = append(parts, value)
		}
	}
	return strings.Join(parts, " ")
}

func codexEvents(b []byte) []Event {
	var events []Event
	eventLines(b, func(raw []byte) {
		var e struct {
			Type string
			Item struct {
				Type, Command, Text string
				ExitCode            *int `json:"exit_code"`
				Changes             []struct{ Kind, Path string }
			}
		}
		if json.Unmarshal(raw, &e) != nil || (e.Type != "item.started" && e.Type != "item.completed") {
			return
		}
		switch e.Item.Type {
		case "command_execution":
			if e.Item.Command == "" {
				return
			}
			text := "$ " + e.Item.Command
			if e.Type == "item.completed" && e.Item.ExitCode != nil {
				text += fmt.Sprintf(" (exit %d)", *e.Item.ExitCode)
			}
			addEvent(&events, "tool", text)
		case "file_change":
			for _, c := range e.Item.Changes {
				addEvent(&events, "tool", c.Kind+" "+c.Path)
			}
		case "agent_message":
			addEvent(&events, "message", e.Item.Text)
		}
	})
	return events
}

func claudeEvents(b []byte) []Event {
	var events []Event
	eventLines(b, func(raw []byte) {
		var e struct {
			Type    string
			Message struct {
				Content []struct {
					Type, Name, Text string
					Input            json.RawMessage
				}
			}
		}
		if json.Unmarshal(raw, &e) != nil || e.Type != "assistant" {
			return
		}
		for _, c := range e.Message.Content {
			switch c.Type {
			case "tool_use":
				addEvent(&events, "tool", toolText(c.Name, c.Input))
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
			switch s.State {
			case "DONE":
				addEvent(&events, "tool", summary)
			case "ERROR":
				addEvent(&events, "error", summary+" failed: "+string(s.ToolInfo.Error))
			}
		}
	})
	flush()
	return events
}

func piEvents(b []byte) []Event {
	var events []Event
	eventLines(b, func(raw []byte) {
		var e struct {
			Type, ToolName string
			Args           json.RawMessage
			Message        json.RawMessage
		}
		if json.Unmarshal(raw, &e) != nil {
			return
		}
		switch e.Type {
		case "tool_execution_start":
			addEvent(&events, "tool", toolText(e.ToolName, e.Args))
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
