package driver

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Descriptor declares a harness driver without Go code: which binary, which
// args (with {{prompt}}/{{cwd}} placeholders), which line-format parser.
// Drop a JSON file in ~/.config/milou/drivers.d/ to add a harness.
type Descriptor struct {
	Name        string            `json:"name"`
	Bin         string            `json:"bin"`
	Args        []string          `json:"args"`
	Env         map[string]string `json:"env"`
	Format      string            `json:"format"`      // pi-jsonl | opencode-jsonl | claude-jsonl | generic-jsonl | text | acp
	Permissions string            `json:"permissions"` // acp format only: "allow" | "deny" (default deny)
}

var formats = map[string]func([]byte, func(Event)){
	"pi-jsonl":       parsePiLine,
	"opencode-jsonl": parseOpencodeLine,
	"claude-jsonl":   parseClaudeLine,
	"dsh-jsonl":      parseGenericJSONL,
	"generic-jsonl":  parseGenericJSONL,
	"text":           parseText,
}

func (d *Descriptor) Build() *CLI {
	return &CLI{
		Bin:   d.Bin,
		Env:   d.Env,
		Parse: formats[d.Format],
		Args: func(prompt, cwd string) []string {
			out := make([]string, len(d.Args))
			for i, a := range d.Args {
				a = strings.ReplaceAll(a, "{{prompt}}", prompt)
				a = strings.ReplaceAll(a, "{{cwd}}", cwd)
				out[i] = a
			}
			return out
		},
	}
}

// descriptorDriver adapts a Descriptor to the Driver interface.
type descriptorDriver struct{ d *Descriptor }

func (dd descriptorDriver) Name() string   { return dd.d.Name }
func (dd descriptorDriver) Binary() string { return dd.d.Bin }
func (dd descriptorDriver) Start(ctx context.Context, prompt, cwd string) (Stream, error) {
	return dd.d.Build().Start(ctx, prompt, cwd)
}

// BuiltinDescriptors ships descriptors for harnesses that need no special
// Go logic. Go drivers (Mojo, Pi, Echo) still win on name collision unless a
// user descriptor shadows them.
func BuiltinDescriptors() []*Descriptor {
	return []*Descriptor{
		{Name: "claude", Bin: "claude", Format: "claude-jsonl",
			Args: []string{"-p", "{{prompt}}", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions"}},
		{Name: "opencode", Bin: "opencode", Format: "opencode-jsonl",
			Args: []string{"run", "--format", "json", "{{prompt}}"}},
		{Name: "dsh", Bin: "dsh", Format: "dsh-jsonl",
			Args: []string{"--profile", "headless", "--json", "{{prompt}}"}},
		{Name: "aider", Bin: "uv", Format: "text",
			Args: []string{"tool", "run", "--python", "3.12", "--from", "aider-chat", "aider",
				"--yes", "--no-git", "--no-show-model-warnings", "--no-pretty", "--message", "{{prompt}}"}},
		{Name: "codex", Bin: "codex", Format: "generic-jsonl",
			Args: []string{"exec", "--json", "{{prompt}}"}},
		{Name: "gemini", Bin: "gemini", Format: "text",
			Args: []string{"-p", "{{prompt}}"}},
		{Name: "crush", Bin: "crush", Format: "generic-jsonl",
			Args: []string{"run", "{{prompt}}"}},
		{Name: "goose", Bin: "goose", Format: "text",
			Args: []string{"run", "-t", "{{prompt}}"}},
		// ACP passthroughs: existing editor adapters become drivers for free.
		{Name: "claude-acp", Bin: "claude-code-acp", Format: "acp",
			Permissions: "allow"},
	}
}

// ConfigDir is ~/.config/milou (or $XDG_CONFIG_HOME/milou).
func ConfigDir() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "milou")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "milou")
}

// LoadDescriptors merges builtin descriptors with user drop-ins from
// $MILOU_DRIVERS_DIR and ~/.config/milou/drivers.d/*.json. Later sources
// override earlier ones on name collision.
func LoadDescriptors() []*Descriptor {
	descs := map[string]*Descriptor{}
	var order []string
	add := func(d *Descriptor) {
		if _, ok := descs[d.Name]; !ok {
			order = append(order, d.Name)
		}
		descs[d.Name] = d
	}
	for _, d := range BuiltinDescriptors() {
		add(d)
	}
	for _, dir := range []string{filepath.Join(ConfigDir(), "drivers.d"), os.Getenv("MILOU_DRIVERS_DIR")} {
		if dir == "" {
			continue
		}
		files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
		sort.Strings(files)
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			var d Descriptor
			if json.Unmarshal(b, &d) != nil || d.Name == "" || d.Bin == "" {
				continue
			}
			add(&d)
		}
	}
	out := make([]*Descriptor, 0, len(order))
	for _, n := range order {
		out = append(out, descs[n])
	}
	return out
}

// named pairs a Driver with its declared name for registry bookkeeping.
type named struct {
	name string
	drv  Driver
}

// Registry returns every usable driver: Go drivers (mojo, pi, echo) first,
// then descriptor-built CLIs. A user descriptor may shadow a Go driver by
// reusing its name.
func Registry() []Driver {
	var out []named
	seen := map[string]bool{}
	add := func(n string, d Driver) {
		if n == "" || seen[n] {
			return
		}
		seen[n] = true
		out = append(out, named{n, d})
	}
	descs := LoadDescriptors()
	shadows := map[string]bool{}
	for _, d := range descs {
		shadows[d.Name] = true
	}
	for _, d := range []Driver{Mojo{}, Pi{}, Echo{}} {
		if !shadows[d.Name()] {
			add(d.Name(), d)
		}
	}
	for _, d := range descs {
		if d.Format == "acp" {
			add(d.Name, ACP{DName: d.Name, Bin: d.Bin, Args: d.Args, Permissions: d.Permissions})
			continue
		}
		if _, ok := formats[d.Format]; !ok {
			continue
		}
		add(d.Name, descriptorDriver{d})
	}
	drivers := make([]Driver, 0, len(out))
	for _, n := range out {
		drivers = append(drivers, n.drv)
	}
	return drivers
}

// Scan reports which known harness binaries exist on PATH.
func Scan() []struct{ Name, Bin, Path string } {
	var rows []struct{ Name, Bin, Path string }
	seen := map[string]bool{}
	probes := []struct{ Name, Bin string }{{"mojo", "agent"}, {"pi", "pi"}, {"echo", ""}}
	for _, d := range BuiltinDescriptors() {
		probes = append(probes, struct{ Name, Bin string }{d.Name, d.Bin})
	}
	for _, p := range probes {
		if p.Bin == "" || seen[p.Name] {
			continue
		}
		seen[p.Name] = true
		path, err := exec.LookPath(p.Bin)
		if err != nil {
			path = ""
		}
		rows = append(rows, struct{ Name, Bin, Path string }{p.Name, p.Bin, path})
	}
	return rows
}

// WriteDescriptors emits drivers.d entries for harnesses found on PATH.
func WriteDescriptors() ([]string, error) {
	dir := filepath.Join(ConfigDir(), "drivers.d")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	var wrote []string
	for _, d := range BuiltinDescriptors() {
		if _, err := exec.LookPath(d.Bin); err != nil {
			continue
		}
		b, _ := json.MarshalIndent(d, "", "  ")
		p := filepath.Join(dir, d.Name+".json")
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return wrote, err
		}
		wrote = append(wrote, p)
	}
	return wrote, nil
}
