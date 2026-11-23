package modelclient

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tlstest"
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

	ep, err := ResolveEndpoint("http://cfg:1/", good, "", envOf(nil))
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

	ep, err = ResolveEndpoint("http://cfg:1", good, "", envOf(map[string]string{"WT_MODEL_URL": "http://env:2", "WT_MODEL_TOKEN_FILE": other}))
	if err != nil || ep.URL != "http://env:2" || ep.Token != "othertok" {
		t.Fatalf("env override = %+v, %v", ep, err)
	}

	ep, err = ResolveEndpoint("", "", "", envOf(map[string]string{"WT_MODEL_URL": "http://env:2", "WT_MODEL_TOKEN": "literal"}))
	if err != nil || ep.Token != "literal" {
		t.Fatalf("literal = %+v, %v", ep, err)
	}

	ep, err = ResolveEndpoint("", "", "", envOf(map[string]string{"WT_MODEL_URL": "http://env:2", "WT_MODEL_TOKEN": "literal", "WT_MODEL_TOKEN_FILE": other}))
	if err != nil || ep.Token != "othertok" {
		t.Fatalf("file beats literal = %+v, %v", ep, err)
	}

	ep, err = ResolveEndpoint("", good, "", envOf(map[string]string{"WT_MODEL_URL": "http://env:2", "WT_MODEL_TOKEN": "literal"}))
	if err != nil || ep.Token != "literal" {
		t.Fatalf("literal beats config token_file = %+v, %v", ep, err)
	}

	loose := tokenFileWith(t, "loose", "x", 0o644)
	_, err = ResolveEndpoint("http://cfg:1", loose, "", envOf(nil))
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
	_, err := ResolveEndpoint("http://cfg:1", "", "", envOf(map[string]string{"WT_MODEL_TOKEN_FILE": loose}))
	if err == nil || !strings.Contains(err.Error(), "WT_MODEL_TOKEN_FILE") || strings.Contains(err.Error(), "token_file under [model-server]") {
		t.Errorf("env-sourced err = %v", err)
	}
	_, err = ResolveEndpoint("http://cfg:1", loose, "", envOf(nil))
	if err == nil || !strings.Contains(err.Error(), "token_file under [model-server]") || strings.Contains(err.Error(), "WT_MODEL_TOKEN_FILE") {
		t.Errorf("config-sourced err = %v", err)
	}
	// A 0700 directory passes the stat and mode checks, then reading fails.
	dir := filepath.Join(t.TempDir(), "d")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err = ResolveEndpoint("http://cfg:1", dir, "", envOf(nil))
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
// names both settings and wt config.
func TestNotConfiguredMessages(t *testing.T) {
	tail := " — set url and token_file under [model-server] in config.toml (or WT_MODEL_URL and WT_MODEL_TOKEN_FILE), or choose the mode in wt config (Mode tab): native, local or remote"
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
	_, err := ResolveEndpoint("", "", "", envOf(nil))
	var nc *NotConfiguredError
	if !errors.As(err, &nc) || !nc.MissingURL || !nc.MissingToken {
		t.Errorf("empty resolve = %v", err)
	}
	_, err = ResolveEndpoint("http://x", "", "", envOf(nil))
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

// TestNoServerConfigured pins the one condition a native launch hangs on:
// only an endpoint with BOTH the url and the token missing (even wrapped by
// Still) is "no server configured". One missing is a half-configured server,
// and any other error is not about configuration at all; both must keep
// refusing, or a typo in config.toml would silently launch without the server.
func TestNoServerConfigured(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"both missing", &NotConfiguredError{MissingURL: true, MissingToken: true}, true},
		{"both missing, wrapped", Still(&NotConfiguredError{MissingURL: true, MissingToken: true}, "nothing was launched"), true},
		{"url only missing", &NotConfiguredError{MissingURL: true}, false},
		{"token only missing", Still(&NotConfiguredError{MissingToken: true}, "x"), false},
		{"another error", errors.New("boom"), false},
		{"nil", nil, false},
	} {
		if got := NoServerConfigured(tc.err); got != tc.want {
			t.Errorf("%s: NoServerConfigured = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestNotConfiguredErrorLeadsAreUnchangedAndTheTailNamesWtConfig pins the
// stage 3 rewrite of NotConfiguredError: its three leads are byte-identical
// (llmbench's wt_bridge hint and several wt tests key on them), and only the
// tail changed: it names wt config and the three modes, and no longer says
// "wt never starts one", which is false for the local mode.
func TestNotConfiguredErrorLeadsAreUnchangedAndTheTailNamesWtConfig(t *testing.T) {
	for _, c := range []struct {
		e    NotConfiguredError
		lead string
	}{
		{NotConfiguredError{MissingURL: true, MissingToken: true}, "no model server is configured; [litellm] is no longer used by wt — "},
		{NotConfiguredError{MissingURL: true}, "the model server url is not configured — "},
		{NotConfiguredError{MissingToken: true}, "the model server token is not configured — "},
	} {
		got := c.e.Error()
		if !strings.HasPrefix(got, c.lead) {
			t.Errorf("%q does not begin with the unchanged lead %q", got, c.lead)
		}
		for _, want := range []string{"wt config", "native, local or remote", "[model-server]", "WT_MODEL_URL"} {
			if !strings.Contains(got, want) {
				t.Errorf("%q does not name %q", got, want)
			}
		}
		if strings.Contains(got, "wt never starts one") || strings.Contains(got, "\n") {
			t.Errorf("%q still says wt never starts a server, or is not one line", got)
		}
	}
}

// writeCA writes data to a new file under dir and returns its path.
func writeCA(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// caCertPEM is a self-signed CA certificate (basicConstraints CA:TRUE) with
// no name constraints, generated in memory: the shape that can vouch for any
// host and earns ResolveEndpoint's one note.
func caCertPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true, IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// TestHTTPSEndpointAccepted proves an https url resolves exactly as written:
// TLS needs no change to the url rules.
func TestHTTPSEndpointAccepted(t *testing.T) {
	ep, err := ResolveEndpoint("https://192.0.2.7:8787", "", "", envOf(map[string]string{"WT_MODEL_TOKEN": "t"}))
	if err != nil || ep.URL != "https://192.0.2.7:8787" || ep.Token != "t" || ep.CAFile != "" {
		t.Fatalf("ep = %+v, err = %v", ep, err)
	}
}

// TestCAFileEnvWinsAndExpandsHome proves decision 14's path rule: a config
// ca_file's leading ~ is expanded against HOME, WT_MODEL_CA_FILE wins over
// the config, and a relative path is made absolute, so the agent (which runs
// in the worktree) reads the same file.
func TestCAFileEnvWinsAndExpandsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	certPath, _ := tlstest.Gen(t, home, tlstest.Opts{})
	pemBytes, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	writeCA(t, home, "ca.pem", pemBytes)
	env := map[string]string{"WT_MODEL_TOKEN": "t"}
	ep, err := ResolveEndpoint("https://h:1", "", "~/ca.pem", envOf(env))
	if err != nil || ep.CAFile != filepath.Join(home, "ca.pem") {
		t.Fatalf("~ expansion: ep.CAFile = %q, err = %v", ep.CAFile, err)
	}
	if len(ep.Notes) != 0 {
		t.Errorf("a non-CA leaf got notes: %q", ep.Notes)
	}
	cwd := t.TempDir()
	writeCA(t, cwd, "env.pem", pemBytes)
	t.Chdir(cwd)
	env["WT_MODEL_CA_FILE"] = "env.pem"
	ep, err = ResolveEndpoint("https://h:1", "", "~/ca.pem", envOf(env))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.Abs("env.pem")
	if ep.CAFile != want || !filepath.IsAbs(ep.CAFile) {
		t.Errorf("env: ep.CAFile = %q, want %q (absolute, the env's file)", ep.CAFile, want)
	}
}

// TestCAFileWithoutPEMRefused proves a ca_file wt cannot trust is refused
// when the endpoint resolves (exit 1 before any request): one holding no PEM
// certificate, and one holding a private-key block beside a certificate. The
// message names the path and never the file's bytes. A CA certificate
// without name constraints resolves, with one note.
func TestCAFileWithoutPEMRefused(t *testing.T) {
	dir := t.TempDir()
	env := envOf(map[string]string{"WT_MODEL_TOKEN": "t"})
	certPath, keyPath := tlstest.Gen(t, dir, tlstest.Opts{})
	cert, _ := os.ReadFile(certPath)
	key, _ := os.ReadFile(keyPath)
	cases := map[string]struct {
		data []byte
		want string
	}{
		"no pem":      {[]byte("SECRETBYTES not a certificate\n"), "holds no PEM certificate"},
		"key in file": {append(append([]byte{}, cert...), key...), "holds a private key: copy only the certificate"},
	}
	for name, c := range cases {
		p := writeCA(t, dir, strings.ReplaceAll(name, " ", "-")+".pem", c.data)
		_, err := ResolveEndpoint("https://h:1", "", p, env)
		if err == nil {
			t.Fatalf("%s: resolved", name)
		}
		msg := err.Error()
		if !strings.Contains(msg, "ca_file "+p+" "+c.want) {
			t.Errorf("%s: err = %q, want the path and %q", name, msg, c.want)
		}
		if strings.Contains(msg, "SECRETBYTES") || strings.Contains(msg, "BEGIN") {
			t.Errorf("%s: err holds file bytes: %q", name, msg)
		}
		if exitcode.Of(err) != 1 {
			t.Errorf("%s: exit = %d, want 1", name, exitcode.Of(err))
		}
	}
	ca := writeCA(t, dir, "ca.pem", caCertPEM(t))
	ep, err := ResolveEndpoint("https://h:1", "", ca, env)
	if err != nil {
		t.Fatal(err)
	}
	if len(ep.Notes) != 1 || !strings.Contains(ep.Notes[0], "without name constraints") || !strings.Contains(ep.Notes[0], ca) {
		t.Errorf("notes = %q, want one naming the CA without name constraints", ep.Notes)
	}
}

// TestCAFileIgnoredForHTTPNote proves a ca_file beside an http url is not
// used (nor read): the endpoint carries no CAFile and one note says so.
func TestCAFileIgnoredForHTTPNote(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.pem")
	ep, err := ResolveEndpoint("http://127.0.0.1:1", "", missing, envOf(map[string]string{"WT_MODEL_TOKEN": "t"}))
	if err != nil {
		t.Fatal(err)
	}
	if ep.CAFile != "" {
		t.Errorf("CAFile = %q for an http url", ep.CAFile)
	}
	if len(ep.Notes) != 1 || ep.Notes[0] != "ca_file is ignored for an http url" {
		t.Errorf("notes = %q", ep.Notes)
	}
}
