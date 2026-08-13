# Lesson 9: Main guard

## Concept Intro

A signature feature is the **main guard**: a `block-main-commit v1` pre-commit
hook that refuses commits to `main`/`master`. The `*-wt` launchers auto-install
it on every run inside a git repo, and offer `--check-guard` (report status)
and `--no-guard` (uninstall).

This lesson ports `guard_status` and the install/uninstall logic. The tricky
part is finding the right hooks directory: `git rev-parse --git-common-dir`
returns the *common* git dir, which for a worktree is the main repo's
`.git` (so the hook applies to all worktrees of the repo). We install one
pre-commit hook there.

The hook itself is a small script. We embed it in the Go binary with
`//go:embed` and write it out when installing, rather than maintaining a
separate `wt-install-guard` shell script.

## New Syntax & Vocabulary

| Term | Meaning |
|---|---|
| `git rev-parse --git-common-dir` | Path to the shared git dir (handles worktrees). |
| `//go:embed` | Embeds a file into the binary at compile time. |
| `os.WriteFile(..., 0o755)` | Writes the hook as executable. |
| `os.Stat` / `os.IsNotExist` | Checks for file existence / absence. |
| `strings.Contains(hookSrc, "block-main-commit v1")` | Detects whether the guard is present. |
| `Status` result | One of: installed, not installed, error. |

## Worked Walkthrough

Embed the hook script. Create `internal/guard/hook.sh`:

```sh
#!/usr/bin/env bash
# block-main-commit v1
# Installed by wt. Blocks commits to main/master unless explicitly bypassed.
set -euo pipefail
branch="$(git symbolic-ref --short HEAD 2>/dev/null || true)"
case "$branch" in
  main|master)
    if [ "${WT_SKIP_MAIN_BLOCK:-0}" = "1" ]; then
      exit 0
    fi
    echo "block-main-commit: commits to '$branch' are blocked."
    echo "Bypass with: git commit --no-verify  (or WT_SKIP_MAIN_BLOCK=1)"
    exit 1
    ;;
esac
exit 0
```

Now `internal/guard/guard.go`:

```go
package guard

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

//go:embed hook.sh
var hookSrc string

const marker = "block-main-commit v1"

// Status describes the guard's installation state.
type Status int

const (
	NotInstalled Status = iota
	Installed
	Err
)

// CommonDir returns the repo's common git dir (handles worktrees).
func CommonDir() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--git-common-dir").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Status reports whether the guard is installed in this repo.
func Status() Status {
	common, err := CommonDir()
	if err != nil {
		return Err
	}
	data, err := os.ReadFile(filepath.Join(common, "hooks", "pre-commit"))
	if os.IsNotExist(err) {
		return NotInstalled
	}
	if err != nil {
		return Err
	}
	if strings.Contains(string(data), marker) {
		return Installed
	}
	return NotInstalled
}

// Install writes the pre-commit hook, preserving any existing hook by
// appending the guard to it. Returns whether it changed anything.
func Install() (bool, error) {
	common, err := CommonDir()
	if err != nil {
		return false, err
	}
	hooksDir := filepath.Join(common, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return false, err
	}
	hookPath := filepath.Join(hooksDir, "pre-commit")

	existing := ""
	if data, err := os.ReadFile(hookPath); err == nil {
		existing = string(data)
	} else if !os.IsNotExist(err) {
		return false, err
	}

	if strings.Contains(existing, marker) {
		return false, nil // already installed
	}

	// Prepend our hook and re-append the user's original hook, if any.
	content := hookSrc
	if strings.TrimSpace(existing) != "" {
		content += "\n# (preserved original pre-commit hook)\n" + existing
	}
	if err := os.WriteFile(hookPath, []byte(content), 0o755); err != nil {
		return false, err
	}
	return true, nil
}

// Uninstall removes the guard, restoring any preserved original hook.
func Uninstall() error {
	common, err := CommonDir()
	if err != nil {
		return err
	}
	hookPath := filepath.Join(common, "hooks", "pre-commit")
	data, err := os.ReadFile(hookPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !strings.Contains(string(data), marker) {
		return nil // not ours, leave it alone
	}

	// Reconstruct: everything after our guard block was the preserved hook.
	idx := strings.Index(string(data), "\n# (preserved original pre-commit hook)\n")
	if idx < 0 {
		return os.Remove(hookPath)
	}
	preserved := string(data)[idx+len("\n# (preserved original pre-commit hook)\n"):]
	if strings.TrimSpace(preserved) == "" {
		return os.Remove(hookPath)
	}
	return os.WriteFile(hookPath, []byte(preserved), 0o755)
}
```

### Wiring the flags

Add to the root command:

```go
cmd.PersistentFlags().Bool("check-guard", false, "Report main guard status and exit")
cmd.PersistentFlags().Bool("no-guard", false, "Remove the main guard and exit")
```

In `rootCmd`'s `RunE`, before the TUI placeholder:

```go
if check, _ := cmd.Flags().GetBool("check-guard"); check {
	switch guard.Status() {
	case guard.Installed:
		fmt.Println("wt: main guard is installed in this repo.")
	case guard.NotInstalled:
		fmt.Println("wt: main guard is NOT installed in this repo.")
		return fmt.Errorf("guard not installed")
	case guard.Err:
		return fmt.Errorf("could not determine guard status (not in a git repo?)")
	}
	return nil
}
if noGuard, _ := cmd.Flags().GetBool("no-guard"); noGuard {
	if err := guard.Uninstall(); err != nil {
		return err
	}
	fmt.Println("wt: main guard removed.")
	return nil
}
```

## Run It

```bash
go run ./cmd/wt --check-guard     # in a repo
go run ./cmd/wt --no-guard        # removes it
go run ./cmd/wt --check-guard     # now reports not installed
```

Try committing to `main` afterward — the guard should block it. `git commit
--no-verify` and `WT_SKIP_MAIN_BLOCK=1` bypass it.

## Try It Yourself

Verify `Install()` is idempotent: run it twice and assert the second call
returns `changed == false`.

<details>
<summary>Solution</summary>

```go
func TestInstallIdempotent(t *testing.T) {
	first, _ := Install()
	second, _ := Install()
	if !first {
		t.Fatal("expected first install to change")
	}
	if second {
		t.Fatal("expected second install to be a no-op")
	}
}
```
</details>

## Checkpoint

```bash
git add -A && git commit -m "lesson 09: main guard" && git tag lesson-09
```
