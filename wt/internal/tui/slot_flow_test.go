package tui

import (
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// TestProfilePostExitSkipsSurveyAndPriceNotice pins what follows a
// slot-profile session: the refcount lines are released, the stop picker runs,
// the summary is printed, and neither the survey nor the price notice is. A
// profile is not one priced model: a survey would be stored under an id no
// registry model has, and the notice would name prices the session never used.
func TestProfilePostExitSkipsSurveyAndPriceNotice(t *testing.T) {
	prevSummary, prevSurvey := pendingSummary, pendingSurveyState
	prevRun, prevNotice, prevRelease, prevStop := runSurvey, emitPriceNotice, releaseSession, runStopPhase
	t.Cleanup(func() {
		pendingSummary, pendingSurveyState = prevSummary, prevSurvey
		runSurvey, emitPriceNotice, releaseSession, runStopPhase = prevRun, prevNotice, prevRelease, prevStop
	})

	var order []string
	pendingSummary = "wt: claude · profile/cloud · 1m0s"
	pendingSurveyState = pendingSurvey{agent: "claude", m: config.Model{ID: "profile/cloud"}, profile: true}
	releaseSession = func() { order = append(order, "release") }
	runSurvey = func(string, config.Model) string { order = append(order, "survey"); return "STATS-BLOCK" }
	runStopPhase = func(*config.Config) { order = append(order, "picker") }
	emitPriceNotice = func(*config.Config) { order = append(order, "notice") }

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	printPendingSummaryAndSurvey(&config.Config{})
	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)

	if got := strings.Join(order, ","); got != "release,picker" {
		t.Errorf("order = %s, want release,picker (no survey, no price notice)", got)
	}
	if s := string(out); !strings.Contains(s, "wt: claude · profile/cloud · 1m0s") || strings.Contains(s, "STATS-BLOCK") {
		t.Errorf("stdout = %q, want the summary line and no survey stats", s)
	}
	if pendingSurveyState.agent != "" || pendingSurveyState.profile || pendingSummary != "" {
		t.Errorf("pending state not reset: %+v %q", pendingSurveyState, pendingSummary)
	}
}

// TestWithSlotProfilesCarriesChecksAndApplier pins the one place Run's slot
// argument reaches the TUI: the checks land on the model, where tableFor reads
// them, and the applier lands in the slotApplier seam. Dropping either makes
// the feature vanish without an error: no profile rows, or every profile
// launch aborting with "no slot-profile applier".
func TestWithSlotProfilesCarriesChecksAndApplier(t *testing.T) {
	old := slotApplier
	t.Cleanup(func() { slotApplier = old })
	called := false
	apply := func(*exec.Cmd, string, catalog.Row) (func() error, error) {
		called = true
		return nil, nil
	}
	checks := []profiles.SlotCheck{{Index: 0, Usable: true, Profile: profiles.SlotProfile{Name: "cloud", Agent: "claude"}}}

	m := withSlotProfiles(newRunModel(false, false, "claude", "", "", "", nil, themes.Default, ".", &config.Config{}), SlotProfiles{Checks: checks, Apply: apply})

	if len(m.slotChecks) != 1 || m.slotChecks[0].Profile.Name != "cloud" {
		t.Fatalf("slotChecks = %+v, want the one check handed in", m.slotChecks)
	}
	if slotApplier == nil {
		t.Fatal("slotApplier is nil: a profile launch would abort")
	}
	if _, _ = slotApplier(nil, "", catalog.Row{}); !called {
		t.Fatal("slotApplier is not the function handed in")
	}
}
