package main

import (
	"context"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)

// remoteAgentScript is the fake claude a remote launch runs. As the login
// check's claude (#597) it answers `--version` with the file WT_FAKE_VERSION
// names and `auth status --json` with the file WT_FAKE_AUTH names (exit
// WT_FAKE_AUTH_EXIT, 0 by default). As the agent it dumps its environment
// and its argv (one per line) to the files WT_FAKE_ENV_DUMP and
// WT_FAKE_ARGV_DUMP name when set, sleeps WT_FAKE_SLEEP seconds when set,
// touches WT_FAKE_MARK when set, and exits WT_FAKE_EXIT (0 by default).
const remoteAgentScript = "#!/bin/sh\n" +
	"case \"$1\" in\n" +
	"--version) [ -n \"$WT_FAKE_VERSION\" ] && cat \"$WT_FAKE_VERSION\"; exit 0 ;;\n" +
	"auth) [ -n \"$WT_FAKE_AUTH\" ] && cat \"$WT_FAKE_AUTH\"; exit ${WT_FAKE_AUTH_EXIT:-0} ;;\n" +
	"esac\n" +
	"[ -n \"$WT_FAKE_ENV_DUMP\" ] && env > \"$WT_FAKE_ENV_DUMP\"\n" +
	"[ -n \"$WT_FAKE_ARGV_DUMP\" ] && printf '%s\\n' \"$@\" > \"$WT_FAKE_ARGV_DUMP\"\n" +
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
	a.hooks.buildGateway = func(agent string, m config.Model, wt string, yolo bool, gw agents.Gateway, models []config.Model, extra []string) (*exec.Cmd, agents.LaunchInfo, error) {
		f.mu.Lock()
		f.Reqs = append(f.Reqs, "BUILD")
		f.mu.Unlock()
		cmd, info, err := agents.BuildLaunchCmdGateway(agent, m, wt, yolo, gw, models, extra)
		if cmd != nil {
			*built = append(*built, cmd)
		}
		return cmd, info, err
	}
	a.hooks.newClient = fastModelClient
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
	return loopbackOnlyClient(ep, modelclient.WithSleep(func(time.Duration) {}))
}

