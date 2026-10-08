package litellm

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// dupLocalConfig is a registry Config.Validate refuses and `wt litellm sync`
// still runs on: the id "dup/q" is on two rows, the first under ollama and
// the second under mtplx. A route's name is the model id, so the two rows can
// only ever share one route.
func dupLocalConfig() *config.Config {
	cfg := testConfig()
	cfg.Models = append(cfg.Models,
		config.Model{ID: "dup/q", ProviderID: "ollama", ModelName: "q:1b", Location: config.LocationLocal},
		config.Model{ID: "dup/q", ProviderID: "mtplx", ModelName: "org/Q", Location: config.LocationLocal},
	)
	return cfg
}

// dupModels returns cfg's rows with the id "dup/q", in registry order.
func dupModels(cfg *config.Config) []config.Model {
	var out []config.Model
	for _, m := range cfg.Models {
		if m.ID == "dup/q" {
			out = append(out, m)
		}
	}
	return out
}

// dupWarning is the one warning a sync gives about an id it will not route.
func dupWarning(times, providers string) string {
	return `model "dup/q" is in the registry ` + times + ` (providers ` + providers + `); its route is left as it is — fix the entry in ` + config.RegistryPath()
}

// routeOf returns the text of id's row in the config.yaml at p, or "".
func routeOf(t *testing.T, p, id string) string {
	t.Helper()
	f, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	row := f.row(id)
	if row == nil {
		return ""
	}
	var v map[string]any
	if err := row.Decode(&v); err != nil {
		t.Fatal(err)
	}
	params, _ := v["litellm_params"].(map[string]any)
	model, _ := params["model"].(string)
	base, _ := params["api_base"].(string)
	return model + " @ " + base
}

// TestSyncRoutesADuplicatedIDFromTheRowHandedIn verifies that when one of two
// registry rows sharing an id is running, the route is built from that row
// and no other: the first row's when the first provider serves it, the
// second's when the second does. Sync used to look the id up and take the
// first row for its checks, and then route every row with the id, the last
// one winning — so with the ollama model running, the route named mtplx's
// server and model, and an agent launched on the id talked to a server that
// was not serving it.
func TestSyncRoutesADuplicatedIDFromTheRowHandedIn(t *testing.T) {
	cfg := dupLocalConfig()
	rows := dupModels(cfg)
	for _, tc := range []struct {
		name  string
		local config.Model
		want  string
	}{
		{"the first row is running", rows[0], "ollama_chat/q:1b @ http://localhost:11434"},
		{"the second row is running", rows[1], "openai/org/Q @ http://localhost:8003/v1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, _, p := opts(t, "model_list: []\n")
			plan, err := PlanSync(cfg, []config.Model{tc.local}, o)
			if err != nil {
				t.Fatal(err)
			}
			if n := len(plan.Add) - len(slices.Compact(slices.Sorted(slices.Values(plan.Add)))); n != 0 {
				t.Errorf("plan.Add = %v names an id more than once", plan.Add)
			}
			if !slices.Contains(plan.Add, "dup/q") || len(plan.Warnings) != 0 {
				t.Errorf("plan: add = %v warnings = %v, want dup/q added and no warning", plan.Add, plan.Warnings)
			}
			res, err := Sync(cfg, []config.Model{tc.local}, o)
			if err != nil {
				t.Fatal(err)
			}
			if got := routeOf(t, p, "dup/q"); got != tc.want {
				t.Errorf("route = %q, want %q", got, tc.want)
			}
			if len(res.Warnings) != 0 {
				t.Errorf("warnings = %v, want none: one row is running, and it is the one routed", res.Warnings)
			}
			n := 0
			for _, oc := range res.Outcomes {
				if oc.ID == "dup/q" {
					n++
				}
			}
			if n != 1 {
				t.Errorf("%d outcomes for dup/q, want 1: %+v", n, res.Outcomes)
			}
		})
	}
}

