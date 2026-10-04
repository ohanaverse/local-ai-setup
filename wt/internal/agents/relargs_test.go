package agents

import (
	"os"
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
