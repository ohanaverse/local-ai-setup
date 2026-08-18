// Package tui implements the Bubble Tea terminal UI for wt.
//
// Lesson 12 establishes the app shell: a single screen that shows a status,
// responds to q/esc/ctrl+c, and demonstrates the Model/Update/View cycle.
// Lesson 13 layers on the worktree/branch picker using bubbles/list.
// Lesson 14 adds the agent+model screen reached after picking a worktree.
// Lesson 15 added a separate model browser, opened with `m`; the picker
// list now subsumes that role (the agent+model screen shows all
// agent-compatible models in the active tag, sourced from config.toml).
package tui

import (
	"fmt"
	"os/exec"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/guard"
	"github.com/ohanaverse/agent-worktree/internal/ollamacheck"
	"github.com/ohanaverse/agent-worktree/internal/rotation"
	"github.com/ohanaverse/agent-worktree/internal/session"
	"github.com/ohanaverse/agent-worktree/internal/worktree"
)

// phase identifies which screen the TUI is currently showing.
type phase int

const (
	phaseList        phase = iota // worktree list (lesson 13)
	phaseModel                    // agent+model picker (lesson 14 + lesson 15 merged)
	phaseResume                   // resume prompt (lesson 16)
	phaseGuardWarn                // confirm before launching on default branch
	phaseOllamaWarn               // confirm before launching with unavailable ollama model
	phaseNewWorktree              // create-new-worktree prompt
)

// resumeModel holds the resume-prompt state for phaseResume (lesson 16).
type resumeModel struct {
	session *session.Session
	choices list.Model
}

// model holds the entire UI state.
type model struct {
	status string
	width  int
	height int

	entries []worktree.Entry
	list    list.Model
	ready   bool

	phase    phase
	agent    string              // current agent name
	tag      string              // active rotation tag group
	rotation *rotation.Rotation  // snapshot rotation for the active (agent, tag); set on picker entry and on 'd' tag toggle
	cfg      *config.Config      // loaded config for the model catalog

	// model picker (the agent+model screen IS the picker)
	models    list.Model // bubble/list of agent+tag models
	modelsTag string     // tag the list was built for; rebuild on change
	modelsFor string     // agent the list was built for; rebuild on change

	// launch state (lesson 16)
	selectedPath string   // worktree path chosen in lesson 13
	yolo         bool     // pass skip-permissions flag to the agent
	extraArgs    []string // user passthrough args after --
	initialAgent string   // agent from --agent flag; "" = use config default
	resume       resumeModel
	launchModel  config.Model // highlighted model captured when entering phaseResume; launched from the resume choices

	// default-branch guard warning
	defaultBranch  string         // repo default branch (e.g. main)
	guardWarnModel list.Model     // confirmation choices for default-branch launch
	guardWarnEntry worktree.Entry // the entry being confirmed

	// ollama availability warning
	ollamaWarnModel list.Model // confirmation choices for unavailable model

	// new-worktree prompt (this lesson)
	newInput         textinput.Model
	newError         string
	pendingHighlight string // branch name to focus after re-enumerating
	repoRoot         string // cached from entriesLoadedMsg to avoid a second rev-parse
	creating         bool   // true while a create is in flight (guards double-Enter)
	listError        string // reload error shown above the list when ready
}

// Init returns the initial command: load worktrees/branches.
func (m model) Init() tea.Cmd {
	return loadEntriesCmd()
}

// isShellAgent returns true when the active agent is "shell", which has no
// model screen, no ollama check, and no session resume.
func isShellAgent(agent string) bool { return agent == "shell" }

// isTyping reports whether the current phase is actively receiving character
// input, in which case 'q' must not quit. The new-worktree prompt is a
// textinput; the list filter is bubbles/list's incremental filter.
func (m model) isTyping() bool {
	if m.phase == phaseNewWorktree {
		return true
	}
	return m.phase == phaseList && m.ready && m.list.FilterState() == list.Filtering
}

// openNewWorktreePrompt transitions to the new-worktree prompt, resetting the
// input and any prior error. Shared by the sentinel-Enter and 'n' entry points
// so prompt setup stays in one place.
func (m model) openNewWorktreePrompt() model {
	m.phase = phaseNewWorktree
	m.newInput = newInputModel(m.width)
	m.newError = ""
	return m
}

