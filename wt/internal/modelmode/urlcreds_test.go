package modelmode

import "testing"

// TestRedactCheckFailsClosed pins redactCheck's fail-closed rule for a
// failed check's message: empty stays empty; a message holding an '@'
// itself, or any 3-byte run of a known '@' url, becomes the fixed
// CheckHidden whole; a message about plain urls comes back unchanged.
func TestRedactCheckFailsClosed(t *testing.T) {
	if got := RedactCheck(""); got != "" {
		t.Errorf("RedactCheck(\"\") = %q, want \"\"", got)
	}
	if got := RedactCheck("x@y"); got != CheckHidden {
		t.Errorf("RedactCheck(\"x@y\") = %q, want %q", got, CheckHidden)
	}
	if got := RedactCheck("dial abc: no host", "http://abc@h"); got != CheckHidden {
		t.Errorf("a 3-byte run of an at-url was not caught: %q", got)
	}
	if got := RedactCheck("plain words", "http://h:1", "http://abc@h"); got != "plain words" {
		t.Errorf("RedactCheck of plain words = %q, want it unchanged", got)
	}
}

// TestTokenPathsAndDefaultListen pins where the session-side tokens are
// written under the config home and the default listen address.
func TestTokenPathsAndDefaultListen(t *testing.T) {
	if got := DefaultLocalTokenPath("/c"); got != "/c/wt-model/server.token" {
		t.Errorf("DefaultLocalTokenPath = %q", got)
	}
	if got := RemoteTokenPath("/c"); got != "/c/agent-wt/remote-model-server.token" {
		t.Errorf("RemoteTokenPath = %q", got)
	}
	if DefaultListen != "127.0.0.1:8787" {
		t.Errorf("DefaultListen = %q", DefaultListen)
	}
}
