package modelserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/candidates"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/spend"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tiers"
)

// newSupportTS is a test server whose Rank, TiersFreshness and Spend seams
// mut sets.
func newSupportTS(t *testing.T, mut func(*Deps)) (*Server, *httptest.Server) {
	t.Helper()
	d := (&fakeLife{snap: serverSnap()}).deps(serverCfg())
	mut(&d)
	return newTestServerDeps(t, d, nil)
}

// TestCandidatesMapsRankAndFreshness pins that the endpoint maps each ranked
// candidate to the wire type (score only when it exists, flags always a list)
// and reports the tiers file's age and stale sources, because a client picks a
// slot model from this list and must see how fresh the ratings are.
func TestCandidatesMapsRankAndFreshness(t *testing.T) {
	price := 1.5
	gen := time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)
	_, ts := newSupportTS(t, func(d *Deps) {
		d.Rank = func(*config.Config, tiers.Index, candidates.Query) []candidates.Candidate {
			return []candidates.Candidate{
				{ID: "omlx/a", Provider: "omlx", Location: "local", Tier: "strong", Confidence: "high", Score: 82.5, HasScore: true, Rated: true, OpenWeights: true, Context: 131072},
				{ID: "openrouter/c", Provider: "openrouter", Location: "cloud", OutputPerMillion: &price, Flags: []string{"unrated"}},
			}
		}
		d.TiersFreshness = func() (tiers.Freshness, bool) {
			return tiers.Freshness{GeneratedAt: gen, StaleSources: []string{"lmarena"}}, true
		}
	})
	st, b := call(t, ts, "GET", "/control/v1/candidates", "")
	if st != 200 {
		t.Fatalf("status %d: %s", st, b)
	}
	var got modelapi.CandidateList
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.TiersGenerated != "2026-11-10T08:00:00Z" || !reflect.DeepEqual(got.StaleSources, []string{"lmarena"}) {
		t.Errorf("freshness = %q %v", got.TiersGenerated, got.StaleSources)
	}
	if len(got.Candidates) != 2 {
		t.Fatalf("candidates = %+v", got.Candidates)
	}
	a, c := got.Candidates[0], got.Candidates[1]
	if a.Score == nil || *a.Score != 82.5 || !a.Rated || !a.OpenWeights || a.Context != 131072 || a.Tier != "strong" {
		t.Errorf("first = %+v", a)
	}
	if c.Score != nil || c.OutputPerMillion == nil || *c.OutputPerMillion != 1.5 || !reflect.DeepEqual(c.Flags, []string{"unrated"}) {
		t.Errorf("second = %+v", c)
	}
	if !strings.Contains(string(b), `"flags":[]`) {
		t.Errorf("a candidate without flags must still carry flags: %s", b)
	}
}

// TestCandidatesWithoutTiersFileOmitsGenerated pins that a missing tiers file
// leaves tiers_generated out and stale_sources an empty list (J6), so a client
// can tell "no file" from "old file".
func TestCandidatesWithoutTiersFileOmitsGenerated(t *testing.T) {
	_, ts := newSupportTS(t, func(d *Deps) {
		d.Rank = func(*config.Config, tiers.Index, candidates.Query) []candidates.Candidate { return nil }
		d.TiersFreshness = func() (tiers.Freshness, bool) { return tiers.Freshness{}, false }
	})
	_, b := call(t, ts, "GET", "/control/v1/candidates", "")
	if string(b) != `{"stale_sources":[],"candidates":[]}`+"\n" {
		t.Errorf("body = %s", b)
	}
}

// TestCandidatesPerProviderNeedsNear pins the CLI's rule: per_provider without
// near is a 400 bad_request, and per_provider must be a non-negative integer.
func TestCandidatesPerProviderNeedsNear(t *testing.T) {
	_, ts := newSupportTS(t, func(d *Deps) {
		d.Rank = func(*config.Config, tiers.Index, candidates.Query) []candidates.Candidate { return nil }
	})
	st, b := call(t, ts, "GET", "/control/v1/candidates?per_provider=2", "")
	wantAPIError(t, st, b, http.StatusBadRequest, modelapi.CodeBadRequest)
	st, b = call(t, ts, "GET", "/control/v1/candidates?near=omlx/a&per_provider=x", "")
	wantAPIError(t, st, b, http.StatusBadRequest, modelapi.CodeBadRequest)
	if st, b = call(t, ts, "GET", "/control/v1/candidates?near=omlx/a&per_provider=2", ""); st != 200 {
		t.Errorf("near with per_provider = %d %s", st, b)
	}
}

// TestCandidatesQueryReachesRank pins that open_only, the provider list, near
// and per_provider arrive in the candidates.Query the ranker is given.
func TestCandidatesQueryReachesRank(t *testing.T) {
	var got candidates.Query
	_, ts := newSupportTS(t, func(d *Deps) {
		d.Rank = func(_ *config.Config, _ tiers.Index, q candidates.Query) []candidates.Candidate {
			got = q
			return nil
		}
	})
	st, b := call(t, ts, "GET", "/control/v1/candidates?open_only=true&provider=omlx,%20mtplx,&near=omlx/a&per_provider=4", "")
	if st != 200 {
		t.Fatalf("status %d: %s", st, b)
	}
	want := candidates.Query{OpenOnly: true, Providers: []string{"omlx", "mtplx"}, Near: "omlx/a", PerProvider: 4}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("query = %+v, want %+v", got, want)
	}
}

