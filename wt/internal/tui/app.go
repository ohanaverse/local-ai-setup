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
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/ollamacheck"
	"github.com/ohanaverse/local-ai-setup/wt/internal/refcount"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rotation"
	"github.com/ohanaverse/local-ai-setup/wt/internal/session"
	"github.com/ohanaverse/local-ai-setup/wt/internal/survey"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
)

// phase identifies which screen the TUI is currently showing.
type phase int

const (
	phaseList           phase = iota // worktree list (lesson 13)
	phaseAgent                       // agent+command picker (PR 2): picks between configured agents and command drivers before the model screen
	phaseModel                       // agent+model picker (lesson 14 + lesson 15 merged)
	phaseResume                      // resume prompt (lesson 16)
	phaseOllamaWarn                  // confirm before launching with unavailable ollama model
	phaseNewWorktree                 // create-new-worktree prompt
	phaseStarting                    // a start runs through the lifecycle engine, with live progress
	phaseReplaceConfirm              // confirm replacing the running occupant before starting
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

	phase phase
	agent string         // current agent name
	tag   string         // active rotation tag group
	cfg   *config.Config // loaded config for the model catalog
	theme themes.Theme   // active color theme; passed from cmd/wt

	// model picker (the agent+model screen IS the picker)
	models list.Model // bubble/list of agent+tag models
	// tableModels is the model list the picker's table was built from,
	// captured in enterModelPhase; refreshTable re-probes the inventory
	// and rebuilds the table from exactly these models.
	tableModels []config.Model
	// pendingSelect is the model id a table refresh wants the cursor on. It is
	// resolved against the rebuilt list inside the refresh message's handler,
	// so a rebuild cannot silently move the user's cursor to another model.
	pendingSelect string
	// refreshGen numbers the picker's table: enterModelPhase, refreshTable and
	// beginStart each bump it, and a tableRefreshedMsg is applied only when it
	// carries the current value, so a slow probe from a superseded refresh or an
	// earlier picker session cannot overwrite a newer table.
	refreshGen int

	// agent+command picker (PR 2): user picks an agent or command before the
	// model screen. Built from buildAgentList in selectedEntryMsg; rebuilt when
	// the agent list itself changes (none expected today, but the field lives
	// here so future per-phase rebuilds are one-line).
	agentList list.Model // bubble/list of agent+command items (PR 2)

	// launch state (lesson 16)
	selectedPath string   // worktree path chosen in lesson 13
	prePath      string   // pre-resolved worktree path (-W/--cwd/outside-repo); skips the worktree picker when non-empty
	yolo         bool     // pass skip-permissions flag to the agent
	allowReplace bool     // --replace mode: the -M start path may stop a running occupant without the dialog
	extraArgs    []string // user passthrough args after --
	initialAgent string   // agent from --agent flag; "" = no agent pinned (agent/command picker is shown)
	pinnedModel  string   // model from --model flag; "" = no model pinned (model picker is shown)
	resume       resumeModel
	launchModel  config.Model // highlighted model captured when entering phaseResume; launched from the resume choices

	// filter inputs (PR 3b): -T/--tags and -F/--family values from the CLI;
	// forwarded to the model screen so the picker can pre-filter the catalog.
	activeTags   string // comma-delimited tag filter from -T/--tags; "" = no filter
	activeFamily string // comma-delimited family filter from -F/--family; "" = no filter

	// default-branch guard warning (title-only; the confirm prompt was removed)
	defaultBranch string // repo default branch (e.g. main)

	// ollama availability warning
	ollamaWarnModel list.Model // confirmation choices for unavailable model

	// start-on-select flow (2026-09-20): Enter on a start row runs the model
	// through the lifecycle engine with live progress; the replace-confirm
	// dialog appears when Start reports the provider's single slot occupied
	// (or unknowable) and AllowReplace was false.
	start    *startState   // in-flight start (phaseStarting); nil when idle
	startRun int           // monotonically increasing run id; stale messages are dropped by id
	replace  *replaceState // replace-confirm dialog (phaseReplaceConfirm)

	// new-worktree prompt (this lesson)
	newInput         textinput.Model
	newError         string
	pendingHighlight string // branch name to focus after re-enumerating
	repoRoot         string // cached from entriesLoadedMsg to avoid a second rev-parse
	creating         bool   // true while a create is in flight (guards double-Enter)
	listError        string // reload error shown above the list when ready

	// fatalErr, when set, is paired with a tea.Quit return so the whole
	// program exits; Run() below surfaces it as the function's returned
	// error, matching the non-TUI path's exit-with-message behavior
	// (cmd/wt/main.go's runLaunchPath). Nothing currently sets this
	// field — enterModelPhase's route-backs return a status, never a
	// fatal error — but it remains wired into Run() for any future
	// genuinely-fatal condition.
	fatalErr error
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
// textinput; the list filters (worktree, agent+command, and model picker)
// are bubbles/list's incremental filter.
func (m model) isTyping() bool {
	if m.phase == phaseNewWorktree {
		return true
	}
	if m.phase == phaseList && m.ready && m.list.FilterState() == list.Filtering {
		return true
	}
	if m.phase == phaseAgent && m.agentList.FilterState() == list.Filtering {
		return true
	}
	return m.phase == phaseModel && m.models.FilterState() == list.Filtering
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
	case startStageMsg, startTickMsg, startDoneMsg:
		// The case list is exactly the set handleStartMsg handles, so it
		// always has something to say.
		return m.handleStartMsg(msg)
	case tableRefreshedMsg:
		// A refresh can land after the user moved on (a second start, a quit);
		// only the picker it was built for should absorb it.
		if m.phase != phaseModel || msg.gen != m.refreshGen {
			return m, nil
		}
		return m, m.applyRefreshedTable(msg)
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
		if m.phase == phaseReplaceConfirm {
			m.replace.choices.SetSize(msg.Width-2, msg.Height-2)
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
			selectFirstEntry(&m.list, func(ei entryItem) bool {
				return ei.entry.Branch == m.pendingHighlight
			})
			m.pendingHighlight = ""
		} else {
			// Default the cursor to the repo root so Enter relaunches
			// where the user already is without an extra keystroke.
			// buildList always pins the (current) entry at index 1
			// (right after the sentinel) when one exists, so this is
			// the starting selection regardless of which branch the
			// repo root is on.
			selectFirstEntry(&m.list, func(ei entryItem) bool {
				return ei.label == "(current)"
			})
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
		// An in-flight start owns the keyboard: esc/q/ctrl+c cancel and a
		// further press quits; every other key is ignored (before the picker
		// wrap-around below, which must not move the cursor mid-start).
		if m.phase == phaseStarting && m.start != nil {
			return m.handleStartKey(msg)
		}
		// Model picker wrap-around: bubble/list does not wrap by default.
		// Skip while the filter input is active (or has just been opened,
		// which resets the cursor to index 0 via GoToStart) so "j"/"k"
		// typed into a filter query reach the text input instead of being
		// swallowed as navigation.
		//
		// Uses VisibleItems(), not Items(): bubbles/list v1.0.0's
		// Index()/Select() are visible-item-coordinate once a filter is
		// applied (FilterApplied, not just Filtering — this guard only
		// skips Filtering), so the wrap edges must be computed in the
		// same filtered coordinates the cursor moves in.
		if m.phase == phaseModel && m.models.FilterState() != list.Filtering {
			items := m.models.VisibleItems()
			if len(items) > 1 {
				switch msg.String() {
				case "up", "k":
					if m.models.Index() == 0 {
						m.models.Select(len(items) - 1)
						return m, nil
					}
				case "down", "j":
					if m.models.Index() == len(items)-1 {
						m.models.Select(0)
						return m, nil
					}
				}
			}
		}
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
			// Every phase transition clears m.status so a stale error from
			// the previous screen (e.g. invalid pinned -M) does not follow
			// the user back to the worktree list with no way to dismiss it.
			if m.phase == phaseAgent {
				m.phase = phaseList
				m.status = ""
				return m, nil
			}
			if m.phase == phaseResume {
				m.phase = phaseModel
				m.status = ""
				return m, nil
			}
			if m.phase == phaseOllamaWarn {
				m.phase = phaseModel
				m.status = ""
				return m, nil
			}
			if m.phase == phaseReplaceConfirm {
				m.phase = phaseModel
				m.status = ""
				return m, nil
			}
			if m.phase == phaseNewWorktree {
				m.phase = phaseList
				m.newError = ""
				m.status = ""
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
				// An agent that cannot launch at all (uninstalled) carries
				// a non-empty issue. Surface it inline instead of letting
				// the user advance to a model screen that can never
				// succeed. (An installed-but-unconfigured agent takes the
				// item.passthrough branch below, not this one.)
				if item.issue != "" {
					m.status = "cannot launch " + item.name + ": " + item.issue
					return m, nil
				}
				if item.passthrough {
					// Installed but no config.toml entry (issue #147):
					// launch directly, no model layer to resolve.
					return m.launchPassthrough(item.name)
				}
				// Agent: validate the model catalog for the agent + active
				// filters (-T/-F), then build the picker list and position
				// the cursor. The full catalog is fetched ONCE here and
				// narrowed in place by EligibleModelsIn, so the registry is
				// walked once per entry.
				firstTag := config.FirstTag(m.activeTags, m.cfg.DefaultTag)
				fullCatalog, err := m.cfg.ModelsForAgent(m.agent)
				if err != nil {
					m.status = "config error: " + err.Error()
					return m, nil
				}
				models, err := m.cfg.EligibleModelsIn(m.agent, fullCatalog, m.activeTags, m.activeFamily)
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
				// While the '/' filter input is focused, Enter must apply
				// the filter query (bubbles/list's own AcceptWhileFiltering
				// binding), not launch the highlighted row. Falling through
				// to the bottom m.models.Update(msg) below lets bubbles/list
				// handle it. Without this guard, Enter always ran the launch
				// path regardless of filter state — committing an ollama
				// check (and potentially rotation/usage state) for whatever
				// model happened to be highlighted underneath the filter
				// overlay, instead of narrowing the list as the user typed.
				if m.models.FilterState() == list.Filtering {
					break
				}
				// The highlighted list item is what gets launched.
				highlighted, ok := m.models.SelectedItem().(*modelItem)
				if !ok {
					return m, nil
				}
				if highlighted.start {
					// A start row: run the model through the lifecycle engine
					// instead of launching. start and blocked are mutually
					// exclusive — renderTable sets exactly one per row, and its
					// discovered-row case clears start before setting blocked —
					// so this order is a guard, not a precedence rule.
					// --replace is the -M pin's permission, not a blanket
					// one: any other start row still gets the dialog.
					return m.beginStart(highlighted, m.allowReplace && highlighted.model.ID == m.pinnedModel)
				}
				if highlighted.blocked != "" {
					m.status = highlighted.blocked
					return m, nil
				}
				// Check ollama availability before launching. Rotation is
				// NOT recorded here: the user can still cancel the ollama
				// warning or the resume prompt, or the check can fail, and
				// in none of those cases did a launch happen. Recording
				// lives in launchAndRecord, the single commit point. The
				// check is skipped when this agent×model pairing will
				// actually route through LiteLLM (any upstream may serve
				// the model) and when the model is not served by ollama —
				// ollamacheck only probes the local ollama daemon. Uses the
				// per-model resolved route rather than the raw
				// cfg.IsLitellm() toggle so a protocol-forced LiteLLM route
				// (e.g. codex+ollama) isn't spuriously blocked by this
				// local-availability check.
				route, _ := m.cfg.ResolveRoute(highlighted.model, agents.ProtocolsFor(m.agent))
				if !route.Litellm && ollamacheck.IsOllamaModel(highlighted.model) {
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
			case phaseReplaceConfirm:
				if item, ok := m.replace.choices.SelectedItem().(choiceItem); ok {
					switch item.choice {
					case replaceCancelChoice:
						m.phase = phaseModel
						return m, nil
					case replaceProceedChoice:
						return m.beginStart(m.replace.item, true)
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
		clampModelSelection(&m)
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
	if m.phase == phaseOllamaWarn && m.width > 0 && m.height > 0 {
		var cmd tea.Cmd
		m.ollamaWarnModel, cmd = m.ollamaWarnModel.Update(msg)
		return m, cmd
	}
	if m.phase == phaseReplaceConfirm && m.width > 0 && m.height > 0 {
		var cmd tea.Cmd
		m.replace.choices, cmd = m.replace.choices.Update(msg)
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
	if m.phase == phaseStarting {
		if m.width <= 0 || m.height <= 0 {
			return "start progress (waiting for window size)"
		}
		return m.startingView()
	}
	if m.phase == phaseReplaceConfirm {
		if m.width <= 0 || m.height <= 0 {
			return "replace confirm (waiting for window size)"
		}
		return m.replace.choices.View() + "\n[enter] choose   [esc] back"
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
	return m, runAndWaitCmd(cmd, name, config.Model{})
}

// launchPassthrough builds and runs a model-driven agent that has no
// config.toml entry, via the buildPassthrough seam — no model routing,
// equivalent to running the installed binary directly. Mirrors
// launchCommand's shape; the zero config.Model passed to runAndWaitCmd
// produces the same command-agent-like post-exit behavior (no survey, no
// stop picker, no price notice).
func (m model) launchPassthrough(name string) (model, tea.Cmd) {
	cmd, err := buildPassthrough(name, m.selectedPath, m.yolo, m.extraArgs)
	if err != nil {
		m.status = "launch failed: " + err.Error()
		return m, nil
	}
	return m, runAndWaitCmd(cmd, name, config.Model{})
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
		// The installed check runs first, regardless of configured state:
		// there is no binary to launch bare or otherwise (decision #3 of the
		// passthrough design — the installed check is early and
		// pinned-agent-only).
		if !installed(m.agent) {
			m.status = "cannot launch " + m.agent + ": not installed — install the binary"
			return m, nil
		}
		if !agents.IsConfigured(m.cfg, m.agent) {
			// Installed but no config.toml entry (issue #147): launch
			// directly, unless the user pinned a model that cannot be
			// honored — an explicit pin must be surfaced, never silently
			// dropped (decision #5).
			if m.pinnedModel != "" {
				m.status = fmt.Sprintf("agent %q is not configured; cannot pin model %q", m.agent, m.pinnedModel)
				return m, nil
			}
			return m.launchPassthrough(m.agent)
		}
		// Pinned agent: skip the picker, run the same model setup
		// that phaseAgent Enter would have run for an agent item.
		// EligibleModelsIn narrows the single fetched catalog by
		// -T/-F filters consistently with the phaseAgent Enter path.
		firstTag := config.FirstTag(m.activeTags, m.cfg.DefaultTag)
		m.tag = firstTag
		fullCatalog, err := m.cfg.ModelsForAgent(m.agent)
		if err != nil {
			m.status = "config error: " + err.Error()
			return m, nil
		}
		models, err := m.cfg.EligibleModelsIn(m.agent, fullCatalog, m.activeTags, m.activeFamily)
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

// runInventory is a test seam: production probes the live local providers.
var runInventory = realRunInventory

// realRunInventory is the production implementation of the runInventory seam: a
// live probe of every local provider.
func realRunInventory(cfg *config.Config) localmodels.Snapshot { return localmodels.Inventory(cfg) }

// enterModelPhase builds the selector table for the agent and either
// transitions to phaseModel or, when exactly one launchable row exists, skips
// the picker. The skip path reuses proceedToLaunch so the session-resume prompt
// and the global rotation recording still run — the rotation only advances
// after the user resolves the resume prompt, so a cancel there leaves
// rotation untouched.
//
// models is the agent's eligible list; local models that are not
// running appear as start rows (Enter runs the lifecycle engine; only blocked
// rows show a hint — their reason) rather than being hidden.
//
// A pinned model (-M) is resolved against the same rows the table shows, so
// the pin and the picker can never disagree: a launch row (cloud, or a local
// model the probe reports running) proceeds straight to launch, an idle local
// start row is selected and shown for the user to start, and a blocked row
// routes back with the row's own reason. A mismatch routes to phaseAgent (the
// bad pin is cleared so re-entry validates fresh).
//
// The header's tag line and hideDiscovered answer two different questions
// and are intentionally not the same value: the tag line shows firstTag (the
// first tag when -T lists several, else DefaultTag), while hideDiscovered
// tracks an explicit -T/-F narrowing, because discovered models are
// unregistered and cannot be filtered by tag or family. With a DefaultTag
// set and no -T, the list is therefore unfiltered by tag
// (EligibleModelsIn only filters when a tag set is present) while the
// header still shows a tag.
//
// models must be non-empty: buildRows emits one row per model, so an empty
// table is only possible from an empty input, and both callers (the phaseAgent
// Enter path and proceedFromSelectedPath's pinned-agent path) already guard
// len(models) == 0 with their own status message. A future caller must do the
// same — there is no empty-table branch here to catch it.
//
// The only route-back is a rejected -M pin: one with no row at all (outside
// the eligible list), or one whose row is blocked.
func (m model) enterModelPhase(agent string, models []config.Model, firstTag string) (model, tea.Cmd) {
	m.tag = firstTag

	routeBack := func(status string) (model, tea.Cmd) {
		m.status = status
		m.pinnedModel = ""
		items := buildAgentList(m.cfg)
		m.agentList = list.New(items, ThemedListDelegate(m.theme), m.width-2, m.height-2)
		m.agentList.Title = "Pick an agent or command"
		m.agentList.SetShowStatusBar(false)
		m.phase = phaseAgent
		return m, nil
	}

	m.tableModels = models
	m.refreshGen++

	inv := runInventory(m.cfg)
	snap := &inv

	// One Rotation for both the marker and the cursor (each New() scans the
	// config dir). A missing rotation.state yields "". tableFor also reads it
	// for the marker; the cursor logic below reuses rot so the config dir is
	// scanned once.
	rot := rotation.New()
	tbl, lastID := m.tableFor(agent, models, snap, rot)

	delegate := ThemedListDelegate(m.theme)
	delegate.ShowDescription = false
	delegate.SetSpacing(0)
	listItems := make([]list.Item, len(tbl.items))
	idIndex := make(map[string]int, len(tbl.items))
	for i, it := range tbl.items {
		listItems[i] = it
		idIndex[it.model.ID] = i
	}
	ml := list.New(listItems, delegate, m.width-2, m.height-2)
	ml.Title = tbl.header
	styleTableTitle(&ml, m.theme)
	ml.SetShowStatusBar(false)
	m.models = ml

	// The -M pin's verdict comes from the same rows the table shows, so the
	// pin can never disagree with what the user is looking at. That is why
	// the inventory probe above is not skipped for a pinned model: the rows
	// need it. The trade — a probe even when the pin turns out unusable — is
	// deliberate, and replaces the old gate's reject-before-probing order.
	if m.pinnedModel != "" {
		idx, ok := idIndex[m.pinnedModel]
		if !ok {
			return routeBack(fmt.Sprintf("model %q is not in the eligible list for agent %q", m.pinnedModel, agent))
		}
		it := tbl.items[idx]
		switch {
		case it.blocked != "":
			// A row the pin cannot use at all: hand the reason back to the
			// agent picker, where the user can choose another agent or fix
			// the model.
			return routeBack(it.blocked)
		case it.start:
			// The pin needs a start: run it through the same start flow every
			// other start row uses, exactly as the non-TUI -M path does, so
			// the worktree flags cannot change whether a pin starts.
			// --replace skips the replace dialog. The row is selected first
			// so a failed or cancelled start returns to the picker on it.
			m.models.Select(idx)
			m.phase = phaseModel
			pinned, ok := m.models.SelectedItem().(*modelItem)
			if !ok {
				return m, nil
			}
			return m.beginStart(pinned, m.allowReplace)
		}
		m.models.Select(idx)
		return m.proceedToLaunch()
	}

	// Cursor: the rotation's next-to-use model among actionable rows
	// (launch or start — anything without a hint), otherwise the first
	// actionable row, otherwise the top.
	var actionable []config.Model
	first := -1
	for i, it := range tbl.items {
		if it.blocked == "" {
			actionable = append(actionable, it.model)
			if first < 0 {
				first = i
			}
		}
	}
	pos := 0
	if first >= 0 {
		pos = first
	}
	// Rotation positions the cursor only when the last-launched model is a
	// registry model: with no rotation state (or a stale/discovered id that
	// is never in cfg.Models) NextFromEligible would fall back to the
	// registry-first model, so the first launchable row is used instead.
	if lastID != "" && config.IndexModelByID(m.cfg.Models, lastID) >= 0 {
		if next, ok := rot.NextFromEligible(actionable, m.cfg); ok {
			if idx, ok := idIndex[next.ID]; ok {
				pos = idx
			}
		}
	}
	m.models.Select(pos)

	// The single-row shortcut launches only: a lone start row must show the
	// picker and wait for Enter, never start a server unasked.
	if len(tbl.items) == 1 && tbl.items[0].blocked == "" && !tbl.items[0].start {
		return m.proceedToLaunch()
	}
	m.phase = phaseModel
	return m, nil
}

// tableFor builds the selector table for agent from models and an inventory
// snapshot, returning the table and the rotation's last-launched id. It is
// the exact table build enterModelPhase performs (rotation last id from rot,
// agent-scoped survey stats, one usage + refcount store read), shared with
// refreshTable so a rebuilt table matches the original in every column.
func (m model) tableFor(agent string, models []config.Model, snap *localmodels.Snapshot, rot *rotation.Rotation) (modelTable, string) {
	lastID, _ := rot.Last()
	// Agent-scoped 30-day survey stats, one Events() read for the picker.
	surveyStats := survey.AgentModelStats(newSurveyStore().Events(), agent, survey.Window30d, time.Now().UTC())
	tbl := buildTable(tableInput{
		cfg: m.cfg, agent: agent, models: models, inventory: snap,
		hideDiscovered: m.activeTags != "" || m.activeFamily != "",
		usage:          newUsageStore(), stats: surveyStats,
	}, newRefcountStore(), lastID)
	return tbl, lastID
}

// tableRefreshedMsg carries a rebuilt model table back from
// refreshTable's command, which runs the live inventory probe off the update
// loop.
type tableRefreshedMsg struct {
	items  []list.Item
	header string
	sel    string // model id the cursor should land on, when it still exists
	gen    int    // refreshGen when the probe was issued; stale messages are dropped
}

// refreshTable re-probes the live inventory and rebuilds the model list,
// keeping the cursor on the same model id when it still exists. Used after a
// start attempt that returns to the picker: a replace can stop the occupant and
// then fail, and the table built before the attempt would be lying about
// RUNNING. The probe itself runs in the returned command, never inline: it
// dials ollama, omlx/mtplx and mlx_lm_server with multi-second per-request
// timeouts, and this path is reached by every failed or cancelled start, so
// probing here would freeze the picker on each retry. A no-op before any table
// was built.
func (m model) refreshTable() (model, tea.Cmd) {
	if len(m.tableModels) == 0 {
		return m, nil
	}
	sel := ""
	if it, ok := m.models.SelectedItem().(*modelItem); ok {
		sel = it.model.ID
	}
	m.refreshGen++
	gen := m.refreshGen
	// The command captures only the values tableFor reads (cfg and the active
	// -T/-F filters), never the live model: the goroutine must not touch it,
	// and copying the whole struct would pin its lists and inputs for the
	// duration of the probe.
	src := model{cfg: m.cfg, activeTags: m.activeTags, activeFamily: m.activeFamily}
	agent, models := m.agent, m.tableModels
	return m, func() tea.Msg {
		inv := runInventory(src.cfg)
		tbl, _ := src.tableFor(agent, models, &inv, rotation.New())
		items := make([]list.Item, len(tbl.items))
		for i, it := range tbl.items {
			items[i] = it
		}
		return tableRefreshedMsg{items: items, header: tbl.header, sel: sel, gen: gen}
	}
}

// resolvePendingSelect puts the cursor back on pendingSelect's model when that
// id is still on screen, and clears the field either way: the model may have
// left the inventory, and a stale id must not capture the cursor later. The
// rebuilt rows are re-sorted (a stopped occupant leaves the running group), so
// the old numeric index is not trustworthy with or without a filter. While the
// user is mid-typing (Filtering) the cursor is theirs to drive.
func (m *model) resolvePendingSelect() {
	if m.pendingSelect == "" {
		return
	}
	if m.models.FilterState() != list.Filtering {
		for i, it := range m.models.VisibleItems() {
			if mi, ok := it.(*modelItem); ok && mi.model.ID == m.pendingSelect {
				m.models.Select(i)
				break
			}
		}
	}
	m.pendingSelect = ""
}

// applyRefreshedTable installs a rebuilt table on the picker. SetItems clears
// filteredItems whenever a filter is applied, and its returned command is the
// only thing that would repopulate them — discarding it leaves the list with no
// visible rows at all, which strands the user on an empty picker (the view
// renders "No items.", Enter does nothing, and the "esc clear filter" its own
// help bar advertises is consumed by the app's esc chain and quits wt).
// Re-applying the same text rather than forwarding that command re-derives the
// matches synchronously and also recomputes pagination, which bubbles only does
// while the user is typing — so the rows, the page bounds and the cursor are
// all correct by the end of this update. The user's filter is preserved either
// way.
func (m *model) applyRefreshedTable(msg tableRefreshedMsg) tea.Cmd {
	cmd := m.models.SetItems(msg.items)
	m.models.Title = msg.header
	if m.models.FilterState() == list.FilterApplied {
		m.models.SetFilterText(m.models.FilterInput.Value())
		cmd = nil // SetFilterText already repopulated the matches
	}
	// Otherwise (no filter, or the user is mid-typing) the command SetItems
	// returned is forwarded as-is: with the list unfiltered there is nothing to
	// repopulate, and while Filtering it feeds the input's own machinery.
	m.pendingSelect = msg.sel
	m.resolvePendingSelect()
	return cmd
}

// proceedToLaunch checks for a prior session and either launches the agent
// directly or transitions to the resume prompt. It is the shared flow used
// by both the phaseModel enter handler and the ollama warning proceed choice.
// Every path that bails back to the picker re-probes the table: this is reached
// from the start flow, where the table was built before the attempt, so a start
// that succeeded and then failed to launch would otherwise leave the picker
// claiming RUNNING="-" for a model that is loaded and serving. The probe is
// deferred to a command, so paying for it on the paths that did not start
// anything costs nothing on the update loop.
func (m model) proceedToLaunch() (model, tea.Cmd) {
	// The highlighted list item is what gets launched, regardless
	// of any other state. m.current is gone; m.models is the
	// single source of truth.
	highlighted, ok := m.models.SelectedItem().(*modelItem)
	if !ok {
		m.status = "no model selected"
		return m.refreshTable()
	}
	// Capture the model so launchAndRecord records exactly this pick in
	// both the no-session and resume paths, without re-reading the picker.
	m.launchModel = highlighted.model
	// Native models launch fresh: resuming a session would restore the
	// session's stored model, silently overriding the user's "native" choice
	// (and, for claude, routing a gateway model at the real Anthropic API).
	// Look up a prior session only for non-native models.
	var sess *session.Session
	if !highlighted.model.Native {
		if r, ok := agents.ByName(m.agent).(agents.Resumer); ok {
			var err error
			sess, err = r.LatestSession(m.selectedPath)
			if err != nil {
				m.status = "session check failed: " + err.Error()
				return m.refreshTable()
			}
		}
	}
	if sess == nil {
		cmd, err := launchAgent(m.agent, highlighted.model, m.selectedPath, m.yolo, nil, m.cfg, m.extraArgs)
		if err != nil {
			m.status = "launch failed: " + err.Error()
			return m.refreshTable()
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
// entry advances rotation), records a live-session refcount entry for the
// model picker's "in use" column, and then runs the agent. Recording
// happens here — the single commit point reached only after the ollama
// check and resume prompt have been satisfied — so a cancelled ollama
// warning, a cancelled resume prompt, or a failed ollama check never
// advances the rotation or the refcount. Both state writes are
// best-effort: a failure of either (or both) surfaces in m.status and the
// launch still proceeds.
func (m model) launchAndRecord(cmd *exec.Cmd) (model, tea.Cmd) {
	var errs []string
	if err := rotation.New().RecordFor(m.agent, m.launchModel.ID); err != nil {
		errs = append(errs, "rotation state not saved: "+err.Error())
	}
	if err := refcount.NewStore().Record(os.Getpid(), m.launchModel.ID); err != nil {
		errs = append(errs, "refcount state not saved: "+err.Error())
	}
	if len(errs) > 0 {
		m.status = strings.Join(errs, "; ")
	}
	return m, runAndWaitCmd(cmd, m.agent, m.launchModel)
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

// selectFirstEntry selects the first kindEntry item in l matching pred and
// reports whether a match was found. Shared by the pendingHighlight,
// default-branch, and current-checkout cursor placements in Update so a
// future change to the iteration shape (e.g. skipping separator rows) only
// needs to happen once.
func selectFirstEntry(l *list.Model, pred func(entryItem) bool) bool {
	for i, it := range l.Items() {
		if ei, ok := it.(entryItem); ok && ei.kind == kindEntry && pred(ei) {
			l.Select(i)
			return true
		}
	}
	return false
}

// isCurrentOnDefaultBranch returns true when the entry is the current
// worktree and its branch matches the repo default branch.
func isCurrentOnDefaultBranch(e worktree.Entry, defaultBranch string) bool {
	return defaultBranch != "" && e.Type == worktree.TypeCurrent && e.Branch == defaultBranch
}

// isDefaultBranchOnly reports whether the user is working directly on the
// repo default branch with nothing else to switch to — the situation where
// a default-branch warning in the picker title is warranted. Returns true
// when the current worktree is on the default branch and the picker, after
// default-branch filtering, contains no other pickable entries. Any other
// surviving entry — a different branch, a detached worktree, or a second
// worktree on main — means there is something to switch to, so the warning is
// suppressed. Entries that buildList would filter out (bare local default
// branch, remote-tracking default-branch refs) are ignored.
func isDefaultBranchOnly(groups []worktree.EntryGroup, defaultBranch string) bool {
	if defaultBranch == "" {
		return false
	}
	onDefault := false
	for _, g := range groups {
		for _, e := range g.Entries {
			if worktree.SkipInPicker(g.Kind, e, defaultBranch) {
				continue
			}
			if isCurrentOnDefaultBranch(e, defaultBranch) {
				onDefault = true
				continue
			}
			// Any other surviving entry means there's something to switch to.
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

// newRunModel builds the TUI's initial model from Run's arguments. It is split
// out of Run so tests can pin the argument-to-field plumbing (e.g. --replace)
// without starting a real tea.Program.
func newRunModel(yolo, allowReplace bool, agent, pinned, tags, family string, extraArgs []string, theme themes.Theme, prePath string, cfg *config.Config) model {
	return model{
		status:       "loading worktrees...",
		cfg:          cfg,
		theme:        theme,
		yolo:         yolo,
		allowReplace: allowReplace,
		initialAgent: agent,
		pinnedModel:  pinned,
		activeTags:   tags,
		activeFamily: family,
		extraArgs:    extraArgs,
		prePath:      prePath,
		repoRoot: func() string {
			if prePath == "" || prePath == "." {
				return ""
			}
			root, err := worktree.RepoRootAt(prePath)
			if err != nil {
				return ""
			}
			return root
		}(),
	}
}

// Run starts the TUI in alternate-screen mode and returns when it quits.
// agent is the --agent flag value ("" = no agent pinned; the agent/command
// picker is shown). pinned is the --model flag value ("" = no model pinned;
// the model picker is shown, and a non-empty pin is validated against the
// agent's eligible list once the agent is resolved). tags is the -T/--tags
// flag value (comma-delimited; "" = no filter). family is the -F/--family
// flag value (comma-delimited; "" = no filter). extraArgs are the user's
// passthrough args after --. theme is the active color theme, loaded by
// cmd/wt. allowReplace grants the -M start path permission to stop a running
// occupant without the replace dialog (it comes from --replace). prePath,
// when non-empty, is a pre-resolved worktree path
// (-W/--cwd/outside-repo): the worktree picker is skipped and control starts
// at the agent/command picker (or model phase when agent is pinned). When
// prePath is inside a git repo, repoRoot is seeded so the new-worktree prompt
// has a valid directory even if it becomes reachable from the pre-path entry
// point. cfg is the already-loaded config from cmd/wt's newApp (validated
// before Run is called); it is not re-loaded here.
func Run(yolo, allowReplace bool, agent, pinned, tags, family string, extraArgs []string, theme themes.Theme, prePath string, cfg *config.Config) error {
	p := tea.NewProgram(newRunModel(yolo, allowReplace, agent, pinned, tags, family, extraArgs, theme, prePath, cfg), tea.WithAltScreen())
	currentProgram = p
	// Reset any summary/survey state captured by a previous run (e.g.
	// from a test invocation sharing the process).
	pendingSummary = ""
	pendingSurveyState = pendingSurvey{}
	finalModel, err := p.Run()
	// The alt-screen is now torn down (p.Run() has returned and bubbletea
	// has called exitAltScreen), so stdout reaches the user's terminal
	// rather than a discarded buffer.
	printPendingSummaryAndSurvey(cfg)
	if err == nil {
		if fm, ok := finalModel.(model); ok && fm.fatalErr != nil {
			err = fm.fatalErr
		}
	}
	return err
}

// printPendingSummaryAndSurvey runs the TUI's post-exit flow once the
// alt-screen is gone, in the same order as the non-TUI path (issues
// #115/#116): release this session's refcount entry, survey (skipped for
// native models), stop picker, summary line, after-survey stats, pricing
// notice. It is extracted from Run() so this ordering is unit-testable
// without a real tea.Program/TTY.
func printPendingSummaryAndSurvey(cfg *config.Config) {
	launched := pendingSurveyState
	summary := pendingSummary
	pendingSurveyState = pendingSurvey{}
	pendingSummary = ""

	var stats string
	if launched.agent != "" {
		releaseSession()
		stats = runSurvey(launched.agent, launched.m)
		// Command agents (e.g. shell) record m.ID == "" and launch no model, and
		// native models run no local server, so a session on either has nothing
		// of its own to stop.
		if launched.m.ID != "" && !launched.m.Native {
			runStopPhase(cfg)
		}
	}
	if summary != "" {
		// Leading "\n" guards against the agent's last byte being
		// non-newline so the summary always lands on a fresh line.
		// Println adds the trailing newline itself.
		fmt.Println("\n" + summary)
	}
	if stats != "" {
		fmt.Println(stats)
	}
	// Command agents never touch a priced model, so the reminder is
	// meaningless for them — same convention runSurvey already uses.
	if summary != "" && launched.m.ID != "" {
		emitPriceNotice()
	}
}
