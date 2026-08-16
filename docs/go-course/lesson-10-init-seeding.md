# Lesson 10: `--init` seeding

## Concept Intro

The `--init` flag seeds project-level instruction files so an agent starts
with context. It ports `seed_agent_instructions` from `wt-core.sh`:

- Creates `AGENTS.md` with a seed template if missing.
- Creates an agent-specific pointer file if the agent supports one:
  - **claude** → `CLAUDE.md` containing `@AGENTS.md`
  - **copilot** → `.github/copilot-instructions.md` containing a pointer
  - codex / pi / agy / opencode → no pointer file
- Never overwrites an existing file (skip-if-exists).

This is a purely file-writing feature with a clear rule set, so it maps
cleanly to a small `initseed` package with a `Seed(agent string, repoRoot string)`
function that returns what it created/skipped.

## New Syntax & Vocabulary

| Term | Meaning |
|---|---|
| `os.WriteFile` / `os.MkdirAll` | Create pointer files and parent dirs. |
| `os.Stat` + `os.IsNotExist` | Skip-if-exists check. |
| `os.Getenv("XDG_CONFIG_HOME")` | (Not needed here — this writes into the repo.) |
| `git rev-parse --show-toplevel` | Locate the repo root to seed into. |
| result struct | Reports which files were created vs already existed. |

## Worked Walkthrough

Create `internal/initseed/initseed.go`:

```go
package initseed

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Result reports what seeding did.
type Result struct {
	Created []string // files created
	Skipped []string // files that already existed
}

// Root returns the current repo's working-tree root.
func Root() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("not in a git working tree")
	}
	return strings.TrimSpace(string(out)), nil
}

// Seed creates AGENTS.md (if missing) and an agent pointer file if the agent
// supports one. Never overwrites existing files.
func Seed(agent, repoRoot string) (*Result, error) {
	res := &Result{}

	// AGENTS.md
	agentsPath := filepath.Join(repoRoot, "AGENTS.md")
	created, err := writeIfMissing(agentsPath, agentsTemplate)
	if err != nil {
		return nil, err
	}
	track(res, created, "AGENTS.md")

	// Agent-specific pointer file.
	pointer, ok := pointerFor(agent)
	if ok {
		ptrPath := filepath.Join(repoRoot, pointer.path)
		created, err := writeIfMissing(ptrPath, pointer.content)
		if err != nil {
			return nil, err
		}
		track(res, created, pointer.path)
	}
	return res, nil
}

func track(res *Result, created bool, name string) {
	if created {
		res.Created = append(res.Created, name)
	} else {
		res.Skipped = append(res.Skipped, name)
	}
}

// writeIfMissing writes content to path only if path does not exist.
func writeIfMissing(path, content string) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil // exists — skip
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

type pointer struct {
	path    string
	content string
}

// pointerFor returns the pointer file for an agent, and whether it has one.
func pointerFor(agent string) (pointer, bool) {
	switch agent {
	case "claude":
		return pointer{"CLAUDE.md", "@AGENTS.md\n"}, true
	case "copilot":
		return pointer{".github/copilot-instructions.md", "Read AGENTS.md and follow all instructions in it.\n"}, true
	default:
		return pointer{}, false
	}
}

const agentsTemplate = `# Agent Instructions

> **Uninitialized.** If this is your first time reading this file in a new
> project, ask the user about the project and fill in the sections below.
> Remove this notice once the file has been customized.

## Project

<!-- What does this project do? -->

## Stack

<!-- Language, framework, key dependencies -->

## Commands

<!-- Build, test, lint, deploy -->

## Conventions

<!-- Code style, naming patterns, important rules -->

## Architecture

<!-- Key directories, modules, data flow -->
`
```

### Wiring the flag

Add `--init` to the root command and handle it in `RunE` *before* anything
that requires an agent binary (it must work even if no agent is installed):

```go
// Seed agent instruction files and exit (no agent binary required).
cmd.Flags().Bool(
    "init",
    false,
    "Seed agent instruction files and exit",
)
```

```go
if initFlag, _ := cmd.Flags().GetBool("init"); initFlag {
	root, err := initseed.Root()
	if err != nil {
		return err
	}
	res, err := initseed.Seed("", root)
	if err != nil {
		return err
	}
	if len(res.Created) == 0 {
		fmt.Println("wt: instruction files already exist.")
	} else {
		fmt.Printf("wt: seeded: %s\n", strings.Join(res.Created, ", "))
	}
	// Also auto-install the guard, like a normal launch would.
	if _, err := guard.Install(); err != nil {
		return err
	}
	return nil
}
```

Note we pass agent `""` so only `AGENTS.md` is seeded here; the per-agent
pointer is seeded when a specific agent is chosen at launch (later lessons).

## Run It

```bash
go run ./cmd/wt --init
```

```
wt: seeded: AGENTS.md
```

Run it again:

```
wt: instruction files already exist.
```

## Tests

The package tests live in `internal/initseed/initseed_test.go` and cover:

- `TestSeedClaude` — creates `AGENTS.md` and `CLAUDE.md`, idempotently skips
  both on a second run.
- `TestSeedCopilot` — creates `.github/copilot-instructions.md` alongside
  `AGENTS.md`.
- `TestSeedUnknownAgent` — agents without a pointer file (e.g. `codex`) only
  get `AGENTS.md`.
- `TestSeedContent` — seeded files contain the expected template and pointer
  text.
- `TestRootInRepo` / `TestRootOutsideRepo` — `Root()` resolves the repo root
  or errors outside a git working tree.
- `TestSeedSkipsExistingPointer` — deleting just `AGENTS.md` re-creates it
  while leaving the existing pointer file untouched.

Run them with:

```bash
go test ./internal/initseed -v
```

## Try It Yourself

Write a test using `t.TempDir()` as the repo root that asserts `Seed("claude",
root)` creates both `AGENTS.md` and `CLAUDE.md`, and that a second call skips
both.

<details>
<summary>Solution</summary>

```go
func TestSeedClaude(t *testing.T) {
	root := t.TempDir()
	res, err := Seed("claude", root)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Created) != 2 {
		t.Fatalf("expected 2 created, got %+v", res.Created)
	}
	for _, f := range []string{"AGENTS.md", "CLAUDE.md"} {
		if _, err := os.Stat(filepath.Join(root, f)); err != nil {
			t.Errorf("%s not created: %v", f, err)
		}
	}
	res2, _ := Seed("claude", root)
	if len(res2.Skipped) != 2 {
		t.Fatalf("expected 2 skipped on second run, got %+v", res2.Skipped)
	}
}
```
</details>

## Checkpoint

```bash
git add -A && git commit -m "lesson 10: --init seeding" && git tag lesson-10
```
