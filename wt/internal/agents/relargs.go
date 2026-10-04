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
// prompts), the bare program name a command agent (shell) runs, and paths
// both directories resolve to the same file. Both directories are compared
// and reported with their symbolic links resolved. launchDir
// may be relative to shellDir, or empty when the agent inherits wt's own
// directory, in which case nothing can differ.
func RelativeArgNotes(agent, shellDir, launchDir string, args []string) []string {
	if shellDir == "" || launchDir == "" || len(args) == 0 {
		return nil
	}
	shellDir = physicalDir(shellDir)
	if !filepath.IsAbs(launchDir) {
		launchDir = filepath.Join(shellDir, launchDir)
	}
	launchDir = physicalDir(launchDir)
	if sameFile(shellDir, launchDir) {
		return nil
	}
	// A driver that takes the arguments as its argv (shell) runs the first one
	// as a program. Without a separator that is a PATH lookup, never a path in
	// either directory, so a directory that happens to share its name ("make"
	// beside a make/ folder) says nothing.
	_, argv := ByName(agent).(ArgSetter)
	var notes []string
	for i, a := range args {
		if a == "" || strings.HasPrefix(a, "-") || strings.HasPrefix(a, "~") || filepath.IsAbs(a) {
			continue
		}
		if argv && i == 0 && !strings.ContainsRune(a, filepath.Separator) {
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

// physicalDir resolves dir's symbolic links, so that a ".." joined onto it
// climbs the directory the kernel would. os.Getwd reports $PWD — the spelling
// the shell arrived by, symlinks included — and filepath.Join removes ".."
// lexically, so "../x" joined onto a symlinked directory names the symlink's
// parent while the shell and the agent both resolve it from the target's. A
// directory that cannot be resolved is returned cleaned, as given.
func physicalDir(dir string) string {
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		return resolved
	}
	return filepath.Clean(dir)
}

// sameFile reports whether a and b name the same existing file or directory.
func sameFile(a, b string) bool {
	ai, aerr := os.Stat(a)
	bi, berr := os.Stat(b)
	return aerr == nil && berr == nil && os.SameFile(ai, bi)
}
