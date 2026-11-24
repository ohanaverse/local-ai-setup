package litellm

import (
	"slices"
	"strings"
	"testing"
)

// apiWrites counts the fake's write calls: a dry run makes none.
func apiWrites(f *fakeDBServer) int {
	return f.requests("POST /model/new") + f.requests("POST /model/delete")
}

// TestPlanSyncSeesDatabaseRoutes pins that the dry run reads the database
// (#598): a running local model whose route already lives there, current, is
// not "would route" (it was, on every run, before the dry run looked), and a
// stale database route is "would unroute". It lists only: no write, and the
// file is untouched.
func TestPlanSyncSeesDatabaseRoutes(t *testing.T) {
	body := "model_list: []\n" + settledSettings
	o, restarts, p, fake := dbOpts(t, body)
	cfg := testConfig()
	// One real start writes the current database row for gemma.
	if _, err := ApplyChange(cfg, Change{Add: localFor(cfg, "ollama/gemma:9b")}, o); err != nil {
		t.Fatal(err)
	}
	fake.seed(stored(managedRow("wt:ollama/old:1b", "ollama/old:1b", map[string]any{"model": "ollama_chat/old:1b"})))
	writes := apiWrites(fake)

	plan, err := PlanSync(cfg, localFor(cfg, "ollama/gemma:9b"), o)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(plan.Add, "ollama/gemma:9b") {
		t.Fatalf("plan.Add = %v, want the current database route left out", plan.Add)
	}
	if !slices.Contains(plan.Remove, "ollama/old:1b") {
		t.Fatalf("plan.Remove = %v, want the stale database route", plan.Remove)
	}
	if apiWrites(fake) != writes || *restarts != 0 || readFile(t, p) != body {
		t.Fatalf("dry run wrote: api writes %d→%d restarts %d", writes, apiWrites(fake), *restarts)
	}
	ids, err := DesiredIDs(cfg, localFor(cfg, "ollama/gemma:9b"), o)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(ids, "ollama/gemma:9b") || slices.Contains(ids, "ollama/old:1b") {
		t.Fatalf("DesiredIDs = %v, want gemma (from the database) and not old", ids)
	}
}

// TestPlanSyncPreviewsTheMigration pins the dry run's preview of the one-time
// move (#598, #694): wt's marked rows, the cloud row too, are listed as "would move to the
// database", not as routes to add, and the plan says the file would change
// (one proxy restart). Nothing is written anywhere.
func TestPlanSyncPreviewsTheMigration(t *testing.T) {
	body := markedLocalRows(t)
	o, restarts, p, fake := dbOpts(t, body)
	cfg := testConfig()
	plan, err := PlanSync(cfg, localFor(cfg, "ollama/gemma:9b", "mtplx/Youssofal--Q"), o)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"mtplx/Youssofal--Q", "ollama/gemma:9b", "openrouter/x/y"}; !slices.Equal(plan.Move, want) {
		t.Fatalf("plan.Move = %v, want %v", plan.Move, want)
	}
	if len(plan.Add) != 0 || !plan.Restart || plan.EnableDB {
		t.Fatalf("plan add=%v restart=%v enable=%v, want no add, a restart, no enabling", plan.Add, plan.Restart, plan.EnableDB)
	}
	if apiWrites(fake) != 0 || *restarts != 0 || readFile(t, p) != body {
		t.Fatal("the dry run wrote")
	}
	var out, errOut strings.Builder
	if err := ReportSyncPlan(&out, &errOut, plan, nil, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "ollama/gemma:9b: would move to the database\n") ||
		!strings.Contains(out.String(), "the proxy would restart once") {
		t.Fatalf("text = %q", out.String())
	}
}

// TestPlanSyncPreviewsEnablingDatabaseRoutes pins the first-time case: a
// config.yaml without store_model_in_db gets the key on the next sync (one
// restart) and the rows move on the sync after. The dry run says so and calls
// no API at all (the proxy would refuse a database write anyway).
func TestPlanSyncPreviewsEnablingDatabaseRoutes(t *testing.T) {
	body := withoutStoreKey(markedLocalRows(t))
	o, _, p, fake := dbOpts(t, body)
	cfg := testConfig()
	plan, err := PlanSync(cfg, localFor(cfg, "ollama/gemma:9b", "mtplx/Youssofal--Q"), o)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.EnableDB || !plan.Restart || len(plan.Move) != 0 {
		t.Fatalf("plan enable=%v restart=%v move=%v, want enabling, a restart, no move yet", plan.EnableDB, plan.Restart, plan.Move)
	}
	if fake.calls != 0 || readFile(t, p) != body {
		t.Fatalf("API calls %d, want none; file untouched", fake.calls)
	}
	var out, errOut strings.Builder
	if err := ReportSyncPlan(&out, &errOut, plan, nil, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "would set general_settings.store_model_in_db: true") {
		t.Fatalf("text = %q", out.String())
	}
}

// TestPlanSyncSaysWhenTheDatabaseCannotBeListed pins that a dry run whose
// listing fails still plans from config.yaml and says, in one warning, that
// the database half is not in the plan.
func TestPlanSyncSaysWhenTheDatabaseCannotBeListed(t *testing.T) {
	o, _, _, fake := dbOpts(t, "model_list: []\n"+settledSettings)
	fake.failList = true
	cfg := testConfig()
	plan, err := PlanSync(cfg, localFor(cfg, "ollama/gemma:9b"), o)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, w := range plan.Warnings {
		if strings.Contains(w, "database routes unavailable") {
			n++
		}
	}
	if n != 1 || apiWrites(fake) != 0 {
		t.Fatalf("warnings %q (writes %d), want one unavailable line and no write", plan.Warnings, apiWrites(fake))
	}
}

// TestPlanSyncHoldsCloudAddsWhenTheDatabaseCannotBeListed pins that a dry run
// whose listing fails does not plan a file write for a cloud add: the real
// sync holds it (holdCloud), because the database may already serve it.
func TestPlanSyncHoldsCloudAddsWhenTheDatabaseCannotBeListed(t *testing.T) {
	o, _, _, fake := dbOpts(t, "model_list: []\n"+settledSettings)
	fake.failList = true
	cfg := testConfig()
	plan, err := PlanSync(cfg, nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(plan.Add, "openrouter/x/y") {
		t.Fatalf("plan adds %v, want the cloud row held", plan.Add)
	}
	if !slices.ContainsFunc(plan.Warnings, func(w string) bool {
		return strings.Contains(w, "not routed until the database answers: openrouter/x/y")
	}) {
		t.Fatalf("warnings %q, want the held line", plan.Warnings)
	}
}
