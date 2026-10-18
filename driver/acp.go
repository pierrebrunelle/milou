package driver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ACP is a passthrough driver: milou plays the ACP *client* role to a
// downstream agent binary (claude-code-acp, gemini-cli, codex-acp). Its
// session/update notifications become driver Events; its client-side
// requests (fs, terminal, permissions) are answered locally so the whole
// adapter ecosystem plugs in without new code per harness.
type ACP struct {
	DName       string
	Bin         string
	Args        []string
	Permissions string // "allow" | "deny" (default deny)
}

func (a ACP) Name() string {
	if a.DName != "" {
		return a.DName
	}
	return "acp:" + a.Bin
}

func (a ACP) Binary() string { return a.Bin }

func (a ACP) Start(ctx context.Context, prompt, cwd string) (Stream, error) {
	cmd := exec.Command(a.Bin, a.Args...)
	cmd.Dir = cwd
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = nil // agent protocol is stdout-only; let stderr inherit
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("acp %s: %w", a.Bin, err)
	}
	s := &acpStream{
		events:  make(chan Event, 64),
		done:    make(chan Result, 1),
		cmd:     cmd,
		conn:    &acpConn{w: bufio.NewWriter(stdin), pending: map[any]chan json.RawMessage{}},
		perms:   a.Permissions,
		terms:   map[string]*termProc{},
		promptC: make(chan json.RawMessage, 1),
	}
	go s.readLoop(stdout)
	if ctx.Done() != nil {
		go func() { <-ctx.Done(); s.Cancel() }()
	}
	if err := s.handshake(ctx, cwd); err != nil {
		s.kill()
		return nil, err
	}
	go func() {
		res := s.awaitPrompt()
		s.kill()
		close(s.events)
		s.done <- res
	}()
	s.send("session/prompt", map[string]any{
		"sessionId": s.sessionID,
		"prompt":    []any{map[string]any{"type": "text", "text": prompt}},
	}, s.promptC)
	return s, nil
}

type acpStream struct {
	events    chan Event
	done      chan Result
	cmd       *exec.Cmd
	conn      *acpConn
	perms     string
	sessionID string
	promptC   chan json.RawMessage
	mu        sync.Mutex
	terms     map[string]*termProc
	killed    bool
}

func (s *acpStream) Events() <-chan Event { return s.events }

func (s *acpStream) Cancel() error {
	s.conn.notify("session/cancel", map[string]any{"sessionId": s.sessionID})
	s.kill()
	return nil
}

func (s *acpStream) Wait() (Result, error) { return <-s.done, nil }

func (s *acpStream) kill() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.killed || s.cmd.Process == nil {
		return
	}
	s.killed = true
	syscall.Kill(-s.cmd.Process.Pid, syscall.SIGTERM)
	time.AfterFunc(2*time.Second, func() { syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL) })
}

// handshake performs initialize + session/new, caching the session id.
func (s *acpStream) handshake(ctx context.Context, cwd string) error {
	reply, err := s.call(ctx, "initialize", map[string]any{
		"protocolVersion": 1,
		"clientInfo":      map[string]any{"name": "milou", "version": "0.1.0"},
		"clientCapabilities": map[string]any{
			"fs":       map[string]any{"readTextFile": true, "writeTextFile": true},
			"terminal": true,
		},
	})
	if err != nil {
		return err
	}
	var init struct {
		ProtocolVersion int `json:"protocolVersion"`
	}
	json.Unmarshal(reply, &init)
	reply, err = s.call(ctx, "session/new", map[string]any{"cwd": cwd, "mcpServers": []any{}})
	if err != nil {
		return err
	}
	var ns struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(reply, &ns); err != nil || ns.SessionID == "" {
		return fmt.Errorf("acp: bad session/new result")
	}
	s.sessionID = ns.SessionID
	return nil
}

func (s *acpStream) call(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	ch := make(chan json.RawMessage, 1)
	if err := s.send(method, params, ch); err != nil {
		return nil, err
	}
	select {
	case raw := <-ch:
		var resp struct {
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &resp) != nil {
			return nil, fmt.Errorf("acp: bad response to %s", method)
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("acp %s: %s", method, resp.Error.Message)
		}
		return resp.Result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(30 * time.Second):
		return nil, fmt.Errorf("acp %s: timeout", method)
	}
}

func (s *acpStream) send(method string, params map[string]any, ch chan json.RawMessage) error {
	return s.conn.send(method, params, ch)
}

