package litellm

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"gopkg.in/yaml.v3"
)

// preStoreSettings is the settings block a config.yaml written before #598
// carries: settled in every other key, but without store_model_in_db.
const preStoreSettings = "litellm_settings:\n  drop_params: true\n  use_chat_completions_url_for_anthropic_messages: true\ngeneral_settings:\n  cancel_on_disconnect: true\n"

// markedLocalRows is two wt-written local rows as a pre-#598 sync left them in
// config.yaml, plus the cloud row, all as Sync builds them for testConfig.
func markedLocalRows(t *testing.T) string {
	t.Helper()
	// Let a nil-Admin sync write the rows, so they are exactly what wt builds.
	o, _, p := opts(t, "model_list: []\n"+settledSettings)
	cfg := testConfig()
	if _, err := Sync(cfg, localFor(cfg, "ollama/gemma:9b", "mtplx/Youssofal--Q"), o); err != nil {
		t.Fatal(err)
	}
	body := readFile(t, p)
	f, _ := Open(p)
	if ids := f.RoutedIDs(); len(ids) != 3 {
		t.Fatalf("seed rows %v, want three", ids)
	}
	return body
}

// withoutStoreKey drops the store_model_in_db line, giving a config.yaml as a
// pre-#598 wt left it.
func withoutStoreKey(body string) string {
	return strings.Replace(body, "  store_model_in_db: true\n", "", 1)
}

// TestEnsureSettingsAddsStoreModelInDBWhenAbsent pins general_settings.
// store_model_in_db: without it LiteLLM refuses /model/new and every local
// route stays in config.yaml, costing a proxy restart per start and stop
// (#598). It is written when absent or null, once, and is idempotent.
func TestEnsureSettingsAddsStoreModelInDBWhenAbsent(t *testing.T) {
	for name, body := range map[string]string{
		"no block":    "model_list: []\nlitellm_settings:\n  drop_params: true\n",
		"empty block": "model_list: []\ngeneral_settings:\n",
		"null key":    "model_list: []\ngeneral_settings:\n  store_model_in_db:\n",
		"other keys":  "model_list: []\ngeneral_settings:\n  database_url: postgresql://u@localhost/db\n",
	} {
		t.Run(name, func(t *testing.T) {
			f, err := Open(writeConfig(t, body))
			if err != nil {
				t.Fatal(err)
			}
			f.EnsureSettings()
			out := enc(t, f)
			if !strings.Contains(out, "store_model_in_db: true") || strings.Count(out, "store_model_in_db") != 1 {
				t.Fatalf("store_model_in_db not enabled once:\n%s", out)
			}
			if strings.Contains(body, "database_url") && !strings.Contains(out, "database_url") {
				t.Fatalf("lost database_url:\n%s", out)
			}
			f2, _ := Open(writeConfig(t, out))
			f2.EnsureSettings()
			if f2.Changed() {
				t.Fatalf("not idempotent:\n%s", enc(t, f2))
			}
		})
	}
}

