package agents

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/tlstest"
)

// TestNodeCAEnv pins decision 14's agent leg: claude and pi (Node) over an
// https gateway with a ca_file get NODE_EXTRA_CA_CERTS and its wt marker;
// codex and an http origin get nothing set. A user's own value is never
// silently overridden: equal is left alone, another file is left with a note
// in token mode, and in header mode refused unless it holds every ca_file
// certificate. A launch not reaching an https gateway clears an inherited
// value only when it is wt's own (equal to the marker), so a nested launch
// does not keep trusting the gateway's certificate; a user's value without
// the marker stays.
func TestNodeCAEnv(t *testing.T) {
	dir := t.TempDir()
	ca, _ := tlstest.Gen(t, dir, tlstest.Opts{})
	otherCert, _ := tlstest.Gen(t, dir, tlstest.Opts{})
	caBytes, _ := os.ReadFile(ca)
	otherBytes, _ := os.ReadFile(otherCert)
	bundle := filepath.Join(dir, "bundle.pem")
	if err := os.WriteFile(bundle, append(append([]byte{}, otherBytes...), caBytes...), 0o644); err != nil {
		t.Fatal(err)
	}
	const https, http = "https://192.0.2.7:8787", "http://127.0.0.1:8787"
	setBoth := []string{"NODE_EXTRA_CA_CERTS=" + ca, "WT_NODE_EXTRA_CA_CERTS=" + ca}
	clearBoth := []string{"NODE_EXTRA_CA_CERTS", "WT_NODE_EXTRA_CA_CERTS"}
	cases := []struct {
		name, agent, origin, caFile string
		header                      bool
		environ                     []string
		set, clear                  []string
		note, err                   bool
	}{
		{name: "claude", agent: "claude", origin: https, caFile: ca, set: setBoth, clear: clearBoth},
		{name: "pi header off", agent: "pi", origin: https, caFile: ca, set: setBoth, clear: clearBoth},
		{name: "claude header", agent: "claude", origin: https, caFile: ca, header: true, set: setBoth, clear: clearBoth},
		{name: "codex", agent: "codex", origin: https, caFile: ca},
		{name: "http origin", agent: "claude", origin: http, caFile: ca},
		{name: "no ca_file", agent: "claude", origin: https},
		{name: "inherited equal", agent: "claude", origin: https, caFile: ca, environ: []string{"NODE_EXTRA_CA_CERTS=" + ca}},
		{name: "other, token mode", agent: "claude", origin: https, caFile: ca, environ: []string{"NODE_EXTRA_CA_CERTS=" + otherCert}, note: true},
		{name: "other, header mode", agent: "claude", origin: https, caFile: ca, header: true, environ: []string{"NODE_EXTRA_CA_CERTS=" + otherCert}, err: true},
		{name: "other holding ca, header mode", agent: "claude", origin: https, caFile: ca, header: true, environ: []string{"NODE_EXTRA_CA_CERTS=" + bundle}},
		{name: "native inherits wt's own", agent: "claude", environ: []string{"NODE_EXTRA_CA_CERTS=/x/ca.pem", "WT_NODE_EXTRA_CA_CERTS=/x/ca.pem"}, clear: clearBoth},
		{name: "http inherits wt's own", agent: "pi", origin: http, caFile: ca, environ: []string{"NODE_EXTRA_CA_CERTS=/x/ca.pem", "WT_NODE_EXTRA_CA_CERTS=/x/ca.pem"}, clear: clearBoth},
		{name: "codex inherits wt's own", agent: "codex", origin: https, caFile: ca, environ: []string{"NODE_EXTRA_CA_CERTS=/x/ca.pem", "WT_NODE_EXTRA_CA_CERTS=/x/ca.pem"}, clear: clearBoth},
		{name: "native, user value", agent: "claude", environ: []string{"NODE_EXTRA_CA_CERTS=/mine.pem"}},
		{name: "https replaces wt's own for another server", agent: "claude", origin: https, caFile: ca, environ: []string{"NODE_EXTRA_CA_CERTS=/x/ca.pem", "WT_NODE_EXTRA_CA_CERTS=/x/ca.pem"}, set: setBoth, clear: clearBoth},
	}
	for _, c := range cases {
		set, clear, note, err := nodeCAEnv(c.agent, c.origin, c.caFile, c.header, c.environ)
		if (err != nil) != c.err {
			t.Errorf("%s: err = %v, want error %v", c.name, err, c.err)
		}
		if err != nil {
			if !strings.Contains(err.Error(), "NODE_EXTRA_CA_CERTS names "+otherCert+", which does not hold the ca_file certificate: unset it or add the certificate to it") {
				t.Errorf("%s: err = %q", c.name, err)
			}
			continue
		}
		slices.Sort(clear)
		if !slices.Equal(set, c.set) || !slices.Equal(clear, c.clear) {
			t.Errorf("%s: set = %q clear = %q, want %q %q", c.name, set, clear, c.set, c.clear)
		}
		if (note != "") != c.note {
			t.Errorf("%s: note = %q, want note %v", c.name, note, c.note)
		}
		if note != "" && !strings.Contains(note, otherCert) {
			t.Errorf("%s: note = %q does not name the file", c.name, note)
		}
	}
}

// TestCommandSetsNodeCAForAnHTTPSGateway proves the env assembler wires
// nodeCAEnv in: a claude launch over an https gateway route with a CA file
// has NODE_EXTRA_CA_CERTS once, equal to it, and a native launch inheriting
// wt's own marked value has neither name.
func TestCommandSetsNodeCAForAnHTTPSGateway(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	ca, _ := tlstest.Gen(t, t.TempDir(), tlstest.Opts{})
	t.Setenv("NODE_EXTRA_CA_CERTS", "")
	os.Unsetenv("NODE_EXTRA_CA_CERTS")
	gw := Gateway{Origin: "https://192.0.2.7:8787", Token: "wt-SENTINEL-tok", CAFile: ca}
	cmd, _, err := BuildLaunchCmdGateway("claude", gwCloudModel(), t.TempDir(), false, gw, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := envValues(cmd.Env, "NODE_EXTRA_CA_CERTS"); !slices.Equal(got, []string{ca}) {
		t.Errorf("NODE_EXTRA_CA_CERTS = %q, want [%s]", got, ca)
	}
	t.Setenv("NODE_EXTRA_CA_CERTS", "/x/ca.pem")
	t.Setenv("WT_NODE_EXTRA_CA_CERTS", "/x/ca.pem")
	cmd, _, err = BuildLaunchCmdGateway("claude", NativeModel(), t.TempDir(), false, gw, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := append(envValues(cmd.Env, "NODE_EXTRA_CA_CERTS"), envValues(cmd.Env, "WT_NODE_EXTRA_CA_CERTS")...); len(got) != 0 {
		t.Errorf("native launch kept %q", got)
	}
}