// Update handles messages and returns the new state plus optional commands.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if m.ready {
			m.list.SetSize(msg.Width-2, msg.Height-2)
		}
		if m.phase == phaseModel {
			m.models.SetSize(msg.Width-2, msg.Height-2)
		}
		if m.phase == phaseResume {
			m.resume.choices.SetSize(msg.Width-2, msg.Height-2)
		}
		if m.phase == phaseNewWorktree {
			m.newInput.Width = msg.Width - 4
		}
	case entriesLoadedMsg:
		if msg.err != nil {
			if m.ready {
				// Reload after a create: the list is still usable, but the
				// error must be visible — View renders it above the list.
				m.listError = msg.err.Error()
			} else {
				m.status = "error: " + msg.err.Error()
			}
			return m, nil
		}
		m.listError = ""
		m.entries = msg.entries
		m.defaultBranch = msg.defaultBranch
		m.repoRoot = msg.repoRoot
		m.list = buildList(msg.entries, m.width-2, m.height-2)
		m.ready = true

		if len(msg.entries) == 1 && isCurrentOnDefaultBranch(msg.entries[0], msg.defaultBranch) {
			m.list.Title = "WARNING: you are on the default branch (" + msg.defaultBranch + ")"
		}
		// Apply a pending highlight (set after a successful
		// new-worktree create) by selecting the matching entry. If
		// the branch isn't found (shouldn't happen post-create),
		// leave the cursor at its default.
		if m.pendingHighlight != "" {
			for i, it := range m.list.Items() {
				if ei, ok := it.(entryItem); ok && ei.kind == kindEntry && ei.entry.Branch == m.pendingHighlight {
					m.list.Select(i)
					break
				}
			}
			m.pendingHighlight = ""
		}
		return m, nil
	case selectedEntryMsg:
		// Resolve the agent: --agent flag wins, else the config default.
		m.selectedPath = msg.entry.Path
		if m.initialAgent != "" {
			m.agent = m.initialAgent
		} else {
			m.agent = m.cfg.DefaultAgent()
		}
		// Shell agent: skip the model screen entirely — go straight to launch.
		if isShellAgent(m.agent) {
			return m.launchShell()
		}
		m.tag = m.cfg.DefaultTag
		// Validation gate: refuse to enter the picker with an empty list.
		// This catches misconfigured catalogs (agent with no compatible models
		// in the default tag) before the user gets a confusing screen.
		models, err := m.cfg.ModelsForAgentAndTag(m.agent, m.tag)
		if err != nil {
			m.status = "config error: " + err.Error()
			return m, nil
		}
		if len(models) == 0 {
			m.status = fmt.Sprintf("no models for agent %q in tag %q — edit your config", m.agent, m.tag)
			return m, nil
		}
		m.phase = phaseModel
		m.models = buildModelList(models, m.width-2, m.height-2)
		m.modelsFor = m.agent
		m.modelsTag = m.tag
		// Snapshot the rotation over the picker's filtered list so
		// rotation's view of "the code tag" matches the picker's,
		// and position the cursor on the model after the last-
		// launched one (fall back to index 0 if no last-launched
		// exists or its ID is no longer in the snapshot).
		m.positionAfterLastLaunched(m.tag, models)
		return m, nil
	case launchDoneMsg:
		if msg.err != nil {
			m.status = "agent exited: " + msg.err.Error()
		}
		return m, tea.Quit
	case newWorktreeCreatedMsg:
		m.creating = false
		if msg.err != nil {
			m.newError = msg.err.Error()
			return m, nil
		}
		m.pendingHighlight = msg.name
		m.phase = phaseList
		return m, loadEntriesCmd()
	case tea.KeyMsg:
		switch msg.String() {
		case "q":
			// 'q' must type a character while the user is entering text
			// (new-worktree prompt, list filter) rather than quitting.
			if !m.isTyping() {
				return m, tea.Quit
			}
		case "ctrl+c":
			return m, tea.Quit
		case "esc":
			// esc is phase-aware: pop back from a nested screen, else quit.
			if m.phase == phaseResume {
				m.phase = phaseModel
				return m, nil
			}
			if m.phase == phaseGuardWarn {
				m.phase = phaseList
				return m, nil
			}
			if m.phase == phaseOllamaWarn {
				m.phase = phaseModel
				return m, nil
			}
			if m.phase == phaseNewWorktree {
				m.phase = phaseList
				m.newError = ""
				return m, nil
			}
			return m, tea.Quit
		case "enter":
			switch m.phase {
			case phaseList:
				if !m.ready {
					return m, nil
				}
				item, ok := m.list.SelectedItem().(entryItem)
				if !ok {
					return m, nil
				}
				if item.kind == kindNewWorktree {
					return m.openNewWorktreePrompt(), nil
				}
				if isCurrentOnDefaultBranch(item.entry, m.defaultBranch) {
					installed := guard.Check() == guard.Installed
					m.guardWarnEntry = item.entry
					m.guardWarnModel = list.New(buildGuardChoices(item.entry.Branch, installed), list.NewDefaultDelegate(), m.width-2, m.height-2)
					m.guardWarnModel.Title = "Launch on default branch?"
					m.phase = phaseGuardWarn
					return m, nil
				}
				return m, func() tea.Msg { return selectedEntryMsg{entry: item.entry} }
			case phaseModel:
				// The highlighted list item is what gets launched.
				highlighted, ok := m.models.SelectedItem().(modelItem)
				if !ok {
					return m, nil
				}
				// Check ollama availability before launching. Rotation is
				// NOT recorded here: the user can still cancel the ollama
				// warning or the resume prompt, or the check can fail, and
				// in none of those cases did a launch happen. Recording
				// lives in launchAndRecord, the single commit point.
				if ollamacheck.IsOllamaModel(highlighted.model) {
					ok, err := ollamacheck.Available(highlighted.model.ModelName)
					if err != nil {
						m.status = "ollama check failed: " + err.Error()
						return m, nil
					}
					if !ok {
						m.ollamaWarnModel = list.New(buildOllamaChoices(), list.NewDefaultDelegate(), m.width-2, m.height-2)
						m.ollamaWarnModel.Title = "Model not available: " + highlighted.model.ModelName
						m.phase = phaseOllamaWarn
						return m, nil
					}
				}
				return m.proceedToLaunch()
			case phaseResume:
				if item, ok := m.resume.choices.SelectedItem().(resumeItem); ok {
					switch item.choice {
					case cancelChoice:
						m.phase = phaseModel
						return m, nil
					case freshChoice:
						cmd, err := launchAgent(m.agent, m.launchModel, m.selectedPath, m.yolo, nil, m.cfg, m.extraArgs)
						if err != nil {
							m.status = "launch failed: " + err.Error()
							return m, nil
						}
						return m.launchAndRecord(cmd)
					case resumeChoice:
						cmd, err := launchAgent(m.agent, m.launchModel, m.selectedPath, m.yolo, m.resume.session, m.cfg, m.extraArgs)
						if err != nil {
							m.status = "launch failed: " + err.Error()
							return m, nil
						}
						return m.launchAndRecord(cmd)
					}
				}
			case phaseGuardWarn:
				if item, ok := m.guardWarnModel.SelectedItem().(guardItem); ok {
					switch item.choice {
					case guardProceedChoice:
						return m, func() tea.Msg { return selectedEntryMsg{entry: m.guardWarnEntry} }
					case guardCancelChoice:
						m.phase = phaseList
						return m, nil
					}
				}
			case phaseOllamaWarn:
				if item, ok := m.ollamaWarnModel.SelectedItem().(ollamaItem); ok {
					switch item.choice {
					case ollamaProceedChoice:
						return m.proceedToLaunch()
					case ollamaCancelChoice:
						m.phase = phaseModel
						return m, nil
					}
				}
			case phaseNewWorktree:
				if m.creating {
					// A create is already in flight; ignore the second Enter
					// so two concurrent `git worktree add` calls can't race.
					return m, nil
				}
				if errMsg := validateNewWorktreeName(m.newInput.Value()); errMsg != "" {
					m.newError = errMsg
					return m, nil
				}
				m.newError = ""
				m.creating = true
				return m, ensureNewWorktreeCmd(m.repoRoot, m.newInput.Value())
			}
		case "n":
			// Open the new-worktree prompt. Skip while the list filter is
			// being typed so 'n' can appear in filter queries.
			if m.phase == phaseList && m.ready && m.list.FilterState() != list.Filtering {
				return m.openNewWorktreePrompt(), nil
			}
		case "d":
			if m.phase == phaseModel {
				prevTag := m.tag
				newTag := oppositeTag(m.tag)
				// Empty-tag defense: if the toggled-to tag has no models,
				// restore the previous tag. Report the empty tag (newTag),
				// not the one the user is staying on.
				m.tag = newTag
				models, err := m.cfg.ModelsForAgentAndTag(m.agent, m.tag)
				if err != nil || len(models) == 0 {
					m.tag = prevTag
					m.status = fmt.Sprintf("tag %q has no models for agent %q", newTag, m.agent)
					return m, nil
				}
				// Rebuild the list and position the cursor on the model
				// after the new tag's last-launched. Cross-skip is gone;
				// each tag rotates independently.
				m.models = buildModelList(models, m.width-2, m.height-2)
				m.modelsTag = m.tag
				m.positionAfterLastLaunched(m.tag, models)
				// Return here: bubbles/list binds `d` to NextPage, so
				// falling through to m.models.Update(msg) would advance
				// the freshly rebuilt list's page on a multi-page tag.
				return m, nil
			}
		}
	}

	if m.phase == phaseModel && m.width > 0 && m.height > 0 {
		var cmd tea.Cmd
		m.models, cmd = m.models.Update(msg)
		return m, cmd
	}
	if m.ready && m.phase == phaseList {
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		return m, cmd
	}
	if m.phase == phaseResume && m.width > 0 && m.height > 0 {
		var cmd tea.Cmd
		m.resume.choices, cmd = m.resume.choices.Update(msg)
		return m, cmd
	}
	if m.phase == phaseGuardWarn && m.width > 0 && m.height > 0 {
		var cmd tea.Cmd
		m.guardWarnModel, cmd = m.guardWarnModel.Update(msg)
		return m, cmd
	}
	if m.phase == phaseOllamaWarn && m.width > 0 && m.height > 0 {
		var cmd tea.Cmd
		m.ollamaWarnModel, cmd = m.ollamaWarnModel.Update(msg)
		return m, cmd
	}
	if m.phase == phaseNewWorktree && m.width > 0 && m.height > 0 {
		var cmd tea.Cmd
		m.newInput, cmd = m.newInput.Update(msg)
		return m, cmd
	}
	return m, nil
}

