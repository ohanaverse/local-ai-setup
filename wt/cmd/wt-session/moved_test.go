package main

import (
	"bytes"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
)

// TestMovedErrorIsOneLineExitOne pins the pointer for a command that moved to
// wt-model: main prints exactly one "wt: ..." line naming the new command and
// saying nothing was changed, and exits 1.
func TestMovedErrorIsOneLineExitOne(t *testing.T) {
	err := movedError("model", "model")
	var buf bytes.Buffer
	code := exitcode.Report("wt", err, &buf)
	if code != 1 || buf.String() != "wt: model moved to wt-model — run 'wt-model model'; nothing was changed\n" {
		t.Fatalf("code %d, %q", code, buf.String())
	}
}
