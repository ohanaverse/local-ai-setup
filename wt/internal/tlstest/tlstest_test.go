package tlstest

import (
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestGenDefaults pins the zero Opts the TLS tests rely on: a leaf (not a
// CA) naming 127.0.0.1 and localhost, valid now, a 0600 key and a 0644
// certificate, both written under the directory given and nowhere else.
func TestGenDefaults(t *testing.T) {
	dir := t.TempDir()
	cert, key := Gen(t, dir, Opts{})
	for _, p := range []string{cert, key} {
		if filepath.Dir(p) != dir {
			t.Errorf("%s is not under %s", p, dir)
		}
	}
	if fi, err := os.Stat(key); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("key mode = %v, %v; want 0600", fi.Mode().Perm(), err)
	}
	if fi, err := os.Stat(cert); err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("cert mode = %v, %v; want 0644", fi.Mode().Perm(), err)
	}
	data, _ := os.ReadFile(cert)
	b, _ := pem.Decode(data)
	leaf, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if leaf.IsCA || len(leaf.IPAddresses) != 1 || !leaf.IPAddresses[0].Equal(net.IPv4(127, 0, 0, 1)) || len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != "localhost" {
		t.Errorf("leaf = CA %t IPs %v DNS %v", leaf.IsCA, leaf.IPAddresses, leaf.DNSNames)
	}
	if now := time.Now(); now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
		t.Errorf("leaf not valid now: %v..%v", leaf.NotBefore, leaf.NotAfter)
	}
	Pair(t, cert, key)
	Pool(t, cert)
	lines := BodyLines(t, cert, key)
	if len(lines) == 0 {
		t.Fatal("no body lines")
	}
	for _, l := range lines {
		if strings.HasPrefix(l, "-") {
			t.Errorf("body line %q is a header", l)
		}
	}
	cert2, key2 := Gen(t, dir, Opts{KeyMode: 0o644, DNS: []string{"wt.invalid"}})
	if cert2 == cert || key2 == key {
		t.Error("a second Gen in the same directory reused a file name")
	}
	if fi, _ := os.Stat(key2); fi.Mode().Perm() != 0o644 {
		t.Errorf("KeyMode not applied: %v", fi.Mode().Perm())
	}
}
