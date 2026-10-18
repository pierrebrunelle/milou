// Package acp implements the Agent Client Protocol wire types and framing.
// ACP runs JSON-RPC 2.0 as newline-delimited JSON over stdio.
package acp

// JSON-RPC 2.0 envelope.

type Request struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      any            `json:"id,omitempty"`
	Method  string         `json:"method"`
	Params  map[string]any `json:"params,omitempty"`
}

type Response struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id"`
	Result  any    `json:"result,omitempty"`
	Error   *Error `json:"error,omitempty"`
}

type Notification struct {
	JSONRPC string         `json:"jsonrpc"`
	Method  string         `json:"method"`
	Params  map[string]any `json:"params,omitempty"`
}

type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Protocol methods the agent (milou) implements.
const (
	MInitialize     = "initialize"
	MAuthenticate   = "authenticate"
	MSessionNew     = "session/new"
	MSessionLoad    = "session/load"
	MSessionPrompt  = "session/prompt"
	MSessionCancel  = "session/cancel"
	MSessionSetMode = "session/set_mode"
)

// Notifications milou emits.
const (
	NSessionUpdate = "session/update"
)

// Initialize negotiation.

type AgentCapabilities struct {
	LoadSession        bool            `json:"loadSession"`
	PromptCapabilities PromptCaps      `json:"promptCapabilities"`
	McpCapabilities    map[string]bool `json:"mcpCapabilities"`
}

type PromptCaps struct {
	Audio           bool `json:"audio"`
	EmbeddedContext bool `json:"embeddedContext"`
	Image           bool `json:"image"`
}

type InitializeResult struct {
	ProtocolVersion   int               `json:"protocolVersion"`
	AgentCapabilities AgentCapabilities `json:"agentCapabilities"`
	AgentInfo         AgentInfo         `json:"agentInfo"`
	AuthMethods       []any             `json:"authMethods"`
}

type AgentInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Sessions.

type NewSessionParams struct {
	Cwd        string `json:"cwd"`
	McpServers []any  `json:"mcpServers"`
}

type NewSessionResult struct {
	SessionID string     `json:"sessionId"`
	Modes     *ModeState `json:"modes,omitempty"`
}

type ModeState struct {
	AvailableModes []Mode `json:"availableModes"`
	CurrentModeID  string `json:"currentModeId"`
}

type Mode struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type SetModeParams struct {
	SessionID string `json:"sessionId"`
	ModeID    string `json:"modeId"`
}

type PromptParams struct {
	SessionID string         `json:"sessionId"`
	Prompt    []ContentBlock `json:"prompt"`
}

type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type PromptResult struct {
	StopReason string `json:"stopReason"`
}
