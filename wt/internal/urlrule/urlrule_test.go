package urlrule

import (
	"strings"
	"testing"
)

// hasUserinfoTrue are strings the one url rule must read as carrying a
// user:password: an ASCII '@' anywhere counts — userinfo before the host, an
// '@' in a path or query, a schemeless pair, a bare '@', and the Hidden
// placeholder itself (it holds an '@', so it is refused like the url it
// stands for).
var hasUserinfoTrue = []string{
	"USERZ:PASSZ@litellm:4000",
	"http://USERZ:PASSZ@localhost:4000",
	"//USERZ:PASSZ@hq",
	"USERZ:p/PASSZ@hq",
	"http://localhost:4000/?k=USERZ@PASSZ",
	"@",
	Hidden,
}

// hasUserinfoFalse are strings the rule must pass through: no ASCII '@'. A
// fullwidth U+FF20 and a percent-encoded %40 are not '@' (no dialler reads
// them as the user separator).
var hasUserinfoFalse = []string{
	"http://localhost:4000",
	"https://proxy.example:4443/v1",
	"http://h/u＠x",
	"http://h/u%40x",
	"",
}

// TestHasUserinfoIsAnyASCIIAt pins the one url @ rule both binaries derive
// their refusal and hiding text from: exactly an ASCII '@' anywhere in the
// raw string, no parsing, so no hostile spelling of the separator bypasses it.
func TestHasUserinfoIsAnyASCIIAt(t *testing.T) {
	for _, raw := range hasUserinfoTrue {
		if !HasUserinfo(raw) {
			t.Errorf("HasUserinfo(%q) = false, want true", raw)
		}
	}
	for _, raw := range hasUserinfoFalse {
		if HasUserinfo(raw) {
			t.Errorf("HasUserinfo(%q) = true, want false", raw)
		}
	}
}

// TestShownReplacesAnAtURLWhole pins that Shown never shows a piece of an
// '@' url: the whole string becomes the placeholder, and the placeholder is
// itself treated as an '@' url, so re-applying the rule to it is idempotent.
func TestShownReplacesAnAtURLWhole(t *testing.T) {
	for _, raw := range hasUserinfoTrue {
		if got := Shown(raw); got != Hidden {
			t.Errorf("Shown(%q) = %q, want %q", raw, got, Hidden)
		}
	}
	for _, raw := range hasUserinfoFalse {
		if got := Shown(raw); got != raw {
			t.Errorf("Shown(%q) = %q, want it unchanged", raw, got)
		}
	}
	if !strings.Contains(Hidden, "@") {
		t.Errorf("the placeholder %q must hold an '@' so it is refused like the url it stands for", Hidden)
	}
}
