// Package tlstest makes TLS certificates for tests: an ECDSA P-256 key and a
// self-signed leaf generated in memory, written only under the directory the
// test hands in (a t.TempDir()). No test reads a real certificate path.
// It imports only the standard library and testing, like internal/gittest.
package tlstest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"testing"
	"time"
)

// Opts shapes one generated certificate. The zero value names 127.0.0.1 and
// localhost, is valid from an hour ago for a day, and writes the key 0600.
// DNS and IPs replace the default names only when either is set.
type Opts struct {
	DNS       []string
	IPs       []net.IP
	NotBefore time.Time
	NotAfter  time.Time
	KeyMode   os.FileMode
}

// Gen writes a fresh certificate (mode 0644) and its private key (o.KeyMode,
// 0600 by default) under dir and returns their paths. Each call makes a new
// key pair and new file names, so two calls in one directory never collide.
// The leaf is not a CA (basicConstraints CA:FALSE, server auth), the shape
// the docs recommend; Go trusts it when it is itself in the root pool.
func Gen(t testing.TB, dir string, o Opts) (certPath, keyPath string) {
	t.Helper()
	now := time.Now()
	if o.NotBefore.IsZero() {
		o.NotBefore = now.Add(-time.Hour)
	}
	if o.NotAfter.IsZero() {
		o.NotAfter = now.Add(24 * time.Hour)
	}
	if o.KeyMode == 0 {
		o.KeyMode = 0o600
	}
	if o.DNS == nil && o.IPs == nil {
		o.DNS = []string{"localhost"}
		o.IPs = []net.IP{net.IPv4(127, 0, 0, 1)}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("tlstest: generate key: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		t.Fatalf("tlstest: serial: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "wt-model test"},
		NotBefore:             o.NotBefore,
		NotAfter:              o.NotAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  false,
		DNSNames:              o.DNS,
		IPAddresses:           o.IPs,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("tlstest: create certificate: %v", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("tlstest: marshal key: %v", err)
	}
	certPath = writeNew(t, dir, "server-*.crt", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
	keyPath = writeNew(t, dir, "server-*.key", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), o.KeyMode)
	return certPath, keyPath
}

// writeNew writes data to a new file under dir named after pattern and forces
// its mode (umask must not decide what a test sees).
func writeNew(t testing.TB, dir, pattern string, data []byte, mode os.FileMode) string {
	t.Helper()
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		t.Fatalf("tlstest: %v", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		t.Fatalf("tlstest: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("tlstest: %v", err)
	}
	if err := os.Chmod(f.Name(), mode); err != nil {
		t.Fatalf("tlstest: %v", err)
	}
	return f.Name()
}

// Pair loads a generated certificate and key as a tls.Certificate, for an
// httptest server's TLS config.
func Pair(t testing.TB, certPath, keyPath string) tls.Certificate {
	t.Helper()
	c, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatalf("tlstest: load pair: %v", err)
	}
	return c
}

// Pool is a certificate pool holding only the certificate at certPath, for a
// test client that must trust exactly that server.
func Pool(t testing.TB, certPath string) *x509.CertPool {
	t.Helper()
	data, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("tlstest: %v", err)
	}
	p := x509.NewCertPool()
	if !p.AppendCertsFromPEM(data) {
		t.Fatalf("tlstest: %s holds no certificate", certPath)
	}
	return p
}

// BodyLines is every base64 body line of the PEM files at paths (headers
// and blank lines left out), so a test can check that no output holds key or
// certificate bytes.
func BodyLines(t testing.TB, paths ...string) []string {
	t.Helper()
	var out []string
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("tlstest: %v", err)
		}
		for rest := data; ; {
			var b *pem.Block
			b, rest = pem.Decode(rest)
			if b == nil {
				break
			}
			enc := pem.EncodeToMemory(&pem.Block{Type: b.Type, Bytes: b.Bytes})
			for _, line := range splitLines(string(enc)) {
				if line != "" && line[0] != '-' {
					out = append(out, line)
				}
			}
		}
	}
	return out
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}
