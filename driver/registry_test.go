package driver

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDescriptorsIncludesBuiltins(t *testing.T) {
	t.Setenv("MILOU_DRIVERS_DIR", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	descs := LoadDescriptors()
	names := map[string]bool{}
	for _, d := range descs {
		names[d.Name] = true
	}
	for _, want := range []string{"claude", "opencode", "dsh", "aider", "codex"} {
		if !names[want] {
			t.Fatalf("missing builtin descriptor %q", want)
		}
	}
}

func TestUserDescriptorOverridesBuiltin(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MILOU_DRIVERS_DIR", dir)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	os.WriteFile(filepath.Join(dir, "claude.json"),
		[]byte(`{"name":"claude","bin":"/usr/bin/false","args":["{{prompt}}"],"format":"text"}`), 0o644)
	for _, d := range LoadDescriptors() {
		if d.Name == "claude" && d.Bin != "/usr/bin/false" {
			t.Fatalf("override lost: %+v", d)
		}
	}
}

func TestDescriptorArgSubstitution(t *testing.T) {
	d := &Descriptor{Bin: "b", Args: []string{"--repo", "{{cwd}}", "{{prompt}}"}, Format: "text"}
	cli := d.Build()
	got := cli.Args("fix it", "/repo")
	if got[1] != "/repo" || got[2] != "fix it" {
		t.Fatalf("args=%v", got)
	}
}

func TestRegistryHasGoDriversAndDescriptors(t *testing.T) {
	t.Setenv("MILOU_DRIVERS_DIR", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	names := map[string]bool{}
	for _, d := range Registry() {
		if names[d.Name()] {
			t.Fatalf("duplicate driver %q", d.Name())
		}
		names[d.Name()] = true
	}
	for _, want := range []string{"mojo", "pi", "echo", "claude", "opencode"} {
		if !names[want] {
			t.Fatalf("missing driver %q", want)
		}
	}
}

func TestDescriptorShadowsGoDriver(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MILOU_DRIVERS_DIR", dir)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	os.WriteFile(filepath.Join(dir, "echo.json"),
		[]byte(`{"name":"echo","bin":"/bin/echo","args":["{{prompt}}"],"format":"text"}`), 0o644)
	for _, d := range Registry() {
		if d.Name() == "echo" {
			if _, ok := d.(descriptorDriver); !ok {
				t.Fatalf("echo was not shadowed by descriptor: %T", d)
			}
			return
		}
	}
	t.Fatal("echo missing")
}
