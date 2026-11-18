package modelsetup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
)

// Secret holds a secret the user typed (a pasted token, a master key). Every
// printed or marshalled form is the redaction "[redacted]" (empty stays
// empty, so absence is visible): fmt's %v, %+v, %#v, %s and %q, JSON and
// text marshalling. Reveal returns the value; Apply and the tab call it only
// at the point of use, never to log or show it.
type Secret string

const redacted = "[redacted]"

func (s Secret) redact() string {
	if s == "" {
		return ""
	}
	return redacted
}

// String redacts the secret.
func (s Secret) String() string { return s.redact() }

// GoString redacts the secret for %#v.
func (s Secret) GoString() string { return strconv.Quote(s.redact()) }

// MarshalJSON redacts the secret.
func (s Secret) MarshalJSON() ([]byte, error) { return json.Marshal(s.redact()) }

// MarshalText redacts the secret.
func (s Secret) MarshalText() ([]byte, error) { return []byte(s.redact()), nil }

// Reveal returns the secret itself; call it only where it is used.
func (s Secret) Reveal() string { return string(s) }

// Desired is what the user asked for. PastedToken (remote) and MasterKey
// (local, used only when the pair is unset and the LiteLLM plist has no key)
// are Secrets: BuildPlan keeps each only in its own mode's Plan.Want, for
// Apply, and no step, note, Format text or refusal holds them.
type Desired struct {
	Mode                       modelclient.Mode
	URL, TokenFile, Listen     string
	PastedToken, MasterKey     Secret
	SaveUnchecked, RemoveAgent bool
}

// StepKind is what one step does.
type StepKind string

// The steps, in the order a local plan runs them; StepRemovePlist belongs to
// the native and remote plans' optional agent removal.
const (
	StepToken       StepKind = "token"
	StepPair        StepKind = "pair"
	StepPlist       StepKind = "plist"
	StepBootout     StepKind = "bootout"
	StepBootstrap   StepKind = "bootstrap"
	StepKickstart   StepKind = "kickstart"
	StepWait        StepKind = "wait"
	StepConfig      StepKind = "config"
	StepRemovePlist StepKind = "remove-plist"
)

// StepAction says how a token or pair step gets its value, so Apply does
// not have to work it out again. It is a marker, never the value.
type StepAction string

// The actions of token and pair steps.
const (
	// ActionGenerateToken writes 32 random bytes, base64, mode 0600.
	ActionGenerateToken StepAction = "generate-token"
	// ActionTightenToken chmods a loose token file to 0600, keeping it.
	ActionTightenToken StepAction = "tighten-token"
	// ActionWritePastedToken writes Plan.Want.PastedToken to a new file.
	ActionWritePastedToken StepAction = "write-pasted-token"
	// ActionReplacePastedToken writes Plan.Want.PastedToken over an existing
	// file; only a plan whose text says "replace" carries it.
	ActionReplacePastedToken StepAction = "replace-pasted-token"
	// ActionPairKeyFromPlist reads LITELLM_MASTER_KEY from the LiteLLM plist
	// at Step.Path when applied.
	ActionPairKeyFromPlist StepAction = "pair-key-from-litellm-plist"
	// ActionPairKeyEntered uses Plan.Want.MasterKey.
	ActionPairKeyEntered StepAction = "pair-key-entered"
)

// Step is one change. Path is the file or plist the step acts on; Value is
// a non-secret operand (the pair's url, the url waited on); Detail is the
// line the user confirms.
type Step struct {
	Kind   StepKind
	Path   string
	Detail string
	Action StepAction
	Value  string
}

// Plan is what BuildPlan decided. Want is the request with every default
// filled in (Mode, URL, TokenFile and Listen as they will be written); Notes
// say what is left alone and anything else worth knowing; Agent is what a
// local plan's plist step renders (nil for native and remote).
type Plan struct {
	Want  Desired
	Steps []Step
	Notes []string
	Agent *AgentSpec
}

// RefusalError is a plan that cannot be made. Each reason says what is still
// true and the way out.
type RefusalError struct{ Reasons []string }

func (e *RefusalError) Error() string { return strings.Join(e.Reasons, "\n") }

