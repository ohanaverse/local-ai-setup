package modelmode

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/urlrule"
)

// RefusalError is a plan that cannot be made. Each reason says what is still
// true and the way out.
type RefusalError struct{ Reasons []string }

func (e *RefusalError) Error() string { return strings.Join(e.Reasons, "\n") }

// Reason builds one refusal reason: the fact, the way out, and that nothing
// was changed.
func Reason(fact, wayOut string) string {
	return fact + "; " + wayOut + "; nothing was changed"
}

// CredentialsReason refuses a url (or listen address) holding an '@'
// anywhere, read as a user:password whatever else it holds: config.toml is
// not a secret file, so wt never stores one there. The text is fixed: it
// echoes no part of what was typed (no host, no user, nothing derived).
func CredentialsReason(what string) string {
	return Reason(what+" contains an @ (read as a user:password); wt does not store credentials in config.toml", "use a token file")
}

// StepKind is what one step does.
type StepKind string

// The two steps the session side writes (the token file and the
// [model-server] table); the admin kinds stay with modelsetup's plan.
const (
	StepToken  StepKind = "token"
	StepConfig StepKind = "config"
)

// StepAction says how a token or pair step gets its value, so Apply does
// not have to work it out again. It is a marker, never the value.
type StepAction string

// The actions of the token steps the session side writes.
const (
	// ActionTightenToken chmods a loose token file to 0600, keeping it.
	ActionTightenToken StepAction = "tighten-token"
	// ActionWritePastedToken writes Plan.Want.PastedToken to a new file.
	ActionWritePastedToken StepAction = "write-pasted-token"
	// ActionReplacePastedToken writes Plan.Want.PastedToken over an existing
	// file; only a plan whose text says "replace" carries it.
	ActionReplacePastedToken StepAction = "replace-pasted-token"
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

// StepLabel is the short ASCII name of a step kind this package knows: the
// token file and the [model-server] write. An admin kind answers ("", false)
// for modelsetup's stepLabel to name itself.
func StepLabel(k StepKind) (string, bool) {
	switch k {
	case StepToken:
		return "token file", true
	case StepConfig:
		return "write [model-server]", true
	}
	return "", false
}

// Desired is what the user asked for: the session half of it (mode, url,
// token file, pasted token, save-unchecked). The admin half — listen, master
// key, agent removal, the LiteLLM action — is modelsetup's AdminWant.
type Desired struct {
	Mode           Mode
	URL, TokenFile string
	PastedToken    Secret
	SaveUnchecked  bool
}

// Plan is the session half of a plan: what was wanted, the steps to get
// there and the notes about what is left alone. The admin half — AdminWant,
// the agent and LiteLLM specs — is modelsetup's Plan.
type Plan struct {
	Want  Desired
	Steps []Step
	Notes []string
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

// plural is "step" or "steps" by count.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// WritesURL reports whether the plan's config step writes a url into
// [model-server] (local and remote do; native clears it). A plan that keeps
// the stored table has no config step and writes nothing.
func (p Plan) WritesURL() bool {
	for _, s := range p.Steps {
		if s.Kind == StepConfig && p.Want.URL != "" {
			return true
		}
	}
	return false
}

// Preflight refuses what no mode can plan around, and fills the request in:
// the parsed mode, and each secret cleared outside the mode that uses it
// (PastedToken is cleared here; MasterKey and LiteLLM are modelsetup's).
func Preflight(st State, want Desired) (Desired, error) {
	if st.Config.ParseErr != "" {
		return Desired{}, &RefusalError{Reasons: []string{Reason(
			"config.toml at "+st.Config.Path+" has a TOML syntax error ("+st.Config.ParseErr+") and is never overwritten",
			"fix it by hand, then re-read")}}
	}
	if st.Config.TableErr != "" {
		// The config step re-reads the whole file and refuses what it cannot
		// decode, so a plan would run every earlier step (token, plist,
		// launchd) and then fail on the last one.
		return Desired{}, &RefusalError{Reasons: []string{Reason(
			"[model-server] in "+st.Config.Path+" cannot be read ("+st.Config.TableErr+") and is never overwritten",
			"fix it by hand, then re-read")}}
	}
	mode, ok := ParseMode(string(want.Mode))
	if !ok {
		return Desired{}, &RefusalError{Reasons: []string{Reason(
			"mode "+strconv.Quote(string(want.Mode))+" is not native, local or remote",
			"choose one of native, local or remote")}}
	}
	want.Mode = mode
	// Each secret stays only in the mode that uses it.
	if mode != ModeRemote {
		want.PastedToken = ""
	}
	return want, nil
}

// Postflight refuses a plan whose config step would fail on a file its writer
// cannot decode (a key outside [model-server], RestErr): the config step is
// last, so such a file would fail it only after every machine change before
// it. A plan that leaves config.toml alone reaches no writer and is not
// refused.
func Postflight(st State, p Plan) error {
	if _, writes := findStep(p, StepConfig); writes && st.Config.RestErr != "" {
		return &RefusalError{Reasons: []string{Reason(
			"config.toml at "+st.Config.Path+" has a key that cannot be read ("+st.Config.RestErr+") and is never overwritten",
			"fix it by hand, then re-read")}}
	}
	return nil
}

// findStep finds the first step of kind k.
func findStep(p Plan, k StepKind) (Step, bool) {
	for _, s := range p.Steps {
		if s.Kind == k {
			return s, true
		}
	}
	return Step{}, false
}

// PlanHooks are a caller's hooks into a plan this package cannot word on its
// own. LeavingLocal runs where the leaving-local note would go (after a
// remote plan's token step, before the config step); when set it replaces the
// default note that wt-model's LaunchAgent is untouched, so modelsetup can
// plan the LaunchAgent and the kept local files without this package knowing
// they exist.
type PlanHooks struct {
	LeavingLocal func(p *Plan)
}

// BuildPlan plans without hooks: native, remote or the local pointer.
func BuildPlan(st State, want Desired) (Plan, error) {
	return BuildPlanWith(st, want, PlanHooks{})
}

// BuildPlanWith runs Preflight, the mode's plan and Postflight.
func BuildPlanWith(st State, want Desired, h PlanHooks) (Plan, error) {
	var err error
	want, err = Preflight(st, want)
	if err != nil {
		return Plan{}, err
	}
	var p Plan
	switch want.Mode {
	case ModeLocal:
		p, err = planLocalPointer(st, want)
	case ModeRemote:
		p, err = planRemote(st, want, h)
	default:
		p, err = planNative(st, want, h)
	}
	if err != nil {
		return Plan{}, err
	}
	if err := Postflight(st, p); err != nil {
		return Plan{}, err
	}
	return p, nil
}

// planNative stores only the mode.
func planNative(st State, want Desired, h PlanHooks) (Plan, error) {
	want.URL, want.TokenFile = "", ""
	p := Plan{Want: want}
	leavingLocal(&p, st, h)
	p.AddConfigStep(st)
	p.AddEnvNote(st)
	return p, nil
}

// leavingLocal runs the caller's leaving-local hook or, without one, notes
// the default: a session neither sets up nor removes wt-model's LaunchAgent.
// The note is L1, keyed on the local check carried in st.LocalServer (state
// built without one gets the short note): an answering local server is about
// to be unused, and the next step that resolves it goes with it.
func leavingLocal(p *Plan, st State, h PlanHooks) {
	if h.LeavingLocal != nil {
		h.LeavingLocal(p)
		return
	}
	if st.Resolution.Mode != ModeLocal {
		return
	}
	if l := st.LocalServer; l.Checked && l.OK {
		p.Notes = append(p.Notes, "wt-model's LaunchAgent is untouched; the local server answers at "+l.URL+" and will be unused. next: stop it with x on `wt-model setup`, or apply native there to remove the LaunchAgent")
		return
	}
	p.Notes = append(p.Notes, "wt-model's LaunchAgent is untouched; remove it with `wt-model setup`")
}

// planRemote writes a pasted token if any, then the table.
func planRemote(st State, want Desired, h PlanHooks) (Plan, error) {
	var reasons []string
	want.URL = strings.TrimSpace(want.URL)
	if urlrule.HasUserinfo(want.URL) {
		// Alone: every other reason may quote the url.
		return Plan{}, &RefusalError{Reasons: []string{CredentialsReason("the url")}}
	}
	if want.URL == "" {
		reasons = append(reasons, Reason("remote mode has no url", "enter the model server's url"))
	}
	var tokenStep *Step
	switch {
	case strings.TrimSpace(want.PastedToken.Reveal()) != "":
		if st.Host.ConfigHome == "" {
			reasons = append(reasons, Reason("the config directory is not known, so the pasted token has nowhere to go", "set HOME or XDG_CONFIG_HOME"))
			break
		}
		path := RemoteTokenPath(st.Host.ConfigHome)
		want.TokenFile = path
		ts, _ := TokenAt(st, path)
		if ts.Dangling {
			// A link that does not resolve: apply would refuse it after the
			// confirmation, so refuse it now.
			reasons = append(reasons, Reason("token path "+path+" cannot be replaced ("+ts.Err+")", "remove or repair the link, then plan again"))
			break
		}
		s := Step{Kind: StepToken, Path: path, Action: ActionWritePastedToken,
			Detail: "write the pasted token to " + path + " (mode 0600)"}
		if ts.Exists {
			s.Action = ActionReplacePastedToken
			s.Detail = "replace " + path + " with the pasted token (mode 0600; the old token there is lost)"
		}
		tokenStep = &s
	case strings.TrimSpace(want.TokenFile) != "":
		path := ExpandIn(strings.TrimSpace(want.TokenFile), st.Host.Home)
		want.TokenFile = path
		ts, seen := TokenAt(st, path)
		switch {
		case !seen:
			reasons = append(reasons, Reason("token file "+path+" was not checked", "check again before applying"))
		case !ts.Exists:
			reasons = append(reasons, Reason("token file "+path+" does not exist", "enter the path of an existing token file, or paste the token"))
		case ts.Loose:
			reasons = append(reasons, Reason("token file "+path+" is readable by others", "run chmod 600 "+path))
		case ts.Empty:
			reasons = append(reasons, Reason("token file "+path+" is empty", "put the server's token in it, or paste the token"))
		case ts.Err != "":
			reasons = append(reasons, Reason("token file "+path+" cannot be read ("+ts.Err+")", "fix it, or paste the token"))
		}
	default:
		reasons = append(reasons, Reason("remote mode has no token", "enter a token file path or paste the token"))
	}
	// A check of any other endpoint (the local server, an old remote) says
	// nothing about this one: it reads as not checked.
	server := st.Server
	if server.Checked && (want.URL == "" || !sameEndpoint(server.URL, want.URL)) {
		server = ServerState{}
	}
	if server.Checked && !server.OK && !want.SaveUnchecked {
		reasons = append(reasons, Reason("the check of "+want.URL+" failed: "+ServerFailure(server),
			"fix the url or token and check again, or choose save anyway (the server may be off)"))
	}
	if len(reasons) > 0 {
		return Plan{}, &RefusalError{Reasons: reasons}
	}
	p := Plan{Want: want}
	if tokenStep != nil {
		p.Steps = append(p.Steps, *tokenStep)
	}
	leavingLocal(&p, st, h)
	p.AddConfigStep(st)
	switch {
	case server.Checked && !server.OK:
		p.Notes = append(p.Notes, "the server did not answer the check ("+ServerFailure(server)+"); the config is saved anyway")
	case !server.Checked:
		p.Notes = append(p.Notes, "the server at "+want.URL+" was not checked")
	}
	p.AddEnvNote(st)
	return p, nil
}

// planLocalPointer plans local mode from the session side: it stores only
// the mode and points at wt-model's own setup for everything else on this
// machine. It keeps the stored url and token file only when the stored table
// is already local (the stored table's mode, not the resolution: an
// environment override does not count) — a remote url must never become
// local's url, so from any other stored mode the stored url and token file
// are ignored and overwritten, not refused. The entered url and token file
// are never used: the pointer describes this machine.
func planLocalPointer(st State, want Desired) (Plan, error) {
	if r := LocalTLSRefusal(st); r != "" {
		return Plan{}, &RefusalError{Reasons: []string{r}}
	}
	t := st.Config.Table
	m, ok := ParseMode(t.Mode)
	storedLocal := ok && m == ModeLocal
	want.URL, want.TokenFile = "", ""
	needDefaultToken := false
	tlsOn := st.LocalTLS.On
	if storedLocal {
		if urlrule.HasUserinfo(t.URL) {
			// Alone: this url would be kept, and config.toml is not a secret
			// file (Observe stores urlrule.Hidden whole).
			return Plan{}, &RefusalError{Reasons: []string{CredentialsReason("the stored url")}}
		}
		switch t.URL {
		case "", LocalURL(DefaultListen, !tlsOn):
			// No url, or the default one in the other scheme: it follows
			// serve's TLS (a hand-written url is kept as written).
			want.URL = LocalURL(DefaultListen, tlsOn)
		default:
			want.URL = t.URL
		}
		if t.TokenFile != "" {
			want.TokenFile = ExpandIn(t.TokenFile, st.Host.Home)
		} else {
			needDefaultToken = true
		}
	} else {
		want.URL = LocalURL(DefaultListen, tlsOn)
		needDefaultToken = true
	}
	if needDefaultToken {
		if st.Host.ConfigHome == "" {
			return Plan{}, &RefusalError{Reasons: []string{Reason(
				"the config directory is not known, so the local token file has no default path",
				"set HOME or XDG_CONFIG_HOME")}}
		}
		want.TokenFile = DefaultLocalTokenPath(st.Host.ConfigHome)
	}
	p := Plan{Want: want}
	p.AddConfigStep(st) // an unchanged table gives its existing "keep [model-server] …" note
	p.Notes = append(p.Notes, "run `wt-model setup` to set up wt-model on this machine")
	p.AddLocalTLSNotes(st)
	p.AddEnvNote(st)
	return p, nil
}

// AddConfigStep appends the config step when the stored table differs from
// Want, else a note that it is kept. It is always the last step.
func (p *Plan) AddConfigStep(st State) {
	t := st.Config.Table
	cur, _ := ParseMode(t.Mode)
	if cur == p.Want.Mode && t.URL == p.Want.URL && ExpandIn(t.TokenFile, st.Host.Home) == p.Want.TokenFile {
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

// AddEnvNote says when the environment overrides what is stored.
func (p *Plan) AddEnvNote(st State) {
	if st.EnvOverride != "" {
		p.Notes = append(p.Notes, "the environment sets "+st.EnvOverride+", which overrides what is stored here")
	}
}

// TokenAt finds the observed description of the token file at path.
func TokenAt(st State, path string) (TokenState, bool) {
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

// ServerFailure words a failed ping.
func ServerFailure(s ServerState) string {
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
