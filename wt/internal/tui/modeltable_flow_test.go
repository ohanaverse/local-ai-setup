package tui

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

func itemIDs(got model) []string {
	var ids []string
	for _, it := range got.models.Items() {
		ids = append(ids, it.(*modelItem).model.ID)
	}
	return ids
}

func indexOfID(got model, id string) int {
	return slices.Index(itemIDs(got), id)
}

// TestEnterModelPhaseHeaderIsListTitle verifies the column header is the
// model list's title (so it renders above the rows) and starts with the
// FAMILY heading after the 4-rune row prefix.
func TestEnterModelPhaseHeaderIsListTitle(t *testing.T) {
	got := flowEnter(t, model{cfg: modelTestConfig(), width: 80, height: 24}, "claude")
	if !strings.HasPrefix(got.models.Title, "    FAMILY") {
		t.Errorf("Title = %q, want 4 spaces then FAMILY", got.models.Title)
	}
	// Alignment: the FAMILY column must start at the same offset in the header
	// as the family text does in the first row's line (after the prefix).
	first := got.models.Items()[0].(*modelItem)
	if !strings.Contains(first.line, first.model.Family) {
		t.Fatalf("line %q lacks family %q", first.line, first.model.Family)
	}
	if strings.Index(got.models.Title, "MODEL") != rowPrefixWidth+strings.Index(first.line, first.model.ID) {
		t.Errorf("MODEL column misaligned: title %q vs line %q", got.models.Title, first.line)
	}
}

// TestEnterModelPhaseAllLocalNoneRunningShowsStartableRows replaces the old
// gate-emptied route-back test: when every eligible model is local and none
// is running, the picker now shows the table with every row startable (Enter
// runs the engine) instead of routing back to the agent picker.
func TestEnterModelPhaseAllLocalNoneRunningShowsStartableRows(t *testing.T) {
	local := &config.Config{
		DefaultTag: "code",
		Providers:  []config.Provider{{ID: "omlx", Location: config.LocationLocal, Protocols: []config.Protocol{config.ProtocolOpenAIChat}, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}}},
		Models: []config.Model{
			{ID: "omlx/a", ProviderID: "omlx", ModelName: "a", Family: "a", Tags: []string{"code"}},
			{ID: "omlx/b", ProviderID: "omlx", ModelName: "b", Family: "b", Tags: []string{"code"}},
		},
		Agents: []config.Agent{{Name: "pi", SupportedProviders: []string{"omlx"}}},
	}
	got := flowEnter(t, model{cfg: local, width: 80, height: 24}, "pi")
	if got.phase != phaseModel {
		t.Fatalf("phase = %v, want phaseModel (no route back)", got.phase)
	}
	items := got.models.Items()
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
	for _, it := range items {
		mi := it.(*modelItem)
		if !mi.start || mi.blocked != "" {
			t.Errorf("%s: start = %v blocked = %q, want a startable row", mi.model.ID, mi.start, mi.blocked)
		}
	}
}

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// assertHeaderAlignsWithRow checks, in a rendered list view, that the header's
// FAMILY and MODEL columns start at the same rune offset as the family and
// model id cells of the first row showing id.
func assertHeaderAlignsWithRow(t *testing.T, view, id, family string) {
	t.Helper()
	var header, row string
	for _, ln := range strings.Split(ansiRE.ReplaceAllString(view, ""), "\n") {
		if strings.Contains(ln, "FAMILY") && header == "" {
			header = ln
		}
		if strings.Contains(ln, id) && row == "" {
			row = ln
		}
	}
	if header == "" || row == "" {
		t.Fatalf("header/row not found in view:\n%s", view)
	}
	off := func(s, sub string) int { return len([]rune(s[:strings.Index(s, sub)])) }
	if off(header, "MODEL") != off(row, id) {
		t.Errorf("MODEL offset %d != row id offset %d\n%q\n%q", off(header, "MODEL"), off(row, id), header, row)
	}
	if off(header, "FAMILY") != off(row, family) {
		t.Errorf("FAMILY offset %d != row family offset %d\n%q\n%q", off(header, "FAMILY"), off(row, family), header, row)
	}
}

// TestModelPickerViewHeaderAlignsWithRows renders the real list view and
// checks the header's FAMILY and MODEL columns start at the same rune offset
// as the first row's family and model id. String-level checks on Title/line
// miss list padding (TitleBar) that shifts the header relative to the rows.
func TestModelPickerViewHeaderAlignsWithRows(t *testing.T) {
	got := flowEnter(t, model{cfg: modelTestConfig(), width: 80, height: 24}, "claude")
	first := got.models.Items()[0].(*modelItem)
	assertHeaderAlignsWithRow(t, got.models.View(), first.model.ID, first.model.Family)
}
