package modelmode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tlstest"
)

// TestLocalURL pins the one local-url rule setup's local plan, the Mode tab's
// local plan and Observe share: http://<listen> as today with TLS off;
// https:// with TLS on, an unspecified listen host replaced by 127.0.0.1
// (no certificate names 0.0.0.0 or ::).
func TestLocalURL(t *testing.T) {
	for _, c := range []struct {
		listen string
		tlsOn  bool
		want   string
	}{
		{"127.0.0.1:8787", false, "http://127.0.0.1:8787"},
		{"127.0.0.1:8787", true, "https://127.0.0.1:8787"},
		{"0.0.0.0:8787", false, "http://0.0.0.0:8787"},
		{"0.0.0.0:8787", true, "https://127.0.0.1:8787"},
		{"[::]:8787", false, "http://[::]:8787"},
		{"[::]:8787", true, "https://127.0.0.1:8787"},
		{"localhost:8787", true, "https://localhost:8787"},
		{"[::1]:8787", true, "https://[::1]:8787"},
	} {
		if got := LocalURL(c.listen, c.tlsOn); got != c.want {
			t.Errorf("LocalURL(%q, %t) = %q, want %q", c.listen, c.tlsOn, got, c.want)
		}
	}
}

// scratchGateway points the config home at a scratch directory whose
// wt-model home config.toml holds body (nothing when empty).
func scratchGateway(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if body == "" {
		return
	}
	p := filepath.Join(dir, "wt-model", "config.toml")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestModeLocalWritesHTTPSWhenTLSConfigured pins decision 13 for the Mode
// tab: with the wt-model home's [gateway] naming a valid tls_cert, the local
// pointer plan (from a stored local table and from a fresh machine) and
// Observe's derived local url are https://, never http://; with no
// [gateway] both stay http:// as today; with an expired tls_cert they are
// http:// plus one note naming the date. A [gateway] that cannot be read
// refuses the local plan.
func TestModeLocalWritesHTTPSWhenTLSConfigured(t *testing.T) {
	cert, key := tlstest.Gen(t, t.TempDir(), tlstest.Opts{})
	now := time.Now()
	expCert, expKey := tlstest.Gen(t, t.TempDir(), tlstest.Opts{NotBefore: now.AddDate(0, -2, 0), NotAfter: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)})
	gw := func(c, k string) string { return "[gateway]\ntls_cert = \"" + c + "\"\ntls_key = \"" + k + "\"\n" }

	observeLocal := func(t *testing.T) State {
		t.Helper()
		f := newFakeEnv(t)
		f.configFile = config.ModelServerFile{Path: plCfgPath, Exists: true, Table: &config.ModelServerState{Mode: "local", TokenFile: plLocalTok}}
		f.files[plLocalTok] = []byte("tok\n")
		f.modes[plLocalTok] = 0o600
		st := Observe(context.Background(), f.Env(), ObserveOptions{Local: LocalFallback{Listen: "0.0.0.0:8787"}})
		return st
	}
	plans := func(t *testing.T, st State) []Plan {
		t.Helper()
		stored := localState()
		stored.LocalTLS = st.LocalTLS
		stored.CAFile = st.CAFile
		fresh := freshState()
		fresh.LocalTLS = st.LocalTLS
		fresh.CAFile = st.CAFile
		return []Plan{mustPlan(t, stored, Desired{Mode: ModeLocal}), mustPlan(t, fresh, Desired{Mode: ModeLocal})}
	}

	t.Run("tls on", func(t *testing.T) {
		scratchGateway(t, gw(cert, key))
		st := observeLocal(t)
		if !st.LocalTLS.On || st.LocalTLS.Cert != cert || st.LocalTLS.DialName != "127.0.0.1" {
			t.Fatalf("LocalTLS = %+v, want on for %s", st.LocalTLS, cert)
		}
		if st.Server.URL != "https://127.0.0.1:8787" {
			t.Errorf("Observe's derived url = %q, want https://127.0.0.1:8787", st.Server.URL)
		}
		for _, p := range plans(t, st) {
			if p.Want.URL != "https://127.0.0.1:8787" || strings.Contains(p.Format(), "http://") {
				t.Errorf("local plan url = %q:\n%s", p.Want.URL, p.Format())
			}
			n := 0
			for _, note := range p.Notes {
				if strings.Contains(note, `ca_file = "`+cert+`"`) {
					n++
				}
			}
			if n != 1 {
				t.Errorf("notes = %q, want one naming ca_file = %q", p.Notes, cert)
			}
		}
	})
	t.Run("no gateway", func(t *testing.T) {
		scratchGateway(t, "")
		st := observeLocal(t)
		if st.LocalTLS != (LocalTLS{}) || st.Server.URL != "http://0.0.0.0:8787" {
			t.Errorf("LocalTLS = %+v, url %q; want plain http as today", st.LocalTLS, st.Server.URL)
		}
		for _, p := range plans(t, st) {
			if p.Want.URL != "http://127.0.0.1:8787" {
				t.Errorf("local plan url = %q, want http://127.0.0.1:8787", p.Want.URL)
			}
			for _, note := range p.Notes {
				if strings.Contains(note, "TLS") || strings.Contains(note, "ca_file") {
					t.Errorf("plain http plan has a TLS note: %q", note)
				}
			}
		}
	})
	t.Run("expired", func(t *testing.T) {
		scratchGateway(t, gw(expCert, expKey))
		st := observeLocal(t)
		if st.LocalTLS.On || st.LocalTLS.Off != "certificate expired 2026-11-01" {
			t.Fatalf("LocalTLS = %+v, want off with the date reason", st.LocalTLS)
		}
		if st.Server.URL != "http://0.0.0.0:8787" {
			t.Errorf("Observe's derived url = %q, want http:// (serve's fallback)", st.Server.URL)
		}
		for _, p := range plans(t, st) {
			if p.Want.URL != "http://127.0.0.1:8787" {
				t.Errorf("local plan url = %q, want http://", p.Want.URL)
			}
			n := 0
			for _, note := range p.Notes {
				if strings.Contains(note, "certificate expired 2026-11-01") {
					n++
				}
			}
			if n != 1 {
				t.Errorf("notes = %q, want one naming the date", p.Notes)
			}
		}
	})
	t.Run("half set", func(t *testing.T) {
		scratchGateway(t, "[gateway]\ntls_cert = \""+cert+"\"\n")
		st := observeLocal(t)
		if st.LocalTLS.Err == "" || st.Server.Checked {
			t.Errorf("LocalTLS = %+v, checked %t; want an error and no ping", st.LocalTLS, st.Server.Checked)
		}
		fresh := freshState()
		fresh.LocalTLS = st.LocalTLS
		p, err := BuildPlan(fresh, Desired{Mode: ModeLocal})
		mustRefuse(t, p, err, "must be set together")
	})
}

