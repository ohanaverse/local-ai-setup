package modelsetup

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
)

const (
	llURL = "http://127.0.0.1:4000"
	llKey = "sk-LLKEY-5551234"
)

// llGood returns a fake where every LiteLLM check passes: a reachable proxy
// with a connected database that accepts the key and serves routes a and b,
// a 0600 plist on port 4000, and a config.yaml older than the proxy process.
func llGood(t *testing.T) (*fakeEnv, config.ProxyPair) {
	f := newFakeEnv(t)
	start := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	f.getReplies = map[string]getReply{
		llURL + "/health/liveliness": {status: 200, body: []byte(`"I'm alive!"`)},
		llURL + "/health/readiness":  {status: 200, body: []byte(`{"status":"healthy","db":"connected"}`)},
		llURL + "/v1/models":         {status: 200, body: []byte(`{"data":[{"id":"a"},{"id":"b"}]}`)},
	}
	f.routes = []string{"a", "b"}
	f.jobs[litellm.ProxyLabel] = LaunchdJob{Loaded: true, Running: true, PID: 321}
	f.processStart = start
	f.files[f.configPath] = []byte("x")
	f.modTimes = map[string]time.Time{f.configPath: start.Add(-time.Hour)}
	f.plistInfo = litellm.ProxyPlistInfo{Port: 4000, Mode: 0o600}
	return f, config.ProxyPair{URL: llURL, APIKey: llKey}
}

// TestCheckLiteLLMAllGood pins that a healthy, in-sync proxy reports every
// check OK and the state as checked.
func TestCheckLiteLLMAllGood(t *testing.T) {
	f, pair := llGood(t)
	st := CheckLiteLLM(context.Background(), f.Env(), pair)
	if !st.Checked {
		t.Fatalf("not checked: %+v", st)
	}
	for name, c := range map[string]Check{"Reachable": st.Reachable, "Database": st.Database, "KeyAccepted": st.KeyAccepted,
		"RoutesInSync": st.RoutesInSync, "RestartOwed": st.RestartOwed, "PortMatches": st.PortMatches, "PlistMode": st.PlistMode} {
		if c.Status != CheckOK {
			t.Errorf("%s = %+v, want OK", name, c)
		}
	}
}

// TestKeyRejectedIsReportedAsRejectedNotAsWrongMasterKey pins that a 401
// fails KeyAccepted with the word "rejected": the stored key may be fine and
// the proxy's own key different, so the check never claims which is wrong.
func TestKeyRejectedIsReportedAsRejectedNotAsWrongMasterKey(t *testing.T) {
	f, pair := llGood(t)
	f.getReplies[llURL+"/v1/models"] = getReply{status: 401}
	st := CheckLiteLLM(context.Background(), f.Env(), pair)
	if st.KeyAccepted.Status != CheckFail || !strings.Contains(st.KeyAccepted.Detail, "rejected") {
		t.Errorf("KeyAccepted = %+v", st.KeyAccepted)
	}
	if strings.Contains(strings.ToLower(st.KeyAccepted.Detail), "master key") {
		t.Errorf("detail blames the master key: %q", st.KeyAccepted.Detail)
	}
	if st.RoutesInSync.Status != CheckSkipped {
		t.Errorf("routes were compared without the model list: %+v", st.RoutesInSync)
	}
}

// TestDatabaseDownIsAWarningNotAFailure pins that a proxy whose database is
// not connected is a warning: the proxy still routes, only spend logs are lost.
func TestDatabaseDownIsAWarningNotAFailure(t *testing.T) {
	f, pair := llGood(t)
	f.getReplies[llURL+"/health/readiness"] = getReply{status: 200, body: []byte(`{"db":"Not connected"}`)}
	st := CheckLiteLLM(context.Background(), f.Env(), pair)
	if st.Database.Status != CheckWarn {
		t.Errorf("Database = %+v, want warn", st.Database)
	}
	f.getReplies[llURL+"/health/readiness"] = getReply{status: 503}
	if st := CheckLiteLLM(context.Background(), f.Env(), pair); st.Database.Status != CheckWarn {
		t.Errorf("Database on 503 = %+v, want warn", st.Database)
	}
}

