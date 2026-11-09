package rootmove

import (
	"slices"

	"github.com/ohanaverse/local-ai-setup/wt/internal/rescue"
)

// WouldMove is the would_move Outcome for a plan Preflight checked: what
// `--dry-run` encodes and prints. It runs no git and changes nothing; the plan
// is copied, so the outcome does not share the caller's slices.
func WouldMove(p *Plan) Outcome {
	cp := *p
	cp.Warnings = slices.Clone(p.Warnings)
	return Outcome{Status: rescue.StatusWouldMove, Plan: cp, Completed: []string{}}
}
