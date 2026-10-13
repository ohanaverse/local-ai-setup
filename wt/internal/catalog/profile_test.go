// Tests for slot-profile rows: resolved validation (the rules that need the
// registry, the live inventory and the [litellm] table), the row Build emits
// for a profile, and what keeps those rows away from callers that did not ask
// for them.
package catalog

import (
	"errors"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)

// TestProfileRowAnswersFromItsVerdict verifies that a row carrying a profile
// reports the profile's verdict and reason, not the location-and-provider
// answer a model row gives. A profile row has no location, so without this
// every profile would read as a launch row and a blocked one would start
// Claude Code with slots that cannot work.
func TestProfileRowAnswersFromItsVerdict(t *testing.T) {
	blocked := Row{Model: config.Model{ID: "profile/x"}, Profile: &ProfileRow{Verdict: ActionBlock, Reason: "slot haiku: nope"}}
	if !blocked.IsProfile() || blocked.Action() != ActionBlock || blocked.BlockReason() != "slot haiku: nope" {
		t.Errorf("blocked profile row: IsProfile=%v action=%v reason=%q", blocked.IsProfile(), blocked.Action(), blocked.BlockReason())
	}
	start := Row{Model: config.Model{ID: "profile/y"}, Profile: &ProfileRow{Verdict: ActionStart}}
	if start.Action() != ActionStart || start.BlockReason() != "" {
		t.Errorf("start profile row: action=%v reason=%q", start.Action(), start.BlockReason())
	}
	model := Row{Location: config.LocationCloud, Model: config.Model{ID: "openrouter/a"}}
	if model.IsProfile() || model.Action() != ActionLaunch {
		t.Errorf("model row: IsProfile=%v action=%v, want an ordinary launch row", model.IsProfile(), model.Action())
	}
}

// slotDeclared are the slots the test agent declares, in display order.
var slotDeclared = []string{"fable", "opus", "sonnet", "haiku"}

// slotTestCfg is catalogTestCfg plus the providers and models the resolved
// rules need, with [litellm] configured. The ids are made up.
func slotTestCfg() *config.Config {
	cfg := catalogTestCfg()
	cfg.Providers = append(cfg.Providers,
		config.Provider{ID: "mlx_lm_server", Location: config.LocationLocal},
		config.Provider{ID: "claude", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "native"}},
		config.Provider{ID: "nopolicy", Location: config.LocationCloud},
	)
	cfg.Models = []config.Model{
		{ID: "openrouter/a", ProviderID: "openrouter", ModelName: "a"},
		{ID: "openrouter/b", ProviderID: "openrouter", ModelName: "b"},
		{ID: "omlx/q", ProviderID: "omlx", ModelName: "q"},
		{ID: "ollama/g", ProviderID: "ollama", ModelName: "g"},
		{ID: "mlx_lm_server/p", ProviderID: "mlx_lm_server", ModelName: "p"},
		{ID: "claude/native", ProviderID: "claude", ModelName: "native", Native: true},
		{ID: "nopolicy/x", ProviderID: "nopolicy", ModelName: "x"},
		{ID: "ghost/x", ProviderID: "ghost", ModelName: "x"},
		{ID: "openrouter/badloc", ProviderID: "openrouter", ModelName: "badloc", Location: "clod"},
	}
	cfg.SetLitellmForTest(config.LitellmState{URL: "http://localhost:4000", APIKey: "sk-test"})
	return cfg
}

// slotCheck is a profile for agent "claude" that passed static validation.
func slotCheck(index int, name, def string, slots map[string]string) profiles.SlotCheck {
	return profiles.SlotCheck{
		Index:    index,
		Profile:  profiles.SlotProfile{Name: name, Agent: "claude", Default: def, Slots: slots},
		Usable:   true,
		Declared: slotDeclared,
	}
}

