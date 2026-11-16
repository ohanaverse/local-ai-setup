package main

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// stubSignals replaces the signal-context seam with a plain cancellable
// context, so a test can cancel a start without installing a real handler.
func stubSignals(t *testing.T) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	old := startSignalCtx
	startSignalCtx = func() (context.Context, context.CancelFunc) { return ctx, cancel }
	t.Cleanup(func() { startSignalCtx = old; cancel() })
	return cancel
}

// TestAskYesNoDefaultsToNo verifies the confirm rule the replace prompt
// relies on: only an explicit y/yes consents; an empty line, other text, or
// EOF is a refusal. A default-yes regression here would let an accidental
// Enter stop a running model.
func TestAskYesNoDefaultsToNo(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  bool
	}{
		{"explicit y", "y\n", true},
		{"explicit yes", "YES\n", true},
		{"empty line refuses", "\n", false},
		{"other text refuses", "n\n", false},
		{"eof refuses", "", false},
	}
	for _, tc := range cases {
		ok, err := askYesNo(strings.NewReader(tc.input))
		if err != nil {
			t.Errorf("%s: err = %v, want nil", tc.name, err)
		}
		if ok != tc.want {
			t.Errorf("%s: askYesNo() = %v, want %v", tc.name, ok, tc.want)
		}
	}
}

// localmodelsEntry builds the occupant entry the engine reports, so the
// tests' expectations read as model names rather than struct literals.
func localmodelsEntry(id, name string) localmodels.Entry {
	return localmodels.Entry{ModelID: id, ModelName: name, Artifact: name, Registered: true}
}

// pipeStderr points osStderr at a pipe until the test ends and returns both
// ends: the test closes w and reads r itself.
func pipeStderr(t *testing.T) (r, w *os.File) {
	t.Helper()
	oldOut := osStderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	osStderr = w
	t.Cleanup(func() { osStderr = oldOut })
	return r, w
}

// readSome drains everything readable from r without blocking, so the
// heartbeat test can poll for output while the ticker is still running.
func readSome(t *testing.T, r *os.File) string {
	t.Helper()
	if err := r.SetReadDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	var buf [4096]byte
	n, _ := r.Read(buf[:])
	return string(buf[:n])
}

// settleCall is one call stubSettleStart recorded.
type settleCall struct {
	target lifecycle.Target
	out    io.Writer
	ctxErr error
}
