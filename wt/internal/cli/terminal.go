package cli

import (
	"os"

	"github.com/charmbracelet/x/term"
)

// IsStdinTTY reports whether stdin is a terminal. Stdin is the "interactive
// invocation" signal, while the prompts themselves read /dev/tty: a
// redirected stdin therefore means a script even when a terminal is attached
// (`wt start x < answers.txt` asks no question). A piped answer must never be
// able to approve a change, and a script must never hang on a prompt nobody
// answers; the command's flags are how a script proceeds.
func IsStdinTTY() bool {
	return term.IsTerminal(os.Stdin.Fd())
}

// StdoutWidth is the terminal's width in columns, or 0 when stdout is not a
// terminal (a pipe or a file has no width to fit).
func StdoutWidth() int {
	w, _, err := term.GetSize(os.Stdout.Fd())
	if err != nil {
		return 0
	}
	return w
}