// TestResolveSlotProfileBlockReasons pins each resolved rule that needs no
// probe, one case per message: a static error carried through, an id that is
// not in the registry, a native model, a provider missing from the registry,
// a location that does not resolve, a provider LiteLLM cannot route, and two
// distinct local models. The reason is the only thing the picker and a -M pin
// show for a profile that cannot launch, so each must name the slot and the
// cause. The last two cases pin the order: a rule is checked over every slot
// before the next rule runs.
func TestResolveSlotProfileBlockReasons(t *testing.T) {
	static := slotCheck(0, "broken", "", map[string]string{"opus": "openrouter/a"})
	static.Err = errors.New("default is missing")
	cases := []struct {
		name  string
		check profiles.SlotCheck
		want  string
	}{
		{"static error", static, "default is missing"},
		{"id not in the registry", slotCheck(0, "p", "opus", map[string]string{"opus": "openrouter/a", "haiku": "ollama/gone"}),
			"slot haiku: ollama/gone is not in the registry"},
		{"native model", slotCheck(0, "p", "opus", map[string]string{"opus": "claude/native"}),
			"slot opus: claude/native is a native model, which has no LiteLLM route"},
		{"provider not in the registry", slotCheck(0, "p", "opus", map[string]string{"opus": "ghost/x"}),
			`slot opus: ghost/x names provider "ghost", which is not in the registry`},
		{"location does not resolve", slotCheck(0, "p", "opus", map[string]string{"opus": "openrouter/badloc"}),
			`slot opus: model "openrouter/badloc" has location "clod"; expected "local" or "cloud"`},
		{"provider without a route policy", slotCheck(0, "p", "opus", map[string]string{"opus": "nopolicy/x"}),
			`slot opus: nopolicy/x cannot be routed: provider "nopolicy" has no LiteLLM mapping`},
		{"two local models", slotCheck(0, "p", "opus", map[string]string{"opus": "omlx/q", "sonnet": "openrouter/a", "haiku": "ollama/g"}),
			"slots name two local models (omlx/q and ollama/g); a slot profile may name one"},
		{"registry rule runs over every slot before the native rule", slotCheck(0, "p", "opus", map[string]string{"opus": "claude/native", "haiku": "ollama/gone"}),
			"slot haiku: ollama/gone is not in the registry"},
		{"native rule runs over every slot before the policy rule", slotCheck(0, "p", "opus", map[string]string{"opus": "nopolicy/x", "haiku": "claude/native"}),
			"slot haiku: claude/native is a native model, which has no LiteLLM route"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := ResolveSlotProfile(slotTestCfg(), nil, tc.check)
			if row.Action() != ActionBlock || row.BlockReason() != tc.want {
				t.Errorf("action = %v, reason = %q\nwant ActionBlock, %q", row.Action(), row.BlockReason(), tc.want)
			}
			if row.Model.ID != "profile/"+tc.check.Profile.Name || row.Profile == nil {
				t.Errorf("row id = %q, profile set = %v", row.Model.ID, row.Profile != nil)
			}
		})
	}
	// A static failure resolves nothing: no models, no location.
	row := ResolveSlotProfile(slotTestCfg(), nil, static)
	if row.Profile.Models != nil || row.Profile.Loc != "" {
		t.Errorf("static failure: models = %v, loc = %q, want none", row.Profile.Models, row.Profile.Loc)
	}
}