func (s *acpStream) awaitPrompt() Result {
	raw := <-s.promptC
	var resp struct {
		Result struct {
			StopReason string `json:"stopReason"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &resp) != nil {
		return Result{StopReason: "error", Error: "bad prompt response"}
	}
	if resp.Error != nil {
		return Result{StopReason: "error", Error: resp.Error.Message}
	}
	if resp.Result.StopReason == "" {
		resp.Result.StopReason = "end_turn"
	}
	return Result{StopReason: resp.Result.StopReason}
}

// readLoop dispatches agent→client traffic: session/update notifications to
// events, fs/terminal/permission requests to local handlers, responses to
// pending calls.
func (s *acpStream) readLoop(rdr interface{ Read([]byte) (int, error) }) {
	sc := bufio.NewScanner(rdr)
	sc.Buffer(make([]byte, 1<<16), 4<<20)
	for sc.Scan() {
		var msg struct {
			ID     any             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal(sc.Bytes(), &msg) != nil {
			continue
		}
		switch {
		case msg.Method == "session/update":
			s.onUpdate(msg.Params)
		case msg.Method != "" && msg.ID != nil:
			go s.onRequest(msg.ID, msg.Method, msg.Params)
		case msg.Method == "":
			s.conn.resolve(msg.ID, sc.Bytes())
		}
	}
	// agent died or closed stdout: fail every pending call so awaitPrompt
	// and in-flight client handlers don't hang
	s.conn.failAll(`{"error":{"code":-32000,"message":"agent closed connection"}}`)
}

func (s *acpStream) onUpdate(raw json.RawMessage) {
	var p struct {
		Update struct {
			Kind    string `json:"sessionUpdate"`
			Content *struct {
				Text string `json:"text"`
			} `json:"content"`
			ToolCallID string `json:"toolCallId"`
			Title      string `json:"title"`
			Status     string `json:"status"`
		} `json:"update"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return
	}
	u := p.Update
	switch u.Kind {
	case "agent_message_chunk", "agent_thought_chunk":
		if u.Content != nil && u.Content.Text != "" {
			s.events <- Event{Kind: "text", Text: u.Content.Text}
		}
	case "tool_call":
		s.events <- Event{Kind: "tool_call", ToolID: u.ToolCallID, Text: u.Title, Status: normStatus(u.Status)}
	case "tool_call_update":
		s.events <- Event{Kind: "tool_result", ToolID: u.ToolCallID, Text: u.Title, Status: normStatus(u.Status)}
	case "plan":
		s.events <- Event{Kind: "plan", Text: u.Title}
	}
}

func normStatus(st string) string {
	switch st {
	case "completed", "failed", "in_progress", "pending":
		return st
	default:
		return "in_progress"
	}
}

// onRequest answers the agent's client-side methods locally.
func (s *acpStream) onRequest(id any, method string, raw json.RawMessage) {
	result, e := s.serveClient(method, raw)
	if e != nil {
		s.conn.respondErr(id, e.Error())
		return
	}
	s.conn.respond(id, result)
}

func (s *acpStream) serveClient(method string, raw json.RawMessage) (any, error) {
	switch method {
	case "fs/read_text_file":
		var p struct {
			Path  string `json:"path"`
			Line  int    `json:"line"`
			Limit int    `json:"limit"`
		}
		json.Unmarshal(raw, &p)
		b, err := os.ReadFile(p.Path)
		if err != nil {
			return nil, err
		}
		content := string(b)
		if p.Limit > 0 {
			lines := strings.Split(content, "\n")
			start := p.Line - 1
			if start < 0 {
				start = 0
			}
			end := start + p.Limit
			if end > len(lines) {
				end = len(lines)
			}
			if start < len(lines) {
				content = strings.Join(lines[start:end], "\n")
			} else {
				content = ""
			}
		}
		return map[string]any{"content": content}, nil
	case "fs/write_text_file":
		var p struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		json.Unmarshal(raw, &p)
		if err := os.WriteFile(p.Path, []byte(p.Content), 0o644); err != nil {
			return nil, err
		}
		return map[string]any{}, nil
	case "session/request_permission":
		return s.permission(raw)
	case "terminal/create":
		return s.termCreate(raw)
	case "terminal/output":
		return s.termOutput(raw)
	case "terminal/wait_for_exit":
		return s.termWait(raw)
	case "terminal/kill", "terminal/release":
		var p struct {
			TerminalID string `json:"terminalId"`
		}
		json.Unmarshal(raw, &p)
		if t := s.terms[p.TerminalID]; t != nil && t.cmd.Process != nil {
			syscall.Kill(-t.cmd.Process.Pid, syscall.SIGKILL)
		}
		return map[string]any{}, nil
	}
	return nil, fmt.Errorf("method not found: %s", method)
}

func (s *acpStream) permission(raw json.RawMessage) (any, error) {
	var p struct {
		Options []struct {
			ID   string `json:"optionId"`
			Kind string `json:"kind"`
		} `json:"options"`
	}
	json.Unmarshal(raw, &p)
	if s.perms != "allow" {
		return map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}, nil
	}
	pick := ""
	for _, o := range p.Options {
		if strings.Contains(o.Kind, "allow") {
			pick = o.ID
			break
		}
	}
	if pick == "" && len(p.Options) > 0 {
		pick = p.Options[0].ID
	}
	return map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": pick}}, nil
}