// TestSyncFreezesAnIDWithTwoDesiredRows verifies that when more than one row
// with the same id is to be routed, sync routes none of them, leaves whatever
// route the id has exactly as it is — never removing it — and says so once,
// naming every provider that holds the id, in the real sync and in the dry
// run alike. One id can have one route, and with two servers behind it no
// route is right; before this the last row won without a word. The cases are
// the three ways two rows can both be desired: both local and running, both
// cloud (always desired), and a cloud row beside a running local one — and the
// one way a row can be undecided: a cloud row beside a local row sync holds
// back because its provider's probe did not succeed (Options.Untouched, in
// either row order). That local server may be serving the id, so the cloud
// row is not "the one row to route": routing it rewrote the route the frozen
// family was promised would stay, while the command said that provider's
// routes "were left unchanged".
func TestSyncFreezesAnIDWithTwoDesiredRows(t *testing.T) {
	cloudPair := testConfig()
	cloudPair.Models = append(cloudPair.Models,
		config.Model{ID: "dup/q", ProviderID: "openrouter", ModelName: "a/q", Location: config.LocationCloud},
		config.Model{ID: "dup/q", ProviderID: "openrouter", ModelName: "b/q", Location: config.LocationCloud},
	)
	mixed := testConfig()
	mixed.Models = append(mixed.Models,
		config.Model{ID: "dup/q", ProviderID: "openrouter", ModelName: "a/q", Location: config.LocationCloud},
		config.Model{ID: "dup/q", ProviderID: "ollama", ModelName: "q:1b", Location: config.LocationLocal},
		config.Model{ID: "dup/q", ProviderID: "mtplx", ModelName: "org/Q", Location: config.LocationLocal},
	)
	// mtplx's probe did not succeed: what runLitellmSync passes for it.
	cloudFirst := testConfig()
	cloudFirst.Models = append(cloudFirst.Models,
		config.Model{ID: "dup/q", ProviderID: "openrouter", ModelName: "a/q", Location: config.LocationCloud},
		config.Model{ID: "dup/q", ProviderID: "mtplx", ModelName: "org/Q", Location: config.LocationLocal},
	)
	localFirst := testConfig()
	localFirst.Models = append(localFirst.Models,
		config.Model{ID: "dup/q", ProviderID: "mtplx", ModelName: "org/Q", Location: config.LocationLocal},
		config.Model{ID: "dup/q", ProviderID: "openrouter", ModelName: "a/q", Location: config.LocationCloud},
	)
	local := dupLocalConfig()
	const existing = `model_list:
  - model_name: dup/q
    litellm_params: {model: openai/earlier, api_base: "http://localhost:1/v1"}
    model_info: {wt_managed: true}
`
	for _, tc := range []struct {
		name    string
		cfg     *config.Config
		localIn []config.Model
		// frozen: the id is in Options.Untouched and mtplx in
		// Options.UntouchedFamilies.
		frozen  bool
		warning string
	}{
		{"two local rows, both running", local, dupModels(local), false, dupWarning("twice", "ollama, mtplx")},
		{"two cloud rows", cloudPair, nil, false, dupWarning("twice", "openrouter, openrouter")},
		{"a cloud row and a running local row of three", mixed, dupModels(mixed)[1:2], false, dupWarning("3 times", "openrouter, ollama, mtplx")},
		{"a cloud row ahead of a local row whose probe failed", cloudFirst, nil, true, dupWarning("twice", "openrouter, mtplx")},
		{"a cloud row behind a local row whose probe failed", localFirst, nil, true, dupWarning("twice", "mtplx, openrouter")},
	} {
		for _, body := range []string{existing, "model_list: []\n"} {
			name := tc.name + "/no route yet"
			if body == existing {
				name = tc.name + "/a route is there"
			}
			t.Run(name, func(t *testing.T) {
				o, _, p := opts(t, body)
				if tc.frozen {
					o.Untouched = []string{"dup/q"}
					o.UntouchedFamilies = []string{"mtplx"}
				}
				plan, err := PlanSync(tc.cfg, tc.localIn, o)
				if err != nil {
					t.Fatal(err)
				}
				for _, ids := range [][]string{plan.Add, plan.Adopt, plan.Rewrite, plan.Remove} {
					if slices.Contains(ids, "dup/q") {
						t.Errorf("the dry run plans a change to dup/q: %+v", plan)
					}
				}
				if !slices.Equal(plan.Warnings, []string{tc.warning}) {
					t.Errorf("dry-run warnings:\n got %q\nwant %q", plan.Warnings, []string{tc.warning})
				}
				before := routeOf(t, p, "dup/q")
				res, err := Sync(tc.cfg, tc.localIn, o)
				if err != nil {
					t.Fatal(err)
				}
				if got := routeOf(t, p, "dup/q"); got != before {
					t.Errorf("route = %q, want it left as %q", got, before)
				}
				for _, oc := range res.Outcomes {
					if oc.ID == "dup/q" {
						t.Errorf("an outcome for dup/q: %+v", oc)
					}
				}
				if !slices.Equal(res.Warnings, []string{tc.warning}) {
					t.Errorf("warnings:\n got %q\nwant %q", res.Warnings, []string{tc.warning})
				}
				// The rest of the registry is still reconciled.
				if routeOf(t, p, "openrouter/x/y") == "" {
					t.Error("the cloud model beside the duplicated id lost its route")
				}
			})
		}
	}
}

