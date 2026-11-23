package tlsfile

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/tlstest"
)

// kindOf is the *Error's Kind, failing the test when err is not one.
func kindOf(t *testing.T, err error) Kind {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("err = %T %v, want *tlsfile.Error", err, err)
	}
	return e.Kind
}

// combined writes cert then key into one file at mode and returns its path.
func combined(t *testing.T, dir, cert, key string, mode os.FileMode) string {
	t.Helper()
	c, _ := os.ReadFile(cert)
	k, _ := os.ReadFile(key)
	p := filepath.Join(dir, "combined.pem")
	if err := os.WriteFile(p, append(c, k...), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestLoadAcceptsA0600Key pins the happy path: a matching pair with a 0600
// key loads, the leaf is parsed and the pair carries it.
func TestLoadAcceptsA0600Key(t *testing.T) {
	cert, key := tlstest.Gen(t, t.TempDir(), tlstest.Opts{})
	pair, leaf, err := Load(cert, key, time.Now())
	if err != nil {
		t.Fatalf("Load = %v", err)
	}
	if pair == nil || leaf == nil || len(pair.Certificate) == 0 {
		t.Fatalf("Load = %v, %v; want the pair and its leaf", pair, leaf)
	}
}

// TestLoadRefusesALooseKey pins the token file's rule for the TLS key: any
// group or other bit refuses, in tokenfile's wording (the path and chmod 600),
// before the key is read.
func TestLoadRefusesALooseKey(t *testing.T) {
	for _, mode := range []os.FileMode{0o644, 0o640} {
		cert, key := tlstest.Gen(t, t.TempDir(), tlstest.Opts{KeyMode: mode})
		_, _, err := Load(cert, key, time.Now())
		if err == nil || kindOf(t, err) != KindLoose {
			t.Fatalf("mode %o: Load = %v, want KindLoose", mode, err)
		}
		msg := err.Error()
		if !strings.Contains(msg, "chmod 600 "+key) || !strings.Contains(msg, key) || !strings.HasPrefix(msg, "TLS key ") {
			t.Errorf("mode %o: message %q, want tokenfile's wording naming %s and chmod 600", mode, msg, key)
		}
	}
}

// TestLoadRefusesBadFiles pins every configuration refusal before the dates:
// a missing file, a directory as the key, a non-PEM file, a certificate file
// that also holds the key, one combined file named as both (also through a
// ~ or ./ spelling) and two halves of different pairs.
func TestLoadRefusesBadFiles(t *testing.T) {
	dir := t.TempDir()
	cert, key := tlstest.Gen(t, dir, tlstest.Opts{})
	cert2, _ := tlstest.Gen(t, dir, tlstest.Opts{})
	junk := filepath.Join(dir, "junk.pem")
	if err := os.WriteFile(junk, []byte("not a pem file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	both := combined(t, dir, cert, key, 0o600)
	certWithKey := combined(t, t.TempDir(), cert, key, 0o644)
	home := t.TempDir()
	t.Setenv("HOME", home)
	homeBoth := combined(t, home, cert, key, 0o600)

	cases := []struct {
		name       string
		cert, key  string
		want       Kind
		chdirToDir string
	}{
		{"missing cert", filepath.Join(dir, "nope.crt"), key, KindStat, ""},
		{"missing key", cert, filepath.Join(dir, "nope.key"), KindStat, ""},
		{"directory as key", cert, t.TempDir(), KindNotRegular, ""},
		{"non-PEM cert", junk, key, KindBadPEM, ""},
		{"non-PEM key", cert, junk, KindBadPEM, ""},
		{"key in cert", certWithKey, key, KindKeyInCert, ""},
		{"same file", both, both, KindSameFile, ""},
		{"same file via ~", "~/combined.pem", homeBoth, KindSameFile, ""},
		{"same file via ./", "./combined.pem", both, KindSameFile, dir},
		{"mismatch", cert2, key, KindMismatch, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.chdirToDir != "" {
				t.Chdir(c.chdirToDir)
			}
			_, _, err := Load(c.cert, c.key, time.Now())
			if err == nil {
				t.Fatal("Load = nil, want a refusal")
			}
			if got := kindOf(t, err); got != c.want {
				t.Errorf("kind = %v (%v), want %v", got, err, c.want)
			}
		})
	}
}

// TestLoadDatesAreClockOnlyAndChecksLast pins owner confirmation 3's split:
// an expired or not-yet-valid pair is a clock event (ClockOnly, with the
// date reason), while the same dates on a pair that is also misconfigured
// report the configuration kind, since the dates are checked last.
// CheckDates gives the same answer from the certificate alone.
func TestLoadDatesAreClockOnlyAndChecksLast(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	expCert, expKey := tlstest.Gen(t, dir, tlstest.Opts{NotBefore: now.AddDate(0, -2, 0), NotAfter: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)})
	futCert, futKey := tlstest.Gen(t, dir, tlstest.Opts{NotBefore: time.Date(2026, 12, 5, 0, 0, 0, 0, time.UTC), NotAfter: now.AddDate(1, 0, 0)})
	for _, c := range []struct {
		cert, key, reason string
		kind              Kind
	}{
		{expCert, expKey, "certificate expired 2026-11-01", KindExpired},
		{futCert, futKey, "certificate not valid until 2026-12-05", KindNotYetValid},
	} {
		pair, leaf, err := Load(c.cert, c.key, now)
		var e *Error
		if !errors.As(err, &e) || e.Kind != c.kind || !e.ClockOnly() || e.DateReason() != c.reason {
			t.Fatalf("Load = %v, want %v ClockOnly with reason %q", err, c.kind, c.reason)
		}
		if pair == nil || leaf == nil {
			t.Errorf("a clock-only error returned no pair or leaf (%v, %v)", pair, leaf)
		}
		cerr := CheckDates(c.cert, now)
		var ce *Error
		if !errors.As(cerr, &ce) || ce.Kind != c.kind || !ce.ClockOnly() || ce.DateReason() != c.reason {
			t.Errorf("CheckDates = %v, want the same %v", cerr, c.kind)
		}
	}
	if err := CheckDates(expCert, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Errorf("CheckDates inside the window = %v, want nil", err)
	}
	// An expired pair that is also mismatched, or has a loose key, reports
	// the configuration kind.
	if _, _, err := Load(expCert, futKey, now); kindOf(t, err) != KindMismatch {
		t.Errorf("expired + mismatched = %v, want KindMismatch", err)
	}
	if err := os.Chmod(expKey, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(expCert, expKey, now); kindOf(t, err) != KindLoose {
		t.Errorf("expired + loose = %v, want KindLoose", err)
	}
	for _, k := range []Kind{KindStat, KindRead, KindNotRegular, KindLoose, KindBadPEM, KindKeyInCert, KindSameFile, KindMismatch} {
		e := &Error{Kind: k, Path: "/x"}
		if e.ClockOnly() || e.DateReason() != "" {
			t.Errorf("kind %v: ClockOnly %t reason %q, want false and empty", k, e.ClockOnly(), e.DateReason())
		}
	}
	// CheckDates on a non-certificate is a configuration error.
	junk := filepath.Join(dir, "junk")
	_ = os.WriteFile(junk, []byte("x"), 0o644)
	if err := CheckDates(junk, now); err == nil || kindOf(t, err) != KindBadPEM {
		t.Errorf("CheckDates(junk) = %v, want KindBadPEM", err)
	}
}

// TestErrorsNeverCarryKeyBytes pins Review Focus 3: no refusal, of any kind,
// quotes a line of the key's or the certificate's PEM body; only paths, a
// fixed reason and the OS error reach the message.
func TestErrorsNeverCarryKeyBytes(t *testing.T) {
	now := time.Now()
	dir := t.TempDir()
	cert, key := tlstest.Gen(t, dir, tlstest.Opts{})
	cert2, key2 := tlstest.Gen(t, dir, tlstest.Opts{})
	expCert, expKey := tlstest.Gen(t, dir, tlstest.Opts{NotBefore: now.Add(-48 * time.Hour), NotAfter: now.Add(-24 * time.Hour)})
	futCert, futKey := tlstest.Gen(t, dir, tlstest.Opts{NotBefore: now.Add(24 * time.Hour), NotAfter: now.Add(48 * time.Hour)})
	looseCert, looseKey := tlstest.Gen(t, dir, tlstest.Opts{KeyMode: 0o644})
	both := combined(t, dir, cert, key, 0o600)
	certWithKey := combined(t, t.TempDir(), cert, key, 0o644)
	unreadable := filepath.Join(dir, "unreadable.key")
	_ = os.WriteFile(unreadable, []byte("x"), 0o000)
	body := tlstest.BodyLines(t, cert, key, cert2, key2, expCert, expKey, futCert, futKey, looseCert, looseKey)

	errs := map[string]error{}
	add := func(name, c, k string) {
		_, _, err := Load(c, k, now)
		errs[name] = err
	}
	add("stat", filepath.Join(dir, "missing"), key)
	add("notregular", cert, t.TempDir())
	add("loose", looseCert, looseKey)
	junk := filepath.Join(dir, "junk.pem")
	_ = os.WriteFile(junk, []byte("not pem\n"), 0o600)
	add("badpem", cert, junk)
	add("keyincert", certWithKey, key)
	add("samefile", both, both)
	add("mismatch", cert2, key)
	add("expired", expCert, expKey)
	add("notyetvalid", futCert, futKey)
	if os.Geteuid() != 0 {
		add("read", cert, unreadable)
	}
	errs["checkdates"] = CheckDates(certWithKey, now)
	seen := map[Kind]bool{}
	for name, err := range errs {
		if err == nil {
			t.Errorf("%s: no error", name)
			continue
		}
		seen[kindOf(t, err)] = true
		for _, line := range body {
			if strings.Contains(err.Error(), line) {
				t.Errorf("%s: error %q holds a PEM body line", name, err)
			}
		}
	}
	for _, k := range []Kind{KindStat, KindNotRegular, KindLoose, KindBadPEM, KindKeyInCert, KindSameFile, KindMismatch, KindExpired, KindNotYetValid} {
		if !seen[k] {
			t.Errorf("no case produced kind %v", k)
		}
	}
}

// TestConfigFloorIsTLS12 pins the protocol choice: TLS 1.2 is the floor and
// the 1.2 suites are set explicitly, non-empty and all AEAD (Go's default
// 1.2 list still carries ECDHE-CBC-SHA).
func TestConfigFloorIsTLS12(t *testing.T) {
	cert, key := tlstest.Gen(t, t.TempDir(), tlstest.Opts{})
	pair, _, err := Load(cert, key, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	c := Config(pair)
	if c.MinVersion != tls.VersionTLS12 {
		t.Errorf("MinVersion = %x, want TLS 1.2", c.MinVersion)
	}
	if len(c.CipherSuites) == 0 {
		t.Fatal("CipherSuites is empty: Go's default 1.2 list would apply")
	}
	for _, s := range c.CipherSuites {
		name := tls.CipherSuiteName(s)
		if strings.Contains(name, "CBC") || !strings.Contains(name, "ECDHE") {
			t.Errorf("suite %s is not an ECDHE AEAD suite", name)
		}
	}
	if len(c.Certificates) != 1 {
		t.Errorf("Certificates = %d, want the one pair", len(c.Certificates))
	}
	if c.InsecureSkipVerify {
		t.Error("InsecureSkipVerify set")
	}
}

// TestDialName pins the name a loopback dial verifies the certificate as:
// 127.0.0.1, then localhost, then the first DNS name, then the first IP.
func TestDialName(t *testing.T) {
	for _, c := range []struct {
		dns  []string
		ips  []net.IP
		want string
	}{
		{[]string{"localhost", "a.example"}, []net.IP{net.ParseIP("192.0.2.7"), net.ParseIP("127.0.0.1")}, "127.0.0.1"},
		{[]string{"a.example", "localhost"}, []net.IP{net.ParseIP("192.0.2.7")}, "localhost"},
		{[]string{"a.example", "b.example"}, []net.IP{net.ParseIP("192.0.2.7")}, "a.example"},
		{nil, []net.IP{net.ParseIP("192.0.2.7"), net.ParseIP("192.0.2.8")}, "192.0.2.7"},
	} {
		leaf := &x509.Certificate{DNSNames: c.dns, IPAddresses: c.ips}
		if got := DialName(leaf); got != c.want {
			t.Errorf("DialName(%v, %v) = %q, want %q", c.dns, c.ips, got, c.want)
		}
	}
	if DialName(nil) != "" {
		t.Error("DialName(nil) is not empty")
	}
}

// TestHasPrivateKey pins the key-block scan reused for ca_file (Task 6b):
// any *PRIVATE KEY block is found, a certificate alone is not one.
func TestHasPrivateKey(t *testing.T) {
	cert, key := tlstest.Gen(t, t.TempDir(), tlstest.Opts{})
	c, _ := os.ReadFile(cert)
	k, _ := os.ReadFile(key)
	if HasPrivateKey(c) {
		t.Error("a certificate reads as holding a key")
	}
	if !HasPrivateKey(k) || !HasPrivateKey(append(c, k...)) {
		t.Error("a key block was not found")
	}
	if !HasPrivateKey([]byte("-----BEGIN EC PRIVATE KEY-----\nbroken\n-----END EC PRIVATE KEY-----\n")) {
		t.Error("a malformed EC key block was not found")
	}
}