// terminal lifecycle -----------------------------------------------------------

type termProc struct {
	cmd *exec.Cmd
	buf *lockedBuf
}

type lockedBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (s *acpStream) termCreate(raw json.RawMessage) (any, error) {
	var p struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
		Cwd     string   `json:"cwd"`
	}
	json.Unmarshal(raw, &p)
	if p.Command == "" {
		return nil, fmt.Errorf("terminal/create: empty command")
	}
	c := exec.Command(p.Command, p.Args...)
	if p.Cwd != "" {
		c.Dir = p.Cwd
	}
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	t := &termProc{cmd: c, buf: &lockedBuf{}}
	c.Stdout = t.buf
	c.Stderr = t.buf
	if err := c.Start(); err != nil {
		return nil, err
	}
	go c.Wait()
	s.mu.Lock()
	id := fmt.Sprintf("t%d", len(s.terms)+1)
	s.terms[id] = t
	s.mu.Unlock()
	return map[string]any{"terminalId": id}, nil
}

func (s *acpStream) termOutput(raw json.RawMessage) (any, error) {
	var p struct {
		TerminalID string `json:"terminalId"`
	}
	json.Unmarshal(raw, &p)
	t := s.terms[p.TerminalID]
	if t == nil {
		return nil, fmt.Errorf("unknown terminal: %s", p.TerminalID)
	}
	out := map[string]any{"output": t.buf.String(), "truncated": false}
	if ps := t.cmd.ProcessState; ps != nil {
		out["exitStatus"] = map[string]any{"exitCode": ps.ExitCode()}
	}
	return out, nil
}

func (s *acpStream) termWait(raw json.RawMessage) (any, error) {
	var p struct {
		TerminalID string `json:"terminalId"`
	}
	json.Unmarshal(raw, &p)
	t := s.terms[p.TerminalID]
	if t == nil {
		return nil, fmt.Errorf("unknown terminal: %s", p.TerminalID)
	}
	for t.cmd.ProcessState == nil {
		time.Sleep(50 * time.Millisecond)
	}
	return map[string]any{"exitCode": t.cmd.ProcessState.ExitCode()}, nil
}

// acpConn serializes outgoing JSON-RPC and resolves pending calls by id.
type acpConn struct {
	wmu     sync.Mutex
	w       *bufio.Writer
	pmu     sync.Mutex
	pending map[any]chan json.RawMessage
	nextID  int
}

func (c *acpConn) send(method string, params map[string]any, ch chan json.RawMessage) error {
	c.pmu.Lock()
	c.nextID++
	id := c.nextID
	if ch != nil {
		c.pending[any(id)] = ch
	}
	c.pmu.Unlock()
	return c.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
}

func (c *acpConn) notify(method string, params map[string]any) error {
	return c.write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (c *acpConn) respond(id any, result any) {
	c.write(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (c *acpConn) respondErr(id any, msg string) {
	c.write(map[string]any{"jsonrpc": "2.0", "id": id,
		"error": map[string]any{"code": -32603, "message": msg}})
}

func (c *acpConn) failAll(raw string) {
	c.pmu.Lock()
	chans := c.pending
	c.pending = map[any]chan json.RawMessage{}
	c.pmu.Unlock()
	for _, ch := range chans {
		ch <- json.RawMessage(raw)
	}
}

func (c *acpConn) resolve(id any, raw []byte) {
	key := id
	if f, ok := id.(float64); ok {
		key = int(f)
	}
	c.pmu.Lock()
	ch := c.pending[key]
	delete(c.pending, key)
	c.pmu.Unlock()
	if ch != nil {
		b := make([]byte, len(raw))
		copy(b, raw)
		ch <- b
	}
}

func (c *acpConn) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if _, err := c.w.Write(b); err != nil {
		return err
	}
	c.w.WriteByte('\n')
	return c.w.Flush()
}
