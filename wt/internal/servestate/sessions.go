package servestate

import (
	"context"
	"errors"
	"fmt"
	"net"
	"syscall"

	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelping"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tlsfile"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tokenfile"
)

// Sessions asks the server r names for its catalog and returns the model ids
// that have at least one client session, mapped to that count (a non-nil,
// empty map when none has). The token is read from r.TokenFile (its
// *tokenfile.Error returned as is, before any request); the address dialled
// is r.Listen, on loopback when it is bound to every interface. A record with
// TLSCert is dialled over https, trusting exactly that certificate file and
// verifying it as the name a loopback dial uses (tlsfile.DialName) at the
// record's start time (serve's own validation); a record
// without it is dialled over http, so a TLS server it does not describe is an
// error, never an empty map. A stale record is the caller's to tolerate: it
// is an unreachable server, an error.
// A record whose pid is no longer running is stale (the server died without
// removing it, and another process may own the port now), so it is an error
// before the token is read or any request is made.
// ctx bounds the whole exchange. No error carries the token.
func Sessions(ctx context.Context, r Record) (map[string]int, error) {
	return sessions(ctx, r, pidAlive)
}

// sessions is Sessions with the liveness check passed per call (a test seam,
// never a package var).
func sessions(ctx context.Context, r Record, alive func(int) bool) (map[string]int, error) {
	if !alive(r.PID) {
		return nil, &StaleError{PID: r.PID}
	}
	tok, err := tokenfile.Load(r.TokenFile)
	if err != nil {
		return nil, err
	}
	ep := modelping.Endpoint{URL: dialURL(r.Listen), Token: tok}
	if r.TLSCert != "" {
		leaf, err := tlsfile.LeafOf(r.TLSCert)
		if err != nil {
			return nil, fmt.Errorf("the model server's certificate named in its runtime record: %w", err)
		}
		ep.URL, ep.CAFile, ep.ServerName = dialURLTLS(r.Listen), r.TLSCert, tlsfile.DialName(leaf)
		// serve validated the certificate when it started and serves it
		// until restarted: verify it (that file only) as of that time, so
		// one that expired since still lets host commands read sessions.
		ep.VerifyAt = r.Started
	}
	var cat modelapi.Catalog
	if err := modelping.GetJSON(ctx, ep, "/control/v1/catalog", &cat); err != nil {
		return nil, err
	}
	out := map[string]int{}
	for _, row := range cat.Rows {
		if row.Sessions > 0 {
			out[row.ID] = row.Sessions
		}
	}
	return out, nil
}

// StaleError is a record whose pid is not running: the server died without
// removing it. Every other Sessions error comes from a server whose process
// is alive, so its client sessions are unknown, never "none".
type StaleError struct{ PID int }

func (e *StaleError) Error() string {
	return fmt.Sprintf("the model server (pid %d) is not running: stale record", e.PID)
}

// dialAddr is the host:port a host command reaches a server bound to listen
// at: loopback when its host is unspecified (0.0.0.0, [::]), else listen
// itself.
func dialAddr(listen string) string {
	if host, port, err := net.SplitHostPort(listen); err == nil {
		if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
			return "127.0.0.1:" + port
		}
	}
	return listen
}

// dialURL is the plain-http url of dialAddr.
func dialURL(listen string) string { return "http://" + dialAddr(listen) }

// dialURLTLS is the https url of dialAddr.
func dialURLTLS(listen string) string { return "https://" + dialAddr(listen) }

// pidAlive reports whether pid is a running process (EPERM means alive).
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
