package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/smoke"
)

// smokeRemoteRun is one `wt smoke` run against the fake model server: stdout,
// the command's stderr, the error, and every smokeExit code it recorded.
type smokeRemoteRun struct {
	stdout, stderr string
	err            error
	exits          []int
	stopPicker     int
}

// smokeOnFake runs `wt smoke args...` on rig's app with smokeExit and the
// stop picker stubbed (a real exit would end the test binary; the picker must
// never open on a client session).
func smokeOnFake(t *testing.T, r *remoteRig, args ...string) smokeRemoteRun {
	t.Helper()
	var run smokeRemoteRun
	oldExit, oldPick := smokeExit, runStopPicker
	t.Cleanup(func() { smokeExit, runStopPicker = oldExit, oldPick })
	smokeExit = func(code int) { run.exits = append(run.exits, code) }
	runStopPicker = func(*config.Config) { run.stopPicker++ }
	run.stdout, run.stderr, run.err = runSmokeCmd(r.a, args...)
	return run
}

// stubGatewayRows replaces the agent run with fn's outcome for every row and
// records each call as "ROW" in the fake's request log (so its order against
// the requests can be read).
func stubGatewayRows(t *testing.T, r *remoteRig, fn func(gw agents.Gateway, agentName string) smoke.ExecOutcome) {
	t.Helper()
	t.Cleanup(smoke.SetGatewayRunForTest(func(gw agents.Gateway, _ []config.Model, agentName string, _ config.Model, _ string, _ string, _ time.Duration, _ smoke.ProfileApplier, cleanup *func() error) smoke.ExecOutcome {
		r.f.mu.Lock()
		r.f.Reqs = append(r.f.Reqs, "ROW")
		r.f.mu.Unlock()
		*cleanup = func() error { return nil }
		return fn(gw, agentName)
	}))
}

func passRows(agents.Gateway, string) smoke.ExecOutcome { return smoke.StubOutcome("ok", 0) }

