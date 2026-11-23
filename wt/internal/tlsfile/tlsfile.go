// Package tlsfile loads the TLS certificate and key wt-model serve speaks
// with ([gateway] tls_cert and tls_key in the wt-model home's config.toml)
// and judges them: the key file's mode (the token file's rule), the PEM
// content, the pair matching and, last, the certificate's dates. Every
// failure is an *Error whose text is a fixed reason per Kind plus the path
// (and the OS reason for a stat or read failure, the date for a date kind):
// never the PEM parser's text, never a byte of either file. It imports only
// the standard library, so the session side can link it too.
package tlsfile

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

// Kind says which check a certificate or key failed.
type Kind int

const (
	KindStat        Kind = iota // a file could not be stat'ed (missing, unreachable)
	KindRead                    // a file could not be read
	KindNotRegular              // a path names a directory or another non-regular file
	KindLoose                   // group or other can access the key file
	KindBadPEM                  // no parseable PEM certificate (tls_cert) or private key (tls_key)
	KindKeyInCert               // the certificate file holds a private-key block
	KindSameFile                // tls_cert and tls_key name one file
	KindMismatch                // the key does not belong to the certificate
	KindExpired                 // the certificate's NotAfter has passed (clock only)
	KindNotYetValid             // the certificate's NotBefore is still ahead (clock only)
)

// Error is a certificate or key failure. Err keeps the OS error of a stat or
// read failure only (errors.Is can tell a missing file); a parse failure keeps
// nothing of the parser, so no text derived from file bytes can surface.
// Date is the certificate's NotAfter (KindExpired) or NotBefore
// (KindNotYetValid); Mode is the key file's mode (KindLoose).
type Error struct {
	Path string
	Kind Kind
	Err  error
	Date time.Time
	Mode os.FileMode
}

// Unwrap exposes the OS error of a stat or read failure.
func (e *Error) Unwrap() error { return e.Err }

// day is how a certificate date is written: the UTC calendar day.
func day(t time.Time) string { return t.UTC().Format("2006-01-02") }

// maxShownPath is the longest path a message prints; a longer one is never
// a real certificate path and may be pasted file content.
const maxShownPath = 200

// shownPath is the path as a message may print it: a fixed stand-in for one
// holding a control character (pasted PEM text spans lines) or longer than
// maxShownPath, so no message can carry file content given as a path.
func shownPath(p string) string {
	if len(p) > maxShownPath {
		return "<path too long>"
	}
	for _, r := range p {
		if unicode.IsControl(r) {
			return "<unprintable path>"
		}
	}
	return p
}

func (e *Error) Error() string {
	e = &Error{Path: shownPath(e.Path), Kind: e.Kind, Err: e.Err, Date: e.Date, Mode: e.Mode}
	switch e.Kind {
	case KindStat:
		return fmt.Sprintf("the file cannot be opened: %s (%s)", osReason(e.Err), e.Path)
	case KindRead:
		return fmt.Sprintf("the file cannot be read: %s (%s)", osReason(e.Err), e.Path)
	case KindNotRegular:
		return fmt.Sprintf("not a regular file (%s)", e.Path)
	case KindLoose:
		return fmt.Sprintf("TLS key %s is accessible by group or other (mode %04o); run chmod 600 %s", e.Path, e.Mode.Perm(), e.Path)
	case KindBadPEM:
		return fmt.Sprintf("not a PEM certificate (tls_cert) or private key (tls_key) that parses (%s)", e.Path)
	case KindKeyInCert:
		return fmt.Sprintf("the certificate file holds a private key; keep the key only in the tls_key file (%s)", e.Path)
	case KindSameFile:
		return fmt.Sprintf("tls_cert and tls_key name the same file; keep the certificate and the key in two files (%s)", e.Path)
	case KindMismatch:
		return fmt.Sprintf("the private key does not match the certificate (%s)", e.Path)
	case KindExpired:
		return fmt.Sprintf("the certificate expired on %s (%s)", day(e.Date), e.Path)
	case KindNotYetValid:
		return fmt.Sprintf("the certificate is not valid until %s (%s)", day(e.Date), e.Path)
	}
	return fmt.Sprintf("the TLS file cannot be used (%s)", e.Path)
}

// ClockOnly reports whether the failure is the certificate's dates alone:
// every configuration check passed (Load checks the dates last), so serve
// falls back to plain http instead of refusing to start.
func (e *Error) ClockOnly() bool { return e.Kind == KindExpired || e.Kind == KindNotYetValid }