// TestResolveSlotProfileNeedsLitellmConfiguredNotEnabled verifies the proxy
// rule: a slot profile needs the [litellm] url and api key, and never reads
// the on/off toggle. With both set an all-cloud profile is a launch row
// whether routing is on or off; with either one empty the row is blocked with
// the `wt litellm set` wording, again whatever the toggle says. Reading the
// toggle would hide every profile from a user who dials providers directly
// for single-model launches, and skipping the key check would launch Claude
// Code with an empty token.
func TestResolveSlotProfileNeedsLitellmConfiguredNotEnabled(t *testing.T) {
	check := slotCheck(0, "cloudy", "sonnet", map[string]string{"sonnet": "openrouter/a", "opus": "openrouter/b"})
	const noURL = "a slot profile always uses the LiteLLM proxy, but no URL is configured — run 'wt litellm set --url ... --api-key ...'"
	const noKey = "a slot profile always uses the LiteLLM proxy, but no API key is configured — run 'wt litellm set --url ... --api-key ...'"
	cases := []struct {
		name       string
		url, key   string
		wantAction Action
		wantReason string
	}{
		{"url and key", "http://localhost:4000", "sk-test", ActionLaunch, ""},
		{"no key", "http://localhost:4000", "", ActionBlock, noKey},
		{"no url", "", "sk-test", ActionBlock, noURL},
		{"neither", "", "", ActionBlock, noURL},
	}
	for _, tc := range cases {
		for _, enabled := range []bool{false, true} {
			cfg := slotTestCfg()
			cfg.SetLitellmForTest(config.LitellmState{Enabled: enabled, URL: tc.url, APIKey: tc.key})
			row := ResolveSlotProfile(cfg, nil, check)
			if row.Action() != tc.wantAction || row.BlockReason() != tc.wantReason {
				t.Errorf("%s, enabled=%v: action = %v, reason = %q\nwant %v, %q", tc.name, enabled, row.Action(), row.BlockReason(), tc.wantAction, tc.wantReason)
			}
		}
	}
}

