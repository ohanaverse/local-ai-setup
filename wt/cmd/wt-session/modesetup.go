// The hand-off from `wt config`'s Mode tab to `wt-model setup`: when the
// tab's local choice asked to set this machine up, wt runs the wt-model it
// found as a child once the editor has closed. wt itself links no admin
// package; the setup is wt-model's.

package main

import (
	"os"
	"os/exec"
)

// runModelSetupChild runs `<path> setup` on wt's own terminal and returns the
// child's error (nil when it exited 0). A package var so tests run no child.
var runModelSetupChild = func(path string) error {
	cmd := exec.Command(path, "setup")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}
