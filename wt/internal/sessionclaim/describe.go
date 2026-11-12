package sessionclaim

import (
	"fmt"
	"time"
)

// Describe names one other session in a warning line: its agent, pid, how long
// it has run at now, and its branch.
func Describe(c Claim, now time.Time) string {
	running := now.Sub(time.Unix(c.Started, 0)).Round(time.Second)
	return fmt.Sprintf("%s (pid %d, running %s, started on %s)", c.Agent, c.PID, running, c.Branch)
}

// CheckFailed is the note a launch prints when the claims in root cannot be
// read or written. It never stops the launch.
func CheckFailed(root string, err error) string {
	return fmt.Sprintf("could not check other sessions in %s (%v); launching anyway", root, err)
}