// TestResolveSlotProfileLocalModelVerdict pins the local-model verdict table:
// the profile's one local model is judged by the row the catalog builds for
// it from the same snapshot. No row blocks the profile (with MissingReason's
// wording when the probe can say why), a row wt cannot start blocks it with
// the slot named, an idle, loading or unknown-presence model makes it a start
// row, and a running one a launch row. A wrong answer here either launches
// Claude Code against a model that is not serving or refuses a profile wt
// could have started.
func TestResolveSlotProfileLocalModelVerdict(t *testing.T) {
	entries := func(e ...localmodels.Entry) *localmodels.Snapshot {
		return &localmodels.Snapshot{Entries: e}
	}
	cases := []struct {
		name        string
		local       string
		snap        *localmodels.Snapshot
		wantAction  Action
		wantReason  string
		wantLocal   bool
		wantRunning bool
		wantLoading bool
	}{
		{name: "confirmed not on disk", local: "omlx/q",
			snap:       entries(localmodels.Entry{ProviderID: "omlx", ModelID: "omlx/q", Registered: true, ArtifactKnown: true}),
			wantAction: ActionBlock, wantReason: "omlx/q is not on disk — pull or download it first"},
		{name: "stopped mlx_lm_server pairing", local: "mlx_lm_server/p",
			snap:       entries(localmodels.Entry{ProviderID: "mlx_lm_server", ModelID: "mlx_lm_server/p", Registered: true}),
			wantAction: ActionBlock, wantReason: "local model \"mlx_lm_server/p\" is not running, and wt cannot start an mlx_lm_server pairing — start it with `llmbench provider isolate --solo mlx_lm_server <target> --draft <draft>`"},
		{name: "no inventory entry", local: "omlx/q",
			snap:       entries(),
			wantAction: ActionBlock, wantReason: "slot haiku: omlx/q is not on disk or running"},
		{name: "a row wt cannot start", local: "mlx_lm_server/p",
			snap:       nil,
			wantAction: ActionBlock, wantReason: "slot haiku: local model \"mlx_lm_server/p\" is not running, and wt cannot start an mlx_lm_server pairing — start it with `llmbench provider isolate --solo mlx_lm_server <target> --draft <draft>`",
			wantLocal: true},
		{name: "on disk, not running", local: "omlx/q",
			snap:       entries(localmodels.Entry{ProviderID: "omlx", ModelID: "omlx/q", Artifact: "q", Registered: true, ArtifactKnown: true}),
			wantAction: ActionStart, wantLocal: true},
		{name: "still loading", local: "omlx/q",
			snap:       entries(localmodels.Entry{ProviderID: "omlx", ModelID: "omlx/q", Artifact: "q", Registered: true, ArtifactKnown: true, Running: true, Loading: true}),
			wantAction: ActionStart, wantLocal: true, wantRunning: true, wantLoading: true},
		{name: "presence unknown", local: "ollama/g",
			snap:       entries(localmodels.Entry{ProviderID: "ollama", ModelID: "ollama/g", Registered: true}),
			wantAction: ActionStart, wantLocal: true},
		{name: "no probe ran", local: "omlx/q",
			snap:       nil,
			wantAction: ActionStart, wantLocal: true},
		{name: "running", local: "omlx/q",
			snap:       entries(localmodels.Entry{ProviderID: "omlx", ModelID: "omlx/q", Artifact: "q", Registered: true, ArtifactKnown: true, Running: true}),
			wantAction: ActionLaunch, wantLocal: true, wantRunning: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			check := slotCheck(0, "mixed", "sonnet", map[string]string{"sonnet": "openrouter/a", "haiku": tc.local})
			row := ResolveSlotProfile(slotTestCfg(), tc.snap, check)
			if row.Action() != tc.wantAction || row.BlockReason() != tc.wantReason {
				t.Errorf("action = %v, reason = %q\nwant %v, %q", row.Action(), row.BlockReason(), tc.wantAction, tc.wantReason)
			}
			if got := row.Profile.Local != nil; got != tc.wantLocal {
				t.Errorf("Local set = %v, want %v", got, tc.wantLocal)
			}
			if tc.wantLocal && row.Profile.Local.Model.ID != tc.local {
				t.Errorf("Local = %q, want %q", row.Profile.Local.Model.ID, tc.local)
			}
			if row.Running != tc.wantRunning || row.Loading != tc.wantLoading {
				t.Errorf("Running = %v, Loading = %v, want %v, %v", row.Running, row.Loading, tc.wantRunning, tc.wantLoading)
			}
			if row.Profile.Loc != "mixed" {
				t.Errorf("Loc = %q, want mixed", row.Profile.Loc)
			}
		})
	}

	// An all-cloud profile has no local model to judge: it launches.
	cloud := ResolveSlotProfile(slotTestCfg(), entries(), slotCheck(0, "cloudy", "sonnet", map[string]string{"sonnet": "openrouter/a"}))
	if cloud.Action() != ActionLaunch || cloud.Profile.Local != nil || cloud.Running || cloud.Profile.Loc != "cloud" {
		t.Errorf("all-cloud profile: action = %v, Local set = %v, Running = %v, Loc = %q", cloud.Action(), cloud.Profile.Local != nil, cloud.Running, cloud.Profile.Loc)
	}
	// One local model may fill every slot the profile fills.
	local := ResolveSlotProfile(slotTestCfg(), nil, slotCheck(0, "localonly", "haiku", map[string]string{"haiku": "omlx/q", "opus": "omlx/q"}))
	if local.Action() != ActionStart || local.Profile.Loc != "local" {
		t.Errorf("all-local profile: action = %v, Loc = %q, want ActionStart, local", local.Action(), local.Profile.Loc)
	}
}

