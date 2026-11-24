package modelmode

import (
	"errors"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tlsfile"
)

// LocalTLS is what this machine's wt-model home says about serve's TLS
// ([gateway] tls_cert and tls_key in its config.toml), judged by the same
// date rule serve applies at start (decision 13). It holds paths and fixed
// reasons only, never certificate or key bytes.
type LocalTLS struct {
	// Cert is tls_cert as configured (~ expanded); empty is plain http.
	Cert string
	// On is true when Cert is set and tlsfile.CheckDates is nil: serve
	// speaks TLS, so a local url is https://.
	On bool
	// Off is the fixed date reason when the certificate is expired or not
	// yet valid (tlsfile's DateReason): serve falls back to plain http.
	Off string
	// DialName is the name a loopback dial verifies the certificate as
	// (tlsfile.DialName); empty when the certificate cannot be read.
	DialName string
	// Err is why the settings cannot be used (a [gateway] key error, a home
	// that does not parse, a certificate that is not one): a local plan
	// refuses on it, never falling back to a silent http://.
	Err string
}

// ReadLocalTLS reads the wt-model home's [gateway] through
// config.GatewaySettings and judges tls_cert at now. It reads the public
// certificate only, never the key.
func ReadLocalTLS(now time.Time) LocalTLS {
	gw, _, err := config.GatewaySettings()
	if err != nil {
		return LocalTLS{Err: err.Error()}
	}
	if gw.TLSCert == "" {
		return LocalTLS{}
	}
	lt := LocalTLS{Cert: gw.TLSCert}
	if err := tlsfile.CheckDates(gw.TLSCert, now); err != nil {
		var te *tlsfile.Error
		if !errors.As(err, &te) || !te.ClockOnly() {
			lt.Err = "[gateway] tls_cert: " + err.Error()
			return lt
		}
		lt.Off = te.DateReason()
	} else {
		lt.On = true
	}
	if leaf, err := tlsfile.LeafOf(gw.TLSCert); err == nil {
		lt.DialName = tlsfile.DialName(leaf)
	}
	return lt
}

// LocalURL is the one rule for the url a session on this machine uses to
// reach the local server listening at listen: http://<listen> as before
// when TLS is off; https:// when it is on, with an unspecified listen host
// (0.0.0.0, ::) replaced by 127.0.0.1, since no certificate names 0.0.0.0.
// Setup's local plan, the Mode tab's local plan and Observe all call it.
func LocalURL(listen string, tlsOn bool) string {
	if !tlsOn {
		return "http://" + listen
	}
	if host, port, err := net.SplitHostPort(listen); err == nil {
		if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
			return "https://127.0.0.1:" + port
		}
	}
	return "https://" + listen
}

// SwapLoopbackScheme returns u with its scheme set to https (tlsOn) or http
// and true, when u is an http(s) url of the other scheme to a loopback host
// (127.0.0.1, ::1, localhost) with no path beyond "/", the shape setup
// writes; host and port are kept. Any other url (non-loopback, a path, a
// query, credentials, the right scheme already) is ("", false).
func SwapLoopbackScheme(u string, tlsOn bool) (string, bool) {
	from, to := "https", "http"
	if tlsOn {
		from, to = "http", "https"
	}
	p, err := url.Parse(u)
	if err != nil || p.Scheme != from || p.User != nil || p.RawQuery != "" || p.Fragment != "" || (p.Path != "" && p.Path != "/") {
		return "", false
	}
	host := p.Hostname()
	ip := net.ParseIP(host)
	if !strings.EqualFold(host, "localhost") && (ip == nil || !(ip.Equal(net.IPv4(127, 0, 0, 1)) || ip.Equal(net.IPv6loopback))) {
		return "", false
	}
	return to + "://" + p.Host, true
}

// LocalTLSRefusal is the refusal reason a local plan gives when the TLS
// settings cannot be read, or "" when they can.
func LocalTLSRefusal(st State) string {
	if st.LocalTLS.Err == "" {
		return ""
	}
	return Reason("wt-model's TLS settings cannot be used ("+st.LocalTLS.Err+")",
		"fix tls_cert and tls_key under [gateway] in "+gatewayHome()+", or remove both for plain http")
}

// gatewayHome names the wt-model home's config.toml for a message.
func gatewayHome() string { return config.ProxyHomeFile() }

// AddLocalTLSNotes adds a local plan's TLS notes: with TLS on and the
// session trusting no CA file, the exact line to add (wt never writes it);
// with TLS configured but off on the certificate's dates, why the url is
// http:// and how to renew.
func (p *Plan) AddLocalTLSNotes(st State) {
	lt := st.LocalTLS
	switch {
	case lt.On && st.CAFile == "":
		p.Notes = append(p.Notes, "TLS is on: local sessions must trust its certificate; add ca_file = \""+lt.Cert+
			"\" under [model-server] in "+st.Config.Path+" (wt does not write it; WT_MODEL_CA_FILE also works)")
	case lt.Off != "":
		p.Notes = append(p.Notes, "TLS is configured but off: "+lt.Off+" (tls_cert "+lt.Cert+
			"), so wt-model serve speaks plain http and the local url is http://; renew it: replace both the tls_cert and tls_key files, then restart serve")
	}
}
