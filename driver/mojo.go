package driver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Mojo drives the mojo-max-agent binary: ./agent --repo DIR TASK. It speaks a
// text-command protocol to an OpenAI-compatible endpoint (MAX_BASE_URL,
// MAX_MODEL from milou's env). Binary resolution: MILOU_MOJO_BIN > PATH.
type Mojo struct{ Bin string }

func (Mojo) Name() string { return "mojo" }

func (m Mojo) Start(ctx context.Context, prompt, cwd string) (Stream, error) {
	bin := m.Bin
	if bin == "" {
		bin = os.Getenv("MILOU_MOJO_BIN")
	}
	if bin == "" {
		var err error
		bin, err = findBin("agent")
		if err != nil {
			return nil, err
		}
	}
	transcript := filepath.Join(os.TempDir(), "milou-mojo-transcript.json")
	cli := &CLI{
		Bin: bin,
		Args: func(p, _ string) []string {
			return []string{"--repo", cwd, "--transcript", transcript,
				"--max-requests", "40", "-v", p}
		},
		Parse: parseMojoLine,
		Final: mojoFinal(transcript),
	}
	return cli.Start(ctx, prompt, cwd)
}

var mojoCmdLine = regexp.MustCompile(`^\$ (.*)$`)

func parseMojoLine(line []byte, emit func(Event)) {
	s := string(line)
	switch {
	case strings.HasPrefix(s, "[cmd] "), strings.HasPrefix(s, "$ "):
		cmd := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(s, "[cmd] "), "$ "))
		emit(Event{Kind: "tool_call", ToolID: "bash", Text: cmd, Status: "in_progress"})
	case strings.HasPrefix(s, "[agent]"), strings.HasPrefix(s, "[tool]"):
		emit(Event{Kind: "text", Text: s})
	}
}

// mojoFinal emits the transcript's tool-call history once the process exits:
// mojo writes transcript.json only at termination.
func mojoFinal(path string) func([]byte, func(Event)) {
	return func(_ []byte, emit func(Event)) {
		b, err := os.ReadFile(path)
		if err != nil {
			return
		}
		var msgs []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}
		if json.Unmarshal(b, &msgs) != nil {
			return
		}
		n := 0
		for _, msg := range msgs {
			if msg.Role != "assistant" || !strings.Contains(msg.Content, "<tool_call>") {
				continue
			}
			n++
			emit(Event{Kind: "tool_call", ToolID: "bash", Text: summarizeCall(msg.Content), Status: "completed"})
		}
	}
}

var argRe = regexp.MustCompile(`"command"\s*:\s*"([^"]*)"`)

func summarizeCall(content string) string {
	if m := argRe.FindStringSubmatch(content); m != nil {
		return m[1]
	}
	return content
}

func findBin(name string) (string, error) {
	for _, d := range filepath.SplitList(os.Getenv("PATH")) {
		p := filepath.Join(d, name)
		if st, err := os.Stat(p); err == nil && st.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", os.ErrNotExist
}
