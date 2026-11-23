package litellm

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// groupCfg is testConfig with one alias: alias/x, a group of the cloud
// openrouter/x/y at order 1 and the local ollama/gemma:9b at order 2.
func groupCfg() *config.Config {
	cfg := testConfig()
	cfg.Aliases = []config.Alias{{ID: "alias/x", Targets: []config.AliasTarget{
		{Model: "openrouter/x/y", Order: 1}, {Model: "ollama/gemma:9b", Order: 2},
	}}}
	return cfg
}

// cloudAliasCfg is testConfig with alias/c naming only openrouter/x/y.
func cloudAliasCfg() *config.Config {
	cfg := testConfig()
	cfg.Aliases = []config.Alias{{ID: "alias/c", Targets: []config.AliasTarget{{Model: "openrouter/x/y", Order: 1}}}}
	return cfg
}

// aliasDBOpts is dbOpts whose registry re-read (Options.LoadRegistry)
// answers *reg, so a test can change "the registry on disk" between writes.
func aliasDBOpts(t *testing.T, body string, reg **config.Config) (Options, *int, string, *fakeDBServer) {
	t.Helper()
	o, restarts, p, fake := dbOpts(t, body)
	o.LoadRegistry = func() (*config.Config, error) { return *reg, nil }
	return o, restarts, p, fake
}

// aliasTargets is the "<target>@<order>" of every alias row (IsAliasDB) the
// fake holds for alias, sorted.
func (f *fakeDBServer) aliasTargets(alias string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for id, row := range f.rows {
		a, target, ok := parseAliasID(id)
		info, _ := row["model_info"].(map[string]any)
		if !ok || a != alias || info[AliasManagedKey] != true {
			continue
		}
		p, _ := row["litellm_params"].(map[string]any)
		out = append(out, target+"@"+fmt.Sprint(p["order"]))
	}
	slices.Sort(out)
	return out
}

// aliasIDs is the ids of the fake's alias rows (any alias), sorted.
func (f *fakeDBServer) aliasIDs() []string {
	var out []string
	for _, id := range f.ids() {
		if strings.HasPrefix(id, DBAliasIDPrefix) {
			out = append(out, id)
		}
	}
	return out
}

func countWarnings(res Result, sub string) int {
	n := 0
	for _, w := range res.Warnings {
		if strings.Contains(w, sub) {
			n++
		}
	}
	return n
}

// TestApplyChangeForUnrelatedModelKeepsAliasRows checks Review Focus 1 end to
// end: once a sync has written alias/x's rows, a start, a stop and a family
// clear of a model no alias targets, and a plain sync, leave every alias row
// exactly as it was. Phase 1 treats any owned row whose name is not desired
// as stale, so an alias row reachable by that path would vanish on the next
// unrelated start.
func TestApplyChangeForUnrelatedModelKeepsAliasRows(t *testing.T) {
	reg := groupCfg()
	o, _, _, fake := aliasDBOpts(t, "model_list: []\n"+settledSettings, &reg)
	if _, err := Sync(reg, localFor(reg, "ollama/gemma:9b"), o); err != nil {
		t.Fatal(err)
	}
	before := fake.aliasIDs()
	if len(before) != 2 {
		t.Fatalf("alias rows %v, want both targets'", before)
	}
	for _, ch := range []Change{
		{Add: localFor(reg, "mtplx/Youssofal--Q")},
		{Remove: []string{"mtplx/Youssofal--Q"}},
		{RemoveFamilies: []string{"mtplx"}},
	} {
		res, err := ApplyChange(reg, ch, o)
		if err != nil {
			t.Fatal(err)
		}
		if got := fake.aliasIDs(); !slices.Equal(got, before) {
			t.Fatalf("after %+v alias rows %v, want %v; warnings %v", ch, got, before, res.Warnings)
		}
	}
	if _, err := Sync(reg, localFor(reg, "ollama/gemma:9b"), o); err != nil {
		t.Fatal(err)
	}
	if got := fake.aliasIDs(); !slices.Equal(got, before) {
		t.Fatalf("after a plain sync alias rows %v, want %v", got, before)
	}
}

