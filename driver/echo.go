package driver

import (
	"context"
	"fmt"
)

// Echo is the smoke-test driver: emits one text event and ends the turn.
type Echo struct{}

func (Echo) Name() string { return "echo" }

func (Echo) Start(_ context.Context, prompt, cwd string) (Stream, error) {
	ch := make(chan Event, 2)
	s := &chanStream{ch: ch, done: make(chan Result, 1)}
	ch <- Event{Kind: "text", Text: fmt.Sprintf("[echo] %s", prompt)}
	s.done <- Result{StopReason: "end_turn"}
	return s, nil
}

type chanStream struct {
	ch   chan Event
	done chan Result
}

func (s *chanStream) Events() <-chan Event  { return s.ch }
func (s *chanStream) Cancel() error         { return nil }
func (s *chanStream) Wait() (Result, error) { r := <-s.done; return r, nil }
