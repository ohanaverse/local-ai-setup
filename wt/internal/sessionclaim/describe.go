package sessionclaim

import (
	"fmt"
	"strings"
	"time"
)

// Describe names one other session for the shared-root menu and the no-terminal
// refusal: its agent, pid, how long it has run at now, and its branch.
func Describe(c Claim, now time.Time) string {
	running := now.Sub(time.Unix(c.Started, 0)).Round(time.Second)
	return fmt.Sprintf("%s (pid %d, running %s, started on %s)", c.Agent, c.PID, running, c.Branch)
}

// Menu is the shared-root question both launch paths show: the root, one line
// per other session and the three keys, each line ending in a newline. The
// plain terminal prints it after "wt: " and asks for a key below it; the
// picker draws it as its screen.
func Menu(root string, others []Claim, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "the repository root %s is in use by another session:\n", root)
	for _, o := range others {
		fmt.Fprintf(&b, "  %s\n", Describe(o, now))
	}
	b.WriteString("  c  continue here    w  new worktree    q  quit (Esc)\n")
	return b.String()
}

// CheckFailed is the note a launch prints when the claims in root cannot be
// read or written. It never stops the launch.
func CheckFailed(root string, err error) string {
	return fmt.Sprintf("could not check other sessions in %s (%v); launching anyway", root, err)
}

// NewWorktreeName is the branch the "new worktree" choice creates for process
// pid at now. It is never a default branch's name.
func NewWorktreeName(pid int, now time.Time) string {
	return fmt.Sprintf("session-%d-%s", pid, now.Format("150405"))
}
