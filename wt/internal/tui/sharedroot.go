package tui

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/sessionclaim"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
)

// RootCheck is cmd/wt's word on the shared-root check for this run. Allow is
// --allow-shared-root: no screen, and the root is claimed all the same.
type RootCheck struct {
	Allow bool
}

// SharedRoot is set by cmd/wt before Run. It is a package var, not a Run
// argument, so the many test doubles of Run's signature stay as they are.
var SharedRoot RootCheck

// sharedRootState is the shared-root screen (phaseSharedRoot): the root, the
// other live sessions holding it, the "new worktree" choice in flight, and the
// phase the screen was shown over, which the launch goes on from.
type sharedRootState struct {
	from     phase
	root     string
	others   []sessionclaim.Claim
	creating bool
	err      string
}

// sharedRootWorktreeMsg answers the screen's "new worktree" choice.
type sharedRootWorktreeMsg struct {
	path string
	err  error
}

// rootSession is what a claim on dir needs. root is "" when dir is not a
// repository's root (a linked worktree, a subdirectory, a bare repository, no
// repository); then nothing else is set. err is a failure to read the common
// directory or this process's start time, with root set so a note can name it.
func rootSession(dir, agent string) (root, commonDir string, self sessionclaim.Claim, err error) {
	root, ok := worktree.RootOf(dir)
	if !ok {
		return "", "", sessionclaim.Claim{}, nil
	}
	commonDir, err = gitOutput(root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return root, "", sessionclaim.Claim{}, err
	}
	branch, err := gitOutput(root, "branch", "--show-current")
	if err != nil || branch == "" {
		branch = "(detached)"
	}
	self, err = sessionclaim.Self(root, branch, agent)
	return root, commonDir, self, err
}

// gitOutput runs git in dir and returns its trimmed output.
func gitOutput(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	return strings.TrimSpace(string(out)), err
}

// sharedRootCheck is the TUI's one shared-root check, made where every launch
// learns its directory (proceedFromSelectedPath), before any agent, model or
// profile starts. It returns the screen's state when m.selectedPath is the
// root and other live sessions hold it, else nil. It only reads: the claim is
// written when the agent starts (holdRoot). A claims directory that cannot be
// read shows no screen; holdRoot then fails the same way and says so.
func (m model) sharedRootCheck() *sharedRootState {
	if SharedRoot.Allow {
		return nil
	}
	root, commonDir, self, err := rootSession(m.selectedPath, "")
	if root == "" || err != nil {
		return nil
	}
	others, err := sessionclaim.Others(commonDir, self)
	if err != nil || len(others) == 0 {
		return nil
	}
	return &sharedRootState{from: m.phase, root: root, others: others}
}

// handleSharedRootKey takes the screen's keys: c continues in the root, w
// creates a new worktree and launches there, q, Esc and ctrl+c quit without
// launching. Other keys do nothing, and so does every key but ctrl+c while
// the new worktree is being created.
func (m model) handleSharedRootKey(msg tea.KeyMsg) (model, tea.Cmd) {
	key := msg.String()
	if key == "ctrl+c" {
		return m, tea.Quit
	}
	if m.sharedRoot.creating {
		return m, nil
	}
	switch key {
	case "c", "C":
		m.phase, m.sharedRoot = m.sharedRoot.from, nil
		return m.proceedWithPath()
	case "w", "W":
		st := *m.sharedRoot
		st.creating, st.err = true, ""
		m.sharedRoot = &st
		return m, sharedRootWorktreeCmd(st.root)
	case "q", "Q", "esc":
		return m, tea.Quit
	}
	return m, nil
}

// sharedRootWorktreeCmd creates the "new worktree" choice's worktree: a new
// branch from HEAD under root/.worktrees, named as the launch without the
// picker names it, so it is never the default branch.
func sharedRootWorktreeCmd(root string) tea.Cmd {
	return func() tea.Msg {
		path, err := worktree.EnsureForName(root, sessionclaim.NewWorktreeName(os.Getpid(), time.Now()))
		return sharedRootWorktreeMsg{path: path, err: err}
	}
}

// handleSharedRootWorktree launches in the worktree the screen created, or
// shows why it could not be created and stays on the screen. A linked
// worktree needs no check and gets no claim.
func (m model) handleSharedRootWorktree(msg sharedRootWorktreeMsg) (model, tea.Cmd) {
	if m.phase != phaseSharedRoot || m.sharedRoot == nil {
		return m, nil
	}
	if msg.err != nil {
		st := *m.sharedRoot
		st.creating, st.err = false, msg.err.Error()
		m.sharedRoot = &st
		return m, nil
	}
	m.phase, m.sharedRoot = m.sharedRoot.from, nil
	m.selectedPath = msg.path
	return m.proceedWithPath()
}

// sharedRootView is phaseSharedRoot's screen: the menu the launch without the
// picker prints (sessionclaim.Menu), and the worktree being created or the
// reason it could not be. It is wrapped, not cut, at the terminal's width, so
// a long root path is shown whole.
func (m model) sharedRootView() string {
	st := m.sharedRoot
	body := strings.TrimSuffix(sessionclaim.Menu(st.root, st.others, time.Now()), "\n")
	if st.creating {
		body += "\n\ncreating a new worktree…"
	}
	if st.err != "" {
		body += "\n\n" + ErrorStyle(m.theme).Render("new worktree: "+st.err)
	}
	return lipgloss.NewStyle().Width(m.width).Render(body)
}

// holdRoot claims the repository root for this session when dir is the root,
// for the agent's run: the run commands call it just before the agent starts
// and the returned release just after it exits. The person has already chosen
// (the screen, or no other session when the picker looked, or Allow), so a
// session that appeared since is accepted. Any other directory gets no claim. A claim that cannot be written is one note on w,
// never a refusal. release is never nil.
func holdRoot(dir, agent string, w io.Writer) (release func()) {
	release = func() {}
	if dir == "" {
		return release
	}
	root, commonDir, self, err := rootSession(dir, agent)
	if root == "" {
		return release
	}
	if err == nil {
		var held func()
		held, _, err = sessionclaim.Join(commonDir, self, func([]sessionclaim.Claim) bool { return true })
		if err == nil {
			return held
		}
	}
	fmt.Fprintf(w, "wt: note: %s\n", sessionclaim.CheckFailed(root, err))
	return release
}
