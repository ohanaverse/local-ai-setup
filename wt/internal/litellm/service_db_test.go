package litellm

import (
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// settledSettings is the settings block EnsureSettings leaves alone, so a
// write that changes no row leaves config.yaml byte-identical.
const settledSettings = "litellm_settings:\n  drop_params: true\n  use_chat_completions_url_for_anthropic_messages: true\ngeneral_settings:\n  cancel_on_disconnect: true\n  store_model_in_db: true\n"

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

// TestFallbackThenHealthySyncLeavesNoTwin checks that a route started in the
// database, then written to config.yaml by a sync that could not list the
// database, does not stay in both places once the database answers again —
// LiteLLM would load-balance the two rows, and later changes would reach only
// one. Since the migration (Task 5) the healthy sync settles it on the
// database side: the file row is moved (its database copy kept, no delete,
// no re-add) and exactly one route remains, the current one.
func TestFallbackThenHealthySyncLeavesNoTwin(t *testing.T) {
	o, _, p, fake := dbOpts(t, "model_list: []\n"+settledSettings)
	cfg := testConfig()
	if _, err := ApplyChange(cfg, Change{Add: localFor(cfg, "ollama/gemma:9b")}, o); err != nil {
		t.Fatal(err)
	}
	fake.failList = true
	res, err := Sync(cfg, localFor(cfg, "ollama/gemma:9b"), o)
	if err != nil || !hasWarning(res, "database routes unavailable (") {
		t.Fatalf("fallback sync: err=%v warnings=%v", err, res.Warnings)
	}
	fake.failList = false
	if _, err := Sync(cfg, localFor(cfg, "ollama/gemma:9b"), o); err != nil {
		t.Fatal(err)
	}
	if got := fake.ids(); !slices.Equal(got, []string{"wt:ollama/gemma:9b"}) || fake.requests("POST /model/delete") != 0 {
		t.Fatalf("database rows %v deletes %d, want the one route kept", got, fake.requests("POST /model/delete"))
	}
	f, _ := Open(p)
	if r := f.row("ollama/gemma:9b"); r != nil {
		t.Fatal("config.yaml still routes it: a twin is left")
	}
	m, err := DBModelFromRow(mustRow(t, cfg, "ollama/gemma:9b"))
	if err != nil {
		t.Fatal(err)
	}
	cur, err := o.Admin.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if p := PlanDB([]DBModel{m}, cur, testMasterKey); !p.Empty() {
		t.Fatalf("the database route is not the current one: %+v", p)
	}
}

// TestStartBesideMarkedFileRowRemovesTheDatabaseTwin checks the same rule on
// a start: a local id whose wt row is still in config.yaml (not migrated yet)
// stays there, and a database row of the same name is deleted.
func TestStartBesideMarkedFileRowRemovesTheDatabaseTwin(t *testing.T) {
	o, _, p, fake := dbOpts(t, "model_list: []\n"+settledSettings)
	cfg := testConfig()
	if _, err := ApplyChange(cfg, Change{Add: localFor(cfg, "ollama/gemma:9b")}, Options{Path: p, Restart: o.Restart}); err != nil {
		t.Fatal(err)
	}
	fake.seed(stored(managedRow("wt:ollama/gemma:9b", "ollama/gemma:9b", map[string]any{"model": "ollama_chat/gemma:9b", "api_base": "http://stale"})))
	if _, err := ApplyChange(cfg, Change{Add: localFor(cfg, "ollama/gemma:9b")}, o); err != nil {
		t.Fatal(err)
	}
	if got := fake.ids(); len(got) != 0 || fake.requests("POST /model/new") != 0 {
		t.Fatalf("database rows %v adds %d, want the twin deleted and nothing added", got, fake.requests("POST /model/new"))
	}
	f, _ := Open(p)
	if r := f.row("ollama/gemma:9b"); r == nil || !IsManaged(r) {
		t.Fatal("config.yaml lost the route")
	}
}

// TestFailedDatabaseDeleteIsReported checks that a stop whose database delete
// fails says so truthfully: an "unrouted" Outcome carrying the error and a
// warning that the route may still be live — never "used config.yaml",
// since the file cannot remove a database row — while a delete that worked
// before it in the same change is reported as a normal "unrouted".
func TestFailedDatabaseDeleteIsReported(t *testing.T) {
	body := "model_list: []\n" + settledSettings
	o, restarts, p, fake := dbOpts(t, body)
	cfg := testConfig()
	if _, err := ApplyChange(cfg, Change{Add: localFor(cfg, "ollama/gemma:9b", "mtplx/Youssofal--Q")}, o); err != nil {
		t.Fatal(err)
	}
	fake.failDeleteID = "wt:ollama/gemma:9b"
	res, err := ApplyChange(cfg, Change{Remove: []string{"mtplx/Youssofal--Q", "ollama/gemma:9b"}}, o)
	if err != nil {
		t.Fatal(err)
	}
	mt, ok := outcomeFor(res, "mtplx/Youssofal--Q")
	if !ok || mt.Action != "unrouted" || mt.Err != nil {
		t.Fatalf("outcomes %+v, want mtplx unrouted", res.Outcomes)
	}
	g, ok := outcomeFor(res, "ollama/gemma:9b")
	if !ok || g.Action != "unrouted" || g.Err == nil {
		t.Fatalf("outcomes %+v, want gemma unrouted with Err", res.Outcomes)
	}
	if !hasWarning(res, "ollama/gemma:9b: its database route was NOT removed and may still be live") || hasWarning(res, "used config.yaml") {
		t.Fatalf("warnings %q", res.Warnings)
	}
	if strings.Contains(strings.Join(res.Warnings, " "), testMasterKey) {
		t.Fatal("a warning carries the master key")
	}
	if got := fake.ids(); !slices.Equal(got, []string{"wt:ollama/gemma:9b"}) {
		t.Fatalf("database rows %v, want gemma still there", got)
	}
	if *restarts != 0 || readFile(t, p) != body {
		t.Fatalf("restarts=%d or config.yaml changed", *restarts)
	}
}

// TestHungProxyFallsBackWithinTheBound checks that a proxy that accepts the
// connection and never answers cannot hold the config.yaml lock past the
// write's one database deadline: the start and the sync fall back to the
// file promptly, with the warning.
func TestHungProxyFallsBackWithinTheBound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	o, _, p := opts(t, "model_list: []\n"+settledSettings)
	o.Admin = &AdminClient{BaseURL: srv.URL, Key: testMasterKey}
	o.dbTimeout = 200 * time.Millisecond
	cfg := testConfig()
	for name, run := range map[string]func() (Result, error){
		"start": func() (Result, error) { return ApplyChange(cfg, Change{Add: localFor(cfg, "ollama/gemma:9b")}, o) },
		"sync":  func() (Result, error) { return Sync(cfg, localFor(cfg, "ollama/gemma:9b", "mtplx/Youssofal--Q"), o) },
	} {
		start := time.Now()
		res, err := run()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if el := time.Since(start); el > 3*time.Second {
			t.Fatalf("%s took %v, want about the 200ms deadline", name, el)
		}
		if res.Via != "file" || !hasWarning(res, "database routes unavailable (") {
			t.Fatalf("%s: via=%q warnings=%v", name, res.Via, res.Warnings)
		}
	}
	f, _ := Open(p)
	if f.row("ollama/gemma:9b") == nil || f.row("mtplx/Youssofal--Q") == nil {
		t.Fatalf("rows %v, want both local routes in config.yaml", f.RoutedIDs())
	}
}

