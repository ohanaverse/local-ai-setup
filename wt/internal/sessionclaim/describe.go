package sessionclaim

import (
	"fmt"
	"strings"
	"time"
)

// Describe names one other session in the warning or refusal text it is
// rendered into — the launch warnings' held entry and `wt restore-root`'s
// root-held refusals: its agent, pid, how long it has run at now, and its
// branch.
func Describe(c Claim, now time.Time) string {
	running := now.Sub(time.Unix(c.Started, 0)).Round(time.Second)
	return fmt.Sprintf("%s (pid %d, running %s, started on %s)", c.Agent, c.PID, running, c.Branch)
}

// CheckFailed is the note a launch prints when the claims in root cannot be
// read or written. It never stops the launch.
func CheckFailed(root string, err error) string {
	return fmt.Sprintf("could not check other sessions in %s (%v); launching anyway", root, err)
}

// DescribeAll names every session in cs on one line, in the order given:
// Describe's entry per claim joined with "; " — the join the launch warnings'
// held entry uses. It renders Holders' values for the cleanup list's held
// reason and the launch warnings; no claims give "".
func DescribeAll(cs []Claim, now time.Time) string {
	parts := make([]string, len(cs))
	for i, c := range cs {
		parts[i] = Describe(c, now)
	}
	return strings.Join(parts, "; ")
}
