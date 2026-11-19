package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunModelSetupChildRunsSetupAndReturnsItsExit pins the real child seam:
// it runs the program it is given with the one argument "setup" and returns
// the child's failure as its error. Why: the Mode tab hands wt only a path,
// and a failed setup must reach the gate's warning and wt config's error.
func TestRunModelSetupChildRunsSetupAndReturnsItsExit(t *testing.T) {
	dir := t.TempDir()
	args := filepath.Join(dir, "args")
	prog := filepath.Join(dir, "wt-model")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + args + "'\nexit 2\n"
	if err := os.WriteFile(prog, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	err := runModelSetupChild(prog)
	var ee *exec.ExitError
	if err == nil || !strings.Contains(err.Error(), "exit status 2") {
		t.Fatalf("err = %v, want the child's exit status 2", err)
	}
	if !errors.As(err, &ee) || ee.ExitCode() != 2 {
		t.Errorf("err = %T %v, want an *exec.ExitError with code 2", err, err)
	}
	got, rerr := os.ReadFile(args)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(got) != "setup\n" {
		t.Errorf("child args = %q, want just setup", got)
	}
}