// TestResolveSlotProfileFillsEverySlot verifies the row a launchable profile
// produces: every slot the agent declares has a model, an unfilled slot
// holding the default slot's model, so no slot is left to send an Anthropic
// id the proxy rejects. It also pins the helpers the launch flow reads the
// row through: Distinct (each model once, the default's first), Cloud (the
// models whose routes are written without a start), SlotOf and Launch.
func TestResolveSlotProfileFillsEverySlot(t *testing.T) {
	check := slotCheck(0, "mixed", "sonnet", map[string]string{"opus": "openrouter/b", "sonnet": "openrouter/a", "haiku": "omlx/q"})
	check.Profile.Env = map[string]string{"MAX_THINKING_TOKENS": "4096"}
	check.Profile.ConfigContent = map[string]any{"modelOverrides": map[string]any{"x": "y"}}
	row := ResolveSlotProfile(slotTestCfg(), nil, check)
	p := row.Profile
	if row.Action() != ActionStart {
		t.Fatalf("action = %v (%s), want ActionStart", row.Action(), row.BlockReason())
	}
	want := map[string]string{"fable": "openrouter/a", "opus": "openrouter/b", "sonnet": "openrouter/a", "haiku": "omlx/q"}
	if len(p.Models) != len(want) {
		t.Fatalf("Models has %d slots, want %d: %v", len(p.Models), len(want), p.Models)
	}
	for slot, id := range want {
		if p.Models[slot].ID != id {
			t.Errorf("slot %s = %q, want %q", slot, p.Models[slot].ID, id)
		}
	}
	if p.Models["sonnet"].ModelName != "a" || p.Models["sonnet"].ProviderID != "openrouter" {
		t.Errorf("slot sonnet = %+v, want the whole registry model", p.Models["sonnet"])
	}
	if got := strings.Join(rowModelIDs(p.Distinct()), ","); got != "openrouter/a,openrouter/b,omlx/q" {
		t.Errorf("Distinct = %s", got)
	}
	if got := strings.Join(rowModelIDs(p.Cloud()), ","); got != "openrouter/a,openrouter/b" {
		t.Errorf("Cloud = %s", got)
	}
	if p.SlotOf("omlx/q") != "haiku" || p.SlotOf("openrouter/a") != "sonnet" || p.SlotOf("nope/x") != "" {
		t.Errorf("SlotOf: omlx/q=%q openrouter/a=%q nope/x=%q", p.SlotOf("omlx/q"), p.SlotOf("openrouter/a"), p.SlotOf("nope/x"))
	}
	// The row itself holds the id and nothing a model row would: no location,
	// no provider, so no caller can route or start "profile/mixed".
	if row.Model.ID != "profile/mixed" || row.Model.ProviderID != "" || row.Model.ModelName != "" || row.Location != "" || row.Status != StatusOK || row.Discovered || row.Unmapped {
		t.Errorf("row = %+v", row)
	}

	sl := p.Launch()
	if sl.Agent != "claude" || sl.Default != "sonnet" || len(sl.Models) != 4 || sl.Models["fable"].ID != "openrouter/a" {
		t.Errorf("Launch = %+v", sl)
	}
	if sl.Env["MAX_THINKING_TOKENS"] != "4096" || sl.ConfigContent["modelOverrides"] == nil {
		t.Errorf("Launch env = %v, config_content = %v", sl.Env, sl.ConfigContent)
	}
	sl.Models["fable"] = config.Model{ID: "changed"}
	if p.Models["fable"].ID != "openrouter/a" {
		t.Error("Launch().Models is the row's own map: a build could change the row")
	}

	// One local model in two slots is one distinct model, named by its first slot.
	twice := ResolveSlotProfile(slotTestCfg(), nil, slotCheck(0, "twice", "opus", map[string]string{"opus": "omlx/q", "haiku": "omlx/q", "sonnet": "openrouter/a"})).Profile
	if got := strings.Join(rowModelIDs(twice.Distinct()), ","); got != "omlx/q,openrouter/a" {
		t.Errorf("Distinct with a model in two slots = %s", got)
	}
	if got := strings.Join(rowModelIDs(twice.Cloud()), ","); got != "openrouter/a" {
		t.Errorf("Cloud with a local default = %s", got)
	}
	if twice.SlotOf("omlx/q") != "opus" {
		t.Errorf("SlotOf(omlx/q) = %q, want the default slot opus", twice.SlotOf("omlx/q"))
	}
}

// rowModelIDs lists the ids of models, in order.
func rowModelIDs(models []config.Model) []string {
	ids := make([]string, len(models))
	for i, m := range models {
		ids[i] = m.ID
	}
	return ids
}

