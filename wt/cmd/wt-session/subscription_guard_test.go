package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/claudelogin"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tui"
	"github.com/spf13/cobra"
)

// subTok is the wt token sentinel of the subscription tests.
const subTok = "wt-SENTINEL-tok"

// subRow is a model on the Claude subscription, launchable.
func subRow(id string) modelapi.Row {
	return modelapi.Row{
		ID: id, GatewayID: id, ProviderID: config.SubscriptionProvider, ModelName: id, Family: "claude",
		Location: "cloud", Tags: []string{"code"}, Registered: true, State: modelapi.StateRunning,
		Action: modelapi.ActionLaunch, Order: 9,
	}
}

// goodLogin is a claude.ai login on a new enough Claude Code.
func goodLogin() claudelogin.Status {
	return claudelogin.Status{LoggedIn: true, Method: "claude.ai", Provider: "firstParty", Version: "2.1.330"}
}

// subRig is newRemoteRig for the subscription tests: the fake's token is
// subTok, its ping says passthrough is on when passthrough is set, the
// session's config.toml opts in when optIn is set, and the login check is a
// stub returning st (recorded as "GUARD" in the fake's request log). A nil
// st keeps the hook unset, for a test that wires its own.
func subRig(t *testing.T, optIn, passthrough bool, st *claudelogin.Status, rows ...modelapi.Row) (*remoteRig, *int) {
	t.Helper()
	r := newRemoteRig(t, rows...)
	r.f.Token = subTok
	r.f.Passthrough = passthrough
	extra := ""
	if optIn {
		extra = "subscription = true\n"
	}
	hooks := r.a.hooks
	r.a = pointAtFake(t, r.f, extra)
	r.a.hooks = hooks
	calls := new(int)
	if st != nil {
		s := *st
		r.a.hooks.claudeLogin = func(context.Context) (claudelogin.Status, error) {
			*calls++
			r.f.mu.Lock()
			r.f.Reqs = append(r.f.Reqs, "GUARD")
			r.f.mu.Unlock()
			return s, nil
		}
	}
	return r, calls
}

