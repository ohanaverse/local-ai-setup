package modelserver

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
)

// TestStopRefusesInUseUnlessConfirmed pins that stopping a model a live
// session uses is the in_use problem naming the sessions and touches
// nothing, and that confirm_in_use stops it anyway through one-entry
// BeginStops, Stop and one SettleRoutes (the stop owed a restart).
func TestStopRefusesInUseUnlessConfirmed(t *testing.T) {
	f := &fakeLife{snap: serverSnap("omlx/a"), stopOwed: true}
	d := f.deps(serverCfg())
	reg := testSessions()
	if err := reg.Register("sess-1", []string{"omlx/a"}); err != nil {
		t.Fatal(err)
	}
	res := d.StopJob(modelapi.StopRequest{Model: "omlx/a"}, reg)(context.Background(), func(string) {})
	p := wantProblem(t, res, modelapi.CodeInUse)
	if !slices.Equal(p.Sessions, []string{"sess-1"}) {
		t.Errorf("sessions = %q, want [sess-1]", p.Sessions)
	}
	if got := f.callList(); len(got) != 0 {
		t.Fatalf("calls = %q, want none on a refusal", got)
	}
	res = d.StopJob(modelapi.StopRequest{Model: "omlx/a", ConfirmInUse: true}, reg)(context.Background(), func(string) {})
	if !res.OK {
		t.Fatalf("confirmed stop = %+v, want OK", res)
	}
	want := []string{"BeginStops omlx/a", "Stop omlx/a", "SettleRoutes"}
	if got := f.callList(); !slices.Equal(got, want) {
		t.Errorf("calls = %q, want %q", got, want)
	}
}

// TestStopPoolCountsTheModelAlone pins that on a Pool provider (omlx) a
// session on a sibling model does not block the stop: the pool unloads one
// model, so only that model's sessions count. With no restart owed, routes
// are not settled.
func TestStopPoolCountsTheModelAlone(t *testing.T) {
	f := &fakeLife{snap: serverSnap("omlx/a", "omlx/b")}
	reg := testSessions()
	if err := reg.Register("sess-1", []string{"omlx/b"}); err != nil {
		t.Fatal(err)
	}
	res := f.deps(serverCfg()).StopJob(modelapi.StopRequest{Model: "omlx/a"}, reg)(context.Background(), func(string) {})
	if !res.OK {
		t.Fatalf("result = %+v, want OK", res)
	}
	if got := f.callList(); !slices.Equal(got, []string{"BeginStops omlx/a", "Stop omlx/a"}) {
		t.Errorf("calls = %q, want a stop with no route settle", got)
	}
}

