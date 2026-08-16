# Lesson 11: Session resume

## Concept Intro

Two agents — claude and opencode — keep local session history. When you
relaunch into a worktree, `wt` can offer to *resume* the most recent session
instead of starting fresh. This lesson ports `find_latest_session`,
`compute_project_slug`, and `relative_time` from `wt-core.sh`, plus the
opencode-specific `_find_opencode_sessions` from `opencode-wt`.

The session locations differ:

- **claude**: `~/.claude/projects/<slug>/*.jsonl`, where `<slug>` is the
  worktree path with every character not in `[a-zA-Z0-9-]` replaced by `-`.
  Note the leading `/` is *not* removed — it becomes a leading `-`, so the
  real dirs look like `-Users-keith-...`.
- **opencode**: `~/.local/share/opencode/storage/session/<project-id>/`,
  where `<project-id>` is the git *commit hash of the repo's root commit*.
  OpenCode stores its sessions as **`.json`** files (not `.jsonl`).

We detect the newest session by **mtime** (last-modified), which is the
"most recent activity" signal the bash version uses.

This lesson is mostly filesystem work: walking a directory, filtering by
extension, and ranking by modification time. It introduces the slug computation
and a tiny relative-time formatter for the UI.

## New Syntax & Vocabulary

| Term | Meaning |
|---|---|
| `os.ReadDir(dir)` | Lists directory entries sorted by filename. |
| `os.FileInfo.ModTime()` | Returns the modification time of an entry. |
| `time.Time.Before` / `sort.Slice` | Rank sessions by recency. |
| `regexp` for slug | Replace non-`[a-zA-Z0-9-]` chars with `-`. |
| `git -C <path> rev-list --max-parents=0 HEAD` | Finds the repo's root commit hash *for that path*. |
| relative time | "just now", "5m ago", "3h ago" — a small `time.Since` helper. |

## Worked Walkthrough

Create `internal/session/session.go`:

```go
package session

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Session identifies a resumable session.
type Session struct {
	ID    string // file stem (claude) or dir name (opencode)
	Path  string // full path
	MTime time.Time
}

// Slug converts an absolute path to the session dir slug: every char outside
// [a-zA-Z0-9-] becomes '-'. The leading slash becomes a leading '-', matching
// the real ~/.claude/projects/-Users-... dirs.
var nonSlug = regexp.MustCompile(`[^a-zA-Z0-9-]`)

func Slug(path string) string {
	return nonSlug.ReplaceAllString(path, "-")
}

// LatestClaude returns the most recently modified claude session for path.
func LatestClaude(path string) (*Session, error) {
	dir := filepath.Join(os.Getenv("HOME"), ".claude", "projects", Slug(path))
	return latestByExt(dir, ".jsonl", func(f os.FileInfo) string {
		return strings.TrimSuffix(f.Name(), ".jsonl")
	})
}

// OpenCodeProjectID returns the repo's root commit hash (opencode's project id).
// It runs git inside path so the id matches the worktree being resumed.
func OpenCodeProjectID(path string) (string, error) {
	out, err := exec.Command("git", "-C", path, "rev-list", "--max-parents=0", "HEAD").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// LatestOpenCode returns the most recently modified opencode session for path.
// OpenCode stores sessions as .json files (not .jsonl) under the project id.
func LatestOpenCode(path string) (*Session, error) {
	projectID, err := OpenCodeProjectID(path)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(os.Getenv("HOME"), ".local", "share", "opencode",
		"storage", "session", projectID)
	return latestByExt(dir, ".json", func(f os.FileInfo) string {
		return f.Name()
	})
}

// latestByExt finds the newest file with the given extension in dir.
func latestByExt(dir, ext string, idOf func(os.FileInfo) string) (*Session, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil // no sessions
	}
	if err != nil {
		return nil, err
	}

	var sessions []Session
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ext) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		sessions = append(sessions, Session{
			ID:    idOf(info),
			Path:  filepath.Join(dir, e.Name()),
			MTime: info.ModTime(),
		})
	}
	if len(sessions) == 0 {
		return nil, nil
	}
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].MTime.After(sessions[j].MTime)
	})
	return &sessions[0], nil
}

// RelativeTime renders a duration as "just now", "5m ago", etc.
func RelativeTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return fmt.Sprintf("%dw ago", int(d.Hours()/24/7))
	}
}
```

