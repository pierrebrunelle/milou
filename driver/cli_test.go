package driver

import (
	"context"
	"strings"
	"testing"
	"time"
)

func shCLI(script string) *CLI {
	return &CLI{
		Bin:  "sh",
		Args: func(_, _ string) []string { return []string{"-c", script} },
	}
}

func drain(t *testing.T, st Stream) []Event {
	t.Helper()
	var evs []Event
	for e := range st.Events() {
		evs = append(evs, e)
	}
	return evs
}

func TestCLIParsesLines(t *testing.T) {
	cli := shCLI(`echo '{"type":"text","part":{"text":"hello"}}'`)
	cli.Parse = parseOpencodeLine
	st, err := cli.Start(context.Background(), "p", ".")
	if err != nil {
		t.Fatal(err)
	}
	evs := drain(t, st)
	res, _ := st.Wait()
	if res.StopReason != "end_turn" {
		t.Fatalf("stop=%q err=%q", res.StopReason, res.Error)
	}
	if len(evs) != 1 || evs[0].Text != "hello" {
		t.Fatalf("events=%v", evs)
	}
}

func TestCLIEmptyOutputEmitsTail(t *testing.T) {
	// a harness that prints noise the parser ignores must still surface output
	st, err := shCLI(`echo 'some non-json error text'`).Start(context.Background(), "p", ".")
	if err != nil {
		t.Fatal(err)
	}
	evs := drain(t, st)
	if len(evs) != 1 || evs[0].Kind != "text" || !strings.Contains(evs[0].Text, "non-json error") {
		t.Fatalf("events=%v", evs)
	}
}

func TestCLIExitErrorSurfaces(t *testing.T) {
	st, err := shCLI(`exit 3`).Start(context.Background(), "p", ".")
	if err != nil {
		t.Fatal(err)
	}
	evs := drain(t, st)
	res, _ := st.Wait()
	if res.StopReason != "error" {
		t.Fatalf("stop=%q", res.StopReason)
	}
	last := evs[len(evs)-1]
	if last.Kind != "error" {
		t.Fatalf("last event=%v", last)
	}
}

func TestCLICancelKillsGroup(t *testing.T) {
	// child spawns a grandchild in its process group; cancel must reach both
	st, err := shCLI(`sleep 60 & sleep 60`).Start(context.Background(), "p", ".")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { drain(t, st); close(done) }()
	time.Sleep(100 * time.Millisecond)
	st.Cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not terminate stream")
	}
	res, _ := st.Wait()
	if res.StopReason != "cancelled" {
		t.Fatalf("stop=%q", res.StopReason)
	}
}

func TestCLIContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	st, err := shCLI(`sleep 60`).Start(ctx, "p", ".")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { drain(t, st); close(done) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ctx cancel did not terminate stream")
	}
}

func TestParseClaudeLine(t *testing.T) {
	var evs []Event
	emit := func(e Event) { evs = append(evs, e) }
	parseClaudeLine([]byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"hi"},{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]}}`), emit)
	if len(evs) != 2 || evs[0].Kind != "text" || evs[1].Kind != "tool_call" || evs[1].Text != "Bash: ls" {
		t.Fatalf("events=%v", evs)
	}
	parseClaudeLine([]byte(`{"type":"system","subtype":"init"}`), emit)
	if len(evs) != 2 {
		t.Fatalf("system line should emit nothing: %v", evs)
	}
}

func TestParsePiLine(t *testing.T) {
	var evs []Event
	emit := func(e Event) { evs = append(evs, e) }
	parsePiLine([]byte(`{"type":"tool_execution_start","toolName":"bash","args":{"command":"ls -la"}}`), emit)
	if len(evs) != 1 || evs[0].Kind != "tool_call" || evs[0].Text != "ls -la" {
		t.Fatalf("events=%v", evs)
	}
}