// reason builds one refusal reason: the fact, the way out, and that nothing
// was changed.
func reason(fact, wayOut string) string {
	return fact + "; " + wayOut + "; nothing was changed"
}

// guide14 is the LiteLLM guide a missing master key points at.
const guide14 = "docs/guides/14-litellm-install-and-configuration.md"

// defaultLiteLLMPort is the proxy's port when its plist names none.
const defaultLiteLLMPort = 4000

// BuildPlan decides the steps that take the machine from st to want. It is
// pure: everything it needs is in st, and it reads no clock and no file.
func BuildPlan(st State, want Desired) (Plan, error) {
	if st.Config.ParseErr != "" {
		return Plan{}, &RefusalError{Reasons: []string{reason(
			"config.toml at "+st.Config.Path+" has a TOML syntax error ("+st.Config.ParseErr+") and is never overwritten",
			"fix it by hand, then re-read")}}
	}
	mode, ok := modelclient.ParseMode(string(want.Mode))
	if !ok {
		return Plan{}, &RefusalError{Reasons: []string{reason(
			"mode "+strconv.Quote(string(want.Mode))+" is not native, local or remote",
			"choose one of native, local or remote")}}
	}
	want.Mode = mode
	// Each secret stays only in the mode that uses it.
	if mode != modelclient.ModeRemote {
		want.PastedToken = ""
	}
	if mode != modelclient.ModeLocal {
		want.MasterKey = ""
	}
	switch mode {
	case modelclient.ModeLocal:
		return planLocal(st, want)
	case modelclient.ModeRemote:
		return planRemote(st, want)
	default:
		return planNative(st, want)
	}
}

// planNative stores only the mode, plus the optional agent removal.
func planNative(st State, want Desired) (Plan, error) {
	want.URL, want.TokenFile, want.Listen = "", "", ""
	p := Plan{Want: want}
	p.leaveAgent(st)
	p.keepLocalSetup(st)
	p.config(st)
	p.envNote(st)
	return p, nil
}

// planRemote writes a pasted token if any, then the table, plus the optional
// agent removal.
func planRemote(st State, want Desired) (Plan, error) {
	var reasons []string
	want.URL = strings.TrimSpace(want.URL)
	if want.URL == "" {
		reasons = append(reasons, reason("remote mode has no url", "enter the model server's url"))
	}
	var tokenStep *Step
	switch {
	case strings.TrimSpace(want.PastedToken.Reveal()) != "":
		if st.Host.ConfigHome == "" {
			reasons = append(reasons, reason("the config directory is not known, so the pasted token has nowhere to go", "set HOME or XDG_CONFIG_HOME"))
			break
		}
		path := RemoteTokenPath(st.Host.ConfigHome)
		want.TokenFile = path
		ts, _ := tokenAt(st, path)
		s := Step{Kind: StepToken, Path: path, Action: ActionWritePastedToken,
			Detail: "write the pasted token to " + path + " (mode 0600)"}
		if ts.Exists {
			s.Action = ActionReplacePastedToken
			s.Detail = "replace " + path + " with the pasted token (mode 0600; the old token there is lost)"
		}
		tokenStep = &s
	case strings.TrimSpace(want.TokenFile) != "":
		path := expandIn(strings.TrimSpace(want.TokenFile), st.Host.Home)
		want.TokenFile = path
		ts, seen := tokenAt(st, path)
		switch {
		case !seen:
			reasons = append(reasons, reason("token file "+path+" was not checked", "check again before applying"))
		case !ts.Exists:
			reasons = append(reasons, reason("token file "+path+" does not exist", "enter the path of an existing token file, or paste the token"))
		case ts.Loose:
			reasons = append(reasons, reason("token file "+path+" is readable by others", "run chmod 600 "+path))
		case ts.Empty:
			reasons = append(reasons, reason("token file "+path+" is empty", "put the server's token in it, or paste the token"))
		case ts.Err != "":
			reasons = append(reasons, reason("token file "+path+" cannot be read ("+ts.Err+")", "fix it, or paste the token"))
		}
	default:
		reasons = append(reasons, reason("remote mode has no token", "enter a token file path or paste the token"))
	}
	// A check of any other endpoint (the local server, an old remote) says
	// nothing about this one: it reads as not checked.
	server := st.Server
	if server.Checked && (want.URL == "" || !sameEndpoint(server.URL, want.URL)) {
		server = ServerState{}
	}
	if server.Checked && !server.OK && !want.SaveUnchecked {
		reasons = append(reasons, reason("the check of "+want.URL+" failed: "+serverFailure(server),
			"fix the url or token and check again, or choose save anyway (the server may be off)"))
	}
	if len(reasons) > 0 {
		return Plan{}, &RefusalError{Reasons: reasons}
	}
	want.Listen = ""
	p := Plan{Want: want}
	if tokenStep != nil {
		p.Steps = append(p.Steps, *tokenStep)
	}
	p.leaveAgent(st)
	p.keepLocalSetup(st)
	p.config(st)
	switch {
	case server.Checked && !server.OK:
		p.Notes = append(p.Notes, "the server did not answer the check ("+serverFailure(server)+"); the config is saved anyway")
	case !server.Checked:
		p.Notes = append(p.Notes, "the server at "+want.URL+" was not checked")
	}
	p.envNote(st)
	return p, nil
}