// useFakeLogin makes the rig's login check the real one (realClaudeLogin),
// which finds the rig's fake claude first on PATH: its `auth status --json`
// answer is status and its `--version` answer is version.
func useFakeLogin(t *testing.T, r *remoteRig, status, version string) {
	t.Helper()
	dir := t.TempDir()
	auth, ver := filepath.Join(dir, "auth.json"), filepath.Join(dir, "version")
	if err := os.WriteFile(auth, []byte(status), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ver, []byte(version), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WT_FAKE_AUTH", auth)
	t.Setenv("WT_FAKE_VERSION", ver)
	r.a.hooks.claudeLogin = realClaudeLogin
}

// nothingRegistered fails the test when the fake saw a client-session
// register, a start, a gateway call or an agent build.
func nothingRegistered(t *testing.T, f *fakeModelServer) {
	t.Helper()
	for _, r := range f.reqs() {
		if strings.HasPrefix(r, "POST /control/v1/sessions") || strings.HasPrefix(r, "POST /control/v1/start") ||
			strings.Contains(r, " /v1/") || r == "BUILD" {
			t.Errorf("the refused launch reached %q\n%s", r, strings.Join(f.reqs(), "\n"))
		}
	}
}

// exitCodeOf is err's exit code, 0 when it carries none.
func exitCodeOf(err error) int {
	c, _ := exitcode.Code(err)
	return c
}

// TestNilLoginHookRefusesInTests proves a test binary never runs the real
// claude (decision 8): with no claudeLogin hook the login check returns the
// "not stubbed" error, and the claude first on PATH (a fake that touches a
// mark) is never run.
func TestNilLoginHookRefusesInTests(t *testing.T) {
	r := newRemoteRig(t)
	mark := filepath.Join(t.TempDir(), "ran")
	t.Setenv("WT_FAKE_MARK", mark)
	_, err := (hooks{}).login(context.Background())
	if !errors.Is(err, errLoginNotStubbed) || !strings.Contains(err.Error(), "not stubbed") {
		t.Fatalf("err = %v, want the not-stubbed refusal", err)
	}
	if _, serr := os.Stat(mark); serr == nil {
		t.Error("the login check ran claude")
	}
	_ = r
}

// TestSubscriptionNoLoginRefusesBeforeAnything is decision 8 end to end on
// the non-TUI path: a subscription model with no claude.ai login (the fake
// claude reports none) is exit 1 naming `claude auth login`, before the client
// session's register, any start, any gateway call and the agent's build.
func TestSubscriptionNoLoginRefusesBeforeAnything(t *testing.T) {
	r, _ := subRig(t, true, true, nil, subRow("claude-haiku-5-5"))
	useFakeLogin(t, r, `{"loggedIn":false,"authMethod":"none","apiProvider":"firstParty"}`, "2.1.330\n")
	err := r.launch(t, context.Background(), "claude-haiku-5-5", "", false)
	if err == nil || exitCodeOf(err) != 1 || !strings.Contains(err.Error(), "claude auth login") {
		t.Fatalf("err = %v (exit %d), want exit 1 naming claude auth login", err, exitCodeOf(err))
	}
	if !strings.Contains(err.Error(), "nothing was started or registered") {
		t.Errorf("err = %v, want it to say nothing was started", err)
	}
	nothingRegistered(t, r.f)
}

// TestSubscriptionWithoutOptInRefuses is decision 12: a machine without
// [model-server] subscription = true never sends its login, whatever the
// server's rows say. The launch is refused before anything is registered and
// before the login check runs; an alias the server re-pointed at a
// subscription target refuses the same way, so the server alone cannot
// switch a launch into header mode.
func TestSubscriptionWithoutOptInRefuses(t *testing.T) {
	alias := modelapi.Row{ID: "alias/sonnet", GatewayID: "alias/sonnet", ProviderID: "alias", ModelName: "sonnet", Kind: modelapi.KindAlias,
		Location: "cloud", Tags: []string{"code"}, Registered: true, State: modelapi.StateRunning, Action: modelapi.ActionLaunch, Order: 8,
		Targets: []modelapi.AliasTarget{{ID: "claude-sonnet-5-5", Order: 1, ProviderID: config.SubscriptionProvider, Location: "cloud", State: modelapi.StateRunning}}}
	for _, pin := range []string{"claude-haiku-5-5", "alias/sonnet"} {
		t.Run(pin, func(t *testing.T) {
			st := goodLogin()
			r, calls := subRig(t, false, true, &st, subRow("claude-haiku-5-5"), alias)
			err := r.launch(t, context.Background(), pin, "", false)
			if err == nil || exitCodeOf(err) != 1 || !strings.Contains(err.Error(), "has not opted in") ||
				!strings.Contains(err.Error(), "[model-server] subscription = true") {
				t.Fatalf("err = %v, want the opt-in refusal", err)
			}
			if *calls != 0 {
				t.Errorf("the login check ran %d times, want 0", *calls)
			}
			nothingRegistered(t, r.f)
		})
	}
}

// TestSubscriptionRemoteHostNoted proves an opted-in launch to a remote https
// server says, in one note, which host the login is sent to (decision 12),
// and a loopback one says nothing. The https server is an httptest TLS server
// reached through a dialer that ignores the url's host, so nothing leaves
// this machine; its certificate is trusted through its own client.
func TestSubscriptionRemoteHostNoted(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(modelapi.Ping{APIMajor: modelapi.APIMajor, Schema: modelapi.SchemaVersion, Server: "wt-model", SubscriptionPassthrough: true})
	}))
	t.Cleanup(ts.Close)
	tr := ts.Client().Transport.(*http.Transport).Clone()
	addr := ts.Listener.Addr().String()
	tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	ep := modelclient.Endpoint{URL: "https://example.com:8787", Token: subTok}
	ms := &modelSession{client: modelclient.New(ep, modelclient.WithHTTPClient(&http.Client{Transport: tr})), ep: ep, subscription: true}
	login := func(context.Context) (claudelogin.Status, error) { return goodLogin(), nil }
	var out bytes.Buffer
	if err := subscriptionGuardTo(context.Background(), &out, ms, []modelapi.Row{subRow("claude-haiku-5-5")}, nil, login); err != nil {
		t.Fatalf("guard: %v", err)
	}
	if n := strings.Count(out.String(), "wt: note: your Claude login will be sent to the model server at example.com\n"); n != 1 {
		t.Errorf("notes = %q, want one naming example.com", out.String())
	}

	r, _ := subRig(t, true, true, nil)
	lms, err := r.a.openModels(context.Background(), launchStill)
	if err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := subscriptionGuardTo(context.Background(), &out, lms, []modelapi.Row{subRow("claude-haiku-5-5")}, nil, login); err != nil {
		t.Fatalf("loopback guard: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("a loopback server got a note: %q", out.String())
	}
}

