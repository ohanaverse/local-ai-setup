package modelclient

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/tokenfile"
)

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func tokenFileWith(t *testing.T, name, body string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestSessionTokenFileErrorsNameTheConfigKey proves a session-side token file
// failure points at token_file under [model-server], never at the server's
// --token-file flag or its umask recipe, for every failure kind.
func TestSessionTokenFileErrorsNameTheConfigKey(t *testing.T) {
	dir := t.TempDir()
	loose := tokenFileWith(t, "loose", "x", 0o644)
	empty := tokenFileWith(t, "empty", "\n", 0o600)
	for name, p := range map[string]string{"loose": loose, "empty": empty, "missing": filepath.Join(dir, "nope")} {
		_, err := tokenfile.Load(p)
		got := SessionTokenError(err).Error()
		if !strings.Contains(got, "token_file under [model-server]") {
			t.Errorf("%s: %q lacks the config key", name, got)
		}
		if strings.Contains(got, "--token-file") || strings.Contains(got, "umask") {
			t.Errorf("%s: %q carries the server's way out", name, got)
		}
		if name == "loose" && !strings.Contains(got, "chmod 600 "+loose) {
			t.Errorf("loose: %q lacks the chmod sentence", got)
		}
	}
}

// TestResolveEndpointPrecedence proves the environment beats config, a token
// file beats a literal token, and a token file goes through tokenfile.Load.
func TestResolveEndpointPrecedence(t *testing.T) {
	good := tokenFileWith(t, "good", "filetok\n", 0o600)
	other := tokenFileWith(t, "other", "othertok\n", 0o600)

	ep, err := ResolveEndpoint("http://cfg:1/", good, envOf(nil))
	if err != nil || ep.URL != "http://cfg:1/" || ep.Token != "filetok" {
		t.Fatalf("config only = %+v, %v", ep, err)
	}
	if ep.Origin() != "http://cfg:1" {
		t.Errorf("Origin = %q", ep.Origin())
	}
	o, tok := ep.Gateway()
	if o != "http://cfg:1" || tok != "filetok" {
		t.Errorf("Gateway = %q, %q", o, tok)
	}

	ep, err = ResolveEndpoint("http://cfg:1", good, envOf(map[string]string{"WT_MODEL_URL": "http://env:2", "WT_MODEL_TOKEN_FILE": other}))
	if err != nil || ep.URL != "http://env:2" || ep.Token != "othertok" {
		t.Fatalf("env override = %+v, %v", ep, err)
	}

	ep, err = ResolveEndpoint("", "", envOf(map[string]string{"WT_MODEL_URL": "http://env:2", "WT_MODEL_TOKEN": "literal"}))
	if err != nil || ep.Token != "literal" {
		t.Fatalf("literal = %+v, %v", ep, err)
	}

	ep, err = ResolveEndpoint("", "", envOf(map[string]string{"WT_MODEL_URL": "http://env:2", "WT_MODEL_TOKEN": "literal", "WT_MODEL_TOKEN_FILE": other}))
	if err != nil || ep.Token != "othertok" {
		t.Fatalf("file beats literal = %+v, %v", ep, err)
	}

	ep, err = ResolveEndpoint("", good, envOf(map[string]string{"WT_MODEL_URL": "http://env:2", "WT_MODEL_TOKEN": "literal"}))
	if err != nil || ep.Token != "literal" {
		t.Fatalf("literal beats config token_file = %+v, %v", ep, err)
	}

	loose := tokenFileWith(t, "loose", "x", 0o644)
	_, err = ResolveEndpoint("http://cfg:1", loose, envOf(nil))
	var te *tokenfile.Error
	if !errors.As(err, &te) || te.Kind != tokenfile.KindLoose {
		t.Errorf("loose file err = %v", err)
	}
}

// TestEnvTokenFileErrorsNameTheEnvVar proves a failing path that came from
// WT_MODEL_TOKEN_FILE names that variable, not the config key, while a
// config-sourced path keeps the config key; and that an unreadable path (a
// directory) is a KindRead error.
func TestEnvTokenFileErrorsNameTheEnvVar(t *testing.T) {
	loose := tokenFileWith(t, "loose", "x", 0o644)
	_, err := ResolveEndpoint("http://cfg:1", "", envOf(map[string]string{"WT_MODEL_TOKEN_FILE": loose}))
	if err == nil || !strings.Contains(err.Error(), "WT_MODEL_TOKEN_FILE") || strings.Contains(err.Error(), "token_file under [model-server]") {
		t.Errorf("env-sourced err = %v", err)
	}
	_, err = ResolveEndpoint("http://cfg:1", loose, envOf(nil))
	if err == nil || !strings.Contains(err.Error(), "token_file under [model-server]") || strings.Contains(err.Error(), "WT_MODEL_TOKEN_FILE") {
		t.Errorf("config-sourced err = %v", err)
	}
	// A 0700 directory passes the stat and mode checks, then reading fails.
	dir := filepath.Join(t.TempDir(), "d")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err = ResolveEndpoint("http://cfg:1", dir, envOf(nil))
	var te *tokenfile.Error
	if !errors.As(err, &te) || te.Kind != tokenfile.KindRead {
		t.Errorf("directory err = %v, want KindRead", err)
	}
}

// TestZeroNotConfiguredErrorIsNotATokenError proves the zero value, with
// neither flag set, does not claim the token is missing.
func TestZeroNotConfiguredErrorIsNotATokenError(t *testing.T) {
	got := (&NotConfiguredError{}).Error()
	if !strings.HasPrefix(got, "no model server is configured") || strings.Contains(got, "token is not configured") {
		t.Errorf("zero value = %q", got)
	}
}

// TestNotConfiguredMessages pins the three exact refusal texts and that each
// names both settings and wt-model serve.
func TestNotConfiguredMessages(t *testing.T) {
	tail := " — set url and token_file under [model-server] in config.toml (or WT_MODEL_URL and WT_MODEL_TOKEN_FILE); start a server yourself with 'wt-model serve --listen ADDR --token-file PATH', because wt never starts one"
	cases := []struct {
		e    NotConfiguredError
		want string
	}{
		{NotConfiguredError{MissingURL: true, MissingToken: true}, "no model server is configured; [litellm] is no longer used by wt" + tail},
		{NotConfiguredError{MissingURL: true}, "the model server url is not configured" + tail},
		{NotConfiguredError{MissingToken: true}, "the model server token is not configured" + tail},
	}
	for _, c := range cases {
		if got := c.e.Error(); got != c.want {
			t.Errorf("got  %q\nwant %q", got, c.want)
		}
	}
	_, err := ResolveEndpoint("", "", envOf(nil))
	var nc *NotConfiguredError
	if !errors.As(err, &nc) || !nc.MissingURL || !nc.MissingToken {
		t.Errorf("empty resolve = %v", err)
	}
	_, err = ResolveEndpoint("http://x", "", envOf(nil))
	if !errors.As(err, &nc) || nc.MissingURL || !nc.MissingToken {
		t.Errorf("no token resolve = %v", err)
	}
}

// TestStillKeepsTheCause proves Still appends the outcome and the original
// error stays findable with errors.As.
func TestStillKeepsTheCause(t *testing.T) {
	err := Still(&NotConfiguredError{MissingURL: true}, "nothing was launched")
	if !strings.HasSuffix(err.Error(), "; nothing was launched") {
		t.Errorf("text = %q", err)
	}
	var nc *NotConfiguredError
	if !errors.As(err, &nc) {
		t.Error("errors.As lost *NotConfiguredError")
	}
}