// TestRoutesInSyncDetectsAMissingAndAnExtraRoute pins that the proxy's model
// ids are compared with the desired routes both ways, and that a nil
// DesiredRoutes skips the check.
func TestRoutesInSyncDetectsAMissingAndAnExtraRoute(t *testing.T) {
	f, pair := llGood(t)
	f.routes = []string{"a", "c"} // b is extra on the proxy, c is missing
	st := CheckLiteLLM(context.Background(), f.Env(), pair)
	d := st.RoutesInSync.Detail
	if st.RoutesInSync.Status != CheckWarn || !strings.Contains(d, "missing c") || !strings.Contains(d, "extra b") {
		t.Errorf("RoutesInSync = %+v", st.RoutesInSync)
	}
	env := f.Env()
	env.DesiredRoutes = nil
	if st := CheckLiteLLM(context.Background(), env, pair); st.RoutesInSync.Status != CheckSkipped {
		t.Errorf("nil DesiredRoutes = %+v, want skipped", st.RoutesInSync)
	}
	f.routesErr = errors.New("boom")
	if st := CheckLiteLLM(context.Background(), f.Env(), pair); st.RoutesInSync.Status != CheckSkipped {
		t.Errorf("routes error = %+v, want skipped", st.RoutesInSync)
	}
}

// TestRestartOwedWhenConfigIsNewerThanTheProxy pins that a config.yaml written
// after the proxy process started means the proxy has not read it yet.
func TestRestartOwedWhenConfigIsNewerThanTheProxy(t *testing.T) {
	f, pair := llGood(t)
	f.modTimes[f.configPath] = f.processStart.Add(time.Minute)
	st := CheckLiteLLM(context.Background(), f.Env(), pair)
	if st.RestartOwed.Status != CheckWarn || !strings.Contains(st.RestartOwed.Detail, "restart") {
		t.Errorf("RestartOwed = %+v", st.RestartOwed)
	}
	if !f.called("Launchd.Print "+litellm.ProxyLabel) || !f.called("Net.ProcessStart 321") {
		t.Errorf("calls = %q", f.calls)
	}
	f.jobs[litellm.ProxyLabel] = LaunchdJob{}
	if st := CheckLiteLLM(context.Background(), f.Env(), pair); st.RestartOwed.Status != CheckSkipped {
		t.Errorf("not running = %+v, want skipped", st.RestartOwed)
	}
}

// TestRestartNotOwedWhenConfigAndProxyShareASecond pins that the proxy's start
// time, which ps reports to the whole second, is not compared against the
// config's sub-second mtime: `wt-model litellm sync` writes config.yaml and
// restarts the proxy within one second, and that must not read as owed.
func TestRestartNotOwedWhenConfigAndProxyShareASecond(t *testing.T) {
	f, pair := llGood(t)
	f.processStart = f.processStart.Truncate(time.Second)
	f.modTimes[f.configPath] = f.processStart.Add(400 * time.Millisecond)
	st := CheckLiteLLM(context.Background(), f.Env(), pair)
	if st.RestartOwed.Status != CheckOK {
		t.Errorf("same-second write = %+v, want ok", st.RestartOwed)
	}
	f.modTimes[f.configPath] = f.processStart.Add(time.Second)
	st = CheckLiteLLM(context.Background(), f.Env(), pair)
	if st.RestartOwed.Status != CheckWarn {
		t.Errorf("write a second later = %+v, want warn", st.RestartOwed)
	}
}

// TestPortMismatch pins that the plist's --port is compared with the port of
// the configured proxy URL.
func TestPortMismatch(t *testing.T) {
	f, pair := llGood(t)
	f.plistInfo.Port = 4001
	st := CheckLiteLLM(context.Background(), f.Env(), pair)
	if st.PortMatches.Status != CheckWarn || !strings.Contains(st.PortMatches.Detail, "4001") {
		t.Errorf("PortMatches = %+v", st.PortMatches)
	}
}

// TestLooseModePlistWarns pins that a plist holding the master key must be
// 0600, and anything looser is a warning.
func TestLooseModePlistWarns(t *testing.T) {
	f, pair := llGood(t)
	f.plistInfo.Mode = 0o644
	st := CheckLiteLLM(context.Background(), f.Env(), pair)
	if st.PlistMode.Status != CheckWarn || !strings.Contains(st.PlistMode.Detail, "0644") {
		t.Errorf("PlistMode = %+v", st.PlistMode)
	}
}

// TestTighterModePlistIsOK pins that a 0400 plist is not flagged: only
// group/other bits are a problem, so a tighter-than-0600 plist reads OK and
// the text names its mode (it is never loosened to 0600).
func TestTighterModePlistIsOK(t *testing.T) {
	f, pair := llGood(t)
	f.plistInfo.Mode = 0o400
	st := CheckLiteLLM(context.Background(), f.Env(), pair)
	if st.PlistMode.Status != CheckOK || !strings.Contains(st.PlistMode.Detail, "mode 0400") {
		t.Errorf("PlistMode = %+v", st.PlistMode)
	}
}

