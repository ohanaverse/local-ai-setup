package cli

import (
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"
)

// TestReadYesNoAppliesTheDefault checks that only y/yes and n/no (any case)
// override the default; an empty line, any other word and EOF keep it.
func TestReadYesNoAppliesTheDefault(t *testing.T) {
	for _, tc := range []struct {
		in       string
		def      bool
		want     bool
		describe string
	}{
		{"y\n", false, true, "y"},
		{"yes\n", false, true, "yes"},
		{"Y\n", false, true, "Y"},
		{"n\n", true, false, "n"},
		{"no\n", true, false, "no"},
		{"\n", false, false, "empty, default no"},
		{"\n", true, true, "empty, default yes"},
		{"maybe\n", true, true, "other word"},
		{"", true, true, "EOF, default yes"},
		{"", false, false, "EOF, default no"},
	} {
		got, err := ReadYesNo(strings.NewReader(tc.in), tc.def)
		if err != nil || got != tc.want {
			t.Errorf("%s: ReadYesNo(%q, %v) = (%v, %v), want (%v, nil)", tc.describe, tc.in, tc.def, got, err, tc.want)
		}
	}
}

// TestAskOnTTYWithoutATerminalReturnsTheCallersError checks that a terminal
// that cannot be opened answers no with the caller's own error, so each
// command names its own flag.
func TestAskOnTTYWithoutATerminalReturnsTheCallersError(t *testing.T) {
	noTTY := errors.New("rerun with --yes")
	open := func() (*os.File, error) { return nil, errors.New("open /dev/tty: device not configured") }
	got, err := AskOnTTY(open, "Stop it?", noTTY)
	if got || !errors.Is(err, noTTY) {
		t.Errorf("AskOnTTY = (%v, %v), want (false, %v)", got, err, noTTY)
	}
}

// TestAskOnTTYAsksOnTheFileItOpened checks that the question goes to the
// opened terminal with a [y/N] suffix and the answer is read back from it.
// A socketpair stands in for the terminal: a temp file cannot, since writing
// the question first leaves the read at EOF.
func TestAskOnTTYAsksOnTheFileItOpened(t *testing.T) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	tty, user := os.NewFile(uintptr(fds[0]), "tty"), os.NewFile(uintptr(fds[1]), "user")
	shown := make(chan string, 1)
	go func() {
		// The user reads the question, types the answer and leaves.
		buf := make([]byte, 256)
		n, _ := user.Read(buf)
		_, _ = user.WriteString("y\n")
		_ = user.Close()
		shown <- string(buf[:n])
	}()
	got, err := AskOnTTY(func() (*os.File, error) { return tty, nil }, "Stop it?", errors.New("no tty"))
	if err != nil || !got {
		t.Errorf("AskOnTTY = (%v, %v), want (true, nil)", got, err)
	}
	if q := <-shown; q != "Stop it? [y/N] " {
		t.Errorf("the question shown = %q, want %q", q, "Stop it? [y/N] ")
	}
}
