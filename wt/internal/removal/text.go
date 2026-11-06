package removal

import (
	"fmt"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// Headline is the first line of an outcome's text: what became of the item's
// worktree and branch.
func Headline(o Outcome) string {
	return fmt.Sprintf("%s %s: worktree %s, branch %s", o.Item.Kind, cleanup.OneLine(o.Item.Name), o.Worktree, o.Branch)
}

// DetailLines are an outcome's losses, warnings and, for a branch this run
// deleted (or may have), the commit it pointed at, one sanitised line each
// and unindented. The command's text and the tick list print these same
// lines, so the one record of a deleted commit cannot read differently in
// the two.
func DetailLines(o Outcome) []string {
	var out []string
	for _, l := range o.LossLines {
		out = append(out, "loss: "+cleanup.OneLine(l))
	}
	for _, w := range o.Warnings {
		out = append(out, "warning: "+cleanup.OneLine(w))
	}
	if o.DeletedTip != "" {
		out = append(out, "branch tip was "+cleanup.OneLine(o.DeletedTip))
	}
	return out
}
