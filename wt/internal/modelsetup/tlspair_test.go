package modelsetup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelmode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tlstest"
)

// writeGateway points the scratch wt-model home's [gateway] at a certificate
// and a key.
func writeGateway(t *testing.T, cert, key string) {
	t.Helper()
	home := config.ProxyHomePath()
	if err := os.MkdirAll(filepath.Dir(home), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "[gateway]\ntls_cert = \"" + cert + "\"\ntls_key = \"" + key + "\"\n"
	if err := os.WriteFile(home, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestRealTLSPairJudgesTheKeyPairAsServeDoes pins #698: setup used to look at
// the certificate's dates alone, so a loose key file or a key that does not
// belong to the certificate was found only when serve refused to start (and
// launchd's KeepAlive looped on it). realTLSPair runs serve's own check
// (tlsfile.Load) and keeps its fixed reason. A certificate that is merely
// expired is not a failure: serve falls back to plain http for that.
func TestRealTLSPairJudgesTheKeyPairAsServeDoes(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	now := time.Now()

	if got := realTLSPair(now); got != "" {
		t.Fatalf("no [gateway]: %q, want nothing to report", got)
	}
	cert, key := tlstest.Gen(t, t.TempDir(), tlstest.Opts{})
	writeGateway(t, cert, key)
	if got := realTLSPair(now); got != "" {
		t.Errorf("a good pair: %q, want nothing to report", got)
	}

	loose, looseKey := tlstest.Gen(t, t.TempDir(), tlstest.Opts{KeyMode: 0o644})
	writeGateway(t, loose, looseKey)
	if got := realTLSPair(now); !strings.Contains(got, "accessible by group or other") || !strings.Contains(got, "chmod 600") {
		t.Errorf("a loose key: %q, want serve's own reason with the fix", got)
	}

	_, otherKey := tlstest.Gen(t, t.TempDir(), tlstest.Opts{})
	writeGateway(t, cert, otherKey)
	if got := realTLSPair(now); !strings.Contains(got, "does not match the certificate") {
		t.Errorf("a mismatched pair: %q, want the mismatch", got)
	}

	writeGateway(t, cert, filepath.Join(t.TempDir(), "missing.key"))
	if got := realTLSPair(now); got == "" {
		t.Error("a missing key file reported nothing")
	}

	expired, expiredKey := tlstest.Gen(t, t.TempDir(), tlstest.Opts{NotBefore: now.Add(-48 * time.Hour), NotAfter: now.Add(-24 * time.Hour)})
	writeGateway(t, expired, expiredKey)
	if got := realTLSPair(now); got != "" {
		t.Errorf("an expired certificate with a good key: %q, want nothing (serve falls back to plain http)", got)
	}
}

// TestRealTLSPairNeverShowsKeyBytes pins that the reason holds a path and a
// fixed sentence, never the contents of either file.
func TestRealTLSPairNeverShowsKeyBytes(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	cert, _ := tlstest.Gen(t, t.TempDir(), tlstest.Opts{})
	garbage := filepath.Join(t.TempDir(), "k.key")
	if err := os.WriteFile(garbage, []byte("-----BEGIN PRIVATE KEY-----\nSECRETBYTES\n-----END PRIVATE KEY-----\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeGateway(t, cert, garbage)
	got := realTLSPair(time.Now())
	if got == "" || strings.Contains(got, "SECRETBYTES") || strings.Contains(got, "BEGIN") {
		t.Errorf("reason = %q, want a path-and-sentence reason with no key bytes", got)
	}
}

// TestObserveReadsTheTLSPairOnlyWhenTLSIsConfigured pins that the check runs
// when the home names a certificate the settings can use, with the host's
// clock, and not otherwise: no tls_cert, or a [gateway] that already cannot be
// used (LocalTLS.Err owns that report), asks nothing.
func TestObserveReadsTheTLSPairOnlyWhenTLSIsConfigured(t *testing.T) {
	var asked []time.Time
	env := func(lt *modelmode.LocalTLS) Env {
		f := obsConfigured(t)
		f.localTLS = lt
		e := f.Env()
		e.TLSPair = func(now time.Time) string {
			asked = append(asked, now)
			return "TLS key /k is accessible by group or other (mode 0644); run chmod 600 /k"
		}
		return e
	}
	st := Observe(context.Background(), env(&modelmode.LocalTLS{Cert: tlsCertPath, On: true}), ObserveOptions{})
	if len(asked) != 1 || !strings.Contains(st.TLSPair, "chmod 600") {
		t.Errorf("TLS configured: asked %d times, TLSPair = %q", len(asked), st.TLSPair)
	}
	asked = nil
	for name, lt := range map[string]*modelmode.LocalTLS{
		"no tls_cert":         {},
		"an unusable gateway": {Cert: tlsCertPath, Err: "[gateway] tls_cert: not a PEM"},
	} {
		st := Observe(context.Background(), env(lt), ObserveOptions{})
		if len(asked) != 0 || st.TLSPair != "" {
			t.Errorf("%s: asked %d times, TLSPair = %q, want none", name, len(asked), st.TLSPair)
		}
	}
}

// TestSetupRefusesWhileTheKeyPairIsBad pins that a local plan, which would
// start or restart serve, refuses while serve's own check of the pair fails,
// naming the reason and the way out, and is unchanged once it passes.
func TestSetupRefusesWhileTheKeyPairIsBad(t *testing.T) {
	st := baseState()
	st.LocalTLS = modelmode.LocalTLS{Cert: tlsCertPath, On: true, DialName: "127.0.0.1"}
	want := Desired{Desired: modelmode.Desired{Mode: modelclient.ModeLocal}}

	st.TLSPair = "TLS key /k is accessible by group or other (mode 0644); run chmod 600 /k"
	p, err := BuildPlan(st, want)
	mustRefuse(t, p, err, "serve would refuse to start", "accessible by group or other", "tls_cert and tls_key")

	st.TLSPair = ""
	if _, err := BuildPlan(st, want); err != nil {
		t.Errorf("a good pair was refused: %v", err)
	}
}
