package modelserver

import (
	"context"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
)

// TestClientAgainstTheRealServer drives a real Server over HTTP through
// modelclient.Client: client and server drift (a renamed key, a wrong path, a
// status the client does not expect) only shows on the wire, and each side's
// own tests pass against their own fakes.
func TestClientAgainstTheRealServer(t *testing.T) {
	clk := &fakeClock{t: testNow}
	up := newFakeUpstream(t)
	f := &fakeLife{snap: serverSnap()}
	_, ts := e2eServer(t, clk, f, up)
	c := modelclient.New(modelclient.Endpoint{URL: ts.URL, Token: testToken})
	ctx := context.Background()
	const m = "omlx/a"

	if p, err := c.Ping(ctx); err != nil || p.Server != "wt-model" {
		t.Fatalf("ping = %+v, %v", p, err)
	}
	cat, err := c.Catalog(ctx)
	if err != nil || len(cat.Rows) == 0 {
		t.Fatalf("catalog = %+v, %v", cat, err)
	}
	if row, err := c.Resolve(ctx, m); err != nil || row.ID != m {
		t.Fatalf("resolve = %+v, %v", row, err)
	}
	if _, err := c.Resolve(ctx, "nope/none"); err == nil {
		t.Fatal("resolve of an unknown model succeeded")
	}
	if err := c.Register(ctx, "s1", []string{m}); err != nil {
		t.Fatal(err)
	}
	id, err := c.SubmitStart(ctx, modelapi.StartRequest{Model: m})
	if err != nil || id == "" {
		t.Fatalf("start = %q, %v", id, err)
	}
	var js modelapi.JobStatus
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if js, err = c.Job(ctx, id); err != nil {
			t.Fatal(err)
		}
		if js.Step == stepGateway {
			up.route(m) // the gateway lists the model; the job may now end
		}
		if e2eFinished(js) {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if js.State != modelapi.JobSucceeded || js.Result == nil || !js.Result.OK {
		t.Fatalf("start job = %+v", js)
	}
	if jobs, err := c.ActiveJobs(ctx, m); err != nil || len(jobs) != 0 {
		t.Fatalf("active jobs = %+v, %v", jobs, err)
	}
	if cl, err := c.Candidates(ctx, modelclient.CandidateQuery{OpenOnly: true}); err != nil || cl.Candidates == nil {
		t.Fatalf("candidates = %+v, %v", cl, err)
	}
	if known, err := c.Heartbeat(ctx, "s1", []string{m}); err != nil || !known {
		t.Fatalf("heartbeat = %v, %v", known, err)
	}
	if err := c.WaitGatewayReady(ctx); err != nil {
		t.Fatalf("gateway: %v", err)
	}
	if err := c.End(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	if known, err := c.Heartbeat(ctx, "s1", nil); err != nil || known {
		t.Fatalf("heartbeat after end = %v, %v; want unknown", known, err)
	}
	if d := time.Until(c.Now()); d > time.Minute || d < -time.Minute {
		t.Errorf("Now() is %v from local", d)
	}
}

// TestCleanAgreesWithCleanupFlatten keeps modelclient's own copy of the
// one-line rule equal to cleanup.Flatten, which it cannot import: the two
// must not drift, or a server string would read differently from a git one.
func TestCleanAgreesWithCleanupFlatten(t *testing.T) {
	corpus := []string{
		"", "plain", "a\nb", "a\r\n\tb", "x\x1b[31mred\x07", "\x1b]0;title\x07", "‮evil‬",
		"⁦a⁩‎‏؜", "c1\u0085\u009bctl", "  lots   of space  ", "emoji 🙂\ttab", "nul\x00byte", " line sep",
	}
	for _, s := range corpus {
		if got, want := modelclient.CleanForTest(s), cleanup.Flatten(s); got != want {
			t.Errorf("clean(%q) = %q, Flatten = %q", s, got, want)
		}
	}
}
