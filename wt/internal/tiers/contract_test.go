package tiers

import (
	"os"
	"testing"
)

// tiersFixture is modeltiers' output contract at the monorepo root: the file
// `modeltiers run` writes from its test fixtures, which modeltiers' own tests
// compare against. wt reads the same file here, so a renamed or retyped key
// in what modeltiers writes fails wt's tests in the change that makes it,
// not the first picker opened after a real run. wt-ci runs on any change
// under docs/contracts/.
const tiersFixture = "../../../docs/contracts/tiers.sample.toml"

// TestContractFixtureParses pins wt's reading of tiers.sample.toml: the
// entries the picker shows, by the identity it looks them up with
// (provider_id + model_name, config.Model's ProviderID and ModelName), with
// their codes, rounded aligned scores, tiers, confidences and source counts;
// and the unrated entries reading as "-". Without it a format change in
// modeltiers would leave every TIER cell "-" with no test failing.
func TestContractFixtureParses(t *testing.T) {
	data, err := os.ReadFile(tiersFixture)
	if err != nil {
		t.Fatalf("read the contract fixture: %v", err)
	}
	ix, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse(%s): %v", tiersFixture, err)
	}

	type want struct {
		short, tier, confidence string
		sources                 int
		note                    string
	}
	cases := map[[2]string]want{
		{"ollama", "glm-5.3:cloud"}: {"S44", "standard", "high", 3,
			"tier standard · score 44 (3 sources) · high confidence"},
		{"ollama", "deepseek-v4-pro:cloud"}: {"S44", "standard", "high", 2,
			"tier standard · score 44 (2 sources) · high confidence"},
		// The provider-side name, not wt's id (openrouter/z-ai--glm-5.3).
		{"openrouter", "z-ai/glm-5.3"}: {"S44", "standard", "high", 3,
			"tier standard · score 44 (3 sources) · high confidence"},
		{"ollama", "qwen3.8:27b-mlx"}: {"S34", "standard", "low", 1,
			"tier standard · score 34 (1 source) · low confidence"},
		{"ollama", "minimax-m3:cloud"}: {"Q35", "quick", "high", 2,
			"tier quick · score 35 (2 sources) · high confidence"},
	}
	for k, w := range cases {
		e, ok := ix.Lookup(k[0], k[1])
		if !ok {
			t.Errorf("%s/%s: not in the fixture as read", k[0], k[1])
			continue
		}
		if got := e.Short(); got != w.short {
			t.Errorf("%s/%s: Short() = %q, want %q", k[0], k[1], got, w.short)
		}
		if e.Tier != w.tier || e.Confidence != w.confidence || e.Sources != w.sources {
			t.Errorf("%s/%s: tier %q confidence %q sources %d, want %q %q %d",
				k[0], k[1], e.Tier, e.Confidence, e.Sources, w.tier, w.confidence, w.sources)
		}
		if notes := e.Notes(); len(notes) == 0 || notes[0] != w.note {
			t.Errorf("%s/%s: Notes()[0] = %q, want %q", k[0], k[1], notes, w.note)
		}
	}

	// Entries that are in the file and not rated read as no rating.
	for _, k := range [][2]string{
		{"ollama", "nomic-embed-text:latest"}, // not_applicable
		{"ollama", "gemma4:31b-mlx"},          // no_votes, a discovered row
	} {
		e, ok := ix.Lookup(k[0], k[1])
		if !ok {
			t.Errorf("%s/%s: not in the fixture as read", k[0], k[1])
			continue
		}
		if e.Rated() || e.Short() != "-" || e.Notes() != nil {
			t.Errorf("%s/%s: an unrated entry reads as %q (rated %v)", k[0], k[1], e.Short(), e.Rated())
		}
	}

	// [[unmapped]] rows and [[candidates]] are not models wt can look up.
	if _, ok := ix.Lookup("ollama", "medgemma:27b"); ok {
		t.Errorf("an [[unmapped]] row was read as an entry")
	}
	if got := len(ix); got != 7 {
		t.Errorf("the fixture has %d [[models]] entries as read, want 7", got)
	}
}
