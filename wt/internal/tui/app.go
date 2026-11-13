// Package tui implements the Bubble Tea terminal UI for wt.
package tui

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/ollamacheck"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
	"github.com/ohanaverse/local-ai-setup/wt/internal/refcount"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rotation"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
)

// phase identifies which screen the TUI is currently showing.
type phase int

const (
	phaseList           phase = iota // worktree list
	phaseAgent                       // agent+command picker (PR 2): picks between configured agents and command drivers before the model screen
	phaseModel                       // agent+model picker
	phaseOllamaWarn                  // confirm before launching with unavailable ollama model
	phaseNewWorktree                 // create-new-worktree prompt
	phaseStarting                    // a start runs through the lifecycle engine, with live progress
	phaseReplaceConfirm              // confirm replacing the running occupant before starting
	phaseRouting                     // a launch wrote the model's missing LiteLLM route; the proxy restarts, with elapsed time (#192)
	phaseSlot                        // a slot-profile launch is writing its routes, restarting or probing the proxy
)

// model holds the entire UI state.
type model struct {
	status string
	width  int
	height int

	// rows is the worktree picker: the row list in launch mode over
	// internal/cleanup's rows (picker_rows.go); ready once the first load
	// arrived. rowsGen numbers the loads, so a PR fill for an older one is
	// dropped.
	rows    cleanupListModel
	rowsGen int
	ready   bool
	// rowsIn is the gather inputs the last rows load ran with (the claim,
	// the PR hooks, the fetch hook): the `F` refresh re-runs exactly them.
	rowsIn cleanup.GatherInputs
	// rowsRefreshing is true from the picker's `F` until its load lands: a
	// second `F` while one is in flight is dropped, one fetch per request.
	rowsRefreshing bool
	// reloadGen is rowsGen when that `F` reload was dispatched: a removal
	// run bumps rowsGen, and a reload from before it is dropped on landing.
	reloadGen int
	// removeFuncs builds each load's row actions (RemoveFuncsBuilder, cmd/wt's
	// closure over cleanupRemoveFuncs); nil makes every row action say it is
	// unavailable.
	removeFuncs RemoveFuncsBuilder

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

	// launch state
	selectedPath string       // worktree path chosen in the worktree list
	prePath      string       // pre-resolved worktree path (-W/--cwd/outside-repo); skips the worktree picker when non-empty
	yolo         bool         // pass skip-permissions flag to the agent
	allowReplace bool         // --replace mode: the -M start path may stop a running occupant without the dialog
	othersMode   string       // --others: "keep" or "stop" answers the -M pinned row's question about other providers' models without the dialog (othersFor); "" asks
	extraArgs    []string     // user passthrough args after --
	initialAgent string       // agent from --agent flag; "" = no agent pinned (agent/command picker is shown)
	pinnedModel  string       // model from --model flag; "" = no model pinned (model picker is shown)
	launchModel  config.Model // the model being launched, captured when the launch began (the routing phase finishes an Update later)
	startNote    string       // what the engine printed during the start that led to this launch (statusNote, #258, #275); launchSelected, or proceedToLaunch when it has no row to launch, takes it once and puts it ahead of the failure

	// filter inputs (PR 3b): -T/--tags and -F/--family values from the CLI;
	// forwarded to the model screen so the picker can pre-filter the catalog.
	activeTags   string // comma-delimited tag filter from -T/--tags; "" = no filter
	activeFamily string // comma-delimited family filter from -F/--family; "" = no filter

	// slotChecks are the statically validated slot profiles
	// (profiles.ValidateSlots). nil means the picker shows no profile row.
	slotChecks []profiles.SlotCheck

	// ollama availability warning
	ollamaWarnModel list.Model // confirmation choices for unavailable model

	// start-on-select flow (2026-09-20): Enter on a start row runs the model
	// through the lifecycle engine with live progress; the replace-confirm
	// dialog appears when Start reports the provider's single slot occupied
	// (or unknowable) and AllowReplace was false, or other providers' models
	// loaded beside the model and the question about them unanswered (#412).
	start    *startState   // in-flight start (phaseStarting); nil when idle
	startRun int           // monotonically increasing run id; stale messages are dropped by id
	replace  *replaceState // replace-confirm dialog (phaseReplaceConfirm)

	// launch-time route check (#192): a launch that had to write the model's
	// LiteLLM route waits for the proxy restart behind phaseRouting instead of
	// freezing the picker.
	routing  *routingState // in-flight route check (phaseRouting); nil when idle
	routeRun int           // monotonically increasing run id; stale messages are dropped by id

	// slot-profile launch: Enter on a profile row runs the launch flow's
	// route writes, restart and probe behind phaseSlot. The flow is kept
	// while the profile's local model starts and on the replace dialog.
	slot    *slotFlow // in-flight profile launch; nil when idle
	slotRun int       // monotonically increasing run id; stale messages are dropped by id

	// creation is the pick that owes its worktree at the commit point (a bare
	// branch, a remote-only branch, or a typed name that owes one): resolved
	// read-only here, made only once the launch gate says go (launchgate.go).
	// nil for every other pick, and cleared by every selection, so an
	// abandoned pick's plan never rides along with a later one.
	creation *launchCreation

	// typedPlan is the plan behind a typed name, kept even when no creation
	// is announced: cmd/wt's -W runs plan.Ensure() unconditionally after its
	// one question (main.go:551), so the commit point runs it here too. A
	// reused worktree is Ensure's idempotent no-op (nothing announced,
	// nothing made); a path that is only debris is git's own error instead of
	// a launch at a directory that is not a worktree. Cleared by every
	// selection, as creation is.
	typedPlan *worktree.Plan

	// pickedRow is the unified-list row this launch was picked from (nil for
	// a typed name or a preset path). The gate's Target carries its PR,
	// upstream and dirt facts — the ones only the list gathered — over the
	// live read (launchwarn.FromRow, folded in by launchTarget). Rebound by
	// every selection, as creation and typedPlan are, so an abandoned pick's
	// row never rides along with a later one.
	pickedRow *cleanup.Row

	// committed is the commit point's in-flight guard: the first selection
	// that reaches a run command takes it (commitLaunch) and every further
	// Enter is ignored while that launch runs. It releases on exactly one
	// outcome — the slot applier's failure (slotAbortMsg), whose return to
	// the model picker is a retry path that must be able to commit again —
	// and otherwise holds to process exit. The agent's own exit
	// (launchDoneMsg) must NOT release it: the terminal is handed back just
	// before that message is processed, so a key queued during teardown can
	// still reach Update before the QuitMsg and would otherwise commit a
	// duplicate run command (a second agent, a second rotation/usage
	// record).
	committed bool

	// new-worktree prompt
	newInput  textinput.Model
	newError  string
	repoRoot  string // cached from rowsLoadedMsg to avoid a second rev-parse
	listError string // reload error shown above the list when ready

	// fatalErr, when set, is paired with a tea.Quit return so the whole
	// program exits; Run() below surfaces it as the function's returned
	// error, matching the non-TUI path's exit-with-message behavior
	// (cmd/wt/main.go's runLaunchPath). Nothing currently sets this
	// field — enterModelPhase's route-backs return a status, never a
	// fatal error — but it remains wired into Run() for any future
	// genuinely-fatal condition.
	fatalErr error

	// agentErr is what running the launched agent returned (nil for an
	// exit 0). Run() returns it once the post-exit flow has run, so cmd/wt
	// exits with the agent's status as the non-TUI path does (#412).
	agentErr error
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
	return loadPickerCmd()
}

