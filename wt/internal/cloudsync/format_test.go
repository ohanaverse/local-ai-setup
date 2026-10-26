package cloudsync

import (
	"slices"
	"testing"
)

// TestGraduatedLines pins the "Graduated" block both flows print: nothing for
// no ids, else a header counting them and one two-space-indented id per line,
// in the order given. The plan's text is what the user approves and what the
// command compares with the re-plan under the registry lock, so a wording
// change here must fail on this pointed test and not only broadly through the
// two plans' Format tests.
func TestGraduatedLines(t *testing.T) {
	for name, tc := range map[string]struct {
		ids  []string
		want []string
	}{
		"none":    {nil, nil},
		"empty":   {[]string{}, nil},
		"one":     {[]string{"a/x"}, []string{"Graduated (manual -> curated) (1):", "  a/x"}},
		"several": {[]string{"b/y", "a/x", "c/z"}, []string{"Graduated (manual -> curated) (3):", "  b/y", "  a/x", "  c/z"}},
	} {
		if got := graduatedLines(tc.ids); !slices.Equal(got, tc.want) {
			t.Errorf("%s: graduatedLines(%v) = %q, want %q", name, tc.ids, got, tc.want)
		}
	}
}

// TestKeptLines pins the "Kept" block: nothing for no ids, else a header that
// names the source the models are missing from (openrouter.ai for one flow,
// ollama.com/pricing for the other) and counts them, then the ids.
func TestKeptLines(t *testing.T) {
	for name, tc := range map[string]struct {
		source string
		ids    []string
		want   []string
	}{
		"none":       {"openrouter.ai", nil, nil},
		"one":        {"openrouter.ai", []string{"o/a"}, []string{"Kept (manual, not on openrouter.ai) (1):", "  o/a"}},
		"other flow": {"ollama.com/pricing", []string{"x", "y"}, []string{"Kept (manual, not on ollama.com/pricing) (2):", "  x", "  y"}},
	} {
		if got := keptLines(tc.source, tc.ids); !slices.Equal(got, tc.want) {
			t.Errorf("%s: keptLines(%q, %v) = %q, want %q", name, tc.source, tc.ids, got, tc.want)
		}
	}
}

// TestDigestLine pins the line a reviewed dry run prints and `--yes
// --approve-removals` takes: the digest appears twice, as what was reviewed
// and as the value to pass, and nothing else varies. A drift in the wording
// would leave the documented command and the line out of step.
func TestDigestLine(t *testing.T) {
	got := digestLine("0bcc560b956e")
	want := "Removal digest: 0bcc560b956e (apply non-interactively with `--yes --approve-removals 0bcc560b956e`)"
	if got != want {
		t.Errorf("digestLine = %q, want %q", got, want)
	}
}

// TestIDBlock pins the shared shape: the header first, then each id indented
// by two spaces. It does not drop an empty id or reorder, and an empty list
// is the header alone; the callers decide when to print a block at all.
func TestIDBlock(t *testing.T) {
	if got, want := idBlock("H:", nil), []string{"H:"}; !slices.Equal(got, want) {
		t.Errorf("idBlock with no ids = %q, want %q", got, want)
	}
	if got, want := idBlock("H (2):", []string{"b", "a"}), []string{"H (2):", "  b", "  a"}; !slices.Equal(got, want) {
		t.Errorf("idBlock = %q, want %q", got, want)
	}
	if got, want := idBlock("H (1):", []string{""}), []string{"H (1):", "  "}; !slices.Equal(got, want) {
		t.Errorf("idBlock with an empty id = %q, want %q", got, want)
	}
}
