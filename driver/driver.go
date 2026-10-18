// Package driver normalizes agent harnesses behind one event stream.
// Local drivers spawn a CLI; cloud drivers create/poll a remote session.
package driver

import "context"

// Event is the normalized unit a driver emits; the session layer maps each
// kind to an acp session/update variant.
type Event struct {
	Kind    string // "text" | "tool_call" | "tool_result" | "plan" | "usage" | "error"
	Text    string // text: chunk payload; tool_call: command summary; error: message
	ToolID  string // tool_call/tool_result correlation id
	Status  string // "pending" | "in_progress" | "completed" | "failed"
	Payload map[string]any
}

// Result carries terminal state for one prompt turn.
type Result struct {
	StopReason string // "end_turn" | "cancelled" | "error"
	Error      string
}

// Driver adapts one harness to the normalized stream.
type Driver interface {
	Name() string
	// Start begins one turn: full prompt text + working dir (the repo).
	Start(ctx context.Context, prompt, cwd string) (Stream, error)
}

// Stream is one in-flight turn.
type Stream interface {
	Events() <-chan Event
	Cancel() error
	Wait() (Result, error)
}