// planLocal sets up the token, the pair, the LaunchAgent and the table, in
// that order.
func planLocal(st State, want Desired) (Plan, error) {
	var reasons []string
	if !st.Platform.OK {
		reasons = append(reasons, reason("local mode cannot run here: "+st.Platform.Reason, "choose native or remote"))
	}
	if st.Host.Binary == "" {
		why := st.Host.BinaryErr
		if why == "" {
			why = "wt-model was not found"
		}
		reasons = append(reasons, reason(why, "run `make install` in wt/"))
	}
	if st.Host.Home == "" || st.Host.ConfigHome == "" {
		reasons = append(reasons, reason("the home directory is not known", "set HOME"))
	}

	// Listen address.
	listen := strings.TrimSpace(want.Listen)
	if listen == "" && st.Agent.Managed && st.Agent.Listen != "" {
		listen = st.Agent.Listen
	}
	if listen == "" {
		listen = DefaultListen
	}
	host, portText, err := net.SplitHostPort(listen)
	port, perr := strconv.Atoi(portText)
	switch {
	case err != nil || perr != nil || port < 1 || port > 65535:
		reasons = append(reasons, reason("listen address "+strconv.Quote(listen)+" is not host:port", "enter one like "+DefaultListen))
	case host != "127.0.0.1" && host != "localhost" && host != "::1":
		reasons = append(reasons, reason("listen address "+listen+" is not loopback", "use 127.0.0.1:<port>"))
	case st.Listener.Port != port:
		reasons = append(reasons, reason("port "+portText+" was not checked for a listener", "check again before applying"))
	case st.Listener.Err != "":
		reasons = append(reasons, reason("port "+portText+" could not be checked for a listener ("+st.Listener.Err+")", "check again before applying"))
	case st.Listener.PID != 0 && !st.Listener.Ours:
		reasons = append(reasons, reason(fmt.Sprintf("port %d is in use by pid %d, which is not wt's LaunchAgent", port, st.Listener.PID),
			"stop that process or choose another port"))
	}

	// The plist path must be wt's or free.
	if st.Agent.Present && !st.Agent.Managed {
		reasons = append(reasons, reason(st.Agent.PlistPath+" exists and is not marked \"managed by wt\", so wt will not overwrite it",
			"move it aside or remove it yourself, or choose native or remote"))
	}

	// Token.
	tokPath := strings.TrimSpace(want.TokenFile)
	if tokPath == "" && st.Agent.Managed && st.Agent.TokenFile != "" {
		tokPath = st.Agent.TokenFile
	}
	if tokPath == "" && st.Host.ConfigHome != "" {
		tokPath = DefaultLocalTokenPath(st.Host.ConfigHome)
	}
	tokPath = expandIn(tokPath, st.Host.Home)
	var tokenStep *Step
	tokenChanged := false
	var tokenKept string
	if tokPath != "" {
		ts, seen := tokenAt(st, tokPath)
		switch {
		case !seen:
			reasons = append(reasons, reason("token file "+tokPath+" was not checked", "check again before applying"))
		case !ts.Exists:
			tokenStep = &Step{Kind: StepToken, Path: tokPath, Action: ActionGenerateToken,
				Detail: "create the token file " + tokPath + " (32 random bytes, base64, mode 0600)"}
			tokenChanged = true
		case ts.Loose:
			tokenStep = &Step{Kind: StepToken, Path: tokPath, Action: ActionTightenToken,
				Detail: "tighten the token file " + tokPath + " to mode 0600, keeping its content"}
		case ts.Empty:
			tokenStep = &Step{Kind: StepToken, Path: tokPath, Action: ActionGenerateToken,
				Detail: "replace the empty token file " + tokPath + " with a new token (32 random bytes, base64, mode 0600)"}
			tokenChanged = true
		case ts.Err != "":
			reasons = append(reasons, reason("token file "+tokPath+" cannot be read ("+ts.Err+")", "fix it or remove it so a new one is made"))
		default:
			tokenKept = "keep the token file " + tokPath
		}
	}

	// Pair.
	var pairStep *Step
	var pairKept string
	if st.Pair.URL != "" && st.Pair.KeySet && st.Pair.Err == "" {
		pairKept = "keep wt-model's LiteLLM pair (" + st.Pair.URL + ")"
	} else {
		lport := st.LiteLLMPlist.Port
		if lport == 0 {
			lport = defaultLiteLLMPort
		}
		url := "http://localhost:" + strconv.Itoa(lport)
		where := ""
		if st.Host.ConfigHome != "" {
			where = " in " + st.Host.ConfigHome + "/wt-model/"
		}
		switch {
		case st.LiteLLMPlist.HasMasterKey:
			pairStep = &Step{Kind: StepPair, Path: st.LiteLLMPlist.Path, Action: ActionPairKeyFromPlist, Value: url,
				Detail: "set wt-model's LiteLLM pair" + where + ": url " + url + ", key LITELLM_MASTER_KEY read from " + st.LiteLLMPlist.Path + " when applied"}
		case strings.TrimSpace(want.MasterKey.Reveal()) != "":
			pairStep = &Step{Kind: StepPair, Action: ActionPairKeyEntered, Value: url,
				Detail: "set wt-model's LiteLLM pair" + where + ": url " + url + ", key as entered (not shown)"}
		default:
			reasons = append(reasons, reason("wt-model's LiteLLM pair is not set and no LITELLM_MASTER_KEY was found in "+orNone(st.LiteLLMPlist.Path),
				"enter the LiteLLM master key, or set up LiteLLM as "+guide14+" describes"))
		}
	}

	if len(reasons) > 0 {
		return Plan{}, &RefusalError{Reasons: reasons}
	}

	want.Listen = listen
	want.TokenFile = tokPath
	want.URL = "http://" + listen
	spec := AgentSpec{Binary: st.Host.Binary, Listen: listen, TokenFile: tokPath, Path: st.Host.Path, Home: st.Host.Home}
	p := Plan{Want: want, Agent: &spec}

	if tokenStep != nil {
		p.Steps = append(p.Steps, *tokenStep)
	} else {
		p.Notes = append(p.Notes, tokenKept)
	}
	if pairStep != nil {
		p.Steps = append(p.Steps, *pairStep)
	} else {
		p.Notes = append(p.Notes, pairKept)
	}

	plistChanged := !(st.Agent.Present && st.Agent.Managed && bytes.Equal(st.Agent.Content, RenderAgentPlist(spec)))
	if plistChanged {
		verb := "write"
		if st.Agent.Present {
			verb = "overwrite"
		}
		p.Steps = append(p.Steps, Step{Kind: StepPlist, Path: st.Agent.PlistPath,
			Detail: verb + " wt's LaunchAgent plist " + st.Agent.PlistPath + ": " + spec.Binary + " serve --listen " + listen +
				" --token-file " + tokPath + ", PATH=" + spec.Path + ", logs " + spec.Home + "/.wt-model.log and " + spec.Home + "/.wt-model.err.log, started at login and kept alive"})
	} else {
		p.Notes = append(p.Notes, "keep the LaunchAgent plist "+st.Agent.PlistPath+" (unchanged)")
	}

	domain := "gui/" + strconv.Itoa(st.Host.UID)
	loaded := st.Agent.Job.Loaded
	started := false
	switch {
	case plistChanged && loaded:
		p.Steps = append(p.Steps,
			Step{Kind: StepBootout, Path: st.Agent.PlistPath, Detail: "unload the running LaunchAgent: launchctl bootout " + domain + "/" + AgentLabel},
			Step{Kind: StepBootstrap, Path: st.Agent.PlistPath, Detail: "load the LaunchAgent: launchctl bootstrap " + domain + " " + st.Agent.PlistPath})
		started = true
	case !loaded:
		p.Steps = append(p.Steps, Step{Kind: StepBootstrap, Path: st.Agent.PlistPath, Detail: "load the LaunchAgent: launchctl bootstrap " + domain + " " + st.Agent.PlistPath})
		started = true
	case pairStep != nil || tokenChanged:
		p.Steps = append(p.Steps, Step{Kind: StepKickstart, Path: st.Agent.PlistPath,
			Detail: "restart the LaunchAgent so it reads the new " + changedWhat(pairStep != nil, tokenChanged) + ": launchctl kickstart -k " + domain + "/" + AgentLabel})
		started = true
	default:
		if !st.Agent.Job.Running {
			p.Notes = append(p.Notes, "the LaunchAgent is loaded but not running; start it from the tab")
		} else {
			p.Notes = append(p.Notes, "keep the running LaunchAgent "+AgentLabel)
		}
	}
	if started {
		p.Steps = append(p.Steps, Step{Kind: StepWait, Value: want.URL, Detail: "wait for wt-model to answer at " + want.URL})
	}
	p.config(st)
	p.envNote(st)
	return p, nil
}

