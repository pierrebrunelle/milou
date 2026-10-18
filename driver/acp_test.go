package driver

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeACPAgent is a minimal ACP-speaking agent: answers initialize/session/new,
// emits session/update notifications, makes one fs/read_text_file and one
// permission request, then returns the prompt result.
const fakeACPAgent = `import sys, json

def send(m):
    sys.stdout.write(json.dumps(m) + "\n")
    sys.stdout.flush()

sid = "s1"
for line in sys.stdin:
    msg = json.loads(line)
    mid, meth = msg.get("id"), msg.get("method")
    if meth == "initialize":
        send({"jsonrpc":"2.0","id":mid,"result":{"protocolVersion":1,"agentCapabilities":{}}})
    elif meth == "session/new":
        send({"jsonrpc":"2.0","id":mid,"result":{"sessionId":sid}})
    elif meth == "session/prompt":
        prompt_id = mid
        send({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":sid,
            "update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"working on it"}}}})
        send({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":sid,
            "update":{"sessionUpdate":"tool_call","toolCallId":"tc1","title":"cat f.txt","status":"in_progress"}}})
        send({"jsonrpc":"2.0","id":99,"method":"fs/read_text_file","params":{"sessionId":sid,"path":"REQPATH"}})
    elif mid == 99 and "result" in msg:
        content = msg["result"]["content"]
        send({"jsonrpc":"2.0","id":100,"method":"session/request_permission","params":{"sessionId":sid,
            "options":[{"optionId":"ok","kind":"allow_once"},{"optionId":"no","kind":"reject_once"}]}})
        pending_content = content
    elif mid == 100 and "result" in msg:
        send({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":sid,
            "update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"READ:"+pending_content+" PERM:"+json.dumps(msg["result"])}}}})
        send({"jsonrpc":"2.0","id":prompt_id,"result":{"stopReason":"end_turn"}})
    elif meth == "session/cancel":
        pass
`

func TestACPPassthrough(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "f.txt")
	os.WriteFile(target, []byte("file-contents-42"), 0o644)
	agent := filepath.Join(dir, "agent.py")
	src := strings.ReplaceAll(fakeACPAgent, "REQPATH", target)
	os.WriteFile(agent, []byte(src), 0o644)

	a := ACP{DName: "fake-acp", Bin: "python3", Args: []string{agent}, Permissions: "allow"}
	st, err := a.Start(context.Background(), "do it", dir)
	if err != nil {
		t.Fatal(err)
	}
	var texts, tools []string
	for ev := range st.Events() {
		switch ev.Kind {
		case "text":
			texts = append(texts, ev.Text)
		case "tool_call":
			tools = append(tools, ev.Text)
		}
	}
	res, _ := st.Wait()
	if res.StopReason != "end_turn" {
		t.Fatalf("stop=%q err=%q", res.StopReason, res.Error)
	}
	if len(tools) != 1 || tools[0] != "cat f.txt" {
		t.Fatalf("tools=%v", tools)
	}
	joined := strings.Join(texts, "|")
	if !strings.Contains(joined, "working on it") {
		t.Fatalf("missing message chunk: %v", texts)
	}
	if !strings.Contains(joined, "READ:file-contents-42") {
		t.Fatalf("fs/read_text_file not served: %v", texts)
	}
	if !strings.Contains(joined, `"outcome": "selected"`) || !strings.Contains(joined, `"optionId": "ok"`) {
		t.Fatalf("permission not answered with allow: %v", texts)
	}
}

func TestACPPermissionDeniedByDefault(t *testing.T) {
	dir := t.TempDir()
	agent := filepath.Join(dir, "agent.py")
	os.WriteFile(agent, []byte(`import sys, json
def send(m): sys.stdout.write(json.dumps(m)+"\n"); sys.stdout.flush()
for line in sys.stdin:
    msg = json.loads(line)
    if msg.get("method") == "initialize":
        send({"jsonrpc":"2.0","id":msg["id"],"result":{"protocolVersion":1}})
    elif msg.get("method") == "session/new":
        send({"jsonrpc":"2.0","id":msg["id"],"result":{"sessionId":"s"}})
    elif msg.get("method") == "session/prompt":
        pid = msg["id"]
        send({"jsonrpc":"2.0","id":7,"method":"session/request_permission",
              "params":{"sessionId":"s","options":[{"optionId":"ok","kind":"allow_once"}]}})
    elif msg.get("id") == 7:
        send({"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s",
              "update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"PERM:"+json.dumps(msg["result"])}}}})
        send({"jsonrpc":"2.0","id":pid,"result":{"stopReason":"end_turn"}})
`), 0o644)
	a := ACP{DName: "deny-acp", Bin: "python3", Args: []string{agent}}
	st, err := a.Start(context.Background(), "x", dir)
	if err != nil {
		t.Fatal(err)
	}
	var joined string
	for ev := range st.Events() {
		joined += ev.Text
	}
	res, _ := st.Wait()
	if res.StopReason != "end_turn" || !strings.Contains(joined, `"outcome": "cancelled"`) {
		t.Fatalf("deny-by-default broken: stop=%q events=%q", res.StopReason, joined)
	}
}

func TestACPAgentDeathFailsWait(t *testing.T) {
	dir := t.TempDir()
	agent := filepath.Join(dir, "die.py")
	os.WriteFile(agent, []byte("import sys; sys.exit(1)\n"), 0o644)
	a := ACP{DName: "die", Bin: "python3", Args: []string{agent}}
	_, err := a.Start(context.Background(), "x", dir)
	if err == nil {
		t.Fatal("expected handshake failure on dead agent")
	}
}

func TestACPTimeout(t *testing.T) {
	dir := t.TempDir()
	agent := filepath.Join(dir, "sleep.py")
	// accepts initialize, then never answers session/new: call() must time out,
	// and Start must return an error instead of hanging forever
	os.WriteFile(agent, []byte(`import sys, json, time
for line in sys.stdin:
    msg = json.loads(line)
    if msg.get("method") == "initialize":
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":msg["id"],"result":{"protocolVersion":1}})+"\n")
        sys.stdout.flush()
    else:
        time.sleep(60)
`), 0o644)
	a := ACP{DName: "slow", Bin: "python3", Args: []string{agent}}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_, err := a.Start(ctx, "x", dir)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}
