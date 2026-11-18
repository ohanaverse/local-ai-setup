package config

import (
	"errors"
	"strings"
)

// The LiteLLM pair url's credential rule (#640), the same rule the
// model-server url follows (modelclient.HasUserinfo / HiddenURL): no
// parsing, no partial echo. A url holding an ASCII '@' anywhere is read as
// carrying a user:password; `litellm set` refuses to write one, and a stored
// one (hand-written, or from an older wt) is never printed: it shows as
// ProxyURLHidden, whole. config may not import modelclient (cmd/wt-model
// links config and may not link the session half), so the two lines are
// repeated here; TestProxyURLRuleIsModelclientsRule holds them equal.
// What the gateway does with a stored url is unchanged.

// ProxyURLHidden stands in for a pair url holding an '@' wherever one would
// be printed. It is modelclient.HiddenURL.
const ProxyURLHidden = "(hidden: the url contains an @)"

// ErrProxyURLHasAt refuses a pair url holding an '@'. Its text is fixed: it
// quotes nothing of the url.
var ErrProxyURLHasAt = errors.New("the LiteLLM url contains an @ (anywhere in it, read as a user:password); wt does not store credentials in the url: give the proxy key with --api-key, and write a literal @ in a path as %40; nothing was written")

// ProxyURLHasAt reports whether raw holds an ASCII '@' anywhere. It is
// modelclient.HasUserinfo.
func ProxyURLHasAt(raw string) bool { return strings.Contains(raw, "@") }

// ShownProxyURL is the form of a pair url that may be printed: raw when it
// holds no '@', else ProxyURLHidden.
func ShownProxyURL(raw string) string {
	if ProxyURLHasAt(raw) {
		return ProxyURLHidden
	}
	return raw
}
