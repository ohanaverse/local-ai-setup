package litellm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

func allLive(string) bool  { return true }
func noneLive(string) bool { return false }

// aliasRowsFor builds the rows of alias id over targets with every target
// live, failing the test on any skip.
func aliasRowsFor(t *testing.T, cfg *config.Config, id string, targets ...config.AliasTarget) []DBModel {
	t.Helper()
	rows, skips := DBModelsFromAlias(cfg, config.Alias{ID: id, Targets: targets}, allLive, testMasterKey)
	if len(skips) != 0 {
		t.Fatalf("skips %+v", skips)
	}
	return rows
}

// storedAlias is m as ApplyAliasDB leaves it in the database: its hash
// stamped in Info.
func storedAlias(m DBModel) DBModel { return stored(m) }

// TestAliasRowCopiesTheTargetParamsWithOrder checks that an alias row is the
// target's own model row (api_base, the launcher-required
// additional_drop_params ensureRowParams adds) renamed to the alias, with the
// target's order, the alias markers and no wt_managed: a row carrying
// wt_managed would be deleted by every phase 1 sync (Review Focus 1), and a
// row without the target's params would not reach the model.
func TestAliasRowCopiesTheTargetParamsWithOrder(t *testing.T) {
	cfg := testConfig()
	rows := aliasRowsFor(t, cfg, "alias/x", config.AliasTarget{Model: "ollama/gemma:9b", Order: 2})
	if len(rows) != 1 {
		t.Fatalf("rows %+v, want 1", rows)
	}
	r := rows[0]
	if r.ModelName != "alias/x" {
		t.Fatalf("model_name %q", r.ModelName)
	}
	if r.Params["model"] != "ollama_chat/gemma:9b" {
		t.Fatalf("model %v", r.Params["model"])
	}
	if r.Params["api_base"] != "http://localhost:11434" {
		t.Fatalf("api_base %v", r.Params["api_base"])
	}
	if r.Params["additional_drop_params"] == nil {
		t.Fatalf("no additional_drop_params: %v", r.Params)
	}
	if r.Params["order"] != 2 {
		t.Fatalf("order %#v, want 2", r.Params["order"])
	}
	if _, ok := r.Info[ManagedKey]; ok {
		t.Fatalf("alias row carries %s: %v", ManagedKey, r.Info)
	}
	if r.Info[AliasManagedKey] != true || r.Info[AliasKey] != "alias/x" || r.Info[AliasTargetKey] != "ollama/gemma:9b" {
		t.Fatalf("info %v", r.Info)
	}
	if _, ok := r.Info[ParamsHashKey]; ok {
		t.Fatal("the row is stamped before ApplyAliasDB")
	}
	h := routeHash(r, testMasterKey)
	if want := DBAliasIDPrefix + "x/" + h[:8] + "/ollama/gemma:9b"; r.ID != want {
		t.Fatalf("id %q, want %q", r.ID, want)
	}
	if !IsAliasDB(r) {
		t.Fatal("IsAliasDB false for an alias row")
	}
}

// TestAliasRowIDChangesWithContent checks that any change to what a row
// routes to (its order, its target's address) gives it a new id: a re-point
// is then an Add plus a Delete, never an in-place update (decision 2).
func TestAliasRowIDChangesWithContent(t *testing.T) {
	cfg := testConfig()
	base := aliasRowsFor(t, cfg, "alias/x", config.AliasTarget{Model: "ollama/gemma:9b", Order: 1})[0]
	reordered := aliasRowsFor(t, cfg, "alias/x", config.AliasTarget{Model: "ollama/gemma:9b", Order: 3})[0]
	if base.ID == reordered.ID {
		t.Fatalf("order change kept id %q", base.ID)
	}
	moved := testConfig()
	moved.Providers[0].Auth.BaseURL = "http://localhost:11435"
	other := aliasRowsFor(t, moved, "alias/x", config.AliasTarget{Model: "ollama/gemma:9b", Order: 1})[0]
	if base.ID == other.ID {
		t.Fatalf("api_base change kept id %q", base.ID)
	}
}

