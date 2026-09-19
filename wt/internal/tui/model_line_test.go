package tui

import (
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/refcount"
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

func (s *mockStore) RecordFor(agent, modelID string) error { return nil }

func (s *mockStore) CountsForAgent(agent string, ids []string) map[string]usage.UsageCounts {
	return s.Counts(ids)
}

// TestModelItemLineFormat verifies the selector table's one-line model
// rendering: header columns, then per row the family, model ID, location,
// status, exposed/running flags, cost cell, and the separate 1D/7D/30D usage
// cells (tags are no longer shown). Rows are ordered by the table's sort
// (cloud first, then non-running local alphabetically). The marker prefix is
// not part of .line — it is composed in Title(). A regression here would make
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
		expected []string // space-joined cells per row, in sorted order
	}{
		{
			name: "mixed families and tags",
			models: []config.Model{
				{ID: "m3", Family: "fam-b", ProviderID: "p2", Location: "cloud", Tags: []string{"t3"}},
				{ID: "m2", Family: "fam-a", ProviderID: "p1", Location: "local", Tags: []string{}},
				{ID: "m1", Family: "fam-a", ProviderID: "p1", Location: "local", Tags: []string{"t1", "t2"}},
			},
			// Group 1 (cloud) sorts before group 2 (non-running local, by id):
			// m3, then m1, m2. No cost data renders "-"; usage is per model.
			expected: []string{
				"fam-b m3 cloud ok - - - 100 200 300",
				"fam-a m1 local ok - - - 1 2 3",
				"fam-a m2 local ok - - - 10 20 30",
			},
		},
		{
			name: "empty family",
			models: []config.Model{
				{ID: "m1", Family: "", ProviderID: "p1", Location: "local", Tags: []string{}},
			},
			// An empty family renders as "-".
			expected: []string{
				"- m1 local ok - - - 1 2 3",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tbl := buildTable(tableInput{models: tt.models, usage: store}, refcount.NewStoreAt(t.TempDir()), "")
			if got, want := strings.Join(strings.Fields(tbl.header), " "), "FAMILY MODEL LOC STATUS EXPOSED RUNNING COST 1D 7D 30D SURVEY"; got != want {
				t.Errorf("header = %q, want %q", got, want)
			}
			if len(tbl.items) != len(tt.expected) {
				t.Fatalf("expected %d items, got %d", len(tt.expected), len(tbl.items))
			}
			for i, it := range tbl.items {
				if got := strings.Join(strings.Fields(it.line), " "); got != tt.expected[i] {
					t.Errorf("item %d: expected %q, got %q", i, tt.expected[i], got)
				}
			}
		})
	}
}
