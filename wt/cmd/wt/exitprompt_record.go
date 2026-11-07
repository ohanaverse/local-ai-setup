package main

import (
	"fmt"

	"github.com/ohanaverse/local-ai-setup/wt/internal/exitprompt"
)

// recordCreation tells the exit prompt that this launch created branch in the
// worktree at path. A failure is a one-line note, never a failed launch: the
// worktree is then simply never offered for removal.
func recordCreation(path, branch string) {
	if err := exitprompt.Record(path, branch); err != nil {
		fmt.Fprintf(osStderr, "wt: note: could not record %s for the exit prompt (%v); it will be kept\n", path, err)
	}
}
