// Package tui implements the Bubble Tea terminal UI for wt.
package tui

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/launchwarn"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
)

// phase identifies which screen the TUI is currently showing.
type phase int

const (
	phaseList           phase = iota // worktree list
	phaseAgent                       // agent+command picker (PR 2): picks between configured agents and command drivers before the model screen
	phaseModel                       // agent+model picker
	phaseNewWorktree                 // create-new-worktree prompt
	phaseStarting                    // a start runs through the lifecycle engine, with live progress
	phaseReplaceConfirm              // confirm replacing the running occupant before starting
	phaseLaunching                   // the API path's launch prepares on the model server (remote_start.go)
	phaseCatalog                     // the model server's catalog is loading, or its fetch failed (F retries)
	phaseAsk                         // the launch gate / profile popups (ask_popup.go)
)

// model holds the entire UI state.
type model struct {
	// hooks are the launch-build test seams; the zero value is the real builds.
	hooks launchHooks
	// guard is the pre-launch guard of a launch on the user's Claude
	// subscription (ProfileLayer.Guard, #597), run inside a command before
	// the client session's register and any start; nil is a no-op.
	guard  SubscriptionGuard
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
	// removeFuncs builds each load's row actions (RemoveFuncsBuilder, cmd/wt-session's
	// closure over cleanupRemoveFuncs); nil makes every row action say it is
	// unavailable.
	removeFuncs RemoveFuncsBuilder
	// protectedPaths are the registry's model-artifact paths, handed in by
	// cmd (which owns the registry) so a picker load can show a row's
	// protected_artifact tag; removal still re-checks in the engine (#578).
	protectedPaths []string

	phase phase
	agent string         // current agent name
	tag   string         // active tag group
	cfg   *config.Config // loaded config for the model catalog
	theme themes.Theme   // active color theme; passed from cmd/wt-session

	// client is the model server the picker reads its table from; nil only
	// for a pinned command agent (shell), whose picker never reaches a model.
	client ModelClient
	// catPricing is the pricing member of the latest catalog the picker
	// loaded and catSeen says there was one: the stale-pricing notice after a
	// launch reads them, and fetches once only when no catalog was loaded.
	catPricing *modelapi.Pricing
	catSeen    bool
	// catalogFailed is true while phaseCatalog shows a failed fetch (m.status
	// then holds its `error: ` text and F retries).
	catalogFailed bool
	// catalogCancel cancels the catalog fetch in flight: esc, a superseding
	// F and the program's end call it.
	catalogCancel context.CancelFunc
	// wire is the eligible catalog rows the API table was built from, by id:
	// what a start or a launch reads (catalog.NeedsStart, ToModel).
	wire map[string]modelapi.Row
	// prep is the API path's launch while it prepares on the model server
	// (phaseLaunching); prepRun numbers them so a superseded result drops.
	prep    *launchPrep
	prepRun int
	// hold is the API path's client session, created on the first model
	// chosen and ended by the post-exit flow (exitTail).
	hold *clientHold
	// rslot is a slot-profile launch on the API path from its prep to the
	// agent's build (slot_flow.go's beginRemoteSlotLaunch): set while its
	// local model's start runs, so the start's success builds the profile's
	// command instead of launching the local model alone.
	rslot *remoteSlot

	// model picker (the agent+model screen IS the picker)
	models list.Model // bubble/list of agent+tag models
	// refreshGen numbers the picker's table: each catalog load and start
	// bumps it, and a load's answer is applied only when it carries the
	// current value, so a slow fetch from a superseded load cannot overwrite a
	// newer table.
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

	// start-on-select flow (2026-09-20): Enter on a start row runs the model
	// through the lifecycle engine with live progress; the replace-confirm
	// dialog appears when Start reports the provider's single slot occupied
	// (or unknowable) and AllowReplace was false, or other providers' models
	// loaded beside the model and the question about them unanswered (#412).
	start    *startState   // in-flight start (phaseStarting); nil when idle
	startRun int           // monotonically increasing run id; stale messages are dropped by id
	replace  *replaceState // replace-confirm dialog (phaseReplaceConfirm)
	ask      *askState     // launch gate / profile popup (phaseAsk)

	// creation is the pick that owes its worktree at the commit point (a bare
	// branch, a remote-only branch, or a typed name that owes one): resolved
	// read-only here, made only once the launch gate says go (launchgate.go).
	// nil for every other pick, and cleared by every selection, so an
	// abandoned pick's plan never rides along with a later one.
	creation *launchCreation

	// typedPlan is the plan behind a typed name, kept even when no creation
	// is announced: cmd/wt-session's -W runs plan.Ensure() unconditionally after its
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

	// gateTarget is the Target the launch popup evaluated (nil: no popup
	// path). The released-phase gate consumes it instead of reading again, so
	// a session that appeared while the popup was open is never confirmed
	// against questions that no longer match what the gate acts on. commitAsk
	// resets it at every commit; a decline clears it.
	gateTarget *launchwarn.Target

	// gateAnswer is set (to true) when the popup answered yes: the
	// released-phase gate then asks nothing raw and only creates and claims.
	// Set together with gateTarget; reset at every commit, cleared by a decline.
	gateAnswer *bool

	// profileCarry is the profile popup's answer (nil: the popup did not ask,
	// and the applier resolves and confirms for itself). Reset at every commit,
	// cleared by a decline; the run command hands it to profileApplier.
	profileCarry *profileCarry

	// gateNotes are the route-note lines a gate popup's yes (and a read
	// failure) owe the terminal after the release. They ride the model and
	// reach pendingRouteNotes only when the launch binds, so a decline after
	// them (the profile popup's "no") leaves nothing behind.
	gateNotes string

	// committed is the commit point's in-flight guard: the first selection
	// that reaches a run command takes it (commitLaunch) and every further
	// Enter is ignored while that launch runs. It releases on exactly one
	// outcome — the slot applier's failure (slotAbortMsg), whose return to
	// the model picker is a retry path that must be able to commit again —
	// and otherwise holds to process exit. The agent's own exit
	// (launchDoneMsg) must NOT release it: the terminal is handed back just
	// before that message is processed, so a key queued during teardown can
	// still reach Update before the QuitMsg and would otherwise commit a
	// duplicate run command (a second agent, a second last-picked/usage
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
	// (cmd/wt-session/main.go's runLaunchPath). Nothing currently sets this
	// field — enterModelPhase's route-backs return a status, never a
	// fatal error — but it remains wired into Run() for any future
	// genuinely-fatal condition.
	fatalErr error

	// agentErr is what running the launched agent returned (nil for an
	// exit 0). Run() returns it once the post-exit flow has run, so cmd/wt-session
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
	return loadPickerCmd(m.protectedPaths)
}

