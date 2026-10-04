package tui

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

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

// pendingRouteNotes holds the lines wt has for the user that arise while the
// alt screen is up, where stderr is not reliably visible. Two things feed it:
// what the launch-time LiteLLM route check printed (#192) —
// `wt: LiteLLM route for <id> updated` and any route warning, collected in the
// check's own buffer (checkLaunchRoute) — and the pre-launch notes about the
// command being built (queueLaunchNotes). It follows pendingSummary's
// capture-then-emit pattern with two emit points: runAndWaitCmd prints it the
// moment it releases the terminal, above the agent's own output, and Run()
// prints whatever is left after p.Run() returns (the user quit, or the launch
// never happened). Appended to by recordRouteNotes; reset by Run() before
// launch.
var pendingRouteNotes string

// flushRouteNotes prints pendingRouteNotes to w and clears it, so the lines
// are shown exactly once. Callers pass os.Stderr at a point where it is the
// real terminal; it takes a writer so tests can read what was printed.
func flushRouteNotes(w io.Writer) {
	notes := pendingRouteNotes
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
}

// pendingSurveyState is populated by runAndWaitCmd next to pendingSummary.
// Run() invokes the survey through it after printing the summary. Reset by
// Run() before launch.
var pendingSurveyState pendingSurvey

// ProfileApplier applies a local-model launch profile (env/args/
// config_content/wrapper) to cmd before it runs, returning a cleanup func
// the caller MUST invoke after cmd.Run() returns (success or failure) to
// restore anything the profile's config_content mechanism rewrote. It
// mirrors cmd/wt's applyProfileForLaunch signature, minus cfg/pp: this
// package cannot import cmd/wt (package main — not importable, and it
// would be a cycle regardless, since cmd/wt already imports internal/tui)
// or internal/profiles directly (mechanism validation needs an
// agent-driver lookup that lives with cmd/wt's own app state). Run's
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

// launchAgent builds the command for agent/model in worktreePath, optionally
// appending passthrough args and a resume flag for claude or opencode. It
// delegates to agents.BuildLaunchCmd so the launch construction logic lives
// in one place.
func launchAgent(agent string, m config.Model, worktreePath string, yolo bool, sess *session.Session, cfg *config.Config, extraArgs []string) (*exec.Cmd, error) {
	cmd, err := agents.BuildLaunchCmd(agent, m, worktreePath, yolo, sess, cfg, extraArgs)
	if err == nil {
		queueLaunchNotes(agent, cmd, extraArgs)
	}
	return cmd, err
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

// buildPassthrough is a test seam wrapping agents.BuildPassthroughCmd,
// mirroring launchAgent above — used for an agent with no config.toml entry
// (issue #147).
var buildPassthrough = agents.BuildPassthroughCmd

// resumeSession is a test seam wrapping agents.ResumeSession — the single
// session lookup both launch paths share. The TUI and the non-TUI path used to
// disagree about what a failed lookup means (abort the launch vs. silently
// launch fresh), so the decision lives in agents.ResumeSession and is reached
// through here.
var resumeSession = agents.ResumeSession

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
		// The terminal is the user's again: print what the route check said
		// before the profile prompt or the agent prints anything, so the
		// lines sit above the agent's own output instead of being lost with
		// the alt screen.
		flushRouteNotes(os.Stderr)
		var profileCleanup func() error
		if profileApplier != nil {
			var perr error
			profileCleanup, perr = profileApplier(cmd, agent, m)
			if perr != nil {
				// Mirrors cmd/wt's runAgentCmd: a pre-launch profile
				// application error (e.g. a wrapper profile naming a
				// missing binary) must still release the refcount entry
				// launchAndRecord already recorded before this command
				// was returned, and still populate pendingSummary
				// (duration 0) so Run() prints it exactly as on a real
				// exit — but there is no session to survey or a stop
				// picker to offer, since nothing ever ran, so
				// pendingSurveyState is deliberately left at its zero
				// value.
				releaseSession()
				pendingSummary = agents.Summary(agent, m, 0)
				// The terminal is released here, so this reaches the
				// user's real stderr. It is the only diagnostic: the
				// Update handler never renders launchDoneMsg's error and
				// Run() does not return it.
				fmt.Fprintf(os.Stderr, "wt: %v\n", perr)
				return launchDoneMsg{err: perr}
			}
		}
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if profileCleanup == nil {
			profileCleanup = func() error { return nil }
		}
		duration, err := agents.RunAndCleanup(cmd, profileCleanup)
		// Capture (do not print) the summary line. Printing here would
		// land inside the alt-screen buffer, which bubbletea discards at
		// tea.Quit shutdown. Run() reads pendingSummary after p.Run()
		// returns and prints it to the parent terminal — the only point
		// in the TUI lifecycle where stdout reaches the user's terminal.
		pendingSummary = agents.Summary(agent, m, duration)
		pendingSurveyState = pendingSurvey{agent: agent, m: m}
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