// TestSyncWarnsWhenTheDatabaseCannotBeListed checks that a sync — the command
// that must always look — says so when it could not read the database, even
// with no local change pending: stale wt rows there would otherwise stay live
// with nothing printed. One line, no file write, no restart.
func TestSyncWarnsWhenTheDatabaseCannotBeListed(t *testing.T) {
	hung := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(hung.Close)
	gone := httptest.NewServer(nil)
	gone.Close()
	for _, tc := range []struct {
		name  string
		setup func(*fakeDBServer, *Options)
	}{
		{"503", func(f *fakeDBServer, _ *Options) { f.failList = true }},
		{"unreachable", func(_ *fakeDBServer, o *Options) { o.Admin = &AdminClient{BaseURL: gone.URL, Key: testMasterKey} }},
		{"hung", func(_ *fakeDBServer, o *Options) {
			o.Admin = &AdminClient{BaseURL: hung.URL, Key: testMasterKey}
			o.dbTimeout = 200 * time.Millisecond
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := "model_list: []\n" + settledSettings
			o, restarts, p, fake := dbOpts(t, body)
			tc.setup(fake, &o)
			cfg := testConfig()
			cfg.Models = slices.DeleteFunc(cfg.Models, func(m config.Model) bool { return m.Location == config.LocationCloud })
			res, err := Sync(cfg, nil, o)
			if err != nil {
				t.Fatal(err)
			}
			var lines []string
			for _, w := range res.Warnings {
				if strings.HasPrefix(w, "database routes unavailable (") {
					lines = append(lines, w)
				}
			}
			if len(lines) != 1 || !strings.HasSuffix(lines[0], "); database routes not checked") {
				t.Fatalf("warnings %q, want one 'database routes not checked' line", res.Warnings)
			}
			if strings.Contains(lines[0], testMasterKey) {
				t.Fatal("the warning carries the master key")
			}
			if *restarts != 0 || res.Changed || readFile(t, p) != body {
				t.Fatalf("restarts=%d changed=%v or config.yaml changed", *restarts, res.Changed)
			}
		})
	}
}