// TestStopExclusiveFamilySumsSessions pins Review Focus 5: an Exclusive
// provider (mtplx) stops as a whole, so a live session on a sibling model of
// the same family blocks the stop of the running one.
func TestStopExclusiveFamilySumsSessions(t *testing.T) {
	f := &fakeLife{snap: serverSnap("mtplx/x")}
	reg := testSessions()
	if err := reg.Register("sess-sib", []string{"mtplx/y"}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register("sess-other", []string{"omlx/a", "openrouter/c"}); err != nil {
		t.Fatal(err)
	}
	res := f.deps(serverCfg()).StopJob(modelapi.StopRequest{Model: "mtplx/x"}, reg)(context.Background(), func(string) {})
	p := wantProblem(t, res, modelapi.CodeInUse)
	if !slices.Equal(p.Sessions, []string{"sess-sib"}) {
		t.Errorf("sessions = %q, want only the mtplx sibling's session", p.Sessions)
	}
	if got := f.callList(); len(got) != 0 {
		t.Errorf("calls = %q, want none", got)
	}
}

// TestStopStillFinishingIsSuccess pins that a stop the provider accepted but
// has not finished (*StillFinishingError) is a success with a step saying so.
func TestStopStillFinishingIsSuccess(t *testing.T) {
	f := &fakeLife{snap: serverSnap("omlx/a"), stopErr: &lifecycle.StillFinishingError{Provider: "ollama", Model: "a", Waited: 30 * time.Second}}
	var steps stepLog
	res := f.deps(serverCfg()).StopJob(modelapi.StopRequest{Model: "omlx/a"}, testSessions())(context.Background(), steps.step)
	if !res.OK || res.Error != nil {
		t.Fatalf("result = %+v, want OK", res)
	}
	got := steps.list()
	if len(got) == 0 || !strings.Contains(got[len(got)-1], "still lists") {
		t.Errorf("steps = %q, want a last step carrying the still-finishing note", got)
	}
}

// TestStopFailureAndUnknown pins the other stop outcomes: an engine failure is
// failed naming the model, an id nothing knows is unknown_model, a cloud
// model is failed (nothing to stop) and a local model that is not running is
// an OK no-op that touches nothing.
func TestStopFailureAndUnknown(t *testing.T) {
	f := &fakeLife{snap: serverSnap("omlx/a"), stopErr: errors.New("boom")}
	res := f.deps(serverCfg()).StopJob(modelapi.StopRequest{Model: "omlx/a"}, testSessions())(context.Background(), func(string) {})
	if p := wantProblem(t, res, modelapi.CodeFailed); !strings.Contains(p.Message, "omlx/a") || !strings.Contains(p.Message, "boom") {
		t.Errorf("message = %q, want the model and the cause", p.Message)
	}
	for _, tc := range []struct{ id, code string }{
		{"omlx/nope", modelapi.CodeUnknownModel},
		{"openrouter/c", modelapi.CodeFailed},
		{"omlx/b", ""},
	} {
		f := &fakeLife{snap: serverSnap("omlx/a")}
		res := f.deps(serverCfg()).StopJob(modelapi.StopRequest{Model: tc.id}, testSessions())(context.Background(), func(string) {})
		if tc.code == "" {
			if !res.OK {
				t.Errorf("%s: result = %+v, want an OK no-op", tc.id, res)
			}
		} else {
			wantProblem(t, res, tc.code)
		}
		if got := f.callList(); len(got) != 0 {
			t.Errorf("%s: calls = %q, want none", tc.id, got)
		}
	}
}

// TestStopExcludesRequesterSession pins StopRequest.Session: the client
// session asking for the stop is not one of the users it would take the
// model from, so a session stopping its own model needs no confirm_in_use,
// while another session's use is still refused and named; a requester beside
// another user is left out of the problem's sessions.
func TestStopExcludesRequesterSession(t *testing.T) {
	f := &fakeLife{snap: serverSnap("omlx/a")}
	d := f.deps(serverCfg())
	reg := testSessions()
	if err := reg.Register("s1", []string{"omlx/a"}); err != nil {
		t.Fatal(err)
	}
	res := d.StopJob(modelapi.StopRequest{Model: "omlx/a", Session: "other"}, reg)(context.Background(), func(string) {})
	if p := wantProblem(t, res, modelapi.CodeInUse); !slices.Equal(p.Sessions, []string{"s1"}) {
		t.Errorf("sessions = %q, want [s1]", p.Sessions)
	}
	if got := f.callList(); len(got) != 0 {
		t.Fatalf("calls = %q, want none on a refusal", got)
	}
	res = d.StopJob(modelapi.StopRequest{Model: "omlx/a", Session: "s1"}, reg)(context.Background(), func(string) {})
	if !res.OK {
		t.Fatalf("the only user stopping its own model = %+v, want OK", res)
	}
	if got := f.callList(); !slices.Equal(got, []string{"BeginStops omlx/a", "Stop omlx/a"}) {
		t.Errorf("calls = %q, want the stop", got)
	}
	if err := reg.Register("s2", []string{"omlx/a"}); err != nil {
		t.Fatal(err)
	}
	res = d.StopJob(modelapi.StopRequest{Model: "omlx/a", Session: "s1"}, reg)(context.Background(), func(string) {})
	p := wantProblem(t, res, modelapi.CodeInUse)
	if !slices.Equal(p.Sessions, []string{"s2"}) || !strings.Contains(p.Message, "1 live client session") {
		t.Errorf("problem = %+v, want only s2, counted once", p)
	}
}

// TestStillFinishingStepHasMarker pins the step a still-finishing stop ends
// on: modelapi.StepStillFinishing, ": ", then the engine's text, so a client
// can tell "accepted, not finished" from a finished stop by the step alone;
// the job is OK, not an error.
func TestStillFinishingStepHasMarker(t *testing.T) {
	still := &lifecycle.StillFinishingError{Provider: "ollama", Model: "a", Waited: 30 * time.Second}
	f := &fakeLife{snap: serverSnap("omlx/a"), stopErr: still}
	var steps stepLog
	res := f.deps(serverCfg()).StopJob(modelapi.StopRequest{Model: "omlx/a"}, testSessions())(context.Background(), steps.step)
	if !res.OK || res.Error != nil {
		t.Fatalf("result = %+v, want OK", res)
	}
	got := steps.list()
	if len(got) == 0 {
		t.Fatal("no steps")
	}
	last := got[len(got)-1]
	if want := modelapi.StepStillFinishing + ": " + still.Error(); last != want {
		t.Errorf("last step = %q, want %q", last, want)
	}
	if !strings.HasPrefix(last, "still finishing: ") {
		t.Errorf("last step = %q, want it to begin %q", last, "still finishing: ")
	}
}