// DateReason is the fixed reason a date failure gives (the ping's tls_off):
// "certificate expired <YYYY-MM-DD>" or "certificate not valid until
// <YYYY-MM-DD>"; empty for every other kind.
func (e *Error) DateReason() string {
	switch e.Kind {
	case KindExpired:
		return "certificate expired " + day(e.Date)
	case KindNotYetValid:
		return "certificate not valid until " + day(e.Date)
	}
	return ""
}

// osReason is an OS error without its operation and path (the message names
// the path itself).
func osReason(err error) string {
	if err == nil {
		return "unknown error"
	}
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	// Any other error may quote the path; only a fixed word is shown.
	return "operation failed"
}

// expand turns a leading ~ into the home directory and cleans the path, so
// two spellings of one file compare equal.
func expand(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			p = filepath.Join(home, strings.TrimPrefix(p[1:], "/"))
		}
	}
	return filepath.Clean(p)
}

// samePath reports whether two cleaned paths name one file: equal as
// absolute paths, or the same file on disk (a link or a hard link).
func samePath(a, b string, ai, bi os.FileInfo) bool {
	aa, errA := filepath.Abs(a)
	ba, errB := filepath.Abs(b)
	if errA == nil && errB == nil && aa == ba {
		return true
	}
	return os.SameFile(ai, bi)
}

// statRegular stats path (following links) and refuses a non-regular file.
func statRegular(path string) (os.FileInfo, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, &Error{Path: path, Kind: KindStat, Err: err}
	}
	if !fi.Mode().IsRegular() {
		return nil, &Error{Path: path, Kind: KindNotRegular}
	}
	return fi, nil
}

// HasPrivateKey reports whether pemBytes holds a private-key block of any
// kind (PKCS#8, EC, RSA, encrypted). It also matches a malformed block by its
// armor line, so a broken key is never taken for a certificate.
func HasPrivateKey(pemBytes []byte) bool {
	for rest := pemBytes; ; {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			break
		}
		if strings.Contains(b.Type, "PRIVATE KEY") {
			return true
		}
	}
	return bytes.Contains(pemBytes, []byte("PRIVATE KEY-----"))
}

// parseLeaf is the first CERTIFICATE block of data, parsed; nil when there
// is none or it does not parse.
func parseLeaf(data []byte) *x509.Certificate {
	for rest := data; ; {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			return nil
		}
		if b.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(b.Bytes)
		if err != nil {
			return nil
		}
		return c
	}
}

// keyParses reports whether data holds a private-key block that parses as
// PKCS#8, SEC 1 (EC) or PKCS#1 (RSA).
func keyParses(data []byte) bool {
	for rest := data; ; {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			return false
		}
		if !strings.HasSuffix(b.Type, "PRIVATE KEY") {
			continue
		}
		if _, err := x509.ParsePKCS8PrivateKey(b.Bytes); err == nil {
			return true
		}
		if _, err := x509.ParseECPrivateKey(b.Bytes); err == nil {
			return true
		}
		if _, err := x509.ParsePKCS1PrivateKey(b.Bytes); err == nil {
			return true
		}
		return false
	}
}

// LeafOf reads the public certificate at certPath (a leading ~ expanded) and
// returns its leaf. It refuses a file that is missing, not regular,
// unreadable, holds a private key or holds no parseable certificate; it does
// not look at the dates (CheckDates does).
func LeafOf(certPath string) (*x509.Certificate, error) {
	certPath = expand(certPath)
	if _, err := statRegular(certPath); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(certPath)
	if err != nil {
		return nil, &Error{Path: certPath, Kind: KindRead, Err: err}
	}
	if HasPrivateKey(data) {
		return nil, &Error{Path: certPath, Kind: KindKeyInCert}
	}
	leaf := parseLeaf(data)
	if leaf == nil {
		return nil, &Error{Path: certPath, Kind: KindBadPEM}
	}
	return leaf, nil
}

// dates is the validity check: KindNotYetValid before NotBefore, KindExpired
// after NotAfter, else nil.
func dates(leaf *x509.Certificate, certPath string, now time.Time) error {
	switch {
	case now.Before(leaf.NotBefore):
		return &Error{Path: certPath, Kind: KindNotYetValid, Date: leaf.NotBefore}
	case now.After(leaf.NotAfter):
		return &Error{Path: certPath, Kind: KindExpired, Date: leaf.NotAfter}
	}
	return nil
}

