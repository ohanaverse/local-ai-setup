package smoke

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
)

// wireFixtureRows are the rows the model server sends for three local
// models: one running (launch), one idle on disk (start) and one not on disk
// (block, with its reason).
func wireFixtureRows() []modelapi.Row {
	row := func(name, state, action, reason string, order int) modelapi.Row {
		return modelapi.Row{
			ID: "ollama/" + name, GatewayID: "ollama/" + name, ProviderID: "ollama", ModelName: name,
			Location: "local", Registered: true, State: state, Action: action, BlockReason: reason, Order: order,
		}
	}
	return []modelapi.Row{
		row("running:1", modelapi.StateRunning, modelapi.ActionLaunch, "", 0),
		row("idle:1", modelapi.StateStopped, modelapi.ActionStart, "", 1),
		row("absent:1", modelapi.StateStopped, modelapi.ActionBlock, "not on disk", 2),
	}
}

// TestCandidatesWireRules proves CandidatesWire and EligibilityWire apply
// smoke's rules to the server's rows: the candidates are the launch and start
// rows, never a blocked one; the eligibility list has no start row; and a
// command agent (shell) is never an eligible agent. It matters because a
// smoke run must offer exactly what can run: a blocked row would be started
// and fail, and a shell row is no agent.
func TestCandidatesWireRules(t *testing.T) {
	ags := []config.Agent{{Name: "claude", SupportedProviders: []string{"ollama"}}, {Name: "shell", AllProviders: true}}
	var got []string
	cands, _ := CandidatesWire(wireFixtureRows(), ags, "")
	for _, c := range cands {
		got = append(got, c.Row.Model.ID+"="+joinAgents(c.Agents))
	}
	if want := []string{"ollama/idle:1=claude,", "ollama/running:1=claude,"}; !slices.Equal(got, want) {
		t.Fatalf("CandidatesWire = %v, want %v", got, want)
	}
	models, agentsFor, _ := EligibilityWire(wireFixtureRows(), ags, "")
	var ids []string
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	if want := []string{"ollama/running:1"}; !slices.Equal(ids, want) {
		t.Fatalf("EligibilityWire models = %v, want %v", ids, want)
	}
	if !slices.Equal(agentsFor["ollama/running:1"], []string{"claude"}) {
		t.Errorf("EligibilityWire agents for ollama/running:1 = %v, want [claude]", agentsFor["ollama/running:1"])
	}
}

func joinAgents(a []string) string {
	s := ""
	for _, x := range a {
		s += x + ","
	}
	return s
}

// gatewayRig is one RunRowGateway test's harness: a gateway on httptest whose
// model list answers probeStatus, a stubbed agent run answering outcomes in
// order, and a ready func that counts its calls.
type gatewayRig struct {
	mu         sync.Mutex
	gw         agents.Gateway
	probes     int
	probeAuth  string
	runs       int
	readyCalls int
	opts       RowOptions
}

func newGatewayRig(t *testing.T, probeStatus int, outcomes ...ExecOutcome) *gatewayRig {
	t.Helper()
	r := &gatewayRig{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.probes++
		r.probeAuth = req.Header.Get("Authorization")
		r.mu.Unlock()
		w.WriteHeader(probeStatus)
	}))
	t.Cleanup(srv.Close)
	r.gw = agents.Gateway{Origin: srv.URL, Token: "gw-token"}
	r.opts = RowOptions{Run: func(_ agents.Gateway, _ []config.Model, _ string, _ config.Model, _ string, _ string, _ time.Duration, _ ProfileApplier, cleanup *func() error) ExecOutcome {
		*cleanup = func() error { return nil }
		r.mu.Lock()
		defer r.mu.Unlock()
		i := min(r.runs, len(outcomes)-1)
		r.runs++
		return outcomes[i]
	}}
	return r
}

