package rootmove

import (
	"errors"
	"slices"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/removal"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rescue"
)

// TestExecuteHooksFireAtTheirRungs runs a happy-path move of a dirty root
// with all five Hooks armed to record their name and delegate to the real
// behavior, and requires the recorded order to be exactly ExecuteInto,
// BeforeRelease, ReleaseRoot, AfterRelease, StepGit, each hook once. Why: the
// hooks sit at the recovery table's rungs (rows 2-3, 4 before the reset, 4
// after it, 7, 5-6); a hook consulted at the wrong rung, twice or never would
// make every row-based failure test lie about where a run stopped.
func TestExecuteHooksFireAtTheirRungs(t *testing.T) {
	t.Parallel()
	f, eng, p := dirtyPlan(t)
	var order []string
	fired := map[string]int{}
	record := func(name string) { fired[name]++; order = append(order, name) }
	eng.Hooks = Hooks{
		ExecuteInto: func(e rescue.Engine, rp *rescue.Plan, dest string) rescue.Outcome {
			record("ExecuteInto")
			return e.ExecuteInto(rp, dest)
		},
		BeforeRelease: func() { record("BeforeRelease") },
		ReleaseRoot: func(g cleanup.Git, rel removal.RootRelease) (removal.ReleaseResult, error) {
			record("ReleaseRoot")
			return removal.ReleaseRoot(g, rel)
		},
		AfterRelease: func() { record("AfterRelease") },
		StepGit: func(g cleanup.Git, args ...string) (cleanup.Out, error) {
			record("StepGit")
			return g.Run(args...)
		},
	}
	o := eng.Execute(p)
	wantStatus(t, o, rescue.StatusMoved, "")
	if want := []string{"ExecuteInto", "BeforeRelease", "ReleaseRoot", "AfterRelease", "StepGit"}; !slices.Equal(order, want) {
		t.Errorf("hook order = %q, want %q", order, want)
	}
	for _, name := range []string{"ExecuteInto", "BeforeRelease", "ReleaseRoot", "AfterRelease", "StepGit"} {
		if fired[name] != 1 {
			t.Errorf("%s fired %d times, want 1", name, fired[name])
		}
	}
	wantRootOnDefault(t, f)
}

// TestHooksAreNotSharedAcrossEngines arms only BeforeRelease on two engines
// over two repositories, stops each run at row 4 with its own failing
// ReleaseRoot stub (the shape TestExecuteRecoveryRows uses), and requires each
// counter to be exactly 1 with the other engine's still 0 after the first
// run. Why: the seams used to be package variables a test swapped, so one
// test's hook could fire in another test's run (Ruling 1: hooks are per
// Engine); a shared hook would arm one engine through the other and make the
// rung tests interfere.
func TestHooksAreNotSharedAcrossEngines(t *testing.T) {
	t.Parallel()
	rf := func() *removal.ReleaseFailed {
		return &removal.ReleaseFailed{Stage: removal.StageSwitch, Result: removal.ReleaseResult{Reset: true, Removed: 1}, Err: errors.New("planted")}
	}
	_, engA, pa := dirtyPlan(t)
	_, engB, pb := dirtyPlan(t)
	aCount, bCount := 0, 0
	arm := func(eng *Engine, n *int) {
		eng.Hooks.BeforeRelease = func() { *n++ }
		eng.Hooks.ReleaseRoot = func(cleanup.Git, removal.RootRelease) (removal.ReleaseResult, error) {
			f := rf()
			return f.Result, f
		}
	}
	arm(&engA, &aCount)
	arm(&engB, &bCount)
	oA := engA.Execute(pa)
	wantStatus(t, oA, rescue.StatusFailed, CodeReleaseFailed)
	if aCount != 1 || bCount != 0 {
		t.Errorf("after the first run A's BeforeRelease fired %d times and B's %d, want 1 and 0 (cross-count)", aCount, bCount)
	}
	oB := engB.Execute(pb)
	wantStatus(t, oB, rescue.StatusFailed, CodeReleaseFailed)
	if aCount != 1 || bCount != 1 {
		t.Errorf("after both runs A's BeforeRelease fired %d times and B's %d, want 1 and 1", aCount, bCount)
	}
}