// TestBuildEmitsProfileRowsOnlyOnRequest verifies who sees profile rows. Build
// emits them only when its input carries the slot profiles, only for the
// input's agent, and never under -T/-F (HideDiscovered). `wt start` and
// `wt smoke` build their catalogs without that input, so a profile can never
// be started, smoke-tested or rotated to as if it were a model.
func TestBuildEmitsProfileRowsOnlyOnRequest(t *testing.T) {
	cfg := slotTestCfg()
	models := []config.Model{cfg.Models[0], cfg.Models[2]} // openrouter/a, omlx/q
	inv := &localmodels.Snapshot{Entries: []localmodels.Entry{
		{ProviderID: "omlx", ModelID: "omlx/q", Artifact: "q", Registered: true, ArtifactKnown: true, Running: true},
		{ProviderID: "omlx", ModelID: "omlx/disc", Artifact: "disc", ArtifactKnown: true},
	}}
	other := slotCheck(1, "for-codex", "opus", map[string]string{"opus": "openrouter/a"})
	other.Profile.Agent = "codex"
	unusable := profiles.SlotCheck{Index: 2, Profile: profiles.SlotProfile{Name: "Bad Name", Agent: "claude"}, Err: errors.New(`name "Bad Name" is malformed: use lowercase letters, digits and hyphens`)}
	checks := []profiles.SlotCheck{
		slotCheck(0, "mixed", "sonnet", map[string]string{"sonnet": "openrouter/a", "haiku": "omlx/q"}),
		other,
		unusable,
		slotCheck(3, "dangling", "opus", map[string]string{"opus": "ollama/gone"}),
	}
	const modelRows = "openrouter/a,omlx/q,omlx/disc"
	cases := []struct {
		name string
		in   Input
		want string
	}{
		{"no slot profiles in the input", Input{Config: cfg, Agent: "claude", Models: models, Inventory: inv}, modelRows},
		{"opted in", Input{Config: cfg, Agent: "claude", Models: models, Inventory: inv, SlotProfiles: checks}, modelRows + ",profile/mixed,profile/dangling"},
		{"another agent's profile", Input{Config: cfg, Agent: "codex", Models: models, Inventory: inv, SlotProfiles: checks}, "openrouter/a,omlx/q,profile/for-codex"},
		{"no agent", Input{Config: cfg, Models: models, Inventory: inv, SlotProfiles: checks}, modelRows},
		{"under -T or -F", Input{Config: cfg, Agent: "claude", Models: models, Inventory: inv, HideDiscovered: true, SlotProfiles: checks}, "openrouter/a,omlx/q"},
		{"no probe ran", Input{Config: cfg, Agent: "claude", Models: models, SlotProfiles: checks}, "openrouter/a,omlx/q,profile/mixed,profile/dangling"},
	}
	for _, tc := range cases {
		if got := strings.Join(rowIDs(Build(tc.in)), ","); got != tc.want {
			t.Errorf("%s: rows = %s\nwant %s", tc.name, got, tc.want)
		}
	}

	// The rows carry resolved validation's verdict against Build's own snapshot:
	// the running local model makes "mixed" a launch row, and a slot left
	// dangling blocks its own row without touching the other.
	rows := Build(Input{Config: cfg, Agent: "claude", Models: models, Inventory: inv, SlotProfiles: checks})
	mixed, _ := Find(rows, "profile/mixed")
	if mixed.Action() != ActionLaunch || !mixed.Running || mixed.Profile.Check.Index != 0 {
		t.Errorf("profile/mixed: action = %v (%s), Running = %v, index = %d", mixed.Action(), mixed.BlockReason(), mixed.Running, mixed.Profile.Check.Index)
	}
	dangling, _ := Find(rows, "profile/dangling")
	if dangling.Action() != ActionBlock || dangling.BlockReason() != "slot opus: ollama/gone is not in the registry" {
		t.Errorf("profile/dangling: action = %v, reason = %q", dangling.Action(), dangling.BlockReason())
	}
}

