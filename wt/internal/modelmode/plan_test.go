package modelmode

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// plPasted is a stand-in pasted token for the Secret test (copied from
// modelsetup/plan_test.go, whose plans have their own copy).
const plPasted = "tok-PASTED-SECRET-42"

// TestSecretRedactsEveryPrintedForm pins that a Secret never prints or
// marshals its value, so a stray %v, %#v, log or JSON dump cannot leak the
// pasted token or master key; only Reveal returns it. (Moved unchanged from
// modelsetup: Secret itself now lives here.)
func TestSecretRedactsEveryPrintedForm(t *testing.T) {
	s := Secret(plPasted)
	txt, _ := s.MarshalText()
	js, _ := json.Marshal(struct{ S Secret }{s})
	for _, out := range []string{fmt.Sprint(s), fmt.Sprintf("%v %+v %#v %s %q", s, s, s, s, s), string(txt), string(js)} {
		if strings.Contains(out, plPasted) || !strings.Contains(out, "[redacted]") {
			t.Errorf("printed form %q", out)
		}
	}
	if s.Reveal() != plPasted {
		t.Errorf("Reveal = %q", s.Reveal())
	}
	if Secret("").String() != "" {
		t.Errorf("an empty secret should print empty, so absence stays visible")
	}
}

// TestReasonSaysNothingWasChanged pins the refusal-reason words byte for
// byte: fact, way out, "nothing was changed"; the credentials reason is the
// fixed url sentence under the same three parts; RefusalError joins its
// reasons with newlines.
func TestReasonSaysNothingWasChanged(t *testing.T) {
	if got := Reason("a", "b"); got != "a; b; nothing was changed" {
		t.Errorf("Reason = %q", got)
	}
	want := "the url contains an @ (read as a user:password); wt does not store credentials in config.toml; use a token file; nothing was changed"
	if got := CredentialsReason("the url"); got != want {
		t.Errorf("CredentialsReason = %q, want %q", got, want)
	}
	if got := (&RefusalError{Reasons: []string{"x", "y"}}).Error(); got != "x\ny" {
		t.Errorf("RefusalError.Error() = %q, want %q", got, "x\ny")
	}
}

// TestStepLabelKnowsOnlyTheSessionKinds pins the split vocabulary: modelmode
// labels the two kinds the session side writes (token file, [model-server]);
// every admin kind (plist, bootout, …) answers unknown here and is labelled
// by modelsetup's own stepLabel.
func TestStepLabelKnowsOnlyTheSessionKinds(t *testing.T) {
	if l, ok := StepLabel(StepToken); !ok || l != "token file" {
		t.Errorf("StepLabel(StepToken) = (%q, %t), want (\"token file\", true)", l, ok)
	}
	if l, ok := StepLabel(StepConfig); !ok || l != "write [model-server]" {
		t.Errorf("StepLabel(StepConfig) = (%q, %t), want (\"write [model-server]\", true)", l, ok)
	}
	for _, k := range []StepKind{"plist", "bootout"} {
		if l, ok := StepLabel(k); ok || l != "" {
			t.Errorf("StepLabel(%q) = (%q, %t), want (\"\", false)", k, l, ok)
		}
	}
}

// TestFormatNamesEveryStepAndNote pins Format's exact words: the mode, the
// step count in singular and plural, the numbered steps and the notes
// section — the admin goldens and the editor's plan screen both render it.
func TestFormatNamesEveryStepAndNote(t *testing.T) {
	if got := (Plan{Want: Desired{Mode: ModeNative}}).Format(); got != "nothing to change for mode native\n" {
		t.Errorf("empty: %q", got)
	}
	if got := (Plan{Want: Desired{Mode: ModeRemote}, Steps: []Step{{Detail: "a"}, {Detail: "b"}}, Notes: []string{"n"}}).Format(); got != "set up mode remote in 2 steps:\n  1. a\n  2. b\nleft alone and notes:\n  - n\n" {
		t.Errorf("two steps: %q", got)
	}
	if got := (Plan{Want: Desired{Mode: ModeRemote}, Steps: []Step{{Detail: "a"}}}).Format(); got != "set up mode remote in 1 step:\n  1. a\n" {
		t.Errorf("one step: %q", got)
	}
}

// TestWritesURLNeedsAConfigStepAndAURL pins WritesURL: a plan writes the url
// only when it has a config step and a url to write.
func TestWritesURLNeedsAConfigStepAndAURL(t *testing.T) {
	if !(Plan{Want: Desired{URL: "u"}, Steps: []Step{{Kind: StepConfig}}}).WritesURL() {
		t.Errorf("config step with a url: want true")
	}
	if (Plan{Want: Desired{URL: "u"}, Steps: []Step{{Kind: StepToken}}}).WritesURL() {
		t.Errorf("token step: want false")
	}
	if (Plan{Want: Desired{URL: ""}, Steps: []Step{{Kind: StepConfig}}}).WritesURL() {
		t.Errorf("no url: want false")
	}
}
