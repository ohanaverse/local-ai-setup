package litellm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// fallbackLines returns the warnings that report a database fallback.
func fallbackLines(res Result) []string {
	var out []string
	for _, w := range res.Warnings {
		if strings.HasPrefix(w, "database routes unavailable (") {
			out = append(out, w)
		}
	}
	return out
}

// TestFailedAddFallsBackOnlyForRowsNotCurrent checks that when one database
// add fails during a sync (an HTTP 500, or a dropped connection after the
// listing succeeded), only the failed route goes to config.yaml: a route the
// database already serves, current, must get no file twin — LiteLLM would
// load-balance the two rows, the file write costs a restart, and a model
// whose add keeps failing would churn every sync.
func TestFailedAddFallsBackOnlyForRowsNotCurrent(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*fakeDBServer)
	}{
		{"http 500", func(f *fakeDBServer) { f.failAddID = DBID("mtplx/Youssofal--Q") }},
		{"transport", func(f *fakeDBServer) { f.abortAddID = DBID("mtplx/Youssofal--Q") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, restarts, p, fake := dbOpts(t, "model_list: []\n"+settledSettings)
			cfg := testConfig()
			cur, err := DBModelFromRow(mustRow(t, cfg, "ollama/gemma:9b"))
			if err != nil {
				t.Fatal(err)
			}
			fake.seed(stored(cur))
			tc.setup(fake)
			res, err := Sync(cfg, localFor(cfg, "ollama/gemma:9b", "mtplx/Youssofal--Q"), o)
			if err != nil {
				t.Fatal(err)
			}
			f, _ := Open(p)
			if f.row("ollama/gemma:9b") != nil {
				t.Fatalf("config.yaml rows %v: the current database route got a file twin", f.RoutedIDs())
			}
			if f.row("mtplx/Youssofal--Q") == nil {
				t.Fatalf("config.yaml rows %v, want the failed route there", f.RoutedIDs())
			}
			if got := fake.ids(); len(got) != 1 || got[0] != DBID("ollama/gemma:9b") {
				t.Fatalf("database rows %v, want the current one kept", got)
			}
			if lines := fallbackLines(res); len(lines) != 1 || !strings.HasSuffix(lines[0], "); used config.yaml") {
				t.Fatalf("warnings %q, want one fallback line", res.Warnings)
			}
			if *restarts != 1 {
				t.Fatalf("restarts=%d, want 1", *restarts)
			}
		})
	}
}

// plainRouteHash is the unkeyed hash routeHash used before the fix wave: the
// sha256 of the canonical JSON of model_name and the raw params.
func plainRouteHash(t *testing.T, m DBModel) string {
	t.Helper()
	b, err := json.Marshal(m.Params)
	if err != nil {
		t.Fatal(err)
	}
	var norm any
	if err := json.Unmarshal(b, &norm); err != nil {
		t.Fatal(err)
	}
	b, err = json.Marshal(map[string]any{"model_name": m.ModelName, "litellm_params": norm})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// TestRouteHashKeysAResolvedAPIKey checks that a row carrying a resolved
// secret as its api_key (rec's key, built from `rec auth token`) is not
// stamped with an unsalted sha256 over that secret: the hash is stored
// unmasked in model_info, so it would allow an offline dictionary check of
// the key. The value is HMAC'd with the proxy master key instead — stable
// across calls, different under another master key — while an
// os.environ/ reference hashes by the reference, as before.
func TestRouteHashKeysAResolvedAPIKey(t *testing.T) {
	secret := managedRow("wt:rec/flint", "rec/flint", map[string]any{"model": "openai/flint", "api_key": "resolved-secret-123"})
	h := routeHash(secret, testMasterKey)
	if h == "" || h == plainRouteHash(t, secret) {
		t.Fatalf("hash %q is the plain sha256 over the resolved key", h)
	}
	if h != routeHash(secret, testMasterKey) {
		t.Fatal("the keyed hash is not stable")
	}
	if h == routeHash(secret, "sk-other-master") {
		t.Fatal("the hash does not depend on the master key")
	}
	if secret.Params["api_key"] != "resolved-secret-123" {
		t.Fatal("routeHash changed the caller's params")
	}
	env := managedRow("wt:rec/flint", "rec/flint", map[string]any{"model": "openai/flint", "api_key": "os.environ/REC_KEY"})
	if he := routeHash(env, testMasterKey); he != plainRouteHash(t, env) || he != routeHash(env, "sk-other-master") {
		t.Fatalf("an os.environ/ reference must hash by the reference: %q", he)
	}

	// Stamped by ApplyDB, planned by PlanDB: the same keyed hash on both sides.
	f, c := newFakeDB(t)
	if _, _, _, err := c.ApplyDB(t.Context(), DBPlan{Add: []DBModel{secret}}); err != nil {
		t.Fatal(err)
	}
	info, _ := f.rows["wt:rec/flint"]["model_info"].(map[string]any)
	if info[ParamsHashKey] != h {
		t.Fatalf("stamped %v, want the keyed hash", info[ParamsHashKey])
	}
	got, err := c.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if p := PlanDB([]DBModel{secret}, got, testMasterKey); !p.Empty() {
		t.Fatalf("plan %+v, want empty under the same master key", p)
	}
	if p := PlanDB([]DBModel{secret}, got, "sk-other-master"); len(p.Update) != 1 {
		t.Fatalf("plan %+v, want one Update after a master-key rotation", p)
	}
}

// bigListing serves /model/info with n rows, each carrying a cost-map-sized
// model_info (pad bytes), the first one a database row; size is the body length.
func bigListing(t *testing.T, n, pad int) (*AdminClient, int) {
	t.Helper()
	filler := strings.Repeat("x", pad)
	var data []map[string]any
	for i := range n {
		data = append(data, map[string]any{
			"model_name":     fmt.Sprintf("openrouter/m%d", i),
			"litellm_params": map[string]any{"model": fmt.Sprintf("openrouter/m%d", i)},
			"model_info":     map[string]any{"id": fmt.Sprintf("cloud-%d", i), "db_model": i == 0, "notes": filler},
		})
	}
	body, err := json.Marshal(map[string]any{"data": data})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(body) }))
	t.Cleanup(srv.Close)
	return &AdminClient{BaseURL: srv.URL, Key: testMasterKey}, len(body)
}