// TestUnreachableSkipsTheAuthenticatedChecks pins that a proxy that does not
// answer fails Reachable, skips the checks that need it, and sends no key.
func TestUnreachableSkipsTheAuthenticatedChecks(t *testing.T) {
	f, pair := llGood(t)
	f.getReplies = map[string]getReply{llURL + "/health/liveliness": {err: errors.New("connection refused")}}
	st := CheckLiteLLM(context.Background(), f.Env(), pair)
	if !st.Checked || st.Reachable.Status != CheckFail {
		t.Errorf("Reachable = %+v checked=%t", st.Reachable, st.Checked)
	}
	for name, c := range map[string]Check{"Database": st.Database, "KeyAccepted": st.KeyAccepted, "RoutesInSync": st.RoutesInSync} {
		if c.Status != CheckSkipped {
			t.Errorf("%s = %+v, want skipped", name, c)
		}
	}
	for _, c := range f.calls {
		if strings.HasPrefix(c, "Net.Get") && strings.Contains(c, "bearer=true") {
			t.Errorf("authenticated call to an unreachable proxy: %s", c)
		}
	}
	// An empty pair checks nothing and says why.
	empty := CheckLiteLLM(context.Background(), f.Env(), config.ProxyPair{URL: llURL})
	if empty.Checked || empty.Reachable.Status != CheckSkipped || empty.Reachable.Detail == "" {
		t.Errorf("empty pair = %+v", empty)
	}
	// A cancelled context is a failed probe, not a panic or a hang.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	g, gp := llGood(t)
	g.getReplies = nil
	g.getErr = context.Canceled
	if st := CheckLiteLLM(ctx, g.Env(), gp); st.Reachable.Status == CheckOK {
		t.Errorf("cancelled context reads reachable: %+v", st.Reachable)
	}
}

// TestChecksUseTheInjectedPlistAndConfigPaths pins that the plist and
// config.yaml paths come from Env.LiteLLM, not from package globals.
func TestChecksUseTheInjectedPlistAndConfigPaths(t *testing.T) {
	f, pair := llGood(t)
	f.plistPath, f.configPath = "/scratch/proxy.plist", "/scratch/config.yaml"
	f.files[f.configPath] = []byte("x")
	f.modTimes[f.configPath] = f.processStart.Add(-time.Hour)
	CheckLiteLLM(context.Background(), f.Env(), pair)
	if !f.called("LiteLLM.ReadPlist /scratch/proxy.plist") || !f.called("FS.Stat /scratch/config.yaml") {
		t.Errorf("calls = %q", f.calls)
	}
}

// TestNothingPrintsTheKey pins that the pair's API key reaches no Detail, no
// state dump and no recorded call, even when Net.Get fails.
func TestNothingPrintsTheKey(t *testing.T) {
	for _, mode := range []string{"good", "rejected", "error"} {
		f, pair := llGood(t)
		switch mode {
		case "rejected":
			f.getReplies[llURL+"/v1/models"] = getReply{status: 401, body: []byte("bad key " + llKey)}
		case "error":
			f.getReplies[llURL+"/v1/models"] = getReply{err: fmt.Errorf("Authorization: Bearer %s", llKey)}
		}
		st := CheckLiteLLM(context.Background(), f.Env(), pair)
		dump := fmt.Sprintf("%+v %q", st, f.calls)
		if strings.Contains(dump, llKey) {
			t.Errorf("%s: key leaked: %s", mode, dump)
		}
	}
}

// TestObserveRunsTheLiteLLMChecksOnlyInLocalMode pins that Observe fills
// State.LiteLLM in local mode and leaves it zero otherwise.
func TestObserveRunsTheLiteLLMChecksOnlyInLocalMode(t *testing.T) {
	f, pair := llGood(t)
	f.pair = pair
	f.configFile = config.ModelServerFile{Exists: true, Table: &config.ModelServerState{Mode: "local"}}
	if st := Observe(context.Background(), f.Env(), ObserveOptions{}); !st.LiteLLM.Checked {
		t.Errorf("local mode: %+v", st.LiteLLM)
	}
	f.configFile.Table.Mode = "native"
	f.calls = nil
	if st := Observe(context.Background(), f.Env(), ObserveOptions{}); st.LiteLLM != (LiteLLMState{}) || f.called("Net.Get") {
		t.Errorf("native mode ran the checks: %+v", st.LiteLLM)
	}
}
