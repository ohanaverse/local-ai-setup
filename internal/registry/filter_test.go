package registry

import (
	"testing"

	"github.com/ohanaverse/agent-worktree/internal/config"
)

// FilterByTag must drop models that don't carry the requested tag. A
// regression here would silently empty the browser when the user presses
// `f` to narrow the list to "design" or another tag — they'd see no
// results and assume no models exist.
func TestFilterByTagKeepsMatching(t *testing.T) {
	models := []config.Model{
		{ID: "code-only", Tags: []string{"code"}},
		{ID: "design-only", Tags: []string{"design"}},
		{ID: "both", Tags: []string{"code", "design"}},
	}
	got := FilterByTag(models, "design")
	ids := idsOf(got)
	want := map[string]bool{"design-only": true, "both": true}
	if len(got) != 2 || !want[got[0].ID] || !want[got[1].ID] {
		t.Errorf("FilterByTag(design) = %v, want %v", ids, want)
	}
}

// FilterByTag with an empty tag must return the input unchanged so the
// no-op path is allocation-free. A regression here would force a copy on
// every keystroke when the filter is inactive.
func TestFilterByTagEmptyReturnsAll(t *testing.T) {
	models := []config.Model{
		{ID: "a", Tags: []string{"code"}},
		{ID: "b", Tags: nil},
	}
	got := FilterByTag(models, "")
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "b" {
		t.Errorf("FilterByTag(\"\") = %v, want original slice", idsOf(got))
	}
}

// FilterByTag must handle a nil/empty input gracefully. The browser
// builds the cache from registry.Discover, which can return nil when
// both curated and discovered are empty.
func TestFilterByTagEmptyInput(t *testing.T) {
	if got := FilterByTag(nil, "code"); len(got) != 0 {
		t.Errorf("FilterByTag(nil, code) = %d, want 0", len(got))
	}
}

// FilterBySource must drop models that don't match the requested source.
// Powers the optional `c` key — a regression here would show "no
// curated models" when curation is actually configured.
func TestFilterBySourceKeepsMatching(t *testing.T) {
	models := []config.Model{
		{ID: "cur-1", Source: config.SourceCurated},
		{ID: "disc-1", Source: config.SourceDiscovered},
		{ID: "cur-2", Source: config.SourceCurated},
	}
	got := FilterBySource(models, config.SourceCurated)
	ids := idsOf(got)
	if len(got) != 2 {
		t.Fatalf("FilterBySource(curated) = %v, want 2 entries", ids)
	}
	for _, m := range got {
		if m.Source != config.SourceCurated {
			t.Errorf("got %q with source %q, want curated", m.ID, m.Source)
		}
	}
}

// FilterBySource with an empty source must return the input unchanged —
// the no-filter case should be allocation-free, matching FilterByTag.
func TestFilterBySourceEmptyReturnsAll(t *testing.T) {
	models := []config.Model{
		{ID: "a", Source: config.SourceCurated},
		{ID: "b", Source: config.SourceDiscovered},
	}
	got := FilterBySource(models, "")
	if len(got) != 2 {
		t.Errorf("FilterBySource(\"\") = %d, want 2", len(got))
	}
}

// FilterBySource must handle a nil/empty input without panicking. The
// source filter runs on the same cache that may be empty on first launch.
func TestFilterBySourceEmptyInput(t *testing.T) {
	if got := FilterBySource(nil, config.SourceCurated); len(got) != 0 {
		t.Errorf("FilterBySource(nil, curated) = %d, want 0", len(got))
	}
}

func idsOf(ms []config.Model) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.ID
	}
	return out
}
