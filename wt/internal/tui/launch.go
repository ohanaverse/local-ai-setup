package tui

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rootclaim"
	"github.com/ohanaverse/local-ai-setup/wt/internal/sessionclaim"
)

// GateError is a launch-gate failure carried out as the run's error: wt's own
// failure at the commit point (the creation or validation the gate ran), as
// opposed to the agent's own exit. It wraps the failure with no Unwrap method
// on purpose: a git failure can carry git's *exec.ExitError (gitError's
// empty-stderr branch, startPhrase's rev-parse failure), and cmd/wt-session's
// agentExitStatus reads any error with an *exec.ExitError anywhere in its
// chain as the agent's own quiet exit (#412) — printing nothing and exiting
// with git's status. Held this way the failure stays a plain error: main
// prints it once as its "wt:" line and exits 1, exactly as cmd/wt-session's -W path
// refuses a creation. The field is exported because the routing seam
// (agentExitStatus, reportExit) lives in package main and its tests build
// this type.
type GateError struct{ Err error }

// Error is the failure's own message, unchanged.
func (e GateError) Error() string { return e.Err.Error() }

// launchDoneMsg is emitted after the agent subprocess exits.
type launchDoneMsg struct {
	// err is what running the agent returned, or the profile error that
	// kept it from starting, or the launch gate's own failure (GateError).
	// Run returns either one, so wt exits as runAgentCmd does (#412).
	err error
	// status is what the launch's own state writes reported (record's
	// errors), shown on the status line as launchAndRecord used to set it.
	status string
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

// pendingRouteNotes holds the lines wt has for the user that arise while the
// alt screen is up, where stderr is not reliably visible. Two things feed it:
// the model server's notes about the client session (a register it refused,
// recordRouteNotes) and the pre-launch notes about the command being built
// (queueLaunchNotes). It follows pendingSummary's
// capture-then-emit pattern with two emit points: runAndWaitCmd prints it the
// moment it releases the terminal, above the agent's own output, and Run()
// prints whatever is left after p.Run() returns (the user quit, or the launch
// never happened). Appended to by recordRouteNotes; reset by Run() before
// launch.
var pendingRouteNotes string

// flushRouteNotes prints pendingRouteNotes to w and clears it, so the lines
// are shown exactly once. Callers pass os.Stderr at a point where it is the
// real terminal; it takes a writer so tests can read what was printed.
//
// What the client session's heartbeat has said so far (clientNotes) goes out
// with them: both emit points are where the terminal is real.
func flushRouteNotes(w io.Writer) {
	notes := pendingRouteNotes + clientNotes.take()
	pendingRouteNotes = ""
	if notes == "" {
		return
	}
	// The captured lines end in a newline; guard anyway so the agent's first
	// line of output never continues one of ours.
	if !strings.HasSuffix(notes, "\n") {
		notes += "\n"
	}
	fmt.Fprint(w, notes)
}

// pendingSurvey holds the agent/model to survey once the alt-screen tears
// down, mirroring pendingSummary's capture-then-emit pattern (printing
// here would land inside the discarded alt-screen buffer).
type pendingSurvey struct {
	agent string
	m     config.Model
	// profile marks a slot-profile session: no survey and no price notice.
	profile bool
}

// pendingSurveyState is populated by runAndWaitCmd next to pendingSummary.
// Run() invokes the survey through it after printing the summary. Reset by
// Run() before launch.
var pendingSurveyState pendingSurvey

// ProfileApplier applies a local-model launch profile (env/args/
// config_content/wrapper) to cmd before it runs, returning a cleanup func
// the caller MUST invoke after cmd.Run() returns (success or failure) to
// restore anything the profile's config_content mechanism rewrote. It
// mirrors cmd/wt-session's applyProfileForLaunch signature, minus cfg/pp: this
// package cannot import cmd/wt-session (package main — not importable, and it
// would be a cycle regardless, since cmd/wt-session already imports internal/tui)
// or internal/profiles directly (mechanism validation needs an
// agent-driver lookup that lives with cmd/wt-session's own app state). Run's
// caller builds the real implementation as a closure over its own
// cfg/precomputedProfiles.
type ProfileApplier func(cmd *exec.Cmd, agent string, m config.Model) (cleanup func() error, err error)

// profileApplier is a seam: production is set by Run() from its
// ProfileApplier parameter before p.Run() starts. nil — the zero value
// every test in this file that calls runAndWaitCmd directly (without going
// through Run()) runs under — means "no profile layer available", and
// runAndWaitCmd treats that as a no-op, matching the non-TUI path's own
// graceful-degradation posture for a missing/disabled profiles.toml.
var profileApplier ProfileApplier

// SlotProfileApplier applies a slot profile's args and wrapper to cmd. It
// fails closed: an error aborts the launch. cmd/wt-session hands in applySlotProfile.
// It is a second closure beside ProfileApplier for the same reason that one
// exists: this package cannot import cmd/wt-session.
type SlotProfileApplier func(cmd *exec.Cmd, agent string, row catalog.Row) (cleanup func() error, err error)

// SlotProfiles is what Run needs for slot profiles: the statically validated
// profiles (the picker builds their rows) and the applier of a profile launch.
type SlotProfiles struct {
	Checks []profiles.SlotCheck
	Apply  SlotProfileApplier
}

// slotApplier is set by Run from SlotProfiles.Apply. nil means no applier:
// a profile launch then aborts with "no slot-profile applier". It is never a
// no-op, unlike a nil profileApplier: a slot profile that cannot be fully
// applied must not launch.
var slotApplier SlotProfileApplier

// withSlotProfiles is how Run's slot argument reaches the TUI: the checks go
// on the model, the applier into its seam. It is split out of Run so a test
// can pin it without a real tea.Program.
func withSlotProfiles(m model, s SlotProfiles) model {
	slotApplier = s.Apply
	m.slotChecks = s.Checks
	return m
}

// shellDir is a test seam over os.Getwd: the directory the user typed the
// command in, which is what they meant a relative passthrough path against.
var shellDir = os.Getwd

// queueLaunchNotes adds the pre-launch notes for a built command to
// pendingRouteNotes. Today that is agents.RelativeArgNotes: a passthrough
// argument naming a file from the directory wt was run in, but not from the
// worktree the agent starts in. The non-TUI path prints these straight to
// stderr; here the picker still owns the terminal, so they wait for
// runAndWaitCmd to release it and are printed above the agent's own output.
func queueLaunchNotes(agent string, cmd *exec.Cmd, extraArgs []string) {
	wd, err := shellDir()
	if err != nil {
		return
	}
	for _, note := range agents.RelativeArgNotes(agent, wd, cmd.Dir, extraArgs) {
		// A launch that failed and was retried builds its command again;
		// the note is about the arguments, so say it once.
		if line := note + "\n"; !strings.Contains(pendingRouteNotes, line) {
			pendingRouteNotes += line
		}
	}
}

// runAndWaitCmd releases the TUI, runs the agent with stdio wired to the
// terminal, restores the TUI, captures the post-run summary line into
// pendingSummary (consumed by Run() after p.Run() returns), and returns a
// launchDoneMsg. It records nothing: a command launch has no model to record.
func runAndWaitCmd(cmd *exec.Cmd, agent string, m config.Model, gate func() (bool, error)) tea.Cmd {
	return runRecordAndWaitWith(cmd, agent, m, nil, gate, func() {})
}

// runRecordAndWaitWith is runAndWaitCmd with the launch's state writes and
// the release a launch that never reached its agent makes (the client
// session's end, handlePrepared). record, when non-nil, is called once the
// launch is committed (the gate is past and the agent starts next, or the
// profile failed, as it always did after recording) and returns the status
// its errors make, "" for none. gate, when non-nil, is the launch's one
// question (m.launchGate, through m.gateFor) at the commit point; a decline
// calls record never, so it leaves no trace. A nil gate is a direct test
// call: it asks nothing — the claim is the run's own and is taken either way,
// once the gate (if any) is past.
func runRecordAndWaitWith(cmd *exec.Cmd, agent string, m config.Model, record func() string, gate func() (bool, error), release func()) tea.Cmd {
	if record == nil {
		record = func() string { return "" }
	}
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
		// The terminal is the user's again: print the pending notes
		// before the profile prompt or the agent prints anything, so the
		// lines sit above the agent's own output instead of being lost with
		// the alt screen.
		flushRouteNotes(os.Stderr)
		var profileCleanup func() error
		if profileApplier != nil {
			var perr error
			profileCleanup, perr = profileApplier(cmd, agent, m)
			if perr != nil {
				// Mirrors cmd/wt-session's runAgentCmd: a pre-launch profile
				// application error (e.g. a wrapper profile naming a
				// missing binary) is still recorded, and must still
				// end the client session, and still
				// populate pendingSummary
				// (duration 0) so Run() prints it exactly as on a real
				// exit — but there is no session to survey or a stop
				// picker to offer, since nothing ever ran, so
				// pendingSurveyState is deliberately left at its zero
				// value.
				status := record()
				release()
				pendingSummary = agents.Summary(agent, m, 0)
				// Run() returns perr, so main prints it once, as it does
				// for runAgentCmd's return.
				return launchDoneMsg{err: perr, status: status}
			}
		}
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if profileCleanup == nil {
			profileCleanup = func() error { return nil }
		}
		if gate != nil {
			if ok, gerr := gate(); !ok {
				// A "no" at the launch gate is a choice, not a failure: the
				// run does not start and nothing is created, claimed,
				// recorded or summarised. It is a declined prompt, so it
				// ends the run with errLaunchDeclined (exit 3), unlike
				// quitting the picker, which stays exit 0. A
				// creation that failed after the person continued (gerr
				// non-nil) is a failure, not a choice: it travels out as the
				// run's error — as a GateError, so main's exit routing never
				// reads a git exit status inside it as the agent's own quiet
				// exit — and main prints it once and exits non-zero, cmd/wt-session's
				// -W path through refuse. Either
				// way the model server may already have started a model for
				// it; pendingSurveyState keeps the standard tail, which ends
				// the client session so the server's idle grace can stop it.
				_ = profileCleanup()
				pendingSurveyState = pendingSurvey{agent: agent, m: m}
				gerr = gateEndError(gerr)
				return launchDoneMsg{err: gerr}
			}
		}
		// Every launch claims the worktree the agent runs in, for exactly
		// the agent's run (Task 405) — the run's own claim and release,
		// taken after the gate (a declined gate claims nothing) and after
		// any worktree creation the gate made. The run commands that cannot
		// claim for themselves (the slot flow's waiter) are covered by the
		// gate's own claim of the same claim file; here it is twice-safe. A
		// command with no Dir names no directory and claims nothing, as
		// claimLaunch decrees for the same empty Dir (filepath.Abs("") is
		// the working directory: a claim there would give every other
		// session a false held warning for a root this launch never named).
		// HoldLaunch's others are dropped on purpose: the gate's warning
		// pass named any other holder already, and a launch prints exactly
		// one held line. A claim that cannot be read or written is one note
		// and never a refusal.
		claimRelease := func() {} // the no-Dir release, so the defer below is unconditional
		if cmd.Dir != "" {
			release, _, cerr := rootclaim.HoldLaunch(cmd.Dir, agent)
			claimRelease = release
			if cerr != nil {
				fmt.Fprintf(os.Stderr, "wt: note: %s\n", sessionclaim.CheckFailed(cmd.Dir, cerr))
			}
		}
		defer claimRelease()
		// The commit point: the agent runs next. The gate has made any
		// worktree this pick owed and claimed the run for the agent.
		status := record()
		duration, err := agents.RunAndCleanup(cmd, profileCleanup)
		releaseRootClaim()
		// Capture (do not print) the summary line. Printing here would
		// land inside the alt-screen buffer, which bubbletea discards at
		// tea.Quit shutdown. Run() reads pendingSummary after p.Run()
		// returns and prints it to the parent terminal — the only point
		// in the TUI lifecycle where stdout reaches the user's terminal.
		pendingSummary = agents.Summary(agent, m, duration)
		pendingSurveyState = pendingSurvey{agent: agent, m: m}
		return launchDoneMsg{err: err, status: status}
	}
}

// choiceItem adapts a prompt choice to list.Item. The prompt screens
// (ollama, replace) share this single type; each keeps its own named
// choice enum, stored in the any-typed choice field.
type choiceItem struct {
	choice any
	title  string
	desc   string
}

func (c choiceItem) FilterValue() string { return c.title }
func (c choiceItem) Title() string       { return c.title }
func (c choiceItem) Description() string { return c.desc }
