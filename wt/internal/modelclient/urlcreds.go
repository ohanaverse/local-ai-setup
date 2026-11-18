package modelclient

import "strings"

// The session side's one rule for a model-server url that may carry
// credentials, the same rule the Model server tab applies (internal/modelsetup
// calls HasUserinfo): no parsing, no partial echo. A model-server url has no
// legitimate '@', so any url holding one is read as carrying a user:password
// and refused before a request is built, and nothing derived from it (no
// host, no user, no scheme, no piece) is ever printed. Finding the credential
// span in an arbitrary string was tried and bypassed three times (a leading
// "//", a password holding '/', '?', '#' or "://", a schemeless url that
// url.Parse reads as an opaque one); this rule has nothing to find.

// HiddenURL stands in for a url holding an '@' wherever one would be shown.
// It holds an '@' itself, so it is refused like the url it stands for.
const HiddenURL = "(hidden: the url contains an @)"

// HasUserinfo reports whether raw holds an ASCII '@' anywhere: every such url
// is read as carrying a user:password. Fullwidth U+FF20 and %40 are not '@'
// (no dialler reads them as the user separator).
func HasUserinfo(raw string) bool {
	return strings.Contains(raw, "@")
}

// CredentialsInURLError refuses a model-server url that contains an '@',
// whatever its source (WT_MODEL_URL or url under [model-server]). Its text is
// fixed: it quotes nothing of the url. It is a hard refusal (exit 1), never a
// "no model server" case (NoServer is false for it).
type CredentialsInURLError struct{}

func (*CredentialsInURLError) Error() string {
	return "the model server url contains an @ (anywhere in it, read as a user:password); wt does not use credentials in the url: use a token file (WT_MODEL_TOKEN_FILE or token_file under [model-server]); write a literal @ in a path as %40"
}

// unparsedAddr stands in for host:port when the url has no host url.Parse
// can find: the raw origin is never echoed in its place.
const unparsedAddr = "(the configured url, which names no host)"

// unbuildableRequest is the reason given when net/http cannot build a
// request from the url: its own error quotes the whole url.
const unbuildableRequest = "the model server url does not parse as a url with a host"
