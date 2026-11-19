package modelmode

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelping"
	"github.com/ohanaverse/local-ai-setup/wt/internal/urlrule"
)

const (
	obsTokenPath = "/Users/user_name/.config/wt-model/token"
	obsFakeToken = "tok-SECRET-0123456789"
)

// obsConfigured returns a fake with a local-mode config, a 0600 token file and
// nothing else, the baseline the Observe tests vary.
func obsConfigured(t *testing.T) *fakeEnv {
	f := newFakeEnv(t)
	f.configFile = config.ModelServerFile{
		Path: "/Users/user_name/.config/agent-wt/config.toml", Exists: true,
		Table: &config.ModelServerState{Mode: "local", URL: "http://127.0.0.1:8787", TokenFile: obsTokenPath},
	}
	f.files[obsTokenPath] = []byte(obsFakeToken + "\n")
	f.modes[obsTokenPath] = 0o600
	return f
}

// TestObserveMissingConfigIsEmptyNotAnError pins that a machine with no
// config.toml observes as an empty, error-free Config part, so a first run
// is not reported as a failure.
func TestObserveMissingConfigIsEmptyNotAnError(t *testing.T) {
	f := newFakeEnv(t)
	st := Observe(context.Background(), f.Env(), ObserveOptions{})
	if st.Config.Exists || st.Config.ParseErr != "" || st.Config.TableErr != "" {
		t.Errorf("Config = %+v, want empty and error-free", st.Config)
	}
	if st.Server.Checked {
		t.Errorf("Server.Checked with nothing configured")
	}
}

// TestObserveLoosenessOfTheTokenFile pins that a group/other-accessible token
// file is reported Loose and is never pinged with, while 0600 is fine.
func TestObserveLoosenessOfTheTokenFile(t *testing.T) {
	f := obsConfigured(t)
	f.modes[obsTokenPath] = 0o644
	st := Observe(context.Background(), f.Env(), ObserveOptions{})
	if !st.Token.Exists || !st.Token.Loose {
		t.Errorf("Token = %+v, want Exists and Loose", st.Token)
	}
	if st.Server.Checked || f.called("Net.Ping") {
		t.Errorf("pinged with a loose token file")
	}
	f.modes[obsTokenPath] = 0o600
	if st := Observe(context.Background(), f.Env(), ObserveOptions{}); st.Token.Loose || !st.Server.Checked {
		t.Errorf("0600: Token = %+v Server = %+v", st.Token, st.Server)
	}
}

// TestObserveNamesEnvOverrides pins that the environment variables in force
// are named (never their values) and that an unset environment reads "".
func TestObserveNamesEnvOverrides(t *testing.T) {
	f := obsConfigured(t)
	if st := Observe(context.Background(), f.Env(), ObserveOptions{}); st.EnvOverride != "" {
		t.Errorf("EnvOverride = %q, want empty", st.EnvOverride)
	}
	f.env["WT_MODEL_URL"] = "http://elsewhere:1"
	f.env["WT_MODEL_TOKEN_FILE"] = obsTokenPath
	st := Observe(context.Background(), f.Env(), ObserveOptions{})
	if st.EnvOverride != "WT_MODEL_URL, WT_MODEL_TOKEN_FILE" {
		t.Errorf("EnvOverride = %q", st.EnvOverride)
	}
	if strings.Contains(st.EnvOverride, "elsewhere") {
		t.Errorf("EnvOverride holds a value: %q", st.EnvOverride)
	}
}

// TestObserveKeepsAnInlineConfigTokenOutOfState pins that a literal token in
// [model-server] is reported as a problem but never copied into State.
func TestObserveKeepsAnInlineConfigTokenOutOfState(t *testing.T) {
	f := obsConfigured(t)
	f.configFile.Table.Token = obsFakeToken
	st := Observe(context.Background(), f.Env(), ObserveOptions{})
	if strings.Contains(fmt.Sprintf("%+v", st), obsFakeToken) {
		t.Errorf("State holds the inline token: %+v", st.Config)
	}
	if len(st.Config.Problems) == 0 {
		t.Errorf("inline token not reported as a problem")
	}
}

// TestObserveExpandsATildeInTheConfiguredTokenFile pins that a config
// token_file of ~/... is read from the home directory, as a session does.
func TestObserveExpandsATildeInTheConfiguredTokenFile(t *testing.T) {
	f := obsConfigured(t)
	f.configFile.Table.TokenFile = "~/.config/wt-model/token"
	st := Observe(context.Background(), f.Env(), ObserveOptions{})
	if !st.Token.Exists || st.Token.Err != "" || !st.Server.Checked {
		t.Errorf("Token = %+v Server = %+v", st.Token, st.Server)
	}
}

