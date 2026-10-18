// Package session maps ACP session methods onto driver turns.
package session

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/pierrebrunelle/milou/acp"
	"github.com/pierrebrunelle/milou/driver"
	"github.com/pierrebrunelle/milou/router"
)

type Manager struct {
	mu       sync.Mutex
	drivers  map[string]driver.Driver
	order    []string // stable mode ordering
	fallback string
	sessions map[string]*Session
	nextID   int
}

type Session struct {
	ID     string
	Cwd    string
	Mode   string        // current driver name
	stream driver.Stream // in-flight turn, cancel target
}

func NewManager(fallback string, drivers ...driver.Driver) *Manager {
	m := &Manager{fallback: fallback, sessions: map[string]*Session{}, drivers: map[string]driver.Driver{}}
	for _, d := range drivers {
		m.drivers[d.Name()] = d
		m.order = append(m.order, d.Name())
	}
	return m
}

func (m *Manager) modes() *acp.ModeState {
	modes := make([]acp.Mode, 0, len(m.order))
	for _, name := range m.order {
		modes = append(modes, acp.Mode{ID: name, Name: name})
	}
	return &acp.ModeState{AvailableModes: modes, CurrentModeID: m.fallback}
}

func (m *Manager) get(id string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[id]
}

// Handle implements acp.Handler for requests.
func (m *Manager) Handle(s *acp.Server, req *acp.Request) (any, *acp.Error) {
	switch req.Method {
	case acp.MInitialize:
		return acp.InitializeResult{
			ProtocolVersion: 1,
			AgentCapabilities: acp.AgentCapabilities{
				PromptCapabilities: acp.PromptCaps{},
			},
			AgentInfo:   acp.AgentInfo{Name: "milou", Version: "0.1.0"},
			AuthMethods: []any{},
		}, nil

	case acp.MSessionNew:
		var p acp.NewSessionParams
		if err := remarshal(req.Params, &p); err != nil {
			return nil, &acp.Error{Code: -32602, Message: "invalid params"}
		}
		m.mu.Lock()
		m.nextID++
		id := fmt.Sprintf("s%d", m.nextID)
		m.sessions[id] = &Session{ID: id, Cwd: p.Cwd, Mode: m.fallback}
		m.mu.Unlock()
		return acp.NewSessionResult{SessionID: id, Modes: m.modes()}, nil

	case acp.MSessionSetMode:
		var p acp.SetModeParams
		if err := remarshal(req.Params, &p); err != nil {
			return nil, &acp.Error{Code: -32602, Message: "invalid params"}
		}
		sess := m.get(p.SessionID)
		if sess == nil {
			return nil, &acp.Error{Code: -32602, Message: "unknown session"}
		}
		if _, ok := m.drivers[p.ModeID]; !ok {
			return nil, &acp.Error{Code: -32602, Message: "unknown driver: " + p.ModeID}
		}
		sess.Mode = p.ModeID
		return map[string]any{}, nil

	case acp.MSessionPrompt:
		var p acp.PromptParams
		if err := remarshal(req.Params, &p); err != nil {
			return nil, &acp.Error{Code: -32602, Message: "invalid params"}
		}
		return m.prompt(s, p)

	default:
		return nil, &acp.Error{Code: -32601, Message: "method not found: " + req.Method}
	}
}

func (m *Manager) prompt(s *acp.Server, p acp.PromptParams) (any, *acp.Error) {
	sess := m.get(p.SessionID)
	if sess == nil {
		return nil, &acp.Error{Code: -32602, Message: "unknown session"}
	}
	text := ""
	for _, b := range p.Prompt {
		if b.Type == "text" {
			text += b.Text
		}
	}
	tag, clean := router.ParsePrompt(text)
	name := sess.Mode
	if tag != "" {
		if _, ok := m.drivers[tag]; !ok {
			return nil, &acp.Error{Code: -32602, Message: "unknown driver: @" + tag}
		}
		name = tag
	}
	d := m.drivers[name]
	if d == nil {
		return nil, &acp.Error{Code: -32603, Message: "no driver: " + name}
	}
	st, err := d.Start(context.Background(), clean, sess.Cwd)
	if err != nil {
		return nil, &acp.Error{Code: -32603, Message: err.Error()}
	}
	sess.stream = st
	for ev := range st.Events() {
		m.emit(s, p.SessionID, ev)
	}
	res, _ := st.Wait()
	sess.stream = nil
	return acp.PromptResult{StopReason: res.StopReason}, nil
}

func (m *Manager) emit(s *acp.Server, sid string, ev driver.Event) {
	switch ev.Kind {
	case "text":
		s.MessageChunk(sid, ev.Text)
	case "tool_call":
		s.ToolCall(sid, ev.ToolID, ev.Text, ev.Status, ev.Payload)
	case "tool_result":
		s.ToolCall(sid, ev.ToolID, ev.Text, ev.Status, ev.Payload)
	case "error":
		s.MessageChunk(sid, "[error] "+ev.Text)
	}
}

// HandleNotify implements acp.Handler for notifications.
func (m *Manager) HandleNotify(_ *acp.Server, n *acp.Notification) {
	if n.Method == acp.MSessionCancel {
		if id, _ := n.Params["sessionId"].(string); id != "" {
			if sess := m.get(id); sess != nil && sess.stream != nil {
				sess.stream.Cancel()
			}
		}
	}
}

func remarshal(in, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}
