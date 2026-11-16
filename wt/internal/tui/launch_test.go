package tui

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
)

// TestRunAndWaitCmdWiresStdio uses a no-op command (true) to verify
// that runAndWaitCmd wires Stdin/Stdout/Stderr and returns a
// launchDoneMsg. Without stdio wiring, the agent would see no input
// and write to /dev/null — a regression here would silently break
// every interactive agent invocation while passing all the picker
// tests above.
func TestRunAndWaitCmdWiresStdio(t *testing.T) {
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skip("true not available")
	}
	cmd := exec.Command(truePath)
	msg := runAndWaitCmd(cmd, "shell", config.Model{}, nil)()
	done, ok := msg.(launchDoneMsg)
	if !ok {
		t.Fatalf("msg = %T, want launchDoneMsg", msg)
	}
	if done.err != nil {
		t.Errorf("true exited with error: %v", done.err)
	}
}

// TestRunAndWaitCmdCapturesSummaryOnSuccess asserts the TUI launch path
// captures the summary line via pendingSummary rather than printing it
// directly. The previous design called fmt.Println inside the closure,
// which landed inside the alt-screen buffer that bubbletea discards at
// tea.Quit shutdown — so the user never saw the summary. Without this
// test, a regression that re-introduced a direct fmt.Println here
// would silently break the user-visible "wt: agent · model · 1s"
// line on every TUI launch.
func TestRunAndWaitCmdCapturesSummaryOnSuccess(t *testing.T) {
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skip("`true` not available")
	}
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	prev := pendingSummary
	pendingSummary = ""
	t.Cleanup(func() { pendingSummary = prev })

	cmd := exec.Command(truePath)
	msg := runAndWaitCmd(cmd, "claude", config.Model{ID: "claude/sonnet"}, nil)()
	if _, ok := msg.(launchDoneMsg); !ok {
		t.Fatalf("msg = %T, want launchDoneMsg", msg)
	}
	w.Close()
	out, _ := io.ReadAll(r)

	// runAndWaitCmd must NOT print the summary itself — that goes into
	// the alt-screen buffer, which is discarded. It must populate the
	// package variable instead.
	if strings.Contains(string(out), "wt: claude · claude/sonnet ·") {
		t.Errorf("runAndWaitCmd printed to stdout directly; summary must be captured into pendingSummary, not printed. stdout = %q", string(out))
	}
	if !strings.Contains(pendingSummary, "wt: claude · claude/sonnet ·") {
		t.Errorf("pendingSummary = %q, want it to contain the formatted line", pendingSummary)
	}
}

// TestRunAndWaitCmdCapturesSummaryOnFailure asserts the summary is
// captured even when the subprocess exits non-zero. The post-run line
// is supposed to fire on every exit, success or failure — a regression
// that only emitted on success would hide what just failed.
func TestRunAndWaitCmdCapturesSummaryOnFailure(t *testing.T) {
	falsePath, err := exec.LookPath("false")
	if err != nil {
		t.Skip("`false` not available")
	}
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	prev := pendingSummary
	pendingSummary = ""
	t.Cleanup(func() { pendingSummary = prev })

	cmd := exec.Command(falsePath)
	msg := runAndWaitCmd(cmd, "shell", config.Model{}, nil)()
	done, ok := msg.(launchDoneMsg)
	if !ok {
		t.Fatalf("msg = %T, want launchDoneMsg", msg)
	}
	if done.err == nil {
		t.Fatal("expected error from `false`")
	}
	w.Close()
	out, _ := io.ReadAll(r)

	if strings.Contains(string(out), "wt: shell ·") {
		t.Errorf("runAndWaitCmd printed to stdout directly on failure; summary must be captured. stdout = %q", string(out))
	}
	if !strings.Contains(pendingSummary, "wt: shell ·") {
		t.Errorf("pendingSummary = %q, want it to contain the formatted line on failure", pendingSummary)
	}
}

// TestRunAndWaitCmdCapturesPendingSurvey verifies runAndWaitCmd stashes the
// agent/model into pendingSurveyState next to pendingSummary, so Run() can
// invoke the post-session survey after the alt-screen tears down — the
// same capture-then-emit pattern the summary line uses.
func TestRunAndWaitCmdCapturesPendingSurvey(t *testing.T) {
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skip("`true` not available")
	}
	prev := pendingSurveyState
	pendingSurveyState = pendingSurvey{}
	t.Cleanup(func() { pendingSurveyState = prev })

	cmd := exec.Command(truePath)
	msg := runAndWaitCmd(cmd, "claude", config.Model{ID: "claude/sonnet"}, nil)()
	if _, ok := msg.(launchDoneMsg); !ok {
		t.Fatalf("msg = %T, want launchDoneMsg", msg)
	}
	if pendingSurveyState.agent != "claude" || pendingSurveyState.m.ID != "claude/sonnet" {
		t.Fatalf("pendingSurveyState = %+v, want agent=claude model=claude/sonnet", pendingSurveyState)
	}
}

