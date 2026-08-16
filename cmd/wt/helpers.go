package main

import (
	"os/exec"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"github.com/ohanaverse/agent-worktree/internal/config"
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

// defaultAgent returns the agent to launch when --agent is not given: the
// first configured agent, falling back to "claude".
func defaultAgent(cfg *config.Config) string {
	if cfg != nil && len(cfg.Agents) > 0 {
		return cfg.Agents[0].Name
	}
	return "claude"
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
