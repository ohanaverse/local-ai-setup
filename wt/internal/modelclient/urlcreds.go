package modelclient

import (
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelping"
	"github.com/ohanaverse/local-ai-setup/wt/internal/urlrule"
)

// HiddenURL stands in for a url holding an '@' wherever one would be shown.
// It holds an '@' itself, so it is refused like the url it stands for.
// The rule lives once in internal/urlrule; this is its modelclient name.
const HiddenURL = urlrule.Hidden

// HasUserinfo reports whether raw holds an ASCII '@' anywhere: every such url
// is read as carrying a user:password. Fullwidth U+FF20 and %40 are not '@'
// (no dialler reads them as the user separator). It is urlrule.HasUserinfo.
func HasUserinfo(raw string) bool { return urlrule.HasUserinfo(raw) }

// CredentialsInURLError refuses a model-server url that contains an '@',
// whatever its source (WT_MODEL_URL or url under [model-server]). Its text is
// fixed: it quotes nothing of the url. It is a hard refusal (exit 1), never a
// "no model server" case (NoServer is false for it). It is
// modelping.CredentialsInURLError.
type CredentialsInURLError = modelping.CredentialsInURLError

// unparsedAddr stands in for host:port when the url has no host url.Parse
// can find: the raw origin is never echoed in its place. It is
// modelping.UnparsedAddr, aliased for this package's texts and tests.
const unparsedAddr = modelping.UnparsedAddr

// unbuildableRequest is the reason given when net/http cannot build a
// request from the url: its own error quotes the whole url. It is
// modelping.UnbuildableRequest.
const unbuildableRequest = modelping.UnbuildableRequest