// TestObserveTokenChoiceMirrorsTheSession pins ResolveEndpoint's order: an env
// token file wins and never falls back to a literal WT_MODEL_TOKEN, which is
// otherwise used untrimmed.
func TestObserveTokenChoiceMirrorsTheSession(t *testing.T) {
	f := obsConfigured(t)
	f.env["WT_MODEL_TOKEN_FILE"] = "/missing/token"
	f.env["WT_MODEL_TOKEN"] = obsFakeToken
	st := Observe(context.Background(), f.Env(), ObserveOptions{})
	if st.Token.Path != "/missing/token" || st.Token.Exists || st.Server.Checked {
		t.Errorf("env file must win without fallback: %+v %+v", st.Token, st.Server)
	}
	f = obsConfigured(t)
	f.env["WT_MODEL_TOKEN"] = " " + obsFakeToken + " "
	f.calls = nil
	st = Observe(context.Background(), f.Env(), ObserveOptions{})
	if !st.Server.Checked || f.called("FS.ReadFile "+obsTokenPath) {
		t.Errorf("literal token: %+v calls %q", st.Server, f.calls)
	}
}

// TestObserveRecordsThePingedURLWithoutUserinfo pins that a url holding an
// '@' is never stored in ServerState: it is not pinged at all (the check
// reads as not made) and is recorded as the fixed urlrule.Hidden, whole (no host
// kept).
func TestObserveRecordsThePingedURLWithoutUserinfo(t *testing.T) {
	f := obsConfigured(t)
	f.configFile.Table.Mode = "remote"
	f.configFile.Table.URL = "https://me:pw-SECRET@gpu.example:8787/"
	st := Observe(context.Background(), f.Env(), ObserveOptions{})
	if st.Server.Checked || st.Server.URL != urlrule.Hidden || f.called("Net.Ping") {
		t.Errorf("Server = %+v, calls %q", st.Server, f.calls)
	}
	if strings.Contains(fmt.Sprintf("%+v", st.Server), "pw-SECRET") {
		t.Errorf("ServerState holds the url's password")
	}
}

// TestObserveStripsUserinfoFromTheConfiguredURL pins that a [model-server]
// url holding an '@' never reaches State.Config.Table (the tab draws that
// table): it is replaced whole by urlrule.Hidden, host included; that it is
// reported as a problem the way an inline token is, and that it is not
// pinged (a failed ping's text could quote the url, or all of it when malformed).
func TestObserveStripsUserinfoFromTheConfiguredURL(t *testing.T) {
	f := obsConfigured(t)
	f.configFile.Table.Mode = "remote"
	f.configFile.Table.URL = "https://me:pw-SECRET@gpu.example:8787/"
	st := Observe(context.Background(), f.Env(), ObserveOptions{})
	if st.Config.Table.URL != urlrule.Hidden {
		t.Errorf("Config.Table.URL = %q, want %q", st.Config.Table.URL, urlrule.Hidden)
	}
	if strings.Contains(fmt.Sprintf("%+v", st), "pw-SECRET") {
		t.Errorf("State holds the url's password")
	}
	found := false
	for _, p := range st.Config.Problems {
		if strings.Contains(p, "credentials") {
			found = true
		}
	}
	if !found {
		t.Errorf("Problems = %q, want one about the url's credentials", st.Config.Problems)
	}
	if st.Server.Checked || f.called("Net.Ping") {
		t.Errorf("Server = %+v, calls %q, want the url not pinged", st.Server, f.calls)
	}
}

