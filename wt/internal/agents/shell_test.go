package agents

import (
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// TestShellDriverRegistered asserts that "shell" is a registered agent so
// ByName("shell") returns a non-nil driver. Without this, shell-wt would
// fail with "unknown agent" instead of launching a shell.
func TestShellDriverRegistered(t *testing.T) {
	d := ByName("shell")
	if d == nil {
		t.Fatal("ByName(shell) = nil, want non-nil driver")
	}
}

// TestShellDriverBuildWithArgs asserts that when SetArgs is called with a
// command, Build execs it directly as argv (no shell wrapping). This is the
// primary shell-wt use case: shell-wt -- echo "hello world" — args stay
// intact as separate argv entries rather than being re-quoted through bash.
func TestShellDriverBuildWithArgs(t *testing.T) {
	d := &shellDriver{}
	d.SetArgs([]string{"echo", "hello world"})
	lc := d.Build(config.Model{}, false, directGateway())
	if lc.Bin != "echo" {
		t.Errorf("Bin = %q, want echo", lc.Bin)
	}
	if len(lc.Args) != 1 || lc.Args[0] != "hello world" {
		t.Fatalf("Args = %v, want [hello world]", lc.Args)
	}
}

// TestShellDriverBuildNoArgs asserts that without SetArgs, Build produces a
// plain `bash` command (interactive shell). This is the shell-wt with no
// command case.
func TestShellDriverBuildNoArgs(t *testing.T) {
	d := &shellDriver{}
	lc := d.Build(config.Model{}, false, directGateway())
	if lc.Bin != "bash" {
		t.Errorf("Bin = %q, want bash", lc.Bin)
	}
	if len(lc.Args) != 0 {
		t.Errorf("Args = %v, want empty (interactive shell)", lc.Args)
	}
}

// TestShellDriverYoloFlagEmpty asserts that shell has no yolo flag. The shell
// agent has no permission-skip concept.
func TestShellDriverYoloFlagEmpty(t *testing.T) {
	d := &shellDriver{}
	if flag := d.YoloFlag(); flag != "" {
		t.Errorf("YoloFlag() = %q, want empty string", flag)
	}
}

// TestShellDriverBuildMetacharactersLiteral asserts that shell metacharacters
// in a passthrough arg (e.g. `|`) land in argv untouched rather than being
// interpreted, since Build execs argv directly with no shell in between. This
// is the documented limitation: pipelines require an explicit
// `shell-wt -- bash -lc '...'` invocation.
func TestShellDriverBuildMetacharactersLiteral(t *testing.T) {
	d := &shellDriver{}
	d.SetArgs([]string{"echo", "a | b"})
	lc := d.Build(config.Model{}, false, directGateway())
	if lc.Bin != "echo" {
		t.Errorf("Bin = %q, want echo", lc.Bin)
	}
	if len(lc.Args) != 1 || lc.Args[0] != "a | b" {
		t.Fatalf("Args = %v, want [a | b] preserved literally", lc.Args)
	}
}

// TestShellDriverImplementsArgSetter asserts that shellDriver implements the
// ArgSetter interface. BuildLaunchCmd uses this to decide whether to pass
// args to the driver directly or append them to the command.
func TestShellDriverImplementsArgSetter(t *testing.T) {
	d := ByName("shell")
	if d == nil {
		t.Fatal("ByName(shell) = nil")
	}
	if _, ok := d.(ArgSetter); !ok {
		t.Error("shell driver does not implement ArgSetter")
	}
}
