package tiers

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestPathFollowsModeltiersRule pins the file's location to modeltiers' own
// rule (paths.output_path): MODELTIERS_OUT, else XDG_CONFIG_HOME, else
// ~/.config, each only when not empty. Were wt to look elsewhere, the picker
// would show no tiers (or a stale file's) after a `modeltiers run` the user
// pointed somewhere on purpose.
func TestPathFollowsModeltiersRule(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	t.Setenv(OutEnv, "/x/out.toml")
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	if got := Path(); got != "/x/out.toml" {
		t.Errorf("with MODELTIERS_OUT: Path() = %q", got)
	}

	t.Setenv(OutEnv, "")
	if got, want := Path(), filepath.Join("/xdg", "local-ai", "tiers.toml"); got != want {
		t.Errorf("with XDG_CONFIG_HOME: Path() = %q, want %q", got, want)
	}

	t.Setenv("XDG_CONFIG_HOME", "")
	if got, want := Path(), filepath.Join(home, ".config", "local-ai", "tiers.toml"); got != want {
		t.Errorf("with neither: Path() = %q, want %q", got, want)
	}
}

// TestShortCell pins the short form for every kind of entry: the code and
// the rounded aligned score, the code alone without a score, the bare score
// for an unrated model that has one (the owner wants every score shown), and
// "-" for a model with neither. No mark for low confidence: the owner chose
// the plain cell.
func TestShortCell(t *testing.T) {
	cases := []struct {
		name string
		e    Entry
		want string
	}{
		{"rated with score", Entry{Tier: "standard", Code: "S", Aligned: 44.3, HasAligned: true}, "S44"},
		{"rounds half up", Entry{Tier: "quick", Code: "Q", Aligned: 33.5, HasAligned: true}, "Q34"},
		{"low confidence is not marked", Entry{Tier: "deep", Code: "D", Confidence: "low", Aligned: 51.6, HasAligned: true}, "D52"},
		{"no score", Entry{Tier: "marathon", Code: "M"}, "M"},
		{"unrated code", Entry{Tier: "unrated", Code: "-"}, "-"},
		{"unrated tier with a stray code", Entry{Tier: "unrated", Code: "S", Aligned: 40, HasAligned: true}, "40"},
		{"unknown code", Entry{Tier: "legendary", Code: "L", Aligned: 40, HasAligned: true}, "40"},
		{"unrated with a score", Entry{Tier: "unrated", Code: "-", Aligned: 9.3, HasAligned: true}, "9"},
		{"unrated, no score", Entry{Tier: "unrated", Code: "-", Confidence: "none"}, "-"},
		{"zero entry", Entry{}, "-"},
	}
	for _, c := range cases {
		if got := c.e.Short(); got != c.want {
			t.Errorf("%s: Short() = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestNotesShortenPartByPart pins the long forms the mode line chooses from:
// longest first, a missing part left out, no form repeated. The mode line
// shows the longest that fits, so a wrong order would drop the tier name
// before the confidence.
func TestNotesShortenPartByPart(t *testing.T) {
	cases := []struct {
		name string
		e    Entry
		want []string
	}{
		{"every part", Entry{Tier: "standard", Code: "S", Confidence: "high", Aligned: 44.3, HasAligned: true, Sources: 3}, []string{
			"tier standard · score 44 (3 sources) · high confidence",
			"tier standard · score 44 (3 sources)",
			"tier standard · score 44",
			"tier standard",
		}},
		{"one source", Entry{Tier: "standard", Code: "S", Confidence: "low", Aligned: 33.7, HasAligned: true, Sources: 1}, []string{
			"tier standard · score 34 (1 source) · low confidence",
			"tier standard · score 34 (1 source)",
			"tier standard · score 34",
			"tier standard",
		}},
		{"no score", Entry{Tier: "deep", Code: "D", Confidence: "high"}, []string{
			"tier deep · high confidence",
			"tier deep",
		}},
		{"confidence none", Entry{Tier: "quick", Code: "Q", Confidence: "none", Aligned: 30, HasAligned: true}, []string{
			"tier quick · score 30",
			"tier quick",
		}},
		{"unrated", Entry{Tier: "unrated", Code: "-"}, nil},
		{"unknown code with a score reads as unrated", Entry{Tier: "legendary", Code: "L", Confidence: "high", Aligned: 40, HasAligned: true, Sources: 1}, []string{
			"unrated · score 40 (1 source) · high confidence",
			"unrated · score 40 (1 source)",
			"unrated · score 40",
			"unrated",
		}},
		{"unrated with a score", Entry{Tier: "unrated", Code: "-", Confidence: "none", Aligned: 9.3, HasAligned: true, Sources: 2}, []string{
			"unrated · score 9 (2 sources)",
			"unrated · score 9",
			"unrated",
		}},
	}
	for _, c := range cases {
		if got := c.e.Notes(); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: Notes() = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestParseToleratesWhatItDoesNotRead pins that only the [[models]] fields
// the picker shows are read: an integer aligned score reads as a number, an
// entry missing its identity is skipped, the first of two entries for one
// model wins, and unknown keys and tables are ignored. A reader stricter
// than that would lose every rating to a key modeltiers adds.
func TestParseToleratesWhatItDoesNotRead(t *testing.T) {
	ix, err := Parse([]byte(`
generated_at = "2026-10-12T15:58:00Z"
future_key = 1

[[models]]
provider_id = "ollama"
model_name = "a"
tier = "deep"
code = "D"
aligned_score = 50
aligned_sources = ["eci"]
something_new = "x"

[[models]]
provider_id = "ollama"
model_name = "a"
tier = "quick"
code = "Q"

[[models]]
model_name = "orphan"
tier = "deep"
code = "D"

[[future_table]]
x = 1
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(ix) != 1 {
		t.Fatalf("Parse kept %d entries, want 1: %+v", len(ix), ix)
	}
	if got := ix.Rating("ollama", "a").Short(); got != "D50" {
		t.Errorf("ollama/a = %q, want D50 (the first entry, integer score)", got)
	}
	if got := ix.Rating("ollama", "missing").Short(); got != "-" {
		t.Errorf("an absent model = %q, want -", got)
	}
}

// TestLoadNeverFails pins modeltiers' rule for readers: a missing file, a
// directory, and a file that is not TOML are each no ratings, never an error
// the picker would have to show.
func TestLoadNeverFails(t *testing.T) {
	dir := t.TempDir()
	if ix := LoadFile(filepath.Join(dir, "absent.toml")); len(ix) != 0 {
		t.Errorf("missing file: %d entries", len(ix))
	}
	if ix := LoadFile(dir); len(ix) != 0 {
		t.Errorf("directory: %d entries", len(ix))
	}
	bad := filepath.Join(dir, "bad.toml")
	if err := os.WriteFile(bad, []byte("[[models]\nprovider_id = "), 0o600); err != nil {
		t.Fatal(err)
	}
	if ix := LoadFile(bad); len(ix) != 0 {
		t.Errorf("unparseable file: %d entries", len(ix))
	}
	wrongType := filepath.Join(dir, "wrong.toml")
	if err := os.WriteFile(wrongType, []byte("models = 3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ix := LoadFile(wrongType); len(ix) != 0 {
		t.Errorf("models of the wrong type: %d entries", len(ix))
	}
	if ix := LoadFile(""); ix != nil {
		t.Errorf("empty path: %v", ix)
	}
	var none Index
	if got := none.Rating("ollama", "a").Short(); got != "-" {
		t.Errorf("nil Index lookup = %q, want -", got)
	}
}

// TestLoadReadsMODELTIERS_OUT pins that Load reads the file the variable
// names, which is also how a test of the picker gives it ratings.
func TestLoadReadsMODELTIERS_OUT(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tiers.toml")
	if err := os.WriteFile(p, []byte("[[models]]\nprovider_id = \"omlx\"\nmodel_name = \"m\"\ntier = \"quick\"\ncode = \"Q\"\naligned_score = 31.2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(OutEnv, p)
	if got := Load().Rating("omlx", "m").Short(); got != "Q31" {
		t.Errorf("Load via MODELTIERS_OUT: %q, want Q31", got)
	}
}

// TestCellRightAlignsTheScore pins the TIER cell's layout: every score is
// padded on the left to the table's widest, after the code's one column, so
// the digits of S44, Q 9 and an unrated model's "  9" end in the same column.
// A regression leaves a ragged column where a one-digit score reads like a
// two-digit one cut short.
func TestCellRightAlignsTheScore(t *testing.T) {
	cases := []struct {
		name   string
		e      Entry
		digits int
		want   string
	}{
		{"two digits", Entry{Tier: "standard", Code: "S", Aligned: 44.3, HasAligned: true}, 2, "S44"},
		{"one digit padded", Entry{Tier: "quick", Code: "Q", Aligned: 9.2, HasAligned: true}, 2, "Q 9"},
		{"unrated keeps the code column blank", Entry{Tier: "unrated", Code: "-", Aligned: 9.3, HasAligned: true}, 2, "  9"},
		{"three-digit table", Entry{Tier: "deep", Code: "D", Aligned: 52, HasAligned: true}, 3, "D 52"},
		{"code alone", Entry{Tier: "marathon", Code: "M"}, 2, "M"},
		{"nothing", Entry{}, 2, "-"},
	}
	for _, c := range cases {
		if got := c.e.Cell(c.digits); got != c.want {
			t.Errorf("%s: Cell(%d) = %q, want %q", c.name, c.digits, got, c.want)
		}
	}
	if got := (Entry{Aligned: 9.3, HasAligned: true}).ScoreDigits(); got != 1 {
		t.Errorf("ScoreDigits(9) = %d, want 1", got)
	}
	if got := (Entry{}).ScoreDigits(); got != 0 {
		t.Errorf("ScoreDigits of no score = %d, want 0", got)
	}
}

// TestEntryScore pins Score, the value a table sorts by: the aligned score
// rounded to a whole number exactly as the TIER column shows it, with ok
// false when the entry has no aligned score (whatever Aligned holds), so an
// unscored row is told apart from a scored 0.
func TestEntryScore(t *testing.T) {
	for _, c := range []struct {
		name   string
		e      Entry
		score  int
		wantOK bool
	}{
		{"rounds down", Entry{Aligned: 44.4, HasAligned: true}, 44, true},
		{"rounds up", Entry{Aligned: 44.5, HasAligned: true}, 45, true},
		{"near zero is a score of 0", Entry{Aligned: 0.3, HasAligned: true}, 0, true},
		{"zero is a score", Entry{HasAligned: true}, 0, true},
		{"unrated entry keeps its score", Entry{Tier: "unrated", Code: "-", Aligned: 9.3, HasAligned: true}, 9, true},
		{"no aligned score", Entry{Tier: "marathon", Code: "M"}, 0, false},
		{"stray Aligned without HasAligned", Entry{Aligned: 30}, 30, false},
		{"zero value", Entry{}, 0, false},
	} {
		got, ok := c.e.Score()
		if got != c.score || ok != c.wantOK {
			t.Errorf("%s: Score() = %d, %v; want %d, %v", c.name, got, ok, c.score, c.wantOK)
		}
	}
}

// TestParseReadsContextAsAPositiveWholeNumber verifies `context` reads as an
// integer or a whole float, and as none (0) when it is missing, zero,
// negative, fractional or not a number: the claude launch declares the value
// as a window, and a bad one would size every compaction wrongly.
func TestParseReadsContextAsAPositiveWholeNumber(t *testing.T) {
	doc := func(v string) []byte {
		line := ""
		if v != "" {
			line = "context = " + v + "\n"
		}
		return []byte("[[models]]\nprovider_id = \"p\"\nmodel_name = \"m\"\n" + line)
	}
	for name, tc := range map[string]struct {
		val  string
		want int
	}{
		"integer":    {"262144", 262144},
		"whole":      {"262144.0", 262144},
		"missing":    {"", 0},
		"zero":       {"0", 0},
		"negative":   {"-5", 0},
		"fractional": {"1.5", 0},
		"too large":  {"9000000000", 0},
		"string":     {`"big"`, 0},
	} {
		t.Run(name, func(t *testing.T) {
			ix, err := Parse(doc(tc.val))
			if err != nil {
				t.Fatal(err)
			}
			if e, _ := ix.Lookup("p", "m"); e.Context != tc.want {
				t.Errorf("Context = %d, want %d", e.Context, tc.want)
			}
		})
	}
}

// TestParseReadsToolCallAndOpenWeights pins that open_weights and tool_call
// are read as booleans and that an entry without tool_call has HasToolCall
// false, so a caller can tell "no tool calling" from "unknown".
func TestParseReadsToolCallAndOpenWeights(t *testing.T) {
	ix, err := Parse([]byte(`
[[models]]
provider_id = "p"
model_name = "a"
open_weights = true
tool_call = true

[[models]]
provider_id = "p"
model_name = "b"
tool_call = false

[[models]]
provider_id = "p"
model_name = "c"
open_weights = "yes"
tool_call = 1
`))
	if err != nil {
		t.Fatal(err)
	}
	a, _ := ix.Lookup("p", "a")
	if !a.OpenWeights || !a.ToolCall || !a.HasToolCall {
		t.Errorf("a = %+v, want open weights and tool call set", a)
	}
	b, _ := ix.Lookup("p", "b")
	if b.OpenWeights || b.ToolCall || !b.HasToolCall {
		t.Errorf("b = %+v, want tool_call false but present", b)
	}
	c, _ := ix.Lookup("p", "c")
	if c.OpenWeights || c.ToolCall || c.HasToolCall {
		t.Errorf("c = %+v, want non-bool values read as none", c)
	}
}

// TestParseFreshness pins the generation time and the stale-source list that
// tells a caller how far to trust the file.
func TestParseFreshness(t *testing.T) {
	f, err := ParseFreshness([]byte(`
generated_at = "2026-10-30T15:30:47Z"

[sources.openrouter]
status = "stale"

[sources.arena]
status = "ok"

[sources.epoch]
status = "failed"
`))
	if err != nil {
		t.Fatal(err)
	}
	if !f.GeneratedAt.Equal(time.Date(2026, 10, 30, 15, 30, 47, 0, time.UTC)) {
		t.Errorf("GeneratedAt = %v", f.GeneratedAt)
	}
	if strings.Join(f.StaleSources, ",") != "epoch,openrouter" {
		t.Errorf("StaleSources = %v", f.StaleSources)
	}
}

// TestLoadWithFreshness pins that no file means no freshness (and no
// entries), not an error, and that a file with a generated_at reports both
// halves from one read — the candidates command decides how much to trust a
// ranking from this.
func TestLoadWithFreshness(t *testing.T) {
	t.Setenv(OutEnv, filepath.Join(t.TempDir(), "absent.toml"))
	if ix, _, ok := LoadWithFreshness(); ok || ix != nil {
		t.Errorf("LoadWithFreshness = %v, %v for a missing file, want no entries and not ok", ix, ok)
	}
	p := filepath.Join(t.TempDir(), "t.toml")
	if err := os.WriteFile(p, []byte("generated_at = \"2026-10-30T15:30:47Z\"\n\n[[models]]\nprovider_id = \"p\"\nmodel_name = \"m\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(OutEnv, p)
	ix, f, ok := LoadWithFreshness()
	if !ok || f.GeneratedAt.IsZero() {
		t.Errorf("LoadWithFreshness = %+v, %v", f, ok)
	}
	if len(ix) != 1 {
		t.Errorf("LoadWithFreshness entries = %v", ix)
	}
	// A file with no generated_at has entries but no freshness.
	if err := os.WriteFile(p, []byte("[[models]]\nprovider_id = \"p\"\nmodel_name = \"m\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ix, _, ok := LoadWithFreshness(); ok || len(ix) != 1 {
		t.Errorf("LoadWithFreshness = %v, %v, want entries without freshness", ix, ok)
	}
}
