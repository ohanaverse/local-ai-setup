package editorkit

import (
	"strings"
	"testing"
)

// TestScrubTakesTheHostWithAnAtInTheQuery pins a deliberate choice: an @ in
// a url's query makes scrub read everything before it as userinfo, so the
// host goes too. That errs on the safe side of showing a credential.
func TestScrubTakesTheHostWithAnAtInTheQuery(t *testing.T) {
	if got, want := Scrub("https://h:1?x=a@b"), "https://b"; got != want {
		t.Errorf("scrub = %q, want %q", got, want)
	}
}

// TestScrubHidesKeysWithoutMangling pins that scrub hides a real-shaped
// sk- key after any separator, and leaves a word that merely contains sk-.
func TestScrubHidesKeysWithoutMangling(t *testing.T) {
	key := "sk-" + strings.Repeat("ab12", 16)
	for _, pre := range []string{"", "=", " ", `"`, ":"} {
		got := Scrub(pre + key + " end")
		if strings.Contains(got, "ab12") || !strings.HasPrefix(got, pre+"sk-<hidden>") {
			t.Errorf("prefix %q: key not hidden or prefix lost: %q", pre, got)
		}
	}
	for _, keep := range []string{"mask-rcnn-large-v2-xxxxxxxxxxxxxxxx", "models/task-runner-abcdefghijklmnop"} {
		if got := Scrub(keep); got != keep {
			t.Errorf("scrub mangled %q into %q", keep, got)
		}
	}
}

// TestScrubTakesUserinfoThroughTheLastAt pins carry-forward 4: a password
// with an @ in it is taken out whole, not up to its first @.
func TestScrubTakesUserinfoThroughTheLastAt(t *testing.T) {
	got := Scrub("see https://me:p@ss@gpu.example:8787/x and http://a@b/ or mailto me@host")
	if want := "see https://gpu.example:8787/x and http://b/ or mailto me@host"; got != want {
		t.Errorf("scrub = %q, want %q", got, want)
	}
}
