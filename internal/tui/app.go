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
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/agent-worktree/internal/agents"
	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/ollamacheck"
	"github.com/ohanaverse/agent-worktree/internal/rotation"
	"github.com/ohanaverse/agent-worktree/internal/session"
	"github.com/ohanaverse/agent-worktree/internal/themes"
	"github.com/ohanaverse/agent-worktree/internal/worktree"
)

// phase identifies which screen the TUI is currently showing.
type phase int

const (
	phaseList        phase = iota // worktree list (lesson 13)
	phaseAgent                    // agent+command picker (PR 2): picks between configured agents and command drivers before the model screen
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

	list  list.Model
	ready bool

	phase    phase
	agent    string             // current agent name
	tag      string             // active rotation tag group
	rotation *rotation.Rotation // snapshot rotation for the active (agent, tag); set on picker entry
	cfg      *config.Config     // loaded config for the model catalog
	theme    themes.Theme       // active color theme; passed from cmd/wt

	// model picker (the agent+model screen IS the picker)
	models list.Model // bubble/list of agent+tag models

	// agent+command picker (PR 2): user picks an agent or command before the
	// model screen. Built from buildAgentList in selectedEntryMsg; rebuilt when
	// the agent list itself changes (none expected today, but the field lives
	// here so future per-phase rebuilds are one-line).
	agentList list.Model // bubble/list of agent+command items (PR 2)

	// launch state (lesson 16)
	selectedPath string   // worktree path chosen in lesson 13
	prePath      string   // pre-resolved worktree path (-W/--cwd/outside-repo); skips the worktree picker when non-empty
	yolo         bool     // pass skip-permissions flag to the agent
	extraArgs    []string // user passthrough args after --
	initialAgent string   // agent from --agent flag; "" = no agent pinned (agent/command picker is shown)
	resume       resumeModel
	launchModel  config.Model // highlighted model captured when entering phaseResume; launched from the resume choices

	// filter inputs (PR 3b): -T/--tags and -F/--family values from the CLI;
	// forwarded to the model screen so the picker can pre-filter the catalog.
	activeTags   string // comma-delimited tag filter from -T/--tags; "" = no filter
	activeFamily string // comma-delimited family filter from -F/--family; "" = no filter

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

// Init returns the initial command. When a worktree path was pre-resolved
// (-W/--cwd/outside-repo), it skips enumeration and proceeds straight through
// the existing selection flow: unpinned (no --agent) lands on the
// agent/command picker, pinned lands on the model phase. Otherwise it loads
// the worktree/branch picker.
func (m model) Init() tea.Cmd {
	if m.prePath != "" {
		return func() tea.Msg {
			return selectedEntryMsg{entry: worktree.Entry{Path: m.prePath}}
		}
	}
	return loadEntriesCmd()
}

// isTyping reports whether the current phase is actively receiving character
// input, in which case 'q' must not quit. The new-worktree prompt is a
// textinput; the list filters (worktree and agent+command) are bubbles/list's
// incremental filter.
func (m model) isTyping() bool {
	if m.phase == phaseNewWorktree {
		return true
	}
	if m.phase == phaseList && m.ready && m.list.FilterState() == list.Filtering {
		return true
	}
	return m.phase == phaseAgent && m.agentList.FilterState() == list.Filtering
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
		if m.phase == phaseAgent {
			m.agentList.SetSize(msg.Width-2, msg.Height-2)
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
		// Pass the three groups straight to buildList, which interleaves them
		// into the picker (sentinel → worktrees → locals → separator → remotes)
		// and tags the (current)/(default) markers on entryItem.
		m.defaultBranch = msg.defaultBranch
		m.repoRoot = msg.repoRoot
		m.list = buildList(msg.groups, msg.defaultBranch, msg.repoRoot, m.theme, m.width-2, m.height-2)
		m.ready = true

		// Default-branch warning: if the only picker target is the current
		// worktree on the repo default branch (no other worktrees, no local
		// branches), warn the user they're working directly on the protected
		// branch. Same intent as before, just on the new grouped shape.
		if isDefaultBranchOnly(msg.groups, m.defaultBranch) {
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
		} else if m.defaultBranch != "" {
			// Default the cursor to the repo default branch so Enter
			// launches on main without an extra keystroke. Only
			// checked-out worktrees on the default branch are pickable
			// (bare default rows are filtered in buildList), so match
			// by Branch == defaultBranch; the first hit wins because
			// each branch appears at most once in the picker.
			for i, it := range m.list.Items() {
				if ei, ok := it.(entryItem); ok && ei.kind == kindEntry && ei.entry.Branch == m.defaultBranch {
					m.list.Select(i)
					break
				}
			}
		}
		return m, nil
	case selectedEntryMsg:
		// Bare branches (TypeBranch, Path="") have no worktree yet. Create
		// one via EnsureForBranch before proceeding, so the agent launches
		// in a worktree rather than in wt's CWD (cmd.Dir="").
		if msg.entry.Type == worktree.TypeBranch && msg.entry.Path == "" {
			m.creating = true
			return m, ensureBranchWorktreeCmd(m.repoRoot, msg.entry.Branch)
		}
		m.selectedPath = msg.entry.Path
		return m.proceedFromSelectedPath()
	case branchWorktreeCreatedMsg:
		m.creating = false
		if msg.err != nil {
			m.listError = msg.err.Error()
			m.phase = phaseList
			return m, nil
		}
		m.selectedPath = msg.path
		return m.proceedFromSelectedPath()
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
			if m.phase == phaseAgent {
				m.phase = phaseList
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
			if m.phase == phaseNewWorktree {
				m.phase = phaseList
				m.newError = ""
				return m, nil
			}
			return m, tea.Quit
		case "enter":
			switch m.phase {
			case phaseAgent:
				item, ok := m.agentList.SelectedItem().(agentItem)
				if !ok {
					return m, nil
				}
				m.agent = item.name
				if item.command {
					// Command (e.g. shell): no model layer — launch directly
					// by the picked driver's name, not a hardcoded "shell".
					return m.launchCommand(item.name)
				}
				// Agent: validate the model catalog for the agent + active
				// filters (-T/-F), then build the picker list and position
				// the cursor. EligibleModels narrows the catalog by tag
				// and family; the rotation slot is keyed by
				// (agent, firstTag, family) so per-slot state matches
				// the cmd/wt launchFiltered path.
				firstTag := config.FirstTag(m.activeTags, m.cfg.DefaultTag)
				models, err := m.cfg.EligibleModels(m.agent, m.activeTags, m.activeFamily)
				if err != nil {
					m.status = "config error: " + err.Error()
					return m, nil
				}
				if len(models) == 0 {
					m.status = fmt.Sprintf("no models for agent %q in tag %q — edit your config", m.agent, firstTag)
					return m, nil
				}
				return m.enterModelPhase(m.agent, models, firstTag)
			case phaseList:
				if !m.ready {
					return m, nil
				}
				// A bare-branch worktree create is in flight; ignore Enter so
				// a second selection can't race a second `git worktree add`.
				if m.creating {
					return m, nil
				}
				item, ok := m.list.SelectedItem().(entryItem)
				if !ok {
					return m, nil
				}
				if item.kind == kindNewWorktree {
					return m.openNewWorktreePrompt(), nil
				}
				// Separators are non-selectable visual dividers: they carry
				// a zero-value worktree.Entry, so Enter must never forward
				// one to selectedEntryMsg (it would launch the agent in wt's
				// CWD with cmd.Dir=""). Ignore the keypress instead.
				if item.kind == kindSeparator {
					return m, nil
				}
				// Default-branch warning: previously gated here, but the
				// picker title (isDefaultBranchOnly) already flags the
				// "nothing-but-default" case and the user opted into
				// main by the new cursor defaulting. Skip the prompt so
				// Enter launches straight through.
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
				ok, err := ollamacheck.Check(highlighted.model)
				if err != nil {
					m.status = "ollama check failed: " + err.Error()
					return m, nil
				}
				if !ok {
					m.ollamaWarnModel = list.New(buildOllamaChoices(), ThemedListDelegate(m.theme), m.width-2, m.height-2)
					m.ollamaWarnModel.Title = "Model not available: " + highlighted.model.ModelName
					m.phase = phaseOllamaWarn
					return m, nil
				}
				return m.proceedToLaunch()
			case phaseResume:
				if item, ok := m.resume.choices.SelectedItem().(choiceItem); ok {
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
				if item, ok := m.guardWarnModel.SelectedItem().(choiceItem); ok {
					switch item.choice {
					case guardProceedChoice:
						return m, func() tea.Msg { return selectedEntryMsg{entry: m.guardWarnEntry} }
					case guardCancelChoice:
						m.phase = phaseList
						return m, nil
					}
				}
			case phaseOllamaWarn:
				if item, ok := m.ollamaWarnModel.SelectedItem().(choiceItem); ok {
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
			// being typed so 'n' can appear in filter queries, and while a
			// bare-branch create is in flight so the prompt can't be yanked
			// away by the create's completion.
			if m.phase == phaseList && m.ready && !m.creating && m.list.FilterState() != list.Filtering {
				return m.openNewWorktreePrompt(), nil
			}
		}
	}

	if m.phase == phaseModel && m.width > 0 && m.height > 0 {
		var cmd tea.Cmd
		m.models, cmd = m.models.Update(msg)
		return m, cmd
	}
	if m.phase == phaseAgent && m.width > 0 && m.height > 0 {
		var cmd tea.Cmd
		m.agentList, cmd = m.agentList.Update(msg)
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
			body += "\n" + ErrorStyle(m.theme).Render(m.newError)
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
	if m.phase == phaseAgent {
		if m.width <= 0 || m.height <= 0 {
			return "agent picker (waiting for window size)"
		}
		return m.phaseAgentView()
	}
	if !m.ready {
		return m.status
	}
	if m.listError != "" {
		return ErrorStyle(m.theme).Render("error: "+m.listError) + "\n" + m.list.View()
	}
	// A pinned --agent that errors (config error, empty model catalog) sets
	// m.status while staying on the worktree list; render it so the failure
	// is visible instead of silently swallowed by the list view.
	if m.status != "" {
		return ErrorStyle(m.theme).Render(m.status) + "\n" + m.list.View()
	}
	return m.list.View()
}

// launchCommand builds and runs a command (no model layer) — e.g. the shell
// driver — in the selected worktree, skipping the model screen, ollama
// check, and session resume. name is the picked command's driver name; the
// same BuildLaunchCmd path as agents is used, so a future command driver
// launches by its own name rather than being hardcoded to "shell".
func (m model) launchCommand(name string) (model, tea.Cmd) {
	cmd, err := launchAgent(name, config.Model{}, m.selectedPath, false, nil, m.cfg, m.extraArgs)
	if err != nil {
		m.status = "launch failed: " + err.Error()
		return m, nil
	}
	return m, runAndWaitCmd(cmd)
}

// proceedFromSelectedPath continues the launch flow once m.selectedPath is
// resolved — either directly from a worktree/current entry, or after
// EnsureForBranch materialized a worktree for a bare branch. It is the body
// of the old selectedEntryMsg handler, extracted so both the direct pick and
// the post-create path share it.
//
// PR 2: the agent/command picker is the new explicit entry point. When the
// user pinned an agent via --agent, mirror the pre-PR-2 UX: a shell --agent
// skips the picker and launches immediately, any other --agent jumps straight
// to phaseModel. This keeps `wt --agent shell` and `wt --agent claude` exactly
// as they behaved before PR 2 — only the unpinned path shows the picker.
func (m model) proceedFromSelectedPath() (model, tea.Cmd) {
	if m.initialAgent != "" {
		m.agent = m.initialAgent
		if agents.IsCommand(m.agent) {
			return m.launchCommand(m.agent)
		}
		// Pinned agent: skip the picker, run the same model setup
		// that phaseAgent Enter would have run for an agent item.
		// Use EligibleModels so -T/-F filters from CLI narrow the
		// catalog consistently with the phaseAgent Enter path.
		firstTag := config.FirstTag(m.activeTags, m.cfg.DefaultTag)
		m.tag = firstTag
		models, err := m.cfg.EligibleModels(m.agent, m.activeTags, m.activeFamily)
		if err != nil {
			m.status = "config error: " + err.Error()
			return m, nil
		}
		if len(models) == 0 {
			m.status = fmt.Sprintf("no models for agent %q in tag %q — edit your config", m.agent, m.tag)
			return m, nil
		}
		return m.enterModelPhase(m.agent, models, firstTag)
	}
	// Unpinned: build the agent+command picker and hand off to phaseAgent.
	// Clear any prior status so a stale error from a previous picker
	// visit doesn't linger on the freshly rendered screen.
	m.status = ""
	items := buildAgentList(m.cfg)
	m.agentList = list.New(items, ThemedListDelegate(m.theme), m.width-2, m.height-2)
	m.agentList.Title = "Pick an agent or command"
	m.agentList.SetShowStatusBar(false)
	m.phase = phaseAgent
	return m, nil
}

// enterModelPhase sets up the model list for the picker, then either
// transitions to phaseModel (when the eligible list has more than one
// model) or skips the picker entirely (when it has exactly one model).
// The skip path reuses proceedToLaunch so the session-resume prompt and
// the per-slot rotation recording still run — the rotation only advances
// after the user resolves the resume prompt, so a cancel there leaves
// rotation untouched. firstTag is the resolved tag for the slot key.
// Caller is responsible for the len(models) == 0 guard.
func (m model) enterModelPhase(agent string, models []config.Model, firstTag string) (model, tea.Cmd) {
	m.models = buildModelList(models, m.theme, m.width-2, m.height-2)
	m.tag = firstTag
	slot := rotation.SlotFromFlags(agent, firstTag, m.activeFamily)
	m.positionAfterLastLaunched(slot, models)
	if len(models) == 1 {
		return m.proceedToLaunch()
	}
	m.phase = phaseModel
	return m, nil
}

// proceedToLaunch checks for a prior session and either launches the agent
// directly or transitions to the resume prompt. It is the shared flow used
// by both the phaseModel enter handler and the ollama warning proceed choice.
func (m model) proceedToLaunch() (model, tea.Cmd) {
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
	// Native models launch fresh: resuming a session would restore the
	// session's stored model, silently overriding the user's "native" choice
	// (and, for claude, routing a gateway model at the real Anthropic API).
	// Look up a prior session only for non-native models.
	var sess *session.Session
	if !highlighted.model.IsNative() {
		var err error
		sess, err = session.LatestForAgent(m.agent, m.selectedPath)
		if err != nil {
			m.status = "session check failed: " + err.Error()
			return m, nil
		}
	}
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
	m.resume.choices = list.New(buildResumeChoices(sess), ThemedListDelegate(m.theme), m.width-2, m.height-2)
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

// loadEntriesCmd returns a command that enumerates worktrees/branches
// and captures the repo root for the new-worktree prompt. The returned
// message carries the three-group Enumerate shape, which the picker
// (buildList) interleaves into sentinel → worktrees → locals →
// separator → remotes.
func loadEntriesCmd() tea.Cmd {
	return func() tea.Msg {
		root, err := worktree.RepoRoot()
		if err != nil {
			return entriesLoadedMsg{err: err}
		}
		groups, err := worktree.Enumerate(root, root)
		defaultBranch, _ := worktree.DefaultBranch(root)
		return entriesLoadedMsg{groups: groups, defaultBranch: defaultBranch, repoRoot: root, err: err}
	}
}

// isCurrentOnDefaultBranch returns true when the entry is the current
// worktree and its branch matches the repo default branch.
func isCurrentOnDefaultBranch(e worktree.Entry, defaultBranch string) bool {
	return defaultBranch != "" && e.Type == worktree.TypeCurrent && e.Branch == defaultBranch
}

// launchesOnDefaultBranch reports whether selecting this entry would land
// the agent on the repo default branch — the situation the phaseGuardWarn
// prompt exists to gate. A checked-out worktree (Path != "") runs a local
// branch, so only an exact default-branch name matches there: a worktree on
// "feature/main" is not the default branch even though its name ends in
// /main. A bare row (Path == "") covers the local default and any
// remote-tracking form of it (origin/main, upstream/main, ...) that
// EnsureForBranch would turn into a local default-branch worktree; gating
// the remote match on groupKind keeps a local branch whose name ends in
// "/<default>" from spuriously triggering the warning.
func launchesOnDefaultBranch(e worktree.Entry, groupKind worktree.GroupKind, defaultBranch string) bool {
	if defaultBranch == "" {
		return false
	}
	if e.Path != "" {
		return e.Branch == defaultBranch
	}
	if e.Branch == defaultBranch {
		return true
	}
	return groupKind == worktree.GroupRemoteBranches && worktree.IsDefaultBranchForm(e.Branch, defaultBranch)
}

// isDefaultBranchOnly reports whether the user is working directly on the
// repo default branch with nothing else to switch to — the situation where
// a default-branch warning in the picker title is warranted. Returns true
// when the current worktree is on the default branch and there are no
// other branches or worktrees to pick. Any other entry — a different
// branch, a detached worktree, or a second worktree on main — means there is
// something to switch to, so the warning is suppressed.
func isDefaultBranchOnly(groups []worktree.EntryGroup, defaultBranch string) bool {
	if defaultBranch == "" {
		return false
	}
	onDefault := false
	for _, g := range groups {
		for _, e := range g.Entries {
			if isCurrentOnDefaultBranch(e, defaultBranch) {
				onDefault = true
				continue
			}
			// Any other entry means there's something to switch to.
			return false
		}
	}
	return onDefault
}

// entriesLoadedMsg carries the enumeration result to Update. groups is
// the three-group shape from worktree.Enumerate (worktrees / local
// branches / remote branches); the handler passes them straight to
// buildList, which interleaves them with a separator between locals
// and remotes. repoRoot is the git repo root, captured at load time so
// the new-worktree prompt can use it without re-resolving.
type entriesLoadedMsg struct {
	groups        []worktree.EntryGroup
	defaultBranch string
	repoRoot      string
	err           error
}

// repoRootFor resolves the git repository root that owns path. path is a
// worktree or repo-root directory; the result is the primary repo root (the
// parent of any .worktrees subdir), or "" if path is not inside a git repo.
//
// Used by Run() to seed model.repoRoot when prePath is set so the new-worktree
// prompt (currently gated on m.ready, but reachable via any future UI
// restoration to the worktree list) has a valid directory to pass to git.
func repoRootFor(path string) string {
	if path == "" || path == "." {
		return ""
	}
	out, err := exec.Command("git", "-C", path, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Run starts the TUI in alternate-screen mode and returns when it quits.
// agent is the --agent flag value ("" = no agent pinned; the agent/command
// picker is shown). tags is the -T/--tags flag value (comma-delimited; "" =
// no filter). family is the -F/--family flag value (comma-delimited; "" =
// no filter). extraArgs are the user's passthrough args after --. theme is
// the active color theme, loaded by cmd/wt. prePath, when non-empty, is a
// pre-resolved worktree path (-W/--cwd/outside-repo): the worktree picker is
// skipped and control starts at the agent/command picker (or model phase when
// agent is pinned). When prePath is inside a git repo, repoRoot is seeded so
// the new-worktree prompt has a valid directory even if it becomes reachable
// from the pre-path entry point.
func Run(yolo bool, agent, tags, family string, extraArgs []string, theme themes.Theme, prePath string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	p := tea.NewProgram(model{
		status:       "loading worktrees...",
		cfg:          cfg,
		theme:        theme,
		yolo:         yolo,
		initialAgent: agent,
		activeTags:   tags,
		activeFamily: family,
		extraArgs:    extraArgs,
		prePath:      prePath,
		repoRoot:     repoRootFor(prePath),
	}, tea.WithAltScreen())
	currentProgram = p
	_, err = p.Run()
	return err
}