// loadPickerCmd loads the worktree picker: it resolves the repository root
// once (#390) and gathers its rows there (loadRowsCmd; it fetches nothing —
// the list's one fetch is the user's `F`, refreshRowsChosenMsg). The PR
// lookups follow once the rows are on screen (gotRows, loadRowPRsCmd).
// Outside a repository the load reports the lookup's error.
func loadPickerCmd(protectedPaths []string) tea.Cmd {
	return func() tea.Msg {
		root, err := findRepoRoot()
		cwd, _ := os.Getwd()
		return loadRowsCmd(root, err, pickerInputs(cwd, protectedPaths), false)()
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
	case slotAbortMsg:
		// The slot applier failed after the terminal was released: no agent
		// ran and nothing was recorded. The run command's outcome is
		// processed, so the commit point's guard releases: the return to the
		// model picker is a retry path, and its next Enter must be able to
		// commit again.
		m.status = "launch failed: " + msg.err.Error()
		m.committed = false
		m.phase = phaseModel
		if h := m.hold; h != nil && len(msg.ids) > 0 {
			// Nothing will use the profile's models now, so they leave the
			// client session (a command, never in Update).
			ids := msg.ids
			return m, func() tea.Msg {
				dropAll(h, ids)
				return nil
			}
		}
		return m, nil
	case catalogLoadedMsg, catalogFailedMsg:
		return m.handleCatalogMsg(msg)
	case jobCancelMsg:
		return m.handleJobCancel(msg)
	case remotePreparedMsg:
		return m.handlePrepared(msg)
	case remoteSlotPreparedMsg:
		return m.handleRemoteSlotPrepared(msg)
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		// Every list is sized by fitLists, from the lines and columns its
		// screen prints around it.
		if m.phase == phaseNewWorktree {
			m.newInput.Width = msg.Width - 4
		}
	case rowsLoadedMsg:
		return m.gotRows(msg)
	case rowsFilledMsg:
		return m.gotFilled(msg)
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
			return m, swapRemoveFuncs(&m.rows, func() RemoveFuncs { return m.removeFuncsFor(msg.Snap, m.rowsIn) })
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
		// A done list is quitting for a hand-off (`r`/`R`): it must not
		// start a fetch whose result the done guard would discard, the same
		// as the PR fill landing in that window (#580).
		if !m.ready || m.phase != phaseList || m.rowsRefreshing || m.rows.done {
			return m, nil
		}
		m.rowsRefreshing, m.reloadGen = true, m.rowsGen
		m.rows.fetching = true
		return m, loadRowsCmd(m.repoRoot, nil, m.rowsIn, true)
	case cleanupSignalMsg:
		// An OS signal is the row list's own while it shows: Ruling T — a
		// removal run in flight is never abandoned; the list remembers the
		// signal and quits once the run ends, after its outcomes print
		// (#577). Everywhere else in the picker it quits now, exactly what
		// Bubble Tea's own handler did.
		if m.ready && m.phase == phaseList {
			return m.updateRows(msg)
		}
		return m, tea.Quit
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
		// And the API path's launch prep, which nothing but esc and ctrl+c
		// may interrupt.
		if m.phase == phaseLaunching && m.prep != nil {
			return m.handleLaunchingKey(msg)
		}
		// The launch gate / profile popups own the keyboard, q and ctrl+c
		// included: they quit through the decline's releases.
		if m.phase == phaseAsk && m.ask != nil {
			return m.updateAsk(msg)
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
		// The catalog's loading and failed screens own the keyboard (only the
		// keys they list act).
		if m.phase == phaseCatalog {
			return m.handleCatalogKey(msg)
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
			if m.phase == phaseReplaceConfirm {
				var drop tea.Cmd
				m, drop = m.dropRemoteSlot()
				m.phase = phaseModel
				m.status = ""
				return m, drop
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
				// succeed.
				if item.issue != "" {
					m.status = "cannot launch " + item.name + ": " + item.issue
					return m, nil
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
				// check (and potentially last-picked/usage state) for whatever
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
				// A blocked row says why, a row the server must start runs
				// the start job, any other launches on the gateway
				// (remote_start.go).
				return m.chooseRemote(highlighted, highlighted.model.ID == m.pinnedModel)
			case phaseReplaceConfirm:
				if item, ok := m.replace.choices.SelectedItem().(choiceItem); ok {
					switch item.choice {
					case replaceCancelChoice:
						m, drop := m.dropRemoteSlot()
						m.phase = phaseModel
						return m, drop
					case replaceProceedChoice:
						return m.retryStart(m.replace.item, true, m.replace.others)
					case othersKeepChoice:
						// #412: keep other providers' models; AllowReplace
						// only for same-family occupants the dialog showed.
						return m.retryStart(m.replace.item, m.replace.allow, othersPlan{answer: modelapi.OthersDecision{Decided: true}})
					case othersStopChoice:
						// Stop exactly the stoppable models the dialog showed.
						return m.retryStart(m.replace.item, m.replace.allow, othersPlan{answer: modelapi.OthersDecision{Decided: true, Stop: m.replace.stop}})
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
				// A typed name is a pick, not a create: cmd/wt-session's -W rule
				// decides which side of the gate it lands on. A name that
				// owes its worktree — a new branch, a remote tracking branch,
				// or a branch no worktree holds yet (and no directory at the
				// plan's path) — is a creation pick, bound here read-only and
				// made at the commit point exactly like a bare-branch row; the
				// rest launch into the worktree they name. Nothing is created
				// here, so a declined gate leaves nothing behind. The plan
				// is kept either way (typedPlan): the commit point runs its
				// Ensure unconditionally, as cmd/wt-session's -W does after its one
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
			body += "\n" + themes.ErrorStyle(m.theme).Render(m.newError)
		}
		body += "\n[enter] create   [esc] cancel"
		return body
	}
	if m.phase == phaseStarting {
		if m.width <= 0 || m.height <= 0 {
			return "start progress (waiting for window size)"
		}
		return clip(m.startingView(), m.width)
	}
	if m.phase == phaseLaunching && m.prep != nil {
		if m.width <= 0 || m.height <= 0 {
			return "launch (waiting for window size)"
		}
		return clip(m.launchingView(), m.width)
	}
	if m.phase == phaseAsk && m.ask != nil {
		if m.width <= 0 || m.height <= 0 {
			return "confirm (waiting for window size)"
		}
		return drawnFrame(&m.ask.choices, m.height, m.askFrames()...)(m.ask.choices.View())
	}
	if m.phase == phaseReplaceConfirm {
		if m.width <= 0 || m.height <= 0 {
			return "replace confirm (waiting for window size)"
		}
		return drawnFrame(&m.replace.choices, m.height, m.replaceFrames()...)(m.replace.choices.View())
	}
	if m.phase == phaseCatalog {
		return m.catalogView()
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
// driver — in the selected worktree, skipping the model screen. name is the
// picked command's driver name; the same launch builder as the agents' is
// used (a command's zero model needs no gateway), so a future command driver
// launches by its own name rather than being hardcoded to "shell".
func (m model) launchCommand(name string) (model, tea.Cmd) {
	cmd, _, err := m.hooks.buildLaunch()(name, config.Model{}, m.selectedPath, false, agents.Gateway{}, nil, m.extraArgs)
	if err == nil {
		queueLaunchNotes(name, cmd, m.extraArgs)
	}
	if err != nil {
		m.status = "launch failed: " + err.Error()
		return m, nil
	}
	return m.commitAsk(config.Model{}, func(mm model) (model, tea.Cmd) {
		return mm.commitLaunch(runAndWaitCmd(cmd, name, config.Model{}, mm.gateFor()))
	})
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
		// The installed check runs first: there is no binary to launch.
		if !installed(m.agent) {
			m.status = "cannot launch " + m.agent + ": not installed — install the binary"
			return m, nil
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
	m.agentList = list.New(items, themes.ListDelegate(m.theme), m.width-2, m.height-2)
	m.agentList.Title = "Pick an agent or command"
	m.agentList.SetShowStatusBar(false)
	m.agentList.InfiniteScrolling = true // the cursor wraps at either edge (spec §6)
	m.phase = phaseAgent
	return m, nil
}

// enterModelPhaseForAgent loads the model server's catalog for m.agent under
// the active filters (-T/-F) and builds the picker from it
// (beginCatalogLoad). In native mode with no -M/-T/-F (a nil client, #615)
// it launches the agent natively instead.
func (m model) enterModelPhaseForAgent() (model, tea.Cmd) {
	if m.client == nil {
		// Only a model-driven agent reaches this phase, and wt-session opens
		// the picker with no client only in native mode with no model asked
		// for (it refuses every other launch, an unset mode included, first).
		return m.launchNative()
	}
	return m.beginCatalogLoad()
}

// launchNative is the launch of m.agent in native mode, with no model server
// (#615): the native model through the same builder as every launch, a zero
// Gateway, and the one native note printed once the launch gate is past (a
// declined gate launches nothing, so it says nothing). Nothing is registered
// or recorded: there is no server and no model id.
func (m model) launchNative() (model, tea.Cmd) {
	mdl := agents.NativeModel()
	cmd, _, err := m.hooks.buildLaunch()(m.agent, mdl, m.selectedPath, m.yolo, agents.Gateway{}, nil, m.extraArgs)
	if err != nil {
		m.status = "launch failed: " + err.Error()
		return m, nil
	}
	queueLaunchNotes(m.agent, cmd, m.extraArgs)
	agent := m.agent
	return m.commitAsk(mdl, func(mm model) (model, tea.Cmd) {
		// The gate is built at bind time: it reads the answer the popup left.
		return mm.commitLaunch(runAndWaitCmd(cmd, agent, mdl, sayAfterGate(mm.gateFor(), os.Stderr, agents.NativeNote(agent))))
	})
}

// sayAfterGate wraps a launch gate so line is written to w only once the gate
// lets the launch go on: the terminal is the user's again at the gate
// (runRecordAndWaitWith released it first), and a declined gate launches
// nothing, so it must not have announced a launch.
func sayAfterGate(gate func() (bool, error), w io.Writer, line string) func() (bool, error) {
	return func() (bool, error) {
		ok, err := gate()
		if ok {
			fmt.Fprintln(w, line)
		}
		return ok, err
	}
}

// routeBackToAgent leaves the model phase for the agent picker with status as
// the reason, clearing any -M pin so re-entry validates fresh: a catalog
// whose pin or table cannot be used routes back this way.
func (m model) routeBackToAgent(status string) (model, tea.Cmd) {
	m.status = status
	m.pinnedModel = ""
	items := buildAgentList(m.cfg)
	m.agentList = list.New(items, themes.ListDelegate(m.theme), m.width-2, m.height-2)
	m.agentList.Title = "Pick an agent or command"
	m.agentList.SetShowStatusBar(false)
	m.agentList.InfiniteScrolling = true // the cursor wraps at either edge (spec §6)
	m.phase = phaseAgent
	return m, nil
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
func newRunModel(yolo, allowReplace bool, others, agent, pinned, tags, family string, extraArgs []string, theme themes.Theme, prePath string, models ModelClient, cfg *config.Config, removeFuncs RemoveFuncsBuilder) model {
	return model{
		client:       models,
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
// cmd/wt-session. allowReplace grants the -M start path permission to stop a running
// occupant without the replace dialog (it comes from --replace). others is
// --others ("keep", "stop" or ""): the -M start path's answer about other
// providers' models loaded beside the model it starts, in place of the
// dialog, by --replace's pinned-row rule (#412). prePath,
// when non-empty, is a pre-resolved worktree path
// (-W/--cwd/outside-repo): the worktree picker is skipped and control starts
// at the agent/command picker (or model phase when agent is pinned). When
// prePath is inside a git repo, repoRoot is seeded so the new-worktree prompt
// has a valid directory even if it becomes reachable from the pre-path entry
// point. models, when non-nil, is the model server the picker reads its
// table from; nil is the legacy registry path. cfg is the already-loaded config from cmd/wt-session's newApp (validated
// before Run is called); it is not re-loaded here.
// profileLayer, when its funcs are non-nil, resolves a local-model launch
// profile at the commit point (asked as a popup) and applies it before the
// agent's cmd.Run(), called from runAndWaitCmd for every launch this session
// makes; cmd/wt-session's main.go builds it as closures over the
// already-loaded profiles.toml state (mirrors the non-TUI path's
// applyProfileForLaunch).
// slots carries the slot profiles: the picker shows a row for each one that
// names this agent, and a profile launch applies slots.Apply in place of
// profileLayer.
// removeFuncs builds the row actions of each picker load (the removal
// engine's window, `wt worktrees`'s cleanupRemoveFuncs in cmd/wt-session's closure):
// nil leaves every row action saying it is unavailable.
// When the launched agent's run failed, Run returns that error (an
// *exec.ExitError for a non-zero exit) after the post-exit flow has run, and
// cmd/wt-session exits with the agent's status (#412).
func Run(yolo, allowReplace bool, others, agent, pinned, tags, family string, extraArgs []string, theme themes.Theme, prePath string, models ModelClient, cfg *config.Config, profileLayer ProfileLayer, slots SlotProfiles, removeFuncs RemoveFuncsBuilder, protectedPaths []string) error {
	m := newRunModel(yolo, allowReplace, others, agent, pinned, tags, family, extraArgs, theme, prePath, models, cfg, removeFuncs)
	m.protectedPaths = protectedPaths
	m.guard = profileLayer.Guard
	initial := withSlotProfiles(m, slots)
	// Bubble Tea's own signal handler quits on the first signal in any
	// phase — mid-removal too, losing the run's outcomes; the model handles
	// them instead (Ruling T, the cleanup list's rule, #577).
	p := tea.NewProgram(initial, tea.WithAltScreen(), tea.WithoutSignalHandler())
	currentProgram = p
	// Reset any summary/survey state captured by a previous run (e.g.
	// from a test invocation sharing the process).
	pendingSummary = ""
	pendingRouteNotes = ""
	clientNotes = &startOutput{what: "client session"}
	pendingSurveyState = pendingSurvey{}
	profileApplier, profileResolver = profileLayer.Apply, profileLayer.Resolve
	finalModel, stop, err := guardedRun(forwardSignals(p.Send), p.Run)
	// The forwarding stops with the program: everything after this runs
	// outside Bubble Tea (the remote start settle, the route notes, the row
	// session's hand-offs and their prompts) and needs Go's default signal
	// handling back — runRescue's rule.
	stop()
	// A start or a catalog fetch the user quit out of on the API path stops
	// waiting first (the job keeps running on the server), so its goroutine
	// is over before anything prints.
	settleRemoteStart(finalModel)
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
	// The tail ends the client session (exitTail): there is no stop picker,
	// the model server stops a model after its idle grace.
	fm, _ := finalModel.(model)
	printPendingSummaryAndSurveyTail(exitTailOf(fm))
	if rerr := runResult(finalModel, err); rerr != nil {
		return rerr
	}
	return serr
}

// runResult is what Run returns once the post-exit flow has run: the
// program's own error, else the model's fatal error, else the error the
// launched agent's run returned (an *exec.ExitError for a non-zero exit,
// which cmd/wt-session turns into wt's own exit status, #412). It is extracted from
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

// flushRouteNotesAfterRun prints, once the alt screen is gone, whatever
// notes are still pending: the user quit, or the launch never happened, so
// runAndWaitCmd never printed them; then what the client session's heartbeat
// said, and from here on lets it write straight to the terminal. It is
// extracted from Run() so it is unit-testable without a real
// tea.Program/TTY.
func flushRouteNotesAfterRun(_ tea.Model, w io.Writer) {
	flushRouteNotes(w)
	clientNotes.release(w)
}
