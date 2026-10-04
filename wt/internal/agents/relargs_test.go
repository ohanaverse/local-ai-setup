package agents

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// relArgsTree builds the layout of the report that prompted the check:
//
//	root/other/            a sibling project
//	root/repo/             the launch directory (the worktree the agent starts in)
//	root/repo/wt/          the directory the user typed the command in
//	root/repo/README.md    a file reachable by the same relative path from both
//	root/repo/wt/notes.md  a file only the shell directory has
//	root/repo/notes.md     a DIFFERENT file with the same relative name
//
// Paths are resolved through EvalSymlinks so macOS's /var → /private/var
// does not make two spellings of one directory look different.
func relArgsTree(t *testing.T) (shell, launch, other string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	other = filepath.Join(root, "other")
	launch = filepath.Join(root, "repo")
	shell = filepath.Join(launch, "wt")
	for _, d := range []string{other, shell} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{filepath.Join(launch, "README.md"), filepath.Join(shell, "notes.md"), filepath.Join(launch, "notes.md")} {
		if err := os.WriteFile(f, []byte(f), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return shell, launch, other
}

// TestRelativeArgNotesFlagsAPathThatOnlyResolvesFromTheShell is the report
// itself: `opencode-wt --yolo -- ../../other`, typed in <repo>/wt, reached
// opencode with its working directory set to <repo>, where the same relative
// path names nothing ("Failed to change directory"). wt cannot know an
// argument is a path, so it does not rewrite it; it says what happened and
// gives the absolute path to pass instead.
func TestRelativeArgNotesFlagsAPathThatOnlyResolvesFromTheShell(t *testing.T) {
	shell, launch, other := relArgsTree(t)
	notes := RelativeArgNotes("opencode", shell, launch, []string{"../../other"})
	if len(notes) != 1 {
		t.Fatalf("notes = %q, want one", notes)
	}
	for _, want := range []string{`"../../other"`, "opencode starts in " + launch, "does not exist", other} {
		if !strings.Contains(notes[0], want) {
			t.Errorf("note %q lacks %q", notes[0], want)
		}
	}
	if !strings.HasPrefix(notes[0], "wt: note: ") {
		t.Errorf("note %q does not start with the wt: note: prefix", notes[0])
	}
}

// TestRelativeArgNotesFlagsADifferentFileOfTheSameName covers the quieter
// failure: the relative path exists from both directories but names two
// different files, so the agent silently opens the wrong one.
func TestRelativeArgNotesFlagsADifferentFileOfTheSameName(t *testing.T) {
	shell, launch, _ := relArgsTree(t)
	notes := RelativeArgNotes("claude", shell, launch, []string{"notes.md"})
	if len(notes) != 1 || !strings.Contains(notes[0], "a different file") || !strings.Contains(notes[0], filepath.Join(shell, "notes.md")) {
		t.Fatalf("notes = %q, want one naming the different file and the shell's absolute path", notes)
	}
}

// TestRelativeArgNotesStaysQuiet pins every case that must not be flagged: a
// note on an argument that is not a mistaken path would train users to ignore
// the real one.
func TestRelativeArgNotesStaysQuiet(t *testing.T) {
	shell, launch, other := relArgsTree(t)
	// One file reachable by the same relative name from both directories: a
	// hard link, the only way two directories share a name for one file.
	if err := os.WriteFile(filepath.Join(launch, "shared.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(launch, "shared.md"), filepath.Join(shell, "shared.md")); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		shell, launch string
		args          []string
	}{
		"typed in the launch directory itself":          {launch, launch, []string{"README.md", "wt"}},
		"an absolute path":                              {shell, launch, []string{other}},
		"a flag, even one carrying a relative path":     {shell, launch, []string{"--add-dir", "--file=notes.md", "-p"}},
		"a prompt and a subcommand that are no file":    {shell, launch, []string{"run", "fix the failing test in ../other"}},
		"a path that does not exist from the shell":     {shell, launch, []string{"../../nope", "missing.md"}},
		"the same file from both directories":           {shell, launch, []string{"shared.md"}},
		"no launch directory (the agent inherits wt's)": {shell, "", []string{"notes.md"}},
		"no arguments": {shell, launch, nil},
	} {
		if notes := RelativeArgNotes("claude", tc.shell, tc.launch, tc.args); len(notes) != 0 {
			t.Errorf("%s: notes = %q, want none", name, notes)
		}
	}
}

// TestRelativeArgNotesResolvesARelativeLaunchDir pins that a launch directory
// given relative to the shell (wt passes "." outside a repo) is resolved
// before comparing, so "." is recognised as the shell's own directory.
func TestRelativeArgNotesResolvesARelativeLaunchDir(t *testing.T) {
	shell, _, _ := relArgsTree(t)
	if notes := RelativeArgNotes("claude", shell, ".", []string{"notes.md"}); len(notes) != 0 {
		t.Fatalf("launch dir \".\": notes = %q, want none", notes)
	}
	if notes := RelativeArgNotes("claude", shell, "..", []string{"../../other"}); len(notes) != 1 {
		t.Fatalf("launch dir \"..\": notes = %q, want one", notes)
	}
}

// TestRelativeArgNotesResolvesASymlinkedShellDir pins that ".." is climbed the
// way the kernel climbs it. os.Getwd reports $PWD, so a shell that arrived
// through a symlink hands wt the symlink's spelling; joining "../../other"
// onto that lexically names the symlink's parent, where nothing exists, and
// the real mistake would go unreported — or be reported with an absolute path
// that is not the one the user meant.
func TestRelativeArgNotesResolvesASymlinkedShellDir(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	launch := filepath.Join(root, "real", "repo")
	other := filepath.Join(root, "real", "other")
	for _, d := range []string{filepath.Join(launch, "wt"), other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(launch, link); err != nil {
		t.Fatal(err)
	}
	notes := RelativeArgNotes("opencode", filepath.Join(link, "wt"), launch, []string{"../../other"})
	if len(notes) != 1 || !strings.Contains(notes[0], other+";") {
		t.Fatalf("notes = %q, want one naming %s", notes, other)
	}
}

// TestRelativeArgNotesSkipsACommandAgentsProgramName pins that the program a
// command agent runs is not mistaken for a path: `shell-wt -- make docs` looks
// "make" up on PATH whatever directory it starts in, so a folder of that name
// beside the shell must not draw a note. Its other arguments, and a program
// given as a path, are still checked.
func TestRelativeArgNotesSkipsACommandAgentsProgramName(t *testing.T) {
	shell, launch, _ := relArgsTree(t)
	for _, d := range []string{"make", "scripts"} {
		if err := os.Mkdir(filepath.Join(shell, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if notes := RelativeArgNotes("shell", shell, launch, []string{"make", "all"}); len(notes) != 0 {
		t.Errorf("bare program name: notes = %q, want none", notes)
	}
	if notes := RelativeArgNotes("shell", shell, launch, []string{"cat", "make"}); len(notes) != 1 {
		t.Errorf("later argument: notes = %q, want one", notes)
	}
	if notes := RelativeArgNotes("shell", shell, launch, []string{"./scripts"}); len(notes) != 1 {
		t.Errorf("program given as a path: notes = %q, want one", notes)
	}
	if notes := RelativeArgNotes("claude", shell, launch, []string{"make"}); len(notes) != 1 {
		t.Errorf("an agent's first argument: notes = %q, want one", notes)
	}
}

// relArgsGit runs git in dir for the worktree fixtures, with the caller's
// identity and repository variables out of the way.
func relArgsGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// relArgsRepo makes a real repository at root/<name> with README.md,
// docs/README.md and docs/guide.md committed, and returns its path.
func relArgsRepo(t *testing.T, root, name string) string {
	t.Helper()
	repo := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(repo, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"README.md", "docs/README.md", "docs/guide.md"} {
		if err := os.WriteFile(filepath.Join(repo, f), []byte(f), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	relArgsGit(t, repo, "init", "-q")
	relArgsGit(t, repo, "add", ".")
	relArgsGit(t, repo, "commit", "-q", "-m", "init")
	return repo
}

// TestRelativeArgNotesStaysQuietForAWorktreeTwin pins the ordinary launch:
// `shell-wt -W feat -- cat README.md`, typed at the repo root, starts in
// .worktrees/feat, where README.md is that checkout's copy of the same tracked
// file — a different file on disk, and the one the user meant. A note there
// fires on every -W launch that names a tracked file and tells the user to pass
// the main checkout's path, which runs the command against the wrong checkout.
// It holds in both directions (a worktree's shell launching at the repo root).
func TestRelativeArgNotesStaysQuietForAWorktreeTwin(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := relArgsRepo(t, root, "repo")
	feat := filepath.Join(repo, ".worktrees", "feat")
	relArgsGit(t, repo, "worktree", "add", "-q", feat, "-b", "feat")

	args := []string{"README.md", "docs", "docs/guide.md", "./README.md"}
	if notes := RelativeArgNotes("claude", repo, feat, args); len(notes) != 0 {
		t.Errorf("repo root → worktree: notes = %q, want none", notes)
	}
	if notes := RelativeArgNotes("claude", feat, repo, args); len(notes) != 0 {
		t.Errorf("worktree → repo root: notes = %q, want none", notes)
	}
	if notes := RelativeArgNotes("claude", filepath.Join(repo, "docs"), filepath.Join(feat, "docs"), []string{"guide.md", "../README.md"}); len(notes) != 0 {
		t.Errorf("the same subdirectory of both: notes = %q, want none", notes)
	}
}

// TestRelativeArgNotesStillFlagsWhatIsNotATwin pins the edges of the twin
// rule, so that it cannot grow into "never note between two checkouts": the
// real mistakes between a checkout and its worktree are still reported.
func TestRelativeArgNotesStillFlagsWhatIsNotATwin(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := relArgsRepo(t, filepath.Join(root, "nest"), "repo")
	feat := filepath.Join(repo, ".worktrees", "feat")
	relArgsGit(t, repo, "worktree", "add", "-q", feat, "-b", "feat")
	unrelated := relArgsRepo(t, root, "unrelated")
	if err := os.WriteFile(filepath.Join(repo, "scratch.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// ../../outside.md climbs out of the worktree into the main checkout, and
	// out of the main checkout altogether: two files that are no twins.
	for _, f := range []string{filepath.Join(repo, "outside.md"), filepath.Join(root, "outside.md")} {
		if err := os.WriteFile(f, []byte(f), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	for name, tc := range map[string]struct {
		shell, launch, arg, want string
	}{
		"typed in a subdirectory, launched at the worktree root": {filepath.Join(repo, "docs"), feat, "README.md", "a different file"},
		"untracked, so the worktree has no copy":                 {repo, feat, "scratch.md", "does not exist"},
		"two unrelated repositories":                             {repo, unrelated, "README.md", "a different file"},
		"a path that leaves the checkout":                        {feat, repo, "../../outside.md", "a different file"},
	} {
		notes := RelativeArgNotes("claude", tc.shell, tc.launch, []string{tc.arg})
		if len(notes) != 1 || !strings.Contains(notes[0], tc.want) {
			t.Errorf("%s: notes = %q, want one saying %q", name, notes, tc.want)
		}
	}
}
