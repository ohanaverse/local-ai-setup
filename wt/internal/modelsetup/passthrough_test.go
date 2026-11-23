package modelsetup

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelmode"
)

// TestSetupNeverEnablesPassthrough pins the Global Constraints rule that no
// production path turns the host switch on: a local setup applied on a
// scratch home, with the real wt-model home writer (config.SetProxy) behind
// the pair step, leaves [gateway] absent when it was absent and exactly as
// written otherwise (an off switch, a wrong-typed value), and never on.
func TestSetupNeverEnablesPassthrough(t *testing.T) {
	for _, tc := range []struct {
		name, gateway string
		want          map[string]any
	}{
		{"absent", "", nil},
		{"off", "\n[gateway]\nsubscription_passthrough = false\n", map[string]any{"subscription_passthrough": false}},
		{"wrong type", "\n[gateway]\nsubscription_passthrough = \"true\"\n", map[string]any{"subscription_passthrough": "true"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("HOME", dir)
			t.Setenv("XDG_CONFIG_HOME", dir)
			home := config.ProxyHomePath()
			if err := os.MkdirAll(filepath.Dir(home), 0o755); err != nil {
				t.Fatal(err)
			}
			if tc.gateway != "" {
				if err := os.WriteFile(home, []byte(tc.gateway), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			f := apEnv(t)
			env := f.Env()
			env.Config = realConfig{}
			p := mustPlan(t, freshState(), Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}})
			if !hasStep(p, StepPair) {
				t.Fatalf("plan has no pair step: %v", stepKinds(p.Steps))
			}
			r := Apply(context.Background(), env, p, func(string) {})
			if r.Err != nil {
				t.Fatalf("Apply: failed %+v err %v", r.Failed, r.Err)
			}
			body, err := os.ReadFile(home)
			if err != nil {
				t.Fatalf("the pair step wrote no home config.toml: %v", err)
			}
			var doc map[string]map[string]any
			if _, err := toml.Decode(string(body), &doc); err != nil {
				t.Fatal(err)
			}
			got, present := doc["gateway"]
			if (tc.want == nil) == present {
				t.Fatalf("[gateway] present = %t after setup, want %t:\n%s", present, tc.want != nil, body)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("[gateway] %s = %#v, want %#v unchanged:\n%s", k, got[k], v, body)
				}
			}
			if gc, _, err := config.GatewaySettings(); err != nil || gc.SubscriptionPassthrough {
				t.Errorf("after setup GatewaySettings = %+v, %v; want off", gc, err)
			}
		})
	}
}

// hasStep reports whether p carries a step of kind k.
func hasStep(p Plan, k StepKind) bool {
	for _, s := range p.Steps {
		if s.Kind == k {
			return true
		}
	}
	return false
}