// TestAliasRowIDStableForSameContent checks that building the same alias
// twice gives the same ids: otherwise every sync would re-point every alias.
func TestAliasRowIDStableForSameContent(t *testing.T) {
	cfg := testConfig()
	targets := []config.AliasTarget{{Model: "ollama/gemma:9b", Order: 1}, {Model: "openrouter/x/y", Order: 2}}
	a := aliasRowsFor(t, cfg, "alias/x", targets...)
	b := aliasRowsFor(t, testConfig(), "alias/x", targets...)
	if !reflect.DeepEqual(planIDs(a), planIDs(b)) || len(a) != 2 {
		t.Fatalf("ids %v vs %v", planIDs(a), planIDs(b))
	}
}

// TestDeadTargetGetsNoRow checks decision 3: a local target that is not
// running gets no row (a dead api_base at order 1 would add a failed attempt
// to every request), while a cloud target is live regardless, and a cloud
// target whose row cannot be built (its key unset in serve's environment) is
// frozen, not dead, so its existing rows are kept rather than unrouted.
func TestDeadTargetGetsNoRow(t *testing.T) {
	cfg := testConfig()
	a := config.Alias{ID: "alias/x", Targets: []config.AliasTarget{{Model: "ollama/gemma:9b", Order: 1}, {Model: "openrouter/x/y", Order: 2}}}
	rows, skips := DBModelsFromAlias(cfg, a, noneLive, testMasterKey)
	if len(rows) != 1 || rows[0].Info[AliasTargetKey] != "openrouter/x/y" {
		t.Fatalf("rows %+v, want only the cloud target's", rows)
	}
	if !reflect.DeepEqual(skips, []AliasSkip{{Target: "ollama/gemma:9b", Reason: "not running"}}) {
		t.Fatalf("skips %+v", skips)
	}

	t.Setenv("WT_TEST_ALIAS_UNSET_KEY", "")
	cfg.Providers[2].Auth.SecretRef = "os.environ/WT_TEST_ALIAS_UNSET_KEY"
	rows, skips = DBModelsFromAlias(cfg, a, allLive, testMasterKey)
	if len(rows) != 1 || rows[0].Info[AliasTargetKey] != "ollama/gemma:9b" {
		t.Fatalf("rows %+v, want only the local target's", rows)
	}
	if len(skips) != 1 || skips[0].Target != "openrouter/x/y" || !skips[0].Frozen || skips[0].Reason == "" {
		t.Fatalf("skips %+v, want the cloud target frozen", skips)
	}
}

// TestAliasRowNeverCarriesAResolvedKeyInItsIDOrSkip checks that a cloud
// target's resolved api_key (decision 13) reaches the row's params only: it is
// in no id, no skip reason and no hash in recoverable form.
func TestAliasRowNeverCarriesAResolvedKeyInItsIDOrSkip(t *testing.T) {
	cfg := testConfig()
	cfg.Providers[2].Auth.SecretRef = "sk-alias-secret-value"
	r := aliasRowsFor(t, cfg, "alias/x", config.AliasTarget{Model: "openrouter/x/y", Order: 1})[0]
	if r.Params["api_key"] != "sk-alias-secret-value" {
		t.Fatalf("api_key %v", r.Params["api_key"])
	}
	if strings.Contains(r.ID, "sk-alias") || strings.Contains(routeHash(r, testMasterKey), "sk-alias") {
		t.Fatal("the key leaked into the id or hash")
	}
}

// TestPlanAliasDBNeverTouchesModelRows checks that the alias plan sees only
// alias rows: a phase 1 wt: row (even one named like an alias), a LiteLLM UI
// row and a row under the alias prefix without the alias marker are never
// planned, whatever deleteOrphans says.
func TestPlanAliasDBNeverTouchesModelRows(t *testing.T) {
	current := []DBModel{
		stored(managedRow("wt:ollama/a", "ollama/a", map[string]any{"model": "ollama/a"})),
		stored(managedRow("wt:alias/x", "alias/x", map[string]any{"model": "ollama/a"})),
		{ID: "ui-row", ModelName: "alias/x", Params: map[string]any{"model": "openai/x"}, Info: map[string]any{}},
		{ID: DBAliasIDPrefix + "x/00000000/ollama/a", ModelName: "alias/x", Params: map[string]any{}, Info: map[string]any{AliasKey: "alias/x"}},
	}
	p := PlanAliasDB(map[string][]DBModel{}, current, testMasterKey, func(string, string) bool { return false }, true)
	if !p.Empty() {
		t.Fatalf("plan %+v, want empty", p)
	}
}