// TestSubscriptionServerOffOrOlderRefuses is decision 2: a server whose ping
// lacks subscription_passthrough (off, or older than this wt) refuses with
// the one message naming the host's fix, with nothing registered. A realistic
// older server marks the row blocked as unmapped; the launch still says
// decision 2's message, not the unmapped reason, on the -M pin and on a slot
// profile naming it.
func TestSubscriptionServerOffOrOlderRefuses(t *testing.T) {
	const want = "does not accept a Claude login: its subscription passthrough is off, or it is older than this wt"
	st := goodLogin()
	t.Run("off, row launchable", func(t *testing.T) {
		r, calls := subRig(t, true, false, &st, subRow("claude-haiku-5-5"))
		err := r.launch(t, context.Background(), "claude-haiku-5-5", "", false)
		if err == nil || exitCodeOf(err) != 1 || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "subscription_passthrough = true") {
			t.Fatalf("err = %v", err)
		}
		if *calls != 0 {
			t.Errorf("login ran %d times", *calls)
		}
		nothingRegistered(t, r.f)
	})
	old := subRow("claude-haiku-5-5")
	old.State, old.Action, old.BlockReason = modelapi.StateBlocked, modelapi.ActionBlock, `provider "claude-subscription" has no LiteLLM mapping, so the gateway cannot route claude-haiku-5-5`
	t.Run("older server, pin", func(t *testing.T) {
		r, _ := subRig(t, true, false, &st, old)
		err := r.launch(t, context.Background(), "claude-haiku-5-5", "", false)
		if err == nil || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), "no LiteLLM mapping") {
			t.Fatalf("err = %v, want decision 2's message", err)
		}
		nothingRegistered(t, r.f)
	})
	t.Run("older server, slot profile", func(t *testing.T) {
		r, _ := subRig(t, true, false, &st, append(slotWireRows(), old)...)
		store := profiles.SlotStore{Profiles: []profiles.SlotProfile{{Name: "mix", Agent: "claude", Default: "sonnet", Slots: map[string]string{"sonnet": "openrouter/a", "haiku": "claude-haiku-5-5"}}}}
		r.a.slotChecks = profiles.ValidateSlots(store, agents.SlotDrivers())
		err := r.launch(t, context.Background(), "profile/mix", "", false)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want decision 2's message", err)
		}
		nothingRegistered(t, r.f)
	})
}

// TestSubscriptionPlainHTTPRemoteRefusedBeforeSending is decision 3's client
// half: subscriptionEndpointOK decides from the url alone (no client, no
// dial) that plain http goes only to loopback (127.0.0.0/8, ::1 after
// unmapping, the exact names localhost and localhost.), and https anywhere.
// WT_MODEL_URL wins over a loopback [model-server] url, so the endpoint
// actually used is judged. In a rig the guard refuses such a launch before
// the client session, any start and any gateway call.
func TestSubscriptionPlainHTTPRemoteRefusedBeforeSending(t *testing.T) {
	for raw, ok := range map[string]bool{
		"http://192.0.2.7:8787":        false,
		"http://[::ffff:192.0.2.7]:1":  false,
		"http://localhost.example.com": false,
		"http://127.1":                 false,
		"http://0.0.0.0":               false,
		"ftp://127.0.0.1":              false,
		"https://host":                 true,
		"https://192.0.2.7:8787":       true,
		"http://127.0.0.1":             true,
		"http://127.9.9.9:1":           true,
		"http://[::1]":                 true,
		"http://[::ffff:127.0.0.1]:1":  true,
		"http://localhost":             true,
		"http://localhost.":            true,
	} {
		err := subscriptionEndpointOK(modelclient.Endpoint{URL: raw, Token: subTok})
		if (err == nil) != ok {
			t.Errorf("subscriptionEndpointOK(%q) = %v, want ok=%v", raw, err, ok)
		}
		if err != nil && strings.Contains(err.Error(), subTok) {
			t.Errorf("the refusal holds the token: %v", err)
		}
	}
	// WT_MODEL_URL over a loopback [model-server] url.
	tok := filepath.Join(t.TempDir(), "tok")
	if err := os.WriteFile(tok, []byte(subTok+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := func(k string) string {
		if k == "WT_MODEL_URL" {
			return "http://192.0.2.7:1"
		}
		return ""
	}
	ep, err := modelclient.ResolveEndpoint("http://127.0.0.1:9", tok, env)
	if err != nil {
		t.Fatal(err)
	}
	if err := subscriptionEndpointOK(ep); err == nil {
		t.Errorf("WT_MODEL_URL=http://192.0.2.7:1 passed (endpoint %q)", ep.URL)
	}

	// The rig: WT_MODEL_URL names a non-loopback plain-http address; every
	// request is dialled to the fake instead, so the test never leaves this
	// machine and can read what was sent.
	st := goodLogin()
	r, calls := subRig(t, true, true, &st, subRow("claude-haiku-5-5"))
	t.Setenv("WT_MODEL_URL", "http://192.0.2.7:1")
	fakeAddr := r.f.Listener.Addr().String()
	r.a.hooks.newClient = func(ep modelclient.Endpoint) *modelclient.Client {
		tr := &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, fakeAddr)
		}}
		return modelclient.New(ep, modelclient.WithHTTPClient(&http.Client{Transport: tr}))
	}
	err = r.launch(t, context.Background(), "claude-haiku-5-5", "", false)
	if err == nil || exitCodeOf(err) != 1 || !strings.Contains(err.Error(), "plain http") || !strings.Contains(err.Error(), "192.0.2.7:1") {
		t.Fatalf("err = %v, want the plain-http refusal", err)
	}
	if *calls != 0 {
		t.Errorf("login ran %d times", *calls)
	}
	nothingRegistered(t, r.f)
}