// TestEnsureSettingsKeepsExplicitFalse pins the opt-out: a hand-written
// store_model_in_db: false — literal or inherited through a merge key — is
// never rewritten, and a sync with the API configured then calls no API at
// all, keeps the local route in config.yaml and says why in one warning.
func TestEnsureSettingsKeepsExplicitFalse(t *testing.T) {
	const settled = "litellm_settings:\n  drop_params: true\n  use_chat_completions_url_for_anthropic_messages: true\n"
	for name, gs := range map[string]string{
		"literal":   "general_settings:\n  cancel_on_disconnect: true\n  store_model_in_db: false\n",
		"inherited": "base: &b {store_model_in_db: false}\ngeneral_settings:\n  <<: *b\n  cancel_on_disconnect: true\n",
	} {
		t.Run(name, func(t *testing.T) {
			body := "model_list: []\n" + settled + gs
			f, err := Open(writeConfig(t, body))
			if err != nil {
				t.Fatal(err)
			}
			f.EnsureSettings()
			if f.Changed() {
				t.Fatalf("an explicit store_model_in_db: false was rewritten:\n%s", enc(t, f))
			}

			o, restarts, p, fake := dbOpts(t, body)
			cfg := testConfig()
			res, err := Sync(cfg, localFor(cfg, "ollama/gemma:9b"), o)
			if err != nil {
				t.Fatal(err)
			}
			if n := len(fake.log); n != 0 {
				t.Fatalf("API requests %v, want none with database models off", fake.log)
			}
			got, _ := Open(p)
			if got.row("ollama/gemma:9b") == nil || res.Via != "file" || *restarts != 1 {
				t.Fatalf("rows %v via=%q restarts=%d, want the file path", got.RoutedIDs(), res.Via, *restarts)
			}
			var lines []string
			for _, w := range res.Warnings {
				if strings.Contains(w, "store_model_in_db") {
					lines = append(lines, w)
				}
			}
			if len(lines) != 1 || !strings.Contains(lines[0], "database routes are off") || !strings.Contains(lines[0], "restart per change") {
				t.Fatalf("warnings %q, want one line saying database routes are off", res.Warnings)
			}
			if hasWarning(res, "database routes unavailable (") {
				t.Fatalf("warnings %q: an opt-out is not an unavailable API", res.Warnings)
			}
		})
	}
}

