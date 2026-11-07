package tui

import (
	"fmt"
	"os"

	"github.com/ohanaverse/local-ai-setup/wt/internal/exitprompt"
)

// recordCreation tells the exit prompt that this launch created branch in the
// worktree at path. A failure is a one-line note, never a failed launch: the
// worktree is then simply never offered for removal. The note goes to stderr
// directly because the creation runs inside a tea.Cmd.
func recordCreation(path, branch string) {
	if err := exitprompt.Record(path, branch); err != nil {
		fmt.Fprintf(os.Stderr, "wt: note: could not record %s for the exit prompt (%v); it will be kept\n", path, err)
	}
}
