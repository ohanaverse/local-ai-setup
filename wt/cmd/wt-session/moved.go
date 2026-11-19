package main

import (
	"errors"

	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
)

// movedError is the one wording of a command that moved to wt-model: exit 1, one line, nothing changed.
// old is the wt command as typed (after "wt "), new the wt-model command that replaces it.
func movedError(old, new string) error {
	return exitcode.New(1, errors.New(old+" moved to wt-model — run 'wt-model "+new+"'; nothing was changed"), false)
}
