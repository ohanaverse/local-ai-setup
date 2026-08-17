// Package tui implements the Bubble Tea terminal UI for wt.
//
// Lesson 12 establishes the app shell: a single screen that shows a status,
// responds to q/esc/ctrl+c, and demonstrates the Model/Update/View cycle.
// Lesson 13 layers on the worktree/branch picker using bubbles/list.
// Lesson 14 adds the agent+model screen reached after picking a worktree.
// Lesson 15 adds the model browser, opened from the agent+model screen
// with `m`, which lets the user pick any model from the registry.
package tui

import (
	"fmt"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
	phaseList       phase = iota // worktree list (lesson 13)
	phaseModel                   // agent+model screen (lesson 14)
	phaseBrowser                 // model browser (lesson 15)
	phaseResume                  // resume prompt (lesson 16)
	phaseGuardWarn               // confirm before launching on default branch
	phaseOllamaWarn              // confirm before launching with unavailable ollama model
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
	agent    string         // current agent name
	tag      string         // active rotation tag group
	otherTag string         // tag group to cross-skip against during rotation
	current  config.Model   // currently shown model
	cfg      *config.Config // loaded config for the model catalog

	// launch state (lesson 16)
	selectedPath string   // worktree path chosen in lesson 13
	yolo         bool     // pass skip-permissions flag to the agent
	extraArgs    []string // user passthrough args after --
	initialAgent string   // agent from --agent flag; "" = use config default
	resume       resumeModel

	// model browser (lesson 15)
	browser      list.Model     // browser list widget
	browserCache []config.Model // snapshot of registry.Discover, per browser-open
	browserTag   string         // "" = all models; otherwise a tag like "code"
	sourceCycle  int            // 0=all, 1=curated, 2=discovered

	// default-branch guard warning
	defaultBranch  string         // repo default branch (e.g. main)
	guardWarnModel list.Model     // confirmation choices for default-branch launch
	guardWarnEntry worktree.Entry // the entry being confirmed

	// ollama availability warning
	ollamaWarnModel list.Model // confirmation choices for unavailable model
}

// Init returns the initial command: load worktrees/branches.
func (m model) Init() tea.Cmd {
	return loadEntriesCmd()
}

// isShellAgent returns true when the active agent is "shell", which has no
// model screen, no ollama check, and no session resume.
func isShellAgent(agent string) bool { return agent == "shell" }

// Update handles messages and returns the new state plus optional commands.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if m.ready {
			m.list.SetSize(msg.Width-2, msg.Height-2)
		}
		if m.phase == phaseBrowser {
			m.refreshBrowser()
		}
		if m.phase == phaseResume {
			m.resume.choices.SetSize(msg.Width-2, msg.Height-2)
		}
	case entriesLoadedMsg:
		if msg.err != nil {
			m.status = "error: " + msg.err.Error()
			return m, nil
		}
		m.entries = msg.entries
		m.defaultBranch = msg.defaultBranch
		m.list = buildList(msg.entries, m.width-2, m.height-2)
		m.ready = true

		if len(msg.entries) == 1 && isCurrentOnDefaultBranch(msg.entries[0], msg.defaultBranch) {
			m.list.Title = "WARNING: you are on the default branch (" + msg.defaultBranch + ")"
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
		m.phase = phaseModel
		m.tag = m.cfg.DefaultTag
		m.current = firstModel(m.cfg, m.tag)
		return m, nil
	case launchDoneMsg:
		if msg.err != nil {
			m.status = "agent exited: " + msg.err.Error()
		}
		return m, tea.Quit
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "esc":
			// esc is phase-aware: pop back from a nested screen, else quit.
			if m.phase == phaseBrowser {
				m.phase = phaseModel
				return m, nil
			}
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
				// Check ollama availability before launching.
				if ollamacheck.IsOllamaModel(m.current) {
					ok, err := ollamacheck.Available(m.current.ModelName)
					if err != nil {
						m.status = "ollama check failed: " + err.Error()
						return m, nil
					}
					if !ok {
						m.ollamaWarnModel = list.New(buildOllamaChoices(), list.NewDefaultDelegate(), m.width-2, m.height-2)
						m.ollamaWarnModel.Title = "Model not available: " + m.current.ModelName
						m.phase = phaseOllamaWarn
						return m, nil
					}
				}

				return m.proceedToLaunch()
			case phaseBrowser:
				if item, ok := m.browser.SelectedItem().(modelItem); ok {
					m.current = item.model
					m.phase = phaseModel
				}
			case phaseResume:
				if item, ok := m.resume.choices.SelectedItem().(resumeItem); ok {
					switch item.choice {
					case cancelChoice:
						m.phase = phaseModel
						return m, nil
					case freshChoice:
						cmd, err := launchAgent(m.agent, m.current, m.selectedPath, m.yolo, nil, m.cfg, m.extraArgs)
						if err != nil {
							m.status = "launch failed: " + err.Error()
							return m, nil
						}
						return m, tea.Batch(tea.Quit, runAndWaitCmd(cmd))
					case resumeChoice:
						cmd, err := launchAgent(m.agent, m.current, m.selectedPath, m.yolo, m.resume.session, m.cfg, m.extraArgs)
						if err != nil {
							m.status = "launch failed: " + err.Error()
							return m, nil
						}
						return m, tea.Batch(tea.Quit, runAndWaitCmd(cmd))
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
					case ollamaSkipChoice:
						// Rotate to next model and return to phaseModel.
						rot := rotation.ForTag(m.cfg, m.tag)
						next, ok := rot.Next(m.otherTag)
						if ok {
							m.current = next
						}
						m.phase = phaseModel
						return m, nil
					case ollamaCancelChoice:
						m.phase = phaseModel
						return m, nil
					}
				}
			}
		case "r":
			if m.phase == phaseModel {
				// Rotate to the next model in the active tag group, skipping
				// whatever the other tag group last used (cross-tag skip).
				rot := rotation.ForTag(m.cfg, m.tag)
				next, ok := rot.Next(m.otherTag)
				if ok {
					m.current = next
				}
			}
		case "m":
			if m.phase == phaseModel {
				// Open the model browser. Reset the cache so each open
				// re-discovers; filter toggles inside the browser reuse it.
				m.phase = phaseBrowser
				m.browserCache = nil
				m.refreshBrowser()
			}
		case "d":
			if m.phase == phaseModel {
				// Toggle the active tag group between code and design.
				if m.tag == "code" {
					m.tag, m.otherTag = "design", "code"
				} else {
					m.tag, m.otherTag = "code", "design"
				}
				// Re-resolve the shown model to the new group's first entry.
				m.current = firstModel(m.cfg, m.tag)
			}
		case "f":
			if m.phase == phaseBrowser {
				// Toggle tag filter between "" (all) and m.tag.
				if m.browserTag == "" {
					m.browserTag = m.tag
				} else {
					m.browserTag = ""
				}
				m.refreshBrowser()
			}
		case "c":
			if m.phase == phaseBrowser {
				// Cycle source filter: 0=all, 1=curated, 2=discovered.
				m.sourceCycle = (m.sourceCycle + 1) % 3
				m.refreshBrowser()
			}
		}
	}

	if m.ready && m.phase == phaseList {
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		return m, cmd
	}
	if m.phase == phaseBrowser && m.width > 0 && m.height > 0 {
		var cmd tea.Cmd
		m.browser, cmd = m.browser.Update(msg)
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
	return m, nil
}

