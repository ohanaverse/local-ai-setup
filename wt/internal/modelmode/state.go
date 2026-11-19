package modelmode

import (
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelping"
)

// ServerKind says how a ping failed. It is modelping.Kind; the constants are
// aliases so both packages' switches read the same names.
type ServerKind = modelping.Kind

const (
	KindNone         = modelping.KindNone
	KindUnreachable  = modelping.KindUnreachable
	KindUnauthorized = modelping.KindUnauthorized
	KindNotWtModel   = modelping.KindNotWtModel
	KindSkew         = modelping.KindSkew
	KindSchema       = modelping.KindSchema
)

// ConfigState is config.toml's [model-server] table as read.
// URLHadCredentials is true when the stored url held an '@' (read as a
// user:password), so Observe replaced Table.URL whole with urlrule.Hidden
// (Problems says so in words); a plan that writes a url writes a retyped one
// in its place (urlrule.Hidden itself holds an '@' and is refused), so the
// credentials are not kept.
type ConfigState struct {
	Path                        string
	Exists                      bool
	ParseErr, TableErr, RestErr string
	Table                       config.ModelServerState
	Problems                    []string
	URLHadCredentials           bool
}

// TokenState describes the token file; the token itself is never held.
// Empty is true when the file exists, is not loose and holds nothing but
// white space (a loose file is never read, so its content is unknown).
type TokenState struct {
	Path                 string
	Exists, Loose, Empty bool
	// Dangling: the path is a symlink that does not resolve (Exists is true
	// too, and Err names it). Writing there would replace the link.
	Dangling bool
	Err      string
}

// ServerState is the result of pinging the configured model server. Checked
// is true only when a URL and a token were known and a Ping was attempted.
// When the URL or token came from the local agent (its listen address or
// token file, used in local mode when nothing else is set) that is a fallback
// a session would not apply, so the tab words it as the agent's, not the
// session's.
//
// URL is the endpoint that was pinged, with any userinfo removed, so a plan
// can tell whether a check was of the url it is about to store.
type ServerState struct {
	URL         string
	Checked, OK bool
	Kind        ServerKind
	Message     string
	API, Schema int
}

// HostState is what a session needs from the host so BuildPlan stays pure:
// the home directory and the base config directory ($XDG_CONFIG_HOME, else
// ~/.config; the parent of agent-wt and wt-model). The admin host facts (the
// wt-model binary, the captured PATH, the uid) are modelsetup's ToolsState.
type HostState struct {
	Home       string
	ConfigHome string
}

// State is the session half of what Observe saw: the mode read, the config
// and token files as found, the server's ping and the host's session paths.
// It never holds a secret (CheckToken never reaches it).
type State struct {
	Resolution  Resolution
	EnvOverride string
	Config      ConfigState
	// Token is the token file a session would use (see Observe).
	Token TokenState
	// TokenFiles describes the other token files a plan may choose: the
	// local default (DefaultLocalTokenPath), the fixed remote path
	// (RemoteTokenPath), the agent's own token file and
	// ObserveOptions.TokenPaths, each once, in that order.
	TokenFiles []TokenState
	Server     ServerState
	Host       HostState
}

// LocalFallback is a local-mode plan's inputs: the token file to use and the
// listen address to point at when the mode resolves local.
type LocalFallback struct {
	TokenFile string
	Listen    string
}

// ObserveOptions tunes Observe's session half. TokenPaths are extra token
// files to describe in State.TokenFiles (a path the user typed for remote,
// say), so BuildPlan can judge them without reading the disk itself.
//
// CheckURL, when set, is the url the user entered: Observe pings it instead
// of the configured one, with CheckToken (a pasted token) or else the token
// read from CheckTokenFile, so a remote plan's check is about the server the
// user named. Neither the token nor the url's userinfo goes into State. A
// url holding an '@' (entered, stored or WT_MODEL_URL) is never pinged: the
// check reads as not made, with the fixed NotCheckedAtURL message.
//
// Local is the local fallback a local-mode read may carry: modelsetup's
// Observe sets it from the LaunchAgent's own token file and listen address.
type ObserveOptions struct {
	TokenPaths     []string
	CheckURL       string
	CheckToken     Secret
	CheckTokenFile string
	Local          LocalFallback
}
