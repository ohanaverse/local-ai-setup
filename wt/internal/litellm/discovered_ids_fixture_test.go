package litellm

import (
	"encoding/json"
	"os"
	"testing"
)

// TestDiscoveredModelIDMatchesContractFixture pins wt's id for an on-disk
// model with no registry entry against the fixture modelman's
// tests/contracts/test_discovered_ids_fixture.py reads too (#195). Each side
// used to pin the format with its own literals, so the two could drift apart
// with both suites green — and they must agree: modelman's running flag and
// `modelman stop <id>` name the route wt wrote, and an omlx-6bit artifact is
// "omlx/…" (the family), never "omlx-6bit/…".
func TestDiscoveredModelIDMatchesContractFixture(t *testing.T) {
	raw, err := os.ReadFile("../../../docs/contracts/discovered-ids.sample.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Cases []struct{ Provider, Artifact, ID string } `json:"cases"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Cases) == 0 {
		t.Fatal("fixture has no cases")
	}
	for _, c := range doc.Cases {
		m := DiscoveredModel(c.Provider, c.Artifact)
		if m.ID != c.ID || m.ModelName != c.Artifact || m.ProviderID != c.Provider {
			t.Errorf("DiscoveredModel(%q, %q) = id %q name %q provider %q, want id %q", c.Provider, c.Artifact, m.ID, m.ModelName, m.ProviderID, c.ID)
		}
	}
}
