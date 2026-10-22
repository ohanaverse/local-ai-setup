package litellm

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// TestDiscoveredModelIDMatchesContractFixture pins wt's id for an on-disk
// model with no registry entry against docs/contracts/discovered-ids.sample.json,
// which this test reads (#195). The id must not change: the LiteLLM route wt
// writes and wt's usage and survey history key on it, and an omlx-6bit
// artifact is "omlx/…" (the family), never "omlx-6bit/…".
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
		// Verify DiscoveredModel (the public API that applies Family)
		m := DiscoveredModel(c.Provider, c.Artifact)
		if m.ID != c.ID || m.ModelName != c.Artifact || m.ProviderID != c.Provider {
			t.Errorf("DiscoveredModel(%q, %q) = id %q name %q provider %q, want id %q", c.Provider, c.Artifact, m.ID, m.ModelName, m.ProviderID, c.ID)
		}
		// Also verify config.DiscoveredModelID directly (public function used
		// in catalog.go, resolve.go, survey) — it must apply Family too.
		got := config.DiscoveredModelID(c.Provider, c.Artifact)
		if got != c.ID {
			t.Errorf("config.DiscoveredModelID(%q, %q) = %q, want %q", c.Provider, c.Artifact, got, c.ID)
		}
		// Verify localmodels.Family mapping matches the fixture's expectation
		family := localmodels.Family(c.Provider)
		expected := family + "/" + c.Artifact
		if expected != c.ID {
			t.Errorf("localmodels.Family(%q)=%q + artifact => %q, but fixture expects %q", c.Provider, family, expected, c.ID)
		}
	}
}
