package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tui"
	"github.com/spf13/cobra"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
	"github.com/ohanaverse/local-ai-setup/wt/internal/ollamacheck"
)

// remoteAgentScript is the fake claude a remote launch runs: it sleeps
// WT_FAKE_SLEEP seconds when set, touches WT_FAKE_MARK when set, and exits
// WT_FAKE_EXIT (0 by default).
const remoteAgentScript = "#!/bin/sh\n" +
	"[ -n \"$WT_FAKE_SLEEP\" ] && sleep \"$WT_FAKE_SLEEP\"\n" +
	"[ -n \"$WT_FAKE_MARK\" ] && touch \"$WT_FAKE_MARK\"\n" +
	"exit ${WT_FAKE_EXIT:-0}\n"

// remoteRig is one remote launch's harness: the fake model server, the app
// pointed at it, the commands the agent builder made and wt's stderr.
type remoteRig struct {
	f      *fakeModelServer
	a      *app
	built  *[]*exec.Cmd
	stderr *lockedBuffer
}

// newRemoteRig points a fresh app at a fake model server serving rows, puts
// the fake claude first on PATH, records every agent build in the fake's
// request log as "BUILD" (so its order against the requests can be read),
// makes job polls immediate and captures stderr.
func newRemoteRig(t *testing.T, rows ...modelapi.Row) *remoteRig {
	t.Helper()
	f := newFakeModelServer(t, rows...)
	a := pointAtFake(t, f, "")
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(remoteAgentScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	stubStdinTTY(t, false)

	built := &[]*exec.Cmd{}
	oldBuild := buildGatewayCmd
	buildGatewayCmd = func(agent string, m config.Model, wt string, yolo bool, gw agents.Gateway, models []config.Model, extra []string) (*exec.Cmd, agents.LaunchInfo, error) {
		f.mu.Lock()
		f.Reqs = append(f.Reqs, "BUILD")
		f.mu.Unlock()
		cmd, info, err := oldBuild(agent, m, wt, yolo, gw, models, extra)
		if cmd != nil {
			*built = append(*built, cmd)
		}
		return cmd, info, err
	}
	oldClient := newModelClient
	newModelClient = fastModelClient
	t.Cleanup(func() { buildGatewayCmd, newModelClient = oldBuild, oldClient })
	return &remoteRig{f: f, a: a, built: built, stderr: captureStderr(t)}
}

// launch runs launchFilteredRemote for claude in a scratch directory, with
// stdout (the summary) captured out of the test's output.
func (r *remoteRig) launch(t *testing.T, ctx context.Context, pinned, others string, replace bool) error {
	t.Helper()
	var err error
	captureFile(t, &os.Stdout, func() {
		err = launchFilteredRemote(ctx, r.a, "claude", t.TempDir(), false, "", "", pinned, pinned != "", nil, others, replace, nil)
	})
	return err
}

// index is the position of the first request starting with prefix, -1 if none.
func index(reqs []string, prefix string) int {
	return slices.IndexFunc(reqs, func(s string) bool { return strings.HasPrefix(s, prefix) })
}

// localRow is a registered local omlx row in the given state and action.
func localRow(id, state, action string, routed *bool) modelapi.Row {
	return modelapi.Row{
		ID: id, GatewayID: id, ProviderID: "omlx", ModelName: strings.TrimPrefix(id, "omlx/"), Family: "f",
		Location: "local", Tags: []string{"code"}, Registered: true, State: state, Action: action, Order: 1, Routed: routed,
	}
}

func boolp(b bool) *bool { return &b }

// fastModelClient is loopbackOnlyModelClient with no sleep between job polls,
// so a start job's polls do not slow the test down.
func fastModelClient(ep modelclient.Endpoint) *modelclient.Client {
	d := &net.Dialer{}
	tr := &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(addr)
		if err != nil || (host != "localhost" && !net.ParseIP(host).IsLoopback()) {
			return nil, fmt.Errorf("cmd/wt tests dial loopback only, not %q", addr)
		}
		return d.DialContext(ctx, network, addr)
	}}
	hc := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return modelclient.New(ep, modelclient.WithHTTPClient(hc), modelclient.WithSleep(func(time.Duration) {}))
}