// run runs one row through RunRowGateway; readyErrs[i] is what the i-th
// ready call answers (nil beyond the list).
func (r *gatewayRig) run(m config.Model, readyErrs ...error) RowResult {
	ready := func(context.Context) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		i := r.readyCalls
		r.readyCalls++
		if i < len(readyErrs) {
			return readyErrs[i]
		}
		return nil
	}
	return RunRowGateway(context.Background(), r.gw, nil, "claude", m, "prompt", "SENTINEL", time.Second, FixedDir("."), nil, ready, r.opts)
}

var gatewayModel = config.Model{ID: "openrouter/a", GatewayID: "openrouter/a", ProviderID: "openrouter", Location: config.LocationCloud}

const gatewayDownReason = "gateway unavailable (503) — the model server may be restarting LiteLLM"

// TestSmokeRowRetriesGateway503 pins R12's one wt-driven retry: a start or
// auto-stop by another client session restarts the proxy mid-run, so the
// first run fails, the probe says 503, ready waits, and the rerun passes: the
// row is PASS, ready was called before the row and once more after the
// failure, and the probe carried the gateway token.
func TestSmokeRowRetriesGateway503(t *testing.T) {
	r := newGatewayRig(t, http.StatusServiceUnavailable,
		StubOutcome("connection refused", 1), StubOutcome("SENTINEL", 0))
	res := r.run(gatewayModel)
	if res.Status != StatusPass {
		t.Fatalf("status = %s (err %v), want PASS after the rerun", res.Status, res.Err)
	}
	if r.runs != 2 || r.readyCalls != 2 || r.probes != 1 {
		t.Errorf("runs=%d ready=%d probes=%d, want 2, 2, 1", r.runs, r.readyCalls, r.probes)
	}
	if r.probeAuth != "Bearer gw-token" {
		t.Errorf("probe Authorization = %q", r.probeAuth)
	}
}

// TestSmokeRowSkipsWhenGatewayStaysDown pins that a gateway that does not come
// back is a SKIP with the 503 reason, never a FAIL (the model server owns the
// proxy; a run that cannot reach it says nothing about the model), whether it
// is down before the row or after a failed one.
func TestSmokeRowSkipsWhenGatewayStaysDown(t *testing.T) {
	down := &modelclient.GatewayDownError{Addr: "127.0.0.1:1"}
	t.Run("before the row", func(t *testing.T) {
		r := newGatewayRig(t, http.StatusServiceUnavailable, StubOutcome("SENTINEL", 0))
		res := r.run(gatewayModel, down)
		if res.Status != StatusSkip || res.Err == nil || res.Err.Error() != gatewayDownReason {
			t.Fatalf("status = %s err = %v, want SKIP %q", res.Status, res.Err, gatewayDownReason)
		}
		if r.runs != 0 {
			t.Errorf("the agent ran %d times against a gateway that is down", r.runs)
		}
	})
	t.Run("after a failed row", func(t *testing.T) {
		r := newGatewayRig(t, http.StatusServiceUnavailable, StubOutcome("connection refused", 1))
		res := r.run(gatewayModel, nil, down)
		if res.Status != StatusSkip || res.Err == nil || res.Err.Error() != gatewayDownReason {
			t.Fatalf("status = %s err = %v, want SKIP %q", res.Status, res.Err, gatewayDownReason)
		}
		if r.runs != 1 {
			t.Errorf("runs = %d, want 1 (no rerun against a gateway that is down)", r.runs)
		}
	})
}

// TestSmokeRowGatewayFailThatIsNot503StaysFail proves only a 503 earns the
// retry: a failed row whose probe answers 200 is a FAIL, the agent ran once
// and ready was not called a second time.
func TestSmokeRowGatewayFailThatIsNot503StaysFail(t *testing.T) {
	r := newGatewayRig(t, http.StatusOK, StubOutcome("wrong answer", 0))
	res := r.run(gatewayModel)
	if res.Status != StatusFail {
		t.Fatalf("status = %s, want FAIL", res.Status)
	}
	if r.runs != 1 || r.readyCalls != 1 {
		t.Errorf("runs=%d ready=%d, want 1, 1", r.runs, r.readyCalls)
	}
}