// TestBuildCollapsesDuplicateProfileNames verifies that profiles sharing a
// name for one agent become a single blocked row, `duplicate profile name`,
// in the first one's place. Row ids stay unique, so the picker, a -M pin and
// the usage counts can never choose between two profiles of one name. The
// same name under another agent is a different profile and is untouched.
func TestBuildCollapsesDuplicateProfileNames(t *testing.T) {
	dup := func(index int, slots map[string]string) profiles.SlotCheck {
		c := slotCheck(index, "twin", "opus", slots)
		c.Duplicate, c.Err = true, profiles.ErrSlotDuplicate
		return c
	}
	codex := slotCheck(2, "twin", "opus", map[string]string{"opus": "openrouter/a"})
	codex.Profile.Agent = "codex"
	checks := []profiles.SlotCheck{
		dup(0, map[string]string{"opus": "openrouter/a"}),
		slotCheck(1, "solo", "opus", map[string]string{"opus": "openrouter/a"}),
		codex,
		dup(3, map[string]string{"opus": "openrouter/b"}),
	}
	rows := Build(Input{Config: slotTestCfg(), Agent: "claude", SlotProfiles: checks})
	if got := strings.Join(rowIDs(rows), ","); got != "profile/twin,profile/solo" {
		t.Fatalf("rows = %s, want profile/twin,profile/solo", got)
	}
	if rows[0].Action() != ActionBlock || rows[0].BlockReason() != "duplicate profile name" {
		t.Errorf("profile/twin: action = %v, reason = %q", rows[0].Action(), rows[0].BlockReason())
	}
	if rows[1].Action() != ActionLaunch {
		t.Errorf("profile/solo: action = %v (%s), want ActionLaunch", rows[1].Action(), rows[1].BlockReason())
	}
	other := Build(Input{Config: slotTestCfg(), Agent: "codex", SlotProfiles: checks})
	if len(other) != 1 || other[0].Model.ID != "profile/twin" || other[0].Action() != ActionLaunch {
		t.Errorf("codex rows = %v, want its own launchable profile/twin", rowIDs(other))
	}
}

// TestBuildYieldsToAProfileProvider verifies the reserved prefix: when the
// registry has a provider with id "profile", its models already own ids of
// the form profile/<name>, so Build emits no profile rows at all and the
// registry rows keep their ids. Emitting both would give two rows one id,
// and a -M pin would launch whichever came first.
func TestBuildYieldsToAProfileProvider(t *testing.T) {
	cfg := slotTestCfg()
	checks := []profiles.SlotCheck{slotCheck(0, "mixed", "sonnet", map[string]string{"sonnet": "openrouter/a"})}
	if SlotPrefixReserved(cfg) || SlotPrefixReserved(nil) {
		t.Fatal("SlotPrefixReserved is true for a registry with no profile provider, or for no config")
	}
	cfg.Providers = append(cfg.Providers, config.Provider{ID: "profile", Location: config.LocationCloud})
	registryModel := config.Model{ID: "profile/mixed", ProviderID: "profile", ModelName: "mixed"}
	cfg.Models = append(cfg.Models, registryModel)
	if !SlotPrefixReserved(cfg) {
		t.Fatal("SlotPrefixReserved is false for a registry with a profile provider")
	}
	rows := Build(Input{Config: cfg, Agent: "claude", Models: []config.Model{registryModel}, SlotProfiles: checks})
	if len(rows) != 1 || rows[0].IsProfile() || rows[0].Model.ProviderID != "profile" {
		t.Errorf("rows = %+v, want the one registry row and no profile row", rows)
	}
	if r, ok := Find(rows, "profile/mixed"); !ok || r.IsProfile() {
		t.Errorf("Find(profile/mixed): found = %v, IsProfile = %v, want the registry model's row", ok, r.IsProfile())
	}
}

