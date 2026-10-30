package slotedit

import (
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)

// fixture is shaped like a real hand-annotated slot-profiles.toml: a file
// header comment, entries with leading comment blocks, a comment directly
// above [profile.env] inside an entry, and a config_content subtable.
const fixture = `# Slot profiles: named sets of models.
# Hand-written.

# First entry.
[[profile]]
name    = "alpha"
agent   = "claude"
default = "sonnet"

[profile.slots]
fable  = "ollama/a:cloud"
opus   = "ollama/b:cloud"
sonnet = "ollama/c:cloud"
haiku  = "ollama/d:cloud"

# Second entry, two lines
# of comment.
[[profile]]
name    = "beta"
agent   = "claude"
default = "sonnet"

[profile.slots]
opus   = "ollama/b:cloud"
sonnet = "ollama/c:cloud"

# Send the summarizer to the cloud.
[profile.env]
REMEMBER_MODEL = "ollama/c:cloud"
# Declares the real window.
CLAUDE_CODE_MAX_CONTEXT_TOKENS = "245760"

[profile.config_content.modelOverrides]
"claude-sonnet-4-6" = "ollama/c:cloud"

# Third entry.
[[profile]]   # trailing comment on the header
name    = "gamma"
agent   = "claude"
default = "opus"

[profile.slots]
opus = "openrouter/x"
`

// mustParse fails the test unless out parses as slot-profiles.toml, and
// returns its profiles.
func mustParse(t *testing.T, out []byte) []profiles.SlotProfile {
	t.Helper()
	st, err := profiles.ParseSlots(out)
	if err != nil {
		t.Fatalf("result does not parse: %v\n%s", err, out)
	}
	if len(st.Unknown) > 0 {
		t.Fatalf("result has unknown keys %v\n%s", st.Unknown, out)
	}
	return st.Profiles
}

// replaceOnce returns s with old replaced by new, failing unless old occurs
// exactly once: the expected output of an edit is the input with one exact
// textual change, so every other byte is pinned.
func replaceOnce(t *testing.T, s, old, new string) string {
	t.Helper()
	if n := strings.Count(s, old); n != 1 {
		t.Fatalf("expected %q once in the fixture, found %d", old, n)
	}
	return strings.Replace(s, old, new, 1)
}

func assertBytes(t *testing.T, got []byte, want string) {
	t.Helper()
	if string(got) != want {
		t.Fatalf("output differs\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

var sample = profiles.SlotProfile{
	Name:    "new-one",
	Agent:   "claude",
	Default: "sonnet",
	Slots: map[string]string{
		"haiku":  "ollama/d:cloud",
		"sonnet": "ollama/c:cloud",
		"fable":  "ollama/a:cloud",
	},
	Env: map[string]string{"ZED": "1", "ALPHA": "two words"},
}

const sampleText = `# A new profile.
# Second line.
[[profile]]
name    = "new-one"
agent   = "claude"
default = "sonnet"

[profile.slots]
fable  = "ollama/a:cloud"
sonnet = "ollama/c:cloud"
haiku  = "ollama/d:cloud"

[profile.env]
ALPHA = "two words"
ZED = "1"
`

// TestAddToEmptyAndMissingFile checks that Add on a missing (nil) or empty
// file writes exactly one entry in the hand-written layout, with no leading
// blank line, and that the result parses: `wt profile slots add` creates
// the file on a machine that has none.
func TestAddToEmptyAndMissingFile(t *testing.T) {
	for name, src := range map[string][]byte{"nil": nil, "empty": []byte("")} {
		t.Run(name, func(t *testing.T) {
			out, err := Add(src, sample, "A new profile.\nSecond line.")
			if err != nil {
				t.Fatal(err)
			}
			assertBytes(t, out, sampleText)
			ps := mustParse(t, out)
			if len(ps) != 1 || ps[0].Name != "new-one" || ps[0].Slots["fable"] != "ollama/a:cloud" || ps[0].Env["ALPHA"] != "two words" {
				t.Fatalf("parsed %+v", ps)
			}
		})
	}
}

// TestAddKeepsExistingBytes checks that Add only appends: the old file is a
// byte-for-byte prefix of the result, separated by one blank line, and a
// file without a trailing newline gets one. The file is hand-annotated, so
// an add must never reflow what is there.
func TestAddKeepsExistingBytes(t *testing.T) {
	out, err := Add([]byte(fixture), sample, "")
	if err != nil {
		t.Fatal(err)
	}
	body := strings.SplitN(sampleText, "[[profile]]", 2)[1]
	assertBytes(t, out, fixture+"\n[[profile]]"+body)
	if ps := mustParse(t, out); len(ps) != 4 {
		t.Fatalf("got %d profiles", len(ps))
	}

	noNL := strings.TrimSuffix(fixture, "\n")
	out, err = Add([]byte(noNL), sample, "")
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, out, fixture+"\n[[profile]]"+body)

	// A file that already ends in a blank line gets no second one.
	out, err = Add([]byte(fixture+"\n"), sample, "")
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, out, fixture+"\n[[profile]]"+body)
}

// TestAddDuplicateName checks that Add refuses a name an entry already has,
// so a second add cannot create an ambiguous file.
func TestAddDuplicateName(t *testing.T) {
	p := sample
	p.Name = "beta"
	if _, err := Add([]byte(fixture), p, ""); err == nil {
		t.Fatal("expected an error for a duplicate name")
	}
}

// TestAddRefusesFieldsItCannotWrite checks that Add errors rather than
// silently dropping args, config_content or wrapper.
func TestAddRefusesFieldsItCannotWrite(t *testing.T) {
	p := sample
	p.Args = []string{"--x"}
	if _, err := Add(nil, p, ""); err == nil {
		t.Fatal("expected an error for args")
	}
}

// TestSetReplacesSlotLineOnly checks that changing one slot rewrites only
// that line, keeping the entry's alignment, and nothing else in the file.
func TestSetReplacesSlotLineOnly(t *testing.T) {
	out, err := Set([]byte(fixture), "beta", Patch{Slots: map[string]string{"sonnet": "openrouter/new"}})
	if err != nil {
		t.Fatal(err)
	}
	want := replaceOnce(t, fixture,
		"opus   = \"ollama/b:cloud\"\nsonnet = \"ollama/c:cloud\"\n\n# Send",
		"opus   = \"ollama/b:cloud\"\nsonnet = \"openrouter/new\"\n\n# Send")
	assertBytes(t, out, want)
	mustParse(t, out)
}

// TestSetDefaultKeepsTrailingComment checks that replacing a value keeps the
// key's padding and any comment after the value.
func TestSetDefaultKeepsTrailingComment(t *testing.T) {
	src := "[[profile]]\nname = \"a\"\nagent = \"claude\"\ndefault   = \"sonnet\"  # why\n\n[profile.slots]\nopus = \"x\"\nsonnet = \"y\"\n"
	d := "opus"
	out, err := Set([]byte(src), "a", Patch{Default: &d})
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, out, replaceOnce(t, src, `default   = "sonnet"  # why`, `default   = "opus"  # why`))
}