// TestSyncRoutesADuplicatedIDWithOneDesiredRow verifies the freeze is for two
// rows that are both to be routed, not for a duplicated id as such: with a
// cloud row beside a local row that is not running, the cloud row is routed
// as it would be alone, and when neither of two local rows is running the
// stale route is removed. A duplicated id is a registry to repair, but a sync
// that can still tell which server the id names must go on naming it.
func TestSyncRoutesADuplicatedIDWithOneDesiredRow(t *testing.T) {
	cfg := testConfig()
	cfg.Models = append(cfg.Models,
		config.Model{ID: "dup/q", ProviderID: "ollama", ModelName: "q:1b", Location: config.LocationLocal},
		config.Model{ID: "dup/q", ProviderID: "openrouter", ModelName: "a/q", Location: config.LocationCloud},
	)
	o, _, p := opts(t, "model_list: []\n")
	// The under-lock recheck the command passes: no local model is desired.
	// It prunes an add of a local id that stopped meanwhile, and the id here
	// is a local row's too — but the row being added is the cloud one, which
	// no probe can un-desire. Pruned, the real sync left the id unrouted
	// after its own dry run had said "would route".
	o.Recheck = func() []string { return nil }
	plan, err := PlanSync(cfg, nil, o)
	if err != nil || !slices.Contains(plan.Add, "dup/q") {
		t.Fatalf("dry run: add = %v, err = %v, want dup/q", plan.Add, err)
	}
	res, err := Sync(cfg, nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if got := routeOf(t, p, "dup/q"); !strings.HasPrefix(got, "openrouter/a/q") || len(res.Warnings) != 0 {
		t.Errorf("route = %q warnings = %v, want the cloud row's route and no warning", got, res.Warnings)
	}

	local := dupLocalConfig()
	o, _, p = opts(t, "model_list:\n  - model_name: dup/q\n    litellm_params: {model: openai/org/Q, api_base: \"http://localhost:8003/v1\"}\n    model_info: {wt_managed: true}\n")
	res, err = Sync(local, nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if got := routeOf(t, p, "dup/q"); got != "" || len(res.Warnings) != 0 {
		t.Errorf("route = %q warnings = %v, want the stale route removed and no warning", got, res.Warnings)
	}
}

// TestSyncChecksTheCredentialsOfTheRowItRoutes verifies the credential check
// that precedes a row's build asks about the provider of the row being
// routed. It used to look the id up and ask about the first row's provider:
// with an unroutable openrouter row ahead of a running ollama row under the
// same id, the ollama route failed with openrouter's "secret_ref resolved
// empty" and the running model got no route.
func TestSyncChecksTheCredentialsOfTheRowItRoutes(t *testing.T) {
	cfg := testConfig()
	cfg.Providers[2].Auth.SecretRef = "WT_TEST_UNSET_SECRET_FOR_DUP"
	if err := os.Unsetenv("WT_TEST_UNSET_SECRET_FOR_DUP"); err != nil {
		t.Fatal(err)
	}
	cfg.Models = []config.Model{
		// A typo in the location: a registry gap, so sync does not route it.
		{ID: "dup/q", ProviderID: "openrouter", ModelName: "a/q", Location: "Cloud"},
		{ID: "dup/q", ProviderID: "ollama", ModelName: "q:1b", Location: config.LocationLocal},
	}
	o, _, p := opts(t, "model_list: []\n")
	res, err := Sync(cfg, cfg.Models[1:], o)
	if err != nil {
		t.Fatal(err)
	}
	for _, oc := range res.Outcomes {
		if oc.Err != nil {
			t.Errorf("outcome %s: %v", oc.ID, oc.Err)
		}
	}
	if got := routeOf(t, p, "dup/q"); got != "ollama_chat/q:1b @ http://localhost:11434" {
		t.Errorf("route = %q, want the running ollama row's", got)
	}
}
