package modelsetup

import (
	"errors"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tlsfile"
)

// realTLSPair is Env.TLSPair on the real machine: serve's own check of the
// [gateway] tls_cert and tls_key pair (tlsfile.Load, the one loadServeTLS
// runs at start). It answers "" when no certificate is configured, when the
// [gateway] table cannot be read (modelmode's LocalTLS.Err already reports
// that), when the pair loads, and when the certificate is only expired or not
// yet valid (serve falls back to plain http for that). Any other failure is
// why serve would refuse to start (a loose key, a mismatched pair, a file
// that is not one), as tlsfile words it: a path and a sentence, never key or
// certificate bytes.
//
// It reads the private key, so only the admin side (Observe) calls it; the
// session side's ReadLocalTLS reads the public certificate alone.
func realTLSPair(now time.Time) string {
	gw, _, err := config.GatewaySettings()
	if err != nil || gw.TLSCert == "" {
		return ""
	}
	_, _, err = tlsfile.Load(gw.TLSCert, gw.TLSKey, now)
	if err == nil {
		return ""
	}
	var te *tlsfile.Error
	if errors.As(err, &te) && te.ClockOnly() {
		return ""
	}
	return err.Error()
}