// TestStaleConfigApplyChangeDoesNotRevertARepoint checks Review Focus 2: a
// start job holding a cfg loaded before `alias set` re-pointed alias/x (from
// the local gemma to the cloud openrouter) must not put the old target back.
// The alias half plans from the registry re-read under the config.yaml lock
// (here a scratch WT_REGISTRY read by config.Load, the LoadRegistry default),
// so the new row survives and the old one is not re-added.
func TestStaleConfigApplyChangeDoesNotRevertARepoint(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))
	regPath := filepath.Join(dir, "registry.toml")
	t.Setenv("WT_REGISTRY", regPath)
	const head = `[[providers]]
id = "ollama"
location = "local"
[providers.auth]
type = "none"
base_url = "http://localhost:11434"

[[providers]]
id = "openrouter"
location = "cloud"
[providers.auth]
type = "api_key"
base_url = "https://openrouter.ai/api/v1"
secret_ref = "sk-test"

[[models]]
id = "ollama/gemma:9b"
provider_id = "ollama"
model_name = "gemma:9b"
family = "gemma"

[[models]]
id = "openrouter/x/y"
provider_id = "openrouter"
model_name = "x/y"
family = "x"

`
	write := func(target string) {
		t.Helper()
		body := head + "[[aliases]]\nid = \"alias/x\"\ntargets = [{ model = \"" + target + "\", order = 1 }]\n"
		if err := os.WriteFile(regPath, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("ollama/gemma:9b")
	stale, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	o, _, _, fake := dbOpts(t, "model_list: []\n"+settledSettings)
	if _, err := Sync(stale, localFor(stale, "ollama/gemma:9b"), o); err != nil {
		t.Fatal(err)
	}
	if got := fake.aliasTargets("alias/x"); !slices.Equal(got, []string{"ollama/gemma:9b@1"}) {
		t.Fatalf("alias rows %v before the re-point", got)
	}
	// `alias set alias/x --target openrouter/x/y` and its sync.
	write("openrouter/x/y")
	fresh, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Sync(fresh, nil, o); err != nil {
		t.Fatal(err)
	}
	if got := fake.aliasTargets("alias/x"); !slices.Equal(got, []string{"openrouter/x/y@1"}) {
		t.Fatalf("alias rows %v after the re-point", got)
	}
	// The start job still holds the cfg it loaded before the set.
	res, err := ApplyChange(stale, Change{Add: localFor(stale, "ollama/gemma:9b")}, o)
	if err != nil {
		t.Fatal(err)
	}
	if got := fake.aliasTargets("alias/x"); !slices.Equal(got, []string{"openrouter/x/y@1"}) {
		t.Fatalf("alias rows %v after the stale start, want the re-point kept; warnings %v", got, res.Warnings)
	}
}

// TestStartOfATargetAddsItsAliasRow checks that starting a group's local
// target adds its alias row (order 2) beside the cloud target's, through the
// database alone: no config.yaml write and no restart, and the alias result
// is no Outcome of the start (lifecycle reads every Outcome as the started
// model's own).
func TestStartOfATargetAddsItsAliasRow(t *testing.T) {
	reg := groupCfg()
	o, restarts, _, fake := aliasDBOpts(t, "model_list: []\n"+settledSettings, &reg)
	if _, err := Sync(reg, nil, o); err != nil {
		t.Fatal(err)
	}
	if got := fake.aliasTargets("alias/x"); !slices.Equal(got, []string{"openrouter/x/y@1"}) {
		t.Fatalf("alias rows %v, want only the cloud target's while gemma is down", got)
	}
	*restarts = 0
	res, err := ApplyChange(reg, Change{Add: localFor(reg, "ollama/gemma:9b")}, o)
	if err != nil {
		t.Fatal(err)
	}
	if got := fake.aliasTargets("alias/x"); !slices.Equal(got, []string{"ollama/gemma:9b@2", "openrouter/x/y@1"}) {
		t.Fatalf("alias rows %v, want both", got)
	}
	if *restarts != 0 || res.Changed {
		t.Fatalf("restarts=%d changed=%v, want none", *restarts, res.Changed)
	}
	if _, ok := outcomeFor(res, "alias/x"); ok || len(res.Warnings) != 0 {
		t.Fatalf("outcomes %+v warnings %v, want no alias outcome and no warning", res.Outcomes, res.Warnings)
	}
}

// TestStopOfATargetRemovesOnlyThatAliasRow checks that stopping the group's
// local target deletes that target's alias row only: the cloud target's row
// keeps its id (it is never rewritten), so the alias stays served without a
// gap.
func TestStopOfATargetRemovesOnlyThatAliasRow(t *testing.T) {
	reg := groupCfg()
	o, restarts, _, fake := aliasDBOpts(t, "model_list: []\n"+settledSettings, &reg)
	if _, err := Sync(reg, localFor(reg, "ollama/gemma:9b"), o); err != nil {
		t.Fatal(err)
	}
	var cloudID string
	for _, id := range fake.aliasIDs() {
		if strings.HasSuffix(id, "/openrouter/x/y") {
			cloudID = id
		}
	}
	*restarts = 0
	adds := fake.requests("POST /model/new")
	res, err := ApplyChange(reg, Change{Remove: []string{"ollama/gemma:9b"}}, o)
	if err != nil {
		t.Fatal(err)
	}
	if got := fake.aliasIDs(); !slices.Equal(got, []string{cloudID}) {
		t.Fatalf("alias rows %v, want only %s", got, cloudID)
	}
	if fake.requests("POST /model/new") != adds || *restarts != 0 {
		t.Fatalf("adds=%d restarts=%d, want no add and no restart", fake.requests("POST /model/new")-adds, *restarts)
	}
	if _, ok := outcomeFor(res, "alias/x"); ok {
		t.Fatalf("outcomes %+v, want no alias outcome", res.Outcomes)
	}
}

// TestCloudOnlyAliasRoutedBySync checks that a sync routes an alias of a
// cloud model (one row, order 1, outcome "routed"), that a second sync writes
// nothing for it, and that removing the alias from the registry makes the
// next sync delete its rows (outcome "unrouted").
func TestCloudOnlyAliasRoutedBySync(t *testing.T) {
	reg := cloudAliasCfg()
	o, _, _, fake := aliasDBOpts(t, "model_list: []\n"+settledSettings, &reg)
	res, err := Sync(reg, nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if oc, ok := outcomeFor(res, "alias/c"); !ok || oc.Action != "routed" || oc.Err != nil {
		t.Fatalf("outcomes %+v, want alias/c routed", res.Outcomes)
	}
	if got := fake.aliasTargets("alias/c"); !slices.Equal(got, []string{"openrouter/x/y@1"}) {
		t.Fatalf("alias rows %v", got)
	}
	adds := fake.requests("POST /model/new")
	res, err = Sync(reg, nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := outcomeFor(res, "alias/c"); ok || fake.requests("POST /model/new") != adds {
		t.Fatalf("second sync outcomes %+v adds %d, want nothing", res.Outcomes, fake.requests("POST /model/new")-adds)
	}
	reg = testConfig()
	res, err = Sync(reg, nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if oc, ok := outcomeFor(res, "alias/c"); !ok || oc.Action != "unrouted" {
		t.Fatalf("outcomes %+v, want alias/c unrouted", res.Outcomes)
	}
	if got := fake.aliasIDs(); len(got) != 0 {
		t.Fatalf("alias rows %v, want none", got)
	}
}

// TestCloudTargetBuildFailureKeepsItsAliasRow checks Review Focus 6: a cloud
// target whose row cannot be built (its secret_ref unset in serve's
// environment) is frozen, not dead: its alias row stays, nothing is added,
// and one warning names it. Deleting the row would unroute every client
// session over an environment difference.
func TestCloudTargetBuildFailureKeepsItsAliasRow(t *testing.T) {
	reg := cloudAliasCfg()
	o, _, _, fake := aliasDBOpts(t, "model_list: []\n"+settledSettings, &reg)
	if _, err := Sync(reg, nil, o); err != nil {
		t.Fatal(err)
	}
	before := fake.aliasIDs()
	t.Setenv("WT_TEST_ALIAS_UNSET_KEY", "")
	broken := cloudAliasCfg()
	broken.Providers[2].Auth.SecretRef = "os.environ/WT_TEST_ALIAS_UNSET_KEY"
	reg = broken
	adds, dels := fake.requests("POST /model/new"), fake.requests("POST /model/delete")
	res, err := Sync(broken, nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if got := fake.aliasIDs(); !slices.Equal(got, before) || fake.requests("POST /model/new") != adds || fake.requests("POST /model/delete") != dels {
		t.Fatalf("alias rows %v (want %v), adds %d, deletes %d", got, before, fake.requests("POST /model/new")-adds, fake.requests("POST /model/delete")-dels)
	}
	if n := countWarnings(res, "alias/c"); n != 1 {
		t.Fatalf("warnings %q, want one naming alias/c", res.Warnings)
	}
	if _, ok := outcomeFor(res, "alias/c"); ok {
		t.Fatalf("outcomes %+v, want none for alias/c", res.Outcomes)
	}
}

// aliasFaultCase seeds alias/c's row with a sound registry, then syncs with
// bad in its place and checks Review Focus 6's freeze: the alias's rows are
// neither added to nor deleted, and exactly one warning names it.
func aliasFaultCase(t *testing.T, bad *config.Config, local []string) {
	t.Helper()
	reg := cloudAliasCfg()
	o, _, _, fake := aliasDBOpts(t, "model_list: []\n"+settledSettings, &reg)
	if _, err := Sync(reg, nil, o); err != nil {
		t.Fatal(err)
	}
	before := fake.aliasIDs()
	if len(before) != 1 {
		t.Fatalf("alias rows %v", before)
	}
	reg = bad
	res, err := Sync(bad, localFor(bad, local...), o)
	if err != nil {
		t.Fatal(err)
	}
	if got := fake.aliasIDs(); !slices.Equal(got, before) {
		t.Fatalf("alias rows %v, want %v kept", got, before)
	}
	for _, id := range fake.ids() {
		if strings.HasPrefix(id, DBAliasIDPrefix) && !slices.Contains(before, id) {
			t.Fatalf("alias row %s added", id)
		}
	}
	if n := countWarnings(res, "alias/c"); n != 1 {
		t.Fatalf("warnings %q, want one naming alias/c", res.Warnings)
	}
	if _, ok := outcomeFor(res, "alias/c"); ok {
		t.Fatalf("outcomes %+v, want none for alias/c", res.Outcomes)
	}
}

// TestMalformedAliasKeepsItsRows checks that a hand-edit slip (an alias the
// reader marks Malformed) freezes the alias whole: its live rows stay, with
// one warning naming the fault, until the registry is repaired.
func TestMalformedAliasKeepsItsRows(t *testing.T) {
	bad := cloudAliasCfg()
	bad.Aliases[0].Malformed = `unknown key "note" (an alias holds id and targets)`
	aliasFaultCase(t, bad, nil)
}

// TestSyncSkipsAnAliasWhoseIDIsAModel checks the twin rule AliasFault
// rejects: an alias whose id is also a model id is skipped with one warning
// and its rows are frozen, never written beside the model's route.
func TestSyncSkipsAnAliasWhoseIDIsAModel(t *testing.T) {
	bad := cloudAliasCfg()
	// A native model, so the model half routes nothing under the id and the
	// only line about alias/c is the alias half's.
	bad.Models = append(bad.Models, config.Model{ID: "alias/c", ProviderID: "claude", ModelName: "c", Native: true})
	aliasFaultCase(t, bad, nil)
}

// TestSyncSkipsAnAliasWithTwoLocalTargets checks that an alias AliasFault
// rejects for two local targets (both running) is skipped with one warning:
// no row of either target is added and the existing rows stay.
func TestSyncSkipsAnAliasWithTwoLocalTargets(t *testing.T) {
	bad := cloudAliasCfg()
	bad.Aliases[0].Targets = append(bad.Aliases[0].Targets,
		config.AliasTarget{Model: "ollama/gemma:9b", Order: 2}, config.AliasTarget{Model: "mtplx/Youssofal--Q", Order: 3})
	aliasFaultCase(t, bad, []string{"ollama/gemma:9b", "mtplx/Youssofal--Q"})
}

// TestLaunchEnsureNeverReachesTheAliasHalf checks that the launch-time cloud
// ensure (AddMissingOnly) of an alias target makes no API call and does not
// re-read the registry: it must never re-resolve alias keys on a launch.
func TestLaunchEnsureNeverReachesTheAliasHalf(t *testing.T) {
	reg := cloudAliasCfg()
	o, _, _, fake := aliasDBOpts(t, "model_list: []\n"+settledSettings, &reg)
	loads := 0
	o.LoadRegistry = func() (*config.Config, error) { loads++; return reg, nil }
	res, err := ApplyChange(reg, Change{Add: localFor(reg, "openrouter/x/y"), AddMissingOnly: true}, o)
	if err != nil {
		t.Fatal(err)
	}
	if fake.calls != 0 || loads != 0 {
		t.Fatalf("API calls %d (%v), registry loads %d, want none", fake.calls, fake.log, loads)
	}
	if len(res.Warnings) != 0 {
		t.Fatalf("warnings %v", res.Warnings)
	}
}

// TestAliasNeverWrittenToConfigYaml checks decision 4: with the database
// half off (store_model_in_db: false, and no admin client at all), an alias
// is never written to config.yaml; the write still goes on and one warning
// names the reason and the repair.
func TestAliasNeverWrittenToConfigYaml(t *testing.T) {
	const off = "litellm_settings:\n  drop_params: true\n  use_chat_completions_url_for_anthropic_messages: true\ngeneral_settings:\n  cancel_on_disconnect: true\n  store_model_in_db: false\n"
	for _, tc := range []struct {
		name  string
		admin bool
	}{{"store off", true}, {"no admin client", false}} {
		t.Run(tc.name, func(t *testing.T) {
			reg := groupCfg()
			o, _, p, fake := aliasDBOpts(t, "model_list: []\n"+off, &reg)
			if !tc.admin {
				o.Admin = nil
			}
			for _, run := range []func() (Result, error){
				func() (Result, error) { return Sync(reg, nil, o) },
				func() (Result, error) { return ApplyChange(reg, Change{Add: localFor(reg, "ollama/gemma:9b")}, o) },
			} {
				res, err := run()
				if err != nil {
					t.Fatal(err)
				}
				if n := countWarnings(res, "alias routes unavailable ("); n != 1 {
					t.Fatalf("warnings %q, want one alias line", res.Warnings)
				}
				if !hasWarning(res, "; registry written, LiteLLM not updated; run 'wt-model litellm sync' when the proxy answers") {
					t.Fatalf("warnings %q, want decision 4's text", res.Warnings)
				}
			}
			if strings.Contains(readFile(t, p), "alias/") || fake.calls != 0 {
				t.Fatalf("config.yaml %q or API calls %d, want no alias anywhere", readFile(t, p), fake.calls)
			}
		})
	}
}

// TestAliasTwinOfAFileRowIsSkipped checks the twin guard: an alias whose id
// a config.yaml row already serves (hand-written) gets no database row
// beside it (LiteLLM would load-balance the two) and one warning, and the
// file row is left alone.
func TestAliasTwinOfAFileRowIsSkipped(t *testing.T) {
	body := "model_list:\n  - model_name: alias/c\n    litellm_params:\n      model: openai/c\n" + settledSettings
	reg := cloudAliasCfg()
	o, _, p, fake := aliasDBOpts(t, body, &reg)
	res, err := Sync(reg, nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if got := fake.aliasIDs(); len(got) != 0 {
		t.Fatalf("alias rows %v, want none beside the file row", got)
	}
	if n := countWarnings(res, "alias/c"); n != 1 {
		t.Fatalf("warnings %q, want one naming alias/c", res.Warnings)
	}
	f, _ := Open(p)
	if !slices.Contains(f.RoutedIDs(), "alias/c") {
		t.Fatalf("config.yaml rows %v, want the hand-written row kept", f.RoutedIDs())
	}
}

// TestAliasSquatterRowIsNamed checks Task 3's carried-over finding: an
// unmarked row under the wta:alias/ prefix (one wt did not write) makes the
// alias's adds fail with LiteLLM's generic duplicate 500, so the warning must
// name that row and say to remove it by hand; the row itself is never
// deleted.
func TestAliasSquatterRowIsNamed(t *testing.T) {
	reg := cloudAliasCfg()
	o, _, _, fake := aliasDBOpts(t, "model_list: []\n"+settledSettings, &reg)
	want := aliasRowsFor(t, reg, "alias/c", config.AliasTarget{Model: "openrouter/x/y", Order: 1})[0]
	fake.seed(DBModel{ID: want.ID, ModelName: "alias/c", Params: map[string]any{"model": "openai/other"}, Info: map[string]any{}})
	res, err := Sync(reg, nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(fake.ids(), want.ID) || fake.requests("POST /model/delete") != 0 {
		t.Fatalf("rows %v deletes %d, want the squatter left alone", fake.ids(), fake.requests("POST /model/delete"))
	}
	if !slices.ContainsFunc(res.Warnings, func(w string) bool {
		return strings.Contains(w, want.ID) && strings.Contains(w, "by hand")
	}) {
		t.Fatalf("warnings %q, want one naming %s and saying to remove it by hand", res.Warnings, want.ID)
	}
}

// TestPlanSyncListsAliasesWithoutWriting checks the dry run: plan.Aliases
// says what the sync would do with each alias (alias/x would be routed to its
// cloud target at order 1, its local target skipped as not running), and the
// only API call is the listing.
func TestPlanSyncListsAliasesWithoutWriting(t *testing.T) {
	reg := groupCfg()
	o, _, _, fake := aliasDBOpts(t, "model_list: []\n"+settledSettings, &reg)
	plan, err := PlanSync(reg, nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Aliases) != 1 {
		t.Fatalf("aliases %+v", plan.Aliases)
	}
	a := plan.Aliases[0]
	if a.ID != "alias/x" || a.Action != AliasActionRoute || !slices.Equal(a.Live, []AliasLive{{Target: "openrouter/x/y", Order: 1}}) {
		t.Fatalf("alias plan %+v", a)
	}
	if len(a.Skipped) != 1 || a.Skipped[0].Target != "ollama/gemma:9b" || a.Skipped[0].Frozen {
		t.Fatalf("skipped %+v", a.Skipped)
	}
	for _, l := range fake.log {
		if l != "GET /model/info" {
			t.Fatalf("API calls %v, want only the listing", fake.log)
		}
	}
	if len(fake.aliasIDs()) != 0 {
		t.Fatal("the dry run wrote a row")
	}
}

// TestNoRestartForAnAliasChange checks that an alias change never costs a
// proxy restart: routing an alias of a running local model and then
// re-pointing it (a new order, so a new row id: add, confirm, delete) leave
// config.yaml alone and the restart counter at zero.
func TestNoRestartForAnAliasChange(t *testing.T) {
	reg := testConfig()
	reg.Aliases = []config.Alias{{ID: "alias/l", Targets: []config.AliasTarget{{Model: "ollama/gemma:9b", Order: 1}}}}
	o, restarts, _, fake := aliasDBOpts(t, "model_list: []\n"+settledSettings, &reg)
	// The cloud model's file row would cost a restart of its own.
	reg.Models = slices.DeleteFunc(reg.Models, func(m config.Model) bool { return m.ID == "openrouter/x/y" })
	if _, err := Sync(reg, localFor(reg, "ollama/gemma:9b"), o); err != nil {
		t.Fatal(err)
	}
	first := fake.aliasIDs()
	repointed := *reg
	repointed.Aliases = []config.Alias{{ID: "alias/l", Targets: []config.AliasTarget{{Model: "ollama/gemma:9b", Order: 3}}}}
	reg = &repointed
	res, err := Sync(reg, localFor(reg, "ollama/gemma:9b"), o)
	if err != nil {
		t.Fatal(err)
	}
	if got := fake.aliasTargets("alias/l"); !slices.Equal(got, []string{"ollama/gemma:9b@3"}) || slices.Equal(fake.aliasIDs(), first) {
		t.Fatalf("alias rows %v (first %v), want the re-pointed row", fake.aliasIDs(), first)
	}
	if oc, ok := outcomeFor(res, "alias/l"); !ok || oc.Action != "rewritten" {
		t.Fatalf("outcomes %+v, want alias/l rewritten", res.Outcomes)
	}
	if *restarts != 0 {
		t.Fatalf("restarts %d, want 0", *restarts)
	}
}

// TestDesiredIDsCountsAliasesWithARow checks that DesiredIDs (the Setup
// tab's "what should the proxy serve" list) names an alias that would have a
// row after a sync, and not one with no live target: otherwise the tab
// would call a routed alias unexpected, or expect one that cannot be served.
func TestDesiredIDsCountsAliasesWithARow(t *testing.T) {
	reg := groupCfg()
	reg.Aliases = append(reg.Aliases, config.Alias{ID: "alias/down", Targets: []config.AliasTarget{{Model: "mtplx/Youssofal--Q", Order: 1}}})
	o, _, _, _ := aliasDBOpts(t, "model_list: []\n"+settledSettings, &reg)
	ids, err := DesiredIDs(reg, nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(ids, "alias/x") || slices.Contains(ids, "alias/down") {
		t.Fatalf("ids %v, want alias/x and not alias/down", ids)
	}
}

// TestStopDeletesALeftoverAliasRowOfTheStoppedTarget checks H1: a re-point
// (or a removal) of an alias made while the database half was down leaves
// the alias's row for its old local target; a later stop of that target
// must delete that row, read from the listed row's id, though the registry
// no longer pairs the alias with the target, or the alias keeps routing to
// a stopped server until a full sync. Every other alias row (the alias's
// own cloud target, an unrelated alias) is left exactly as it was.
func TestStopDeletesALeftoverAliasRowOfTheStoppedTarget(t *testing.T) {
	for _, tc := range []struct {
		name  string
		after []config.Alias // the registry after the edit made while the database was down
		want  []string       // alias/x's remaining targets
	}{
		{"re-pointed", []config.Alias{{ID: "alias/x", Targets: []config.AliasTarget{{Model: "openrouter/x/y", Order: 1}}}}, []string{"openrouter/x/y@1"}},
		{"removed", nil, []string{"openrouter/x/y@1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := groupCfg()
			reg.Aliases = append(reg.Aliases, config.Alias{ID: "alias/c", Targets: []config.AliasTarget{{Model: "openrouter/x/y", Order: 1}}})
			o, _, _, fake := aliasDBOpts(t, "model_list: []\n"+settledSettings, &reg)
			if _, err := Sync(reg, localFor(reg, "ollama/gemma:9b"), o); err != nil {
				t.Fatal(err)
			}
			if got := fake.aliasTargets("alias/x"); !slices.Equal(got, []string{"ollama/gemma:9b@2", "openrouter/x/y@1"}) {
				t.Fatalf("alias/x rows %v before the edit", got)
			}
			var keep []string
			for _, id := range fake.aliasIDs() {
				if !strings.HasSuffix(id, "/ollama/gemma:9b") {
					keep = append(keep, id)
				}
			}
			// The edit while the database was down: the registry changes, no
			// alias row does.
			next := testConfig()
			next.Aliases = append(slices.Clone(tc.after), config.Alias{ID: "alias/c", Targets: []config.AliasTarget{{Model: "openrouter/x/y", Order: 1}}})
			reg = next
			res, err := ApplyChange(reg, Change{Remove: []string{"ollama/gemma:9b"}}, o)
			if err != nil {
				t.Fatal(err)
			}
			if got := fake.aliasIDs(); !slices.Equal(got, keep) {
				t.Fatalf("alias rows %v, want %v (only the stopped target's row gone); warnings %v", got, keep, res.Warnings)
			}
		})
	}
}

// scratchRegistryRealHome points the registry at a scratch file (WT_REGISTRY)
// while the wt-model home stays the default one under a temporary HOME: the
// documented ad-hoc recipe that redirects the registry and config.yaml but
// not the proxy pair.
func scratchRegistryRealHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("WT_REGISTRY", filepath.Join(t.TempDir(), "registry.toml"))
	if !scratchRegistryRealProxy() {
		t.Fatal("the fixture is not a scratch registry beside the default wt-model home")
	}
}

// TestScratchRegistrySyncDeletesNoAliasRow checks H2: a full sync run with a
// redirected registry but the default wt-model home reaches the real proxy's
// database with a registry that did not write its alias rows. It must
// delete none of them (an orphan delete would wipe every real alias) and
// say so once, while an alias the scratch registry has is still added.
func TestScratchRegistrySyncDeletesNoAliasRow(t *testing.T) {
	reg := groupCfg()
	o, _, _, fake := aliasDBOpts(t, "model_list: []\n"+settledSettings, &reg)
	if _, err := Sync(reg, localFor(reg, "ollama/gemma:9b"), o); err != nil {
		t.Fatal(err)
	}
	before := fake.aliasIDs()
	if len(before) != 2 {
		t.Fatalf("alias rows %v, want both of alias/x", before)
	}
	scratchRegistryRealHome(t)
	reg = cloudAliasCfg() // the scratch registry: no alias/x, an alias/c of its own
	res, err := Sync(reg, nil, o)
	if err != nil {
		t.Fatal(err)
	}
	got := fake.aliasIDs()
	for _, id := range before {
		if !slices.Contains(got, id) {
			t.Errorf("alias row %s was deleted; rows now %v", id, got)
		}
	}
	if targets := fake.aliasTargets("alias/c"); !slices.Equal(targets, []string{"openrouter/x/y@1"}) {
		t.Errorf("alias/c rows %v, want the scratch registry's alias added", targets)
	}
	if n := countWarnings(res, "no alias row was deleted"); n != 1 {
		t.Errorf("warnings %v, want one saying no alias row was deleted", res.Warnings)
	}
}
