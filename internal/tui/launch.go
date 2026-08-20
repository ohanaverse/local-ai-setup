package tui

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/agent-worktree/internal/agents"
	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/session"
)

// launchDoneMsg is emitted after the agent subprocess exits.
type launchDoneMsg struct {
	err error
}

// currentProgram holds the running tea.Program so runAndWaitCmd can
// release/restore the terminal. It is set in Run().
var currentProgram *tea.Program

// launchAgent builds the command for agent/model in worktreePath, optionally
// appending passthrough args and a resume flag for claude or opencode. It
// delegates to agents.BuildLaunchCmd so the launch construction logic lives
// in one place.
func launchAgent(agent string, m config.Model, worktreePath string, yolo bool, sess *session.Session, cfg *config.Config, extraArgs []string) (*exec.Cmd, error) {
	return agents.BuildLaunchCmd(agent, m, worktreePath, yolo, sess, cfg, extraArgs)
}

// runAndWaitCmd releases the TUI, runs the agent with stdio wired to the
// terminal, restores the TUI, and returns a launchDoneMsg.
func runAndWaitCmd(cmd *exec.Cmd) tea.Cmd {
	return func() tea.Msg {
		if currentProgram != nil {
			currentProgram.ReleaseTerminal()
		}
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		err := cmd.Run()
		if currentProgram != nil {
			currentProgram.RestoreTerminal()
		}
		return launchDoneMsg{err: err}
	}
}

// resumeOption identifies a choice in the resume prompt.
type resumeOption int

const (
	resumeChoice resumeOption = iota
	freshChoice
	cancelChoice
)

// choiceItem adapts a prompt choice to list.Item. The three prompt screens
// (resume, guard, ollama) share this single type; each keeps its own named
// choice enum, stored in the any-typed choice field.
type choiceItem struct {
	choice any
	title  string
	desc   string
}

func (c choiceItem) FilterValue() string { return c.title }
func (c choiceItem) Title() string       { return c.title }
func (c choiceItem) Description() string { return c.desc }

// buildResumeChoices creates the resume prompt list items.
func buildResumeChoices(sess *session.Session) []list.Item {
	items := []list.Item{
		choiceItem{choice: freshChoice, title: "Start fresh", desc: "Launch without resuming a session"},
		choiceItem{choice: cancelChoice, title: "Cancel", desc: "Return to agent+model screen"},
	}
	if sess != nil {
		items = append([]list.Item{choiceItem{
			choice: resumeChoice,
			title:  fmt.Sprintf("Resume %s", sess.ID),
			desc:   session.RelativeTime(sess.MTime),
		}}, items...)
	}
	return items
}

// guardChoice identifies a choice in the default-branch guard prompt.
type guardChoice int

const (
	guardProceedChoice guardChoice = iota
	guardCancelChoice
)

// buildGuardChoices creates the default-branch confirmation list items.
func buildGuardChoices(branch string, installed bool) []list.Item {
	hint := "commits to " + branch + " are blocked"
	if !installed {
		hint = "WARNING: main guard is NOT installed — commits to " + branch + " are NOT blocked"
	}
	return []list.Item{
		choiceItem{choice: guardProceedChoice, title: "Proceed anyway", desc: hint},
		choiceItem{choice: guardCancelChoice, title: "Cancel", desc: "Return to worktree list"},
	}
}

// ollamaChoice identifies a choice in the ollama availability prompt.
type ollamaChoice int

const (
	ollamaProceedChoice ollamaChoice = iota
	ollamaCancelChoice
)

// buildOllamaChoices creates the ollama availability confirmation list
// items. With implicit rotation-by-launch, the user can navigate the
// picker with up/down and press Enter on a different model; there is
// no "skip to next" shortcut.
func buildOllamaChoices() []list.Item {
	return []list.Item{
		choiceItem{choice: ollamaProceedChoice, title: "Proceed anyway", desc: "Launch with unavailable model (may fail)"},
		choiceItem{choice: ollamaCancelChoice, title: "Cancel", desc: "Return to the agent+model screen"},
	}
}