// TestRunAndWaitCmdAppliesProfileApplier is the regression lock for the
// code-review finding that runAndWaitCmd (the interactive picker's launch
// path) never applied local-model launch profiles at all. It stubs
// profileApplier to record the (cmd, agent, m) it was called with and
// returns a cleanup that records whether it ran; both must fire around
// cmd.Run(), exactly mirroring the non-TUI path's runAgentCmd.
func TestRunAndWaitCmdAppliesProfileApplier(t *testing.T) {
	oldApplier := profileApplier
	t.Cleanup(func() { profileApplier = oldApplier })

	var gotAgent string
	var gotModel config.Model
	cleanupCalled := false
	profileApplier = func(cmd *exec.Cmd, agent string, m config.Model) (func() error, error) {
		gotAgent, gotModel = agent, m
		cmd.Env = append(cmd.Env, "WT_TEST_PROFILE_APPLIED=1")
		return func() error { cleanupCalled = true; return nil }, nil
	}

	cmd := exec.Command("true")
	m := config.Model{ID: "ollama/x", ModelName: "x"}
	msg := runAndWaitCmd(cmd, "claude", m, nil)()

	if done, ok := msg.(launchDoneMsg); !ok || done.err != nil {
		t.Fatalf("runAndWaitCmd() = %#v, want a successful launchDoneMsg", msg)
	}
	if gotAgent != "claude" || gotModel.ID != "ollama/x" {
		t.Errorf("profileApplier called with (%q, %+v), want (claude, {ID: ollama/x})", gotAgent, gotModel)
	}
	if !slices.Contains(cmd.Env, "WT_TEST_PROFILE_APPLIED=1") {
		t.Error("profileApplier's cmd.Env mutation did not survive into cmd.Run()")
	}
	if !cleanupCalled {
		t.Error("profileApplier's cleanup was not called after cmd.Run()")
	}
}

// TestRunAndWaitCmdProfileApplierErrorSkipsRun is the regression lock for
// the TUI-side equivalent of the non-TUI runAgentCmd fix (plan item #5): a
// pre-launch profileApplier error must skip cmd.Run() entirely, still
// populate pendingSummary (duration 0),
// and come back as a launchDoneMsg carrying the error — not print it — so
// Run returns it once and main reports it as `wt: <error>` with exit 1
// (#412). It must NOT populate pendingSurveyState, since nothing ran and
// there is no session to survey or offer a stop picker for.
func TestRunAndWaitCmdProfileApplierErrorSkipsRun(t *testing.T) {
	oldApplier := profileApplier
	t.Cleanup(func() { profileApplier = oldApplier })
	pendingSummary, pendingSurveyState = "", pendingSurvey{}
	t.Cleanup(func() { pendingSummary, pendingSurveyState = "", pendingSurvey{} })

	profileApplier = func(cmd *exec.Cmd, agent string, m config.Model) (func() error, error) {
		return nil, errors.New("wrapper not installed")
	}

	sentinelDir := t.TempDir()
	sentinel := filepath.Join(sentinelDir, "should-not-exist")
	cmd := exec.Command("touch", sentinel)
	m := config.Model{ID: "ollama/x", ModelName: "x"}

	msg := runAndWaitCmd(cmd, "pi", m, nil)()

	done, ok := msg.(launchDoneMsg)
	if !ok || done.err == nil {
		t.Fatalf("runAndWaitCmd() = %#v, want a launchDoneMsg carrying the profileApplier error", msg)
	}
	if _, statErr := os.Stat(sentinel); !os.IsNotExist(statErr) {
		t.Error("sentinel file exists — cmd.Run() executed despite the profileApplier error")
	}
	if pendingSummary == "" {
		t.Error("pendingSummary was not set on a profileApplier error")
	}
	if pendingSurveyState.agent != "" {
		t.Error("pendingSurveyState was populated despite the profileApplier error — nothing ran, there is no session to survey")
	}
}