// failOnRegistry makes the registry fail the test if it is read: the
// registry file is garbage. (This binary has no local inventory probe left
// to stub.)
func failOnRegistry(t *testing.T) {
	t.Helper()
	reg := filepath.Join(t.TempDir(), "registry.toml")
	if err := os.WriteFile(reg, []byte("this is [not toml\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WT_REGISTRY", reg)
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
	r.a.hostCfg, r.a.hostSet = nil, true
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

// TestLaunchRemoteReachesNoHostSeam proves a launch through runLaunchPath
// with an endpoint configured reaches none of the host's set-up (the stop
// picker, the refcount, the registry's price notice, the ollama check and the
// route check of the old registry path are gone, the boundary test keeps them
// out, and this binary has no local inventory probe left to reach): the TUI
// panics if opened, and the agent still launches.
func TestLaunchRemoteReachesNoHostSeam(t *testing.T) {
	r := newRemoteRig(t, localRow("omlx/q", modelapi.StateRunning, modelapi.ActionLaunch, boolp(true)))
	oldTUI := tuiRun
	t.Cleanup(func() { tuiRun = oldTUI })
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
	r.a.hooks.beatInterval = 20 * time.Millisecond
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
	row := wireRow("openrouter/a", modelapi.ActionLaunch, 1)
	row.Context = 131072 // a known window: no window note beside the one under test
	r := newRemoteRig(t, row)
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
	if strings.Contains(errOut, "may stop") {
		t.Errorf("a cloud model is never auto-stopped, the note must not say so:\n%s", errOut)
	}
}

// TestLaunchRemoteRegisterFailureNoteNamesAutoStopForALocalModel proves the
// register-failure note warns of the server's idle stop only for a local row,
// the only kind the auto-stopper touches.
func TestLaunchRemoteRegisterFailureNoteNamesAutoStopForALocalModel(t *testing.T) {
	r := newRemoteRig(t, localRow("omlx/q", modelapi.StateRunning, modelapi.ActionLaunch, boolp(true)))
	r.f.RegisterStatus = 500
	if err := r.launch(t, context.Background(), "", "", false); err != nil {
		t.Fatalf("launch: %v", err)
	}
	if got := r.stderr.String(); !strings.Contains(got, "the server may stop omlx/q once it is idle") {
		t.Errorf("stderr lacks the auto-stop risk for a local model:\n%s", got)
	}
}

// TestRegisterRefusedIsThePermanentKinds pins which register errors mean the
// server will refuse the later DELETE too (so End's failure is not news): the
// refused token, the API skew and the wrong server, but not a transient one.
func TestRegisterRefusedIsThePermanentKinds(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{&modelclient.UnauthorizedError{Addr: "a"}, true},
		{fmt.Errorf("wrapped: %w", &modelclient.SkewError{Addr: "a"}), true},
		{&modelclient.NotModelServerError{Addr: "a"}, true},
		{errors.New("register failed"), false},
	} {
		if got := registerRefused(tc.err); got != tc.want {
			t.Errorf("registerRefused(%v) = %v, want %v", tc.err, got, tc.want)
		}
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

// TestLaunchRemoteCtrlCBeforeTheJobExistsExits2 pins the Ctrl-C that lands in
// the start's first call (the active-jobs read), before any job has an id:
// the same exit 2 and detached wording as a Ctrl-C mid-poll, not a bare
// `context canceled` exit 1. The session still ends and nothing is built.
func TestLaunchRemoteCtrlCBeforeTheJobExistsExits2(t *testing.T) {
	r := newRemoteRig(t, localRow("omlx/q", modelapi.StateStopped, modelapi.ActionStart, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.f.OnJobs = func(rc context.Context) {
		cancel()
		<-rc.Done() // answer only once the client has given up on the call
	}
	err := r.launch(t, ctx, "omlx/q", "", false)
	if err == nil || !strings.Contains(err.Error(), "detached") || !strings.HasSuffix(err.Error(), "nothing was launched") {
		t.Fatalf("err = %v, want the detached text and the still clause", err)
	}
	if strings.Contains(err.Error(), "context canceled") {
		t.Errorf("err = %v, must not be the bare cancel", err)
	}
	if exitcode.Of(err) != 2 {
		t.Errorf("exit = %d, want 2", exitcode.Of(err))
	}
	if r.f.count("DELETE /control/v1/sessions/") != 1 {
		t.Errorf("no DELETE after the interrupt:\n%s", strings.Join(r.f.reqs(), "\n"))
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
// caller hands the picker its model-server client when an endpoint is
// configured (and the session config with it, never the host's), and, since
// the flip, that with none configured the picker refuses (an unset mode,
// exit 1, nothing was launched), with native mode chosen it opens natively
// (a nil client; #615) unless the launch asks for a model (-T), which
// refuses before the TUI opens (exit 1, nothing was launched), while a pinned
// command agent's worktree picker still opens, with no client too. The picker's fetch
// itself runs inside the TUI; nothing here reaches the fake server.
func TestPickerGetsTheModelServerOnlyWhenOneIsConfigured(t *testing.T) {
	r := newRemoteRig(t)
	oldTUI, oldGuard := tuiRun, maybeInstallGuard
	t.Cleanup(func() { tuiRun, maybeInstallGuard = oldTUI, oldGuard })
	var gotModels tui.ModelClient
	var gotCfg *config.Config
	var called bool
	tuiRun = func(_, _ bool, _ string, _, _, _, _ string, _ []string, _ themes.Theme, _ string, models tui.ModelClient, cfg *config.Config, _ tui.ProfileLayer, _ tui.SlotProfiles, _ tui.RemoveFuncsBuilder, _ []string) error {
		called, gotModels, gotCfg = true, models, cfg
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
	if sess, serr := r.a.sessionCfg(); serr != nil || gotCfg == nil || gotCfg != sess || (r.a.hostSet && gotCfg == r.a.hostCfg) {
		t.Errorf("tuiRun cfg = %p, want the session config %p (not the host's; err %v)", gotCfg, sess, serr)
	}
	if len(r.f.Reqs) != 0 {
		t.Errorf("the launch path asked the server %v before the picker opened", r.f.Reqs)
	}

	// No endpoint and no mode: the picker refuses before it opens.
	for _, k := range []string{"WT_MODEL_URL", "WT_MODEL_TOKEN_FILE", "WT_MODEL_TOKEN"} {
		t.Setenv(k, "")
	}
	unset := &app{}
	unset.sessCfgSet, unset.sessCfg = true, &config.Config{}
	called = false
	if err := runLaunchPath(cmd, unset, "", "", "", "", nil, "", ""); err == nil || err.Error() != unsetText(launchStill) || exitcode.Of(err) != 1 || called {
		t.Errorf("no mode: err = %v (exit %d), called = %v; want %q, exit 1, no TUI", err, exitcode.Of(err), called, unsetText(launchStill))
	}

	// Native mode: the picker opens natively (#615), with no client,
	// unless the launch asks for a model, which refuses before the
	// picker opens.
	t.Setenv("WT_MODEL_MODE", "native")
	a := &app{}
	a.sessCfgSet, a.sessCfg = true, &config.Config{}
	called, gotModels = false, nil
	if err := runLaunchPath(cmd, a, "", "", "", "", nil, "", ""); err != nil || !called || gotModels != nil {
		t.Errorf("native: err = %v, called = %v, models = %v; want a native picker with no client", err, called, gotModels)
	}
	called = false
	err := runLaunchPath(cmd, a, "", "", "code", "", nil, "", "")
	if want := nativeRefusalText(launchStill); err == nil || err.Error() != want || exitcode.Of(err) != 1 {
		t.Errorf("native, -T: err = %v (exit %d), want %q, exit 1", err, exitcode.Of(err), want)
	}
	if called {
		t.Error("native, -T: the TUI opened")
	}

	// No endpoint, a pinned command: its worktree picker needs no model.
	called, gotModels = false, nil
	if err := runLaunchPath(cmd, a, "shell", "", "", "", nil, "", ""); err != nil {
		t.Fatalf("shell runLaunchPath: %v", err)
	}
	if !called || gotModels != nil {
		t.Errorf("shell: called = %v, models = %v, want a call with a nil client", called, gotModels)
	}
}

// TestSeveralMatchesOpenThePickerOnTheServer pins the endpoint path's -A X
// launch with no -M and several launchable models: on a terminal it opens the
// model server's picker (whose cursor, with no previous launch, lands on the
// first unblocked row), as the registry path opens its picker, instead of
// launching a model the user never saw; without a terminal it refuses as the
// registry path does.
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
	tuiRun = func(_, _ bool, _ string, agent, _, _, _ string, _ []string, _ themes.Theme, _ string, models tui.ModelClient, _ *config.Config, _ tui.ProfileLayer, _ tui.SlotProfiles, _ tui.RemoveFuncsBuilder, _ []string) error {
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
	// and launch nothing; never a silent pick.
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

// tlsSubRig is subRig (opted in, passthrough on, a good login stub) with the
// fake model server on https: an httptest TLS server whose certificate only
// its own file vouches for. With withCA, config.toml's [model-server] names
// that file as ca_file. It returns the rig, the login call count and the
// certificate's path.
func tlsSubRig(t *testing.T, withCA bool) (*remoteRig, *int, string) {
	t.Helper()
	st := goodLogin()
	r, calls := subRig(t, true, true, &st, subRow("claude-haiku-5-5"))
	r.f.Server.Close()
	r.f.Server = httptest.NewUnstartedServer(http.HandlerFunc(r.f.serve))
	r.f.StartTLS()
	t.Cleanup(r.f.Close)
	ca := filepath.Join(t.TempDir(), "server.crt")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: r.f.Certificate().Raw}), 0o644); err != nil {
		t.Fatal(err)
	}
	extra := "subscription = true\n"
	if withCA {
		extra += fmt.Sprintf("ca_file = %q\n", ca)
	}
	hooks := r.a.hooks
	r.a = pointAtFake(t, r.f, extra)
	r.a.hooks = hooks
	t.Setenv("NODE_EXTRA_CA_CERTS", "")
	os.Unsetenv("NODE_EXTRA_CA_CERTS")
	t.Setenv("WT_NODE_EXTRA_CA_CERTS", "")
	os.Unsetenv("WT_NODE_EXTRA_CA_CERTS")
	return r, calls, ca
}

// TestSubscriptionGuardUntrustedCertRefusesBeforeRegistration proves an
// opted-in header-mode launch to an https server this machine does not
// trust (no ca_file) stops at the handshake with decision 14's ca_file hint,
// exit 1: the fake saw no request at all (the token was never written), so
// no client session, no start and no login check.
func TestSubscriptionGuardUntrustedCertRefusesBeforeRegistration(t *testing.T) {
	r, calls, _ := tlsSubRig(t, false)
	err := r.launch(t, context.Background(), "claude-haiku-5-5", "", false)
	if err == nil || exitCodeOf(err) > 1 || !strings.Contains(err.Error(), "set ca_file under [model-server]") {
		t.Fatalf("err = %v, want the ca_file hint", err)
	}
	if *calls != 0 {
		t.Errorf("login ran %d times", *calls)
	}
	if reqs := r.f.reqs(); len(reqs) != 0 {
		t.Errorf("the untrusted server saw requests: %q", reqs)
	}
}

// TestSubscriptionLaunchOverHTTPSWithCAFile proves the same launch with
// ca_file set goes through (the test client, loopbackOnlyClient, honours the
// endpoint's CA file like the real one) and hands the agent the CA: the
// child's environment has NODE_EXTRA_CA_CERTS exactly once, equal to the
// absolute ca_file.
func TestSubscriptionLaunchOverHTTPSWithCAFile(t *testing.T) {
	r, calls, ca := tlsSubRig(t, true)
	envDump := filepath.Join(t.TempDir(), "env")
	t.Setenv("WT_FAKE_ENV_DUMP", envDump)
	if err := r.launch(t, context.Background(), "claude-haiku-5-5", "", false); err != nil {
		t.Fatalf("launch: %v", err)
	}
	if *calls != 1 {
		t.Errorf("login ran %d times, want 1", *calls)
	}
	env, err := os.ReadFile(envDump)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, line := range strings.Split(string(env), "\n") {
		if v, ok := strings.CutPrefix(line, "NODE_EXTRA_CA_CERTS="); ok {
			got = append(got, v)
		}
	}
	if len(got) != 1 || got[0] != ca || !filepath.IsAbs(got[0]) {
		t.Errorf("NODE_EXTRA_CA_CERTS = %q, want once %s", got, ca)
	}
}

// TestTLSOffNote proves decision 2's tls_off note: a server whose ping says
// its TLS is off prints exactly one "wt: note:" line naming the reason and
// plain http, through openModels on the CLI path (a header-mode launch after
// it adds no second one) and through the guard on the TUI path (asked twice,
// still once); a ping without it prints none.
func TestTLSOffNote(t *testing.T) {
	const note = "wt: note: the model server's TLS is off (certificate expired 2026-11-01); this connection is plain http\n"
	st := goodLogin()
	t.Run("cli", func(t *testing.T) {
		r, _ := subRig(t, true, true, &st, subRow("claude-haiku-5-5"))
		r.f.TLSOff = "certificate expired 2026-11-01"
		if _, err := r.a.openModels(context.Background(), launchStill); err != nil {
			t.Fatal(err)
		}
		if err := r.launch(t, context.Background(), "claude-haiku-5-5", "", false); err != nil {
			t.Fatalf("launch: %v", err)
		}
		if n := strings.Count(r.stderr.String(), note); n != 1 || strings.Count(r.stderr.String(), "TLS is off") != 1 {
			t.Errorf("stderr = %q, want the note once", r.stderr.String())
		}
	})
	t.Run("tui", func(t *testing.T) {
		r, _ := subRig(t, true, true, &st, subRow("claude-haiku-5-5"))
		r.f.TLSOff = "certificate expired 2026-11-01"
		ms, err := r.a.buildModels(launchStill)
		if err != nil {
			t.Fatal(err)
		}
		scfg, _ := r.a.sessionCfg()
		if _, err := tui.NewModelClient(ms.client, ms.ep).Catalog(context.Background()); err != nil {
			t.Fatal(err)
		}
		guard := tuiSubscriptionGuard(r.a, ms, scfg)
		for range 2 {
			if err := guard(context.Background(), []modelapi.Row{subRow("claude-haiku-5-5")}); err != nil {
				t.Fatalf("guard: %v", err)
			}
		}
		if n := strings.Count(r.stderr.String(), note); n != 1 {
			t.Errorf("stderr = %q, want the note once", r.stderr.String())
		}
	})
	t.Run("on", func(t *testing.T) {
		r, _ := subRig(t, true, true, &st, subRow("claude-haiku-5-5"))
		if err := r.launch(t, context.Background(), "claude-haiku-5-5", "", false); err != nil {
			t.Fatalf("launch: %v", err)
		}
		if strings.Contains(r.stderr.String(), "TLS is off") {
			t.Errorf("stderr = %q, want no note", r.stderr.String())
		}
	})
}

// TestSlotLaunchOverHTTPSWithCAFile proves the slot-profile launch passes
// the session's ca_file into its gateway: a claude slot profile launched
// against the https fake with ca_file set reaches the agent with
// NODE_EXTRA_CA_CERTS once, equal to the ca_file.
func TestSlotLaunchOverHTTPSWithCAFile(t *testing.T) {
	r, _, ca := tlsSubRig(t, true)
	r.f.Rows = slotWireRows()
	store := profiles.SlotStore{Profiles: []profiles.SlotProfile{{Name: "mix", Agent: "claude", Default: "sonnet", Slots: map[string]string{"sonnet": "openrouter/a", "haiku": "openrouter/b"}}}}
	r.a.slotChecks = profiles.ValidateSlots(store, agents.SlotDrivers())
	envDump := filepath.Join(t.TempDir(), "env")
	t.Setenv("WT_FAKE_ENV_DUMP", envDump)
	if err := r.launch(t, context.Background(), "profile/mix", "", false); err != nil {
		t.Fatalf("launch: %v", err)
	}
	env, err := os.ReadFile(envDump)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, line := range strings.Split(string(env), "\n") {
		if v, ok := strings.CutPrefix(line, "NODE_EXTRA_CA_CERTS="); ok {
			got = append(got, v)
		}
	}
	if len(got) != 1 || got[0] != ca {
		t.Errorf("NODE_EXTRA_CA_CERTS = %q, want once %s", got, ca)
	}
}

// TestSubscriptionRefusesNodeTLSRejectUnauthorized proves the guard refuses
// a header-mode launch whose environment turns Node's certificate check off
// (NODE_TLS_REJECT_UNAUTHORIZED 0 or false), naming the variable and never
// its value, before any client session or login check; unset or 1 launches.
func TestSubscriptionRefusesNodeTLSRejectUnauthorized(t *testing.T) {
	st := goodLogin()
	for _, v := range []string{"0", "false"} {
		r, calls := subRig(t, true, true, &st, subRow("claude-haiku-5-5"))
		t.Setenv("NODE_TLS_REJECT_UNAUTHORIZED", v)
		err := r.launch(t, context.Background(), "claude-haiku-5-5", "", false)
		if err == nil || !strings.Contains(err.Error(), "NODE_TLS_REJECT_UNAUTHORIZED") || strings.Contains(err.Error(), "NODE_TLS_REJECT_UNAUTHORIZED="+v) {
			t.Fatalf("%s: err = %v, want the refusal naming the variable", v, err)
		}
		if *calls != 0 {
			t.Errorf("%s: login ran %d times", v, *calls)
		}
		nothingRegistered(t, r.f)
	}
	for _, v := range []string{"1", ""} {
		r, _ := subRig(t, true, true, &st, subRow("claude-haiku-5-5"))
		t.Setenv("NODE_TLS_REJECT_UNAUTHORIZED", v)
		if v == "" {
			os.Unsetenv("NODE_TLS_REJECT_UNAUTHORIZED")
		}
		if err := r.launch(t, context.Background(), "claude-haiku-5-5", "", false); err != nil {
			t.Errorf("%q: launch: %v", v, err)
		}
	}
}

// TestLaunchRemoteNotesAnUnknownWindow pins #689: a gateway model whose
// window nobody knows prints one `wt: note:` naming the fix, a known window
// prints none, and the note is neither a launch warning nor a refusal.
func TestLaunchRemoteNotesAnUnknownWindow(t *testing.T) {
	r := newRemoteRig(t, wireRow("openrouter/a", modelapi.ActionLaunch, 1))
	t.Setenv("CLAUDE_CODE_MAX_CONTEXT_TOKENS", "")
	if err := r.launch(t, context.Background(), "", "", false); err != nil {
		t.Fatalf("launch: %v", err)
	}
	if got := r.stderr.String(); !strings.Contains(got, "wt-model model edit openrouter/a --context-window N") || strings.Count(got, "wt: note:") != 1 {
		t.Errorf("stderr = %q, want one window note", got)
	}

	known := wireRow("openrouter/a", modelapi.ActionLaunch, 1)
	known.Context = 131072
	r = newRemoteRig(t, known)
	if err := r.launch(t, context.Background(), "", "", false); err != nil {
		t.Fatalf("launch: %v", err)
	}
	if got := r.stderr.String(); strings.Contains(got, "context window") {
		t.Errorf("stderr = %q, want no window note for a known window", got)
	}

	r = newRemoteRig(t, wireRow("openrouter/a", modelapi.ActionLaunch, 1))
	t.Setenv("CLAUDE_CODE_MAX_CONTEXT_TOKENS", "500000")
	if err := r.launch(t, context.Background(), "", "", false); err != nil {
		t.Fatalf("launch: %v", err)
	}
	if got := r.stderr.String(); strings.Contains(got, "context window") {
		t.Errorf("stderr = %q, want no note when the user's variable is set", got)
	}
}
