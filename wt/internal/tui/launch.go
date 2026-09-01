package tui

import (
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/session"
)

// launchDoneMsg is emitted after the agent subprocess exits.
type launchDoneMsg struct {
	err error
}

// currentProgram holds the running tea.Program so runAndWaitCmd can
// release/restore the terminal. It is set in Run().
var currentProgram *tea.Program

// pendingSummary is populated by runAndWaitCmd after the agent subprocess
// exits. Run() reads it after p.Run() returns and prints it to the parent
// terminal — the only point in the TUI lifecycle where stdout is the parent
// terminal rather than the alt-screen buffer. Printing inside runAndWaitCmd
// lands in the alt-screen and is discarded when the TUI shuts down, so the
// capture-then-emit pattern is required. Reset by Run() before launch.
var pendingSummary string

// launchAgent builds the command for agent/model in worktreePath, optionally
// appending passthrough args and a resume flag for claude or opencode. It
// delegates to agents.BuildLaunchCmd so the launch construction logic lives
// in one place.
func launchAgent(agent string, m config.Model, worktreePath string, yolo bool, sess *session.Session, cfg *config.Config, extraArgs []string) (*exec.Cmd, error) {
	return agents.BuildLaunchCmd(agent, m, worktreePath, yolo, sess, cfg, extraArgs)
}

// runAndWaitCmd releases the TUI, runs the agent with stdio wired to the
// terminal, restores the TUI, captures the post-run summary line into
// pendingSummary (consumed by Run() after p.Run() returns), and returns a
// launchDoneMsg.
func runAndWaitCmd(cmd *exec.Cmd, agent string, m config.Model) tea.Cmd {
	return func() tea.Msg {
		if currentProgram != nil {
			currentProgram.ReleaseTerminal()
			// defer the restore so a panic in cmd.Run() still returns
			// the terminal to the alt-screen state; otherwise the user's
			// shell is left in raw mode with no TUI frame.
			defer func() {
				if currentProgram != nil {
					_ = currentProgram.RestoreTerminal()
				}
			}()
		}
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		start := time.Now()
		err := cmd.Run()
		// Capture (do not print) the summary line. Printing here would
		// land inside the alt-screen buffer, which bubbletea discards at
		// tea.Quit shutdown. Run() reads pendingSummary after p.Run()
		// returns and prints it to the parent terminal — the only point
		// in the TUI lifecycle where stdout reaches the user's terminal.
		pendingSummary = agents.Summary(agent, m, time.Since(start))
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

// buildResumeChoices creates the resume prompt list items. The first item is
// the default cursor position for bubbles/list, so Start fresh is placed at
// index 0 to match the user's "default to start fresh" preference — Resume is
// offered but opt-in, and Cancel backs out without launching.
func buildResumeChoices(sess *session.Session) []list.Item {
	items := []list.Item{
		choiceItem{choice: freshChoice, title: "Start fresh", desc: "Launch without resuming a session"},
		choiceItem{choice: cancelChoice, title: "Cancel", desc: "Return to agent+model screen"},
	}
	if sess != nil {
		items = append(items, choiceItem{
			choice: resumeChoice,
			title:  fmt.Sprintf("Resume %s", sess.ID),
			desc:   session.RelativeTime(sess.MTime),
		})
	}
	return items
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