// TestSmokeRowGatewayRerunFailureIsFail proves the rerun is the last word:
// when it fails too, the row is FAIL and there is no third run.
func TestSmokeRowGatewayRerunFailureIsFail(t *testing.T) {
	r := newGatewayRig(t, http.StatusServiceUnavailable, StubOutcome("x", 1), StubOutcome("y", 1))
	res := r.run(gatewayModel)
	if res.Status != StatusFail || r.runs != 2 {
		t.Fatalf("status = %s runs = %d, want FAIL after exactly 2 runs", res.Status, r.runs)
	}
}

// TestSmokeRowGatewayNativeModelNeverWaitsForTheGateway proves a native model
// launches bare: it never goes to the gateway, so neither the readiness wait
// nor the probe is involved.
func TestSmokeRowGatewayNativeModelNeverWaitsForTheGateway(t *testing.T) {
	r := newGatewayRig(t, http.StatusServiceUnavailable, StubOutcome("boom", 1))
	m := config.Model{ID: "claude/native", ProviderID: "claude", Native: true}
	res := r.run(m)
	if res.Status != StatusFail || r.readyCalls != 0 || r.probes != 0 {
		t.Fatalf("status=%s ready=%d probes=%d, want FAIL, 0, 0", res.Status, r.readyCalls, r.probes)
	}
}

// TestSmokeRowOverTLSWithCAFile proves the row's 503 probe trusts the
// gateway's ca_file: against an https fake whose certificate only the CA
// file vouches for, the probe reaches the fake (its 503 earns the rerun)
// rather than failing the handshake and reading silently as "not 503".
func TestSmokeRowOverTLSWithCAFile(t *testing.T) {
	r := newGatewayRig(t, http.StatusServiceUnavailable, StubOutcome("connection refused", 1), StubOutcome("SENTINEL", 0))
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.probes++
		r.probeAuth = req.Header.Get("Authorization")
		r.mu.Unlock()
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	srv.StartTLS()
	t.Cleanup(srv.Close)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o644); err != nil {
		t.Fatal(err)
	}
	r.gw = agents.Gateway{Origin: srv.URL, Token: "gw-token", CAFile: ca}
	res := r.run(gatewayModel)
	if res.Status != StatusPass || r.probes != 1 || r.probeAuth != "Bearer gw-token" {
		t.Fatalf("status = %s (err %v), probes = %d, want PASS after the probe reached the TLS fake", res.Status, res.Err, r.probes)
	}
}

// subscriptionWireRows are a cloud row, a model on the Claude subscription
// and an alias whose one target is on the subscription.
func subscriptionWireRows() []modelapi.Row {
	return []modelapi.Row{
		{ID: "openrouter/a", GatewayID: "openrouter/a", ProviderID: "openrouter", ModelName: "a", Location: "cloud",
			Registered: true, State: modelapi.StateRunning, Action: modelapi.ActionLaunch, Order: 1},
		{ID: "claude-haiku-5-5", GatewayID: "claude-haiku-5-5", ProviderID: config.SubscriptionProvider, ModelName: "claude-haiku-5-5",
			Location: "cloud", Registered: true, State: modelapi.StateRunning, Action: modelapi.ActionLaunch, Order: 2},
		{ID: "alias/sonnet", GatewayID: "alias/sonnet", ProviderID: "alias", ModelName: "sonnet", Kind: modelapi.KindAlias,
			Location: "cloud", Registered: true, State: modelapi.StateRunning, Action: modelapi.ActionLaunch, Order: 3,
			Targets: []modelapi.AliasTarget{{ID: "claude-sonnet-5-5", Order: 1, ProviderID: config.SubscriptionProvider, Location: "cloud", State: modelapi.StateRunning}}},
	}
}

