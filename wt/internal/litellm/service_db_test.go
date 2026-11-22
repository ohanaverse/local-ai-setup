package litellm

import (
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// settledSettings is the settings block EnsureSettings leaves alone, so a
// write that changes no row leaves config.yaml byte-identical.
const settledSettings = "litellm_settings:\n  drop_params: true\n  use_chat_completions_url_for_anthropic_messages: true\ngeneral_settings:\n  cancel_on_disconnect: true\n"

// dbOpts is opts with Options.Admin pointed at a fresh fake LiteLLM model API.
func dbOpts(t *testing.T, body string) (Options, *int, string, *fakeDBServer) {
	t.Helper()
	o, restarts, p := opts(t, body)
	f, c := newFakeDB(t)
	o.Admin = c
	return o, restarts, p, f
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// requests counts the fake's requests matching "METHOD /path".
func (f *fakeDBServer) requests(what string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, l := range f.log {
		if l == what {
			n++
		}
	}
	return n
}

func (f *fakeDBServer) ids() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for id := range f.rows {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

func outcomeFor(res Result, id string) (Outcome, bool) {
	for _, oc := range res.Outcomes {
		if oc.ID == id {
			return oc, true
		}
	}
	return Outcome{}, false
}

func hasWarning(res Result, sub string) bool {
	return slices.ContainsFunc(res.Warnings, func(w string) bool { return strings.Contains(w, sub) })
}

// TestStartAddsLocalRouteThroughAPIWithoutRestart checks the point of #598:
// with the admin API available, a local model's route is created as a
// database row (POST /model/new, id wt:<id>, wt_managed) and config.yaml is
// neither rewritten nor the proxy restarted — a restart drains in-flight
// requests for minutes. A second identical start writes nothing.
func TestStartAddsLocalRouteThroughAPIWithoutRestart(t *testing.T) {
	body := "model_list: []\n" + settledSettings
	o, restarts, p, fake := dbOpts(t, body)
	cfg := testConfig()
	res, err := ApplyChange(cfg, Change{Add: localFor(cfg, "ollama/gemma:9b")}, o)
	if err != nil {
		t.Fatal(err)
	}
	if fake.requests("POST /model/new") != 1 || *restarts != 0 || res.Changed || res.Via != "db" {
		t.Fatalf("adds=%d restarts=%d changed=%v via=%q, want 1/0/false/db", fake.requests("POST /model/new"), *restarts, res.Changed, res.Via)
	}
	if got := readFile(t, p); got != body {
		t.Fatalf("config.yaml changed:\n%s", got)
	}
	if oc, ok := outcomeFor(res, "ollama/gemma:9b"); !ok || oc.Action != "routed" || !oc.Written || oc.Err != nil {
		t.Fatalf("outcomes %+v, want one written routed", res.Outcomes)
	}
	if got := fake.ids(); !slices.Equal(got, []string{"wt:ollama/gemma:9b"}) {
		t.Fatalf("database rows %v", got)
	}
	row := fake.rows["wt:ollama/gemma:9b"]
	if row["model_name"] != "ollama/gemma:9b" || row["model_info"].(map[string]any)[ManagedKey] != true {
		t.Fatalf("row %+v, want model_name ollama/gemma:9b and wt_managed", row)
	}
	if len(res.Warnings) != 0 {
		t.Fatalf("warnings %v", res.Warnings)
	}

	res, err = ApplyChange(cfg, Change{Add: localFor(cfg, "ollama/gemma:9b")}, o)
	if err != nil {
		t.Fatal(err)
	}
	if fake.requests("POST /model/new") != 1 || *restarts != 0 {
		t.Fatalf("repeat start: adds=%d restarts=%d, want no new write", fake.requests("POST /model/new"), *restarts)
	}
	if oc, ok := outcomeFor(res, "ollama/gemma:9b"); !ok || oc.Action != "routed" || oc.Written {
		t.Fatalf("repeat outcomes %+v, want routed and not written", res.Outcomes)
	}
}

// TestStopDeletesThroughAPI checks that a stop removes the database row (by
// id and by family clear) with no restart and no file write, and that a stop
// whose route is already gone (a proxy that lost its database, Review Focus
// 1) is a quiet no-op rather than an error.
func TestStopDeletesThroughAPI(t *testing.T) {
	body := "model_list: []\n" + settledSettings
	o, restarts, p, fake := dbOpts(t, body)
	cfg := testConfig()
	if _, err := ApplyChange(cfg, Change{Add: localFor(cfg, "ollama/gemma:9b", "mtplx/Youssofal--Q")}, o); err != nil {
		t.Fatal(err)
	}
	res, err := ApplyChange(cfg, Change{Remove: []string{"ollama/gemma:9b"}}, o)
	if err != nil {
		t.Fatal(err)
	}
	if fake.requests("POST /model/delete") != 1 || *restarts != 0 || res.Changed {
		t.Fatalf("deletes=%d restarts=%d changed=%v", fake.requests("POST /model/delete"), *restarts, res.Changed)
	}
	if got := actionIDs(res.Outcomes, "unrouted"); !slices.Equal(got, []string{"ollama/gemma:9b"}) {
		t.Fatalf("outcomes %+v", res.Outcomes)
	}
	res, err = ApplyChange(cfg, Change{RemoveFamilies: []string{"mtplx"}}, o)
	if err != nil {
		t.Fatal(err)
	}
	if got := actionIDs(res.Outcomes, "unrouted"); !slices.Equal(got, []string{"mtplx/Youssofal--Q"}) {
		t.Fatalf("family clear outcomes %+v", res.Outcomes)
	}
	if got := fake.ids(); len(got) != 0 {
		t.Fatalf("database rows left %v", got)
	}
	res, err = ApplyChange(cfg, Change{Remove: []string{"ollama/gemma:9b"}}, o)
	if err != nil || len(res.Outcomes) != 0 || len(res.Warnings) != 0 {
		t.Fatalf("stop of a gone route: err=%v outcomes=%+v warnings=%v, want a no-op", err, res.Outcomes, res.Warnings)
	}
	if *restarts != 0 || readFile(t, p) != body {
		t.Fatalf("restarts=%d or config.yaml changed", *restarts)
	}
}

// TestCloudRowStillWritesFileAndRestarts checks the split: in one change, the
// cloud row goes to config.yaml with exactly one restart (as before #598)
// while the local row goes to the database only.
func TestCloudRowStillWritesFileAndRestarts(t *testing.T) {
	o, restarts, p, fake := dbOpts(t, "model_list: []\n"+settledSettings)
	cfg := testConfig()
	res, err := ApplyChange(cfg, Change{Add: localFor(cfg, "ollama/gemma:9b", "openrouter/x/y")}, o)
	if err != nil {
		t.Fatal(err)
	}
	if *restarts != 1 || !res.Changed || res.Via != "db" {
		t.Fatalf("restarts=%d changed=%v via=%q, want 1/true/db", *restarts, res.Changed, res.Via)
	}
	f, _ := Open(p)
	if ids := f.RoutedIDs(); !slices.Equal(ids, []string{"openrouter/x/y"}) {
		t.Fatalf("config.yaml rows %v, want only the cloud row", ids)
	}
	if got := fake.ids(); !slices.Equal(got, []string{"wt:ollama/gemma:9b"}) {
		t.Fatalf("database rows %v", got)
	}
	for _, id := range []string{"ollama/gemma:9b", "openrouter/x/y"} {
		if oc, ok := outcomeFor(res, id); !ok || oc.Action != "routed" || !oc.Written {
			t.Fatalf("outcomes %+v, want %s written routed", res.Outcomes, id)
		}
	}
}

// TestAPIOffFallsBackToFileAndWarns checks the fallback: a proxy with
// store_model_in_db off, or one that cannot be reached, gets the local row in
// config.yaml with today's restart, and one warning line says why — without
// the master key in it.
func TestAPIOffFallsBackToFileAndWarns(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*fakeDBServer, *Options)
	}{
		{"store off", func(f *fakeDBServer, _ *Options) { f.addFailBody = realStoreOffBody }},
		{"unreachable", func(_ *fakeDBServer, o *Options) {
			srv := httptest.NewServer(nil)
			srv.Close()
			o.Admin = &AdminClient{BaseURL: srv.URL, Key: testMasterKey}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, restarts, p, fake := dbOpts(t, "model_list: []\n"+settledSettings)
			tc.setup(fake, &o)
			cfg := testConfig()
			res, err := ApplyChange(cfg, Change{Add: localFor(cfg, "ollama/gemma:9b")}, o)
			if err != nil {
				t.Fatal(err)
			}
			f, _ := Open(p)
			if ids := f.RoutedIDs(); !slices.Equal(ids, []string{"ollama/gemma:9b"}) || *restarts != 1 || res.Via != "file" {
				t.Fatalf("rows=%v restarts=%d via=%q, want the file path", ids, *restarts, res.Via)
			}
			var warn []string
			for _, w := range res.Warnings {
				if strings.HasPrefix(w, "database routes unavailable (") {
					warn = append(warn, w)
				}
			}
			if len(warn) != 1 || !strings.HasSuffix(warn[0], "); used config.yaml") || strings.Contains(warn[0], "\n") {
				t.Fatalf("warnings %q, want one fallback line", res.Warnings)
			}
			if strings.Contains(strings.Join(res.Warnings, " "), testMasterKey) {
				t.Fatal("a warning carries the master key")
			}
			if oc, ok := outcomeFor(res, "ollama/gemma:9b"); !ok || oc.Action != "routed" || !oc.Written {
				t.Fatalf("outcomes %+v", res.Outcomes)
			}
		})
	}
}

// TestUnmarkedYamlRowBlocksDBTwin checks Review Focus 2: a registry model
// whose id already has a hand-written config.yaml row gets no database row
// beside it (two rows under one name would be a silent load-balanced group);
// it is skipped with a warning naming the id, and nothing is written.
func TestUnmarkedYamlRowBlocksDBTwin(t *testing.T) {
	body := "model_list:\n  - model_name: ollama/gemma:9b\n    litellm_params: {model: ollama_chat/gemma:9b, api_base: 'http://localhost:11434'}\n" + settledSettings
	o, _, p, fake := dbOpts(t, body)
	cfg := testConfig()
	for name, run := range map[string]func() (Result, error){
		"start": func() (Result, error) { return ApplyChange(cfg, Change{Add: localFor(cfg, "ollama/gemma:9b")}, o) },
		"sync":  func() (Result, error) { return Sync(cfg, localFor(cfg, "ollama/gemma:9b"), o) },
	} {
		res, err := run()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if fake.requests("POST /model/new") != 0 {
			t.Fatalf("%s: a database twin was added", name)
		}
		if !hasWarning(res, "ollama/gemma:9b") {
			t.Fatalf("%s: warnings %v, want one naming the id", name, res.Warnings)
		}
		if _, ok := outcomeFor(res, "ollama/gemma:9b"); ok {
			t.Fatalf("%s: outcomes %+v, want none for the skipped id", name, res.Outcomes)
		}
	}
	// The sync routes the cloud model in the file; the hand-written row stays.
	f, _ := Open(p)
	if r := f.row("ollama/gemma:9b"); r == nil || IsManaged(r) {
		t.Fatal("the hand-written row was replaced")
	}
}

// TestSuccessThatIsNotListedFallsBack checks Review Focus 5: LiteLLM answers
// /model/new with 200 but /model/info does not list the row afterwards (a
// database that is not really storing). That is not a success: the route
// goes to config.yaml with a warning.
func TestSuccessThatIsNotListedFallsBack(t *testing.T) {
	o, restarts, p, fake := dbOpts(t, "model_list: []\n"+settledSettings)
	fake.swallowAdds = true
	cfg := testConfig()
	res, err := ApplyChange(cfg, Change{Add: localFor(cfg, "ollama/gemma:9b")}, o)
	if err != nil {
		t.Fatal(err)
	}
	f, _ := Open(p)
	if ids := f.RoutedIDs(); !slices.Equal(ids, []string{"ollama/gemma:9b"}) || *restarts != 1 || res.Via != "file" {
		t.Fatalf("rows=%v restarts=%d via=%q, want the file path", ids, *restarts, res.Via)
	}
	if !hasWarning(res, "database routes unavailable (") {
		t.Fatalf("warnings %v", res.Warnings)
	}
}

// TestSyncReconcilesDatabaseRoutes checks Sync with the API: running local
// models get database rows, a stale wt row in the database is deleted, a
// frozen family's row stays, a managed row that is not a wt: id is reported
// once and left alone, cloud rows go to the file with one restart, and a
// second sync writes nothing anywhere.
func TestSyncReconcilesDatabaseRoutes(t *testing.T) {
	o, restarts, p, fake := dbOpts(t, "model_list: []\n"+settledSettings)
	fake.seed(stored(managedRow("wt:ollama/old:1b", "ollama/old:1b", map[string]any{"model": "ollama_chat/old:1b"})))
	fake.seed(stored(managedRow("wt:mtplx/frozen", "mtplx/frozen", map[string]any{"model": "openai/frozen"})))
	fake.seed(stored(managedRow("ui-made", "ollama/ui", map[string]any{"model": "ollama_chat/ui"})))
	o.UntouchedFamilies = []string{"mtplx"}
	cfg := testConfig()
	res, err := Sync(cfg, localFor(cfg, "ollama/gemma:9b"), o)
	if err != nil {
		t.Fatal(err)
	}
	if got := fake.ids(); !slices.Equal(got, []string{"ui-made", "wt:mtplx/frozen", "wt:ollama/gemma:9b"}) {
		t.Fatalf("database rows %v", got)
	}
	if *restarts != 1 || res.Via != "db" {
		t.Fatalf("restarts=%d via=%q, want 1/db", *restarts, res.Via)
	}
	f, _ := Open(p)
	if ids := f.RoutedIDs(); !slices.Equal(ids, []string{"openrouter/x/y"}) {
		t.Fatalf("config.yaml rows %v", ids)
	}
	if got := actionIDs(res.Outcomes, "unrouted"); !slices.Equal(got, []string{"ollama/old:1b"}) {
		t.Fatalf("outcomes %+v", res.Outcomes)
	}
	if got := actionIDs(res.Outcomes, "routed"); !slices.Contains(got, "ollama/gemma:9b") {
		t.Fatalf("outcomes %+v", res.Outcomes)
	}
	n := 0
	for _, w := range res.Warnings {
		if strings.Contains(w, "ui-made") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("warnings %q, want one line naming ui-made", res.Warnings)
	}

	writes := fake.requests("POST /model/new") + fake.requests("POST /model/delete")
	res, err = Sync(cfg, localFor(cfg, "ollama/gemma:9b"), o)
	if err != nil {
		t.Fatal(err)
	}
	if again := fake.requests("POST /model/new") + fake.requests("POST /model/delete"); again != writes || *restarts != 1 || res.Changed || len(res.Outcomes) != 0 {
		t.Fatalf("second sync: db writes %d→%d restarts=%d changed=%v outcomes=%+v, want a no-op", writes, again, *restarts, res.Changed, res.Outcomes)
	}
}

// TestSyncRewritesChangedDatabaseRoute checks that a database row whose
// stored hash no longer matches the registry's row is replaced and reported
// "rewritten" — the action sync --json already uses for a changed wt row.
func TestSyncRewritesChangedDatabaseRoute(t *testing.T) {
	o, _, _, fake := dbOpts(t, "model_list: []\n"+settledSettings)
	fake.seed(stored(managedRow("wt:ollama/gemma:9b", "ollama/gemma:9b", map[string]any{"model": "ollama_chat/gemma:9b", "api_base": "http://old"})))
	cfg := testConfig()
	cfg.Models = slices.DeleteFunc(cfg.Models, func(m config.Model) bool { return m.Location == config.LocationCloud })
	res, err := Sync(cfg, localFor(cfg, "ollama/gemma:9b"), o)
	if err != nil {
		t.Fatal(err)
	}
	if got := actionIDs(res.Outcomes, "rewritten"); !slices.Equal(got, []string{"ollama/gemma:9b"}) {
		t.Fatalf("outcomes %+v, want ollama/gemma:9b rewritten", res.Outcomes)
	}
}

// TestNilAdminCallsNoAPI checks that without Options.Admin nothing is listed
// or written through the API and Via says "file": today's path exactly.
func TestNilAdminCallsNoAPI(t *testing.T) {
	o, restarts, p := opts(t, "model_list: []\n"+settledSettings)
	cfg := testConfig()
	res, err := ApplyChange(cfg, Change{Add: localFor(cfg, "ollama/gemma:9b")}, o)
	if err != nil || res.Via != "file" || *restarts != 1 {
		t.Fatalf("err=%v via=%q restarts=%d", err, res.Via, *restarts)
	}
	f, _ := Open(p)
	if ids := f.RoutedIDs(); !slices.Equal(ids, []string{"ollama/gemma:9b"}) {
		t.Fatalf("rows %v", ids)
	}
}