// TestObservePingsTheEnteredEndpoint pins ObserveOptions.Check: when the tab
// asks about a url the user typed, Observe pings that url (not the stored
// one) with the token the user entered (pasted, else read from the typed
// token file), so a remote plan's check gate is about the server the user
// named. Without an entered token nothing is pinged and the reason is said;
// no State field holds the entered token. An entered url with an '@' is
// pinged neither (TestObserveNeverPingsAnAtURL has the full set), nor is the
// stored one in its place.
func TestObservePingsTheEnteredEndpoint(t *testing.T) {
	f := obsConfigured(t)
	st := Observe(context.Background(), f.Env(), ObserveOptions{
		CheckURL: "https://gpu.example:8787", CheckToken: Secret("  tok-ENTERED  "),
	})
	if !f.called("Net.Ping https://gpu.example:8787") || f.called("Net.Ping http://127.0.0.1:8787") {
		t.Errorf("calls = %q, want the entered url pinged as typed and not the stored one", f.calls)
	}
	if len(f.pingTokens) != 1 || f.pingTokens[0] != "tok-ENTERED" {
		t.Errorf("ping tokens = %q, want the pasted token, trimmed", f.pingTokens)
	}
	if !st.Server.Checked || st.Server.URL != "https://gpu.example:8787" {
		t.Errorf("Server = %+v, want the entered url recorded", st.Server)
	}
	if s := fmt.Sprintf("%+v", st); strings.Contains(s, "tok-ENTERED") {
		t.Errorf("State holds the entered token")
	}

	// An entered url with an '@': nothing is pinged, the stored url included.
	f = obsConfigured(t)
	st = Observe(context.Background(), f.Env(), ObserveOptions{
		CheckURL: "https://me:pw-SECRET@gpu.example:8787", CheckToken: Secret("tok-ENTERED"),
	})
	if f.called("Net.Ping") || st.Server.Checked || st.Server.URL != urlrule.Hidden {
		t.Errorf("calls = %q Server = %+v, want no ping and the url hidden", f.calls, st.Server)
	}
	if s := fmt.Sprintf("%+v", st); strings.Contains(s, "tok-ENTERED") || strings.Contains(s, "pw-SECRET") {
		t.Errorf("State holds the entered token or the url's password")
	}

	// A typed token file is read for the ping.
	f = obsConfigured(t)
	f.files["/srv/remote.token"] = []byte("tok-FILE\n")
	f.modes["/srv/remote.token"] = 0o600
	st = Observe(context.Background(), f.Env(), ObserveOptions{CheckURL: "https://gpu.example:8787", CheckTokenFile: "/srv/remote.token"})
	if len(f.pingTokens) != 1 || f.pingTokens[0] != "tok-FILE" || !st.Server.Checked {
		t.Errorf("ping tokens = %q Server = %+v, want the typed file's token", f.pingTokens, st.Server)
	}

	// No entered token: not pinged, and the reason is said.
	f = obsConfigured(t)
	st = Observe(context.Background(), f.Env(), ObserveOptions{CheckURL: "https://gpu.example:8787"})
	if f.called("Net.Ping") || st.Server.Checked || !strings.Contains(st.Server.Message, "no token") {
		t.Errorf("calls = %q Server = %+v, want no ping and a message naming the missing token", f.calls, st.Server)
	}
}

// TestObserveRecordsAClassifiedPingFailure pins that a ping failure reaches
// State already classified (Kind plus the numbers the server spoke, from the
// answer itself), not just as an error string.
func TestObserveRecordsAClassifiedPingFailure(t *testing.T) {
	f := obsConfigured(t)
	f.pingErr = &modelping.SkewError{Addr: "a", Server: 2, Client: 1}
	st := Observe(context.Background(), f.Env(), ObserveOptions{})
	if !st.Server.Checked || st.Server.OK {
		t.Errorf("Server = %+v, want checked and not OK", st.Server)
	}
	if st.Server.Kind != KindSkew {
		t.Errorf("Kind = %v, want skew", st.Server.Kind)
	}
	if st.Server.API != 2 {
		t.Errorf("API = %d, want 2 from the answer", st.Server.API)
	}
	if st.Server.Message != f.pingErr.Error() {
		t.Errorf("Message = %q, want %q", st.Server.Message, f.pingErr.Error())
	}
}

