package tiers

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
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

// TestShortCell pins the picker's TIER cell for every kind of entry: the code
// and the rounded aligned score, the code alone without a score, and "-" for
// an unrated or unknown model. No mark for low confidence: the owner chose
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
		{"unrated tier with a stray code", Entry{Tier: "unrated", Code: "S", Aligned: 40, HasAligned: true}, "-"},
		{"unknown code", Entry{Tier: "legendary", Code: "L", Aligned: 40, HasAligned: true}, "-"},
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
