package agents

import (
	"strings"
	"testing"
)

// TestNativeModelBuildsEveryDriverWithoutAGateway proves the native sentinel
// (a launch with no model server configured) builds each model-driven agent
// from a zero Gateway: no route, no gateway origin or token anywhere in the
// command's arguments or environment, and no error. A native launch that went
// through a route would either refuse for want of a gateway or hand the agent
// a token it was never given.
func TestNativeModelBuildsEveryDriverWithoutAGateway(t *testing.T) {
	for _, name := range []string{"claude", "codex", "copilot", "pi", "opencode", "agy"} {
		t.Run(name, func(t *testing.T) {
			fakeBin(t, name)
			m := NativeModel()
			if !m.Native || m.ID != "" {
				t.Fatalf("NativeModel() = %+v, want Native with no id", m)
			}
			if r := GatewayRoute(testGW(), m); r.Litellm || r.APIKey != "" || r.BaseOrigin != "" {
				t.Fatalf("GatewayRoute for the native model = %+v, want the zero Route", r)
			}
			cmd, _, err := BuildLaunchCmdGateway(name, m, t.TempDir(), false, Gateway{}, nil, nil)
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			all := strings.Join(append(append([]string{}, cmd.Args...), cmd.Env...), "\n")
			for _, bad := range []string{testGWToken, testGWOrigin, "--model"} {
				if strings.Contains(all, bad) {
					t.Errorf("native %s command carries %q:\n%s", name, bad, all)
				}
			}
		})
	}
}

// TestNativeNoteSaysWhatHappenedAndTheWayOut pins the stderr line a native
// launch prints: a wt: note naming the agent, that nothing is configured and
// the agent's own configuration decides the model, and the config key that
// turns wt-model on. The fallback must never be silent, and a refusal never
// carries this text.
func TestNativeNoteSaysWhatHappenedAndTheWayOut(t *testing.T) {
	got := NativeNote("claude")
	for _, want := range []string{"wt: note: ", "no model server is configured", "claude natively", "own configuration", "[model-server]"} {
		if !strings.Contains(got, want) {
			t.Errorf("NativeNote = %q, missing %q", got, want)
		}
	}
	if strings.Contains(got, "\n") {
		t.Errorf("NativeNote = %q, want one line", got)
	}
}