// TestObserveUsesTheLocalFallbackOnlyInLocalMode pins ObserveOptions.Local:
// a local-mode read with no url or token_file stored uses the fallback's
// token file (as the chosen Token, so TokenFiles does not list it twice) and
// pings the fallback's listen; the same fallback in remote mode is inert —
// no ping, no token chosen — and its path stays listed among the files a
// plan may choose.
func TestObserveUsesTheLocalFallbackOnlyInLocalMode(t *testing.T) {
	f := newFakeEnv(t)
	f.configFile = config.ModelServerFile{
		Path: "/Users/user_name/.config/agent-wt/config.toml", Exists: true,
		Table: &config.ModelServerState{Mode: "local"},
	}
	f.files["/a/tok"] = []byte("tok-AGENT\n")
	f.modes["/a/tok"] = 0o600
	opts := ObserveOptions{Local: LocalFallback{TokenFile: "/a/tok", Listen: "127.0.0.1:9911"}}
	st := Observe(context.Background(), f.Env(), opts)
	if !f.called("Net.Ping http://127.0.0.1:9911") {
		t.Errorf("calls = %q, want the fallback's listen pinged", f.calls)
	}
	if len(f.pingTokens) != 1 || f.pingTokens[0] != "tok-AGENT" {
		t.Errorf("ping tokens = %q, want the fallback token file's token", f.pingTokens)
	}
	if st.Token.Path != "/a/tok" {
		t.Errorf("Token.Path = %q, want the fallback's token file", st.Token.Path)
	}
	if len(st.TokenFiles) != 2 {
		t.Errorf("TokenFiles = %+v, want only the default local and remote paths (the chosen token seeds seen)", st.TokenFiles)
	}
	for _, ts := range st.TokenFiles {
		if ts.Path == "/a/tok" {
			t.Errorf("TokenFiles lists the chosen token twice: %+v", st.TokenFiles)
		}
	}

	f = newFakeEnv(t)
	f.configFile = config.ModelServerFile{
		Path: "/Users/user_name/.config/agent-wt/config.toml", Exists: true,
		Table: &config.ModelServerState{Mode: "remote"},
	}
	f.files["/a/tok"] = []byte("tok-AGENT\n")
	f.modes["/a/tok"] = 0o600
	st = Observe(context.Background(), f.Env(), opts)
	if f.called("Net.Ping") {
		t.Errorf("calls = %q, want no ping in remote mode", f.calls)
	}
	if st.Token.Path != "" {
		t.Errorf("Token.Path = %q, want no token chosen in remote mode", st.Token.Path)
	}
	if len(st.TokenFiles) != 3 || st.TokenFiles[2].Path != "/a/tok" {
		t.Errorf("TokenFiles = %+v, want the fallback's path listed third", st.TokenFiles)
	}
}

// TestObserveWithoutAFallbackPingsTheDefaultLocalURL pins the default when
// no Local fallback is set: a local-mode read with a stored token but no url
// pings the fixed default listen.
func TestObserveWithoutAFallbackPingsTheDefaultLocalURL(t *testing.T) {
	f := newFakeEnv(t)
	f.configFile = config.ModelServerFile{
		Path: "/Users/user_name/.config/agent-wt/config.toml", Exists: true,
		Table: &config.ModelServerState{Mode: "local", TokenFile: obsTokenPath},
	}
	f.files[obsTokenPath] = []byte(obsFakeToken + "\n")
	f.modes[obsTokenPath] = 0o600
	st := Observe(context.Background(), f.Env(), ObserveOptions{})
	if !f.called("Net.Ping http://127.0.0.1:8787") {
		t.Errorf("calls = %q, want the default local listen pinged", f.calls)
	}
	if !st.Server.Checked {
		t.Errorf("Server = %+v, want checked", st.Server)
	}
}

// TestObserveClassifiesEachPingFailure pins that every ping error type maps to
// its own ServerKind in State (so the tab can say what is wrong), that a
// wrapped error classifies like its cause, that a skew or schema failure
// carries the number the server spoke, and that a good answer carries the
// API and schema numbers. It guards PingServer, which modelsetup's Observe
// and its wait step both rely on.
func TestObserveClassifiesEachPingFailure(t *testing.T) {
	cases := []struct {
		name        string
		err         error
		want        ServerKind
		api, schema int
	}{
		{"none", nil, KindNone, 1, 3},
		{"unreachable", &modelping.UnreachableError{Addr: "a", Reason: "refused"}, KindUnreachable, 0, 0},
		{"unauthorized", &modelping.UnauthorizedError{Addr: "a"}, KindUnauthorized, 0, 0},
		{"not wt-model", &modelping.NotModelServerError{Addr: "a", Why: "x"}, KindNotWtModel, 0, 0},
		{"skew", &modelping.SkewError{Addr: "a", Server: 2, Client: 1}, KindSkew, 2, 0},
		{"schema", &modelping.SchemaError{Addr: "a", Server: 4, Client: 3}, KindSchema, 0, 4},
		{"wrapped", fmt.Errorf("ctx: %w", &modelping.UnauthorizedError{Addr: "a"}), KindUnauthorized, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := obsConfigured(t)
			f.pingErr = c.err
			f.ping = modelapi.Ping{APIMajor: 1, Schema: 3}
			st := Observe(context.Background(), f.Env(), ObserveOptions{})
			if !st.Server.Checked || st.Server.Kind != c.want || st.Server.OK != (c.err == nil) {
				t.Errorf("Server = %+v, want kind %v", st.Server, c.want)
			}
			if c.err != nil && st.Server.Message == "" {
				t.Errorf("no message for %v", c.err)
			}
			if st.Server.API != c.api || st.Server.Schema != c.schema {
				t.Errorf("API/Schema = %d/%d, want %d/%d", st.Server.API, st.Server.Schema, c.api, c.schema)
			}
		})
	}
}
