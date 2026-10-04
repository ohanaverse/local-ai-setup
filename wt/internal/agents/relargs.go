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
//
// One "different file" is not a mistake: the twin. `shell-wt -W feat -- cat
// README.md`, typed at the repo root, starts in .worktrees/feat, whose
// README.md is that checkout's copy of the same tracked path — the file the
// user launched a worktree to reach. When the two directories sit at the same
// place in two checkouts of one repository (twinCheckout), an argument that
// stays inside the shell's checkout and exists in both is left alone; one
// missing from the launch checkout (untracked, or not on that branch) is still
// reported.
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
	shellRoot, twins := twinCheckout(shellDir, launchDir)
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
			if os.SameFile(si, li) || (twins && within(shellRoot, fromShell)) {
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

// twinCheckout reports whether shellDir and launchDir are the same directory
// of two different checkouts of one repository — the main checkout and a
// linked worktree, or two worktrees — and returns the root of shellDir's. A
// relative path that stays inside that root then names the same repository
// path from either directory.
func twinCheckout(shellDir, launchDir string) (shellRoot string, ok bool) {
	shellRoot, shellGit, sok := checkout(shellDir)
	launchRoot, launchGit, lok := checkout(launchDir)
	if !sok || !lok || shellRoot == launchRoot || !sameFile(shellGit, launchGit) {
		return "", false
	}
	sr, serr := filepath.Rel(shellRoot, shellDir)
	lr, lerr := filepath.Rel(launchRoot, launchDir)
	return shellRoot, serr == nil && lerr == nil && sr == lr
}

// checkout finds the git checkout dir is in: its root (the nearest ancestor
// holding a .git entry) and the repository's common git directory, which every
// checkout of one repository shares. It reads the files git itself writes
// rather than running git — the picker calls this on its update goroutine. The
// main checkout's .git is that directory; a linked worktree's is a file,
// "gitdir: <common>/worktrees/<name>", and that directory's commondir file
// points back at the common one (absent for a submodule, whose gitdir is its
// own).
func checkout(dir string) (root, commonGit string, ok bool) {
	for root = dir; ; root = filepath.Dir(root) {
		dotGit := filepath.Join(root, ".git")
		if fi, err := os.Stat(dotGit); err == nil {
			if fi.IsDir() {
				return root, dotGit, true
			}
			b, err := os.ReadFile(dotGit)
			gitDir, found := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir:")
			if err != nil || !found {
				return "", "", false
			}
			gitDir = strings.TrimSpace(gitDir)
			if !filepath.IsAbs(gitDir) {
				gitDir = filepath.Join(root, gitDir)
			}
			if c, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
				common := strings.TrimSpace(string(c))
				if !filepath.IsAbs(common) {
					common = filepath.Join(gitDir, common)
				}
				return root, common, true
			}
			return root, gitDir, true
		}
		if root == filepath.Dir(root) {
			return "", "", false
		}
	}
}

// within reports whether path is dir or lies beneath it, lexically.
func within(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