// TestModeLocalSwapsOnlyTheSchemeOfALoopbackURL pins review finding I3 and
// the Mode tab half of I1: a stored local url on loopback (127.0.0.1, ::1,
// localhost) of the opposite scheme is setup's, whatever its port, so the
// plan swaps only the scheme (host and port kept) and notes that serve, which
// reads its TLS settings only at start, must be restarted on the host; a
// non-loopback or hand-written url (a path) is kept as written, and a url
// already of the right scheme gets no restart note.
func TestModeLocalSwapsOnlyTheSchemeOfALoopbackURL(t *testing.T) {
	on := LocalTLS{Cert: "/etc/wt/server.crt", On: true, DialName: "127.0.0.1"}
	off := LocalTLS{Cert: "/etc/wt/server.crt", Off: "certificate expired 2026-11-01", DialName: "127.0.0.1"}
	for _, c := range []struct {
		stored  string
		tls     LocalTLS
		want    string
		restart bool
	}{
		{"http://127.0.0.1:9911", on, "https://127.0.0.1:9911", true},
		{"http://localhost:9911", on, "https://localhost:9911", true},
		{"http://[::1]:9911", on, "https://[::1]:9911", true},
		{"https://127.0.0.1:9911", off, "http://127.0.0.1:9911", true},
		{"https://127.0.0.1:9911", on, "https://127.0.0.1:9911", false},
		{"http://127.0.0.1:9911", LocalTLS{}, "http://127.0.0.1:9911", false},
		{"http://gpu.example:8787", on, "http://gpu.example:8787", false},
		{"http://127.0.0.1:9911/custom", on, "http://127.0.0.1:9911/custom", false},
	} {
		st := localState()
		st.Config.Table.URL = c.stored
		st.LocalTLS = c.tls
		st.CAFile = "/etc/wt/ca.pem"
		p := mustPlan(t, st, Desired{Mode: ModeLocal})
		if p.Want.URL != c.want {
			t.Errorf("stored %q (TLS on %t): url = %q, want %q", c.stored, c.tls.On, p.Want.URL, c.want)
		}
		n := 0
		for _, note := range p.Notes {
			if strings.Contains(note, "restart") && strings.Contains(note, "wt-model setup") {
				n++
			}
		}
		if (n == 1) != c.restart || n > 1 {
			t.Errorf("stored %q (TLS on %t): %d restart notes, want restart=%t: %q", c.stored, c.tls.On, n, c.restart, p.Notes)
		}
	}
}

// TestObserveWithoutAWtModelHomeCreatesNothing pins promoted minor 1: the
// Mode tab's Observe on a machine with no wt-model home (an empty config
// home) reads its TLS settings without creating the wt-model directory or a
// lock file in it.
func TestObserveWithoutAWtModelHomeCreatesNothing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	f := newFakeEnv(t)
	st := Observe(context.Background(), f.Env(), ObserveOptions{})
	if st.LocalTLS != (LocalTLS{}) {
		t.Errorf("LocalTLS = %+v, want plain", st.LocalTLS)
	}
	if _, err := os.Stat(filepath.Join(dir, "wt-model")); !os.IsNotExist(err) {
		t.Errorf("Observe created the wt-model home (%v)", err)
	}
}
