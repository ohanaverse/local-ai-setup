package agents

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RelativeArgNotes reports the passthrough arguments (those after `--`) that
// look like a path the user meant relative to the directory they typed the
// command in, but that the agent will resolve somewhere else.
//
// wt starts an agent with its working directory set to the worktree it
// launches in (SetDir), and hands the passthrough arguments over untouched.
// A relative path among them is then resolved by the agent from that
// directory, not from the shell's: `opencode-wt --yolo -- ../../other`, typed
// in <repo>/wt, reached opencode in <repo> and failed with "Failed to change
// directory". wt cannot tell a path from any other word, so it never rewrites
// an argument — a prompt may contain something that merely looks like one. It
// only says what is about to happen and gives the absolute path to use.
//
// An argument is reported when it exists relative to shellDir and, relative
// to launchDir, either does not exist or is a different file. Everything else
// is left alone: flags (a leading "-", including "--file=x"), absolute paths
// and "~" forms, words that name nothing from the shell (subcommands,
// prompts), and paths both directories resolve to the same file. launchDir
// may be relative to shellDir, or empty when the agent inherits wt's own
// directory, in which case nothing can differ.
func RelativeArgNotes(agent, shellDir, launchDir string, args []string) []string {
	if shellDir == "" || launchDir == "" || len(args) == 0 {
		return nil
	}
	if !filepath.IsAbs(launchDir) {
		launchDir = filepath.Join(shellDir, launchDir)
	}
	launchDir = filepath.Clean(launchDir)
	if sameFile(shellDir, launchDir) {
		return nil
	}
	var notes []string
	for _, a := range args {
		if a == "" || strings.HasPrefix(a, "-") || strings.HasPrefix(a, "~") || filepath.IsAbs(a) {
			continue
		}
		fromShell := filepath.Join(shellDir, a)
		si, err := os.Stat(fromShell)
		if err != nil {
			continue
		}
		fromLaunch := filepath.Join(launchDir, a)
		where := "where it does not exist"
		if li, lerr := os.Stat(fromLaunch); lerr == nil {
			if os.SameFile(si, li) {
				continue
			}
			where = "where it is a different file (" + fromLaunch + ")"
		}
		notes = append(notes, fmt.Sprintf(
			"wt: note: %q is a relative path. %s starts in %s, %s. From %s it is %s; pass that absolute path.",
			a, agent, launchDir, where, shellDir, fromShell))
	}
	return notes
}

// sameFile reports whether a and b name the same existing file or directory.
func sameFile(a, b string) bool {
	ai, aerr := os.Stat(a)
	bi, berr := os.Stat(b)
	return aerr == nil && berr == nil && os.SameFile(ai, bi)
}