// TestSubscriptionOldClaudeRefused proves a Claude Code older than
// MinVersion (it would not send ANTHROPIC_CUSTOM_HEADERS) is refused before
// anything, naming the minimum and the version found.
func TestSubscriptionOldClaudeRefused(t *testing.T) {
	r, _ := subRig(t, true, true, nil, subRow("claude-haiku-5-5"))
	useFakeLogin(t, r, `{"loggedIn":true,"authMethod":"claude.ai","apiProvider":"firstParty"}`, "2.1.200 (Claude Code)\n")
	err := r.launch(t, context.Background(), "claude-haiku-5-5", "", false)
	if err == nil || exitCodeOf(err) != 1 || !strings.Contains(err.Error(), claudelogin.MinVersion) || !strings.Contains(err.Error(), "2.1.200") {
		t.Fatalf("err = %v, want the version refusal", err)
	}
	nothingRegistered(t, r.f)
}

// stubLaunchProfile makes profiles.toml one enabled claude profile matching
// model, with env and config_content.
func stubLaunchProfile(t *testing.T, model string, env map[string]string, cc map[string]any) {
	t.Helper()
	old := loadProfileStore
	t.Cleanup(func() { loadProfileStore = old })
	loadProfileStore = func() (profiles.Store, error) {
		return profiles.Store{Enabled: true, Profiles: []profiles.Profile{{Agent: "claude", Match: "model", Model: model, Env: env, ConfigContent: cc}}}, nil
	}
	oldConfirm := confirmProfile
	t.Cleanup(func() { confirmProfile = oldConfirm })
	confirmProfile = func(profiles.ResolvedProfile) (bool, error) { return true, nil }
}

// TestSubscriptionLaunchProfileCannotOverrideHeaderEnv is decision 7's
// launch-profile rule (and carry-overs 1 and 2): a launch profile matching
// the subscription model that sets ANTHROPIC_CUSTOM_HEADERS,
// ANTHROPIC_AUTH_TOKEN, CLAUDE_CODE_OAUTH_TOKEN, ANTHROPIC_API_KEY,
// ANTHROPIC_BASE_URL or a CLAUDE_CODE_USE_* switch (in env or its
// config_content env), or a top-level apiKeyHelper, is refused, exit 1, before
// any client session: applied after the build it would replace the merged
// header or displace the checked login. The refusal names the key, not the
// value.
func TestSubscriptionLaunchProfileCannotOverrideHeaderEnv(t *testing.T) {
	const val = "VAL-SENTINEL"
	type tc struct {
		name, key string
		env       map[string]string
		cc        map[string]any
	}
	var cases []tc
	for _, k := range []string{"ANTHROPIC_CUSTOM_HEADERS", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_USE_BEDROCK"} {
		cases = append(cases, tc{"env " + k, k, map[string]string{k: val}, nil})
	}
	cases = append(cases,
		tc{"config_content.env CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", nil, map[string]any{"env": map[string]any{"CLAUDE_CODE_OAUTH_TOKEN": val}}},
		tc{"config_content apiKeyHelper", "apiKeyHelper", nil, map[string]any{"apiKeyHelper": "/bin/echo " + val}},
	)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := goodLogin()
			r, _ := subRig(t, true, true, &st, subRow("claude-haiku-5-5"))
			stubLaunchProfile(t, "claude-haiku-5-5", c.env, c.cc)
			err := r.launch(t, context.Background(), "claude-haiku-5-5", "", false)
			if err == nil || exitCodeOf(err) != 1 || !strings.Contains(err.Error(), c.key) {
				t.Fatalf("err = %v, want exit 1 naming %s", err, c.key)
			}
			if strings.Contains(err.Error(), val) {
				t.Errorf("the refusal echoes the value: %v", err)
			}
			nothingRegistered(t, r.f)
		})
	}
}