// View renders the screen as a string.
func (m model) View() string {
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
	if m.phase == phaseBrowser {
		return m.browserView()
	}
	if m.phase == phaseModel {
		style := lipgloss.NewStyle().Padding(2, 2)
		return style.Render(
			fmt.Sprintf("agent : %s\nmodel : %s\n\ntag : %s\n\n"+
				"[r] rotate   [m] browse models   [enter] launch   [q] quit",
				m.agent, m.current.ID, m.tag))
	}
	if !m.ready {
		return m.status
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
	return m, tea.Batch(tea.Quit, runAndWaitCmd(cmd))
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
	if sess == nil {
		cmd, err := launchAgent(m.agent, m.current, m.selectedPath, m.yolo, nil, m.cfg, m.extraArgs)
		if err != nil {
			m.status = "launch failed: " + err.Error()
			return m, nil
		}
		return m, tea.Batch(tea.Quit, runAndWaitCmd(cmd))
	}
	m.phase = phaseResume
	m.resume.session = sess
	m.resume.choices = list.New(buildResumeChoices(sess), list.NewDefaultDelegate(), m.width-2, m.height-2)
	m.resume.choices.Title = "Resume previous session?"
	return m, nil
}

// firstModel returns the first model in a tag group, or a "(none)" placeholder.
func firstModel(cfg *config.Config, tag string) config.Model {
	none := config.Model{ID: "(none)", ProviderID: "", Location: config.LocationCloud}
	if cfg == nil {
		return none
	}
	ms := cfg.ModelsWithTag(tag)
	if len(ms) == 0 {
		return none
	}
	return ms[0]
}

// loadEntriesCmd returns a command that enumerates worktrees/branches.
func loadEntriesCmd() tea.Cmd {
	return func() tea.Msg {
		root, err := worktree.RepoRoot()
		if err != nil {
			return entriesLoadedMsg{err: err}
		}
		entries, err := worktree.Enumerate(root, root)
		defaultBranch, _ := worktree.DefaultBranch(root)
		return entriesLoadedMsg{entries: entries, defaultBranch: defaultBranch, err: err}
	}
}

// isCurrentOnDefaultBranch returns true when the entry is the current
// worktree and its branch matches the repo default branch.
func isCurrentOnDefaultBranch(e worktree.Entry, defaultBranch string) bool {
	return defaultBranch != "" && e.Type == worktree.TypeCurrent && e.Branch == defaultBranch
}

// entriesLoadedMsg carries the enumeration result to Update.
type entriesLoadedMsg struct {
	entries       []worktree.Entry
	defaultBranch string
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
