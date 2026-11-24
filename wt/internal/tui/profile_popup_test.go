package tui

import (
	"errors"
	"os/exec"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)

// stubProfileResolver installs a resolver for one test and counts its calls.
func stubProfileResolver(t *testing.T, rp profiles.ResolvedProfile, matched bool, err error) *int {
	t.Helper()
	old := profileResolver
	calls := 0
	profileResolver = func(string, config.Model) (profiles.ResolvedProfile, bool, error) {
		calls++
		return rp, matched, err
	}
	t.Cleanup(func() { profileResolver = old })
	return &calls
}

var testProfile = profiles.ResolvedProfile{Sources: []string{"omlx/qwen3.8"}}

// TestCommitAskProfilePopupApply: a matching profile with no gate warnings
// opens the profile popup; "apply profile" binds with the answer carried on
// the model. It matters because the raw /dev/tty prompt this replaces fired
// in exactly this case, and the carried answer is what keeps the released
// phase from asking it again.
func TestCommitAskProfilePopupApply(t *testing.T) {
	forceTTY(t)
	stubRouteNotes(t)
	stubProfileResolver(t, testProfile, true, nil)
	root := rootRepo(t)
	linked := linkedWorktree(t, root)
	m := model{agent: "claude", selectedPath: linked, launchModel: config.Model{ID: "ollama/x"}, phase: phaseModel}
	var got model
	next, _ := m.commitAsk(m.launchModel, func(mm model) (model, tea.Cmd) { got = mm; return mm, nil })
	if next.phase != phaseAsk || next.ask == nil || next.ask.purpose != askProfile {
		t.Fatalf("commitAsk = phase %v; want the profile popup", next.phase)
	}
	if !strings.Contains(strings.Join(next.ask.body, "\n"), "omlx/qwen3.8") {
		t.Errorf("popup body = %v; want the profile's sources", next.ask.body)
	}
	updateMsg(next, askKey("enter"))
	if got.profileCarry == nil || !got.profileCarry.apply || len(got.profileCarry.rp.Sources) != 1 {
		t.Errorf("bound model's profileCarry = %+v; want the popup's (rp, true)", got.profileCarry)
	}
}

// TestCommitAskProfilePopupDecline: "go back" declines the launch like the
// gate's go-back: nothing is bound, the hold drop runs, the guard is released.
func TestCommitAskProfilePopupDecline(t *testing.T) {
	forceTTY(t)
	stubRouteNotes(t)
	stubProfileResolver(t, testProfile, true, nil)
	root := rootRepo(t)
	linked := linkedWorktree(t, root)
	m := model{agent: "claude", selectedPath: linked, launchModel: config.Model{ID: "ollama/x"}, phase: phaseModel}
	built, dropped := false, false
	next, _ := m.commitAsk(m.launchModel, func(mm model) (model, tea.Cmd) { built = true; return mm, nil }, func() tea.Msg { dropped = true; return nil })
	after, cmd := updateMsg(next, askKey("esc"))
	if built || after.phase != phaseModel || after.committed || after.profileCarry != nil {
		t.Errorf("decline: built %v phase %v committed %v carry %v", built, after.phase, after.committed, after.profileCarry)
	}
	if cmd == nil {
		t.Fatal("decline returned no drop command")
	}
	cmd()
	if !dropped {
		t.Error("decline did not run the hold drop")
	}
}

// TestCommitAskGateThenProfile: with warnings and a matching profile, the gate
// popup comes first, then the profile popup, then the bind carries both
// answers. It matters because the order (gate before profile) is the design's
// call: a gate decline must not waste a profile answer.
func TestCommitAskGateThenProfile(t *testing.T) {
	forceTTY(t)
	stubRouteNotes(t)
	t.Cleanup(releaseRootClaim)
	calls := stubProfileResolver(t, testProfile, true, nil)
	root := rootRepo(t)
	m := model{agent: "claude", selectedPath: root, launchModel: config.Model{ID: "ollama/x"}, phase: phaseModel}
	var got model
	next, _ := m.commitAsk(m.launchModel, func(mm model) (model, tea.Cmd) { got = mm; return mm, nil })
	if next.ask == nil || next.ask.purpose != askGate || *calls != 0 {
		t.Fatalf("first popup = %+v (resolver calls %d); want the gate, profile not yet resolved", next.ask, *calls)
	}
	next, _ = updateMsg(next, askKey("enter"))
	if next.phase != phaseAsk || next.ask.purpose != askProfile || !next.committed {
		t.Fatalf("after the gate yes: phase %v ask %+v; want the profile popup, still committed", next.phase, next.ask)
	}
	updateMsg(next, askKey("enter"))
	if got.gateAnswer == nil || got.gateTarget == nil || got.profileCarry == nil || !got.profileCarry.apply {
		t.Errorf("bound model: gate (%v, %v) carry %+v; want both answers", got.gateAnswer, got.gateTarget, got.profileCarry)
	}
}

