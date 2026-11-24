package litellm

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// cloudSentinel is the labelled provider key these tests route; it must never
// reach an output (Review Focus 2). testConfig's openrouter provider carries
// it as its secret_ref, which BuildEntry writes as the row's api_key.
const cloudSentinel = "wtSENTINEL-provider-key"

const cloudID = "openrouter/x/y"

func sentinelConfig() *config.Config {
	cfg := testConfig()
	cfg.Providers[2].Auth.SecretRef = cloudSentinel
	return cfg
}

// cloudRow builds the desired database row of a cloud id the way the sync does.
func cloudRow(t *testing.T, cfg *config.Config, id string) DBModel {
	t.Helper()
	m, err := DBModelFromRow(mustRow(t, cfg, id))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// TestDBEligible pins which ids the database half may carry: local rows and
// registry cloud models (a Claude subscription model included); never a native
// model, an id the registry lacks, or a provider without a LiteLLM mapping.
func TestDBEligible(t *testing.T) {
	cfg := subscriptionConfig()
	cfg.Models = append(cfg.Models, config.Model{ID: "nomap/m", ProviderID: "nomap", ModelName: "m", Location: config.LocationCloud})
	cfg.Providers = append(cfg.Providers, config.Provider{ID: "nomap", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "api_key", SecretRef: "k"}})
	for id, want := range map[string]bool{
		"ollama/gemma:9b":      true,  // local
		"mtplx/Youssofal--Q":   true,  // local
		"openrouter/x/y":       true,  // registry cloud
		subID:                  true,  // keyless subscription row
		"claude/sonnet":        false, // native
		"openrouter/not-there": false, // not in the registry
		"hand/written":         false,
		"nomap/m":              false, // no LiteLLM mapping
		"ollama/discovered:1b": true,  // a discovered local id (prefix)
	} {
		if got := dbEligible(cfg, id); got != want {
			t.Errorf("dbEligible(%q) = %v, want %v", id, got, want)
		}
	}
}

// TestCloudRowDesiredShape (Task 1): the desired database row of a cloud
// model is wt:<id>, marked managed, carries the resolved key (decision 1) and
// the model_info the file row had; its stamp is stable across runs, changes
// with the key and never contains it.
func TestCloudRowDesiredShape(t *testing.T) {
	cfg := sentinelConfig()
	a := cloudRow(t, cfg, cloudID)
	if a.ID != "wt:"+cloudID || a.ModelName != cloudID || !isManagedDB(a) {
		t.Fatalf("row %+v, want id wt:%s, managed", a, cloudID)
	}
	if a.Params["api_key"] != cloudSentinel || a.Params["model"] != cloudID {
		t.Fatalf("params %v, want the resolved key (decision 1)", a.Params)
	}
	if a.Info["input_cost_per_token"] != 0.25 {
		t.Fatalf("model_info %v, want the registry's own pricing", a.Info)
	}
	h := routeHash(a, testMasterKey)
	if h == "" || h != routeHash(cloudRow(t, cfg, cloudID), testMasterKey) {
		t.Fatalf("stamp %q is not stable across runs", h)
	}
	if strings.Contains(h, cloudSentinel) {
		t.Fatalf("stamp %q contains the key", h)
	}
	other := sentinelConfig()
	other.Providers[2].Auth.SecretRef = cloudSentinel + "-rotated"
	if routeHash(cloudRow(t, other, cloudID), testMasterKey) == h {
		t.Fatal("stamp did not change with the key")
	}
	if routeHash(a, testMasterKey+"-rotated") == h {
		t.Fatal("stamp did not change with the proxy key it is keyed with")
	}
}

