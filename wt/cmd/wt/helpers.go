package main

import (
	"fmt"
	"os"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"github.com/charmbracelet/x/term"
	"github.com/ohanaverse/local-ai-setup/wt/internal/guard"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
	"github.com/spf13/cobra"
)

// mustGetString returns a string flag value, ignoring the error. The flag is
// always registered, so GetString cannot fail here.
func mustGetString(cmd *cobra.Command, name string) string {
	v, _ := cmd.Flags().GetString(name)
	return v
}

// yolo reports whether the --yolo flag was set.
func yolo(cmd *cobra.Command) bool {
	v, _ := cmd.Flags().GetBool("yolo")
	return v
}

// borderStyle returns the table border style for the active theme.
func borderStyle(theme themes.Theme) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Token(themes.TokenBorder))
}

// renderTable renders a simple lipgloss table from headers and rows.
// theme controls the border color.
func renderTable(headers []string, rows [][]string, theme themes.Theme) string {
	t := table.New().
		Headers(headers...).
		Rows(rows...).
		Border(lipgloss.NormalBorder()).
		BorderStyle(borderStyle(theme)).
		BorderRow(true)
	return t.Render()
}

// maybeInstallGuard installs the main guard. Callers must ensure the
// current directory is inside a git repo before invoking this helper. Errors
// are written to stderr and ignored so that a guard-install failure does not
// block the agent launch. This matches the bash engine's best-effort
// behavior. It is a package-level var so tests can stub it out — the real
// guard.Install operates on the test process's cwd, which would otherwise
// install the hook into the repo under test.
var maybeInstallGuard = func() {
	if _, err := guard.Install(); err != nil {
		fmt.Fprintf(os.Stderr, "wt: failed to auto-install main guard: %v\n", err)
	}
}

// checkGuardStatus returns the guard status in the current repo. It returns
// an error when not inside a git repo so callers can report a clear message.
func checkGuardStatus() (guard.Status, error) {
	if !worktree.IsRepo(".") {
		return guard.Err, fmt.Errorf("not inside a git repository")
	}
	return guard.Check(), nil
}

// removeGuard uninstalls the guard in the current repo. It returns an error
// when not inside a git repository.
func removeGuard() error {
	if !worktree.IsRepo(".") {
		return fmt.Errorf("not inside a git repository")
	}
	return guard.Uninstall()
}

// isStdinTTY reports whether stdin is attached to a terminal. Used to gate
// the picker paths in main.go so a non-interactive invocation (CI, cron,
// piped command) gets a clear error pointing at -A instead of Bubble Tea's
// opaque "could not open a new TTY" failure from /dev/tty.
//
// stdin (not stdout) is checked because that's the input Bubble Tea reads
// from. Piping output to a file or another program doesn't make the TUI
// unusable on its own, but `wt < /dev/null` does.
//
// term.IsTerminal does a TIOCGWINSZ/TCGETS ioctl to confirm TTY-ness; a
// plain os.FileInfo mode check would return true for /dev/null and other
// character devices, which isn't strict enough.
func isStdinTTY() bool {
	return term.IsTerminal(os.Stdin.Fd())
}

// stdinTTY is a test seam wrapping isStdinTTY. Production code uses the real
// terminal check; tests override it to control the TTY state deterministically.
var stdinTTY = isStdinTTY

// errPickerNeedsTTY is returned by the rootCmd RunE when an unpinned launch
// path would otherwise try to open the interactive picker (Bubble Tea's
// WithAltScreen opens /dev/tty, which fails opaquely outside a terminal).
// The message tells the user exactly which flag to add so their script
// works again.
var errPickerNeedsTTY = fmt.Errorf(
	"the agent/command picker needs a TTY; pass -A <agent> to launch without it " +
		"(or run wt interactively from a terminal)")

// errModelPickerNeedsTTY is returned when the model picker would be shown but
// stdin is not a TTY. Unlike errPickerNeedsTTY (agent/command picker), the
// agent is already resolved here, so the fix is to pin the model with -M.
var errModelPickerNeedsTTY = fmt.Errorf(
	"the model picker needs a TTY; pass -M <model> to launch without it " +
		"(or run wt interactively from a terminal)")

// pickerNeedsTTYError returns the TTY error for the picker that would be
// shown next: the agent/command picker when no agent is pinned, or the model
// picker when the agent is resolved but the model is not.
func pickerNeedsTTYError(agent string) error {
	if agent == "" {
		return errPickerNeedsTTY
	}
	return errModelPickerNeedsTTY
}
