package litellm

import (
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
