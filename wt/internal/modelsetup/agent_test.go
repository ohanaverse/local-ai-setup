package modelsetup

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func testSpec() AgentSpec {
	return AgentSpec{
		Binary:    "/Users/user_name/.local/bin/wt-model",
		Listen:    DefaultListen,
		TokenFile: "/Users/user_name/.config/wt-model/token",
		Path:      "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin",
		Home:      "/Users/user_name",
	}
}

// TestRenderAgentPlistMatchesTheGolden pins the exact bytes of the rendered
// plist so any change to the LaunchAgent is a deliberate, reviewed one.
// Run with UPDATE_GOLDEN=1 to regenerate.
func TestRenderAgentPlistMatchesTheGolden(t *testing.T) {
	got := RenderAgentPlist(testSpec())
	if !bytes.Equal(got, RenderAgentPlist(testSpec())) {
		t.Fatal("render is not deterministic")
	}
	const golden = "testdata/agent.golden.plist"
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("plist differs from golden:\n%s", got)
	}
}

// TestParseAgentPlistRoundTrips checks that what the renderer writes is read
// back as managed with the same binary, listen address and token file, so a
// later setup run can recognise its own plist.
func TestParseAgentPlistRoundTrips(t *testing.T) {
	s := testSpec()
	p, err := ParseAgentPlist(RenderAgentPlist(s))
	if err != nil {
		t.Fatal(err)
	}
	want := AgentPlist{Managed: true, Label: AgentLabel, Binary: s.Binary, Listen: s.Listen, TokenFile: s.TokenFile}
	if p != want {
		t.Errorf("got %+v, want %+v", p, want)
	}
}

// TestParseAgentPlistWithoutTheMarkerIsNotManaged guards the overwrite rule:
// a plist is wt's only with both the marker and the label, so a hand-written
// one with our label, or one with the marker and another label, is left alone.
func TestParseAgentPlistWithoutTheMarkerIsNotManaged(t *testing.T) {
	rendered := RenderAgentPlist(testSpec())
	noMarker := bytes.Replace(rendered, []byte("<!-- managed by wt -->\n"), nil, 1)
	if bytes.Equal(noMarker, rendered) {
		t.Fatal("marker not found in rendered plist")
	}
	p, err := ParseAgentPlist(noMarker)
	if err != nil {
		t.Fatal(err)
	}
	if p.Managed || p.Label != AgentLabel {
		t.Errorf("label without marker: %+v", p)
	}
	otherLabel := []byte(strings.Replace(string(rendered), "<string>"+AgentLabel+"</string>", "<string>com.example.other</string>", 1))
	p, err = ParseAgentPlist(otherLabel)
	if err != nil {
		t.Fatal(err)
	}
	if p.Managed || p.Label != "com.example.other" {
		t.Errorf("marker with another label: %+v", p)
	}
}

// TestRenderEscapesXMLSpecials checks that XML metacharacters in any field
// are escaped and survive a parse unchanged, so the plist stays well formed.
func TestRenderEscapesXMLSpecials(t *testing.T) {
	s := testSpec()
	s.Path = "/a&b:/c<d>"
	s.TokenFile = "/tmp/x&y/token"
	out := RenderAgentPlist(s)
	if strings.Contains(string(out), "a&b") || !strings.Contains(string(out), "a&amp;b") {
		t.Errorf("PATH not escaped:\n%s", out)
	}
	p, err := ParseAgentPlist(out)
	if err != nil {
		t.Fatal(err)
	}
	if p.TokenFile != s.TokenFile || !p.Managed {
		t.Errorf("round trip: %+v", p)
	}
}

// TestParseAgentPlistRejectsBinary checks that a binary plist is an error
// rather than "not managed", since it cannot be read at all.
func TestParseAgentPlistRejectsBinary(t *testing.T) {
	if _, err := ParseAgentPlist([]byte("bplist00\x00\x01")); err == nil {
		t.Fatal("expected an error for a binary plist")
	}
}
