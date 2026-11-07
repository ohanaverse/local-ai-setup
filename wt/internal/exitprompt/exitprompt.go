// Package exitprompt holds what the post-exit worktree prompt needs to know
// about the launch: which branch and worktree this process created, and the
// tip the branch had then. The record lives in memory only; nothing is
// written to disk to remember it.
package exitprompt

import (
	"fmt"
	"os/exec"
	"strings"
	"sync"
)

// Created is a worktree and branch this launch created. Branch is the short
// name; Tip is the full sha the branch pointed at when it was created.
type Created struct {
	Path, Branch, Tip string
}

var (
	mu       sync.Mutex
	recorded Created
	have     bool
)

// Record notes that this launch created branch in the worktree at path. It
// reads the worktree's HEAD at once, so the tip is the creation tip. The
// latest call wins. On error nothing is recorded.
func Record(path, branch string) error {
	out, err := exec.Command("git", "-C", path, "rev-parse", "HEAD").Output()
	if err != nil {
		return fmt.Errorf("read the tip of %s: %w", path, err)
	}
	mu.Lock()
	defer mu.Unlock()
	recorded = Created{Path: path, Branch: branch, Tip: strings.TrimSpace(string(out))}
	have = true
	return nil
}

// Recorded returns what Record stored, and false when nothing was.
func Recorded() (Created, bool) {
	mu.Lock()
	defer mu.Unlock()
	return recorded, have
}

// Reset forgets the record (tests).
func Reset() {
	mu.Lock()
	defer mu.Unlock()
	recorded, have = Created{}, false
}