// View renders the screen as a string.
func (m model) View() string {
	if m.phase == phaseNewWorktree {
		if m.width <= 0 || m.height <= 0 {
			return "new worktree prompt (waiting for window size)"
		}
		body := m.newInput.View()
		if m.newError != "" {
			body += "\n" + errorStyle.Render(m.newError)
		}
		if m.creating {
			body += "\ncreating " + m.newInput.Value() + "…"
		} else {
			body += "\n[enter] create   [esc] cancel"
		}
		return body
	}
	if m.phase == phaseResume {
		if m.width <= 0 || m.height <= 0 {
			return "resume prompt (waiting for window size)"
		}
		return m.resume.choices.View() + "\n[enter] choose   [esc] back"
	}
	if m.phase == phaseOllamaWarn {
		if m.width <= 0 || m.height <= 0 {
			return "ollama availability warning (waiting for window size)"
		}
		return m.ollamaWarnModel.View() + "\n[enter] choose   [esc] back"
	}
	if m.phase == phaseGuardWarn {
		if m.width <= 0 || m.height <= 0 {
			return "default-branch warning (waiting for window size)"
		}
		return m.guardWarnModel.View() + "\n[enter] choose   [esc] back"
	}
	if m.phase == phaseModel {
		if m.width <= 0 || m.height <= 0 {
			return "model picker (waiting for window size)"
		}
		return m.phaseModelView()
	}
	if !m.ready {
		return m.status
	}
	if m.listError != "" {
		return errorStyle.Render("error: "+m.listError) + "\n" + m.list.View()
	}
	return m.list.View()
}

