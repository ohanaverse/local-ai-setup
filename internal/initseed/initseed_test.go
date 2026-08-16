package initseed

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestSeedClaude creates AGENTS.md and CLAUDE.md for claude, and idempotently
// skips them on a second call. This mirrors the bash wrapper's
// seed_agent_instructions behavior and ensures users never lose customizations.
func TestSeedClaude(t *testing.T) {
	root := t.TempDir()

	res, err := Seed("claude", root)
	if err != nil {
		t.Fatal(err)
	}
	wantCreated := []string{"AGENTS.md", "CLAUDE.md"}
	if len(res.Created) != len(wantCreated) {
		t.Fatalf("expected %d created, got %v", len(wantCreated), res.Created)
	}
	for i, name := range wantCreated {
		if res.Created[i] != name {
			t.Errorf("created[%d] = %q, want %q", i, res.Created[i], name)
		}
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Errorf("%s not created: %v", name, err)
		}
	}
	if len(res.Skipped) != 0 {
		t.Fatalf("expected 0 skipped on first run, got %v", res.Skipped)
	}

	res2, err := Seed("claude", root)
	if err != nil {
		t.Fatal(err)
	}
	if len(res2.Created) != 0 {
		t.Fatalf("expected 0 created on second run, got %v", res2.Created)
	}
	if len(res2.Skipped) != len(wantCreated) {
		t.Fatalf("expected %d skipped on second run, got %v", len(wantCreated), res2.Skipped)
	}
}

// TestSeedCopilot creates the .github/copilot-instructions.md pointer so Copilot
// reads AGENTS.md automatically, matching the bash copilot-wt --init behavior.
func TestSeedCopilot(t *testing.T) {
	root := t.TempDir()

	res, err := Seed("copilot", root)
	if err != nil {
		t.Fatal(err)
	}
	wantCreated := []string{"AGENTS.md", ".github/copilot-instructions.md"}
	if len(res.Created) != len(wantCreated) {
		t.Fatalf("expected %d created, got %v", len(wantCreated), res.Created)
	}
	for _, name := range wantCreated {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Errorf("%s not created: %v", name, err)
		}
	}
}

// TestSeedUnknownAgent creates only AGENTS.md when the agent has no pointer
// file, so --init (which passes an empty agent name) is safe and minimal.
func TestSeedUnknownAgent(t *testing.T) {
	root := t.TempDir()

	res, err := Seed("codex", root)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Created) != 1 || res.Created[0] != "AGENTS.md" {
		t.Fatalf("expected only AGENTS.md created, got %v", res.Created)
	}
}

// TestSeedContent verifies the seeded files contain the expected pointer text so
// agents actually know where to look for instructions.
func TestSeedContent(t *testing.T) {
	root := t.TempDir()

	if _, err := Seed("claude", root); err != nil {
		t.Fatal(err)
	}

	agents, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(agents), "## Project") {
		t.Error("AGENTS.md missing Project section")
	}

	claude, err := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(claude) != "@AGENTS.md\n" {
		t.Errorf("CLAUDE.md = %q, want \"@AGENTS.md\\n\"", string(claude))
	}
}

// TestRootInRepo returns the working-tree root when inside a normal git repo.
// The --init flag relies on this to know where to write instruction files.
func TestRootInRepo(t *testing.T) {
	dir := t.TempDir()
	if err := gitInit(t, dir); err != nil {
		t.Fatal(err)
	}

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	root, err := Root()
	if err != nil {
		t.Fatalf("Root() error = %v", err)
	}
	resolvedRoot, _ := filepath.EvalSymlinks(root)
	resolvedWant, _ := filepath.EvalSymlinks(dir)
	if resolvedRoot != resolvedWant {
		t.Fatalf("Root() = %q, want %q", root, dir)
	}
}

// TestRootOutsideRepo errors when not inside a git working tree, so --init
// cannot accidentally seed files into an arbitrary directory.
func TestRootOutsideRepo(t *testing.T) {
	oldWd, _ := os.Getwd()
	os.Chdir(t.TempDir())
	defer os.Chdir(oldWd)

	if _, err := Root(); err == nil {
		t.Fatal("Root() error = nil, want error outside repo")
	}
}

// TestSeedSkipsExistingPointer only re-creates AGENTS.md when AGENTS.md is
// missing but the pointer file already exists, so a user can re-run --init
// after deleting just the main instructions file.
func TestSeedSkipsExistingPointer(t *testing.T) {
	root := t.TempDir()

	if _, err := Seed("claude", root); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}

	res, err := Seed("claude", root)
	if err != nil {
		t.Fatal(err)
	}
	wantCreated := []string{"AGENTS.md"}
	wantSkipped := []string{"CLAUDE.md"}
	if !slicesEqual(res.Created, wantCreated) {
		t.Fatalf("created = %v, want %v", res.Created, wantCreated)
	}
	if !slicesEqual(res.Skipped, wantSkipped) {
		t.Fatalf("skipped = %v, want %v", res.Skipped, wantSkipped)
	}
}

func gitInit(t *testing.T, dir string) error {
	t.Helper()
	if out, err := exec.Command("git", "init", dir).CombinedOutput(); err != nil {
		return fmt.Errorf("git init: %w\n%s", err, out)
	}
	return nil
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