// config appends the config step when the stored table differs from Want,
// else a note that it is kept. It is always the last step.
func (p *Plan) config(st State) {
	t := st.Config.Table
	cur, _ := modelclient.ParseMode(t.Mode)
	if cur == p.Want.Mode && t.URL == p.Want.URL && expandIn(t.TokenFile, st.Host.Home) == p.Want.TokenFile {
		p.Notes = append(p.Notes, "keep [model-server] in "+st.Config.Path+" (already mode = \""+string(p.Want.Mode)+"\")")
		return
	}
	fields := "mode = \"" + string(p.Want.Mode) + "\""
	if p.Want.URL != "" {
		fields += ", url = \"" + p.Want.URL + "\""
	}
	if p.Want.TokenFile != "" {
		fields += ", token_file = \"" + p.Want.TokenFile + "\""
	}
	var cleared []string
	if p.Want.URL == "" && t.URL != "" {
		cleared = append(cleared, "url")
	}
	if p.Want.TokenFile == "" && t.TokenFile != "" {
		cleared = append(cleared, "token_file")
	}
	if len(cleared) > 0 {
		fields += "; clear " + strings.Join(cleared, " and ")
	}
	verb := "write"
	if !st.Config.Exists {
		verb = "create"
	}
	p.Steps = append(p.Steps, Step{Kind: StepConfig, Path: st.Config.Path,
		Detail: verb + " [model-server] in " + st.Config.Path + ": " + fields})
}