// launchShell builds and runs a shell command (or interactive bash) in the
// selected worktree, skipping the model screen, ollama check, and session
// resume. Used by the shell agent path.
func (m model) launchShell() (model, tea.Cmd) {
	cmd, err := launchAgent("shell", config.Model{}, m.selectedPath, false, nil, m.cfg, m.extraArgs)
	if err != nil {
		m.status = "launch failed: " + err.Error()
		return m, nil
	}
	return m, runAndWaitCmd(cmd)
}

// proceedToLaunch checks for a prior session and either launches the agent
// directly or transitions to the resume prompt. It is the shared flow used
// by both the phaseModel enter handler and the ollama warning proceed choice.
func (m model) proceedToLaunch() (model, tea.Cmd) {
	sess, err := session.LatestForAgent(m.agent, m.selectedPath)
	if err != nil {
		m.status = "session check failed: " + err.Error()
		return m, nil
	}
	// The highlighted list item is what gets launched, regardless
	// of any other state. m.current is gone; m.models is the
	// single source of truth.
	highlighted, ok := m.models.SelectedItem().(modelItem)
	if !ok {
		m.status = "no model selected"
		return m, nil
	}
	// Capture the model so launchAndRecord records exactly this pick in
	// both the no-session and resume paths, without re-reading the picker.
	m.launchModel = highlighted.model
	if sess == nil {
		cmd, err := launchAgent(m.agent, highlighted.model, m.selectedPath, m.yolo, nil, m.cfg, m.extraArgs)
		if err != nil {
			m.status = "launch failed: " + err.Error()
			return m, nil
		}
		return m.launchAndRecord(cmd)
	}
	m.phase = phaseResume
	m.resume.session = sess
	m.resume.choices = list.New(buildResumeChoices(sess), list.NewDefaultDelegate(), m.width-2, m.height-2)
	m.resume.choices.Title = "Resume previous session?"
	return m, nil
}

