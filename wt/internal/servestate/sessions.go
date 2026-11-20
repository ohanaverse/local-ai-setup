package servestate

import (
	"context"
	"net"

	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelping"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tokenfile"
)

// Sessions asks the server r names for its catalog and returns the model ids
// that have at least one client session, mapped to that count (a non-nil,
// empty map when none has). The token is read from r.TokenFile (its
// *tokenfile.Error returned as is, before any request); the address dialled
// is r.Listen, on loopback when it is bound to every interface. A stale
// record is the caller's to tolerate: it is an unreachable server, an error.
// ctx bounds the whole exchange. No error carries the token.
func Sessions(ctx context.Context, r Record) (map[string]int, error) {
	tok, err := tokenfile.Load(r.TokenFile)
	if err != nil {
		return nil, err
	}
	var cat modelapi.Catalog
	if err := modelping.GetJSON(ctx, modelping.Endpoint{URL: dialURL(r.Listen), Token: tok}, "/control/v1/catalog", &cat); err != nil {
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

// dialURL is the url a host command reaches a server bound to listen at:
// loopback when its host is unspecified (0.0.0.0, [::]), else listen itself.
func dialURL(listen string) string {
	if host, port, err := net.SplitHostPort(listen); err == nil {
		if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
			return "http://127.0.0.1:" + port
		}
	}
	return "http://" + listen
}