// TestSyncAddsACloudRowToTheDatabaseOnly (Task 2): a registry cloud model is
// added as a database row with no config.yaml write and no restart; a second
// sync is a no-op; a changed key replaces the row once; a model removed from
// the registry loses its row.
func TestSyncAddsACloudRowToTheDatabaseOnly(t *testing.T) {
	body := "model_list: []\n" + settledSettings
	o, restarts, p, fake := dbOpts(t, body)
	cfg := sentinelConfig()
	res, err := Sync(cfg, nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if got := fake.ids(); !slices.Equal(got, []string{"wt:" + cloudID}) {
		t.Fatalf("database rows %v", got)
	}
	if readFile(t, p) != body || *restarts != 0 || res.Changed || res.Via != "db" {
		t.Fatalf("file changed=%v restarts=%d via=%q, want an untouched file, no restart, db", res.Changed, *restarts, res.Via)
	}
	if oc, ok := outcomeFor(res, cloudID); !ok || oc.Action != "routed" || !oc.Written {
		t.Fatalf("outcomes %+v", res.Outcomes)
	}

	writes := fake.requests("POST /model/new") + fake.requests("POST /model/delete")
	if res, err = Sync(cfg, nil, o); err != nil {
		t.Fatal(err)
	}
	if again := fake.requests("POST /model/new") + fake.requests("POST /model/delete"); again != writes || len(res.Outcomes) != 0 {
		t.Fatalf("second sync wrote (%d→%d) or reported %+v", writes, again, res.Outcomes)
	}

	cfg.Providers[2].Auth.SecretRef = cloudSentinel + "-rotated"
	res, err = Sync(cfg, nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if oc, ok := outcomeFor(res, cloudID); !ok || oc.Action != "rewritten" || *restarts != 0 {
		t.Fatalf("outcomes %+v restarts=%d, want a rewritten row and no restart", res.Outcomes, *restarts)
	}
	if k := fake.rows["wt:"+cloudID]["litellm_params"].(map[string]any)["api_key"]; k != cloudSentinel+"-rotated" {
		t.Fatalf("the stored row keeps the old key")
	}

	cfg.Models = slices.DeleteFunc(cfg.Models, func(m config.Model) bool { return m.ID == cloudID })
	res, err = Sync(cfg, nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if got := fake.ids(); len(got) != 0 || *restarts != 0 {
		t.Fatalf("database rows %v restarts=%d, want the row removed and no restart", got, *restarts)
	}
	if oc, ok := outcomeFor(res, cloudID); !ok || oc.Action != "unrouted" {
		t.Fatalf("outcomes %+v", res.Outcomes)
	}
}

// TestHandWrittenCloudRowGetsNoDatabaseTwin (Task 2): an unmarked config.yaml
// row under a cloud id is the user's; no database row is added beside it, and
// one warning names the id.
func TestHandWrittenCloudRowGetsNoDatabaseTwin(t *testing.T) {
	body := "model_list:\n  - model_name: " + cloudID + "\n    litellm_params: {model: " + cloudID + "}\n" + settledSettings
	o, restarts, p, fake := dbOpts(t, body)
	cfg := sentinelConfig()
	res, err := Sync(cfg, nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.ids()) != 0 || *restarts != 0 {
		t.Fatalf("database rows %v restarts=%d, want a hand-written row left alone", fake.ids(), *restarts)
	}
	_ = p
	if !hasWarning(res, cloudID+": config.yaml has a hand-written row") {
		t.Fatalf("warnings %q", res.Warnings)
	}
}

// TestCloudMigrationAddsToDatabaseBeforeRemovingFromFile (Task 3, Review Focus
// 1): a config.yaml with a wt-written cloud row and a hand-written one: the
// first sync adds the database row, confirms it by listing, removes only the
// wt-written file row and restarts once; the second sync does nothing.
func TestCloudMigrationAddsToDatabaseBeforeRemovingFromFile(t *testing.T) {
	cfg := sentinelConfig()
	seed, _, sp := opts(t, "model_list: []\n"+settledSettings)
	if _, err := Sync(cfg, nil, seed); err != nil { // the file path writes the wt row
		t.Fatal(err)
	}
	// A hand-written row beside the wt one: written as a flow row, as the file is.
	body := strings.Replace(readFile(t, sp), "model_list: [", "model_list: [{model_name: hand/made, litellm_params: {model: openai/hand, api_base: 'http://localhost:9'}}, ", 1)
	if !strings.Contains(body, "hand/made") {
		t.Fatalf("fixture did not take the hand-written row:\n%s", body)
	}
	o, restarts, p, fake := dbOpts(t, body)
	var logAtSave, idsAtSave []string
	o.beforeSave = func() error {
		fake.mu.Lock()
		logAtSave = slices.Clone(fake.log)
		fake.mu.Unlock()
		idsAtSave = fake.ids()
		return nil
	}
	res, err := Sync(cfg, nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(idsAtSave, []string{"wt:" + cloudID}) {
		t.Fatalf("database rows when config.yaml was saved: %v, want the cloud row", idsAtSave)
	}
	add := slices.Index(logAtSave, "POST /model/new")
	if add < 0 || !slices.Contains(logAtSave[add+1:], "GET /model/info") {
		t.Fatalf("requests before the save %v, want the add confirmed by a listing", logAtSave)
	}
	f, _ := Open(p)
	if got := f.RoutedIDs(); !slices.Equal(got, []string{"hand/made"}) || f.row(cloudID) != nil {
		t.Fatalf("config.yaml rows %v, want only the hand-written one", got)
	}
	if *restarts != 1 || !res.Changed || res.Via != "db" {
		t.Fatalf("restarts=%d changed=%v via=%q, want 1/true/db", *restarts, res.Changed, res.Via)
	}
	if oc, ok := outcomeFor(res, cloudID); !ok || oc.Action != "rewritten" || !oc.Written {
		t.Fatalf("outcomes %+v, want the cloud row reported once as rewritten", res.Outcomes)
	}
	if res, err = Sync(cfg, nil, o); err != nil || res.Changed || *restarts != 1 || len(res.Outcomes) != 0 {
		t.Fatalf("second sync: err=%v changed=%v restarts=%d outcomes=%+v, want nothing", err, res.Changed, *restarts, res.Outcomes)
	}
}

// TestCloudMigrationNeverLeavesTheModelUnrouted (Review Focus 1): when the
// database refuses the add, the wt row stays in config.yaml (no write, no
// restart), one warning says why, and the next healthy sync moves it.
func TestCloudMigrationNeverLeavesTheModelUnrouted(t *testing.T) {
	cfg := sentinelConfig()
	seed, _, sp := opts(t, "model_list: []\n"+settledSettings)
	if _, err := Sync(cfg, nil, seed); err != nil {
		t.Fatal(err)
	}
	body := readFile(t, sp)
	o, restarts, p, fake := dbOpts(t, body)
	fake.addFailBody = realStoreOffBody
	res, err := Sync(cfg, nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if readFile(t, p) != body || *restarts != 0 || res.Via != "file" || !hasWarning(res, "); used config.yaml") {
		t.Fatalf("restarts=%d via=%q warnings=%q, want the file untouched and the fallback line", *restarts, res.Via, res.Warnings)
	}
	fake.addFailBody = ""
	if _, err := Sync(cfg, nil, o); err != nil {
		t.Fatal(err)
	}
	f, _ := Open(p)
	if got := f.RoutedIDs(); len(got) != 0 || !slices.Equal(fake.ids(), []string{"wt:" + cloudID}) || *restarts != 1 {
		t.Fatalf("after the healthy sync: file %v database %v restarts=%d", got, fake.ids(), *restarts)
	}
}

// TestCloudFallbackWhenTheDatabaseCannotCarryTheRow (Task 4): with database
// models off, or no admin client at all, a cloud add takes today's file path
// and restart, and the first says why in one line. A proxy that does not
// answer the listing is held instead: nothing is written beside a database
// row that may exist, and one warning names the ids.
func TestCloudFallbackWhenTheDatabaseCannotCarryTheRow(t *testing.T) {
	cfg := sentinelConfig()
	t.Run("store off", func(t *testing.T) {
		off := strings.Replace(settledSettings, "store_model_in_db: true", "store_model_in_db: false", 1)
		o, restarts, p, fake := dbOpts(t, "model_list: []\n"+off)
		res, err := Sync(cfg, nil, o)
		if err != nil {
			t.Fatal(err)
		}
		f, _ := Open(p)
		if len(fake.log) != 0 || !slices.Equal(f.RoutedIDs(), []string{cloudID}) || *restarts != 1 || res.Via != "file" {
			t.Fatalf("requests %v rows %v restarts=%d via=%q, want the file path", fake.log, f.RoutedIDs(), *restarts, res.Via)
		}
		if !hasWarning(res, "database routes are off") {
			t.Fatalf("warnings %q", res.Warnings)
		}
	})
	t.Run("no admin client", func(t *testing.T) {
		o, restarts, p := opts(t, "model_list: []\n"+settledSettings)
		if _, err := Sync(cfg, nil, o); err != nil {
			t.Fatal(err)
		}
		f, _ := Open(p)
		if !slices.Equal(f.RoutedIDs(), []string{cloudID}) || *restarts != 1 {
			t.Fatalf("rows %v restarts=%d", f.RoutedIDs(), *restarts)
		}
	})
	t.Run("proxy does not answer", func(t *testing.T) {
		body := "model_list: []\n" + settledSettings
		o, restarts, p, fake := dbOpts(t, body)
		fake.failList = true
		res, err := Sync(cfg, nil, o)
		if err != nil {
			t.Fatal(err)
		}
		if readFile(t, p) != body || *restarts != 0 {
			t.Fatalf("file changed or restarts=%d, want a held row and no write", *restarts)
		}
		if !hasWarning(res, "not routed until the database answers: "+cloudID) {
			t.Fatalf("warnings %q", res.Warnings)
		}
		fake.failList = false
		if _, err := Sync(cfg, nil, o); err != nil || !slices.Equal(fake.ids(), []string{"wt:" + cloudID}) {
			t.Fatalf("after the proxy answers: err=%v rows %v", err, fake.ids())
		}
	})
}

// TestLaunchEnsureFindsTheDatabaseRow: the launch-time ensure of a cloud model
// whose row sync moved to the database has no file row but must not write a
// file twin (and restart the proxy for it); when the database lacks the row,
// the ensure adds it there with no restart.
func TestLaunchEnsureFindsTheDatabaseRow(t *testing.T) {
	cfg := sentinelConfig()
	body := "model_list: []\n" + settledSettings
	o, restarts, p, fake := dbOpts(t, body)
	ch := Change{Add: localFor(cfg, cloudID), AddMissingOnly: true}
	if _, err := ApplyChange(cfg, ch, o); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fake.ids(), []string{"wt:" + cloudID}) || *restarts != 0 || readFile(t, p) != body {
		t.Fatalf("rows %v restarts=%d: want one database add and no file write", fake.ids(), *restarts)
	}
	adds := fake.requests("POST /model/new")
	// The shell holds another key now: a row that is there is sync's, neither
	// rebuilt nor compared.
	cfg.Providers[2].Auth.SecretRef = "a-different-key"
	if _, err := ApplyChange(cfg, ch, o); err != nil {
		t.Fatal(err)
	}
	if fake.requests("POST /model/new") != adds || fake.requests("POST /model/delete") != 0 || *restarts != 0 || readFile(t, p) != body {
		t.Fatalf("the second ensure wrote: adds %d→%d", adds, fake.requests("POST /model/new"))
	}
	if k := fake.rows["wt:"+cloudID]["litellm_params"].(map[string]any)["api_key"]; k != cloudSentinel {
		t.Fatalf("the ensure replaced sync's key")
	}
}

// TestSubscriptionRowFollowsTheGateInTheDatabase (Task 5, Review Focus 3): a
// clean gate adds the keyless row to the database; a seen hazard deletes it
// and adds none; a gate that could not look keeps it and adds none.
func TestSubscriptionRowFollowsTheGateInTheDatabase(t *testing.T) {
	reg := subscriptionConfig()
	o, restarts, p, fake := dbOpts(t, "model_list: []\n"+settledSettings)
	o = cleanProxy(o)
	before := readFile(t, p)

	// Could not look, nothing routed yet: none is added (fail closed).
	failing := o
	failing.ModelInfo = func(context.Context) ([]DeploymentInfo, error) { return nil, fmt.Errorf("503") }
	if _, err := Sync(reg, nil, failing); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(fake.ids(), DBID(subID)) {
		t.Fatalf("rows %v: a subscription row was added without a look", fake.ids())
	}

	if _, err := Sync(reg, nil, o); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(fake.ids(), DBID(subID)) || readFile(t, p) != before || *restarts != 0 {
		t.Fatalf("rows %v restarts=%d, want the row in the database only", fake.ids(), *restarts)
	}
	if _, ok := fake.rows[DBID(subID)]["litellm_params"].(map[string]any)["api_key"]; ok {
		t.Fatalf("the subscription row carries a key: %v", fake.rows[DBID(subID)])
	}

	// Could not look, row present: kept, none added.
	if _, err := Sync(reg, nil, failing); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(fake.ids(), DBID(subID)) || fake.requests("POST /model/delete") != 0 {
		t.Fatalf("rows %v deletes=%d, want the row kept", fake.ids(), fake.requests("POST /model/delete"))
	}

	// A seen hazard: deleted, with no restart (it is a database delete).
	hazard := o
	hazard.ProxyEnv = func() ProxyEnv { return ProxyEnvOf("test env", map[string]string{"ANTHROPIC_API_KEY": cloudSentinel}) }
	res, err := Sync(reg, nil, hazard)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(fake.ids(), DBID(subID)) || *restarts != 0 {
		t.Fatalf("rows %v restarts=%d, want the row deleted under a hazard", fake.ids(), *restarts)
	}
	if ws := subscriptionWarnings(res.Warnings); len(ws) != 1 || strings.Contains(ws[0], cloudSentinel) {
		t.Fatalf("subscription warnings %q, want one that does not hold the value", ws)
	}
}

// TestNoSecretReachesAnOutput (Task 6, Review Focus 2): with a sentinel
// provider key, none of the dry run, the sync result, a failed add's message
// or the stored stamp holds it, and the stored row holds it only as the key
// LiteLLM was sent.
func TestNoSecretReachesAnOutput(t *testing.T) {
	cfg := sentinelConfig()
	o, _, _, fake := dbOpts(t, "model_list: []\n"+settledSettings)

	plan, err := PlanSync(cfg, nil, o)
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut strings.Builder
	for _, asJSON := range []bool{false, true} {
		if err := ReportSyncPlan(&out, &errOut, plan, plan.Warnings, asJSON); err != nil {
			t.Fatal(err)
		}
	}
	if s := out.String() + errOut.String() + fmt.Sprintf("%+v", plan); strings.Contains(s, cloudSentinel) {
		t.Fatalf("the dry run holds the key:\n%s", s)
	}

	// A failed add whose body echoes the request, as a proxy may.
	fake.addFailBody = `{"error":{"message":"bad request: api_key=` + cloudSentinel + `"}}`
	res, err := Sync(cfg, nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if s := fmt.Sprintf("%+v %q", res, res.Warnings); strings.Contains(s, cloudSentinel) {
		t.Fatalf("the sync result holds the key: %s", s)
	}

	fake.addFailBody = ""
	if res, err = Sync(cfg, nil, o); err != nil {
		t.Fatal(err)
	}
	if s := fmt.Sprintf("%+v", res); strings.Contains(s, cloudSentinel) {
		t.Fatalf("the sync result holds the key: %s", s)
	}
	row := fake.rows["wt:"+cloudID]
	info, _ := json.Marshal(row["model_info"])
	if strings.Contains(string(info), cloudSentinel) {
		t.Fatalf("the stored model_info (the stamp) holds the key: %s", info)
	}
	listed, err := o.Admin.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(listed); strings.Contains(string(b), cloudSentinel) {
		t.Fatalf("the listing holds the key: %s", b)
	}
}

// TestSaltKeyRefusalNamesTheKeyAndDeletesNothingUnlisted (spike question 3b,
// Review Focus: never delete a row the listing does not show): LiteLLM lists
// none of wt's rows (stored under another salt key) and answers the add of an
// existing id with HTTP 500. The sync falls back with one warning that names
// LITELLM_SALT_KEY, and no delete is sent for a row the listing does not show.
func TestSaltKeyRefusalNamesTheKeyAndDeletesNothingUnlisted(t *testing.T) {
	body := "model_list: []\n" + settledSettings
	o, _, _, fake := dbOpts(t, body)
	fake.addFailBody = realDuplicateBody
	res, err := Sync(sentinelConfig(), nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if !hasWarning(res, "LITELLM_SALT_KEY") {
		t.Fatalf("warnings %q, want one naming the salt key", res.Warnings)
	}
	if n := fake.requests("POST /model/delete"); n != 0 {
		t.Fatalf("%d deletes sent for rows the listing does not show", n)
	}
}
