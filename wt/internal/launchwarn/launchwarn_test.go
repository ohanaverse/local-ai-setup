package launchwarn

import (
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/sessionclaim"
)

func fixedNow(t *testing.T) {
	t.Helper()
	old := now
	now = func() time.Time { return time.Unix(1000, 0) }
	t.Cleanup(func() { now = old })
}

func codes(ws []Warning) []string {
	var c []string
	for _, w := range ws {
		c = append(c, w.Code)
	}
	return c
}

func has(ws []Warning, code string) (Warning, bool) {
	for _, w := range ws {
		if w.Code == code {
			return w, true
		}
	}
	return Warning{}, false
}

func base() Target {
	return Target{Dir: "/r/.worktrees/x", Branch: "x", Default: "main", RootBranch: "main"}
}

func TestCollectNoWarningsIsOneInfoLine(t *testing.T) {
	ws, info := Collect(base())
	if len(ws) != 0 {
		t.Fatalf("warnings = %v", ws)
	}
	if info != "launching in /r/.worktrees/x" || strings.Contains(info, "\n") {
		t.Errorf("info = %q", info)
	}
}

func TestCollectRootWarns(t *testing.T) {
	tg := base()
	tg.Dir, tg.Branch, tg.IsRoot = "/r", "main", true
	ws, _ := Collect(tg)
	if len(ws) != 1 || ws[0].Code != CodeRoot {
		t.Fatalf("warnings = %v", codes(ws))
	}
	if strings.HasPrefix(ws[0].Text, "wt: ") {
		t.Errorf("text has prefix: %q", ws[0].Text)
	}
}

func TestCollectRootChosenSuppressesRootWarning(t *testing.T) {
	tg := base()
	tg.Dir, tg.Branch, tg.IsRoot, tg.RootChosen = "/r", "feat", true, true
	tg.RootBranch = "feat"
	ws, _ := Collect(tg)
	if _, ok := has(ws, CodeRoot); ok {
		t.Errorf("root warned despite --root: %v", codes(ws))
	}
	w, ok := has(ws, CodeRootDrift)
	if !ok || w.Text != "root is on feat, not main" {
		t.Errorf("drift = %+v ok=%v", w, ok)
	}
}

func TestCollectHeld(t *testing.T) {
	fixedNow(t)
	tg := base()
	tg.Holders = []sessionclaim.Claim{{PID: 42, Started: 900, Branch: "x", Agent: "claude"}}
	ws, _ := Collect(tg)
	w, ok := has(ws, CodeHeld)
	if !ok || !strings.Contains(w.Text, "claude (pid 42, running 1m40s, started on x)") {
		t.Fatalf("held = %+v", w)
	}
	if _, ok := has(ws, CodeDirtyHeld); ok {
		t.Error("dirty_held without dirty")
	}
}

func TestCollectDefaultBranchOnlyWhenNotRoot(t *testing.T) {
	tg := base()
	tg.Branch = "main"
	ws, _ := Collect(tg)
	w, ok := has(ws, CodeDefaultBranch)
	if !ok || !strings.Contains(w.Text, "wt restore-root") {
		t.Fatalf("default = %+v ok=%v", w, ok)
	}
	tg.IsRoot, tg.RootChosen = true, true
	ws, _ = Collect(tg)
	if _, ok := has(ws, CodeDefaultBranch); ok {
		t.Error("default_branch warned for the root")
	}
}

func TestCollectPRMergedUpstreamGoneDirtyHeld(t *testing.T) {
	fixedNow(t)
	tg := base()
	tg.PRState, tg.PRNumber, tg.UpstreamGone, tg.Dirty = "merged", 7, true, true
	tg.Holders = []sessionclaim.Claim{{PID: 1, Started: 1000, Branch: "x", Agent: "pi"}}
	ws, _ := Collect(tg)
	want := []string{CodeHeld, CodeDirtyHeld, CodePRMerged, CodeUpstreamGone}
	got := codes(ws)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("codes = %v want %v", got, want)
	}
	if w, _ := has(ws, CodePRMerged); w.Text != "PR #7 is merged" {
		t.Errorf("pr text = %q", w.Text)
	}
	for _, s := range []string{"", "unknown", "open"} {
		tg := base()
		tg.PRState, tg.PRNumber = s, 7
		if ws, _ := Collect(tg); len(ws) != 0 {
			t.Errorf("state %q warned: %v", s, codes(ws))
		}
	}
}
