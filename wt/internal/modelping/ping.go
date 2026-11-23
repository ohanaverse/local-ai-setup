package modelping

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/urlrule"
)

const (
	// ServerName is the identity every ping answer must carry: anything else
	// is not a wt-model, whatever else it says.
	ServerName = "wt-model"
	// DefaultTimeout is the per-request timeout of one control call.
	DefaultTimeout = 10 * time.Second
	// DialTimeout bounds the TCP dial of one control request.
	DialTimeout = 5 * time.Second
	// MaxResponse is the read limit for one answer: an answer at or over it
	// is refused by name (*ResponseTooLargeError) rather than truncated into
	// a decode error that would misread it as not a wt-model server.
	MaxResponse = 32 << 20
)

// NewHTTPClient is the transport every control request rides on: no
// environment proxy, a bounded dial, no redirect followed and TLS 1.2 as the
// floor. It is NewHTTPClientFor with no CA file, which cannot fail.
func NewHTTPClient() *http.Client {
	hc, _ := NewHTTPClientFor(Endpoint{})
	return hc
}

// CAFileError is a CA file wt cannot use: unreadable, holding no PEM
// certificate, or holding a private key. It names the path (a stand-in for
// one that cannot be a real path, so pasted file content is never echoed)
// and never the file's bytes.
type CAFileError struct {
	Path, Reason string
}

func (e *CAFileError) Error() string { return "ca_file " + shownPath(e.Path) + " " + e.Reason }

// CAKeyReason is the reason a ca_file holding a private-key block is refused.
const CAKeyReason = "holds a private key: copy only the certificate"

// maxShownPath is the longest path a message prints; a longer one is never a
// real file path and may be pasted file content.
const maxShownPath = 200

// shownPath is p as a message may print it: a fixed stand-in for one holding
// a control character (pasted PEM text spans lines) or longer than
// maxShownPath.
func shownPath(p string) string {
	if len(p) > maxShownPath {
		return "<path too long>"
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return "<unprintable path>"
		}
	}
	return p
}

