# milou

**Bring-your-own-harness.** One binary that sits between any ACP client (Zed,
JetBrains, Neovim) and every coding agent you have — local CLIs today, cloud
agent APIs next. Pick the loop per prompt: `@pi fix this bug`, `@mojo add a
test`, `@devin refactor the module`.

The industry already lets you swap the *model* (BYOK, OpenRouter, local
endpoints). milou swaps the other layer — the *harness*: the system prompt,
tool set, and orchestration loop that makes a model an agent.

## Status

Working prototype. `initialize`, `session/new`, `session/prompt` (with
`@driver` routing), `session/set_mode`, and `session/cancel` work over stdio
NDJSON-RPC. `--serve ADDR` exposes every driver as an OpenAI-compatible
`model` (`milou/pi`, `milou/claude`) so any chat UI or SDK can pick a harness
without speaking ACP. `go test ./...` is green (19 cases).

## Adding a harness without code

Drop a JSON file in `~/.config/milou/drivers.d/`:

```json
{"name":"crush","bin":"crush","args":["run","{{prompt}}"],"format":"generic-jsonl"}
```

`{{prompt}}`/`{{cwd}}` substitute at spawn; `format` selects the line parser
(`pi-jsonl`, `opencode-jsonl`, `claude-jsonl`, `generic-jsonl`, `text`).
`format: "acp"` instead speaks the Agent Client Protocol to the binary —
milou plays client, answering the agent's `fs/*`, `terminal/*`, and
permission requests locally (`"permissions":"allow"` to auto-accept). That
makes every existing ACP adapter (`claude-code-acp`, `gemini-cli`, …) a
milou driver for free.

## Design

```
ACP client (Zed…)                    drivers
       │  JSON-RPC 2.0 / stdio              │
       ▼                                     ▼
  acp/        protocol types + framing   driver/  Driver iface → Event stream
  session/    session state, turns              ├── cli.go    subprocess base
  router/     @tags + ACP modes                 ├── mojo.go   ../mojo-max-agent
                                                ├── pi.go     pi --mode json
                                                ├── opencode.go
                                                ├── dsh.go
                                                ├── aider.go
                                                └── devin.go  POST + poll
```

Normalized `driver.Event`s map onto ACP `session/update` notifications
(`agent_message_chunk`, `tool_call`, `plan`). Harnesses expose their
capabilities differently; unknown event types degrade to text chunks.

## Drivers

| name | mechanism | status |
| --- | --- | --- |
| echo | in-process | works |
| mojo | `agent --repo DIR` subprocess | works |
| pi | `pi -p --mode json` | works |
| claude | `claude -p --output-format stream-json` | descriptor |
| opencode | `opencode run --format json` | descriptor |
| dsh | `dsh --profile headless --json` | descriptor |
| aider | `uv tool run --from aider-chat` | descriptor |
| codex | `codex exec --json` | descriptor (not installed) |
| gemini | `gemini -p` | descriptor (not installed) |
| crush, goose | CLIs | descriptors (not installed) |
| claude-acp | `claude-code-acp` via ACP passthrough | descriptor |
| devin | `api.devin.ai/v1/sessions` + poll | planned (needs `DEVIN_API_KEY`) |

## Try it

```bash
go build -o milou ./cmd/milou
milou --check        # registered drivers + binary probe
milou --scan         # which known harnesses are on PATH
milou --scan --write # emit drivers.d descriptors for what was found
milou --test pi      # one canned prompt through a driver, prints events
```

Register in Zed `settings.json`:

```json
"agent_servers": {
  "milou": { "command": "/path/to/milou", "args": [] }
}
```

Then in the agent panel: `@pi <task>`, or pick the harness from the mode
selector (each driver is an ACP mode).

## Any client, not just ACP

`milou --serve 127.0.0.1:9000` starts an OpenAI-compatible endpoint. Drivers
become models — point any OpenAI-speaking tool (Open WebUI, Cline, an SDK,
curl) at it and select the harness by name:

```bash
curl http://127.0.0.1:9000/v1/chat/completions -d '{
  "model": "milou/pi",
  "stream": true,
  "messages": [{"role":"user","content":"fix the bug in calc.py"}]
}'
```

`model: "milou"` alone uses the default driver, and a leading `@name` tag in
the message still overrides it.

## Relationship to mojo-max-agent

milou reuses the adapter contract proven in `mojo-max-agent/bench/` (spawn →
drain events → kill-group) and consumes its benchmark output: per-harness
pass/latency/RSS rows are the evidence the future auto-router routes on.
mojo-max-agent remains the local harness competitor — smallest footprint in
the fleet; milou is the neutral dispatch layer above all of them.
