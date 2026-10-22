// Tests for the TIER column, the tier note on the mode line and the tiers on
// a slot profile's slot line: the advisory ratings modeltiers writes to
// tiers.toml, shown in the picker as guidance for choosing a model.
package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tiers"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tuilayout"
)

// writeTiers writes a tiers.toml rating the slot fixture's models and points
// MODELTIERS_OUT at it for the test: openrouter/a standard with three
// sources and high confidence, omlx/qwen3.8 quick with one source and low
// confidence, openrouter/b unrated with an aligned score of 9.3. claude/opus
// has no entry.
func writeTiers(t *testing.T) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "tiers.toml")
	body := `generated_at = "2026-10-12T15:58:00Z"

[[models]]
provider_id = "openrouter"
model_name = "a"
tier = "standard"
code = "S"
confidence = "high"
aligned_score = 44.3
aligned_sources = ["intelligence_index", "arena_agent", "eci"]

[[models]]
provider_id = "omlx"
model_name = "qwen3.8"
tier = "quick"
code = "Q"
confidence = "low"
aligned_score = 31.2
aligned_sources = ["intelligence_index"]

[[models]]
provider_id = "openrouter"
model_name = "b"
tier = "unrated"
code = "-"
reason = "no_votes"
confidence = "none"
aligned_score = 9.3
aligned_sources = ["arena_agent", "eci"]
`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(tiers.OutEnv, p)
}

// TestTierColumnShowsTheFilesRatings pins the TIER cell of every kind of row:
// the code and rounded aligned score of a rated model, looked up by its
// provider and provider-side name; an unrated model's score with the code's
// column left blank, right-aligned under the others' digits; "-" for a model
// the file does not have and for a slot-profile row, whose slots' tiers go on
// its slot line instead. A lookup by wt's id instead would rate nothing whose
// id is not "<provider>/<name>" (an openrouter id with a slash in its name).
func TestTierColumnShowsTheFilesRatings(t *testing.T) {
	writeTiers(t)
	tbl := slotTable(t, slotTestConfig(), idleOmlxSnapshot(), slotRowChecks())
	if !strings.Contains(tbl.header, "TIER") {
		t.Fatalf("header %q has no TIER column", tbl.header)
	}
	for id, want := range map[string]string{
		"openrouter/a":  "S44",
		"omlx/qwen3.8":  "Q31",
		"openrouter/b":  "  9",
		"claude/opus":   "-",
		"profile/cloud": "-",
		"profile/mixed": "-",
	} {
		if got := strings.TrimRight(tableItem(t, tbl, id).cells[colTier], " "); got != want {
			t.Errorf("%s: TIER = %q, want %q", id, got, want)
		}
	}
	if got := tableItem(t, tbl, "profile/mixed").slotTiers; got["sonnet"] != "S44" || got["haiku"] != "Q31" {
		t.Errorf("profile/mixed: slot tiers = %v, want sonnet S44 and haiku Q31", got)
	}
	// Unfilled slots hold the default's model (catalog.ProfileRow.Models) and
	// are rated with it; slotLine lists only the filled ones.
	if got := tableItem(t, tbl, "profile/cloud").slotTiers; got["opus"] != "S44" || got["haiku"] != "" {
		t.Errorf("profile/cloud: slot tiers = %v, want opus S44 and none for haiku (its model is unrated)", got)
	}
}

// TestTierColumnWithoutAFile pins that a missing tiers file changes nothing
// but the TIER cells, which are all "-": the picker opens, prints nothing
// about it, and no mode line carries a tier.
func TestTierColumnWithoutAFile(t *testing.T) {
	t.Setenv(tiers.OutEnv, filepath.Join(t.TempDir(), "absent.toml"))
	tbl := slotTable(t, slotTestConfig(), idleOmlxSnapshot(), slotRowChecks())
	for _, it := range tbl.items {
		if got := strings.TrimSpace(it.cells[colTier]); got != "-" {
			t.Errorf("%s: TIER = %q with no tiers file, want -", it.model.ID, got)
		}
		if it.tier != nil || it.slotTiers != nil {
			t.Errorf("%s: tier note %q / slot tiers %v with no tiers file", it.model.ID, it.tier, it.slotTiers)
		}
	}
}

// TestAnAllDashTierColumnIsDroppedFirst pins which column a table one
// column too wide for its list gives up: with ratings, the 30D usage
// column, so TIER outlasts the usage columns and COST; with no row rated or scored
// (no tiers file, the case of a user who never ran modeltiers), TIER, a
// column of dashes. Without this, that user's picker drops COST at a width
// where it fits beside everything but the dashes.
func TestAnAllDashTierColumnIsDroppedFirst(t *testing.T) {
	oneTooNarrow := func(tbl modelTable) *tuilayout.Columns {
		cols := tbl.items[0].cols
		cols.Fit(1 << 20)
		full := max(cols.Width(), utf8.RuneCountInString(cols.Header())+tuilayout.TitleRoom)
		cols.Fit(full - 1)
		return cols
	}
	t.Run("rated", func(t *testing.T) {
		writeTiers(t)
		cols := oneTooNarrow(slotTable(t, slotTestConfig(), idleOmlxSnapshot(), slotRowChecks()))
		if cols.Shown(col30D) || !cols.Shown(colTier) || !cols.Shown(col7D) {
			t.Errorf("rated table: 30D shown %v, 7D shown %v, TIER shown %v; want 30D dropped and 7D and TIER kept", cols.Shown(col30D), cols.Shown(col7D), cols.Shown(colTier))
		}
	})
	t.Run("no tiers file", func(t *testing.T) {
		t.Setenv(tiers.OutEnv, filepath.Join(t.TempDir(), "absent.toml"))
		cols := oneTooNarrow(slotTable(t, slotTestConfig(), idleOmlxSnapshot(), slotRowChecks()))
		if cols.Shown(colTier) || !cols.Shown(col30D) || !cols.Shown(colCost) {
			t.Errorf("unrated table: TIER shown %v, 30D shown %v, COST shown %v; want only TIER dropped", cols.Shown(colTier), cols.Shown(col30D), cols.Shown(colCost))
		}
	})
}