// CheckDates reads only the public certificate at certPath and judges it at
// now: nil when it is valid, a ClockOnly *Error when only its dates fail, or
// another *Error when the file itself cannot be used. Setup and the Mode tab
// count TLS as on only when it is nil (decision 13), the rule serve applies.
func CheckDates(certPath string, now time.Time) error {
	leaf, err := LeafOf(certPath)
	if err != nil {
		return err
	}
	return dates(leaf, expand(certPath), now)
}

// Load reads and judges the pair serve speaks with, at now. Both paths have a
// leading ~ expanded and are cleaned. The order is fixed, configuration
// first and the dates last, so a ClockOnly error means every other check
// passed: the key (stat, regular, no group or other bit), the certificate
// (stat, regular), the two not being one file, both readable, no key in the
// certificate file, a parseable certificate and key, the key matching the
// certificate, then the dates. With a ClockOnly error the pair and the leaf
// are returned beside it (the caller reports the dates and serves plain
// http); with any other error both are nil.
func Load(certPath, keyPath string, now time.Time) (*tls.Certificate, *x509.Certificate, error) {
	certPath, keyPath = expand(certPath), expand(keyPath)
	kfi, err := statRegular(keyPath)
	if err != nil {
		return nil, nil, err
	}
	if perm := kfi.Mode().Perm(); perm&0o077 != 0 {
		return nil, nil, &Error{Path: keyPath, Kind: KindLoose, Mode: perm}
	}
	cfi, err := statRegular(certPath)
	if err != nil {
		return nil, nil, err
	}
	if samePath(certPath, keyPath, cfi, kfi) {
		return nil, nil, &Error{Path: certPath, Kind: KindSameFile}
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, nil, &Error{Path: keyPath, Kind: KindRead, Err: err}
	}
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, nil, &Error{Path: certPath, Kind: KindRead, Err: err}
	}
	if HasPrivateKey(certPEM) {
		return nil, nil, &Error{Path: certPath, Kind: KindKeyInCert}
	}
	leaf := parseLeaf(certPEM)
	if leaf == nil {
		return nil, nil, &Error{Path: certPath, Kind: KindBadPEM}
	}
	if !keyParses(keyPEM) {
		return nil, nil, &Error{Path: keyPath, Kind: KindBadPEM}
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		// Both halves parsed on their own, so what is left is the pair:
		// the parser's text is never kept.
		return nil, nil, &Error{Path: keyPath, Kind: KindMismatch}
	}
	pair.Leaf = leaf
	if derr := dates(leaf, certPath, now); derr != nil {
		return &pair, leaf, derr
	}
	return &pair, leaf, nil
}

// cipherSuites are the TLS 1.2 suites serve offers: ECDHE with AES-GCM or
// ChaCha20-Poly1305 only (Go's default 1.2 list still has ECDHE-CBC-SHA).
// TLS 1.3 suites are not configurable and all AEAD. A fresh slice per call.
func cipherSuites() []uint16 {
	return []uint16{
		tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
		tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
		tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
		tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
		tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
		tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
	}
}

// Config is serve's TLS configuration for c: TLS 1.2 as the floor, the six
// AEAD suites above for 1.2, HTTP/2 left to net/http's defaults.
func Config(c *tls.Certificate) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{*c},
		MinVersion:   tls.VersionTLS12,
		CipherSuites: cipherSuites(),
	}
}

// DialName is the name a loopback dial verifies leaf as: 127.0.0.1 or
// localhost when the certificate names one (a host command dials loopback),
// else its first DNS name, else its first IP address; empty for nil.
func DialName(leaf *x509.Certificate) string {
	if leaf == nil {
		return ""
	}
	loop := net.IPv4(127, 0, 0, 1)
	for _, ip := range leaf.IPAddresses {
		if ip.Equal(loop) {
			return "127.0.0.1"
		}
	}
	for _, d := range leaf.DNSNames {
		if strings.EqualFold(d, "localhost") {
			return "localhost"
		}
	}
	if len(leaf.DNSNames) > 0 {
		return leaf.DNSNames[0]
	}
	if len(leaf.IPAddresses) > 0 {
		return leaf.IPAddresses[0].String()
	}
	return ""
}