// TestSettingsWarningsNameStoreModelInDBWhenGeneralSettingsIsShared checks
// that a general_settings EnsureSettings cannot write (an alias, a root merge)
// and that does not enable database models is reported with the API
// configured, and never without it (Admin nil keeps today's warnings).
func TestSettingsWarningsNameStoreModelInDBWhenGeneralSettingsIsShared(t *testing.T) {
	body := "model_list: []\nbase: &b {database_url: postgresql://u@localhost/db, cancel_on_disconnect: true}\ngeneral_settings: *b\n" +
		"litellm_settings:\n  drop_params: true\n  use_chat_completions_url_for_anthropic_messages: true\n"
	o, _, _, fake := dbOpts(t, body)
	cfg := testConfig()
	res, err := Sync(cfg, nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if !hasWarning(res, "did not write store_model_in_db: true") || !hasWarning(res, "database routes are off") || len(fake.log) != 0 {
		t.Fatalf("warnings %q requests %v", res.Warnings, fake.log)
	}
	o.Admin = nil
	res, err = Sync(cfg, nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if hasWarning(res, "store_model_in_db") {
		t.Fatalf("Admin nil: warnings %q, want none about store_model_in_db", res.Warnings)
	}
}

// TestFirstSyncEnablesStoreModelInDBAndDefersMigration checks the first sync
// against a pre-#598 config.yaml: database models are off on the running
// proxy until the restart that loads the new setting, so that sync calls no
// API, writes the setting (one restart), keeps the local rows in the file and
// says what happens next in one warning. The next sync moves them.
func TestFirstSyncEnablesStoreModelInDBAndDefersMigration(t *testing.T) {
	body := withoutStoreKey(markedLocalRows(t))
	for name, run := range map[string]func(Options) (Result, error){
		"sync": func(o Options) (Result, error) {
			cfg := testConfig()
			return Sync(cfg, localFor(cfg, "ollama/gemma:9b", "mtplx/Youssofal--Q"), o)
		},
		"start": func(o Options) (Result, error) {
			cfg := testConfig()
			return ApplyChange(cfg, Change{Add: localFor(cfg, "ollama/gemma:9b")}, o)
		},
	} {
		t.Run(name, func(t *testing.T) {
			o, restarts, p, fake := dbOpts(t, body)
			res, err := run(o)
			if err != nil {
				t.Fatal(err)
			}
			if len(fake.log) != 0 || *restarts != 1 || res.Via != "file" {
				t.Fatalf("requests %v restarts=%d via=%q, want no API call, one restart, file", fake.log, *restarts, res.Via)
			}
			out := readFile(t, p)
			if !strings.Contains(out, "store_model_in_db: true") {
				t.Fatalf("setting not written:\n%s", out)
			}
			f, _ := Open(p)
			if ids := f.RoutedIDs(); len(ids) != 3 {
				t.Fatalf("rows %v, want the local rows still in config.yaml", ids)
			}
			n := 0
			for _, w := range res.Warnings {
				if w == "database routes enabled; local routes move to the database on the next sync" {
					n++
				}
			}
			if n != 1 || hasWarning(res, "database routes unavailable (") {
				t.Fatalf("warnings %q, want the one enabling line", res.Warnings)
			}

			cfg := testConfig()
			res, err = Sync(cfg, localFor(cfg, "ollama/gemma:9b", "mtplx/Youssofal--Q"), o)
			if err != nil {
				t.Fatal(err)
			}
			f, _ = Open(p)
			if ids := f.RoutedIDs(); !slices.Equal(ids, []string{"openrouter/x/y"}) || *restarts != 2 || res.Via != "db" {
				t.Fatalf("next sync: rows %v restarts=%d via=%q, want the local rows moved", ids, *restarts, res.Via)
			}
			if got := fake.ids(); !slices.Equal(got, []string{"wt:mtplx/Youssofal--Q", "wt:ollama/gemma:9b"}) {
				t.Fatalf("database rows %v", got)
			}
		})
	}
}

// TestMigrationAddsToDBBeforeRemovingFromFile checks the one-time move of
// wt's local config.yaml rows into the database: every row is added through
// the API and confirmed by a listing BEFORE config.yaml loses it, the file
// write removes them all at once with one restart, the cloud row stays in
// the file, and each moved id is reported "rewritten". A proxy that dies
// after accepting the adds (the confirming listing fails) must leave every
// row in config.yaml, so no model is ever unrouted, with the one fallback
// warning and no file write.
func TestMigrationAddsToDBBeforeRemovingFromFile(t *testing.T) {
	body := markedLocalRows(t)
	ids := []string{"ollama/gemma:9b", "mtplx/Youssofal--Q"}

	t.Run("healthy", func(t *testing.T) {
		o, restarts, p, fake := dbOpts(t, body)
		var atSave []string
		var logAtSave []string
		o.beforeSave = func() error {
			atSave = fake.ids()
			fake.mu.Lock()
			logAtSave = slices.Clone(fake.log)
			fake.mu.Unlock()
			return nil
		}
		cfg := testConfig()
		res, err := Sync(cfg, localFor(cfg, ids...), o)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(atSave, []string{"wt:mtplx/Youssofal--Q", "wt:ollama/gemma:9b"}) {
			t.Fatalf("database rows when config.yaml was saved: %v, want both", atSave)
		}
		lastAdd := slices.Index(logAtSave, "POST /model/new")
		for i, l := range logAtSave {
			if l == "POST /model/new" {
				lastAdd = i
			}
		}
		if lastAdd < 0 || !slices.Contains(logAtSave[lastAdd+1:], "GET /model/info") {
			t.Fatalf("requests before the save %v, want the adds confirmed by a listing", logAtSave)
		}
		f, _ := Open(p)
		if got := f.RoutedIDs(); !slices.Equal(got, []string{"openrouter/x/y"}) {
			t.Fatalf("config.yaml rows %v, want only the cloud row", got)
		}
		if *restarts != 1 || !res.Changed || res.Via != "db" {
			t.Fatalf("restarts=%d changed=%v via=%q, want 1/true/db", *restarts, res.Changed, res.Via)
		}
		for _, id := range ids {
			if oc, ok := outcomeFor(res, id); !ok || oc.Action != "rewritten" || !oc.Written || oc.Err != nil {
				t.Fatalf("outcomes %+v, want %s rewritten", res.Outcomes, id)
			}
		}
		if got := actionIDs(res.Outcomes, "unrouted"); len(got) != 0 {
			t.Fatalf("outcomes %+v: a moved route is not unrouted", res.Outcomes)
		}
		if fake.requests("POST /model/delete") != 0 {
			t.Fatal("a migration deleted a database row")
		}
	})

	t.Run("proxy dies after the adds", func(t *testing.T) {
		o, restarts, p, fake := dbOpts(t, body)
		k := &killAfterAdds{fake: fake, adds: len(ids)}
		srv := httptest.NewServer(k)
		t.Cleanup(srv.Close)
		o.Admin = &AdminClient{BaseURL: srv.URL, Key: testMasterKey}
		cfg := testConfig()
		res, err := Sync(cfg, localFor(cfg, ids...), o)
		if err != nil {
			t.Fatal(err)
		}
		if got := fake.ids(); len(got) != len(ids) {
			t.Fatalf("database rows %v, want the adds made before the proxy died", got)
		}
		if got := readFile(t, p); got != body || *restarts != 0 || res.Changed {
			t.Fatalf("restarts=%d changed=%v, config.yaml:\n%s\nwant it untouched", *restarts, res.Changed, got)
		}
		var lines []string
		for _, w := range res.Warnings {
			if strings.HasPrefix(w, "database routes unavailable (") {
				lines = append(lines, w)
			}
		}
		if len(lines) != 1 || !strings.HasSuffix(lines[0], "); used config.yaml") || res.Via != "file" {
			t.Fatalf("warnings %q via=%q, want one fallback line", res.Warnings, res.Via)
		}
		if strings.Contains(strings.Join(res.Warnings, " "), testMasterKey) {
			t.Fatal("a warning carries the master key")
		}
	})

	t.Run("add refused", func(t *testing.T) {
		o, restarts, p, fake := dbOpts(t, body)
		fake.addFailBody = realStoreOffBody
		cfg := testConfig()
		res, err := Sync(cfg, localFor(cfg, ids...), o)
		if err != nil {
			t.Fatal(err)
		}
		if got := readFile(t, p); got != body || *restarts != 0 || !hasWarning(res, "); used config.yaml") {
			t.Fatalf("restarts=%d warnings=%q config.yaml:\n%s\nwant it untouched", *restarts, res.Warnings, got)
		}
	})
}

// killAfterAdds serves fake until it has answered adds POST /model/new
// requests, then answers every later request 503: a proxy that accepted the
// adds and died before the confirming listing.
type killAfterAdds struct {
	fake *fakeDBServer
	adds int
	mu   sync.Mutex
	seen int
}

func (k *killAfterAdds) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	k.mu.Lock()
	dead := k.seen >= k.adds
	if !dead && r.Method == http.MethodPost && r.URL.Path == "/model/new" {
		k.seen++
	}
	k.mu.Unlock()
	if dead {
		w.WriteHeader(503)
		w.Write([]byte(`{"error":"service unavailable"}`))
		return
	}
	k.fake.ServeHTTP(w, r)
}

// TestMigrationIsIdempotent checks that the sync after a migration changes
// nothing anywhere: no API write, no file write, no restart, no outcome.
func TestMigrationIsIdempotent(t *testing.T) {
	o, restarts, p, fake := dbOpts(t, markedLocalRows(t))
	cfg := testConfig()
	ids := []string{"ollama/gemma:9b", "mtplx/Youssofal--Q"}
	if _, err := Sync(cfg, localFor(cfg, ids...), o); err != nil {
		t.Fatal(err)
	}
	after := readFile(t, p)
	writes := fake.requests("POST /model/new") + fake.requests("POST /model/delete")
	res, err := Sync(cfg, localFor(cfg, ids...), o)
	if err != nil {
		t.Fatal(err)
	}
	if again := fake.requests("POST /model/new") + fake.requests("POST /model/delete"); again != writes || *restarts != 1 || res.Changed || len(res.Outcomes) != 0 || readFile(t, p) != after {
		t.Fatalf("second sync: db writes %d→%d restarts=%d changed=%v outcomes=%+v, want a no-op", writes, again, *restarts, res.Changed, res.Outcomes)
	}
	if len(res.Warnings) != 0 {
		t.Fatalf("warnings %q", res.Warnings)
	}
}

// TestInterruptedMigrationConverges checks a process killed after the
// migration's adds were confirmed and before config.yaml was saved: each
// route is then in both places. During a sync the database copy wins for a
// row being migrated, so the next sync keeps the confirmed database rows (no
// delete, no re-add) and only drops the file rows; the sync after that is a
// no-op. A start that lands in between instead keeps the file rows (the file
// wins outside a sync) and deletes every twin of a route config.yaml serves;
// the next sync then moves them again, once. Either way it ends with every local route in the database
// only — no delete-and-re-add cycle.
func TestInterruptedMigrationConverges(t *testing.T) {
	body := markedLocalRows(t)
	ids := []string{"ollama/gemma:9b", "mtplx/Youssofal--Q"}
	killed := errors.New("killed before the save")
	for _, startBetween := range []bool{false, true} {
		name := "sync, sync"
		if startBetween {
			name = "start, sync, sync"
		}
		t.Run(name, func(t *testing.T) {
			o, restarts, p, fake := dbOpts(t, body)
			cfg := testConfig()
			kill := o
			kill.beforeSave = func() error { return killed }
			if _, err := Sync(cfg, localFor(cfg, ids...), kill); !errors.Is(err, killed) {
				t.Fatalf("err=%v, want the simulated kill", err)
			}
			if got := fake.ids(); len(got) != 2 || readFile(t, p) != body || *restarts != 0 {
				t.Fatalf("after the kill: database %v restarts=%d, want both rows in both places", got, *restarts)
			}
			if startBetween {
				if _, err := ApplyChange(cfg, Change{Add: localFor(cfg, "ollama/gemma:9b")}, o); err != nil {
					t.Fatal(err)
				}
				if got := fake.ids(); len(got) != 0 {
					t.Fatalf("after the start: database %v, want the twins deleted", got)
				}
			}
			adds, dels := fake.requests("POST /model/new"), fake.requests("POST /model/delete")
			if _, err := Sync(cfg, localFor(cfg, ids...), o); err != nil {
				t.Fatal(err)
			}
			wantAdds := adds
			if startBetween {
				wantAdds += 2 // the twins went with the start; the sync adds each again, once
			}
			if fake.requests("POST /model/new") != wantAdds || fake.requests("POST /model/delete") != dels {
				t.Fatalf("converging sync: adds %d→%d deletes %d→%d, want %d adds and no delete",
					adds, fake.requests("POST /model/new"), dels, fake.requests("POST /model/delete"), wantAdds)
			}
			res, err := Sync(cfg, localFor(cfg, ids...), o)
			if err != nil {
				t.Fatal(err)
			}
			if res.Changed || len(res.Outcomes) != 0 {
				t.Fatalf("third sync: changed=%v outcomes=%+v, want a no-op", res.Changed, res.Outcomes)
			}
			f, _ := Open(p)
			if got := f.RoutedIDs(); !slices.Equal(got, []string{"openrouter/x/y"}) {
				t.Fatalf("config.yaml rows %v, want no local row", got)
			}
			if got := fake.ids(); !slices.Equal(got, []string{"wt:mtplx/Youssofal--Q", "wt:ollama/gemma:9b"}) {
				t.Fatalf("database rows %v, want both", got)
			}
		})
	}
}

// TestMigrationReplacesAStaleTwin checks that a database row left under a
// migrating id with an old hash (an interrupted migration followed by a
// registry change) is replaced with the current row before the file row
// goes, so the route that remains is the current one.
func TestMigrationReplacesAStaleTwin(t *testing.T) {
	o, _, p, fake := dbOpts(t, markedLocalRows(t))
	fake.seed(stored(managedRow("wt:ollama/gemma:9b", "ollama/gemma:9b", map[string]any{"model": "ollama_chat/gemma:9b", "api_base": "http://stale"})))
	cfg := testConfig()
	if _, err := Sync(cfg, localFor(cfg, "ollama/gemma:9b"), o); err != nil {
		t.Fatal(err)
	}
	f, _ := Open(p)
	if f.row("ollama/gemma:9b") != nil {
		t.Fatal("the file row was not moved")
	}
	m, err := DBModelFromRow(mustRow(t, cfg, "ollama/gemma:9b"))
	if err != nil {
		t.Fatal(err)
	}
	cur, err := o.Admin.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if p := PlanDB([]DBModel{m}, cur); len(p.Add)+len(p.Update) != 0 {
		t.Fatalf("database row is not the current one: %+v", p)
	}
}

func mustRow(t *testing.T, cfg *config.Config, id string) *yaml.Node {
	t.Helper()
	row, err := prepareModel(cfg, localFor(cfg, id)[0])
	if err != nil {
		t.Fatal(err)
	}
	ensureRowParams(row) // what every database row carries
	return row
}

// TestMigrationKeepsARowWithUserParamsInTheFile checks that a wt row the user
// added a param to (a timeout, say) is not moved: the database row is rebuilt
// from the registry on every sync and would drop it. It stays in config.yaml
// with a warning naming the param, while a plain row beside it moves.
func TestMigrationKeepsARowWithUserParamsInTheFile(t *testing.T) {
	body := strings.Replace(markedLocalRows(t), "model: 'ollama_chat/gemma:9b', ", "model: 'ollama_chat/gemma:9b', timeout: 600, ", 1)
	if !strings.Contains(body, "timeout: 600") {
		t.Fatalf("fixture did not take the param:\n%s", body)
	}
	o, _, p, fake := dbOpts(t, body)
	cfg := testConfig()
	res, err := Sync(cfg, localFor(cfg, "ollama/gemma:9b", "mtplx/Youssofal--Q"), o)
	if err != nil {
		t.Fatal(err)
	}
	f, _ := Open(p)
	if got := f.RoutedIDs(); !slices.Equal(got, []string{"openrouter/x/y", "ollama/gemma:9b"}) {
		t.Fatalf("config.yaml rows %v, want gemma kept beside the cloud row", got)
	}
	if !strings.Contains(readFile(t, p), "timeout: 600") {
		t.Fatal("the user's param was lost")
	}
	if got := fake.ids(); !slices.Equal(got, []string{"wt:mtplx/Youssofal--Q"}) {
		t.Fatalf("database rows %v, want only mtplx moved", got)
	}
	if !hasWarning(res, "ollama/gemma:9b: its config.yaml row has params wt does not set (timeout)") {
		t.Fatalf("warnings %q", res.Warnings)
	}
}

// TestFailedMigrationDropsTheTwinsItWouldHaveKept checks the other side of
// "the database wins during a migration": when the migration's database half
// fails (here one add is refused), the rows stay in config.yaml, which then
// serves them again, so a database copy an interrupted migration left behind
// is deleted rather than left load-balanced beside the file row. The next
// healthy sync migrates again.
func TestFailedMigrationDropsTheTwinsItWouldHaveKept(t *testing.T) {
	body := markedLocalRows(t)
	o, restarts, p, fake := dbOpts(t, body)
	cfg := testConfig()
	mt, err := DBModelFromRow(mustRow(t, cfg, "mtplx/Youssofal--Q"))
	if err != nil {
		t.Fatal(err)
	}
	fake.seed(stored(mt)) // the current row, as an interrupted migration leaves it
	fake.addFailBody = realStoreOffBody
	res, err := Sync(cfg, localFor(cfg, "ollama/gemma:9b", "mtplx/Youssofal--Q"), o)
	if err != nil {
		t.Fatal(err)
	}
	if got := fake.ids(); len(got) != 0 {
		t.Fatalf("database rows %v, want the twin deleted", got)
	}
	if readFile(t, p) != body || *restarts != 0 || res.Via != "file" || !hasWarning(res, "); used config.yaml") {
		t.Fatalf("restarts=%d via=%q warnings=%q, want config.yaml untouched and the fallback line", *restarts, res.Via, res.Warnings)
	}
	fake.addFailBody = ""
	if _, err := Sync(cfg, localFor(cfg, "ollama/gemma:9b", "mtplx/Youssofal--Q"), o); err != nil {
		t.Fatal(err)
	}
	f, _ := Open(p)
	if got := f.RoutedIDs(); !slices.Equal(got, []string{"openrouter/x/y"}) || len(fake.ids()) != 2 {
		t.Fatalf("after the healthy sync: config.yaml %v database %v, want both moved", got, fake.ids())
	}
}
