package acp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
)

// Server reads NDJSON-RPC requests from stdin and writes responses/notifications
// to stdout. Requests dispatch on goroutines so session/cancel can interrupt an
// in-flight turn; writes are serialized by wmu.
type Server struct {
	in  io.Reader
	out *bufio.Writer
	h   Handler
	wmu sync.Mutex
}

type Handler interface {
	Handle(s *Server, req *Request) (any, *Error)
	HandleNotify(s *Server, n *Notification)
}

func NewServer(h Handler) *Server {
	return &Server{in: os.Stdin, out: bufio.NewWriter(os.Stdout), h: h}
}

func (s *Server) Run() error {
	sc := bufio.NewScanner(s.in)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var msg map[string]any
		if err := json.Unmarshal(line, &msg); err != nil {
			s.writeError(nil, -32700, "parse error")
			continue
		}
		if _, hasID := msg["id"]; hasID {
			var req Request
			if err := json.Unmarshal(line, &req); err != nil {
				s.writeError(msg["id"], -32600, "invalid request")
				continue
			}
			go func() {
				result, e := s.h.Handle(s, &req)
				if e != nil {
					s.writeError(req.ID, e.Code, e.Message)
				} else {
					s.write(Response{JSONRPC: "2.0", ID: req.ID, Result: result})
				}
			}()
		} else {
			var n Notification
			if err := json.Unmarshal(line, &n); err == nil {
				s.h.HandleNotify(s, &n)
			}
		}
	}
	return sc.Err()
}

// Notify emits a protocol notification (e.g. session/update).
func (s *Server) Notify(method string, params map[string]any) {
	s.write(Notification{JSONRPC: "2.0", Method: method, Params: params})
}

func (s *Server) write(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		fmt.Fprintf(os.Stderr, "milou: marshal error: %v\n", err)
		return
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	s.out.Write(b)
	s.out.WriteByte('\n')
	s.out.Flush()
}

func (s *Server) writeError(id any, code int, msg string) {
	s.write(Response{JSONRPC: "2.0", ID: id, Error: &Error{Code: code, Message: msg}})
}

// Update helpers -------------------------------------------------------------

// MessageChunk emits an agent_message_chunk session update.
func (s *Server) MessageChunk(sessionID, text string) {
	s.Notify(NSessionUpdate, map[string]any{
		"sessionId": sessionID,
		"update": map[string]any{
			"sessionUpdate": "agent_message_chunk",
			"content":       map[string]any{"type": "text", "text": text},
		},
	})
}

// ToolCall emits a tool_call session update for a command execution.
func (s *Server) ToolCall(sessionID, id, title, status string, output any) {
	s.Notify(NSessionUpdate, map[string]any{
		"sessionId": sessionID,
		"update": map[string]any{
			"sessionUpdate": "tool_call",
			"toolCallId":    id,
			"title":         title,
			"kind":          "execute",
			"status":        status,
			"rawOutput":     output,
		},
	})
}
