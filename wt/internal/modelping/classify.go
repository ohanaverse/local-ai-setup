package modelping

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
)

// Kind says how a ping failed: the names and the order modelsetup's observe
// state has used since the model server tab existed.
type Kind int

const (
	KindNone Kind = iota
	KindUnreachable
	KindUnauthorized
	KindNotWtModel
	KindSkew
	KindSchema
)

// Outcome is one classified ping failure: its Kind, the error's text whole
// (Message, empty for a nil error), and the server's api major or schema when
// a skew or schema error says what it speaks.
type Outcome struct {
	Kind    Kind
	Message string
	API     int
	Schema  int
}

// Classify maps a ping error onto an Outcome: nil is the zero Outcome; a
// typed error is its Kind, through any wrap (errors.As); an error of no known
// type reads as unreachable, since a ping only fails at the transport (a
// *TLSTrustError, which Send returns for the TLS failures, too: its Message
// is the hint). Its
// Message is err.Error() whole, so a caller that shows the message never has
// to re-derive it from the type.
func Classify(err error) Outcome {
	if err == nil {
		return Outcome{}
	}
	o := Outcome{Message: err.Error()}
	var ua *UnauthorizedError
	var ns *NotModelServerError
	var sk *SkewError
	var sc *SchemaError
	switch {
	case errors.As(err, &ua):
		o.Kind = KindUnauthorized
	case errors.As(err, &ns):
		o.Kind = KindNotWtModel
	case errors.As(err, &sk):
		o.Kind, o.API = KindSkew, sk.Server
	case errors.As(err, &sc):
		o.Kind, o.Schema = KindSchema, sc.Server
	default:
		o.Kind = KindUnreachable
	}
	return o
}

// Checked pings ep and hands back the classification beside a failure: the
// ping, KindNone and "" when it answers cleanly; otherwise the error as it
// came, its Kind and Message. The error is returned unchanged so a caller
// can still errors.As it.
func Checked(ctx context.Context, ep Endpoint) (modelapi.Ping, Kind, string, error) {
	p, err := Ping(ctx, ep)
	if err == nil {
		return p, KindNone, "", nil
	}
	o := Classify(err)
	return p, o.Kind, o.Message, err
}

// Fixed hint texts (decision 14). They name config keys and the address,
// never a token.
const (
	hintUnknownAuthority = "the model server's certificate is not trusted: set ca_file under [model-server] in ~/.config/agent-wt/config.toml (or WT_MODEL_CA_FILE) to the server's certificate"
	hintPlainServer      = "the model server speaks plain http, not https: the handshake failed before anything was sent; use http:// in [model-server] url only for a loopback server (if the server's TLS certificate expired, wt-model serve falls back to plain http: renew it on the host and restart serve)"
	// httpsServerBody is how Go's TLS server answers a plain-http request.
	httpsServerBody = "Client sent an HTTP request to an HTTPS server"
)

// tlsTrustError maps a transport error of a request to ep onto its
// *TLSTrustError, or nil when it is no TLS failure: an untrusted authority,
// a certificate not naming the host, one outside its dates, and an https
// request answered by a plain-http server. Each fails during the handshake,
// before the request (and its bearer token) is written.
func tlsTrustError(ep Endpoint, err error) *TLSTrustError {
	addr := Addr(ep.URL)
	var ua x509.UnknownAuthorityError
	var he x509.HostnameError
	var ci x509.CertificateInvalidError
	var rh tls.RecordHeaderError
	hint := ""
	switch {
	case errors.As(err, &he):
		hint = fmt.Sprintf("the certificate does not name %s: regenerate it with a subjectAltName for that host (an IP needs an IP SAN)", Clean(he.Host))
	case errors.As(err, &ci) && ci.Reason == x509.Expired:
		hint = "the model server's certificate is outside its validity dates by this machine's clock"
		if c := ci.Cert; c != nil {
			if now := time.Now(); now.After(c.NotAfter) {
				hint = "the model server's certificate expired on " + c.NotAfter.UTC().Format("2006-01-02")
			} else if now.Before(c.NotBefore) {
				hint = "the model server's certificate is not valid until " + c.NotBefore.UTC().Format("2006-01-02")
			}
		}
		hint += ": check both machines' clocks, or renew it on the host (replace tls_cert and tls_key, restart serve)"
	case errors.As(err, &ua):
		hint = hintUnknownAuthority
		if ep.CAFile != "" {
			hint = "the model server's certificate is not trusted by ca_file " + shownPath(ep.CAFile) + ": set ca_file under [model-server] in ~/.config/agent-wt/config.toml (or WT_MODEL_CA_FILE) to the server's certificate"
		}
	case errors.As(err, &rh) || strings.Contains(err.Error(), "server gave HTTP response to HTTPS client"):
		hint = hintPlainServer
	default:
		var cv *tls.CertificateVerificationError
		if !errors.As(err, &cv) {
			return nil
		}
		hint = hintUnknownAuthority
	}
	return &TLSTrustError{Addr: addr, Hint: hint, Err: err}
}

// httpToTLSError is the *TLSTrustError of an http request answered by a TLS
// server (Go's 400 with httpsServerBody), or nil for any other answer. The
// whole request, its bearer token included, already went in clear.
func httpToTLSError(ep Endpoint, r *Response) *TLSTrustError {
	if r.Status != 400 || !strings.HasPrefix(string(r.Body), httpsServerBody) {
		return nil
	}
	if u, err := url.Parse(ep.Origin()); err != nil || !strings.EqualFold(u.Scheme, "http") {
		return nil
	}
	addr := Addr(ep.URL)
	return &TLSTrustError{Addr: addr, Hint: httpToTLSHint(addr)}
}

// httpToTLSHint says the scheme to use and that the token was already sent
// unencrypted to addr; for a host that is not loopback, to rotate it.
func httpToTLSHint(addr string) string {
	a := Clean(addr)
	h := "the model server speaks https: use https://" + a + " in [model-server] url; this request already sent the token unencrypted to " + a
	if !loopbackAddr(addr) {
		h += "; rotate the token on the host (wt-model setup)"
	}
	return h
}

// loopbackAddr reports whether host:port names a loopback host (an IP in
// 127.0.0.0/8 or ::1, or the name localhost).
func loopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if strings.EqualFold(strings.TrimSuffix(host, "."), "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.Unmap().IsLoopback()
}