// TestPlanAliasDBKeepsFrozenTargetRows checks decision 3's freeze: a frozen
// target's rows stay while the (alias, target) pair is configured, but a
// target dropped from the alias, or an alias removed, loses its rows whatever
// the freeze (keep is the caller's answer and says false for those).
func TestPlanAliasDBKeepsFrozenTargetRows(t *testing.T) {
	cfg := testConfig()
	cloud := storedAlias(aliasRowsFor(t, cfg, "alias/x", config.AliasTarget{Model: "openrouter/x/y", Order: 1})[0])
	local := storedAlias(aliasRowsFor(t, cfg, "alias/x", config.AliasTarget{Model: "ollama/gemma:9b", Order: 2})[0])
	gone := storedAlias(aliasRowsFor(t, cfg, "alias/gone", config.AliasTarget{Model: "openrouter/x/y", Order: 1})[0])
	frozen := map[string]bool{"openrouter/x/y": true}
	for _, tc := range []struct {
		name       string
		configured map[string][]string // alias -> targets still configured
		desired    map[string][]DBModel
		wantDelete []string
	}{
		{
			name:       "frozen target kept",
			configured: map[string][]string{"alias/x": {"openrouter/x/y", "ollama/gemma:9b"}, "alias/gone": {"openrouter/x/y"}},
			desired:    map[string][]DBModel{"alias/x": {local}},
		},
		{
			name:       "target dropped from the alias deleted although frozen",
			configured: map[string][]string{"alias/x": {"ollama/gemma:9b"}, "alias/gone": {"openrouter/x/y"}},
			desired:    map[string][]DBModel{"alias/x": {local}},
			wantDelete: []string{cloud.ID},
		},
		{
			name:       "alias removed deleted although frozen",
			configured: map[string][]string{"alias/x": {"openrouter/x/y", "ollama/gemma:9b"}},
			desired:    map[string][]DBModel{"alias/x": {local}},
			wantDelete: []string{gone.ID},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			keep := func(alias, target string) bool {
				return frozen[target] && slices.Contains(tc.configured[alias], target)
			}
			p := PlanAliasDB(tc.desired, []DBModel{cloud, local, gone}, testMasterKey, keep, true)
			var del []string
			for _, a := range p.Aliases {
				if len(a.Add)+len(a.Replace) != 0 {
					t.Fatalf("plan %+v adds rows", p)
				}
				del = append(del, a.Delete...)
			}
			if !slices.Equal(del, tc.wantDelete) {
				t.Fatalf("deletes %v, want %v", del, tc.wantDelete)
			}
		})
	}
	// Without deleteOrphans an alias absent from desired is not touched.
	p := PlanAliasDB(map[string][]DBModel{"alias/x": {local}}, []DBModel{cloud, local, gone}, testMasterKey,
		func(string, string) bool { return false }, false)
	if len(p.Aliases) != 1 || p.Aliases[0].Alias != "alias/x" || !slices.Equal(p.Aliases[0].Delete, []string{cloud.ID}) {
		t.Fatalf("plan %+v, want only alias/x's cloud row deleted", p)
	}
}

// TestPlanAliasDBReplacesARowWithAWrongStamp checks that a row whose id is
// wanted but whose stored hash is not (edited outside wt, e.g. the LiteLLM
// UI) is replaced, and that a row with the right stamp plans nothing.
func TestPlanAliasDBReplacesARowWithAWrongStamp(t *testing.T) {
	cfg := testConfig()
	want := aliasRowsFor(t, cfg, "alias/x", config.AliasTarget{Model: "ollama/gemma:9b", Order: 1})[0]
	edited := storedAlias(want)
	edited.Info[ParamsHashKey] = "not-the-hash"
	p := PlanAliasDB(map[string][]DBModel{"alias/x": {want}}, []DBModel{edited}, testMasterKey, func(string, string) bool { return false }, true)
	if len(p.Aliases) != 1 || !slices.Equal(planIDs(p.Aliases[0].Replace), []string{want.ID}) ||
		len(p.Aliases[0].Add)+len(p.Aliases[0].Delete) != 0 {
		t.Fatalf("plan %+v, want one Replace", p)
	}
	if p := PlanAliasDB(map[string][]DBModel{"alias/x": {want}}, []DBModel{storedAlias(want)}, testMasterKey, func(string, string) bool { return false }, true); !p.Empty() {
		t.Fatalf("plan %+v, want empty", p)
	}
}