// leaveAgent handles a wt LaunchAgent when leaving local: removal when asked
// for and the plist is wt's, else a note that it stays.
func (p *Plan) leaveAgent(st State) {
	a := st.Agent
	if !a.Present {
		return
	}
	if !a.Managed {
		if p.Want.RemoveAgent {
			p.Notes = append(p.Notes, a.PlistPath+" is not marked \"managed by wt\" and is left alone")
		}
		return
	}
	if !p.Want.RemoveAgent {
		state := "left installed"
		if a.Job.Running {
			state += " and running"
		} else if a.Job.Loaded {
			state += " and loaded"
		}
		p.Notes = append(p.Notes, "the LaunchAgent "+a.PlistPath+" is "+state)
		return
	}
	if a.Job.Loaded {
		p.Steps = append(p.Steps, Step{Kind: StepBootout, Path: a.PlistPath,
			Detail: "unload the LaunchAgent: launchctl bootout gui/" + strconv.Itoa(st.Host.UID) + "/" + AgentLabel})
	}
	p.Steps = append(p.Steps, Step{Kind: StepRemovePlist, Path: a.PlistPath,
		Detail: "remove the LaunchAgent plist " + a.PlistPath})
}

// keepLocalSetup notes that leaving local keeps the token file and the pair.
func (p *Plan) keepLocalSetup(st State) {
	tok := st.Agent.TokenFile
	if tok == "" && st.Host.ConfigHome != "" {
		if ts, ok := tokenAt(st, DefaultLocalTokenPath(st.Host.ConfigHome)); ok && ts.Exists {
			tok = ts.Path
		}
	}
	if tok != "" {
		p.Notes = append(p.Notes, "keep the local token file "+tok)
	}
	if st.Pair.URL != "" || st.Pair.KeySet {
		p.Notes = append(p.Notes, "keep wt-model's LiteLLM pair ("+orNone(st.Pair.URL)+")")
	}
}

