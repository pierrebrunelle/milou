package driver

import (
	"context"
	"encoding/json"
)

// Pi drives badlogic's coding agent: pi -p --mode json TASK, emitting one
// JSON event per line (message_start, message_update, tool_execution_start…).
type Pi struct {
	AgentDir string // PI_CODING_AGENT_DIR with the bench-style models.json
}

func (Pi) Name() string { return "pi" }

func (p Pi) Start(ctx context.Context, prompt, cwd string) (Stream, error) {
	env := map[string]string{}
	if p.AgentDir != "" {
		env["PI_CODING_AGENT_DIR"] = p.AgentDir
	}
	cli := &CLI{
		Bin: "pi",
		Args: func(t, _ string) []string {
			return []string{"-p", "--mode", "json",
				"--no-context-files", "--no-extensions", "--no-mcp", "--no-session", t}
		},
		Env:   env,
		Parse: parsePiLine,
	}
	return cli.Start(ctx, prompt, cwd)
}

func parsePiLine(line []byte, emit func(Event)) {
	var e struct {
		Type     string `json:"type"`
		ToolName string `json:"toolName"`
		Message  struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
		Args map[string]any `json:"args"`
	}
	if json.Unmarshal(line, &e) != nil {
		return
	}
	switch e.Type {
	case "tool_execution_start":
		label, _ := e.Args["command"].(string)
		if label == "" {
			if p, _ := e.Args["path"].(string); p != "" {
				label = e.ToolName + " " + p
			} else {
				label = e.ToolName
			}
		}
		emit(Event{Kind: "tool_call", ToolID: e.ToolName, Text: label, Status: "in_progress"})
	case "tool_execution_end":
		emit(Event{Kind: "tool_result", ToolID: e.ToolName, Text: e.ToolName, Status: "completed"})
	case "message_update":
		for _, c := range e.Message.Content {
			if c.Type == "text" && c.Text != "" {
				emit(Event{Kind: "text", Text: c.Text})
			}
		}
	}
}
