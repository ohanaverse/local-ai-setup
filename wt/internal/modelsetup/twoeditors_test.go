package modelsetup

import (
	"context"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelmode"
)

// TestModeTabRemoteThenSetupTabLocalAgree pins that the two editors read and
// write one [model-server] table consistently: what the Mode tab (wt config)
// stores as remote, the Setup tab (wt-model setup) observes as remote and
// replaces with a plan that says so, never a half-configured mix.
func TestModeTabRemoteThenSetupTabLocalAgree(t *testing.T) {
	ctx := context.Background()
	// A host where wt-model is installed and the LiteLLM plist holds a key,
	// as TestApplyIsIdempotent sets it up.
	f := apEnv(t)
	f.env["PATH"] = plPATH
	f.files[plBinary], f.modes[plBinary] = []byte("bin"), 0o755
	f.plistInfo = litellm.ProxyPlistInfo{Path: plLiteLLM, Port: 4000, EnvNames: []string{"LITELLM_MASTER_KEY"}}
	env := f.Env()

	// 1. The Mode tab's path: modelmode alone, over the same machine.
	mst := modelmode.Observe(ctx, env.Mode(), modelmode.ObserveOptions{})
	p, err := modelmode.BuildPlan(mst, modelmode.Desired{Mode: modelmode.ModeRemote,
		URL: "https://gpu.example:8787", PastedToken: "tok-TWO-EDITORS", SaveUnchecked: true})
	if err != nil {
		t.Fatalf("the Mode tab's remote plan: %v", err)
	}
	if r := modelmode.Apply(ctx, env.Mode(), p, nil); r.Failed != nil || r.Err != nil {
		t.Fatalf("the Mode tab's apply failed at %+v: %v", r.Failed, r.Err)
	}

	// 2. The Setup tab's read sees what the Mode tab stored.
	st := Observe(ctx, env, ObserveOptions{})
	if st.Resolution.Mode != modelmode.ModeRemote || st.Config.Table.URL != "https://gpu.example:8787" {
		t.Fatalf("the Setup tab read mode %q url %q, want remote at the stored url", st.Resolution.Mode, st.Config.Table.URL)
	}

	// 3. The Setup tab's local plan replaces the table, and says so.
	lp, err := BuildPlan(st, Desired{Desired: modelmode.Desired{Mode: modelmode.ModeLocal}})
	if err != nil {
		t.Fatalf("the Setup tab's local plan: %v", err)
	}
	configs := 0
	for _, s := range lp.Steps {
		if s.Kind == StepConfig {
			configs++
		}
	}
	if configs != 1 || lp.Want.Mode != modelmode.ModeLocal {
		t.Fatalf("local plan: %d config steps, mode %q, want one and local:\n%s", configs, lp.Want.Mode, lp.Format())
	}
	text := lp.Format()
	const urlLine = twoEditorsURLLine
	if !strings.Contains(text, urlLine) {
		t.Errorf("local plan does not write [model-server] as %q:\n%s", urlLine, text)
	}
	if strings.Contains(text, "gpu.example") {
		t.Errorf("the remote url leaked into the local plan:\n%s", text)
	}
}

// twoEditorsURLLine is the url the local plan writes over a stored remote
// table, as planLocal produces it today: a change to which url wins shows here.
const twoEditorsURLLine = `write [model-server] in /Users/user_name/.config/agent-wt/config.toml: mode = "local", url = "http://127.0.0.1:8787", token_file = "/Users/user_name/.config/wt-model/server.token"`