// aliasRowCounts wraps the fake proxy so that after every request it records
// how many rows are named alias: the probe of Review Focus 2's "no gap".
func aliasRowCounts(t *testing.T, f *fakeDBServer, alias string) (*AdminClient, *[]int) {
	t.Helper()
	var counts []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.ServeHTTP(w, r)
		f.mu.Lock()
		n := 0
		for _, row := range f.rows {
			if row["model_name"] == alias {
				n++
			}
		}
		counts = append(counts, n)
		f.mu.Unlock()
	}))
	t.Cleanup(srv.Close)
	return &AdminClient{BaseURL: srv.URL, Key: testMasterKey}, &counts
}

// TestAliasRepointAddsBeforeItDeletes checks Review Focus 2: a re-point adds
// the new rows, re-lists to confirm them and only then deletes the old ones,
// so the alias name has a row after every single API call (no gap a client
// session would see as an unknown model).
func TestAliasRepointAddsBeforeItDeletes(t *testing.T) {
	cfg := testConfig()
	old := aliasRowsFor(t, cfg, "alias/x", config.AliasTarget{Model: "ollama/gemma:9b", Order: 1})
	f, _ := newFakeDB(t)
	for _, m := range old {
		f.seed(storedAlias(m))
	}
	c, counts := aliasRowCounts(t, f, "alias/x")
	next := aliasRowsFor(t, cfg, "alias/x",
		config.AliasTarget{Model: "openrouter/x/y", Order: 1}, config.AliasTarget{Model: "ollama/gemma:9b", Order: 2})
	cur, err := c.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p := PlanAliasDB(map[string][]DBModel{"alias/x": next}, cur, testMasterKey, func(string, string) bool { return false }, true)
	f.mu.Lock()
	f.log = nil
	f.mu.Unlock()
	*counts = nil
	added, deleted, warnings, err := c.ApplyAliasDB(context.Background(), p)
	if err != nil || len(warnings) != 0 || added != 2 || deleted != 1 {
		t.Fatalf("added %d deleted %d warnings %v err %v", added, deleted, warnings, err)
	}
	lastAdd, firstDelete, relist := -1, -1, -1
	for i, l := range f.log {
		switch l {
		case "POST /model/new":
			lastAdd = i
		case "POST /model/delete":
			if firstDelete < 0 {
				firstDelete = i
			}
		case "GET /model/info":
			if lastAdd >= 0 && firstDelete < 0 {
				relist = i
			}
		}
	}
	if lastAdd < 0 || firstDelete < 0 || relist < 0 || !(lastAdd < relist && relist < firstDelete) {
		t.Fatalf("call log %v: want every add, then a re-list, then the deletes", f.log)
	}
	for i, n := range *counts {
		if n == 0 {
			t.Fatalf("after call %d (%s) the alias had no row; log %v", i, f.log[i], f.log)
		}
	}
	if got := f.ids(); !slices.Equal(got, planIDs(sortedByID(next))) {
		t.Fatalf("rows %v, want %v", got, planIDs(next))
	}
	again, err := c.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if p := PlanAliasDB(map[string][]DBModel{"alias/x": next}, again, testMasterKey, func(string, string) bool { return false }, true); !p.Empty() {
		t.Fatalf("not converged: %+v", p)
	}
}

func sortedByID(ms []DBModel) []DBModel {
	out := slices.Clone(ms)
	slices.SortFunc(out, func(a, b DBModel) int { return strings.Compare(a.ID, b.ID) })
	return out
}

// TestAliasRepointAddFailureKeepsTheOldRows checks Review Focus 2's failure
// half: when /model/new answers 500 (and the re-list does not show the row),
// no delete is sent, the alias keeps its old row, and one warning says the
// next sync converges. Deleting first would unroute every client session.
func TestAliasRepointAddFailureKeepsTheOldRows(t *testing.T) {
	cfg := testConfig()
	old := aliasRowsFor(t, cfg, "alias/x", config.AliasTarget{Model: "ollama/gemma:9b", Order: 1})[0]
	next := aliasRowsFor(t, cfg, "alias/x", config.AliasTarget{Model: "openrouter/x/y", Order: 1})[0]
	f, c := newFakeDB(t)
	f.seed(storedAlias(old))
	f.failAddID = next.ID
	cur, err := c.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p := PlanAliasDB(map[string][]DBModel{"alias/x": {next}}, cur, testMasterKey, func(string, string) bool { return false }, true)
	added, deleted, warnings, err := c.ApplyAliasDB(context.Background(), p)
	if err != nil || added != 0 || deleted != 0 || len(warnings) != 1 {
		t.Fatalf("added %d deleted %d warnings %v err %v", added, deleted, warnings, err)
	}
	if !strings.Contains(warnings[0], "alias/x") {
		t.Fatalf("warning %q should name the alias", warnings[0])
	}
	if strings.Contains(warnings[0], "sk-test") {
		t.Fatalf("warning %q carries a key", warnings[0])
	}
	if f.requests("POST /model/delete") != 0 {
		t.Fatalf("log %v: a delete was sent", f.log)
	}
	if got := f.ids(); !slices.Equal(got, []string{old.ID}) {
		t.Fatalf("rows %v, want the old row only", got)
	}
}