// envNote says when the environment overrides what is stored.
func (p *Plan) envNote(st State) {
	if st.EnvOverride != "" {
		p.Notes = append(p.Notes, "the environment sets "+st.EnvOverride+", which overrides what is stored here")
	}
}

// Format is the text the user confirms: every step, numbered, then what is
// left alone. Deterministic and ASCII apart from the paths and urls it names.
func (p Plan) Format() string {
	var b strings.Builder
	if len(p.Steps) == 0 {
		b.WriteString("nothing to change for mode " + string(p.Want.Mode) + "\n")
	} else {
		fmt.Fprintf(&b, "set up mode %s in %d %s:\n", p.Want.Mode, len(p.Steps), plural(len(p.Steps), "step", "steps"))
		for i, s := range p.Steps {
			fmt.Fprintf(&b, "  %d. %s\n", i+1, s.Detail)
		}
	}
	if len(p.Notes) > 0 {
		b.WriteString("left alone and notes:\n")
		for _, n := range p.Notes {
			b.WriteString("  - " + n + "\n")
		}
	}
	return b.String()
}

// tokenAt finds the observed description of the token file at path.
func tokenAt(st State, path string) (TokenState, bool) {
	if path == "" {
		return TokenState{}, false
	}
	if st.Token.Path == path {
		return st.Token, true
	}
	for _, ts := range st.TokenFiles {
		if ts.Path == path {
			return ts, true
		}
	}
	return TokenState{}, false
}

// expandIn expands a leading ~ against home; a path it cannot expand is
// returned as given.
func expandIn(path, home string) string {
	if p, err := expandHome(path, home, nil); err == nil {
		return p
	}
	return path
}

// sameEndpoint reports whether two urls name the same endpoint: scheme, host
// and port case-insensitively, path with any trailing slash trimmed. A url
// that does not parse matches nothing.
func sameEndpoint(a, b string) bool {
	ua, ea := url.Parse(strings.TrimSpace(a))
	ub, eb := url.Parse(strings.TrimSpace(b))
	if ea != nil || eb != nil || ua.Host == "" || ub.Host == "" {
		return false
	}
	return strings.EqualFold(ua.Scheme, ub.Scheme) && strings.EqualFold(ua.Host, ub.Host) &&
		strings.TrimRight(ua.Path, "/") == strings.TrimRight(ub.Path, "/")
}

// serverFailure words a failed ping.
func serverFailure(s ServerState) string {
	var what string
	switch s.Kind {
	case KindUnauthorized:
		what = "the server rejected the token"
	case KindNotWtModel:
		what = "the url answers but is not wt-model"
	case KindSkew:
		what = fmt.Sprintf("the server speaks API %d, which this wt does not", s.API)
	case KindSchema:
		what = fmt.Sprintf("the server's catalog schema %d is not one this wt reads", s.Schema)
	default:
		what = "the server did not answer"
	}
	if s.Message != "" {
		what += ": " + s.Message
	}
	return what
}

func changedWhat(pair, token bool) string {
	switch {
	case pair && token:
		return "pair and token"
	case pair:
		return "pair"
	default:
		return "token"
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