// TestHeaderModeAppliersRefuseTheProfile pins the appliers' backstop (for a
// path where the profile resolves after the guard): applyResolvedProfile and
// applySlotProfile refuse a profile that would undo header mode when the
// build reported header mode, and apply it unchanged otherwise.
func TestHeaderModeAppliersRefuseTheProfile(t *testing.T) {
	rp := profiles.ResolvedProfile{Env: map[string]string{"CLAUDE_CODE_USE_VERTEX": "1"}}
	cmd := trueCmd(t)
	if _, err := applyResolvedProfile(cmd, "claude", rp, nil, true); err == nil || !strings.Contains(err.Error(), "CLAUDE_CODE_USE_VERTEX") {
		t.Errorf("header mode: err = %v", err)
	}
	if slices.Contains(cmd.Env, "CLAUDE_CODE_USE_VERTEX=1") {
		t.Error("the refused profile was applied")
	}
	cmd = trueCmd(t)
	if _, err := applyResolvedProfile(cmd, "claude", rp, nil, false); err != nil || !slices.Contains(cmd.Env, "CLAUDE_CODE_USE_VERTEX=1") {
		t.Errorf("token mode: err = %v, env %v", err, cmd.Env)
	}
	check := slotFixtureCheck(0, "mix", "sonnet", map[string]string{"sonnet": "openrouter/a"})
	check.Profile.ConfigContent = map[string]any{"apiKeyHelper": "/bin/echo x"}
	if _, err := applySlotProfile(trueCmd(t), "claude", slotRow(check), true); err == nil || !strings.Contains(err.Error(), "apiKeyHelper") {
		t.Errorf("slot header mode: err = %v", err)
	}
	if _, err := applySlotProfile(trueCmd(t), "claude", slotRow(check), false); err != nil {
		t.Errorf("slot token mode: %v", err)
	}
}

// trueCmd is a command with a scratch Dir and an empty Env.
func trueCmd(t *testing.T) *exec.Cmd {
	t.Helper()
	c := exec.Command("true")
	c.Dir = t.TempDir()
	c.Env = []string{}
	return c
}

// TestSubscriptionLaunchChildEnvAndArgv is the launched agent's view of
// header mode: its environment holds ANTHROPIC_CUSTOM_HEADERS once, carrying
// the wt token line, and no ANTHROPIC_AUTH_TOKEN, and its argv never holds
// the token. A nested launch (the parent environment already holds a
// header-mode ANTHROPIC_CUSTOM_HEADERS) of claude/native gets no wt token
// line: it talks to Anthropic directly.
func TestSubscriptionLaunchChildEnvAndArgv(t *testing.T) {
	native := modelapi.Row{ID: "claude/native", ProviderID: "claude", ModelName: "native", Native: true,
		Location: "cloud", Registered: true, State: modelapi.StateRunning, Action: modelapi.ActionLaunch, Tags: []string{"code"}}
	named := native
	named.ID, named.ModelName, named.Order = "claude/opus", "opus", 2
	st := goodLogin()
	r, calls := subRig(t, true, true, &st, subRow("claude-haiku-5-5"), native, named)
	dir := t.TempDir()
	envDump, argvDump := filepath.Join(dir, "env"), filepath.Join(dir, "argv")
	t.Setenv("WT_FAKE_ENV_DUMP", envDump)
	t.Setenv("WT_FAKE_ARGV_DUMP", argvDump)
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "FAKE-inherited")
	if err := r.launch(t, context.Background(), "claude-haiku-5-5", "", false); err != nil {
		t.Fatalf("launch: %v", err)
	}
	if *calls != 1 {
		t.Errorf("login ran %d times, want 1", *calls)
	}
	env, _ := os.ReadFile(envDump)
	argv, _ := os.ReadFile(argvDump)
	var headers []string
	for _, line := range strings.Split(string(env), "\n") {
		if strings.HasPrefix(line, "ANTHROPIC_CUSTOM_HEADERS=") {
			headers = append(headers, line)
		}
		if strings.HasPrefix(line, "ANTHROPIC_AUTH_TOKEN=") {
			t.Errorf("the child has ANTHROPIC_AUTH_TOKEN")
		}
	}
	if len(headers) != 1 || !strings.Contains(headers[0], "x-wt-model-token: "+subTok) {
		t.Errorf("ANTHROPIC_CUSTOM_HEADERS lines = %d (%v), want one with the token line", len(headers), len(headers) > 0)
	}
	if len(argv) == 0 || strings.Contains(string(argv), subTok) {
		t.Errorf("argv is empty or holds the token (%d bytes)", len(argv))
	}

	// Nested: the parent's environment has a header-mode line. Neither the
	// bare native row nor a named native model (claude/opus, Claude Code's
	// own) may carry it on to Anthropic.
	t.Setenv("ANTHROPIC_CUSTOM_HEADERS", "x-wt-model-token: "+subTok)
	for _, id := range []string{"claude/native", "claude/opus"} {
		if err := os.Remove(envDump); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if err := r.launch(t, context.Background(), id, "", false); err != nil {
			t.Fatalf("%s launch: %v", id, err)
		}
		env, err := os.ReadFile(envDump)
		if err != nil {
			t.Fatalf("%s: no env dump: %v", id, err)
		}
		if strings.Contains(string(env), "x-wt-model-token") {
			t.Errorf("the nested %s launch carries the wt token line", id)
		}
	}
}

