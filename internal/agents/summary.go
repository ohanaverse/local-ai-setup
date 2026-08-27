package agents

import (
	"fmt"
	"time"

	"github.com/ohanaverse/agent-worktree/internal/config"
)

// Summary formats the post-run summary line printed to stdout after an agent
// or command subprocess exits. Command agents (m.ID == "") omit the model
// segment. Durations ≥1s are rounded to seconds; shorter durations are shown
// in milliseconds.
func Summary(agent string, m config.Model, d time.Duration) string {
	if d >= time.Second {
		d = d.Round(time.Second)
	} else {
		d = d.Round(time.Millisecond)
	}
	if m.ID == "" {
		return fmt.Sprintf("wt: %s · %s", agent, d)
	}
	return fmt.Sprintf("wt: %s · %s · %s", agent, m.ID, d)
}