// TestSetAddsMissingSlotAndEnvTable checks that a slot the entry lacks is
// added at the end of its table (before the comment that sits above the next
// table), and that an entry without [profile.env] gains one at the end of
// its block, before the blank line that separates it from the next entry.
// The comments of both entries stay as they were.
func TestSetAddsMissingSlotAndEnvTable(t *testing.T) {
	out, err := Set([]byte(fixture), "alpha", Patch{SetEnv: map[string]string{"B": "2", "A": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	want := replaceOnce(t, fixture,
		"haiku  = \"ollama/d:cloud\"\n\n# Second entry",
		"haiku  = \"ollama/d:cloud\"\n\n[profile.env]\nA = \"1\"\nB = \"2\"\n\n# Second entry")
	assertBytes(t, out, want)
	mustParse(t, out)

	out, err = Set([]byte(fixture), "beta", Patch{Slots: map[string]string{"haiku": "h", "fable": "f"}})
	if err != nil {
		t.Fatal(err)
	}
	want = replaceOnce(t, fixture,
		"sonnet = \"ollama/c:cloud\"\n\n# Send",
		"sonnet = \"ollama/c:cloud\"\nfable  = \"f\"\nhaiku  = \"h\"\n\n# Send")
	assertBytes(t, out, want)
	mustParse(t, out)

	// The last entry, in a file with no trailing newline, gains a table.
	noNL := strings.TrimSuffix(fixture, "\n")
	out, err = Set([]byte(noNL), "gamma", Patch{SetEnv: map[string]string{"K": "v"}})
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, out, fixture+"\n[profile.env]\nK = \"v\"\n")
	mustParse(t, out)
}

// TestSetUnsetEnvRemovesLineOnly checks that unsetting an env key removes
// that one line, and leaves the comment above it and the table alone.
func TestSetUnsetEnvRemovesLineOnly(t *testing.T) {
	out, err := Set([]byte(fixture), "beta", Patch{UnsetEnv: []string{"REMEMBER_MODEL"}})
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, out, replaceOnce(t, fixture, "REMEMBER_MODEL = \"ollama/c:cloud\"\n", ""))
	mustParse(t, out)

	if _, err := Set([]byte(fixture), "beta", Patch{UnsetEnv: []string{"NOPE"}}); err == nil {
		t.Fatal("expected an error for an env key the entry does not set")
	}
}

// TestSetKeepsSubtablesAndMidEntryComments checks that editing env in an
// entry with a comment above [profile.env], comments between env keys and a
// [profile.config_content.modelOverrides] subtable changes the targeted lines
// only: a replaced key in place, a new key after the table's last key.
func TestSetKeepsSubtablesAndMidEntryComments(t *testing.T) {
	out, err := Set([]byte(fixture), "beta", Patch{SetEnv: map[string]string{
		"REMEMBER_MODEL": "ollama/z:cloud",
		"NEW_KEY":        "n",
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := replaceOnce(t, fixture, "REMEMBER_MODEL = \"ollama/c:cloud\"", "REMEMBER_MODEL = \"ollama/z:cloud\"")
	want = replaceOnce(t, want, "CLAUDE_CODE_MAX_CONTEXT_TOKENS = \"245760\"\n", "CLAUDE_CODE_MAX_CONTEXT_TOKENS = \"245760\"\nNEW_KEY = \"n\"\n")
	assertBytes(t, out, want)
	ps := mustParse(t, out)
	if ps[1].ConfigContent == nil {
		t.Fatal("config_content lost")
	}
}

// TestSetAmbiguousName checks that Set and Remove refuse a name two entries
// share, and a name no entry has: an edit must never guess its target.
func TestSetAmbiguousName(t *testing.T) {
	src := fixture + "\n[[profile]]\nname = \"beta\"\nagent = \"claude\"\ndefault = \"opus\"\n"
	if _, err := Set([]byte(src), "beta", Patch{Slots: map[string]string{"opus": "x"}}); err == nil {
		t.Fatal("expected an error for an ambiguous name")
	}
	if _, err := Remove([]byte(src), "beta"); err == nil {
		t.Fatal("expected Remove to refuse an ambiguous name")
	}
	if _, err := Set([]byte(fixture), "nope", Patch{}); err == nil {
		t.Fatal("expected an error for a missing name")
	}
	if _, err := Remove([]byte(fixture), "nope"); err == nil {
		t.Fatal("expected Remove to refuse a missing name")
	}
}

// TestRemoveTakesCommentBlockNotHeader checks that removing the first entry
// takes its comment block but keeps the file header, which a blank line
// separates from the entry.
func TestRemoveTakesCommentBlockNotHeader(t *testing.T) {
	out, err := Remove([]byte(fixture), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	first := fixture[strings.Index(fixture, "# First entry."):strings.Index(fixture, "# Second entry")]
	assertBytes(t, out, replaceOnce(t, fixture, first, ""))
	ps := mustParse(t, out)
	if len(ps) != 2 || ps[0].Name != "beta" {
		t.Fatalf("parsed %+v", ps)
	}
	if !strings.HasPrefix(string(out), "# Slot profiles: named sets of models.\n# Hand-written.\n\n# Second entry") {
		t.Fatalf("header lost:\n%s", out)
	}
}

// TestRemoveMiddleEntryLeavesNeighbours checks that removing the middle
// entry takes its comment block, its mid-entry comments and its subtables,
// and leaves both neighbours (and the last entry's comment block) intact.
func TestRemoveMiddleEntryLeavesNeighbours(t *testing.T) {
	out, err := Remove([]byte(fixture), "beta")
	if err != nil {
		t.Fatal(err)
	}
	mid := fixture[strings.Index(fixture, "# Second entry"):strings.Index(fixture, "# Third entry.")]
	assertBytes(t, out, replaceOnce(t, fixture, mid, ""))
	ps := mustParse(t, out)
	if len(ps) != 2 || ps[0].Name != "alpha" || ps[1].Name != "gamma" {
		t.Fatalf("parsed %+v", ps)
	}
}

// TestRemoveLastEntry checks that removing the last entry also drops the
// blank separator before it, so the file still ends in exactly one newline.
func TestRemoveLastEntry(t *testing.T) {
	out, err := Remove([]byte(fixture), "gamma")
	if err != nil {
		t.Fatal(err)
	}
	cut := fixture[:strings.Index(fixture, "# Third entry.")]
	assertBytes(t, out, strings.TrimSuffix(cut, "\n"))
	mustParse(t, out)

	// The only entry: the header survives, ending in one newline.
	only := "# header\n\n# c\n[[profile]]\nname = \"a\"\n"
	out, err = Remove([]byte(only), "a")
	if err != nil {
		t.Fatal(err)
	}
	assertBytes(t, out, "# header\n")
}

// TestLeadingComments checks the one definition of "the comment belongs to
// the entry below": a contiguous # block directly above [[profile]]; a
// block cut off by a blank line, or a file header, belongs to no entry.
func TestLeadingComments(t *testing.T) {
	src := `# file header

# about a
#   indented
#
[[profile]]
name = "a"

[[profile]]
name = "b"

# orphan, separated by a blank line

[[profile]]
name = "c"
`
	got := LeadingComments([]byte(src))
	want := map[string]string{"a": "about a\n  indented\n", "b": "", "c": ""}
	if len(got) != len(want) {
		t.Fatalf("got %q", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %q want %q", k, got[k], v)
		}
	}
	if fc := LeadingComments([]byte(fixture)); fc["beta"] != "Second entry, two lines\nof comment." || fc["alpha"] != "First entry." {
		t.Errorf("fixture comments %q", fc)
	}
}