// TestRunAndWaitCmdNilProfileApplierIsNoop verifies a nil profileApplier
// (its zero value — the state every other test in this file runs under,
// since only Run() ever sets it) leaves runAndWaitCmd's behavior exactly
// as it was before this task, so the profile layer is opt-in via Run's new
// parameter and never a behavior change for a caller that doesn't wire it.
func TestRunAndWaitCmdNilProfileApplierIsNoop(t *testing.T) {
	oldApplier := profileApplier
	profileApplier = nil
	t.Cleanup(func() { profileApplier = oldApplier })

	cmd := exec.Command("true")
	msg := runAndWaitCmd(cmd, "shell", config.Model{}, nil)()
	if done, ok := msg.(launchDoneMsg); !ok || done.err != nil {
		t.Fatalf("runAndWaitCmd() = %#v, want a successful launchDoneMsg", msg)
	}
}

// relativeArgTUIFixture mirrors the report behind the relative-argument note:
// the command is typed in <repo>/wt, the agent starts in <repo>, and
// "../../other" names a sibling project only from the first. It installs a
// fake `claude`, points the shell-directory seam at <repo>/wt, and clears the
// pending notes.
func relativeArgTUIFixture(t *testing.T) (launch, other string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	other = filepath.Join(root, "other")
	launch = filepath.Join(root, "repo")
	shell := filepath.Join(launch, "wt")
	for _, d := range []string{other, shell} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	oldDir, oldNotes := shellDir, pendingRouteNotes
	shellDir = func() (string, error) { return shell, nil }
	pendingRouteNotes = ""
	t.Cleanup(func() { shellDir, pendingRouteNotes = oldDir, oldNotes })
	return launch, other
}

