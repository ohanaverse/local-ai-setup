package tui

import (
	"os/exec"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// TestWithSlotProfilesCarriesChecksAndApplier pins the one place Run's slot
// argument reaches the TUI: the checks land on the model, where tableFor reads
// them, and the applier lands in the slotApplier seam. Dropping either makes
// the feature vanish without an error: no profile rows, or every profile
// launch aborting with "no slot-profile applier".
func TestWithSlotProfilesCarriesChecksAndApplier(t *testing.T) {
	old := slotApplier
	t.Cleanup(func() { slotApplier = old })
	called := false
	apply := func(*exec.Cmd, string, catalog.Row, bool) (func() error, error) {
		called = true
		return nil, nil
	}
	checks := []profiles.SlotCheck{{Index: 0, Usable: true, Profile: profiles.SlotProfile{Name: "cloud", Agent: "claude"}}}

	m := withSlotProfiles(newRunModel(false, false, "", "claude", "", "", "", nil, themes.Default, ".", nil, &config.Config{}, nil), SlotProfiles{Checks: checks, Apply: apply})

	if len(m.slotChecks) != 1 || m.slotChecks[0].Profile.Name != "cloud" {
		t.Fatalf("slotChecks = %+v, want the one check handed in", m.slotChecks)
	}
	if slotApplier == nil {
		t.Fatal("slotApplier is nil: a profile launch would abort")
	}
	if _, _ = slotApplier(nil, "", catalog.Row{}, false); !called {
		t.Fatal("slotApplier is not the function handed in")
	}
}

// stubSlotApplier sets the slotApplier seam for one test.
func stubSlotApplier(t *testing.T, apply SlotProfileApplier) {
	t.Helper()
	old := slotApplier
	slotApplier = apply
	t.Cleanup(func() { slotApplier = old })
}

// What a settling restart can print while it lets a request in flight finish
// (#377): the notice after two seconds, the warning at the bound.
const (
	drainNote  = "wt: the LiteLLM proxy is finishing a request in flight; it restarts when the request ends (up to 3m0s)\n"
	cutOffNote = "wt: the LiteLLM proxy was still serving a request after 3m0s; it was restarted anyway and that request was cut off\n"
)