// TestApplyAliasDBRefusesForeignIDs checks that ApplyAliasDB touches no row
// outside the alias id space, whatever plan it is handed: a wt: id or a UI
// id in any list is refused with no HTTP call, and a listed row under the
// alias prefix without the alias marker is never deleted.
func TestApplyAliasDBRefusesForeignIDs(t *testing.T) {
	f, c := newFakeDB(t)
	f.seed(stored(managedRow("wt:ollama/a", "ollama/a", map[string]any{"model": "ollama/a"})))
	_, deleted, _, err := c.ApplyAliasDB(context.Background(), AliasDBPlan{Aliases: []AliasRowPlan{
		{Alias: "alias/x", Delete: []string{"wt:ollama/a", "ui-row"}},
	}})
	if err == nil || deleted != 0 || f.calls != 0 {
		t.Fatalf("deleted %d err %v calls %d", deleted, err, f.calls)
	}
	unmarked := DBModel{ID: DBAliasIDPrefix + "x/00000000/ollama/a", ModelName: "alias/x", Params: map[string]any{"model": "ollama/a"}, Info: map[string]any{AliasKey: "alias/x"}}
	f.seed(unmarked)
	_, deleted, warnings, err := c.ApplyAliasDB(context.Background(), AliasDBPlan{Aliases: []AliasRowPlan{
		{Alias: "alias/x", Delete: []string{unmarked.ID}},
	}})
	if err != nil || deleted != 0 || len(warnings) != 1 || f.requests("POST /model/delete") != 0 {
		t.Fatalf("deleted %d warnings %v err %v log %v", deleted, warnings, err, f.log)
	}
}

// TestPhase1PredicatesExcludeAliasRows checks Review Focus 1: an alias row
// fails phase 1's ownership test (isManagedDB plus the wt: prefix), so
// PlanDB never deletes it and ApplyDB refuses its id. Every shipped phase 1
// binary uses the same predicates; a row they matched would be deleted by
// every plain sync, start or stop.
func TestPhase1PredicatesExcludeAliasRows(t *testing.T) {
	r := storedAlias(aliasRowsFor(t, testConfig(), "alias/x", config.AliasTarget{Model: "ollama/gemma:9b", Order: 1})[0])
	if isManagedDB(r) && strings.HasPrefix(r.ID, DBIDPrefix) {
		t.Fatalf("alias row %+v passes the phase 1 ownership test", r)
	}
	if p := PlanDB(nil, []DBModel{r}, testMasterKey); !p.Empty() {
		t.Fatalf("PlanDB %+v, want empty", p)
	}
	want := managedRow("wt:ollama/gemma:9b", "ollama/gemma:9b", map[string]any{"model": "ollama/gemma:9b"})
	if p := PlanDB([]DBModel{want}, []DBModel{r}, testMasterKey); len(p.Delete)+len(p.Update) != 0 {
		t.Fatalf("PlanDB %+v touches the alias row", p)
	}
	f, c := newFakeDB(t)
	f.seed(r)
	_, _, deleted, err := c.ApplyDB(context.Background(), DBPlan{Delete: []string{r.ID}})
	if err == nil || deleted != 0 || f.calls != 0 {
		t.Fatalf("ApplyDB deleted %d err %v calls %d, want a refusal with no call", deleted, err, f.calls)
	}
}