// TestWithTierNote pins how the mode line takes the tier: after the mode and
// price modeLine chose, the longest note that fits, shorter ones as the width
// shrinks, and none at all before the mode or the price gives way. The tier
// is guidance the TIER column already shows; the price and the mode are not
// shown anywhere else on a narrow terminal.
func TestWithTierNote(t *testing.T) {
	notes := tiers.Entry{Tier: "standard", Code: "S", Confidence: "high", Aligned: 44.3, HasAligned: true, Sources: 3}.Notes()
	base := "LiteLLM: on   cost 0.4/0.04/1.6"
	for _, c := range []struct {
		width int
		want  string
	}{
		{200, base + "   tier standard · score 44 (3 sources) · high confidence"},
		{76, base + "   tier standard · score 44 (3 sources)"},
		{60, base + "   tier standard · score 44"},
		{50, base + "   tier standard"},
		{40, base},
	} {
		got := withTierNote(base, notes, c.width)
		if got != c.want {
			t.Errorf("width %d: %q, want %q", c.width, got, c.want)
		}
		if lipgloss.Width(got) > c.width {
			t.Errorf("width %d: %q is %d columns", c.width, got, lipgloss.Width(got))
		}
	}
	if got := withTierNote(base, nil, 200); got != base {
		t.Errorf("no notes: %q, want the line unchanged", got)
	}
}

// TestSlotLineTiers pins the slot line with ratings: each rated slot model's
// TIER cell after its name, and on a narrow line the tiers given up before
// any slot is.
func TestSlotLineTiers(t *testing.T) {
	codes := map[string]string{"opus": "S44", "haiku": "Q34"}
	full := "opus* glm-5.2:cloud S44 · fable kimi-k3:cloud · sonnet glm-5.3-flash:cloud · haiku qwen3.8 Q34"
	plain := "opus* glm-5.2:cloud · fable kimi-k3:cloud · sonnet glm-5.3-flash:cloud · haiku qwen3.8"
	for _, c := range []struct {
		width int
		want  string
	}{
		{0, full},
		{120, full},
		{lipgloss.Width(full), full},
		{lipgloss.Width(full) - 1, plain},
		{86, plain},
		{85, "opus* glm-5.2:cloud S44 · fable kimi-k3:cloud · sonnet glm-5.3-flash:cloud · +1 more"},
		{80, "opus* glm-5.2:cloud · fable kimi-k3:cloud · sonnet glm-5.3-flash:cloud · +1 more"},
	} {
		if got := slotLine(slotLineRow(), codes, c.width); got != c.want {
			t.Errorf("width %d: %q, want %q", c.width, got, c.want)
		}
	}
}

// TestPickerScreenShowsTiers pins the picker on a real screen at 80x24 and
// 160x40, through Update and View: the TIER heading is drawn, the highlighted
// rated row's mode line names its tier after the price when there is room,
// and a profile row's slot line carries its slots' tiers.
func TestPickerScreenShowsTiers(t *testing.T) {
	writeTiers(t)
	for _, c := range []struct {
		width, height     int
		modelLine, slotLn string
	}{
		{80, 24, "LiteLLM: on   cost 0.4/0.04/1.6   tier standard · score 44 (3 sources)", "sonnet* a S44 · haiku qwen3.8 Q31"},
		{160, 40, "LiteLLM: on   cost 0.4/0.04/1.6   tier standard · score 44 (3 sources) · high confidence", "sonnet* a S44 · haiku qwen3.8 Q31"},
	} {
		stubInventory(t, runningOmlxSnapshot())
		m := slotPicker(t, slotTestConfig(), slotRowChecks(), c.width, c.height)
		m.models.Select(indexOfID(m, "openrouter/a"))
		m = resized(t, m, c.width, c.height)
		view := m.View()
		assertFits(t, "tiers", view, c.width, c.height)
		if !strings.Contains(view, "TIER") || !strings.Contains(view, "S44") {
			t.Errorf("%dx%d: no TIER column with S44:\n%s", c.width, c.height, view)
		}
		if got := footerLine(t, view); got != c.modelLine {
			t.Errorf("%dx%d: mode line = %q, want %q", c.width, c.height, got, c.modelLine)
		}
		m.models.Select(indexOfID(m, "profile/mixed"))
		m = resized(t, m, c.width, c.height)
		if got := footerLine(t, m.View()); got != c.slotLn {
			t.Errorf("%dx%d: slot line = %q, want %q", c.width, c.height, got, c.slotLn)
		}
	}
}
