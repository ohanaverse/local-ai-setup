package guard

import (
	_ "embed"
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

// Check reports whether the guard is installed in this repo.
func Check() Status {
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