// TestBuildYieldsToAProfilePrefixedModel verifies the reserved prefix for a
// model: registry validation does not tie a model's id to its provider, so a
// hand-written row can have the id profile/<name> under any provider. It is
// treated as a provider named "profile" is: no profile row is built, so the
// id stays the model's alone. With both rows, catalog.Find (first match) and
// the TUI's id index (last match) would launch different things for one pin.
func TestBuildYieldsToAProfilePrefixedModel(t *testing.T) {
	cfg := slotTestCfg()
	checks := []profiles.SlotCheck{slotCheck(0, "mixed", "sonnet", map[string]string{"sonnet": "openrouter/a"})}
	if got := SlotPrefixHolder(cfg); got != "" {
		t.Fatalf("SlotPrefixHolder = %q for a registry that leaves the prefix free", got)
	}
	registryModel := config.Model{ID: "profile/mixed", ProviderID: "ollama", ModelName: "mixed"}
	cfg.Models = append(cfg.Models, registryModel)
	if !SlotPrefixReserved(cfg) {
		t.Fatal("SlotPrefixReserved is false for a registry with a model id that begins with profile/")
	}
	if got, want := SlotPrefixHolder(cfg), `a model whose id begins with "profile/" (profile/mixed)`; got != want {
		t.Errorf("SlotPrefixHolder = %q, want %q", got, want)
	}
	rows := Build(Input{Config: cfg, Agent: "claude", Models: []config.Model{registryModel}, SlotProfiles: checks})
	if len(rows) != 1 || rows[0].IsProfile() || rows[0].Model.ProviderID != "ollama" {
		t.Errorf("rows = %+v, want the one registry row and no profile row", rows)
	}
	if r, ok := Find(rows, "profile/mixed"); !ok || r.IsProfile() {
		t.Errorf("Find(profile/mixed): found = %v, IsProfile = %v, want the registry model's row", ok, r.IsProfile())
	}

	// A provider with the id holds the prefix as before, and is named first.
	cfg.Providers = append(cfg.Providers, config.Provider{ID: "profile", Location: config.LocationCloud})
	if got, want := SlotPrefixHolder(cfg), `a provider with id "profile"`; got != want {
		t.Errorf("SlotPrefixHolder = %q, want %q", got, want)
	}
	if SlotPrefixHolder(nil) != "" {
		t.Error("SlotPrefixHolder(nil) is not empty")
	}
}

// TestFindResolvesAProfilePin verifies that a -M profile/<name> pin finds the
// profile's row among an agent's rows, with the verdict the picker shows, and
// that a profile written for another agent is not found, so the caller gives
// its "not in the eligible list" message.
func TestFindResolvesAProfilePin(t *testing.T) {
	cfg := slotTestCfg()
	other := slotCheck(1, "for-codex", "opus", map[string]string{"opus": "openrouter/a"})
	other.Profile.Agent = "codex"
	rows := Build(Input{
		Config: cfg, Agent: "claude", Models: []config.Model{cfg.Models[0]},
		SlotProfiles: []profiles.SlotCheck{
			slotCheck(0, "mixed", "sonnet", map[string]string{"sonnet": "openrouter/a", "haiku": "omlx/q"}),
			other,
		},
	})
	r, ok := Find(rows, "profile/mixed")
	if !ok || !r.IsProfile() || r.Action() != ActionStart {
		t.Fatalf("Find(profile/mixed): found = %v, IsProfile = %v, action = %v", ok, r.IsProfile(), r.Action())
	}
	if got := strings.Join(rowModelIDs(r.Profile.Distinct()), ","); got != "openrouter/a,omlx/q" {
		t.Errorf("pinned profile's models = %s", got)
	}
	if _, ok := Find(rows, "profile/for-codex"); ok {
		t.Error("Find(profile/for-codex) found a row among claude's rows")
	}
	if m, ok := Find(rows, "openrouter/a"); !ok || m.IsProfile() {
		t.Errorf("Find(openrouter/a): found = %v, IsProfile = %v, want the model row", ok, m.IsProfile())
	}
}