// TestSpendReturnsRowsAndUnattributed pins the mapping of the spend result,
// that the bounds reach the query unchanged, and that rows is [] when empty.
func TestSpendReturnsRowsAndUnattributed(t *testing.T) {
	var gs, ge time.Time
	_, ts := newSupportTS(t, func(d *Deps) {
		d.Spend = func(_ context.Context, s, e time.Time) (spend.Result, error) {
			gs, ge = s, e
			return spend.Result{Rows: []spend.Row{{Model: "omlx/a", Requests: 3, PromptTokens: 100, CompletionTokens: 50, Spend: 0.25}}, Unattributed: 2}, nil
		}
	})
	st, b := call(t, ts, "GET", "/control/v1/spend?start=2026-11-01T00:00:00Z&end=2026-11-15T00:00:00Z", "")
	if st != 200 {
		t.Fatalf("status %d: %s", st, b)
	}
	var rep modelapi.SpendReport
	if err := json.Unmarshal(b, &rep); err != nil {
		t.Fatal(err)
	}
	want := modelapi.SpendReport{Rows: []modelapi.SpendRow{{Model: "omlx/a", Requests: 3, PromptTokens: 100, CompletionTokens: 50, Spend: 0.25}}, Unattributed: 2}
	if !reflect.DeepEqual(rep, want) {
		t.Errorf("report = %+v", rep)
	}
	if !gs.Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)) || !ge.Equal(time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("bounds = %v %v", gs, ge)
	}
	_, ts2 := newSupportTS(t, func(d *Deps) {
		d.Spend = func(context.Context, time.Time, time.Time) (spend.Result, error) { return spend.Result{}, nil }
	})
	_, b = call(t, ts2, "GET", "/control/v1/spend?start=2026-11-01T00:00:00Z&end=2026-11-15T00:00:00Z", "")
	if !strings.Contains(string(b), `"rows":[]`) {
		t.Errorf("empty rows must be []: %s", b)
	}
}

// TestSpendBoundsAreRequired pins that a missing or unparsable start or end is
// a 400 bad_request and the database is never asked.
func TestSpendBoundsAreRequired(t *testing.T) {
	asked := false
	_, ts := newSupportTS(t, func(d *Deps) {
		d.Spend = func(context.Context, time.Time, time.Time) (spend.Result, error) {
			asked = true
			return spend.Result{}, nil
		}
	})
	for _, q := range []string{
		"", "?start=2026-11-01T00:00:00Z", "?end=2026-11-15T00:00:00Z",
		"?start=yesterday&end=2026-11-15T00:00:00Z", "?start=2026-11-01T00:00:00Z&end=2026-11-15",
	} {
		st, b := call(t, ts, "GET", "/control/v1/spend"+q, "")
		wantAPIError(t, st, b, http.StatusBadRequest, modelapi.CodeBadRequest)
	}
	if asked {
		t.Error("the database was queried for a bad request")
	}
}

// TestSpendNotConfiguredVersusUnavailable pins the two 503 codes: nothing
// names the database (either sentinel) is spend_not_configured, any other
// failure is spend_unavailable with the error's text, and neither message ever
// carries the connection string, which must not leave the host. The
// not-configured message is fixed text, since the wrapped error can name the
// host's local file paths.
func TestSpendNotConfiguredVersusUnavailable(t *testing.T) {
	const dsn = "postgresql://user:secret@db.internal:5432/litellm"
	cases := []struct {
		name string
		err  error
		code string
	}{
		{"no database", fmt.Errorf("%w: reason", litellm.ErrNoDatabase), modelapi.CodeSpendNotConfigured},
		{"no connection string", spend.ErrNoConnectionString, modelapi.CodeSpendNotConfigured},
		{"unreachable", errors.New("psql could not connect to db.internal:5432"), modelapi.CodeSpendUnavailable},
	}
	for _, c := range cases {
		_, ts := newSupportTS(t, func(d *Deps) {
			d.Spend = func(context.Context, time.Time, time.Time) (spend.Result, error) { return spend.Result{}, c.err }
		})
		st, b := call(t, ts, "GET", "/control/v1/spend?start=2026-11-01T00:00:00Z&end=2026-11-15T00:00:00Z", "")
		e := wantAPIError(t, st, b, http.StatusServiceUnavailable, c.code)
		if strings.Contains(e.Error.Message, dsn) || strings.Contains(e.Error.Message, "secret") {
			t.Errorf("%s: message leaks the connection string: %q", c.name, e.Error.Message)
		}
		if c.code == modelapi.CodeSpendUnavailable && e.Error.Message != "spend unavailable: "+c.err.Error() {
			t.Errorf("%s: message = %q", c.name, e.Error.Message)
		}
		// The not-configured text is fixed: a wrapped error can name host paths.
		if c.code == modelapi.CodeSpendNotConfigured && e.Error.Message != "spend is not configured on the model server" {
			t.Errorf("%s: message = %q, want the fixed text", c.name, e.Error.Message)
		}
	}
}