// loadPickerCmd loads the worktree picker: it resolves the repository root
// once (#390) and gathers its rows there (loadRowsCmd; it fetches nothing —
// the list's one fetch is the user's `F`, refreshRowsChosenMsg). The PR
// lookups follow once the rows are on screen (gotRows, loadRowPRsCmd).
// Outside a repository the load reports the lookup's error.
func loadPickerCmd() tea.Cmd {
	return func() tea.Msg {
		root, err := findRepoRoot()
		cwd, _ := os.Getwd()
		return loadRowsCmd(root, err, pickerInputs(cwd), false)()
	}
}

// isTyping reports whether the current phase is actively receiving character
// input, in which case 'q' must not quit. The new-worktree prompt is a
// textinput; the list filters (worktree, agent+command, and model picker)
// are bubbles/list's incremental filter.
func (m model) isTyping() bool {
	if m.phase == phaseNewWorktree {
		return true
	}
	if m.phase == phaseList && m.ready && m.rows.filtering {
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
// Whatever the message did, the list on screen is then fitted to the lines its
// screen prints around it (fitLists, layout.go).
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(msg)
	next.fitLists()
	return next, cmd
}

// update is Update without the final list fit.
func (m model) update(msg tea.Msg) (model, tea.Cmd) {
	switch msg := msg.(type) {
	case startStageMsg, startTickMsg, startDoneMsg:
		// The case list is exactly the set handleStartMsg handles, so it
		// always has something to say.
		return m.handleStartMsg(msg)
	case routeDoneMsg, routeTickMsg:
		// Likewise exactly the set handleRouteMsg handles.
		return m.handleRouteMsg(msg)
	case slotPreparedMsg, slotSettledMsg, slotTickMsg:
		// Likewise exactly the set handleSlotMsg handles.
		return m.handleSlotMsg(msg)
	case slotAbortMsg:
		// The slot applier failed after the terminal was released: no agent
		// ran and nothing was recorded. The run command's outcome is
		// processed, so the commit point's guard releases: the return to the
		// model picker is a retry path, and its next Enter must be able to
		// commit again.
		m.status = "launch failed: " + msg.err.Error()
		m.committed = false
		m.phase = phaseModel
		return m.refreshTable()
	case tableRefreshedMsg:
		// A refresh can land after the user moved on (a second start, a quit);
		// only the picker it was built for should absorb it.
		if m.phase != phaseModel || msg.gen != m.refreshGen {
			return m, nil
		}
		if len(msg.items) == 0 {
			// The re-probe left no row at all (#179 Phase B: the model a
			// failed start was for is no longer on disk or running). Route
			// back like enterModelPhase's empty table does instead of
			// stranding the user on a "No items." picker; the status the
			// failed start left, if any, already says what happened.
			status := m.status
			if status == "" {
				status = catalog.NoRowsReason(m.agent)
			}
			return m.routeBackToAgent(status)
		}
		return m, m.applyRefreshedTable(msg)
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		// Every list is sized by fitLists, from the lines and columns its
		// screen prints around it.
		if m.phase == phaseNewWorktree {
			m.newInput.Width = msg.Width - 4
		}
	case rowsLoadedMsg:
		return m.gotRows(msg)
	case rowPRsLoadedMsg:
		// The PR lookups of the latest load arrived: swapped in with the
		// cursor, the filter and the scroll kept (withReport). The filled
		// snapshot builds the row actions' removal funcs, so the engine's
		// re-check stands on the rows now on screen (swapRemoveFuncs: a
		// flow in progress defers the swap to its return to rest, and a
		// done list takes nothing at all — the quit window keeps the
		// hand-off instance its key checked).
		if m.ready && msg.gen == m.rowsGen && !m.rows.done {
			m.rows = m.rows.withReport(msg.Report)
			swapRemoveFuncs(&m.rows, func() RemoveFuncs { return m.removeFuncsFor(msg.Snap, m.rowsIn) })
		}
		return m, nil
	case rowChosenMsg:
		// Enter on a picker row: the launch flow's own message, so an owed
		// creation is bound to the launch (selectedEntryMsg) and the row's
		// own PR/upstream facts ride with it to the gate.
		return m, emit(selectedEntryMsg{entry: entryOfRow(msg.Row), row: &msg.Row})
	case newWorktreeChosenMsg:
		return m.openNewWorktreePrompt(), nil
	case refreshRowsChosenMsg:
		// Launch mode's `F`: fetch the remotes and re-gather the rows, the
		// picker's only fetch. A refresh already in flight owns the key:
		// the message is dropped, never a second fetch stacked on the first.
		if !m.ready || m.phase != phaseList || m.rowsRefreshing {
			return m, nil
		}
		m.rowsRefreshing, m.reloadGen = true, m.rowsGen
		m.rows.fetching = true
		return m, loadRowsCmd(m.repoRoot, nil, m.rowsIn, true)
	case cleanupPreviewMsg, cleanupProgressMsg, cleanupRunDoneMsg, cleanupRefreshMsg, cleanupRefetchMsg:
		// The row list runs its own flows inside itself: the `x` removal
		// run (preview, progress, done, the post-run refresh) and, in
		// manage mode, the `F` re-read's answer.
		if m.ready && m.phase == phaseList {
			return m.updateRows(msg)
		}
		return m, nil
	case selectedEntryMsg:
		// Every selection rebinds the launch: a creation an earlier pick owed
		// — and the typed name's plan behind it — is dropped first, so esc
		// back to the list and a re-pick can never drive the abandoned pick's
		// worktree at the commit point. Bare
		// branches (TypeBranch, Path="") and remote-only branches have no
		// worktree yet. The pick owes one: resolve the plan read-only here
		// — so the launch gate can state the creation in launchwarn's frozen
		// wording and a declined gate creates nothing — and let the commit
		// point make it (launchCreation). The plan's path is where the agent
		// launches, rather than wt's CWD (cmd.Dir="").
		m.creation = nil
		m.typedPlan = nil
		m.pickedRow = msg.row
		if msg.entry.Type == worktree.TypeBranch && msg.entry.Path == "" {
			plan, err := worktree.Resolve(m.repoRoot, msg.entry.Branch)
			if err != nil {
				m.listError = err.Error()
				m.phase = phaseList
				return m, nil
			}
			m.creation = &launchCreation{repoRoot: m.repoRoot, name: msg.entry.Branch, plan: plan}
			m.selectedPath = plan.Path()
			return m.proceedFromSelectedPath()
		}
		m.selectedPath = msg.entry.Path
		return m.proceedFromSelectedPath()
	case launchDoneMsg:
		if msg.status != "" {
			m.status = msg.status
		}
		if msg.err != nil {
			m.status = "agent exited: " + msg.err.Error()
		}
		m.agentErr = msg.err
		// The guard is deliberately NOT released here: the run command's
		// closure hands the terminal back (input restarts at RestoreTerminal)
		// just before this message is processed, so a KeyMsg already queued
		// is handled before the QuitMsg and must not commit a duplicate run
		// command. Held to process exit, the guard makes that race harmless.
		// Only the retry path (slotAbortMsg) releases it.
		return m, tea.Quit
	case tea.KeyMsg:
		// An in-flight start owns the keyboard: esc/q/ctrl+c cancel and a
		// further press quits; every other key is ignored (before the picker
		// wrap-around below, which must not move the cursor mid-start).
		if m.phase == phaseStarting && m.start != nil {
			return m.handleStartKey(msg)
		}
		// So does an in-flight route check, for the same reason and at the same
		// place: the launch it precedes is already decided, so no key may move
		// the cursor, pop a screen or quit by accident. Only ctrl+c gets out.
		if m.phase == phaseRouting {
			return m.handleRouteKey(msg)
		}
		// And a profile launch's route stage, for the same reason.
		if m.phase == phaseSlot && m.slot != nil {
			return m.handleSlotKey(msg)
		}
		// The worktree picker is the row list: every key is its own (q and
		// esc quit there, `/` filters, and while the filter is typed every
		// printable key goes into it). ctrl+c quits from rest. A double Enter
		// cannot start two launches: the commit point takes one (the
		// committed guard at each run command's birth, commitLaunch).
		if m.phase == phaseList && m.ready {
			// ctrl+c is the row list's own unless the list is at rest: it
			// cancels a confirm and waits out a removal run (never
			// abandoned, Ruling T); only a list at rest quits wt.
			if msg.String() == "ctrl+c" && m.rows.phase == cleanupPhaseList {
				return m, tea.Quit
			}
			return m.updateRows(msg)
		}
		// A status that has the screen to itself (statusAlone) is taken down
		// by the next key, which does nothing else: the table was not on
		// screen, so no key can have been meant for it. ctrl+c still quits.
		if m.phase == phaseModel && m.statusAlone() {
			if msg.String() == "ctrl+c" {
				return m, tea.Quit
			}
			m.status = ""
			return m, nil
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
			if m.phase == phaseOllamaWarn || m.phase == phaseReplaceConfirm {
				if m.phase == phaseReplaceConfirm {
					m = m.dropSlot()
				}
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
				// Agent: validate the model catalog, then build the picker
				// list and position the cursor.
				return m.enterModelPhaseForAgent()
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
				if highlighted.profile != nil && highlighted.blocked == "" {
					// A slot profile is its own launch unit: its routes, its
					// local model's start and its command all come from the
					// profile, never from this row's id-only Model. A blocked
					// profile falls through to the reason below.
					return m.beginSlotLaunch(highlighted)
				}
				if highlighted.start {
					// A start row: run the model through the lifecycle engine
					// instead of launching. start and blocked are mutually
					// exclusive — renderTable sets exactly one per row, and its
					// route-refusal and route-error cases clear start when they
					// set blocked — so this order is a guard, not a precedence
					// rule.
					// --replace is the -M pin's permission, not a blanket
					// one: any other start row still gets the dialog.
					// So is --others.
					pinned := highlighted.model.ID == m.pinnedModel
					return m.beginStart(highlighted, m.allowReplace && pinned, m.othersFor(pinned))
				}
				if highlighted.blocked != "" {
					m.status = highlighted.blocked
					return m, nil
				}
				// Check ollama availability before launching. Rotation is
				// NOT recorded here: the user can still cancel the ollama
				// warning, or the check can fail, and in neither case did
				// a launch happen. Recording
				// lives in launchAndRecord, the single commit point. The
				// check is skipped when this agent×model pairing will
				// actually route through LiteLLM (any upstream may serve
				// the model) and when the model is not served by ollama —
				// ollamacheck only asks the ollama daemon the registry
				// names. Uses the
				// per-model resolved route rather than the raw
				// cfg.IsLitellm() toggle so a protocol-forced LiteLLM route
				// (e.g. codex+ollama) isn't spuriously blocked by this
				// local-availability check.
				route, _ := m.cfg.ResolveRoute(highlighted.model, agents.ProtocolsFor(m.agent))
				if !route.Litellm && ollamacheck.IsOllamaModel(highlighted.model) {
					ok, err := ollamacheck.Check(m.cfg, highlighted.model)
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
						// A profile launch that got this far ends here; a
						// restart it still owes is settled (dropSlot).
						m = m.dropSlot()
						m.phase = phaseModel
						return m, nil
					case replaceProceedChoice:
						return m.retryStart(m.replace.item, true, m.replace.others)
					case othersKeepChoice:
						// #412: keep other providers' models; AllowReplace
						// only for same-family occupants the dialog showed.
						return m.retryStart(m.replace.item, m.replace.allow, othersPlan{answer: lifecycle.OthersAnswer{Decided: true}})
					case othersStopChoice:
						// Stop exactly the stoppable models the dialog showed.
						return m.retryStart(m.replace.item, m.replace.allow, othersPlan{answer: lifecycle.OthersAnswer{Decided: true, Stop: m.replace.stop}})
					}
				}
			case phaseNewWorktree:
				name := m.newInput.Value()
				if errMsg := validateNewWorktreeName(name); errMsg != "" {
					m.newError = errMsg
					return m, nil
				}
				plan, err := worktree.Resolve(m.repoRoot, name)
				if err != nil {
					m.newError = err.Error()
					return m, nil
				}
				m.newError = ""
				// A typed name is a pick, not a create: cmd/wt's -W rule
				// decides which side of the gate it lands on. A name that
				// owes its worktree — a new branch, a remote tracking branch,
				// or a branch no worktree holds yet (and no directory at the
				// plan's path) — is a creation pick, bound here read-only and
				// made at the commit point exactly like a bare-branch row; the
				// rest launch into the worktree they name. Nothing is created
				// here, so a declined gate leaves nothing behind. The plan
				// is kept either way (typedPlan): the commit point runs its
				// Ensure unconditionally, as cmd/wt's -W does after its one
				// question (main.go:551).
				m.creation = nil
				m.typedPlan = plan
				m.pickedRow = nil // a typed name is not a row pick
				if _, serr := os.Stat(plan.Path()); plan.Creates() || plan.Tracks() != "" || serr != nil {
					m.creation = &launchCreation{repoRoot: m.repoRoot, name: name, plan: plan}
				}
				m.selectedPath = plan.Path()
				return m.proceedFromSelectedPath()
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
		body += "\n[enter] create   [esc] cancel"
		return body
	}
	if m.phase == phaseOllamaWarn {
		if m.width <= 0 || m.height <= 0 {
			return "ollama availability warning (waiting for window size)"
		}
		return m.choiceFrame()(m.ollamaWarnModel.View())
	}
	if m.phase == phaseStarting {
		if m.width <= 0 || m.height <= 0 {
			return "start progress (waiting for window size)"
		}
		return clip(m.startingView(), m.width)
	}
	if m.phase == phaseRouting {
		if m.width <= 0 || m.height <= 0 {
			return "route update (waiting for window size)"
		}
		return clip(m.routingView(), m.width)
	}
	if m.phase == phaseSlot {
		if m.width <= 0 || m.height <= 0 {
			return "slot profile launch (waiting for window size)"
		}
		return clip(m.slotView(), m.width)
	}
	if m.phase == phaseReplaceConfirm {
		if m.width <= 0 || m.height <= 0 {
			return "replace confirm (waiting for window size)"
		}
		return drawnFrame(&m.replace.choices, m.height, m.replaceFrames()...)(m.replace.choices.View())
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
	return m.worktreeFrame()(m.rows.View())
}

// launchCommand builds and runs a command (no model layer) — e.g. the shell
// driver — in the selected worktree, skipping the model screen and the
// ollama check. name is the picked command's driver name; the
// same BuildLaunchCmd path as agents is used, so a future command driver
// launches by its own name rather than being hardcoded to "shell".
func (m model) launchCommand(name string) (model, tea.Cmd) {
	cmd, err := launchAgent(name, config.Model{}, m.selectedPath, false, m.cfg, m.extraArgs)
	if err != nil {
		m.status = "launch failed: " + err.Error()
		return m, nil
	}
	return m.commitLaunch(runAndWaitCmd(cmd, name, config.Model{}, m.gateFor()))
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
	queueLaunchNotes(name, cmd, m.extraArgs)
	return m.commitLaunch(runAndWaitCmd(cmd, name, config.Model{}, m.gateFor()))
}

// commitLaunch binds a run command to the launch in flight. The first caller
// to reach it takes the launch (committed) and gets the command; while
// committed, every further call returns nil — a second Enter pressed before
// the terminal is released must not start a second run command (with it a
// second creation binding, a second gate and a second agent). The guard
// releases only where a retry must be able to commit (slotAbortMsg); on the
// quit path it holds to process exit, so a key queued during teardown cannot
// start a second launch.
func (m model) commitLaunch(cmd tea.Cmd) (model, tea.Cmd) {
	if m.committed {
		return m, nil
	}
	m.committed = true
	return m, cmd
}

// proceedFromSelectedPath continues the launch flow once m.selectedPath is
// resolved — either directly from a worktree/current entry, or as the plan
// path of a pick that owes its worktree (m.creation, made at the commit
// point). It is the body of the old selectedEntryMsg handler.
//
// PR 2: the agent/command picker is the new explicit entry point. When the
// user pinned an agent via --agent, mirror the pre-PR-2 UX: a shell --agent
// skips the picker and launches immediately, any other --agent jumps straight
// to phaseModel. This keeps `wt --agent shell` and `wt --agent claude` exactly
// as they behaved before PR 2 — only the unpinned path shows the picker.
//
// The warnings every launch shows — root, held, PR merged, upstream gone,
// default branch, dirty and held — are one question at the commit point
// (launchGate), not a screen here: the pick proceeds through the flow and the
// gate speaks when the agent is about to run.
func (m model) proceedFromSelectedPath() (model, tea.Cmd) {
	return m.proceedWithPath()
}

// proceedWithPath is proceedFromSelectedPath: the flow continues to the agent
// or the model screen.
func (m model) proceedWithPath() (model, tea.Cmd) {
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
		return m.enterModelPhaseForAgent()
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

// enterModelPhaseForAgent validates the model catalog for m.agent under the
// active filters (-T/-F) and hands the eligible models to enterModelPhase. The
// full catalog is fetched once and narrowed in place by EligibleModelsIn, so
// the registry is walked once per entry. A config error or an empty eligible
// list sets the status and leaves the phase where it is.
func (m model) enterModelPhaseForAgent() (model, tea.Cmd) {
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
}

// enterModelPhase builds the selector table for the agent and either
// transitions to phaseModel or, when exactly one launchable row exists, skips
// the picker. The skip path reuses proceedToLaunch so the launch-time route
// check and the global rotation recording still run, exactly as for a row
// picked by hand.
//
// models is the agent's eligible list; local models that are not
// running appear as start rows (Enter runs the lifecycle engine; only blocked
// rows show a hint — their reason) rather than being hidden.
//
// A pinned model (-M) is resolved against the same rows the table shows, so
// the pin and the picker can never disagree: a launch row (cloud, or a local
// model the probe reports running) proceeds straight to launch, an idle local
// start row is selected and shown for the user to start, and a blocked row
// routes back with the row's own reason. A pinned slot profile
// (-M profile/<name>) that is not blocked begins its launch through
// beginSlotLaunch; a blocked one routes back like any blocked row. A mismatch
// routes to phaseAgent (the bad pin is cleared so re-entry validates fresh).
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
// models must be non-empty: the caller, enterModelPhaseForAgent, guards
// len(models) == 0 with its own status message. A non-empty list can still build zero rows (#179
// Phase B: a registry local model that is neither on disk nor running has no
// row), so an empty table routes back with catalog.NoRowsReason rather than
// opening a "No items." picker whose Enter does nothing.
//
// Route-backs: a rejected -M pin — one with no row (MissingReason when the
// pin is in the eligible list and the probe can say why, else "not in the
// eligible list"), or one whose row is blocked — and an empty table.
func (m model) enterModelPhase(agent string, models []config.Model, firstTag string) (model, tea.Cmd) {
	m.tag = firstTag

	// A closure, not a method value: it must see the m.tableModels/m.models
	// assignments made below before it is called.
	routeBack := func(status string) (model, tea.Cmd) { return m.routeBackToAgent(status) }

	m.tableModels = models
	m.refreshGen++

	inv := runInventory(m.cfg)
	snap := &inv

	// One Rotation for both the marker and the cursor (each New() scans the
	// config dir). tableFor reads the last-picked row id from it for the
	// marker ("" when nothing was ever launched); the cursor logic below
	// reuses rot so the config dir is scanned once.
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
			// MissingReason only for a pin the agent could use: one outside
			// the eligible list (unsupported provider, filtered by -T/-F)
			// would be told to pull, then refused as ineligible after.
			if config.IndexModelByID(models, m.pinnedModel) >= 0 {
				if reason := catalog.MissingReason(m.cfg, snap, m.pinnedModel); reason != "" {
					return routeBack(reason)
				}
			}
			// The same reason resolveModel gives a slot profile hidden by
			// -T/-F or held by another agent (#412).
			if reason := catalog.SlotPinReason(m.cfg, m.slotChecks, agent, m.pinnedModel, m.activeTags, m.activeFamily); reason != "" {
				return routeBack(reason)
			}
			return routeBack(fmt.Sprintf("model %q is not in the eligible list for agent %q", m.pinnedModel, agent))
		}
		it := tbl.items[idx]
		switch {
		case it.blocked != "":
			// A row the pin cannot use at all: hand the reason back to the
			// agent picker, where the user can choose another agent or fix
			// the model.
			return routeBack(it.blocked)
		case it.profile != nil:
			// A slot profile: begin its launch, as the non-TUI pin does. The
			// row is selected first so a failed launch returns to the picker
			// on it. --replace applies when its local model has to start
			// (handleSlotMsg reads it against the pin).
			m.models.Select(idx)
			m.phase = phaseModel
			pinned, ok := m.models.SelectedItem().(*modelItem)
			if !ok {
				return m, nil
			}
			return m.beginSlotLaunch(pinned)
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
			return m.beginStart(pinned, m.allowReplace, m.othersFor(true))
		}
		m.models.Select(idx)
		return m.proceedToLaunch()
	}

	if len(tbl.items) == 0 {
		return routeBack(catalog.NoRowsReason(agent))
	}

	// Cursor, in this order: the last-picked row when it is a slot profile
	// that is here and not blocked (profiles do not rotate, so the one just
	// used is offered again); otherwise the rotation's next-to-use model
	// among actionable model rows (launch or start: anything without a
	// hint); otherwise the first actionable row, a profile row included;
	// otherwise the top.
	//
	// actionable holds model rows only. Rotation walks the registry, so a
	// profile row could never be its answer, and a profile row's model is an
	// id with nothing else in it.
	var actionable []config.Model
	first := -1
	for i, it := range tbl.items {
		if it.blocked != "" {
			continue
		}
		if first < 0 {
			first = i
		}
		if it.profile == nil {
			actionable = append(actionable, it.model)
		}
	}
	pos := 0
	if first >= 0 {
		pos = first
	}
	if idx, ok := idIndex[lastID]; ok && tbl.items[idx].profile != nil && tbl.items[idx].blocked == "" {
		pos = idx
	} else if rotLast, _ := rot.Last(); rotLast != "" && config.IndexModelByID(m.cfg.Models, rotLast) >= 0 {
		// Rotation positions the cursor only when the last-launched model is
		// a registry model: with no rotation state (or a stale/discovered id
		// that is never in cfg.Models) NextFromEligible would fall back to
		// the registry-first model, so the first actionable row is used
		// instead. The pointer is read from rotation.state (rot.Last), not
		// from lastID: after a profile launch the two differ.
		if next, ok := rot.NextFromEligible(actionable, m.cfg); ok {
			if idx, ok := idIndex[next.ID]; ok {
				pos = idx
			}
		}
	}
	m.models.Select(pos)

	// The single-row shortcut launches only: a lone start row must show the
	// picker and wait for Enter, never start a server unasked. A lone
	// slot-profile row waits too: the shortcut launches the row as a model,
	// and a profile row's model is only its id.
	if len(tbl.items) == 1 && tbl.items[0].blocked == "" && !tbl.items[0].start && tbl.items[0].profile == nil {
		return m.proceedToLaunch()
	}
	m.phase = phaseModel
	// The list was built at the bare window size; give it the picker's.
	m.fitLists()
	return m, nil
}

// routeBackToAgent leaves the model phase for the agent picker with status as
// the reason, clearing any -M pin so re-entry validates fresh. Shared by
// enterModelPhase's route-backs and a table refresh that left no rows.
func (m model) routeBackToAgent(status string) (model, tea.Cmd) {
	m.status = status
	m.pinnedModel = ""
	items := buildAgentList(m.cfg)
	m.agentList = list.New(items, ThemedListDelegate(m.theme), m.width-2, m.height-2)
	m.agentList.Title = "Pick an agent or command"
	m.agentList.SetShowStatusBar(false)
	m.phase = phaseAgent
	return m, nil
}

// tableFor builds the selector table for agent from models and an inventory
// snapshot, returning the table and the last-picked row id, which is the row
// the marker sits on: a model's id or a slot profile's (rot.LastPicked). It
// is not the rotation pointer; a caller that needs that reads rot.Last. This
// is the exact table build enterModelPhase performs (last-picked id from rot,
// one usage + refcount store read), shared with refreshTable so a rebuilt
// table matches the original in every column.
func (m model) tableFor(agent string, models []config.Model, snap *localmodels.Snapshot, rot *rotation.Rotation) (modelTable, string) {
	lastID, _ := rot.LastPicked()
	tbl := buildTable(tableInput{
		cfg: m.cfg, agent: agent, models: models, inventory: snap,
		hideDiscovered: m.activeTags != "" || m.activeFamily != "",
		usage:          newUsageStore(),
		slots:          m.slotChecks,
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
	// The command captures only the values tableFor reads (cfg, the active
	// -T/-F filters and the slot profiles), never the live model: the
	// goroutine must not touch it, and copying the whole struct would pin its
	// lists and inputs for the duration of the probe.
	src := model{cfg: m.cfg, activeTags: m.activeTags, activeFamily: m.activeFamily, slotChecks: m.slotChecks}
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

// proceedToLaunch is the one funnel every model launch passes through: the
// phaseModel enter handler, the ollama warning's proceed choice, a pinned or
// lone launch row in enterModelPhase, and a start that just succeeded. It
// captures the highlighted row as the launch model, runs the launch-time
// LiteLLM route check where one is needed, and continues in launchSelected —
// in this same Update, unless the check had to write the route, in which case
// phaseRouting shows the proxy restart first.
func (m model) proceedToLaunch() (model, tea.Cmd) {
	// A guard, not a path: Enter and the pin send a profile row to
	// beginSlotLaunch themselves. Anything else that lands here with a
	// profile highlighted must not go on to ResolveRoute and the single-model
	// launch with the row's id-only Model.
	if it, ok := m.models.SelectedItem().(*modelItem); ok && it.profile != nil {
		if it.blocked != "" {
			m.status = it.blocked
			m.phase = phaseModel
			return m, nil
		}
		return m.beginSlotLaunch(it)
	}
	// The highlighted list item is what gets launched, regardless
	// of any other state. m.models is the single source of truth.
	highlighted, ok := m.models.SelectedItem().(*modelItem)
	if !ok {
		// Reached after a start too (finishStart), and this way back to the
		// picker never gets to launchSelected: the start's note (#258) is
		// shown and dropped here, or it would sit on the model and be put
		// ahead of some later, unrelated launch failure.
		m.status = withNote(m.startNote, "no model selected")
		m.startNote = ""
		return m.refreshTable()
	}
	// Capture the model so launchAndRecord records exactly this pick, also
	// when the launch continues from the routing phase an Update later,
	// without re-reading the picker.
	m.launchModel = highlighted.model
	// A new attempt: whatever the status line still shows — an older error —
	// is not about this launch, and the picker renders m.status. No caller sets a status just before calling
	// this that is meant to survive it.
	m.status = ""
	// A launch row is a model that was already running, and wt may not be
	// what started it, so its LiteLLM route may not exist yet. A row this
	// flow just started is checked too (#275): its start writes the route,
	// but a start whose route write failed still succeeds, and the check is
	// what tries again before the agent meets "Invalid model name". With the
	// route in place it changes nothing and waits for nothing.
	if m.cfg == nil {
		return m.launchSelected()
	}
	// Only a launch that goes through LiteLLM needs the route: a direct or
	// native launch leaves config.yaml alone.
	if route, _ := m.cfg.ResolveRoute(highlighted.model, agents.ProtocolsFor(m.agent)); !route.Litellm {
		return m.launchSelected()
	}
	return m.checkLaunchRoute(highlighted.model)
}

// launchSelected launches m.launchModel. It reads m.launchModel rather than the
// picker's cursor because it also runs from the routing phase's completion
// message, an Update later than the one that picked the row.
// Every path that bails back to the picker re-probes the table: this is reached
// from the start flow, where the table was built before the attempt, so a start
// that succeeded and then failed to launch would otherwise leave the picker
// claiming RUNNING="-" for a model that is loaded and serving. The probe is
// deferred to a command, so paying for it on the paths that did not start
// anything costs nothing on the update loop.
func (m model) launchSelected() (model, tea.Cmd) {
	// wt starts the agent fresh. Whether to continue an earlier conversation
	// is the agent's business: the user says so with the agent's own flags
	// after `--` (m.extraArgs), which launchAgent passes through unchanged.
	// The note of the start that led here (#258), taken once: a later launch
	// from the same picker is not about that start.
	note := m.startNote
	m.startNote = ""
	cmd, err := launchAgent(m.agent, m.launchModel, m.selectedPath, m.yolo, m.cfg, m.extraArgs)
	if err != nil {
		m.status = withNote(note, "launch failed: "+err.Error())
		return m.refreshTable()
	}
	return m.launchAndRecord(cmd)
}

// launchAndRecord runs the agent and has its run command record the model as
// last-launched (so the next picker entry advances rotation) and a
// live-session refcount entry for the model picker's "in use" column.
// Recording is the single commit point, reached only after the ollama check
// has been satisfied and, in the run command, once the launch gate is past —
// so a cancelled ollama warning, a failed ollama check or a declined gate
// never advances the rotation or the refcount. Both state writes are
// best-effort: a failure of either (or both) surfaces in m.status (through
// launchDoneMsg) and the launch still proceeds.
func (m model) launchAndRecord(cmd *exec.Cmd) (model, tea.Cmd) {
	agent, id := m.agent, m.launchModel.ID
	record := func() string {
		var errs []string
		if err := rotation.New().RecordFor(agent, id); err != nil {
			// RecordFor's error names the file that failed (rotation.state, or
			// last-picked.state, a write of its own).
			errs = append(errs, err.Error())
		}
		if err := refcount.NewStore().Record(os.Getpid(), id); err != nil {
			errs = append(errs, "refcount state not saved: "+err.Error())
		}
		return strings.Join(errs, "; ")
	}
	return m.commitLaunch(runRecordAndWaitCmd(cmd, m.agent, m.launchModel, record, m.gateFor()))
}

// findRepoRoot is the picker's `git rev-parse --show-toplevel`
// (worktree.RepoRoot), a seam so a test can count the lookups a picker open
// makes (#390).
var findRepoRoot = worktree.RepoRoot

// loadingStatus is the placeholder View shows until the worktree list
// arrives. It lives in m.status, so the load has to take it back down: every
// later screen renders m.status, and a placeholder left there reads as a
// stuck "loading" message on the agent and model pickers.
const loadingStatus = "loading worktrees..."

// newRunModel builds the TUI's initial model from Run's arguments. It is split
// out of Run so tests can pin the argument-to-field plumbing (e.g. --replace)
// without starting a real tea.Program.
func newRunModel(yolo, allowReplace bool, others, agent, pinned, tags, family string, extraArgs []string, theme themes.Theme, prePath string, cfg *config.Config, removeFuncs RemoveFuncsBuilder) model {
	return model{
		status:       loadingStatus,
		cfg:          cfg,
		theme:        theme,
		yolo:         yolo,
		allowReplace: allowReplace,
		othersMode:   others,
		initialAgent: agent,
		pinnedModel:  pinned,
		activeTags:   tags,
		activeFamily: family,
		extraArgs:    extraArgs,
		prePath:      prePath,
		removeFuncs:  removeFuncs,
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
// occupant without the replace dialog (it comes from --replace). others is
// --others ("keep", "stop" or ""): the -M start path's answer about other
// providers' models loaded beside the model it starts, in place of the
// dialog, by --replace's pinned-row rule (#412). prePath,
// when non-empty, is a pre-resolved worktree path
// (-W/--cwd/outside-repo): the worktree picker is skipped and control starts
// at the agent/command picker (or model phase when agent is pinned). When
// prePath is inside a git repo, repoRoot is seeded so the new-worktree prompt
// has a valid directory even if it becomes reachable from the pre-path entry
// point. cfg is the already-loaded config from cmd/wt's newApp (validated
// before Run is called); it is not re-loaded here.
// applyProfile, when non-nil, applies a local-model launch profile before
// the agent's cmd.Run() and is called from runAndWaitCmd for every launch
// this session makes; cmd/wt's main.go builds it as a closure over the
// already-loaded profiles.toml state (mirrors the non-TUI path's
// applyProfileForLaunch).
// slots carries the slot profiles: the picker shows a row for each one that
// names this agent, and a profile launch applies slots.Apply in place of
// applyProfile.
// removeFuncs builds the row actions of each picker load (the removal
// engine's window, `wt cleanup`'s cleanupRemoveFuncs in cmd/wt's closure):
// nil leaves every row action saying it is unavailable.
// When the launched agent's run failed, Run returns that error (an
// *exec.ExitError for a non-zero exit) after the post-exit flow has run, and
// cmd/wt exits with the agent's status (#412).
func Run(yolo, allowReplace bool, others, agent, pinned, tags, family string, extraArgs []string, theme themes.Theme, prePath string, cfg *config.Config, applyProfile ProfileApplier, slots SlotProfiles, removeFuncs RemoveFuncsBuilder) error {
	initial := withSlotProfiles(newRunModel(yolo, allowReplace, others, agent, pinned, tags, family, extraArgs, theme, prePath, cfg, removeFuncs), slots)
	p := tea.NewProgram(initial, tea.WithAltScreen())
	currentProgram = p
	// Reset any summary/survey state captured by a previous run (e.g.
	// from a test invocation sharing the process).
	pendingSummary = ""
	pendingRouteNotes = ""
	settleOut = &startOutput{what: settleOutWhat}
	drainLine.Store(nil)
	pendingSurveyState = pendingSurvey{}
	profileApplier = applyProfile
	finalModel, err := p.Run()
	// The alt-screen is now torn down (p.Run() has returned and bubbletea
	// has called exitAltScreen), so stdout reaches the user's terminal
	// rather than a discarded buffer.
	// Route output first: it belongs to the launch (or to the launch that
	// never happened), so it goes above the summary and the survey.
	flushRouteNotesAfterRun(finalModel, os.Stderr)
	// The row session's end (RunCleanupList's, one pass and no re-entry):
	// the `r`/`R` hand-off runs on the real terminal and the session's
	// removal log prints however the session ended — the results screen's
	// "these results print again when you quit" is this line. A program that
	// itself failed does no hand-off (RunCleanupList's rule) but still gets
	// its log.
	serr := finishRowSession(finalModel, err)
	printPendingSummaryAndSurvey(cfg)
	if rerr := runResult(finalModel, err); rerr != nil {
		return rerr
	}
	return serr
}

// runResult is what Run returns once the post-exit flow has run: the
// program's own error, else the model's fatal error, else the error the
// launched agent's run returned (an *exec.ExitError for a non-zero exit,
// which cmd/wt turns into wt's own exit status, #412). It is extracted from
// Run() so it is unit-testable without a real tea.Program/TTY.
func runResult(final tea.Model, err error) error {
	if err != nil {
		return err
	}
	fm, ok := final.(model)
	if !ok {
		return nil
	}
	if fm.fatalErr != nil {
		return fm.fatalErr
	}
	return fm.agentErr
}

// finishRowSession is the row list's session end in the picker, RunCleanupList's
// for one pass: the `r` key's row and the `R` key's restore hand off to their
// RemoveFuncs once the alt screen is gone (runRescue, runRestoreRoot — they
// ask on the terminal, and there is no re-entry afterwards: the picker
// session ends with the hand-off and wt exits), then the session's removal
// log prints however the session ended. A program run that failed does no
// hand-off (RunCleanupList's rule) but still gets its log. progErr is what
// the program run returned; the returned error is the hand-off's, or the log
// write's when nothing else failed (as there, never over an earlier one).
// It is split out of Run() so it is unit-testable without a real
// tea.Program/TTY.
func finishRowSession(final tea.Model, progErr error) error {
	fm, ok := final.(model)
	if !ok {
		return nil
	}
	var err, werr error
	printed := 0
	switch {
	case progErr != nil:
		err = progErr
	case fm.rows.exitErr != nil:
		// A post-run re-read that failed ends the session before the
		// hand-offs, its own error (RunCleanupList's order).
		err = fm.rows.exitErr
	default:
		// The picker's program ran with Bubble Tea's own signal handler and
		// forwarded nothing of its own, so there is no forwarding to stop
		// before the hand-off asks on the terminal. The log so far prints
		// first, as in RunCleanupList: the hand-off's prompt can be answered
		// with ctrl+c. There is no re-entry after a hand-off here, so a
		// note (declined, refused, done) ends the session with the error the
		// hand-off would have returned.
		if fm.rows.rescueRow != nil || fm.rows.restoreRoot {
			printed, werr = flushLog(fm.rows.log, printed, werr)
		}
		err = runRescue(fm.rows, fm.rows.rm, func() {})
		if err == nil {
			err = runRestoreRoot(fm.rows, fm.rows.rm, func() {})
		}
		if note, rest := noteOf(err); note != nil {
			err = rest
		}
	}
	if msg := fm.rows.exitMessageFrom(printed); msg != "" {
		if _, w := fmt.Fprintln(os.Stdout, msg); w != nil && werr == nil {
			werr = w
		}
	}
	if err == nil {
		err = werr
	}
	return err
}

// flushRouteNotesAfterRun prints, once the alt screen is gone, whatever route
// output is still pending: the user quit, or the launch never happened, so
// runAndWaitCmd never printed it. A check that is still in flight (the user
// pressed ctrl+c on the routing screen, so its message was never handled) is
// settled first — that waits for the proxy restart, which the process has to
// wait for before exiting anyway, and is when the last of its output is in.
// That wait can last as long as the restart, on a terminal that otherwise
// looks idle, so it is announced first — but only when there really is
// something left to wait for: a check whose command already finished settles
// instantly and gets no line. It is extracted from Run() so it is
// unit-testable without a real tea.Program/TTY.
func flushRouteNotesAfterRun(final tea.Model, w io.Writer) {
	if fm, ok := final.(model); ok && fm.routing != nil {
		if !fm.routing.settled.Load() {
			fmt.Fprintln(w, "wt: waiting for the LiteLLM proxy restart…")
			// The screen that showed why is gone.
			if d := drainView(); d != "" {
				fmt.Fprintf(w, "wt: %s\n", d)
			}
		}
		pendingRouteNotes += fm.routing.settle()
	}
	if fm, ok := final.(model); ok {
		settleQuitSlot(fm, w)
	}
	flushRouteNotes(w)
	// A settling restart started from the picker can still be running, and
	// main() waits for it: from here on its lines have a terminal to go to.
	settleOut.release(w)
	// A start that was still running when wt quit (ctrl+c twice on the start
	// screen) never reported, so what the engine had printed is still in its
	// buffer: print it, and let whatever the engine and its proxy restart
	// print from here on go straight to the terminal. main() waits for that
	// restart before the process exits.
	//
	// A start the user quit out of at the routing stage (startState.quitting)
	// is waited for as well. Nothing of it can be called off there, and its
	// goroutine still has the proxy wait and the check after it to make
	// (settleStart, #343), which for a server that is gone removes the
	// route and starts one more restart. main()'s one WaitPendingRoutes is
	// over when the first restart is, so without this the process left in
	// the middle of the check: the dead model's route still in config.yaml,
	// or config.yaml rewritten and the proxy never restarted. The quit while
	// a cancel drains is not waited for: that ctrl+c is the way out of a
	// teardown that hangs.
	//
	// The start of a profile's local model was told a restart is owed. The
	// quit that is waited for settles it after the wait (settleQuitSlotStart);
	// the one that is not was settled above (settleQuitSlot).
	if fm, ok := final.(model); ok && fm.start != nil && fm.start.out != nil {
		fm.start.out.release(w)
		if fm.start.quitting {
			settleQuitStart(fm.start.ch, w)
			settleQuitSlotStart(fm)
		}
	}
}

// quitStartGrace is how long a start that wt is quitting out of may take to
// finish before the terminal says what it is waiting for. A start that had
// already reported, or is about to, gets no line.
var quitStartGrace = 200 * time.Millisecond

// settleQuitStart returns once the goroutine of a start the user quit out of
// has finished: it closes ch after its result, which nothing reads any more.
// The wait is the proxy restart's, which the process waits for before it
// exits in any case, and one request to the provider's server; it is
// announced once it has lasted quitStartGrace.
func settleQuitStart(ch <-chan tea.Msg, w io.Writer) {
	grace := time.NewTimer(quitStartGrace)
	defer grace.Stop()
	for {
		select {
		case _, open := <-ch:
			if !open {
				return
			}
		case <-grace.C:
			fmt.Fprintln(w, "wt: waiting for the LiteLLM proxy restart…")
			for range ch {
			}
			return
		}
	}
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

	if launched.profile {
		// A slot-profile session. The refcount lines are released, then the
		// stop picker runs as after any non-native launch; it is not
		// filtered to the profile. No survey and no price notice: the
		// profile is not one priced registry model.
		releaseSession()
		runStopPhase(cfg)
		if summary != "" {
			fmt.Println("\n" + summary)
		}
		return
	}

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
		emitPriceNotice(cfg)
	}
}