### Wiring

Add a helper that, given an agent name and worktree path, returns the session
to resume (or nil):

```go
// LatestForAgent returns the newest session for agent in worktreePath.
func LatestForAgent(agent, worktreePath string) (*Session, error) {
	switch agent {
	case "claude":
		return LatestClaude(worktreePath)
	case "opencode":
		return LatestOpenCode(worktreePath)
	default:
		return nil, nil
	}
}
```

The bash wrappers resume with a per-agent flag — claude uses `--resume <id>`,
opencode uses `--session <id>`. That launch wiring belongs to the TUI (lesson
16); this lesson only *detects* the session. The bash `wt_pre_exec` also skips
the resume prompt when `--cwd` is used or a model mode (`--code`/`--design`/
`--native`) is explicitly requested — a rule to carry into the TUI later.

### Test-only CLI flag

The `wt` CLI has no TUI yet, so add a `--debug-session` test helper to
`cmd/wt/main.go`, mirroring the existing `--debug-worktrees` / `--rotate-tag`
flags:

```go
// Test-only flag: print the newest resumable session for an agent.
cmd.Flags().String(
	"debug-session",
	"",
	"Print newest session for an agent (claude|opencode) (test helper)",
)
```

```go
if agent, _ := cmd.Flags().GetString("debug-session"); agent != "" {
	root, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return fmt.Errorf("not in a git repo: %w", err)
	}
	cwdRoot := strings.TrimSpace(string(root))
	s, err := session.LatestForAgent(agent, cwdRoot)
	if err != nil {
		return err
	}
	if s == nil {
		fmt.Println("(no sessions)")
		return nil
	}
	fmt.Printf("resume %s (last %s)\n", s.ID, session.RelativeTime(s.MTime))
	return nil
}
```

## Run It

```bash
go run ./cmd/wt --debug-session claude
```

```
resume 4f1c2a9b-e4d8-4c3f-9b2a-1d2e3f4a5b6c (last 3h ago)
```

(Or `(no sessions)`, if there are none.)

## Tests

The package tests live in `internal/session/session_test.go` and cover:

- `TestSlug` — asserts `Slug("/a/b/c.d") == "-a-b-c-d"` (leading slash becomes
  a leading `-`, dots → dashes), matching the real `-Users-...` dirs.
- `TestRelativeTime` — asserts the m/h/d/w buckets match the bash helper.
- `TestLatestByExtNoDir` — a missing session dir yields `nil`, not an error.
- `TestLatestByExtRanking` — the newest file (by mtime) is returned.
- `TestOpenCodeProjectID` — the project id is the repo's root commit hash,
  resolved for the given path via `git -C`.
- `TestLatestClaude` / `TestLatestOpenCode` — end-to-end: override `HOME` to a
  temp dir, build the real slug / project-id path, and assert the newest
  session is found (`.jsonl` for claude, `.json` for opencode).

Run them with:

```bash
go test ./internal/session -v
```

## Try It Yourself

Write a unit test for `Slug` that asserts
`Slug("/a/b/c.d") == "-a-b-c-d"` (leading slash becomes a leading `-`,
dots → dashes).

<details>
<summary>Solution</summary>

```go
func TestSlug(t *testing.T) {
	got := Slug("/a/b/c.d")
	want := "-a-b-c-d"
	if got != want {
		t.Fatalf("Slug() = %q, want %q", got, want)
	}
}
```
</details>

## Checkpoint

```bash
git add -A && git commit -m "lesson 11: session resume" && git tag lesson-11
```