// TestCommitAskProfileResolveErrorNoPopup: a resolve error asks nothing and
// binds unprofiled-carry-free, so the applier's own legacy path reports the
// warning once. It matters because a broken profiles.toml must never stop the
// agent from starting.
func TestCommitAskProfileResolveErrorNoPopup(t *testing.T) {
	forceTTY(t)
	stubProfileResolver(t, profiles.ResolvedProfile{}, false, errors.New("bad toml"))
	root := rootRepo(t)
	linked := linkedWorktree(t, root)
	m := model{agent: "claude", selectedPath: linked, launchModel: config.Model{ID: "ollama/x"}}
	bound := false
	next, _ := m.commitAsk(m.launchModel, func(mm model) (model, tea.Cmd) {
		bound = true
		if mm.profileCarry != nil {
			t.Errorf("carry = %+v; want none after a resolve error", mm.profileCarry)
		}
		return mm, nil
	})
	if !bound || next.phase == phaseAsk {
		t.Errorf("bound %v phase %v; want a straight bind", bound, next.phase)
	}
}

// TestCommitAskProfileNoTTYSkipsResolver: with no terminal the resolver is
// never consulted and the legacy applier path decides (today's auto-yes).
func TestCommitAskProfileNoTTYSkipsResolver(t *testing.T) {
	old := stdinTTY
	stdinTTY = func() bool { return false }
	t.Cleanup(func() { stdinTTY = old })
	calls := stubProfileResolver(t, testProfile, true, nil)
	root := rootRepo(t)
	linked := linkedWorktree(t, root)
	m := model{agent: "claude", selectedPath: linked, launchModel: config.Model{ID: "ollama/x"}}
	bound := false
	m.commitAsk(m.launchModel, func(mm model) (model, tea.Cmd) { bound = true; return mm, nil })
	if !bound || *calls != 0 {
		t.Errorf("bound %v, resolver calls %d; want a straight bind with no resolve", bound, *calls)
	}
}

// TestRunRecordAndWaitWithPassesTheCarry: the carried popup answer reaches the
// applier as (rp, apply); with no carry the applier gets (nil, false) and
// resolves for itself. It matters because this hop is what stops a second
// prompt in the released phase.
func TestRunRecordAndWaitWithPassesTheCarry(t *testing.T) {
	old := profileApplier
	t.Cleanup(func() { profileApplier = old })
	var gotRP *profiles.ResolvedProfile
	var gotApply bool
	profileApplier = func(_ *exec.Cmd, _ string, _ config.Model, rp *profiles.ResolvedProfile, apply bool, _ bool) (func() error, error) {
		gotRP, gotApply = rp, apply
		return func() error { return nil }, nil
	}
	rp := testProfile
	runRecordAndWaitWith(exec.Command("true"), "claude", config.Model{ID: "ollama/x"}, nil, nil, func() {}, &profileCarry{rp: &rp, apply: true}, false)()
	if gotRP == nil || !gotApply {
		t.Errorf("applier got (%v, %v); want the carried (rp, true)", gotRP, gotApply)
	}
	runRecordAndWaitWith(exec.Command("true"), "claude", config.Model{ID: "ollama/x"}, nil, nil, func() {}, nil, false)()
	if gotRP != nil || gotApply {
		t.Errorf("applier got (%v, %v) with no carry; want (nil, false)", gotRP, gotApply)
	}
}

