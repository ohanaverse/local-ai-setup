package tui

import (
	"fmt"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/usage"
)

// mockStore implements usage.Store for testing.
type mockStore struct {
	counts map[string]usage.UsageCounts
}

func (s *mockStore) Counts(ids []string) map[string]usage.UsageCounts {
	res := make(map[string]usage.UsageCounts)
	for _, id := range ids {
		if c, ok := s.counts[id]; ok {
			res[id] = c
		}
	}
	return res
}

func (s *mockStore) Record(modelID string) error {
	return nil
}

// TestModelItemLineFormat verifies the compact one-line model rendering:
// family column, 30-day family count, model ID, location, per-model
// 1d/7d/30d counts, and optional tag list. A regression here would make
// the picker either unreadable or misleading about which model is selected.
func TestModelItemLineFormat(t *testing.T) {
	store := &mockStore{
		counts: map[string]usage.UsageCounts{
			"m1": {OneDay: 1, SevenDay: 2, ThirtyDay: 3},
			"m2": {OneDay: 10, SevenDay: 20, ThirtyDay: 30},
			"m3": {OneDay: 100, SevenDay: 200, ThirtyDay: 300},
		},
	}

	tests := []struct {
		name     string
		models   []config.Model
		expected []string
	}{
		{
			name: "mixed families and tags",
			models: []config.Model{
				{ID: "m3", Family: "fam-b", ProviderID: "p2", Location: "cloud", Tags: []string{"t3"}},
				{ID: "m2", Family: "fam-a", ProviderID: "p1", Location: "local", Tags: []string{}},
				{ID: "m1", Family: "fam-a", ProviderID: "p1", Location: "local", Tags: []string{"t1", "t2"}},
			},
			// Sort order should be m3 (fam30d:300), then m2, m1 (fam30d:33).
			// Within fam-a, m2 (composite 30+20+10=60) > m1 (composite 3+2+1=6).
			expected: []string{
				fmt.Sprintf("%-5s  %3d  %-2s  %-5s  %-11s [%s]", "fam-b", 300, "m3", "cloud", "100/200/300", "t3"),
				fmt.Sprintf("%-5s  %3d  %-2s  %-5s  %-11s", "fam-a", 33, "m2", "local", "10/20/30"),
				fmt.Sprintf("%-5s  %3d  %-2s  %-5s  %-11s [%s]", "fam-a", 33, "m1", "local", "1/2/3", "t1,t2"),
			},
		},
		{
			name: "empty family",
			models: []config.Model{
				{ID: "m1", Family: "", ProviderID: "p1", Location: "local", Tags: []string{}},
			},
			// famWidth: 0, but empty family renders as "-".
			// fam30d still reflects the empty-family aggregate (3), so a
			// launch under the "" family is not shown as 0 next to the sort.
			expected: []string{
				fmt.Sprintf("%-0s  %3d  %-2s  %-5s  %-11s", "-", 3, "m1", "local", "1/2/3"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// We need to use the production sortModelsByUsage but without the actual file system.
			// buildModelItems (Phase 1) will take the store.
			familyOf := make(map[string]string, len(tt.models))
			for _, m := range tt.models {
				familyOf[m.ID] = m.Family
			}
			items := buildModelItems(tt.models, familyOf, store)
			if len(items) != len(tt.expected) {
				t.Fatalf("expected %d items, got %d", len(tt.expected), len(items))
			}
			for i, it := range items {
				if it.line != tt.expected[i] {
					t.Errorf("item %d: expected %q, got %q", i, tt.expected[i], it.line)
				}
			}
		})
	}
}