// TestSubscriptionLaunchWritesNoFileAndNoArgv proves header mode writes the
// wt token nowhere: with the token given only as WT_MODEL_TOKEN (no token
// file), a full subscription launch leaves no file holding the token bytes
// under the scratch worktree, the scratch HOME or the config home, and the
// agent's argv does not hold it either.
func TestSubscriptionLaunchWritesNoFileAndNoArgv(t *testing.T) {
	st := goodLogin()
	r, _ := subRig(t, true, true, &st, subRow("claude-haiku-5-5"))
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfgHome := os.Getenv("XDG_CONFIG_HOME")
	// The config names the url only; the token comes from the environment.
	body := "default_tag = \"code\"\n\n[model-server]\nurl = \"" + r.f.URL + "\"\nsubscription = true\n"
	if err := os.WriteFile(filepath.Join(cfgHome, "agent-wt", "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(cfgHome, "token")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WT_MODEL_TOKEN", subTok)
	// The agent's argv is dumped outside the walked directories.
	argvDump := filepath.Join(t.TempDir(), "argv")
	t.Setenv("WT_FAKE_ARGV_DUMP", argvDump)
	wt := t.TempDir()
	var err error
	captureFile(t, &os.Stdout, func() {
		err = launchFilteredRemote(context.Background(), r.a, "claude", wt, false, "", "", "claude-haiku-5-5", true, nil, "", false, nil)
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if argv, rerr := os.ReadFile(argvDump); rerr != nil || len(argv) == 0 || bytes.Contains(argv, []byte(subTok)) {
		t.Errorf("argv dump missing (%v), empty or holding the token", rerr)
	}
	for _, root := range []string{wt, home, cfgHome} {
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, werr error) error {
			if werr != nil || d.IsDir() {
				return nil
			}
			if b, rerr := os.ReadFile(p); rerr == nil && bytes.Contains(b, []byte(subTok)) {
				t.Errorf("%s holds the token", p)
			}
			return nil
		})
	}
}

// TestNonSubscriptionLaunchSkipsTheGuard proves an ordinary launch never
// runs the login check: the guard returns at once when no row uses the
// subscription, opted in or not.
func TestNonSubscriptionLaunchSkipsTheGuard(t *testing.T) {
	st := goodLogin()
	r, calls := subRig(t, false, false, &st, wireRow("openrouter/a", modelapi.ActionLaunch, 1))
	if err := r.launch(t, context.Background(), "openrouter/a", "", false); err != nil {
		t.Fatalf("launch: %v", err)
	}
	if *calls != 0 {
		t.Errorf("login ran %d times, want 0", *calls)
	}
}

// TestTUISubscriptionLaunchAfterPickerPing proves the TUI's guard
// (tuiSubscriptionGuard) reads the ping the picker's own first request made:
// openModels never ran on that path, and the cached ping's
// subscription_passthrough lets the launch through without a second ping.
func TestTUISubscriptionLaunchAfterPickerPing(t *testing.T) {
	st := goodLogin()
	r, calls := subRig(t, true, true, &st, subRow("claude-haiku-5-5"))
	ms, err := r.a.buildModels(launchStill)
	if err != nil {
		t.Fatal(err)
	}
	scfg, _ := r.a.sessionCfg()
	if _, err := tui.NewModelClient(ms.client, ms.ep).Catalog(context.Background()); err != nil {
		t.Fatalf("picker catalog: %v", err)
	}
	guard := tuiSubscriptionGuard(r.a, ms, scfg)
	if err := guard(context.Background(), []modelapi.Row{subRow("claude-haiku-5-5")}); err != nil {
		t.Fatalf("guard refused: %v", err)
	}
	if *calls != 1 {
		t.Errorf("login ran %d times, want 1", *calls)
	}
	if n := r.f.count("GET /control/v1/ping"); n != 1 {
		t.Errorf("pings = %d, want the picker's one", n)
	}
}

// TestLoginExpiringSoonNotes proves a login expiring within 24 h is one
// note (decision 8) and one further off says nothing.
func TestLoginExpiringSoonNotes(t *testing.T) {
	r, _ := subRig(t, true, true, nil)
	ms, err := r.a.openModels(context.Background(), launchStill)
	if err != nil {
		t.Fatal(err)
	}
	for d, want := range map[time.Duration]bool{2 * time.Hour: true, 72 * time.Hour: false} {
		st := goodLogin()
		st.ExpiresAt = time.Now().Add(d)
		var out bytes.Buffer
		login := func(context.Context) (claudelogin.Status, error) { return st, nil }
		if err := subscriptionGuardTo(context.Background(), &out, ms, []modelapi.Row{subRow("claude-haiku-5-5")}, nil, login); err != nil {
			t.Fatal(err)
		}
		got := strings.Count(out.String(), "wt: note: your claude.ai login expires at")
		if (got == 1) != want || got > 1 {
			t.Errorf("expiry in %v: notes %q", d, out.String())
		}
	}
}

// TestSubscriptionClaudeNotOnPathSaysSo proves a machine with no claude on
// PATH is told to install it (or put it on PATH), not to run
// `claude auth login`, a command it cannot run; nothing is registered.
func TestSubscriptionClaudeNotOnPathSaysSo(t *testing.T) {
	r, _ := subRig(t, true, true, nil, subRow("claude-haiku-5-5"))
	r.a.hooks.claudeLogin = realClaudeLogin
	t.Setenv("PATH", t.TempDir())
	err := r.launch(t, context.Background(), "claude-haiku-5-5", "", false)
	if err == nil || exitCodeOf(err) != 1 || !strings.Contains(err.Error(), "not found on PATH") || strings.Contains(err.Error(), "claude auth login") {
		t.Fatalf("err = %v, want the not-on-PATH refusal", err)
	}
	nothingRegistered(t, r.f)
}

// TestPickerGetsTheSubscriptionGuard pins main.go's wiring of the TUI guard
// (#597): the picker's ProfileLayer carries a Guard whenever a model server
// is configured, and that Guard is the real one: with a subscription row and
// no [model-server] subscription opt-in it refuses, and with no subscription
// row it passes without running the login check.
func TestPickerGetsTheSubscriptionGuard(t *testing.T) {
	st := goodLogin()
	r, calls := subRig(t, false, true, &st, subRow("claude-haiku-5-5"), wireRow("openrouter/a", modelapi.ActionLaunch, 1))
	oldTUI, oldGuard := tuiRun, maybeInstallGuard
	t.Cleanup(func() { tuiRun, maybeInstallGuard = oldTUI, oldGuard })
	maybeInstallGuard = func() {}
	var got tui.ProfileLayer
	tuiRun = func(_, _ bool, _ string, _, _, _, _ string, _ []string, _ themes.Theme, _ string, _ tui.ModelClient, _ *config.Config, pl tui.ProfileLayer, _ tui.SlotProfiles, _ tui.RemoveFuncsBuilder, _ []string) error {
		got = pl
		return nil
	}
	cmd := &cobra.Command{}
	cmd.Flags().String("model", "", "")
	cmd.Flags().Bool("yolo", false, "")
	if err := runLaunchPath(cmd, r.a, "", "", "", "", nil, "", ""); err != nil {
		t.Fatalf("runLaunchPath: %v", err)
	}
	if got.Guard == nil {
		t.Fatal("the picker's ProfileLayer has no Guard")
	}
	ctx := tui.WithGuardLaunch(context.Background(), tui.GuardLaunch{Agent: "claude"})
	if err := got.Guard(ctx, []modelapi.Row{subRow("claude-haiku-5-5")}); err == nil || !strings.Contains(err.Error(), "has not opted in") {
		t.Errorf("subscription row, no opt-in: err = %v, want the opt-in refusal", err)
	}
	if err := got.Guard(ctx, []modelapi.Row{wireRow("openrouter/a", modelapi.ActionLaunch, 1)}); err != nil {
		t.Errorf("ordinary row: err = %v, want nil", err)
	}
	if *calls != 0 {
		t.Errorf("the login check ran %d times, want 0", *calls)
	}
}

// TestSubscriptionSlotProfileKeysRefusedBeforeRegister proves a slot
// profile whose own env or config_content would undo header mode (here a
// top-level apiKeyHelper) is refused by the pre-launch guard, before the
// client session's register, any start and the login check, on the -M
// profile pin and through the TUI's guard closure, not only later by the
// slot build.
func TestSubscriptionSlotProfileKeysRefusedBeforeRegister(t *testing.T) {
	st := goodLogin()
	r, calls := subRig(t, true, true, &st, append(slotWireRows(), subRow("claude-haiku-5-5"))...)
	sp := profiles.SlotProfile{Name: "mix", Agent: "claude", Default: "sonnet",
		Slots:         map[string]string{"sonnet": "openrouter/a", "haiku": "claude-haiku-5-5"},
		ConfigContent: map[string]any{"apiKeyHelper": "/bin/echo VAL-SENTINEL"}}
	r.a.slotChecks = profiles.ValidateSlots(profiles.SlotStore{Profiles: []profiles.SlotProfile{sp}}, agents.SlotDrivers())
	if r.a.slotChecks[0].Err != nil {
		t.Fatalf("static validation refused the profile: %v", r.a.slotChecks[0].Err)
	}
	err := r.launch(t, context.Background(), "profile/mix", "", false)
	if err == nil || exitCodeOf(err) != 1 || !strings.Contains(err.Error(), "apiKeyHelper") || strings.Contains(err.Error(), "VAL-SENTINEL") {
		t.Fatalf("err = %v, want the guard's refusal naming apiKeyHelper", err)
	}
	if !strings.Contains(err.Error(), "nothing was started or registered") {
		t.Errorf("err = %v, want the guard's refusal (not the build's)", err)
	}
	nothingRegistered(t, r.f)
	if *calls != 0 {
		t.Errorf("the login check ran %d times, want 0", *calls)
	}

	ms, merr := r.a.openModels(context.Background(), launchStill)
	if merr != nil {
		t.Fatal(merr)
	}
	scfg, _ := r.a.sessionCfg()
	ctx := tui.WithGuardLaunch(context.Background(), tui.GuardLaunch{Agent: "claude", Slot: true, SlotProfile: &sp})
	gerr := tuiSubscriptionGuard(r.a, ms, scfg)(ctx, []modelapi.Row{wireRow("openrouter/a", modelapi.ActionLaunch, 1), subRow("claude-haiku-5-5")})
	if gerr == nil || !strings.Contains(gerr.Error(), "apiKeyHelper") {
		t.Errorf("TUI guard: err = %v, want the apiKeyHelper refusal", gerr)
	}
}

// TestHeaderModeFlagReachesTheAppliers pins the header-mode flag at the two
// non-TUI call sites (#597): the build's LaunchInfo.HeaderMode reaches
// runTail.HeaderMode (launchFilteredRemote) and applySlotProfile
// (launchSlotProfileRemote). The guard is out of the way (no row uses the
// subscription) and the build reports header mode, so only the applier can
// refuse: a matching launch profile setting ANTHROPIC_BASE_URL or
// ANTHROPIC_CUSTOM_HEADERS, and a slot profile with a top-level
// apiKeyHelper, must still stop the launch before the agent runs.
func TestHeaderModeFlagReachesTheAppliers(t *testing.T) {
	for _, key := range []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_CUSTOM_HEADERS"} {
		t.Run("single "+key, func(t *testing.T) {
			r := newRemoteRig(t, wireRow("openrouter/a", modelapi.ActionLaunch, 1))
			mark := filepath.Join(t.TempDir(), "ran")
			t.Setenv("WT_FAKE_MARK", mark)
			r.a.hooks.buildGateway = func(agent string, m config.Model, wt string, yolo bool, gw agents.Gateway, models []config.Model, extra []string) (*exec.Cmd, agents.LaunchInfo, error) {
				cmd, info, err := agents.BuildLaunchCmdGateway(agent, m, wt, yolo, gw, models, extra)
				info.HeaderMode = true
				return cmd, info, err
			}
			stubLaunchProfile(t, "openrouter/a", map[string]string{key: "x-litellm-tags: a"}, nil)
			var err error
			captureFile(t, &os.Stdout, func() {
				err = launchFilteredRemote(context.Background(), r.a, "claude", t.TempDir(), false, "", "", "openrouter/a", true, nil, "", false, nil)
			})
			if err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("err = %v, want the applier's refusal naming %s", err, key)
			}
			if _, serr := os.Stat(mark); serr == nil {
				t.Error("the agent ran")
			}
		})
	}
	t.Run("slot apiKeyHelper", func(t *testing.T) {
		r, _ := slotRig(t, map[string]string{"sonnet": "openrouter/a"})
		mark := filepath.Join(t.TempDir(), "ran")
		t.Setenv("WT_FAKE_MARK", mark)
		sp := profiles.SlotProfile{Name: "mix", Agent: "claude", Default: "sonnet", Slots: map[string]string{"sonnet": "openrouter/a"},
			ConfigContent: map[string]any{"apiKeyHelper": "/bin/echo x"}}
		r.a.slotChecks = profiles.ValidateSlots(profiles.SlotStore{Profiles: []profiles.SlotProfile{sp}}, agents.SlotDrivers())
		r.a.hooks.buildSlotGateway = func(sl profiles.SlotLaunch, wt string, yolo bool, gw agents.Gateway, extra []string) (*exec.Cmd, agents.LaunchInfo, error) {
			cmd, info, err := agents.BuildSlotLaunchCmdGatewayInfo(sl, wt, yolo, gw, extra)
			info.HeaderMode = true
			return cmd, info, err
		}
		err := r.launch(t, context.Background(), "profile/mix", "", false)
		if err == nil || !strings.Contains(err.Error(), "apiKeyHelper") {
			t.Fatalf("err = %v, want the slot applier's refusal", err)
		}
		if _, serr := os.Stat(mark); serr == nil {
			t.Error("the agent ran")
		}
	})
}
