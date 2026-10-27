package tui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rootclaim"
	"github.com/ohanaverse/local-ai-setup/wt/internal/sessionclaim"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tuilayout"
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

// rootChecked is what the last sharedRootCheck learned, for the run that
// follows it: the root it looked up (so holdRoot does not ask git again) and
// the PIDs of the sessions the person saw on the screen and chose to continue
// beside (so holdRoot asks only about a session that came after). It is a
// package var because the run commands are built without the model.
var rootChecked struct {
	dir      string
	root     rootclaim.Root
	accepted map[int]bool
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
	root, ok, err := rootclaim.Lookup(m.selectedPath)
	if !ok || err != nil {
		return nil
	}
	rootChecked.dir, rootChecked.root, rootChecked.accepted = m.selectedPath, root, nil
	self, err := root.Self("")
	if err != nil {
		return nil
	}
	others, err := sessionclaim.Others(root.CommonDir, self)
	if err != nil || len(others) == 0 {
		return nil
	}
	return &sharedRootState{from: m.phase, root: root.Path, others: others}
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
		rootChecked.accepted = map[int]bool{}
		for _, o := range m.sharedRoot.others {
			rootChecked.accepted[o.PID] = true
		}
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
// reason it could not be. Its first line and its keys stay on screen at any
// height (tuilayout.MessageAlone cuts the sessions in between when they do not
// fit), and a long line is wrapped at the terminal's width, not cut, so a long
// root path is shown whole.
func (m model) sharedRootView() string {
	st := m.sharedRoot
	lines := strings.Split(strings.TrimSuffix(sessionclaim.Menu(st.root, st.others, time.Now()), "\n"), "\n")
	top, keys := lines[0], lines[len(lines)-1]
	body := strings.Join(lines[1:len(lines)-1], "\n")
	if st.creating {
		body += "\n\ncreating a new worktree…"
	}
	if st.err != "" {
		body += "\n\n" + ErrorStyle(m.theme).Render("new worktree: "+st.err)
	}
	// Wrapped first, so a long root path is shown whole instead of cut; the
	// height cut then counts the wrapped lines.
	wrap := lipgloss.NewStyle().Width(m.width)
	if m.width > 0 {
		top, body, keys = wrap.Render(top), wrap.Render(body), wrap.Render(keys)
	}
	return tuilayout.MessageAlone(top, body, keys, m.width, m.height, lipgloss.NewStyle())
}

// holdRoot claims the repository root for this session when dir is the root,
// for the agent's run: the run commands call it just before the agent starts
// and the returned release just after it exits. The person has already chosen
// (the screen, or no other session when the picker looked, or Allow), so only
// a session that appeared since is asked about: on the plain terminal the
// alt screen has been released by then, as in a launch without the picker.
// c continues beside it, q (or Esc) is ok == false and the run does not
// start; w is not offered any more, the agent being built for this directory.
// Any other directory gets no claim. A claim that cannot be written is one
// note on w, never a refusal. release is never nil.
func holdRoot(dir, agent string, in io.Reader, w io.Writer) (release func(), ok bool) {
	release = func() {}
	if dir == "" {
		return release, true
	}
	root, found := rootChecked.root, dir == rootChecked.dir
	if !found {
		var isRoot bool
		var err error
		if root, isRoot, err = rootclaim.Lookup(dir); !isRoot {
			return release, true
		} else if err != nil {
			fmt.Fprintf(w, "wt: note: %s\n", sessionclaim.CheckFailed(root.Path, err))
			return release, true
		}
	}
	self, err := root.Self(agent)
	if err == nil {
		declined := false
		var held func()
		held, _, err = sessionclaim.Join(root.CommonDir, self, func(others []sessionclaim.Claim) bool {
			var newcomers []sessionclaim.Claim
			for _, o := range others {
				if !SharedRoot.Allow && !rootChecked.accepted[o.PID] {
					newcomers = append(newcomers, o)
				}
			}
			if len(newcomers) == 0 {
				return true
			}
			for {
				switch rootclaim.Choose(root, newcomers, in, w) {
				case 'c':
					return true
				case 'w':
					fmt.Fprintln(w, "wt: a new worktree cannot be chosen now; c to continue here, q to quit")
				default:
					declined = true
					return false
				}
			}
		})
		if err == nil {
			return held, !declined
		}
	}
	fmt.Fprintf(w, "wt: note: %s\n", sessionclaim.CheckFailed(root.Path, err))
	return release, true
}
