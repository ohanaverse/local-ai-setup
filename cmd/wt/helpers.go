package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/guard"
	"github.com/spf13/cobra"
)

// mustGetString returns a string flag value, ignoring the error. The flag is
// always registered, so GetString cannot fail here.
func mustGetString(cmd *cobra.Command, name string) string {
	v, _ := cmd.Flags().GetString(name)
	return v
}

// yolo reports whether the --yolo flag was set.
func yolo(cmd *cobra.Command) bool {
	v, _ := cmd.Flags().GetBool("yolo")
	return v
}

// inGitRepo reports whether the current directory is inside a git repo.
func inGitRepo() bool {
	return inGitRepoAt(".")
}

// inGitRepoAt reports whether dir is inside a git repo. Separated from
// inGitRepo so tests can point it at a temp repo without chdir'ing the
// process.
func inGitRepoAt(dir string) bool {
	return exec.Command("git", "-C", dir, "rev-parse", "--git-dir").Run() == nil
}

// defaultModel returns the model to launch for an agent: the agent's native
// model (e.g. claude/native) if present, else the first model in the default
// tag group.
func defaultModel(cfg *config.Config, agent string) config.Model {
	for _, m := range cfg.Models {
		if m.ID == agent+"/native" {
			return m
		}
	}
	ms := cfg.ModelsWithTag(cfg.DefaultTag)
	if len(ms) > 0 {
		return ms[0]
	}
	return config.Model{ID: "(none)", Location: config.LocationCloud}
}

// borderStyle is the shared table border colour.
var borderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

// renderTable renders a simple lipgloss table from headers and rows.
func renderTable(headers []string, rows [][]string) string {
	t := table.New().
		Headers(headers...).
		Rows(rows...).
		Border(lipgloss.NormalBorder()).
		BorderStyle(borderStyle).
		BorderRow(true)
	return t.Render()
}

// maybeInstallGuard installs the main guard when inside a git repo. Errors
// are written to stderr and ignored so that a guard-install failure does not
// block the agent launch. This matches the bash engine's best-effort
// behavior.
func maybeInstallGuard() {
	if !inGitRepo() {
		return
	}
	if _, err := guard.Install(); err != nil {
		fmt.Fprintf(os.Stderr, "wt: failed to auto-install main guard: %v\n", err)
	}
}

// checkGuardStatus returns the guard status in the current repo. It returns
// an error when not inside a git repo so callers can report a clear message.
func checkGuardStatus() (guard.Status, error) {
	if !inGitRepo() {
		return guard.Err, fmt.Errorf("not inside a git repository")
	}
	return guard.Check(), nil
}

// removeGuard uninstalls the guard in the current repo. It returns an error
// when not inside a git repository.
func removeGuard() error {
	if !inGitRepo() {
		return fmt.Errorf("not inside a git repository")
	}
	return guard.Uninstall()
}
