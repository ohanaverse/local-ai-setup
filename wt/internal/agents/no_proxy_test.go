package agents

import (
	"os"
	"slices"
	"testing"
)

// TestNoProxyEnv pins the proxy leak guard (#597): a claude header-mode
// launch over a plain http gateway adds the gateway's host to both NO_PROXY
// and no_proxy, appended to an inherited value with a comma (never
// replacing it) or created when absent, so an inherited HTTP_PROXY cannot
// carry the claude.ai login and the wt token in clear through a proxy. An
// IPv6 literal goes in both spellings (::1 and [::1]). Token mode, https,
// native (no origin) and other agents are left exactly as they were, and a
// host already listed is not added twice.
func TestNoProxyEnv(t *testing.T) {
	cases := []struct {
		name, agent, origin string
		header              bool
		env                 []string
		wantUpper, wantLow  []string // every NO_PROXY / no_proxy value, in order
	}{
		{"created", "claude", "http://127.0.0.1:4100", true, nil,
			[]string{"127.0.0.1"}, []string{"127.0.0.1"}},
		{"appended", "claude", "http://localhost:4100", true, []string{"NO_PROXY=corp.example,.internal", "no_proxy=corp.example"},
			[]string{"corp.example,.internal,localhost"}, []string{"corp.example,localhost"}},
		{"ipv6", "claude", "http://[::1]:4100", true, []string{"NO_PROXY=corp.example"},
			[]string{"corp.example,::1,[::1]"}, []string{"::1,[::1]"}},
		{"already listed", "claude", "http://127.0.0.1:4100", true, []string{"NO_PROXY=a, 127.0.0.1", "no_proxy=127.0.0.1"},
			[]string{"a, 127.0.0.1"}, []string{"127.0.0.1"}},
		{"token mode", "claude", "http://127.0.0.1:4100", false, []string{"NO_PROXY=corp.example"},
			[]string{"corp.example"}, nil},
		{"https", "claude", "https://gw.example:8787", true, []string{"NO_PROXY=corp.example"},
			[]string{"corp.example"}, nil},
		{"native", "claude", "", true, nil, nil, nil},
		{"pi", "pi", "http://127.0.0.1:4100", true, nil, nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := noProxyEnv(c.agent, c.origin, c.header, slices.Clone(c.env))
			if u := envValues(got, "NO_PROXY"); !slices.Equal(u, c.wantUpper) {
				t.Errorf("NO_PROXY = %q, want %q", u, c.wantUpper)
			}
			if l := envValues(got, "no_proxy"); !slices.Equal(l, c.wantLow) {
				t.Errorf("no_proxy = %q, want %q", l, c.wantLow)
			}
		})
	}
}

// TestHeaderModeLaunchesBypassTheProxyForTheGateway proves both launch paths
// apply noProxyEnv: a single-model header-mode claude launch and a
// header-mode slot launch over http://127.0.0.1 get the host appended to the
// inherited NO_PROXY and no_proxy; a token-mode launch of the same gateway
// keeps the inherited values unchanged.
func TestHeaderModeLaunchesBypassTheProxyForTheGateway(t *testing.T) {
	fakeBin(t, "claude")
	t.Setenv("NO_PROXY", "corp.example")
	t.Setenv("no_proxy", "")
	os.Unsetenv("no_proxy")
	gw := Gateway{Origin: "http://127.0.0.1:4100", Token: subTestToken}
	cmd, info, err := BuildLaunchCmdGateway("claude", subModel("claude-opus-5-5"), t.TempDir(), false, gw, nil, nil)
	if err != nil || !info.HeaderMode {
		t.Fatalf("build: %v (header mode %v)", err, info.HeaderMode)
	}
	if u, l := envValues(cmd.Env, "NO_PROXY"), envValues(cmd.Env, "no_proxy"); !slices.Equal(u, []string{"corp.example,127.0.0.1"}) || !slices.Equal(l, []string{"127.0.0.1"}) {
		t.Errorf("single-model: NO_PROXY %q no_proxy %q", u, l)
	}
	in := subSlotInput()
	cmd, info, err = BuildSlotLaunchCmdGatewayInfo(in.SlotLaunch, t.TempDir(), false, gw, nil)
	if err != nil || !info.HeaderMode {
		t.Fatalf("slot build: %v (header mode %v)", err, info.HeaderMode)
	}
	if u, l := envValues(cmd.Env, "NO_PROXY"), envValues(cmd.Env, "no_proxy"); !slices.Equal(u, []string{"corp.example,127.0.0.1"}) || !slices.Equal(l, []string{"127.0.0.1"}) {
		t.Errorf("slot: NO_PROXY %q no_proxy %q", u, l)
	}
	cmd, info, err = BuildLaunchCmdGateway("claude", gwCloudModel(), t.TempDir(), false, gw, nil, nil)
	if err != nil || info.HeaderMode {
		t.Fatalf("token build: %v (header mode %v)", err, info.HeaderMode)
	}
	if u, l := envValues(cmd.Env, "NO_PROXY"), envValues(cmd.Env, "no_proxy"); !slices.Equal(u, []string{"corp.example"}) || len(l) != 0 {
		t.Errorf("token mode: NO_PROXY %q no_proxy %q, want unchanged", u, l)
	}
}
