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

// launchMsg tells the app to quit the TUI and run the agent.
type launchMsg struct {
	cmd *exec.Cmd
}

// launchDoneMsg is emitted after the agent subprocess exits.
type launchDoneMsg struct {
	err error
}

// currentProgram holds the running tea.Program so runAndWaitCmd can
// release/restore the terminal. It is set in Run().
var currentProgram *tea.Program

// launchAgent builds the command for agent/model in worktreePath, optionally
// appending a resume flag for claude or opencode.
func launchAgent(agent string, m config.Model, worktreePath string, yolo bool, sess *session.Session) (*exec.Cmd, error) {
	d := agents.ByName(agent)
	if d == nil {
		return nil, fmt.Errorf("unknown agent: %s", agent)
	}
	cmd, err := agents.Command(d, m, yolo, worktreePath)
	if err != nil {
		return nil, err
	}
	if sess != nil {
		switch agent {
		case "claude":
			cmd.Args = append(cmd.Args, "--resume", sess.ID)
		case "opencode":
			cmd.Args = append(cmd.Args, "--session", sess.ID)
		}
	}
	return cmd, nil
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

// resumeItem adapts a resume prompt choice to list.Item.
type resumeItem struct {
	choice resumeOption
	title  string
	desc   string
}

func (r resumeItem) FilterValue() string { return r.title }
func (r resumeItem) Title() string       { return r.title }
func (r resumeItem) Description() string { return r.desc }

// buildResumeChoices creates the resume prompt list items.
func buildResumeChoices(sess *session.Session) []list.Item {
	items := []list.Item{
		resumeItem{choice: freshChoice, title: "Start fresh", desc: "Launch without resuming a session"},
		resumeItem{choice: cancelChoice, title: "Cancel", desc: "Return to agent+model screen"},
	}
	if sess != nil {
		items = append([]list.Item{resumeItem{
			choice: resumeChoice,
			title:  fmt.Sprintf("Resume %s", sess.ID),
			desc:   session.RelativeTime(sess.MTime),
		}}, items...)
	}
	return items
}