// TestLaunchAgentQueuesTheRelativePathNote pins the picker's half of the
// relative-argument note. The picker owns the terminal while it builds the
// launch, so a note printed then would be lost under the alt screen; it is
// queued with the other pending notes and printed when the terminal is
// released, above the agent's own output. The argument is passed on
// unchanged.
func TestLaunchAgentQueuesTheRelativePathNote(t *testing.T) {
	launch, other := relativeArgTUIFixture(t)
	cmd, err := buildNativeLaunch(launch, []string{"../../other", "--verbose"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(cmd.Args, "../../other") {
		t.Errorf("args = %v, want the relative argument passed through unchanged", cmd.Args)
	}
	for _, want := range []string{`wt: note: "../../other" is a relative path`, "claude starts in " + launch, other} {
		if !strings.Contains(pendingRouteNotes, want) {
			t.Errorf("pending notes %q lack %q", pendingRouteNotes, want)
		}
	}
	if n := strings.Count(pendingRouteNotes, "wt: note:"); n != 1 {
		t.Errorf("pending notes = %q, want exactly one", pendingRouteNotes)
	}
	// A launch that failed and is retried builds the command again; the note
	// must not pile up.
	if _, err := buildNativeLaunch(launch, []string{"../../other", "--verbose"}); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(pendingRouteNotes, "wt: note:"); n != 1 {
		t.Errorf("after a second build pending notes = %q, want still one", pendingRouteNotes)
	}
	var printed bytes.Buffer
	flushRouteNotes(&printed)
	if !strings.Contains(printed.String(), other) || pendingRouteNotes != "" {
		t.Errorf("flushed %q, left %q; want the note printed once and the queue empty", printed.String(), pendingRouteNotes)
	}
}

// TestLaunchAgentQueuesNothingWithoutAMistakenPath pins that an ordinary
// picker launch leaves the queue empty.
func TestLaunchAgentQueuesNothingWithoutAMistakenPath(t *testing.T) {
	launch, _ := relativeArgTUIFixture(t)
	for _, args := range [][]string{nil, {"run", "say hi"}, {"--verbose"}} {
		if _, err := buildNativeLaunch(launch, args); err != nil {
			t.Fatal(err)
		}
	}
	if pendingRouteNotes != "" {
		t.Fatalf("pending notes = %q, want none", pendingRouteNotes)
	}
}

// TestPickerLaunchesStraightThroughAPriorSession pins that the picker no
// longer stops to ask about an earlier session (#198, #204). It used to look
// one up for the worktree and show a "Resume previous session?" prompt; wt
// now leaves sessions to the agent, so Enter on a model launches it — here
// with a prior claude session on disk for the directory, which is what used
// to raise the prompt — and the launch carries no resume flag.
func TestPickerLaunchesStraightThroughAPriorSession(t *testing.T) {
	stubFakeBinary(t, "claude")
	tempStateDir(t)
	stubUsageStore(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	launch, home := filepath.Join(root, "repo"), filepath.Join(root, "home")
	t.Setenv("HOME", home)
	sd, ok := agents.ByName("claude").(agents.StateDirer)
	if !ok {
		t.Fatal("the claude driver no longer reports a state directory")
	}
	projects := sd.StateDir(launch)
	for _, d := range []string{launch, projects} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(projects, "0000aaaa-prior-session.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	next, cmd := enterRemote(t, nativePicker(t, launch), "claude/native")

	if cmd == nil {
		t.Fatalf("no launch command: phase = %v status = %q", next.phase, next.status)
	}
	if next.phase != phaseLaunching || strings.Contains(next.status, "launch failed") {
		t.Fatalf("phase = %v status = %q, want the launch to go straight ahead", next.phase, next.status)
	}
	if view := next.View(); strings.Contains(view, "Resume") {
		t.Fatalf("the picker still shows a resume prompt:\n%s", view)
	}
	built, err := buildNativeLaunch(launch, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range built.Args {
		if a == "--resume" || a == "--continue" || strings.Contains(a, "prior-session") {
			t.Fatalf("launch args %v carry a resume flag wt added", built.Args)
		}
	}
}

// readClaimFields parses one claim file's key=value lines — the cross-repo
// contract format (docs/contracts/session-claims.md). A missing file gives
// nil: the agent saw no claim.
func readClaimFields(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			fields[k] = v
		}
	}
	return fields
}

// TestPickerLaunchClaimsPickedWorktree checks the picker's launch claims the
// worktree it picked: the claim names the picked worktree's own top level in
// git's text, its branch and the agent, live exactly while the agent runs.
// Why: every launch path claims after the worktree exists (Task 405) — the
// picker is one, and a session in a picked linked worktree was invisible to
// the "in use by another session" warning while claims were root-only.
func TestPickerLaunchClaimsPickedWorktree(t *testing.T) {
	keepPendingPostExit(t)
	t.Cleanup(releaseRootClaim)
	profileApplier = nil
	root := rootRepo(t)
	picked := linkedWorktree(t, root)

	// A claude that copies the claim its repository holds for this process
	// while it runs; the claim file is released before the launch returns.
	claimCopy := filepath.Join(t.TempDir(), "claim")
	binDir := t.TempDir()
	script := "#!/bin/sh\n" +
		"d=$(git rev-parse --path-format=absolute --git-common-dir 2>/dev/null) && cp \"$d/wt-sessions/" + strconv.Itoa(os.Getpid()) + "\" '" + claimCopy + "' 2>/dev/null\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	tempStateDir(t)
	stubUsageStore(t)
	answerOnStdio(t, "")

	next, prep := enterRemote(t, nativePicker(t, picked), "claude/native")
	if prep == nil {
		t.Fatalf("no launch command: phase = %v status = %q", next.phase, next.status)
	}
	next, cmd := updateMsg(next, prep())
	if cmd == nil || strings.Contains(next.status, "launch failed") {
		t.Fatalf("no run command: phase = %v status = %q", next.phase, next.status)
	}
	done, ok := cmd().(launchDoneMsg)
	if !ok || done.err != nil {
		t.Fatalf("msg = %#v; the picked-worktree launch did not run clean", done)
	}
	claim := readClaimFields(t, claimCopy)
	if claim == nil {
		t.Fatal("no claim was written for the picked worktree")
	}
	if claim["path"] != picked {
		t.Errorf("claim path = %q; want the picked worktree %q", claim["path"], picked)
	}
	if claim["branch"] != "feature" {
		t.Errorf("claim branch = %q; want %q", claim["branch"], "feature")
	}
	if claim["agent"] != "claude" {
		t.Errorf("claim agent = %q; want %q", claim["agent"], "claude")
	}
	if claimExists(root) {
		t.Error("the claim outlived the agent")
	}
}

// nativeModel is the native claude model the launch tests run.
var nativeModel = config.Model{ID: "claude/native", ProviderID: "claude", ModelName: "native", Native: true}

// buildNativeLaunch builds the native claude launch in dir with args the way
// the picker does (the launch build, then the queued pre-launch notes).
func buildNativeLaunch(dir string, args []string) (*exec.Cmd, error) {
	cmd, _, err := buildGatewayLaunch("claude", nativeModel, dir, false, agents.Gateway{}, nil, args)
	if err == nil {
		queueLaunchNotes("claude", cmd, args)
	}
	return cmd, err
}

// nativePicker is the model picker over a model server whose catalog holds
// the native claude model alone, with dir as the chosen worktree.
func nativePicker(t *testing.T, dir string) model {
	t.Helper()
	f := &fakeModels{cat: modelapi.Catalog{Schema: 1, Generated: "2026-11-16T12:00:00Z", Rows: []modelapi.Row{
		wireRow("claude/native", "claude", func(r *modelapi.Row) { r.Native, r.ModelName, r.Order = true, "native", 1 }),
	}}}
	m := remoteTable(t, f)
	m.selectedPath = dir
	return m
}
