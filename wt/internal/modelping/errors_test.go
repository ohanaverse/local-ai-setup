package modelping

import (
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/urlrule"
)

// TestErrorTextsAreByteForByte pins the ping errors' texts as moved from
// modelclient: callers and scripts match on these strings, so the move below
// modelclient may not change a byte of any of them.
func TestErrorTextsAreByteForByte(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"unreachable",
			&UnreachableError{Addr: "127.0.0.1:8787", Reason: "connection refused"},
			"model server at 127.0.0.1:8787 is not reachable (connection refused) — start it with 'wt-model serve --listen 127.0.0.1:8787 --token-file PATH', or fix url under [model-server] in config.toml"},
		{"unreachable at a hidden url",
			&UnreachableError{Addr: urlrule.Hidden},
			(&CredentialsInURLError{}).Error()},
		{"unreachable at an unparsed url",
			&UnreachableError{Addr: UnparsedAddr, Reason: UnbuildableRequest},
			UnbuildableRequest + " (it is not shown) — write it as http://HOST:PORT under [model-server] in config.toml, or in WT_MODEL_URL"},
		{"unauthorized",
			&UnauthorizedError{Addr: "h:1"},
			"model server at h:1 refused the token (" + modelapi.MsgUnauthorized + ") — check token_file under [model-server] in config.toml and that the server was started with the same token"},
		{"skew, server behind",
			&SkewError{"h:1", 2, 1},
			"model server at h:1 speaks API major 2, this wt speaks 1 — upgrade wt"},
		{"skew, server ahead",
			&SkewError{"h:1", 0, 1},
			"model server at h:1 speaks API major 0, this wt speaks 1 — upgrade wt-model"},
		{"schema, server older",
			&SchemaError{"h:1", 1, 2},
			"model server at h:1 speaks catalog schema 1, this wt needs 2 — restart wt-model (it is older than this wt)"},
		{"schema, server newer",
			&SchemaError{"h:1", 3, 2},
			"model server at h:1 sends catalog schema 3, this wt reads up to 2 — upgrade wt"},
		{"not a wt-model",
			&NotModelServerError{"h:1", "x"},
			"h:1 is not a wt-model server (x) — check url under [model-server] in config.toml"},
		{"answer too large",
			&ResponseTooLargeError{"h:1", 32 << 20},
			"model server at h:1 sent an answer larger than 32 MiB — this wt reads answers up to that size"},
		{"credentials in url",
			&CredentialsInURLError{},
			"the model server url contains an @ (anywhere in it, read as a user:password); wt does not use credentials in the url: use a token file (WT_MODEL_TOKEN_FILE or token_file under [model-server]); write a literal @ in a path as %40"},
	}
	for _, c := range cases {
		if got := c.err.Error(); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}

	// Clean applies inside every text: a hostile Why cannot forge a line or
	// override the direction of the rest.
	hostile := (&NotModelServerError{Addr: "h:1", Why: "a\nb‮"}).Error()
	if strings.Contains(hostile, "\n") {
		t.Errorf("a newline survived the text: %q", hostile)
	}
	if strings.Contains(hostile, "‮") {
		t.Errorf("an RLO (U+202E) survived the text: %q", hostile)
	}
}

// TestAddrNeverEchoesTheURL pins Addr's fail-closed display: an '@' url shows
// the placeholder whole, a url with no host shows the fixed no-host stand-in,
// and an ordinary one shows only its host:port — never a piece of the raw url.
func TestAddrNeverEchoesTheURL(t *testing.T) {
	if got := Addr("http://u:p@h:1"); got != urlrule.Hidden {
		t.Errorf("Addr of an '@' url = %q, want %q", got, urlrule.Hidden)
	}
	if got := Addr("hostzz9"); got != UnparsedAddr {
		t.Errorf("Addr of a hostless url = %q, want %q", got, UnparsedAddr)
	}
	if got := Addr("http://127.0.0.1:8787/x/"); got != "127.0.0.1:8787" {
		t.Errorf("Addr = %q, want %q", got, "127.0.0.1:8787")
	}
}

// TestEndpointOriginAndGateway pins the endpoint pair the ping and gateway
// code share: Origin drops every trailing slash, Gateway passes origin and
// token through unchanged.
func TestEndpointOriginAndGateway(t *testing.T) {
	ep := Endpoint{URL: "http://h:1//", Token: "t"}
	if got := ep.Origin(); got != "http://h:1" {
		t.Errorf("Origin() = %q, want %q", got, "http://h:1")
	}
	origin, token := ep.Gateway()
	if origin != "http://h:1" || token != "t" {
		t.Errorf("Gateway() = (%q, %q), want (%q, %q)", origin, token, "http://h:1", "t")
	}
}
