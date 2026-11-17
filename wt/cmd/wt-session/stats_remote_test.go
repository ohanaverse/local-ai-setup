package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
)

// statsRig points an app at a fake model server whose catalog holds one
// family-qwen row and seeds launches in its isolated config home.
func statsRig(t *testing.T) (*app, *fakeModelServer) {
	t.Helper()
	f := newFakeModelServer(t, localRow("omlx/q", "running", "launch", nil))
	f.Rows[0].Family = "qwen"
	a := pointAtFake(t, f, "")
	a.hooks.newClient = fastModelClient
	return a, f
}

// seedLaunchesHome writes usage.jsonl under the config home pointAtFake made.
func seedLaunchesHome(t *testing.T, models ...string) {
	t.Helper()
	asOf := time.Now().UTC()
	var data []byte
	for _, m := range models {
		line, _ := json.Marshal(map[string]any{"model_id": m, "agent": "claude", "timestamp": asOf.Add(-time.Hour)})
		data = append(data, append(line, '\n')...)
	}
	if err := os.WriteFile(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "agent-wt", "usage.jsonl"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// runStatsOK runs `wt stats args…`, returning stdout and stderr.
func runStatsOK(t *testing.T, a *app, args ...string) (string, string) {
	t.Helper()
	return runStats(t, a, args...)
}

// TestStatsSpendFromTheServer proves that with an endpoint configured the
// usage table's spend comes from the model server: spend rows join to the
// catalog's families (--family qwen finds omlx/q, whose prefix would say
// omlx), and a server that has no database (spend_not_configured) or fails
// otherwise, or is unreachable, degrades with the existing statuses and notes
// while the launch counts still print.
func TestStatsSpendFromTheServer(t *testing.T) {
	t.Run("joins to catalog families", func(t *testing.T) {
		a, f := statsRig(t)
		f.Spend = modelapi.SpendReport{Rows: []modelapi.SpendRow{{Model: "omlx/q", Requests: 7, PromptTokens: 1000, CompletionTokens: 50, Spend: 0.25}}}
		seedLaunchesHome(t, "omlx/q", "omlx/other")
		out, errOut := runStatsOK(t, a, "--family", "qwen")
		if !strings.Contains(out, "omlx/q") || !strings.Contains(out, "$0.2500") || strings.Contains(out, "omlx/other") {
			t.Errorf("stdout =\n%s", out)
		}
		if errOut != "" {
			t.Errorf("stderr = %q", errOut)
		}
		if f.count("GET /control/v1/spend?") != 1 {
			t.Errorf("requests = %v", f.reqs())
		}
	})
	t.Run("a discovered row's family is not used", func(t *testing.T) {
		// The registry path maps registered models only; a model the server
		// merely found on disk falls back to its id's provider prefix.
		a, f := statsRig(t)
		disc := localRow("omlx/disc", "running", "launch", nil)
		disc.Family, disc.Registered = "qwen", false
		f.Rows = append(f.Rows, disc)
		f.Spend = modelapi.SpendReport{Rows: []modelapi.SpendRow{{Model: "omlx/disc", Requests: 2, Spend: 0.5}}}
		seedLaunchesHome(t, "omlx/disc")
		out, _ := runStatsOK(t, a, "--family", "qwen")
		if strings.Contains(out, "omlx/disc") {
			t.Errorf("a discovered row was matched by its family:\n%s", out)
		}
	})
	degraded := map[string]struct {
		status string
		setup  func(f *fakeModelServer)
		note   string
		reason string // what the reason must carry: the server's own words
	}{
		"not configured": {"not_configured", func(f *fakeModelServer) { f.SpendCode = modelapi.CodeSpendNotConfigured }, "wt: spend unavailable: ", "the LiteLLM database is not reachable"},
		"unavailable":    {"unavailable", func(f *fakeModelServer) { f.SpendCode = modelapi.CodeSpendUnavailable }, "wt: spend unavailable: ", "the LiteLLM database is not reachable"},
		"unreachable":    {"unavailable", func(f *fakeModelServer) { f.Close() }, "wt: spend unavailable: ", "is not reachable"},
	}
	for name, tc := range degraded {
		t.Run(name, func(t *testing.T) {
			a, f := statsRig(t)
			seedLaunchesHome(t, "omlx/q")
			tc.setup(f)
			out, errOut := runStatsOK(t, a, "--json")
			var doc struct {
				Usage struct {
					SpendStatus string `json:"spend_status"`
					SpendReason string `json:"spend_reason"`
					Rows        []struct {
						Model    string `json:"model"`
						Launches int    `json:"launches"`
					} `json:"rows"`
				} `json:"usage"`
			}
			if err := json.Unmarshal([]byte(out), &doc); err != nil {
				t.Fatalf("stats --json: %v\n%s", err, out)
			}
			if doc.Usage.SpendStatus != tc.status || !strings.HasPrefix(doc.Usage.SpendReason, "spend unavailable: ") || !strings.Contains(doc.Usage.SpendReason, tc.reason) {
				t.Errorf("status %q reason %q, want %q and a reason", doc.Usage.SpendStatus, doc.Usage.SpendReason, tc.status)
			}
			if len(doc.Usage.Rows) != 1 || doc.Usage.Rows[0].Model != "omlx/q" || doc.Usage.Rows[0].Launches != 1 {
				t.Errorf("launch counts lost: %+v", doc.Usage.Rows)
			}
			if !strings.HasPrefix(errOut, tc.note) {
				t.Errorf("stderr = %q, want the note %q…", errOut, tc.note)
			}
		})
	}
}

// TestSpendEndUsesServerTime is R16: with the client clock three minutes
// behind the server's, the `end` bound the spend request carries is the
// server's now (from its Date header), not the client's, so rows logged in
// the last three minutes are not cut off. The window start is the server's
// now minus the window.
func TestSpendEndUsesServerTime(t *testing.T) {
	a, f := statsRig(t)
	f.ClockSkew = 3 * time.Minute // the server is 3 minutes ahead of this machine
	seedLaunchesHome(t, "omlx/q")
	before := time.Now()
	runStatsOK(t, a, "--json", "--window", "7d")
	var q url.Values
	for _, r := range f.reqs() {
		if strings.HasPrefix(r, "GET /control/v1/spend?") {
			q, _ = url.ParseQuery(strings.TrimPrefix(r, "GET /control/v1/spend?"))
		}
	}
	if q == nil {
		t.Fatalf("no spend request in %v", f.reqs())
	}
	end, err := time.Parse(time.RFC3339, q.Get("end"))
	if err != nil {
		t.Fatalf("end = %q: %v", q.Get("end"), err)
	}
	start, err := time.Parse(time.RFC3339, q.Get("start"))
	if err != nil {
		t.Fatalf("start = %q: %v", q.Get("start"), err)
	}
	serverNow := before.Add(3 * time.Minute)
	if d := end.Sub(serverNow); d < -5*time.Second || d > 5*time.Second {
		t.Errorf("end = %s is %s from the server's now %s (the client's clock would be 3 minutes earlier)", end, d, serverNow)
	}
	if d := end.Sub(start) - 7*24*time.Hour; d != 0 {
		t.Errorf("window = %s, want 7d", end.Sub(start))
	}
}

// remotePriceNote runs the remote launch path's PriceNote for a server
// whose catalog says pricing and whose clock runs skew from the real one,
// returning what it printed on stdout.
func remotePriceNote(t *testing.T, pricing *modelapi.Pricing, skew time.Duration) string {
	t.Helper()
	a, f := statsRig(t)
	f.Pricing, f.ClockSkew = pricing, skew
	ms, err := a.openModels(t.Context(), "x")
	if err != nil {
		t.Fatal(err)
	}
	note := remotePriceNoteFunc(t.Context(), ms)
	return captureFile(t, &os.Stdout, func() { note() })
}

// TestPriceNoticeFromCatalog proves the stale-pricing notice after a remote
// launch comes from the catalog's pricing member: a stamp older than a week
// prints once (the date it names and the cloud-sync advice), a fresh one and
// a catalog with no OpenRouter-priced model print nothing, and a priced
// catalog that was never refreshed says so.
func TestPriceNoticeFromCatalog(t *testing.T) {
	stamp := func(age time.Duration) *modelapi.Pricing {
		return &modelapi.Pricing{Updated: time.Now().Add(-age).UTC().Format(time.RFC3339)}
	}
	got := remotePriceNote(t, stamp(10*24*time.Hour), 0)
	want := agents.PriceNotice(time.Now().Add(-10*24*time.Hour).Truncate(time.Second), true, time.Now()) + "\n"
	if got != want || strings.Count(got, "cloud-sync") != 1 {
		t.Errorf("stale stamp printed %q, want %q", got, want)
	}
	if got := remotePriceNote(t, stamp(time.Hour), 0); got != "" {
		t.Errorf("fresh stamp printed %q", got)
	}
	if got := remotePriceNote(t, nil, 0); got != "" {
		t.Errorf("no OpenRouter-priced model printed %q", got)
	}
	if got := remotePriceNote(t, &modelapi.Pricing{}, 0); got != "wt: token pricing has never been refreshed — run 'wt cloud-sync'\n" {
		t.Errorf("never refreshed printed %q", got)
	}
}

// TestPriceNoticeUsesServerClock is R16 for the notice: the age of the stamp
// is measured on the server's clock. The server's clock runs 2 hours behind
// this machine's, and the stamp is 6 days 23 hours old by the server's clock
// (fresh) but 7 days 1 hour old by this machine's (stale): nothing prints.
// Run the other way round (the server's clock 2 hours ahead, the stamp 7 days
// 1 hour old by the server's), the notice prints though this machine thinks
// the stamp fresh. (The notice's threshold is seven days, so the figures are
// set around it; with a client-clock bug either run would give the other
// answer.)
func TestPriceNoticeUsesServerClock(t *testing.T) {
	skew := -2 * time.Hour
	serverNow := time.Now().Add(skew)
	fresh := &modelapi.Pricing{Updated: serverNow.Add(-(7*24*time.Hour - time.Hour)).UTC().Format(time.RFC3339)}
	if got := remotePriceNote(t, fresh, skew); got != "" {
		t.Errorf("stamp fresh on the server's clock printed %q", got)
	}
	skew = 2 * time.Hour
	serverNow = time.Now().Add(skew)
	stale := &modelapi.Pricing{Updated: serverNow.Add(-(7*24*time.Hour + time.Hour)).UTC().Format(time.RFC3339)}
	if got := remotePriceNote(t, stale, skew); !strings.Contains(got, "token pricing last refreshed") {
		t.Errorf("stamp stale on the server's clock printed %q", got)
	}
}

// TestRotateListsCatalogRowsByOrder proves `wt rotate TAG` with an endpoint
// walks the server's catalog rows that carry the tag, in Order (not the
// order the server listed them in), registered rows only: with no launch
// recorded it names the first, and after a recorded launch the next one,
// wrapping at the end. A tag no row carries is the same error as before.
func TestRotateListsCatalogRowsByOrder(t *testing.T) {
	mk := func(id string, order int, tags string, reg bool) modelapi.Row {
		r := localRow(id, "running", "launch", nil)
		r.Order, r.Registered, r.Tags = order, reg, strings.Fields(tags)
		return r
	}
	f := newFakeModelServer(t,
		mk("omlx/c", 3, "code", true), mk("omlx/a", 1, "code", true), mk("omlx/x", 2, "other", true),
		mk("omlx/b", 2, "code", true), mk("omlx/found", 0, "code", false))
	a := pointAtFake(t, f, "")
	a.hooks.newClient = fastModelClient
	next := func(tag string) string {
		var err error
		out := captureFile(t, &os.Stdout, func() {
			cmd := rotateCmd(a)
			cmd.SetArgs([]string{tag})
			cmd.SetErr(&bytes.Buffer{})
			err = cmd.Execute()
		})
		if err != nil {
			return "ERR " + err.Error()
		}
		return strings.TrimSpace(out)
	}
	if got := next("code"); got != "omlx/a" {
		t.Errorf("no launch recorded: %q, want the first by Order (omlx/a)", got)
	}
	for _, step := range [][2]string{{"omlx/a", "omlx/b"}, {"omlx/b", "omlx/c"}, {"omlx/c", "omlx/a"}} {
		if err := os.WriteFile(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "agent-wt", "rotation.state"), []byte(step[0]+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := next("code"); got != step[1] {
			t.Errorf("after %s: %q, want %s", step[0], got, step[1])
		}
	}
	if got := next("nothing"); got != fmt.Sprintf("ERR no models tagged %q", "nothing") {
		t.Errorf("unknown tag: %q", got)
	}
	if f.count("GET /control/v1/catalog") == 0 {
		t.Errorf("rotate never asked the server: %v", f.reqs())
	}
}

// TestRemoteLaunchPrintsThePriceNoticeAfterTheSummary proves the non-TUI
// remote launch wires the notice (runTail.PriceNote): a catalog whose pricing
// stamp is stale on the server's clock puts the notice on stdout after the
// summary, once; a fresh stamp prints none.
func TestRemoteLaunchPrintsThePriceNoticeAfterTheSummary(t *testing.T) {
	for name, tc := range map[string]struct {
		age  time.Duration
		want int
	}{"stale": {10 * 24 * time.Hour, 1}, "fresh": {time.Hour, 0}} {
		t.Run(name, func(t *testing.T) {
			r := newRemoteRig(t, localRow("omlx/q", "running", "launch", boolp(true)))
			r.f.Pricing = &modelapi.Pricing{Updated: time.Now().Add(-tc.age).UTC().Format(time.RFC3339)}
			var err error
			out := captureFile(t, &os.Stdout, func() {
				err = launchFilteredRemote(t.Context(), r.a, "claude", t.TempDir(), false, "", "", "", false, nil, "", false, nil)
			})
			if err != nil {
				t.Fatal(err)
			}
			if n := strings.Count(out, "token pricing last refreshed"); n != tc.want {
				t.Errorf("notice printed %d times, want %d:\n%s", n, tc.want, out)
			}
			if tc.want == 1 && !strings.HasSuffix(strings.TrimRight(out, "\n"), "run 'wt cloud-sync'") {
				t.Errorf("the notice is not last, after the summary:\n%s", out)
			}
		})
	}
}

// TestCollectUsageNarrowingIsTheSameOnBothPaths pins what --agent, --model and
// --family do to the usage report on the server path (collectUsageRemote) and
// the no-server path (collectUsageNoServer) alike: --agent skips spend with
// spendSkippedReason (the spend log has no agent) and --model/--family mark
// the report Narrowed and keep only the rows of that model or family. Two
// models are launched and logged in spend, so a filter that never reached
// buildUsageRows leaves two rows and fails; on the server path the spend
// answer's Unattributed must be carried to the report. It exists so folding
// the two collectors' shared tail into one function cannot let the paths
// drift apart.
func TestCollectUsageNarrowingIsTheSameOnBothPaths(t *testing.T) {
	a, f := statsRig(t)
	other := localRow("omlx/z", "running", "launch", nil)
	other.Family = "other"
	f.Rows = append(f.Rows, other)
	f.Spend = modelapi.SpendReport{Unattributed: 4, Rows: []modelapi.SpendRow{
		{Model: "omlx/q", Requests: 1, Spend: 0.1}, {Model: "omlx/z", Requests: 2, Spend: 0.2}}}
	seedLaunchesHome(t, "omlx/q", "omlx/z")
	ms, err := a.openModels(context.Background(), "spend is not shown")
	if err != nil {
		t.Fatal(err)
	}
	asOf := time.Now().UTC()
	families := map[string]string{"omlx/q": "qwen", "omlx/z": "other"}
	paths := map[string]func(statsFilter) usageReport{
		"server": func(sf statsFilter) usageReport {
			return collectUsageRemote(context.Background(), ms, time.Hour, asOf, sf)
		},
		"no server": func(sf statsFilter) usageReport {
			return collectUsageNoServer(errors.New("down"), time.Hour, asOf, sf, families)
		},
	}
	models := func(rep usageReport) []string {
		var ids []string
		for _, r := range rep.Rows {
			ids = append(ids, r.Model)
		}
		return ids
	}
	for name, collect := range paths {
		t.Run(name, func(t *testing.T) {
			rep := collect(statsFilter{agent: "claude"})
			if rep.SpendStatus != spendSkipped || rep.SpendReason != spendSkippedReason || rep.Narrowed {
				t.Errorf("--agent: status %q reason %q narrowed %v", rep.SpendStatus, rep.SpendReason, rep.Narrowed)
			}
			for _, sf := range []statsFilter{{model: "omlx/q"}, {family: "qwen"}} {
				rep := collect(sf)
				if !rep.Narrowed || rep.SpendStatus == spendSkipped {
					t.Errorf("%+v: narrowed %v status %q", sf, rep.Narrowed, rep.SpendStatus)
				}
				if got := models(rep); len(got) != 1 || got[0] != "omlx/q" {
					t.Errorf("%+v: rows %v, want only omlx/q", sf, got)
				}
				if name == "server" {
					if rep.SpendStatus != spendOK || rep.Unattributed != 4 {
						t.Errorf("%+v: status %q unattributed %d, want %q and 4", sf, rep.SpendStatus, rep.Unattributed, spendOK)
					}
				}
			}
			rep = collect(statsFilter{})
			if rep.Narrowed || rep.SpendStatus == spendSkipped {
				t.Errorf("no filter: narrowed %v status %q", rep.Narrowed, rep.SpendStatus)
			}
			if got := models(rep); len(got) != 2 {
				t.Errorf("no filter: rows %v, want both models", got)
			}
		})
	}
}

// TestCollectUsageNoServerMapsTheOpenError pins the no-server spend status:
// an open error that is (or wraps) a *modelclient.NotConfiguredError reads
// spendNotConfigured, any other open error spendUnavailable, and both carry
// the error's text as the reason. Scripts read that status, so a failed
// server must not pass for an unconfigured one.
func TestCollectUsageNoServerMapsTheOpenError(t *testing.T) {
	nc := &modelclient.NotConfiguredError{MissingURL: true}
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"not configured":         {nc, spendNotConfigured},
		"wrapped not configured": {modelclient.Still(nc, "x"), spendNotConfigured},
		"unreachable":            {errors.New("connection refused"), spendUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			rep := collectUsageNoServer(tc.err, time.Hour, time.Now().UTC(), statsFilter{}, nil)
			if rep.SpendStatus != tc.want || !strings.Contains(rep.SpendReason, tc.err.Error()) {
				t.Errorf("status %q reason %q, want %q naming %q", rep.SpendStatus, rep.SpendReason, tc.want, tc.err)
			}
		})
	}
}