// TestSmokeRemoteRunsThroughTheGateway proves a client smoke row is built by
// the real gateway builder: the agent process sees the model server's origin
// and token and never a provider origin, and the registry is never read. The
// fake claude dumps its environment and arguments, which is all the agent
// would ever know.
func TestSmokeRemoteRunsThroughTheGateway(t *testing.T) {
	r := newRemoteRig(t, wireRow("openrouter/a", modelapi.ActionLaunch, 1))
	failOnRegistry(t)
	dump := t.TempDir() + "/env.txt"
	bin := t.TempDir()
	script := "#!/bin/sh\n{ env; echo \"ARGS: $*\"; } > " + dump + "\nexit 0\n"
	if err := os.WriteFile(bin+"/claude", []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	run := smokeOnFake(t, r, "openrouter/a", "--only", "claude", "--prompt", "hi")
	if run.err != nil || len(run.exits) != 0 {
		t.Fatalf("err = %v exits = %v\nstderr: %s", run.err, run.exits, run.stderr)
	}
	b, err := os.ReadFile(dump)
	if err != nil {
		t.Fatalf("the agent never ran: %v\nstdout: %s\nstderr: %s", err, run.stdout, run.stderr)
	}
	got := string(b)
	if !strings.Contains(got, r.f.URL) || !strings.Contains(got, r.f.Token) {
		t.Errorf("the agent did not get the endpoint origin %s and token:\n%s", r.f.URL, got)
	}
	for _, provider := range []string{"openrouter.ai", "api.anthropic.com"} {
		if strings.Contains(got, provider) {
			t.Errorf("the agent saw a provider origin %q:\n%s", provider, got)
		}
	}
	if !strings.Contains(run.stdout, "[PASS ] claude") {
		t.Errorf("stdout = %q", run.stdout)
	}
}

// TestSmokeRemoteStartsLocalAndEndsTheSession pins the run's order on a
// client session: register the model, then the start job, then the rows (each
// after a gateway readiness check), then the session ends (DELETE). A local
// model is registered before it is started so the server's idle scan counts
// it from the first moment.
func TestSmokeRemoteStartsLocalAndEndsTheSession(t *testing.T) {
	r := newRemoteRig(t, localRow("omlx/q", modelapi.StateStopped, modelapi.ActionStart, nil))
	stubGatewayRows(t, r, passRows)
	run := smokeOnFake(t, r, "omlx/q", "--only", "claude")
	if run.err != nil {
		t.Fatalf("err = %v\n%s", run.err, run.stderr)
	}
	reqs := r.f.reqs()
	reg, start, poll := index(reqs, "POST /control/v1/sessions"), index(reqs, "POST /control/v1/start"), index(reqs, "GET /control/v1/jobs/job-0")
	ready, row, del := index(reqs, "GET /v1/models"), index(reqs, "ROW"), index(reqs, "DELETE /control/v1/sessions/")
	order := []int{reg, start, poll, ready, row, del}
	for i, v := range order {
		if v < 0 || (i > 0 && v <= order[i-1]) {
			t.Fatalf("order register,start,poll,ready,row,delete = %v, want increasing\n%s", order, strings.Join(reqs, "\n"))
		}
	}
	if len(r.f.Registers) != 1 || len(r.f.Registers[0]) != 1 || r.f.Registers[0][0] != "omlx/q" {
		t.Errorf("registers = %v, want one session with omlx/q", r.f.Registers)
	}
	if n := r.f.count("DELETE /control/v1/sessions/"); n != 1 {
		t.Errorf("session deletes = %d, want 1", n)
	}
	for _, q := range reqs {
		if strings.Contains(q, "omlx/q") && !strings.Contains(q, "?model=") && strings.Contains(q, "/control/v1/") {
			t.Errorf("a model id sits in a URL path: %s", q)
		}
	}
}

// TestSmokeRemoteNeverStartsAnUnknownRoute proves R7 for smoke: a running
// local model whose route the server could not read is tested as it is, with
// no start job, and the session still ends.
func TestSmokeRemoteNeverStartsAnUnknownRoute(t *testing.T) {
	r := newRemoteRig(t, localRow("omlx/q", modelapi.StateRunning, modelapi.ActionLaunch, nil))
	stubGatewayRows(t, r, passRows)
	run := smokeOnFake(t, r, "omlx/q", "--only", "claude")
	if run.err != nil {
		t.Fatalf("err = %v", run.err)
	}
	if n := r.f.count("POST /control/v1/start"); n != 0 {
		t.Errorf("start posts = %d, want none (an unknown route never starts)", n)
	}
	if n := r.f.count("DELETE /control/v1/sessions/"); n != 1 {
		t.Errorf("session deletes = %d, want 1", n)
	}
}

// TestSmokeRemoteNoStopPrompt pins the exit flow of a client run: the stop
// picker is never offered, even on a terminal; a model smoke started gets the
// one note saying the model server stops it after its idle grace; a model it
// did not start, and a --json run, get no note.
func TestSmokeRemoteNoStopPrompt(t *testing.T) {
	const note = "wt: note: smoke started omlx/q; the model server stops it after its idle grace unless a client session uses it\n"
	t.Run("started on a terminal", func(t *testing.T) {
		r := newRemoteRig(t, localRow("omlx/q", modelapi.StateStopped, modelapi.ActionStart, nil))
		stubGatewayRows(t, r, passRows)
		stubStdinTTY(t, true)
		run := smokeOnFake(t, r, "omlx/q", "--only", "claude")
		if run.err != nil || run.stopPicker != 0 {
			t.Fatalf("err = %v stop pickers = %d", run.err, run.stopPicker)
		}
		if got := run.stderr + r.stderr.String(); strings.Count(got, note) != 1 {
			t.Errorf("stderr = %q, want the note once", got)
		}
	})
	t.Run("json prints no note", func(t *testing.T) {
		r := newRemoteRig(t, localRow("omlx/q", modelapi.StateStopped, modelapi.ActionStart, nil))
		stubGatewayRows(t, r, passRows)
		stubStdinTTY(t, true)
		run := smokeOnFake(t, r, "omlx/q", "--only", "claude", "--json")
		if run.stopPicker != 0 || strings.Contains(run.stderr+r.stderr.String(), "smoke started") {
			t.Errorf("pickers = %d stderr = %q", run.stopPicker, run.stderr+r.stderr.String())
		}
	})
	t.Run("nothing started prints no note", func(t *testing.T) {
		r := newRemoteRig(t, wireRow("openrouter/a", modelapi.ActionLaunch, 1))
		stubGatewayRows(t, r, passRows)
		stubStdinTTY(t, true)
		run := smokeOnFake(t, r, "openrouter/a", "--only", "claude")
		if run.stopPicker != 0 || strings.Contains(run.stderr+r.stderr.String(), "smoke started") {
			t.Errorf("pickers = %d stderr = %q", run.stopPicker, run.stderr+r.stderr.String())
		}
	})
}

// TestSmokeRemoteJSONShapeUnchanged pins the --json contract on a client run:
// the same top-level keys, the same row keys and the same PASS/SKIP words as
// the registry path, with the 503 reason as the SKIP row's error.
func TestSmokeRemoteJSONShapeUnchanged(t *testing.T) {
	r := newRemoteRig(t, wireRow("openrouter/a", modelapi.ActionLaunch, 1))
	stubGatewayRows(t, r, passRows)
	run := smokeOnFake(t, r, "openrouter/a", "--only", "claude", "--prompt", "hi", "--json")
	if run.err != nil {
		t.Fatalf("err = %v", run.err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(run.stdout), &doc); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, run.stdout)
	}
	keys := func(m map[string]json.RawMessage) string {
		var ks []string
		for k := range m {
			ks = append(ks, k)
		}
		sortStrings(ks)
		return strings.Join(ks, ",")
	}
	if got := keys(doc); got != "model,rows,run_id" {
		t.Errorf("top-level keys = %s", got)
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(doc["rows"], &rows); err != nil || len(rows) != 1 {
		t.Fatalf("rows = %s (%v)", doc["rows"], err)
	}
	if got := keys(rows[0]); got != "agent,command,duration_ms,exit_code,model,output,status" {
		t.Errorf("row keys = %s", got)
	}
	if string(rows[0]["status"]) != `"PASS"` {
		t.Errorf("status = %s", rows[0]["status"])
	}

	// A gateway that stays down is a SKIP row carrying the reason.
	r2 := newRemoteRig(t, wireRow("openrouter/a", modelapi.ActionLaunch, 1))
	r2.f.GatewayStatus = 503
	stubGatewayRows(t, r2, passRows)
	run2 := smokeOnFake(t, r2, "openrouter/a", "--only", "claude", "--json")
	var rep smokeJSONReport
	if err := json.Unmarshal([]byte(run2.stdout), &rep); err != nil || len(rep.Rows) != 1 {
		t.Fatalf("report = %s (%v)", run2.stdout, err)
	}
	if rep.Rows[0].Status != "SKIP" || rep.Rows[0].Error != "gateway unavailable (503) — the model server may be restarting LiteLLM" {
		t.Errorf("row = %+v", rep.Rows[0])
	}
}

// TestSmokeRemoteUnknownVersusUntestable pins the two refusals of a pinned
// model on a client run: an id the server does not know says so with the
// server's own text; a model the server knows but that cannot be tested (a
// blocked row, or one no agent can use) says it cannot be smoke-tested, with
// the block reason when there is one. Both end the run with exit 1 and start
// and register nothing.
func TestSmokeRemoteUnknownVersusUntestable(t *testing.T) {
	blocked := wireRow("openrouter/blocked", modelapi.ActionBlock, 2)
	blocked.BlockReason = "openrouter/blocked: no route on the model server"
	nobody := wireRow("zzz/nobody", modelapi.ActionLaunch, 3)
	nobody.ProviderID, nobody.Native = "zzz", true // a native row of an agent that does not exist
	r := newRemoteRig(t, wireRow("openrouter/a", modelapi.ActionLaunch, 1), blocked, nobody)
	stubGatewayRows(t, r, passRows)

	check := func(id, want, notWant string) {
		t.Helper()
		run := smokeOnFake(t, r, id, "--only", "claude")
		if run.err == nil || !strings.Contains(run.err.Error(), want) || strings.Contains(run.err.Error(), notWant) {
			t.Fatalf("%s: err = %v, want %q and not %q", id, run.err, want, notWant)
		}
		if exitcode.Of(run.err) != 1 {
			t.Errorf("%s: exit = %d, want 1", id, exitcode.Of(run.err))
		}
	}
	check("openrouter/nope", `unknown model "openrouter/nope"`, "cannot be smoke-tested")
	check("openrouter/blocked", "no route on the model server", "unknown model")
	check("zzz/nobody", `model "zzz/nobody" cannot be smoke-tested right now`, "unknown model")
	if n := r.f.count("POST"); n != 0 {
		t.Errorf("POSTs = %d, want none\n%s", n, strings.Join(r.f.reqs(), "\n"))
	}
}

// TestSmokeFailStillExits1 pins C9's named exception on a client run: a FAIL
// row ends the run with exit 1 after the report (and the session is ended
// first), while a SKIP row (a gateway that stays down) leaves the exit at 0.
func TestSmokeFailStillExits1(t *testing.T) {
	t.Run("fail", func(t *testing.T) {
		r := newRemoteRig(t, wireRow("openrouter/a", modelapi.ActionLaunch, 1))
		stubGatewayRows(t, r, func(agents.Gateway, string) smoke.ExecOutcome { return smoke.StubOutcome("boom", 1) })
		run := smokeOnFake(t, r, "openrouter/a", "--only", "claude")
		if run.err != nil || len(run.exits) != 1 || run.exits[0] != 1 {
			t.Fatalf("err = %v exits = %v, want exit 1", run.err, run.exits)
		}
		reqs := r.f.reqs()
		if index(reqs, "DELETE /control/v1/sessions/") < 0 {
			t.Errorf("the session was not ended before the exit\n%s", strings.Join(reqs, "\n"))
		}
		if !strings.Contains(run.stdout, "FAIL: 1") {
			t.Errorf("stdout = %q", run.stdout)
		}
	})
	t.Run("skip because the gateway is down", func(t *testing.T) {
		r := newRemoteRig(t, wireRow("openrouter/a", modelapi.ActionLaunch, 1))
		r.f.GatewayStatus = 503
		stubGatewayRows(t, r, passRows)
		run := smokeOnFake(t, r, "openrouter/a", "--only", "claude")
		if run.err != nil || len(run.exits) != 0 {
			t.Fatalf("err = %v exits = %v, want exit 0", run.err, run.exits)
		}
		if !strings.Contains(run.stdout, "[SKIP ] claude") || !strings.Contains(run.stdout, "SKIP: 1") {
			t.Errorf("stdout = %q", run.stdout)
		}
	})
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// TestSmokeRemoteNoteForAnAlreadyRunningModel proves the auto-stop warning is
// not only for a model smoke started: smoke registers a running local model
// too, so the server stops it after its idle grace, and on a terminal one note
// says that (a cloud model is never stopped, so it gets none).
func TestSmokeRemoteNoteForAnAlreadyRunningModel(t *testing.T) {
	const note = "wt: note: smoke used omlx/q, which was already running; the model server stops it after its idle grace unless a client session uses it\n"
	r := newRemoteRig(t, localRow("omlx/q", modelapi.StateRunning, modelapi.ActionLaunch, boolp(true)))
	stubGatewayRows(t, r, passRows)
	stubStdinTTY(t, true)
	run := smokeOnFake(t, r, "omlx/q", "--only", "claude", "--prompt", "hi")
	if run.err != nil {
		t.Fatalf("err = %v", run.err)
	}
	if got := run.stderr + r.stderr.String(); strings.Count(got, note) != 1 || strings.Contains(got, "smoke started") {
		t.Errorf("stderr = %q, want the already-running note once", got)
	}
	// Not on a terminal: no note.
	r2 := newRemoteRig(t, localRow("omlx/q", modelapi.StateRunning, modelapi.ActionLaunch, boolp(true)))
	stubGatewayRows(t, r2, passRows)
	run2 := smokeOnFake(t, r2, "omlx/q", "--only", "claude")
	if strings.Contains(run2.stderr+r2.stderr.String(), "smoke used") {
		t.Errorf("a non-terminal run printed the note: %q", run2.stderr)
	}
}

// TestSmokeRemoteGatewayDownSkipsTheRestAtOnce pins that a gateway that stays
// down stalls the run once, not once per row: the first row waits its full
// readiness wait and is SKIP; the other rows are SKIP with the same reason and
// never call the gateway again (the fake sees the same number of model-list
// polls as for a single-row run).
func TestSmokeRemoteGatewayDownSkipsTheRestAtOnce(t *testing.T) {
	polls := func(only string) (int, smokeRemoteRun) {
		r := newRemoteRig(t, wireRow("openrouter/a", modelapi.ActionLaunch, 1))
		r.f.GatewayStatus = 503
		stubGatewayRows(t, r, passRows)
		run := smokeOnFake(t, r, "openrouter/a", "--only", only)
		return r.f.count("GET /v1/models"), run
	}
	one, _ := polls("claude")
	three, run := polls("claude,codex,pi")
	if one == 0 || three != one {
		t.Errorf("gateway polls: one row = %d, three rows = %d, want equal (and not 0)", one, three)
	}
	if !strings.Contains(run.stdout, "SKIP: 3") || run.err != nil || len(run.exits) != 0 {
		t.Errorf("stdout = %q err = %v exits = %v, want 3 SKIP, exit 0", run.stdout, run.err, run.exits)
	}
	if n := strings.Count(run.stderr, "SKIP ("); n != 3 {
		t.Errorf("SKIP progress lines = %d, want 3\n%s", n, run.stderr)
	}
}

// TestSmokeRemoteServerGoneStopsTheRun pins that a model server that vanishes
// mid-run stops the remaining rows instead of failing each: the row that ran is
// reported, the next row's readiness check finds the server unreachable, and
// the run ends with exit 1 naming the cause once, with no FAIL row for it.
func TestSmokeRemoteServerGoneStopsTheRun(t *testing.T) {
	r := newRemoteRig(t, wireRow("openrouter/a", modelapi.ActionLaunch, 1))
	calls := 0
	stubGatewayRows(t, r, func(agents.Gateway, string) smoke.ExecOutcome {
		calls++
		r.f.Close() // the server is gone before the next row
		return smoke.StubOutcome("ok", 0)
	})
	run := smokeOnFake(t, r, "openrouter/a", "--only", "claude,codex,pi", "--prompt", "hi")
	if run.err == nil || exitcode.Of(run.err) != 1 {
		t.Fatalf("err = %v, want exit 1", run.err)
	}
	if calls != 1 {
		t.Errorf("agent runs = %d, want 1 (the rest never ran)", calls)
	}
	if !strings.Contains(run.stdout, "[PASS ] claude") || strings.Contains(run.stdout, "FAIL: 1") || strings.Contains(run.stdout, "Usage:") {
		t.Errorf("stdout = %q, want the row that ran and no FAIL", run.stdout)
	}
	if n := strings.Count(run.err.Error(), "unreachable") + strings.Count(run.err.Error(), "127.0.0.1"); n == 0 {
		t.Errorf("err = %v does not name the cause", run.err)
	}
}