// launchAndRecord records the model as last-launched (so the next picker
// entry advances rotation) and then runs the agent. Recording happens
// here — the single commit point reached only after the ollama check and
// resume prompt have been satisfied — so a cancelled ollama warning, a
// cancelled resume prompt, or a failed ollama check never advances the
// rotation. The state write is best-effort: a failure surfaces in m.status
// and the launch still proceeds.
func (m model) launchAndRecord(cmd *exec.Cmd) (model, tea.Cmd) {
	if m.rotation != nil {
		if err := m.rotation.RecordLaunch(m.launchModel); err != nil {
			m.status = "rotation state not saved: " + err.Error()
		}
	}
	return m, runAndWaitCmd(cmd)
}

// loadEntriesCmd returns a command that enumerates worktrees/branches.
// loadEntriesCmd returns a command that enumerates worktrees/branches
// and captures the repo root for the new-worktree prompt.
func loadEntriesCmd() tea.Cmd {
	return func() tea.Msg {
		root, err := worktree.RepoRoot()
		if err != nil {
			return entriesLoadedMsg{err: err}
		}
		entries, err := worktree.Enumerate(root, root)
		defaultBranch, _ := worktree.DefaultBranch(root)
		return entriesLoadedMsg{entries: entries, defaultBranch: defaultBranch, repoRoot: root, err: err}
	}
}

// isCurrentOnDefaultBranch returns true when the entry is the current
// worktree and its branch matches the repo default branch.
func isCurrentOnDefaultBranch(e worktree.Entry, defaultBranch string) bool {
	return defaultBranch != "" && e.Type == worktree.TypeCurrent && e.Branch == defaultBranch
}

// entriesLoadedMsg carries the enumeration result to Update.
// repoRoot is the git repo root, captured at load time so the
// new-worktree prompt can use it without re-resolving.
type entriesLoadedMsg struct {
	entries       []worktree.Entry
	defaultBranch string
	repoRoot      string
	err           error
}

// Run starts the TUI in alternate-screen mode and returns when it quits.
// agent is the --agent flag value ("" = use config default). extraArgs are
// the user's passthrough args after --.
func Run(yolo bool, agent string, extraArgs []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	p := tea.NewProgram(model{
		status:       "loading worktrees...",
		cfg:          cfg,
		yolo:         yolo,
		initialAgent: agent,
		extraArgs:    extraArgs,
	}, tea.WithAltScreen())
	currentProgram = p
	_, err = p.Run()
	return err
}