// failOnRegistry makes every registry and local-provider seam fail the test
// if it is reached: the registry file is garbage, the local inventory probe,
// the ollama check, the route check and the refcount sweep all report.
func failOnRegistry(t *testing.T) {
	t.Helper()
	reg := filepath.Join(t.TempDir(), "registry.toml")
	if err := os.WriteFile(reg, []byte("this is [not toml\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WT_REGISTRY", reg)
	oldProbe, oldRoute, oldSweep := probeInventory, ensureModelRoute, sweepRefcounts
	probeInventory = func(*config.Config) localmodels.Snapshot {
		t.Error("the remote path probed a local provider")
		return localmodels.Snapshot{}
	}
	ensureModelRoute = func(*config.Config, config.Model) bool {
		t.Error("the remote path ran the launch-time route check")
		return false
	}
	sweepRefcounts = func() { t.Error("the remote path swept the refcount file") }
	t.Cleanup(ollamacheck.StubListForTest(func(string) ([]string, bool, error) {
		t.Error("the remote path ran the ollama check")
		return nil, true, nil
	}))
	t.Cleanup(func() { probeInventory, ensureModelRoute, sweepRefcounts = oldProbe, oldRoute, oldSweep })
}

// TestLaunchRemoteRegistersBeforeStart pins the body order of a remote launch
// of a model that must be started: the client session is registered first,
// then the start is posted and polled, and only then is the agent built. A
// model started before its client session exists could be auto-stopped by the
// server's idle scan before the agent ever reaches it.
func TestLaunchRemoteRegistersBeforeStart(t *testing.T) {
	r := newRemoteRig(t, localRow("omlx/q", modelapi.StateStopped, modelapi.ActionStart, nil))
	if err := r.launch(t, context.Background(), "omlx/q", "", false); err != nil {
		t.Fatalf("launch: %v", err)
	}
	reqs := r.f.reqs()
	reg, start, poll, build := index(reqs, "POST /control/v1/sessions"), index(reqs, "POST /control/v1/start"), index(reqs, "GET /control/v1/jobs/job-0"), index(reqs, "BUILD")
	if reg < 0 || start < 0 || poll < 0 || build < 0 || !(reg < start && start < poll && poll < build) {
		t.Errorf("order register=%d start=%d poll=%d build=%d, want increasing\n%s", reg, start, poll, build, strings.Join(reqs, "\n"))
	}
	if got := r.f.Registers; len(got) == 0 || !slices.Equal(got[0], []string{"omlx/q"}) {
		t.Errorf("registers = %v, want the model first", got)
	}
}

// TestLaunchRemoteDoesNotStartARoutedRunningModel proves a running local model
// the gateway lists launches without a start job: only a pin of an idle model
// or an explicit unrouted row may start anything.
func TestLaunchRemoteDoesNotStartARoutedRunningModel(t *testing.T) {
	r := newRemoteRig(t, localRow("omlx/q", modelapi.StateRunning, modelapi.ActionLaunch, boolp(true)))
	if err := r.launch(t, context.Background(), "", "", false); err != nil {
		t.Fatalf("launch: %v", err)
	}
	if n := r.f.count("POST /control/v1/start"); n != 0 {
		t.Errorf("starts posted = %d, want 0", n)
	}
	if len(*r.built) != 1 {
		t.Errorf("agents built = %d, want 1", len(*r.built))
	}
}

// TestLaunchRemoteStartsARunningModelKnownToBeUnrouted proves R7's one start
// of a running model: routed is explicitly false, so the start job (which adds
// the route) runs before the agent is built.
func TestLaunchRemoteStartsARunningModelKnownToBeUnrouted(t *testing.T) {
	r := newRemoteRig(t, localRow("omlx/q", modelapi.StateRunning, modelapi.ActionLaunch, boolp(false)))
	if err := r.launch(t, context.Background(), "", "", false); err != nil {
		t.Fatalf("launch: %v", err)
	}
	reqs := r.f.reqs()
	if n := r.f.count("POST /control/v1/start"); n != 1 || index(reqs, "POST /control/v1/start") > index(reqs, "BUILD") {
		t.Errorf("starts = %d, want one before the build\n%s", n, strings.Join(reqs, "\n"))
	}
}

// TestLaunchRemoteDoesNotStartWhenRoutingIsUnknown proves a LiteLLM hiccup
// must not start a running model: with routed absent (the server could not ask
// LiteLLM) no start is posted and the agent launches (R7).
func TestLaunchRemoteDoesNotStartWhenRoutingIsUnknown(t *testing.T) {
	r := newRemoteRig(t, localRow("omlx/q", modelapi.StateRunning, modelapi.ActionLaunch, nil))
	if err := r.launch(t, context.Background(), "", "", false); err != nil {
		t.Fatalf("launch: %v", err)
	}
	if n := r.f.count("POST /control/v1/start"); n != 0 {
		t.Errorf("starts posted = %d, want 0", n)
	}
	if len(*r.built) != 1 {
		t.Errorf("agents built = %d, want 1", len(*r.built))
	}
}

// TestLaunchRemoteBlockedPinShowsServerReason proves a pin of a blocked row
// fails with the server's own reason, exit 1, before any client session is
// registered or any agent built.
func TestLaunchRemoteBlockedPinShowsServerReason(t *testing.T) {
	row := localRow("omlx/q", modelapi.StateBlocked, modelapi.ActionBlock, nil)
	row.BlockReason = "omlx is not installed on the model server"
	r := newRemoteRig(t, row)
	err := r.launch(t, context.Background(), "omlx/q", "", false)
	if err == nil || !strings.Contains(err.Error(), row.BlockReason) || !strings.HasSuffix(err.Error(), "nothing was launched") {
		t.Fatalf("err = %v, want the block reason and the still clause", err)
	}
	if exitcode.Of(err) != 1 {
		t.Errorf("exit = %d, want 1", exitcode.Of(err))
	}
	if len(r.f.Registers) != 0 || len(*r.built) != 0 {
		t.Errorf("registers=%v built=%d, want neither", r.f.Registers, len(*r.built))
	}
}

// TestLaunchRemoteNativeRowLaunchesBare is Review Focus 4: a native row (the
// claude subscription) never receives the gateway origin or token, in its
// environment or its arguments.
func TestLaunchRemoteNativeRowLaunchesBare(t *testing.T) {
	native := modelapi.Row{ID: "claude/native", ProviderID: "claude", ModelName: "native", Native: true,
		Location: "cloud", Registered: true, State: modelapi.StateRunning, Action: modelapi.ActionLaunch, Tags: []string{"code"}}
	r := newRemoteRig(t, native)
	if err := r.launch(t, context.Background(), "claude/native", "", false); err != nil {
		t.Fatalf("launch: %v", err)
	}
	if len(*r.built) != 1 {
		t.Fatalf("agents built = %d, want 1", len(*r.built))
	}
	cmd := (*r.built)[0]
	for _, s := range append(slices.Clone(cmd.Args), cmd.Env...) {
		if strings.Contains(s, r.f.URL) || s == "ANTHROPIC_AUTH_TOKEN="+r.f.Token {
			t.Errorf("native launch carries the gateway: %q", s)
		}
	}
}

// TestLaunchRemoteNeedsNoRegistry proves the remote path reads no
// registry.toml (a garbage one is in place), probes no local provider and runs
// no ollama or route check: the model server is its only source.
func TestLaunchRemoteNeedsNoRegistry(t *testing.T) {
	r := newRemoteRig(t, wireRow("openrouter/a", modelapi.ActionLaunch, 1))
	failOnRegistry(t)
	r.a.cfg = nil
	if err := r.launch(t, context.Background(), "", "", false); err != nil {
		t.Fatalf("launch: %v", err)
	}
	if len(*r.built) != 1 {
		t.Errorf("agents built = %d, want 1", len(*r.built))
	}
}

// TestLaunchRemoteEndsTheSessionBeforeExit proves the client session's DELETE
// goes out before wt exits with the agent's own status (7): runAgentCmd's
// os.Exit would otherwise skip it. The launch runs in a child process, whose
// fake server logs each request to a file the parent reads.
func TestLaunchRemoteEndsTheSessionBeforeExit(t *testing.T) {
	if log := os.Getenv("WT_TEST_REMOTE_EXIT_LOG"); log != "" {
		r := newRemoteRig(t, wireRow("openrouter/a", modelapi.ActionLaunch, 1))
		r.f.Log = log
		t.Setenv("WT_FAKE_EXIT", "7")
		_ = r.launch(t, context.Background(), "", "", false)
		os.Exit(0)
	}
	log := filepath.Join(t.TempDir(), "reqs")
	cmd := exec.Command(os.Args[0], "-test.run=^TestLaunchRemoteEndsTheSessionBeforeExit$")
	cmd.Env = append(os.Environ(), "WT_TEST_REMOTE_EXIT_LOG="+log)
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 7 {
		t.Fatalf("child err = %v, want exit status 7\n%s", err, out)
	}
	b, _ := os.ReadFile(log)
	if !strings.Contains(string(b), "DELETE /control/v1/sessions/") {
		t.Errorf("no DELETE before the exit; requests:\n%s", b)
	}
}

// TestLaunchRemoteNeverOffersTheStopPickerOrRefcount proves a launch through
// runLaunchPath with an endpoint configured reaches none of the registry
// path's tail or set-up: the stop picker, the refcount release and sweep, the
// registry's price notice, the ollama check and the route check all panic if
// reached, and the agent still launches.
func TestLaunchRemoteNeverOffersTheStopPickerOrRefcount(t *testing.T) {
	r := newRemoteRig(t, localRow("omlx/q", modelapi.StateRunning, modelapi.ActionLaunch, boolp(true)))
	oldPicker, oldRelease, oldSweep, oldNotice, oldRoute, oldTUI := runStopPicker, releaseSession, sweepRefcounts, emitPriceNotice, ensureModelRoute, tuiRun
	t.Cleanup(func() {
		runStopPicker, releaseSession, sweepRefcounts, emitPriceNotice, ensureModelRoute, tuiRun = oldPicker, oldRelease, oldSweep, oldNotice, oldRoute, oldTUI
	})
	runStopPicker = func(*config.Config) { panic("runStopPicker reached on the remote path") }
	releaseSession = func() { panic("releaseSession reached on the remote path") }
	sweepRefcounts = func() { panic("sweepRefcounts reached on the remote path") }
	emitPriceNotice = func(*config.Config) { panic("emitPriceNotice reached on the remote path") }
	ensureModelRoute = func(*config.Config, config.Model) bool { panic("ensureModelRoute reached on the remote path") }
	t.Cleanup(ollamacheck.StubListForTest(func(string) ([]string, bool, error) { panic("ollamacheck reached on the remote path") }))
	tuiRun = nil // a nil func panics if called

	cmd := &cobra.Command{}
	cmd.Flags().String("model", "", "")
	cmd.Flags().Bool("yolo", false, "")
	var err error
	captureFile(t, &os.Stdout, func() {
		err = runLaunchPath(cmd, r.a, "claude", "", "", "", nil, t.TempDir(), "")
	})
	if err != nil {
		t.Fatalf("runLaunchPath: %v", err)
	}
	if len(*r.built) != 1 {
		t.Errorf("agents built = %d, want 1", len(*r.built))
	}
}

// TestLaunchRemoteHeartbeatFailureDoesNotKillTheAgent is Review Focus 1: a
// heartbeat the server fails while the agent runs is one `wt: note:` line and
// nothing else: the agent runs to its end and the launch succeeds.
func TestLaunchRemoteHeartbeatFailureDoesNotKillTheAgent(t *testing.T) {
	r := newRemoteRig(t, wireRow("openrouter/a", modelapi.ActionLaunch, 1))
	r.f.BeatStatus = 500
	old := remoteBeatInterval
	remoteBeatInterval = 20 * time.Millisecond
	t.Cleanup(func() { remoteBeatInterval = old })
	mark := filepath.Join(t.TempDir(), "done")
	t.Setenv("WT_FAKE_SLEEP", "0.4")
	t.Setenv("WT_FAKE_MARK", mark)
	if err := r.launch(t, context.Background(), "", "", false); err != nil {
		t.Fatalf("launch: %v", err)
	}
	if !fileExists(mark) {
		t.Error("the agent did not run to its end")
	}
	errOut := r.stderr.String()
	if n := strings.Count(errOut, "wt: note: model server heartbeat failed"); n != 1 {
		t.Errorf("heartbeat notes = %d, want 1:\n%s", n, errOut)
	}
}

// TestLaunchRemoteRegisterFailureIsANoteAndLaunches proves a client session
// the server would not register is advisory: one `wt: note:` line, the agent
// still launches, and the launch's outcome is unchanged.
func TestLaunchRemoteRegisterFailureIsANoteAndLaunches(t *testing.T) {
	r := newRemoteRig(t, wireRow("openrouter/a", modelapi.ActionLaunch, 1))
	r.f.RegisterStatus = 500
	if err := r.launch(t, context.Background(), "", "", false); err != nil {
		t.Fatalf("launch: %v", err)
	}
	if len(*r.built) != 1 {
		t.Errorf("agents built = %d, want 1", len(*r.built))
	}
	errOut := r.stderr.String()
	if n := strings.Count(errOut, "wt: note:"); n != 1 || !strings.Contains(errOut, "client session") {
		t.Errorf("stderr has %d notes, want one naming the client session:\n%s", n, errOut)
	}
}

// TestLaunchRemoteCtrlCDuringStartExits2EndsSessionAndLaunchesNothing is
// Review Focus 2 on the CLI: a Ctrl-C (the context cancelled mid-poll)
// detaches from the job, the client session's DELETE still goes out on a
// context the cancel does not reach, the error is the detached text with
// "nothing was launched", exit 2, and no agent is built.
func TestLaunchRemoteCtrlCDuringStartExits2EndsSessionAndLaunchesNothing(t *testing.T) {
	r := newRemoteRig(t, localRow("omlx/q", modelapi.StateStopped, modelapi.ActionStart, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.f.Hang = true
	r.f.OnPoll = cancel
	err := r.launch(t, ctx, "omlx/q", "", false)
	if err == nil || !strings.Contains(err.Error(), "detached — job job-0 keeps running") || !strings.HasSuffix(err.Error(), "nothing was launched") {
		t.Fatalf("err = %v, want the detached text and the still clause", err)
	}
	if exitcode.Of(err) != 2 {
		t.Errorf("exit = %d, want 2", exitcode.Of(err))
	}
	if r.f.count("DELETE /control/v1/sessions/") != 1 {
		t.Errorf("no DELETE after the detach:\n%s", strings.Join(r.f.reqs(), "\n"))
	}
	if len(*r.built) != 0 {
		t.Errorf("agents built = %d, want none", len(*r.built))
	}
}

// TestLaunchRemoteWrongTokenExits1NamingTokenFile is Review Focus 5 at
// launch: a refused token fails at the first call, exit 1, naming the
// token_file key, with no client session registered and nothing launched.
func TestLaunchRemoteWrongTokenExits1NamingTokenFile(t *testing.T) {
	r := newRemoteRig(t, wireRow("openrouter/a", modelapi.ActionLaunch, 1))
	r.f.Token = "rotated"
	err := r.launch(t, context.Background(), "", "", false)
	if err == nil || !strings.Contains(err.Error(), "token_file under [model-server]") {
		t.Fatalf("err = %v, want the token_file way out", err)
	}
	if exitcode.Of(err) != 1 {
		t.Errorf("exit = %d, want 1", exitcode.Of(err))
	}
	if len(r.f.Registers) != 0 || len(*r.built) != 0 {
		t.Errorf("registers=%v built=%d, want neither", r.f.Registers, len(*r.built))
	}
}

// TestStartFailureReregistersWithoutTheModel proves a start job that failed
// takes its model out of the client session (a re-register without it) before
// the client session ends, exit 2 with the server's message and "nothing was
// launched", and nothing is built.
func TestStartFailureReregistersWithoutTheModel(t *testing.T) {
	r := newRemoteRig(t, localRow("omlx/q", modelapi.StateStopped, modelapi.ActionStart, nil))
	r.f.Outcome = func(int, modelapi.StartRequest) *modelapi.Problem {
		return &modelapi.Problem{Code: modelapi.CodeFailed, Message: "omlx/q did not load: out of memory"}
	}
	err := r.launch(t, context.Background(), "omlx/q", "", false)
	if err == nil || !strings.Contains(err.Error(), "out of memory") || !strings.HasSuffix(err.Error(), "nothing was launched") {
		t.Fatalf("err = %v, want the server's message and the still clause", err)
	}
	if exitcode.Of(err) != 2 {
		t.Errorf("exit = %d, want 2", exitcode.Of(err))
	}
	if got := r.f.Registers; len(got) != 2 || !slices.Equal(got[0], []string{"omlx/q"}) || len(got[1]) != 0 {
		t.Errorf("registers = %v, want [[omlx/q] []]", got)
	}
	reqs := r.f.reqs()
	lastReg := -1
	for i, s := range reqs {
		if s == "POST /control/v1/sessions" {
			lastReg = i
		}
	}
	if d := index(reqs, "DELETE /control/v1/sessions/"); d < 0 || d < lastReg {
		t.Errorf("no DELETE after the re-register:\n%s", strings.Join(reqs, "\n"))
	}
	if len(*r.built) != 0 {
		t.Errorf("agents built = %d, want none", len(*r.built))
	}
}

// TestUnreachableServerFailsFastWithAddress proves a model server that does
// not answer fails the launch at once, exit 1, naming the address tried, with
// nothing launched and no fall back to the registry or a local provider.
func TestUnreachableServerFailsFastWithAddress(t *testing.T) {
	r := newRemoteRig(t, wireRow("openrouter/a", modelapi.ActionLaunch, 1))
	failOnRegistry(t)
	addr := strings.TrimPrefix(r.f.URL, "http://")
	r.f.Close()
	began := time.Now()
	err := r.launch(t, context.Background(), "", "", false)
	if err == nil || !strings.Contains(err.Error(), addr) {
		t.Fatalf("err = %v, want the address %s", err, addr)
	}
	if exitcode.Of(err) != 1 {
		t.Errorf("exit = %d, want 1", exitcode.Of(err))
	}
	if d := time.Since(began); d > 5*time.Second {
		t.Errorf("took %s, want a fast failure", d)
	}
	if len(*r.built) != 0 {
		t.Errorf("agents built = %d, want none", len(*r.built))
	}
}

// TestPickerGetsTheModelServerOnlyWhenOneIsConfigured proves the one tui.Run
// caller hands the picker its model-server client exactly when an endpoint is
// configured (and the session config with it), and nil otherwise, so the
// legacy registry path is unchanged. The picker's fetch itself runs inside the
// TUI; nothing here reaches the fake server.
func TestPickerGetsTheModelServerOnlyWhenOneIsConfigured(t *testing.T) {
	r := newRemoteRig(t)
	oldTUI, oldGuard := tuiRun, maybeInstallGuard
	t.Cleanup(func() { tuiRun, maybeInstallGuard = oldTUI, oldGuard })
	var gotModels tui.ModelClient
	var called bool
	tuiRun = func(_, _ bool, _ string, _, _, _, _ string, _ []string, _ themes.Theme, _ string, models tui.ModelClient, _ *config.Config, _ tui.ProfileApplier, _ tui.SlotProfiles, _ tui.RemoveFuncsBuilder) error {
		called, gotModels = true, models
		return nil
	}
	cmd := &cobra.Command{}
	cmd.Flags().String("model", "", "")
	cmd.Flags().Bool("yolo", false, "")
	if err := runLaunchPath(cmd, r.a, "", "", "", "", nil, "", ""); err != nil {
		t.Fatalf("runLaunchPath: %v", err)
	}
	if !called || gotModels == nil {
		t.Fatalf("tuiRun called = %v, models nil = %v, want the model-server client", called, gotModels == nil)
	}
	if len(r.f.Reqs) != 0 {
		t.Errorf("the launch path asked the server %v before the picker opened", r.f.Reqs)
	}

	// No endpoint: nil.
	for _, k := range []string{"WT_MODEL_URL", "WT_MODEL_TOKEN_FILE", "WT_MODEL_TOKEN"} {
		t.Setenv(k, "")
	}
	a := &app{cfg: &config.Config{}}
	a.sessCfgSet, a.sessCfg = true, &config.Config{}
	called, gotModels = false, nil
	if err := runLaunchPath(cmd, a, "", "", "", "", nil, "", ""); err != nil {
		t.Fatalf("legacy runLaunchPath: %v", err)
	}
	if !called || gotModels != nil {
		t.Errorf("legacy: called = %v, models = %v, want a call with nil", called, gotModels)
	}
}

// TestSeveralMatchesOpenThePickerOnTheServer pins the endpoint path's -A X
// launch with no -M and several launchable models: on a terminal it opens the
// model server's picker (whose cursor lands on the rotation's next row), as
// the registry path opens its picker, instead of launching a model the user
// never saw; without a terminal it refuses as the registry path does.
func TestSeveralMatchesOpenThePickerOnTheServer(t *testing.T) {
	cloud := func(id string, order int) modelapi.Row {
		return modelapi.Row{ID: id, GatewayID: id, ProviderID: "openrouter", ModelName: strings.TrimPrefix(id, "openrouter/"), Family: "f",
			Location: "cloud", Tags: []string{"code"}, Registered: true, State: modelapi.StateRunning, Action: modelapi.ActionLaunch, Order: order}
	}
	r := newRemoteRig(t, cloud("openrouter/a", 1), cloud("openrouter/b", 2))
	stubStdinTTY(t, true)
	oldTUI := tuiRun
	t.Cleanup(func() { tuiRun = oldTUI })
	var gotModels tui.ModelClient
	var gotAgent string
	called := false
	tuiRun = func(_, _ bool, _ string, agent, _, _, _ string, _ []string, _ themes.Theme, _ string, models tui.ModelClient, _ *config.Config, _ tui.ProfileApplier, _ tui.SlotProfiles, _ tui.RemoveFuncsBuilder) error {
		called, gotModels, gotAgent = true, models, agent
		return nil
	}
	cmd := &cobra.Command{}
	cmd.Flags().String("model", "", "")
	cmd.Flags().Bool("yolo", false, "")
	if err := runLaunchPath(cmd, r.a, "claude", "", "", "", nil, t.TempDir(), ""); err != nil {
		t.Fatalf("runLaunchPath: %v", err)
	}
	if !called || gotModels == nil || gotAgent != "claude" {
		t.Fatalf("tuiRun called %v, models nil %v, agent %q: want the server's picker for claude", called, gotModels == nil, gotAgent)
	}
	if len(*r.built) != 0 {
		t.Errorf("an agent was built before the picker: %d", len(*r.built))
	}

	// Without a terminal the registry path's rule (P5): refuse, naming -M,
	// and launch nothing; never a silent rotation.
	stubStdinTTY(t, false)
	called = false
	err := runLaunchPath(cmd, r.a, "claude", "", "", "", nil, t.TempDir(), "")
	if !errors.Is(err, errModelPickerNeedsTTY) {
		t.Errorf("no terminal: err = %v, want %v", err, errModelPickerNeedsTTY)
	}
	if called || len(*r.built) != 0 || index(r.f.Reqs, "POST /control/v1/sessions") >= 0 {
		t.Errorf("no terminal: tuiRun called %v, builds %d, requests %v: want nothing registered or launched", called, len(*r.built), r.f.Reqs)
	}
}