// TestSmokeWireDropsSubscriptionRows pins decision 9's walk (#597): with no
// allowed id, a subscription row and an alias with a subscription target are
// never candidates nor eligible, and come back once each in the dropped list
// (sorted); allowing one id keeps exactly that row, for claude alone, and
// still drops the other. It matters because a smoke candidate is a real
// request: one on the subscription spends the owner's plan without being
// asked.
func TestSmokeWireDropsSubscriptionRows(t *testing.T) {
	ags := []config.Agent{{Name: "claude", AllProviders: true}, {Name: "pi", AllProviders: true}, {Name: "codex", AllProviders: true}}
	ids := func(cs []Candidate) []string {
		var out []string
		for _, c := range cs {
			out = append(out, c.Row.Model.ID+"="+joinAgents(c.Agents))
		}
		return out
	}
	cands, dropped := CandidatesWire(subscriptionWireRows(), ags, "")
	if got, want := ids(cands), []string{"openrouter/a=claude,codex,pi,"}; !slices.Equal(got, want) {
		t.Fatalf("CandidatesWire = %v, want %v", got, want)
	}
	if want := []string{"alias/sonnet", "claude-haiku-5-5"}; !slices.Equal(dropped, want) {
		t.Fatalf("dropped = %v, want %v", dropped, want)
	}
	models, _, edropped := EligibilityWire(subscriptionWireRows(), ags, "")
	if len(models) != 1 || models[0].ID != "openrouter/a" || !slices.Equal(edropped, dropped) {
		t.Fatalf("EligibilityWire = %v dropped %v, want only openrouter/a and %v", models, edropped, dropped)
	}

	cands, dropped = CandidatesWire(subscriptionWireRows(), ags, "claude-haiku-5-5")
	if got, want := ids(cands), []string{"claude-haiku-5-5=claude,", "openrouter/a=claude,codex,pi,"}; !slices.Equal(got, want) {
		t.Fatalf("CandidatesWire allowing claude-haiku-5-5 = %v, want %v", got, want)
	}
	if want := []string{"alias/sonnet"}; !slices.Equal(dropped, want) {
		t.Fatalf("dropped allowing claude-haiku-5-5 = %v, want %v", dropped, want)
	}
}

// TestSmokeSubscriptionHeaderModeReachesTheApplier proves the row's profile
// applier gets the build's own LaunchInfo.HeaderMode (#597): true for claude
// on a subscription model, false for a model that is not. Smoke used to
// re-derive the flag beside the build; reading the build's flag keeps the
// applier's backstop (a profile undoing header mode) on the real mode.
func TestSmokeSubscriptionHeaderModeReachesTheApplier(t *testing.T) {
	writeFakeAgentBinary(t, "claude")
	for _, tc := range []struct {
		m    config.Model
		want bool
	}{
		{config.Model{ID: "claude-haiku-5-5", ModelName: "claude-haiku-5-5", Location: config.LocationCloud, Subscription: true}, true},
		{config.Model{ID: "openrouter/a", ModelName: "a", Location: config.LocationCloud}, false},
	} {
		var got []bool
		applier := func(cmd *exec.Cmd, oneShotArgs []string, headerMode bool) (func() error, error) {
			got = append(got, headerMode)
			cmd.Args = append(cmd.Args, oneShotArgs...)
			return func() error { return nil }, nil
		}
		var cleanup func() error
		if out := gatewayRunForTest(context.Background(), "claude", tc.m, "hi", FixedDir(t.TempDir()), 5*time.Second, applier, &cleanup); out.StartErr != nil {
			t.Fatalf("%s: outcome = %+v", tc.m.ID, out)
		}
		if len(got) != 1 || got[0] != tc.want {
			t.Errorf("%s: the applier saw headerMode %v, want [%v]", tc.m.ID, got, tc.want)
		}
	}
}