// NewHTTPClientFor is NewHTTPClient's transport for ep: when ep.CAFile is
// set, its certificates are added to the system roots (a file that cannot be
// read or holds no PEM certificate is a *CAFileError, before any dial: never
// a fall back to the system pool alone; nor is one holding a private key);
// ep.ServerName, when set, is the name
// the certificate is verified as. Verification is never skipped.
func NewHTTPClientFor(ep Endpoint) (*http.Client, error) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	// No environment proxy: the bearer token and the prompts that pass
	// through the gateway never go to a third party's HTTP(S)_PROXY.
	tr.Proxy = nil
	tr.DialContext = (&net.Dialer{Timeout: DialTimeout}).DialContext
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: ep.ServerName}
	if !ep.VerifyAt.IsZero() {
		at := ep.VerifyAt
		cfg.Time = func() time.Time { return at }
	}
	if ep.CAFile != "" {
		data, err := os.ReadFile(ep.CAFile)
		if err != nil {
			reason := err.Error()
			var pe *fs.PathError
			if errors.As(err, &pe) {
				reason = pe.Err.Error()
			}
			return nil, &CAFileError{Path: ep.CAFile, Reason: "cannot be read: " + reason}
		}
		if bytes.Contains(data, []byte("PRIVATE KEY-----")) {
			return nil, &CAFileError{Path: ep.CAFile, Reason: CAKeyReason}
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(data) {
			return nil, &CAFileError{Path: ep.CAFile, Reason: "holds no PEM certificate"}
		}
		cfg.RootCAs = pool
	}
	tr.TLSClientConfig = cfg
	// A 3xx is never followed: it would replay the bearer token and the
	// request body to whatever the Location names.
	return &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

// Response is the answer a request came back with, whatever its status.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// Send performs one request under the per-request timeout and returns the
// response whatever its status; only transport failures are errors. A url
// holding an '@' is refused before any request is built (ResolveEndpoint
// refuses it first; this is the backstop for an Endpoint built directly), and
// a url with no host, or one net/http cannot build a request from, fails with
// fixed text: neither quotes the url (url.Parse's own error quotes it whole,
// and the transport's names a schemeless url's first word as its scheme).
// A cancelled context returns the context's own error, never disguised as
// the server being down.
func Send(ctx context.Context, hc *http.Client, timeout time.Duration, ep Endpoint, method, rawurl string, body any) (*Response, error) {
	switch Addr(ep.URL) {
	case urlrule.Hidden:
		return nil, &CredentialsInURLError{}
	case UnparsedAddr:
		return nil, &UnreachableError{Addr: UnparsedAddr, Reason: UnbuildableRequest}
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	bound := timeout // the bound that will expire first, for the error text
	if dl, ok := ctx.Deadline(); ok && time.Until(dl) < bound {
		bound = time.Until(dl)
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, method, rawurl, rd)
	if err != nil {
		return nil, &UnreachableError{Addr: UnparsedAddr, Reason: UnbuildableRequest}
	}
	req.Header.Set("Authorization", "Bearer "+ep.Token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := hc.Do(req)
	var data []byte
	if err == nil {
		// One byte past the limit, so an answer at or over it is named
		// rather than silently truncated into a decode error that would
		// misread it as not a wt-model server.
		data, err = io.ReadAll(io.LimitReader(resp.Body, MaxResponse+1))
		resp.Body.Close()
	}
	if err == nil && len(data) > MaxResponse {
		return nil, &ResponseTooLargeError{Addr: Addr(ep.URL), Limit: MaxResponse}
	}
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil, ctx.Err()
		}
		if rctx.Err() == nil {
			if te := tlsTrustError(ep, err); te != nil {
				return nil, te
			}
		}
		reason := ""
		if rctx.Err() != nil {
			reason = "timed out after " + bound.Round(10*time.Millisecond).String()
		} else {
			var ue *url.Error
			if errors.As(err, &ue) {
				err = ue.Err
			}
			reason = err.Error()
		}
		return nil, &UnreachableError{Addr: Addr(ep.URL), Reason: reason}
	}
	out := &Response{Status: resp.StatusCode, Header: resp.Header, Body: data}
	if te := httpToTLSError(ep, out); te != nil {
		return nil, te
	}
	return out, nil
}

// CheckPing reads one ping response: the decoded Ping beside the typed error
// for an answer that is not a clean wt-model ping (401, any non-200, an
// unparsable or misidentifying body, an API major or schema this wt does not
// speak). A clean answer returns the Ping and nil.
func CheckPing(addr string, r *Response) (modelapi.Ping, error) {
	if r.Status == http.StatusUnauthorized {
		return modelapi.Ping{}, &UnauthorizedError{Addr: addr}
	}
	var p modelapi.Ping
	if r.Status != http.StatusOK {
		return p, &NotModelServerError{Addr: addr, Why: "GET /control/v1/ping answered HTTP " + strconv.Itoa(r.Status)}
	}
	if err := json.Unmarshal(r.Body, &p); err != nil {
		return p, &NotModelServerError{Addr: addr, Why: "its ping answer is not JSON"}
	}
	if p.Server != ServerName {
		return p, &NotModelServerError{Addr: addr, Why: fmt.Sprintf("it identifies as %q", p.Server)}
	}
	if p.APIMajor != modelapi.APIMajor {
		return p, &SkewError{Addr: addr, Server: p.APIMajor, Client: modelapi.APIMajor}
	}
	if p.Schema != modelapi.SchemaVersion {
		return p, &SchemaError{Addr: addr, Server: p.Schema, Client: modelapi.SchemaVersion}
	}
	return p, nil
}

// Ping checks that a server answers, is a wt-model and speaks this API major:
// one GET to its ping endpoint over a fresh transport.
func Ping(ctx context.Context, ep Endpoint) (modelapi.Ping, error) {
	hc, err := NewHTTPClientFor(ep)
	if err != nil {
		return modelapi.Ping{}, err
	}
	defer hc.CloseIdleConnections()
	return pingOn(ctx, hc, ep)
}

// pingOn is Ping over a transport the caller owns, so GetJSON's handshake and
// its GET share one connection.
func pingOn(ctx context.Context, hc *http.Client, ep Endpoint) (modelapi.Ping, error) {
	r, err := Send(ctx, hc, DefaultTimeout, ep, "GET", ep.Origin()+"/control/v1/ping", nil)
	if err != nil {
		return modelapi.Ping{}, err
	}
	return CheckPing(Addr(ep.URL), r)
}

// GetJSON handshakes with the server (its ping error returned as is), then
// reads one JSON document from path into out (nil for none). A 401, a
// non-2xx (a redirect included, which Send never follows) and an undecodable
// body each raise their typed error with nothing of the body leaked into it.
// First caller: the phase 4 stage 2 admin commands; it lives here because
// the spine puts every request the model side shares in this package.
func GetJSON(ctx context.Context, ep Endpoint, path string, out any) error {
	hc, err := NewHTTPClientFor(ep)
	if err != nil {
		return err
	}
	defer hc.CloseIdleConnections()
	if _, err := pingOn(ctx, hc, ep); err != nil {
		return err
	}
	addr := Addr(ep.URL)
	r, err := Send(ctx, hc, DefaultTimeout, ep, "GET", ep.Origin()+path, nil)
	if err != nil {
		return err
	}
	switch {
	case r.Status == http.StatusUnauthorized:
		return &UnauthorizedError{Addr: addr}
	case r.Status >= 300 && r.Status < 400:
		return &NotModelServerError{Addr: addr, Why: "it answered HTTP " + strconv.Itoa(r.Status) + ", a redirect, which wt does not follow"}
	case r.Status < 200 || r.Status > 299:
		// A wt-model's own error answer says what the server said (#649);
		// a body that is not an APIError still classifies NotModelServerError.
		if ae, ok := DecodeAPIError(r.Body); ok {
			return ae.Err()
		}
		return &NotModelServerError{Addr: addr, Why: "GET " + path + " answered HTTP " + strconv.Itoa(r.Status)}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(r.Body, out); err != nil {
		return &NotModelServerError{Addr: addr, Why: "its answer to GET " + path + " is not the expected JSON"}
	}
	return nil
}