// TestListReadsALargeListingAndNamesAnOversizedOne checks that /model/info,
// which returns every deployment (each cloud row from config.yaml with
// LiteLLM's full cost-map model_info), is read whole past 1 MiB — a few
// hundred cloud models must not turn every route write into a fallback
// with a JSON decode error — and that a listing past the 32 MiB cap is
// refused with an error saying so, not a misleading decode error.
func TestListReadsALargeListingAndNamesAnOversizedOne(t *testing.T) {
	c, size := bigListing(t, 600, 3000)
	if size <= 1<<20 {
		t.Fatalf("listing is %d bytes, want over 1 MiB", size)
	}
	rows, err := c.List(t.Context())
	if err != nil || len(rows) != 1 || rows[0].ID != "cloud-0" {
		t.Fatalf("rows %d err %v, want the one database row", len(rows), err)
	}
	c, size = bigListing(t, 3, 11<<20)
	if size <= 32<<20 {
		t.Fatalf("listing is %d bytes, want over 32 MiB", size)
	}
	_, err = c.List(t.Context())
	if err == nil || !strings.Contains(err.Error(), "larger than") || strings.Contains(err.Error(), "JSON") {
		t.Fatalf("err %v, want an explicit 'larger than' error", err)
	}
}

// TestCancelledSyncWritesNothing checks that a sync whose context is
// cancelled (wt-model serve shutting down mid start-up sync) does not take
// the fallback: the database listing fails only because of the cancel, and
// falling back would write config.yaml and bounce the proxy on the way out.
func TestCancelledSyncWritesNothing(t *testing.T) {
	body := "model_list: []\n" + settledSettings
	o, restarts, p, fake := dbOpts(t, body)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	o.Ctx = ctx
	cfg := testConfig()
	res, err := Sync(cfg, localFor(cfg, "ollama/gemma:9b"), o)
	if err != nil {
		t.Fatal(err)
	}
	if *restarts != 0 || res.Changed || readFile(t, p) != body {
		t.Fatalf("restarts=%d changed=%v or config.yaml changed:\n%s", *restarts, res.Changed, readFile(t, p))
	}
	if fake.requests("POST /model/new") != 0 {
		t.Fatal("a database add was made")
	}
	if !hasWarning(res, "sync cancelled; config.yaml was not written") || hasWarning(res, "used config.yaml") {
		t.Fatalf("warnings %q, want the cancel line and no fallback line", res.Warnings)
	}
}

// TestOptOutLineOnlyOnSyncAndDryRun checks that with store_model_in_db: false
// written by hand (the opt-out) the "database routes are off" line is said by
// the sync and the dry run, where the user reviews routes, and never by a
// start or stop: a model server would otherwise log it on every job, cloud
// changes included.
func TestOptOutLineOnlyOnSyncAndDryRun(t *testing.T) {
	const off = "database routes are off"
	body := "model_list: []\n" + strings.Replace(settledSettings, "store_model_in_db: true", "store_model_in_db: false", 1)
	o, _, _, fake := dbOpts(t, body)
	cfg := testConfig()
	for name, ch := range map[string]Change{
		"local start": {Add: localFor(cfg, "ollama/gemma:9b")},
		"cloud start": {Add: localFor(cfg, "openrouter/x/y")},
		"stop":        {Remove: []string{"ollama/gemma:9b"}},
	} {
		res, err := ApplyChange(cfg, ch, o)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if hasWarning(res, off) {
			t.Fatalf("%s: warnings %q, want no opt-out line", name, res.Warnings)
		}
	}
	res, err := Sync(cfg, localFor(cfg, "ollama/gemma:9b"), o)
	if err != nil {
		t.Fatal(err)
	}
	if !hasWarning(res, off) {
		t.Fatalf("sync warnings %q, want the opt-out line", res.Warnings)
	}
	plan, err := PlanSync(cfg, localFor(cfg, "ollama/gemma:9b"), o)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(plan.Warnings, func(w string) bool { return strings.Contains(w, off) }) {
		t.Fatalf("dry-run warnings %q, want the opt-out line", plan.Warnings)
	}
	if fake.calls != 0 {
		t.Fatalf("%d API calls with the opt-out, want 0", fake.calls)
	}
}
