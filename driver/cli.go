package driver

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// CLI is the subprocess driver base: spawn in its own process group, stream
// parsed lines, SIGTERM the whole group on Cancel (same watchdog discipline
// as the benchmark runner).
type CLI struct {
	Bin   string // executable name or absolute path
	Args  func(prompt, cwd string) []string
	Env   map[string]string
	Parse func(line []byte, emit func(Event)) // line -> zero or more events
	Final func(log []byte, emit func(Event))  // whole log at exit (optional)
}

func (c *CLI) Binary() string { return c.Bin }

func (c *CLI) Start(ctx context.Context, prompt, cwd string) (Stream, error) {
	cmd := exec.Command(c.Bin, c.Args(prompt, cwd)...)
	cmd.Dir = cwd
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	env := os.Environ()
	for k, v := range c.Env {
		env = append(env, k+"="+v)
	}
	cmd.Env = env
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = cmd.Stdout // merged stream
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("%s: %w", c.Bin, err)
	}
	s := &cliStream{
		events: make(chan Event, 64),
		done:   make(chan Result, 1),
		cmd:    cmd,
		parse:  c.Parse,
		final:  c.Final,
	}
	go s.drain(stdout)
	if ctx.Done() != nil {
		go func() {
			<-ctx.Done()
			s.Cancel()
		}()
	}
	return s, nil
}

type cliStream struct {
	events chan Event
	done   chan Result
	cmd    *exec.Cmd
	parse  func([]byte, func(Event))
	final  func([]byte, func(Event))
	buf    []byte
}

func (s *cliStream) Events() <-chan Event { return s.events }

func (s *cliStream) Cancel() error {
	if s.cmd.Process == nil {
		return nil
	}
	// negative pid -> whole process group
	pid := s.cmd.Process.Pid
	syscall.Kill(-pid, syscall.SIGTERM)
	time.AfterFunc(3*time.Second, func() { syscall.Kill(-pid, syscall.SIGKILL) })
	return nil
}

func (s *cliStream) Wait() (Result, error) { return <-s.done, nil }

const bufCap = 4 << 20 // keep only the tail of a harness's output for Final

func (s *cliStream) drain(rdr interface{ Read([]byte) (int, error) }) {
	sc := bufio.NewScanner(rdr)
	sc.Buffer(make([]byte, 1<<16), 1<<20)
	emitted := 0
	emit := func(e Event) { emitted++; s.events <- e }
	for sc.Scan() {
		line := sc.Bytes()
		s.buf = append(s.buf, line...)
		s.buf = append(s.buf, '\n')
		if len(s.buf) > bufCap {
			s.buf = append([]byte(nil), s.buf[len(s.buf)-bufCap:]...)
		}
		if s.parse != nil {
			s.parse(line, emit)
		}
	}
	err := s.cmd.Wait()
	if s.final != nil {
		s.final(s.buf, emit)
	}
	if emitted == 0 && len(s.buf) > 0 {
		tail := s.buf
		if len(tail) > 8192 {
			tail = tail[len(tail)-8192:]
		}
		s.events <- Event{Kind: "text", Text: string(tail)}
	}
	if err != nil {
		s.events <- Event{Kind: "error", Text: err.Error()}
	}
	close(s.events)
	res := Result{StopReason: "end_turn"}
	if err != nil {
		if ws, ok := s.cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			res = Result{StopReason: "cancelled"}
		} else {
			res = Result{StopReason: "error", Error: err.Error()}
		}
	}
	s.done <- res
}
