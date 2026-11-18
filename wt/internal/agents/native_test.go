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

// TestNativeNoteNamesNativeModeAndWtConfig pins the stderr line a native
// launch prints, word for word: a bare wt: line (informational, style guide
// M4, not a "note:") saying the launch is native because native mode was
// chosen, naming the agent and that its own configuration decides the model,
// and pointing at wt config to change the mode. Since stage 3 native is an
// explicit choice, so the line no longer says "no model server is
// configured" (which is now the unset refusal's lead) or names config keys.
func TestNativeNoteNamesNativeModeAndWtConfig(t *testing.T) {
	got := NativeNote("claude")
	want := "wt: native mode — launching claude natively, with its own configuration; to choose models through wt-model, change the mode in wt config"
	if got != want {
		t.Errorf("NativeNote =\n  %q\nwant\n  %q", got, want)
	}
	if strings.Contains(got, "no model server is configured") {
		t.Errorf("NativeNote = %q carries the refusal lead", got)
	}
}

// TestNativeLaunchClearsNoEnvironment proves the zero-ID native launch (no
// model server configured) leaves the user's own ANTHROPIC_* and COPILOT_*
// variables alone, since they are the "own configuration" the native note
// promises, while a registered native model (claude/native) still clears the
// gateway variables so the subscription wins.
func TestNativeLaunchClearsNoEnvironment(t *testing.T) {
	builds := map[string]LaunchCmd{
		"claude":  claudeDriver{}.Build(NativeModel(), false, Route{}),
		"copilot": copilotDriver{}.Build(NativeModel(), false, Route{}),
	}
	for name, cmd := range builds {
		if len(cmd.ClearEnv) != 0 || len(cmd.Env) != 0 {
			t.Errorf("native %s launch touches the environment: clear=%v env=%v", name, cmd.ClearEnv, cmd.Env)
		}
	}
	t.Run("registered native model still clears", func(t *testing.T) {
		m := NativeModel()
		m.ID, m.ProviderID = "claude/native", "claude"
		if lc := (claudeDriver{}).Build(m, false, Route{}); len(lc.ClearEnv) == 0 {
			t.Error("a registered native model must clear the gateway variables")
		}
	})
}
