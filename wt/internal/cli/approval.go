// Package cli holds the command-line helpers both wt binaries use: approval
// refusals, y/N questions on the terminal, the plain table. A session-safe
// leaf: it imports exitcode and the terminal libraries only (x/term, and
// lipgloss for the plain table's widths).
package cli

import (
	"errors"
	"fmt"

	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
)

// ExitApprovalMissing is C9's code 3: the command needed consent it did not
// get (no --yes and no terminal, or the prompt was declined) and changed
// nothing.
const ExitApprovalMissing = 3

// ErrNoTerminal is what a y/N prompt returns when there is no terminal to ask
// on. The command that asked turns it into ApprovalMissing, naming its own
// flag and what it did not do.
var ErrNoTerminal = errors.New("there is no terminal to ask")

// ApprovalMissing is P4's refusal, exit 3: "approval missing: pass <flag>
// (there is no terminal to ask); <state>", where state says what did not
// happen ("nothing was stopped").
func ApprovalMissing(flag, state string) error {
	return exitcode.New(ExitApprovalMissing, fmt.Errorf("approval missing: pass %s (%s); %s", flag, ErrNoTerminal.Error(), state), false)
}

// NoTerminalRefusal turns a prompt's ErrNoTerminal into ApprovalMissing(flag,
// state) and passes every other error through.
func NoTerminalRefusal(err error, flag, state string) error {
	if errors.Is(err, ErrNoTerminal) {
		return ApprovalMissing(flag, state)
	}
	return err
}

// Declined is an answered "no": exit 3, like a missing approval, on one line
// "approval missing: declined; <state>", where state says what did not happen
// or what is still true ("nothing was stopped", "omlx/x still running").
func Declined(state string) error {
	return exitcode.New(ExitApprovalMissing, errors.New("approval missing: declined; "+state), false)
}
