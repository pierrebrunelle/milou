package driver

import (
	"encoding/json"
	"strings"
)

// Format profiles: one line parser per harness output dialect. Descriptor
// files reference these by name; unknown output degrades to a raw-text tail
// emitted by cliStream (never silently empty).

func parseText(line []byte, emit func(Event)) {
	if s := strings.TrimSpace(string(line)); s != "" {
		emit(Event{Kind: "text", Text: s})
	}
}

// claude-jsonl: `claude -p --output-format stream-json --verbose` emits
// {"type":"system"}, {"type":"assistant","message":{"content":[text|tool_use]}},
// {"type":"user","message":{"content":[tool_result]}}, {"type":"result"}.
func parseClaudeLine(line []byte, emit func(Event)) {
	var e struct {
		Type    string `json:"type"`
		Message struct {
			Content []struct {
				Type  string         `json:"type"`
				Text  string         `json:"text"`
				Name  string         `json:"name"`
				Input map[string]any `json:"input"`
			} `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &e) != nil {
		return
	}
	for _, c := range e.Message.Content {
		switch c.Type {
		case "text":
			if c.Text != "" {
				emit(Event{Kind: "text", Text: c.Text})
			}
		case "tool_use":
			emit(Event{Kind: "tool_call", ToolID: c.Name, Text: toolLabel(c.Name, c.Input), Status: "in_progress"})
		case "tool_result":
			emit(Event{Kind: "tool_result", ToolID: c.Name, Text: "tool_result", Status: "completed"})
		}
	}
}

// opencode-jsonl: `opencode run --format json` emits
// {"type":"text","part":{"text":...}}, {"type":"tool_use","part":{"tool":...,"state":{"input","status"}}},
// {"type":"step_start"|"step_finish"}.
func parseOpencodeLine(line []byte, emit func(Event)) {
	var e struct {
		Type string `json:"type"`
		Part struct {
			Text  string `json:"text"`
			Tool  string `json:"tool"`
			State struct {
				Status string         `json:"status"`
				Input  map[string]any `json:"input"`
			} `json:"state"`
		} `json:"part"`
	}
	if json.Unmarshal(line, &e) != nil {
		return
	}
	switch e.Type {
	case "text":
		if e.Part.Text != "" {
			emit(Event{Kind: "text", Text: e.Part.Text})
		}
	case "tool_use":
		st := e.Part.State.Status
		if st == "" {
			st = "in_progress"
		}
		emit(Event{Kind: "tool_call", ToolID: e.Part.Tool, Text: toolLabel(e.Part.Tool, e.Part.State.Input), Status: st})
	}
}

// generic-jsonl: last-resort profile for harnesses whose event schema is
// unverified (dsh, codex). Hunts for common text/tool fields at top level.
func parseGenericJSONL(line []byte, emit func(Event)) {
	var e struct {
		Type    string          `json:"type"`
		Text    string          `json:"text"`
		Tool    string          `json:"tool"`
		Name    string          `json:"name"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(line, &e) != nil {
		return
	}
	if e.Text != "" && !strings.Contains(e.Type, "tool") {
		emit(Event{Kind: "text", Text: e.Text})
		return
	}
	if len(e.Content) > 0 && e.Content[0] == '"' {
		var s string
		if json.Unmarshal(e.Content, &s) == nil && s != "" {
			emit(Event{Kind: "text", Text: s})
		}
		return
	}
	if strings.Contains(e.Type, "tool") || e.Tool != "" {
		label := e.Tool
		if label == "" {
			label = e.Name
		}
		if label == "" {
			label = e.Type
		}
		emit(Event{Kind: "tool_call", ToolID: label, Text: label, Status: "in_progress"})
	}
}

func toolLabel(name string, input map[string]any) string {
	for _, k := range []string{"command", "file_path", "path", "pattern"} {
		if v, _ := input[k].(string); v != "" {
			return name + ": " + v
		}
	}
	return name
}
