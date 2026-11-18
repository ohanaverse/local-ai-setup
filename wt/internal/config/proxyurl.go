package config

import (
	"errors"

	"github.com/ohanaverse/local-ai-setup/wt/internal/urlrule"
)

// The LiteLLM pair url's credential rule (#640) lives once in
// internal/urlrule — the same rule the model-server url follows — and this
// file holds config's names for it: `litellm set` refuses to write a url
// holding an ASCII '@' anywhere, and a stored one (hand-written, or from an
// older wt) is never printed: it shows as ProxyURLHidden, whole. What the
// gateway does with a stored url is unchanged.

// ProxyURLHidden stands in for a pair url holding an '@' wherever one would
// be printed. It is urlrule.Hidden.
const ProxyURLHidden = urlrule.Hidden

// ErrProxyURLHasAt refuses a pair url holding an '@'. Its text is fixed: it
// quotes nothing of the url.
var ErrProxyURLHasAt = errors.New("the LiteLLM url contains an @ (anywhere in it, read as a user:password); wt does not store credentials in the url: give the proxy key with --api-key, and write a literal @ in a path as %40; nothing was written")

// ProxyURLHasAt reports whether raw holds an ASCII '@' anywhere. It is
// urlrule.HasUserinfo.
func ProxyURLHasAt(raw string) bool { return urlrule.HasUserinfo(raw) }

// ShownProxyURL is the form of a pair url that may be printed: raw when it
// holds no '@', else ProxyURLHidden. It is urlrule.Shown.
func ShownProxyURL(raw string) string { return urlrule.Shown(raw) }