// TestDeclineAfterGateYesQueuesNoRouteNotes: a gate yes followed by a profile
// "no" declines the whole launch, so the launching/creating line the gate
// yes would have queued must never reach the terminal. It matters because a
// declined launch populates nothing: a stale "creating branch …" line would
// claim something that never happened and would print above the next launch.
func TestDeclineAfterGateYesQueuesNoRouteNotes(t *testing.T) {
	forceTTY(t)
	stubRouteNotes(t)
	t.Cleanup(releaseRootClaim)
	stubProfileResolver(t, testProfile, true, nil)
	root := rootRepo(t)
	m := model{agent: "claude", selectedPath: root, launchModel: config.Model{ID: "ollama/x"}, phase: phaseModel}
	next, _ := m.commitAsk(m.launchModel, func(mm model) (model, tea.Cmd) { return mm, nil })
	next, _ = updateMsg(next, askKey("enter")) // gate yes -> profile popup
	if next.ask == nil || next.ask.purpose != askProfile {
		t.Fatalf("want the profile popup, got %+v", next.ask)
	}
	updateMsg(next, askKey("esc")) // profile go back -> decline
	if pendingRouteNotes != "" {
		t.Errorf("route notes after a decline = %q; want none", pendingRouteNotes)
	}
}

// TestCommitAskProfilePopupLaunchWithoutProfile: the popup offers a third
// answer beside "apply profile" and "go back" — launch this once without the
// profile, the raw /dev/tty prompt's "n" — and it launches. It matters
// because "go back" was the only way out of the popup, so a local model whose
// profile the person did not want could not be launched from the picker at
// all, and the carried (rp, false) is what keeps the released phase from
// asking the same question again on /dev/tty.
func TestCommitAskProfilePopupLaunchWithoutProfile(t *testing.T) {
	forceTTY(t)
	stubRouteNotes(t)
	stubProfileResolver(t, testProfile, true, nil)
	linked := linkedWorktree(t, rootRepo(t))
	m := model{agent: "claude", selectedPath: linked, launchModel: config.Model{ID: "ollama/x"}, phase: phaseModel}
	var got model
	built := false
	next, _ := m.commitAsk(m.launchModel, func(mm model) (model, tea.Cmd) {
		built = true
		got = mm
		return mm, nil
	}, func() tea.Msg { return nil })
	if next.ask == nil || next.ask.purpose != askProfile {
		t.Fatalf("want the profile popup, got %+v", next.ask)
	}
	after, _ := updateMsg(next, askKey("n"))
	if !built {
		t.Fatalf("n left phase %v: want the launch to proceed without the profile", after.phase)
	}
	if after.phase == phaseAsk || after.committed {
		t.Errorf("after n: phase %v committed %v; want the picker's launching phase", after.phase, after.committed)
	}
	if got.profileCarry == nil || got.profileCarry.apply || got.profileCarry.rp == nil {
		t.Errorf("carry = %+v; want (rp, false)", got.profileCarry)
	}
}

// TestCommitAskProfilePopupCursorSelectsWithoutProfile: the third answer is a
// real choice item too — down, enter picks "launch without profile" — and the
// last item stays "go back", which still declines. It matters because the
// keys are a shortcut, not the only way to the answer.
func TestCommitAskProfilePopupCursorSelectsWithoutProfile(t *testing.T) {
	forceTTY(t)
	stubRouteNotes(t)
	stubProfileResolver(t, testProfile, true, nil)
	linked := linkedWorktree(t, rootRepo(t))
	m := model{agent: "claude", selectedPath: linked, launchModel: config.Model{ID: "ollama/x"}, phase: phaseModel}
	var got model
	next, _ := m.commitAsk(m.launchModel, func(mm model) (model, tea.Cmd) { got = mm; return mm, nil })
	if next.ask == nil || next.ask.purpose != askProfile {
		t.Fatalf("want the profile popup, got %+v", next.ask)
	}
	next, _ = updateMsg(next, askKey("down"))
	updateMsg(next, askKey("enter"))
	if got.profileCarry == nil || got.profileCarry.apply {
		t.Errorf("carry = %+v; want (rp, false) from the middle item", got.profileCarry)
	}

	var declined bool
	next, _ = m.commitAsk(m.launchModel, func(mm model) (model, tea.Cmd) { return mm, nil },
		func() tea.Msg { declined = true; return nil })
	if next.ask == nil || next.ask.purpose != askProfile {
		t.Fatalf("want the profile popup again, got %+v", next.ask)
	}
	next, _ = updateMsg(next, askKey("down"))
	next, _ = updateMsg(next, askKey("down"))
	after, cmd := updateMsg(next, askKey("enter"))
	if cmd != nil {
		cmd()
	}
	if !declined || after.phase != phaseModel {
		t.Errorf("last item: declined %v phase %v; want the decline back to the picker", declined, after.phase)
	}
}