// TestSyncLeavesAliasRowsToTheAliasPath checks that the route write's own
// views of the database (owned, routeRows, the foreign-id warning and
// AdminClient.Routes) leave alias rows out and ownedAliases holds exactly
// them: a sync that planned against an alias row would delete it as stale.
func TestSyncLeavesAliasRowsToTheAliasPath(t *testing.T) {
	alias := storedAlias(aliasRowsFor(t, testConfig(), "alias/x", config.AliasTarget{Model: "ollama/gemma:9b", Order: 1})[0])
	model := stored(managedRow("wt:ollama/gemma:9b", "ollama/gemma:9b", map[string]any{"model": "ollama/gemma:9b"}))
	d := &dbRoutes{current: []DBModel{alias, model}, listed: true}
	if got := planIDs(d.owned()); !slices.Equal(got, []string{model.ID}) {
		t.Fatalf("owned %v", got)
	}
	if got := d.routeRows(); len(got) != 1 || got[0].ID != "ollama/gemma:9b" {
		t.Fatalf("routeRows %+v", got)
	}
	if w := d.foreignWarning(); w != "" {
		t.Fatalf("foreign warning %q", w)
	}
	if got := planIDs(d.ownedAliases()); !slices.Equal(got, []string{alias.ID}) {
		t.Fatalf("ownedAliases %v", got)
	}
	f, c := newFakeDB(t)
	f.seed(alias)
	f.seed(model)
	names, err := c.Routes(context.Background())
	if err != nil || !slices.Equal(names, []string{"ollama/gemma:9b"}) {
		t.Fatalf("Routes %v err %v", names, err)
	}
}

// TestApplyAliasDBReplacesAnEditedRow checks the Replace path end to end: a
// row whose stamp was changed outside wt is deleted and re-added with the
// right hash, after which the plan is empty (converged).
func TestApplyAliasDBReplacesAnEditedRow(t *testing.T) {
	want := aliasRowsFor(t, testConfig(), "alias/x", config.AliasTarget{Model: "ollama/gemma:9b", Order: 1})[0]
	edited := storedAlias(want)
	edited.Info[ParamsHashKey] = "not-the-hash"
	f, c := newFakeDB(t)
	f.seed(edited)
	keep := func(string, string) bool { return false }
	p := PlanAliasDB(map[string][]DBModel{"alias/x": {want}}, []DBModel{edited}, testMasterKey, keep, true)
	added, deleted, warnings, err := c.ApplyAliasDB(context.Background(), p)
	if err != nil || len(warnings) != 0 || added != 1 || deleted != 1 {
		t.Fatalf("added %d deleted %d warnings %v err %v", added, deleted, warnings, err)
	}
	cur, err := c.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if again := PlanAliasDB(map[string][]DBModel{"alias/x": {want}}, cur, testMasterKey, keep, true); !again.Empty() {
		t.Fatalf("not converged: %+v", again)
	}
}

// TestRedirectAliasGetsRowsEqualToItsTargets pins #687 decision 3: an alias
// with redirect_to builds rows from its target alias's targets (same target,
// same order, same params), each named for the old id, so the old id keeps
// routing with no gap after a rename; a redirect whose target alias is gone
// builds none and says why. If the planner read a.Targets, the old id would
// lose its rows the moment it became a redirect.
func TestRedirectAliasGetsRowsEqualToItsTargets(t *testing.T) {
	cfg := testConfig()
	cfg.Aliases = []config.Alias{
		{ID: "alias/new", Targets: []config.AliasTarget{{Model: "ollama/gemma:9b", Order: 2}}},
		{ID: "alias/old", RedirectTo: "alias/new"},
	}
	oldA, _ := cfg.AliasByID("alias/old")
	newA, _ := cfg.AliasByID("alias/new")
	oldRows, skips := DBModelsFromAlias(cfg, oldA, allLive, testMasterKey)
	newRows, _ := DBModelsFromAlias(cfg, newA, allLive, testMasterKey)
	if len(skips) != 0 || len(oldRows) != 1 || len(newRows) != 1 {
		t.Fatalf("old rows %+v skips %+v, new rows %+v", oldRows, skips, newRows)
	}
	o, n := oldRows[0], newRows[0]
	if o.ModelName != "alias/old" || o.Info[AliasKey] != "alias/old" || o.Info[AliasTargetKey] != n.Info[AliasTargetKey] || o.Params["order"] != n.Params["order"] || o.Params["model"] != n.Params["model"] {
		t.Errorf("redirect row %+v differs from its target's %+v beyond the name", o, n)
	}
	cfg.Aliases = cfg.Aliases[1:]
	rows, _ := DBModelsFromAlias(cfg, cfg.Aliases[0], allLive, testMasterKey)
	if len(rows) != 0 {
		t.Errorf("a redirect to a missing alias built rows %+v", rows)
	}
}
