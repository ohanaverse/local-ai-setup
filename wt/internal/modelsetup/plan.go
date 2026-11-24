package modelsetup

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelmode"
)

// Secret holds a secret the user typed (a pasted token, a master key); every
// printed or marshalled form is "[redacted]". It lives in internal/modelmode;
// this name is modelsetup's alias.
type Secret = modelmode.Secret

// AdminWant is the admin half of a Desired: what the setup side decides that
// a session never chooses. MasterKey (local, used only when the pair is unset
// and the LiteLLM plist has no key) is a Secret: BuildPlan keeps it only in a
// local plan's Plan.Admin, for Apply, and no step, note, Format text or
// refusal holds it (PastedToken, the session half's secret, stays in
// Plan.Want). LiteLLM is what a local plan does to the LiteLLM LaunchAgent
// (nothing by default).
type AdminWant struct {
	Listen      string
	MasterKey   Secret
	RemoveAgent bool
	LiteLLM     LiteLLMAction
}

// Desired is what the user asked for: the session half (mode, url, token
// file, pasted token, save-unchecked) is modelmode's; the admin half is
// AdminWant.
type Desired struct {
	modelmode.Desired
	AdminWant
}

// LiteLLMAction is what a local plan does to the LiteLLM LaunchAgent. wt never
// installs LiteLLM: it creates a minimal plist only when none exists and
// LiteLLM is installed, and otherwise changes only LITELLM_MASTER_KEY.
type LiteLLMAction string

const (
	// LiteLLMNone leaves the LiteLLM plist alone; it plans exactly as
	// LiteLLMUseExistingKey.
	LiteLLMNone LiteLLMAction = ""
	// LiteLLMUseExistingKey reads the plist's master key into wt-model's pair
	// when the pair is unset; the plist is never edited.
	LiteLLMUseExistingKey LiteLLMAction = "use-existing-key"
	// LiteLLMCreate writes a minimal LiteLLM plist with new master and salt
	// keys, loads it and pairs wt-model with the new master key.
	LiteLLMCreate LiteLLMAction = "create"
	// LiteLLMRotateKey replaces LITELLM_MASTER_KEY in the existing plist
	// (after a backup), pairs wt-model with it and restarts both.
	LiteLLMRotateKey LiteLLMAction = "rotate-key"
	// LiteLLMTighten changes only the LiteLLM plist's mode, to 0600, and
	// only when it is looser; the plan is that one chmod step.
	LiteLLMTighten LiteLLMAction = "tighten"
)

// StepKind is what one step does. It is modelmode.StepKind.
type StepKind = modelmode.StepKind

// The steps, in the order a local plan runs them; StepRemovePlist belongs to
// the native and remote plans' optional agent removal. StepToken and
// StepConfig — the two kinds the session side writes — are modelmode's.
const (
	StepPair        StepKind = "pair"
	StepPlist       StepKind = "plist"
	StepBootout     StepKind = "bootout"
	StepBootstrap   StepKind = "bootstrap"
	StepKickstart   StepKind = "kickstart"
	StepWait        StepKind = "wait"
	StepRemovePlist StepKind = "remove-plist"

	// The LiteLLM steps of a create or rotate plan. A create plan writes the
	// plist (StepLiteLLMPlist) and loads it with StepBootstrap and StepWait
	// (ActionWaitLiteLLM); a rotation backs it up, edits the key, tightens
	// the mode and restarts LiteLLM.
	StepLiteLLMPlist     StepKind = "litellm-plist"
	StepBackup           StepKind = "backup"
	StepMasterKey        StepKind = "master-key"
	StepChmod            StepKind = "chmod"
	StepKickstartLiteLLM StepKind = "kickstart-litellm"
)

// The two step kinds the session side writes, aliased from modelmode.
const (
	StepToken  = modelmode.StepToken
	StepConfig = modelmode.StepConfig
)

// StepAction says how a token or pair step gets its value, so Apply does
// not have to work it out again. It is a marker, never the value. It is
// modelmode.StepAction.
type StepAction = modelmode.StepAction

// The actions of pair steps; the three token actions (tighten, write pasted,
// replace pasted) are modelmode's.
const (
	// ActionGenerateToken writes 32 random bytes, base64, mode 0600.
	ActionGenerateToken StepAction = "generate-token"
	// ActionPairKeyFromPlist reads LITELLM_MASTER_KEY from the LiteLLM plist
	// at Step.Path when applied.
	ActionPairKeyFromPlist StepAction = "pair-key-from-litellm-plist"
	// ActionPairKeyEntered uses Plan.Admin.MasterKey.
	ActionPairKeyEntered StepAction = "pair-key-entered"
	// ActionPairKeyNew uses the master key an earlier step of the same Apply
	// generated and wrote to the LiteLLM plist (create or rotate); the key is
	// held by that Apply alone, never by the plan.
	ActionPairKeyNew StepAction = "pair-key-new"
	// ActionWaitLiteLLM marks a wait step that waits for LiteLLM (its
	// /health/liveliness under Step.Value) rather than wt-model.
	ActionWaitLiteLLM StepAction = "wait-litellm"
)

// The three token actions, aliased from modelmode.
const (
	ActionTightenToken       = modelmode.ActionTightenToken
	ActionWritePastedToken   = modelmode.ActionWritePastedToken
	ActionReplacePastedToken = modelmode.ActionReplacePastedToken
)

// Step is one change. Path is the file or plist the step acts on; Value is
// a non-secret operand (the pair's url, the url waited on); Detail is the
// line the user confirms. It is modelmode.Step.
type Step = modelmode.Step

// Plan is what BuildPlan decides. The session half (Want with Mode, URL and
// TokenFile as they will be written, the Steps, and the Notes on what is left
// alone) is modelmode's; Admin is the admin half of the request (Listen as it
// will be written); Agent is what a local plan's plist step renders (nil for
// native and remote). LiteLLM is what a create plan's StepLiteLLMPlist
// renders, without its keys: Apply generates them when it writes the file, so
// no plan ever holds one (nil for any plan that does not create).
type Plan struct {
	modelmode.Plan
	Admin   AdminWant
	Agent   *AgentSpec
	LiteLLM *LiteLLMSpec
	// LockPath is the cross-process lock Apply holds for the whole run
	// (<config home>/wt-model/setup.lock), so two sessions' applies never
	// interleave (#642). Empty when the config home is unknown.
	LockPath string
}

// SetupLockPath is where an Apply's lock lives for a config home.
func SetupLockPath(configHome string) string {
	if configHome == "" {
		return ""
	}
	return filepath.Join(configHome, "wt-model", "setup.lock")
}

// RefusalError is a plan that cannot be made. Each reason says what is still
// true and the way out. It is modelmode.RefusalError.
type RefusalError = modelmode.RefusalError

// reason builds one refusal reason: the fact, the way out, and that nothing
// was changed. It is modelmode.Reason (this package's call sites keep the
// short name).
func reason(fact, wayOut string) string { return modelmode.Reason(fact, wayOut) }

// guide14 is the LiteLLM guide a missing master key points at.
const guide14 = "docs/guides/14-litellm-install-and-configuration.md"

// DefaultLiteLLMPort is the proxy's port when its plist names none.
const DefaultLiteLLMPort = 4000

// BuildPlan decides the steps that take the machine from st to want. It is
// pure: everything it needs is in st, and it reads no clock and no file.
func BuildPlan(st State, want Desired) (Plan, error) {
	p, err := buildPlan(st, want)
	if err != nil {
		return Plan{}, err
	}
	p.LockPath = SetupLockPath(st.Host.ConfigHome)
	return p, nil
}

func buildPlan(st State, want Desired) (Plan, error) {
	d, err := modelmode.Preflight(st.State, want.Desired)
	if err != nil {
		return Plan{}, err
	}
	want.Desired = d
	// Each secret stays only in the mode that uses it.
	if want.Mode != modelmode.ModeLocal {
		want.MasterKey = ""
		want.LiteLLM = LiteLLMNone
	}
	var p Plan
	switch want.Mode {
	case modelmode.ModeLocal:
		// The tighten plan is one chmod: no Postflight, no restart note,
		// as before.
		if want.LiteLLM == LiteLLMTighten {
			return planTightenLiteLLM(st, want)
		}
		p, err = planLocal(st, want)
		if err != nil {
			return Plan{}, err
		}
		// Same failure as TableErr above, from a key outside [model-server]
		// (RestErr): the config step is last, so a file its writer cannot
		// decode would fail it only after every machine change before it.
		// BuildPlanWith already ran this for native and remote.
		if err := modelmode.Postflight(st.State, p.Plan); err != nil {
			return Plan{}, err
		}
	default:
		// Preflight already ran (idempotent: the mode is parsed, PastedToken
		// cleared), and BuildPlanWith repeats it; Postflight runs there.
		mp, err := modelmode.BuildPlanWith(st.State, want.Desired, modelmode.PlanHooks{
			LeavingLocal: func(mp *modelmode.Plan) {
				leaveAgent(mp, st, want.AdminWant)
				keepLocalSetup(mp, st)
			},
		})
		if err != nil {
			return Plan{}, err
		}
		p = Plan{Plan: mp, Admin: want.AdminWant}
		p.Admin.Listen = ""
	}
	p.restartNote()
	return p, nil
}

// restartNote says, once, that a step that restarts or unloads a running job
// ends its requests in flight at once (launchctl kickstart -k, bootout):
// unlike `wt-model litellm restart`, nothing waits for them to finish.
func (p *Plan) restartNote() {
	for _, s := range p.Steps {
		if s.Kind == StepKickstart || s.Kind == StepKickstartLiteLLM || s.Kind == StepBootout {
			p.Notes = append(p.Notes, "a restart or unload here (launchctl kickstart -k, bootout) ends the requests in flight at once; unlike `wt-model litellm restart`, nothing waits for them to finish")
			return
		}
	}
}

// planLocal sets up the token, the pair, the LaunchAgent and the table, in
// that order.
func planLocal(st State, want Desired) (Plan, error) {
	var reasons []string
	if !st.Platform.OK {
		reasons = append(reasons, reason("local mode cannot run here: "+st.Platform.Reason, "choose native or remote"))
	}
	if st.Tools.Binary == "" {
		why := st.Tools.BinaryErr
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
	case strings.Contains(listen, "@"):
		reasons = append(reasons, modelmode.CredentialsReason("the listen address"))
	case err != nil || perr != nil || port < 1 || port > 65535:
		reasons = append(reasons, reason("listen address "+strconv.Quote(listen)+" is not host:port", "enter one like "+DefaultListen))
	case host != "127.0.0.1" && host != "localhost" && host != "::1":
		reasons = append(reasons, reason("listen address "+listen+" is not loopback", "use 127.0.0.1:<port>"))
	case st.Listener.Port != port:
		reasons = append(reasons, reason("port "+portText+" was not checked for a listener", "check again before applying"))
	case st.Listener.PID != 0 && !st.Listener.Ours:
		reasons = append(reasons, reason(fmt.Sprintf("port %d is in use by pid %d, which is not wt's LaunchAgent", port, st.Listener.PID),
			"stop that process or choose another port"))
	}

	// TLS settings that cannot be read refuse (never a silent http://).
	if r := modelmode.LocalTLSRefusal(st.State); r != "" {
		reasons = append(reasons, r)
	}

	// The plist path must be wt's or free. A key rotate writes only the
	// LiteLLM plist and the pair, so it leaves a loaded agent plist that is not
	// wt's as written and only restarts it (#708); an agent that is not loaded
	// would have to be bootstrapped from a file wt does not own, so that and
	// every other plan still refuses.
	keepUnmanaged := want.LiteLLM == LiteLLMRotateKey && st.Agent.Present && !st.Agent.Managed && st.Agent.Job.Loaded
	if st.Agent.Present && !st.Agent.Managed && !keepUnmanaged {
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
	tokPath = modelmode.ExpandIn(tokPath, st.Host.Home)
	// A kept hand-written plist is never rewritten, so the url and token file
	// the plan stores for [model-server] must be the ones the kept server
	// runs: wt-model serve requires --listen and --token-file, so they are in
	// the plist wt leaves as written, and the restart the plan makes changes
	// neither. A plist wt cannot read as arguments stays refused (#708).
	if keepUnmanaged {
		switch {
		case st.Agent.Err != "":
			reasons = append(reasons, reason("the LaunchAgent plist "+st.Agent.PlistPath+" cannot be read as an XML plist ("+st.Agent.Err+"), so wt cannot tell what --listen and --token-file the server it would restart runs, and wt will not store [model-server] for it",
				"fix the plist, or move it aside or remove it yourself, or choose native or remote"))
		case st.Agent.Listen == "" || st.Agent.TokenFile == "":
			reasons = append(reasons, reason("the LaunchAgent plist "+st.Agent.PlistPath+" does not give wt-model serve both --listen and --token-file, so wt cannot tell what the server it would restart runs, and wt will not store [model-server] for it",
				"fix the plist, or move it aside or remove it yourself, or choose native or remote"))
		case portOf(st.Agent.Listen) != port:
			reasons = append(reasons, reason("the LaunchAgent plist "+st.Agent.PlistPath+" runs wt-model --listen "+st.Agent.Listen+", while the plan would store url "+modelmode.LocalURL(listen, st.LocalTLS.On)+"; wt leaves that plist as written, so the restart does not change its listen",
				"enter "+st.Agent.Listen+" in the listen field, or move the plist aside or remove it yourself, or choose native or remote"))
		case modelmode.ExpandIn(st.Agent.TokenFile, st.Host.Home) != tokPath:
			reasons = append(reasons, reason("the LaunchAgent plist "+st.Agent.PlistPath+" runs wt-model --token-file "+st.Agent.TokenFile+", while the plan would store token file "+tokPath+"; wt leaves that plist as written, so the restart does not make the server read the one it stores",
				"enter "+st.Agent.TokenFile+" in the token field, or move the plist aside or remove it yourself, or choose native or remote"))
		}
	}
	var tokenStep *Step
	tokenChanged := false
	var tokenKept string
	if tokPath != "" {
		ts, seen := modelmode.TokenAt(st.State, tokPath)
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

	// LiteLLM: create or rotate decide the pair themselves.
	var lsteps []Step
	var lspec *LiteLLMSpec
	var lnotes []string
	switch want.LiteLLM {
	case LiteLLMNone, LiteLLMUseExistingKey:
	case LiteLLMCreate:
		var rs []string
		lspec, lsteps, lnotes, rs = planLiteLLMCreate(st)
		reasons = append(reasons, rs...)
	case LiteLLMRotateKey:
		var rs []string
		lsteps, lnotes, rs = planLiteLLMRotate(st)
		reasons = append(reasons, rs...)
	default:
		reasons = append(reasons, reason("LiteLLM action "+strconv.Quote(string(want.LiteLLM))+" is not one wt knows",
			"choose the plist's master key, a new master key, or create the plist"))
	}
	newKey := want.LiteLLM == LiteLLMCreate || want.LiteLLM == LiteLLMRotateKey
	if newKey {
		want.MasterKey = "" // a generated key replaces any entered one
	}

	// Pair.
	var pairStep *Step
	var pairKept string
	if newKey {
		url := liteLLMURL(st, want.LiteLLM)
		pairStep = &Step{Kind: StepPair, Action: ActionPairKeyNew, Value: url,
			Detail: "set wt-model's LiteLLM pair" + pairWhere(st) + ": url " + url + ", key the new LITELLM_MASTER_KEY (not shown)"}
	} else if st.Pair.URL != "" && st.Pair.KeySet && st.Pair.Err == "" &&
		!(want.LiteLLM == LiteLLMUseExistingKey && st.LiteLLMPlist.HasMasterKey) {
		// Asking for the plist's key re-sets even a set pair (it may hold a
		// stale key); every other request keeps it.
		pairKept = "keep wt-model's LiteLLM pair (" + modelmode.ShownURL(st.Pair.URL) + ")"
	} else {
		url := liteLLMURL(st, want.LiteLLM)
		where := pairWhere(st)
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
	// https:// only while serve will speak TLS (decision 13).
	want.URL = modelmode.LocalURL(listen, st.LocalTLS.On)
	spec := AgentSpec{Binary: st.Tools.Binary, Listen: listen, TokenFile: tokPath, Path: st.Tools.Path, Home: st.Host.Home}
	p := Plan{Plan: modelmode.Plan{Want: want.Desired}, Admin: want.AdminWant, Agent: &spec, LiteLLM: lspec}
	pairChanged := false

	if tokenStep != nil {
		p.Steps = append(p.Steps, *tokenStep)
	} else {
		p.Notes = append(p.Notes, tokenKept)
	}
	// The LiteLLM plist changes come before the pair, so the pair is set only
	// once the plist holds the key it is given (rotate: then LiteLLM restarts
	// right after the pair, before wt-model).
	if want.LiteLLM == LiteLLMRotateKey {
		n := len(lsteps) - 2 // the trailing LiteLLM restart and wait follow the pair
		p.Steps = append(p.Steps, lsteps[:n]...)
		p.Steps = append(p.Steps, *pairStep)
		p.Steps = append(p.Steps, lsteps[n:]...)
		pairStep = nil
		pairChanged = true
	} else {
		p.Steps = append(p.Steps, lsteps...)
	}
	if pairStep != nil {
		p.Steps = append(p.Steps, *pairStep)
		pairChanged = true
	} else if !pairChanged {
		p.Notes = append(p.Notes, pairKept)
	}

	plistChanged := !keepUnmanaged && !(st.Agent.Present && st.Agent.Managed && bytes.Equal(st.Agent.Content, RenderAgentPlist(spec)))
	if keepUnmanaged {
		p.Notes = append(p.Notes, "keep the LaunchAgent plist "+st.Agent.PlistPath+" as written: it is not marked \"managed by wt\" and a key rotate does not write it")
	} else if plistChanged {
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

	domain := "gui/" + strconv.Itoa(st.Tools.UID)
	loaded := st.Agent.Job.Loaded
	// serve reads [gateway] only at start: when the url written changes
	// scheme from the stored local one, or the running serve (its record,
	// when it names the running agent's pid) speaks the other scheme, a
	// loaded agent is reloaded so the url and the server converge. A kept
	// hand-written agent converges through the kickstart instead: a
	// bootout plus bootstrap reads the same file, and kickstart -k restarts
	// it just as finally.
	schemeChanged := schemeChanges(st, want.URL)
	started := false
	switch {
	case !keepUnmanaged && (plistChanged || schemeChanged) && loaded:
		p.Steps = append(p.Steps,
			Step{Kind: StepBootout, Path: st.Agent.PlistPath, Detail: "unload the running LaunchAgent: launchctl bootout " + domain + "/" + AgentLabel},
			Step{Kind: StepBootstrap, Path: st.Agent.PlistPath, Detail: "load the LaunchAgent: launchctl bootstrap " + domain + " " + st.Agent.PlistPath})
		started = true
	case !loaded:
		p.Steps = append(p.Steps, Step{Kind: StepBootstrap, Path: st.Agent.PlistPath, Detail: "load the LaunchAgent: launchctl bootstrap " + domain + " " + st.Agent.PlistPath})
		started = true
	case pairChanged || tokenChanged:
		p.Steps = append(p.Steps, Step{Kind: StepKickstart, Path: st.Agent.PlistPath,
			Detail: "restart the LaunchAgent so it reads the new " + changedWhat(pairChanged, tokenChanged) + ": launchctl kickstart -k " + domain + "/" + AgentLabel})
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
	if schemeChanged && loaded && !plistChanged {
		p.Notes = append(p.Notes, "wt-model serve reads its TLS settings only at start, so it is reloaded to speak the url's scheme")
	}
	p.AddConfigStep(st.State)
	p.Notes = append(p.Notes, lnotes...)
	p.AddLocalTLSNotes(st.State)
	p.AddEnvNote(st.State)
	return p, nil
}

// schemeChanges reports whether a local plan writing url changes the scheme
// a session uses or serve speaks: the stored local url's scheme differs from
// url's, or the running serve's record (only when it names the running
// agent's pid, never a stale one) says it speaks the other scheme.
func schemeChanges(st State, url string) bool {
	https := strings.HasPrefix(url, "https://")
	t := st.Config.Table
	if m, ok := modelmode.ParseMode(t.Mode); ok && m == modelmode.ModeLocal && t.URL != "" &&
		strings.HasPrefix(t.URL, "https://") != https {
		return true
	}
	if s := st.Serve; s.Seen && st.Agent.Job.Running && s.PID == st.Agent.Job.PID && s.TLS != https {
		return true
	}
	return false
}

// leaveAgent handles a wt LaunchAgent when leaving local: removal when asked
// for and the plist is wt's, else a note that it stays. It is the LeavingLocal
// hook modelmode's native and remote plans run; a has the admin half of the
// request (a modelmode plan carries no AdminWant).
func leaveAgent(p *modelmode.Plan, st State, a AdminWant) {
	ag := st.Agent
	if !ag.Present {
		return
	}
	if !ag.Managed {
		if a.RemoveAgent {
			p.Notes = append(p.Notes, ag.PlistPath+" is not marked \"managed by wt\" and is left alone")
		}
		return
	}
	if !a.RemoveAgent {
		state := "left installed"
		if ag.Job.Running {
			state += " and running"
		} else if ag.Job.Loaded {
			state += " and loaded"
		}
		p.Notes = append(p.Notes, "the LaunchAgent "+ag.PlistPath+" is "+state)
		return
	}
	if ag.Job.Loaded {
		p.Steps = append(p.Steps, Step{Kind: StepBootout, Path: ag.PlistPath,
			Detail: "unload the LaunchAgent: launchctl bootout gui/" + strconv.Itoa(st.Tools.UID) + "/" + AgentLabel})
	}
	p.Steps = append(p.Steps, Step{Kind: StepRemovePlist, Path: ag.PlistPath,
		Detail: "remove the LaunchAgent plist " + ag.PlistPath})
}

// keepLocalSetup notes that leaving local keeps the token file and the pair.
func keepLocalSetup(p *modelmode.Plan, st State) {
	tok := st.Agent.TokenFile
	if tok == "" && st.Host.ConfigHome != "" {
		if ts, ok := modelmode.TokenAt(st.State, DefaultLocalTokenPath(st.Host.ConfigHome)); ok && ts.Exists {
			tok = ts.Path
		}
	}
	if tok != "" {
		p.Notes = append(p.Notes, "keep the local token file "+tok)
	}
	if st.Pair.URL != "" || st.Pair.KeySet {
		p.Notes = append(p.Notes, "keep wt-model's LiteLLM pair ("+orNone(modelmode.ShownURL(st.Pair.URL))+")")
	}
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

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// liteLLMURL is the url wt-model's pair names for LiteLLM: the port a create
// plan writes (DefaultLiteLLMPort), else the plist's --port, else the default.
func liteLLMURL(st State, a LiteLLMAction) string {
	port := st.LiteLLMPlist.Port
	if a == LiteLLMCreate || port == 0 {
		port = DefaultLiteLLMPort
	}
	return "http://localhost:" + strconv.Itoa(port)
}

// pairWhere says where the pair is written, for a step's text.
func pairWhere(st State) string {
	if st.Host.ConfigHome == "" {
		return ""
	}
	return " in " + st.Host.ConfigHome + "/wt-model/"
}

// planLiteLLMCreate plans a minimal LiteLLM LaunchAgent: only when no plist
// exists and LiteLLM is installed (a litellm binary and config.yaml), since wt
// never installs LiteLLM. The spec has no keys: Apply generates them as it
// writes the file. The steps are the plist, its load and the wait for
// LiteLLM; the caller puts the pair after them.
func planLiteLLMCreate(st State) (*LiteLLMSpec, []Step, []string, []string) {
	lp := st.LiteLLMPlist
	inst := st.LiteLLMInstall
	var reasons []string
	switch {
	case lp.Path == "":
		reasons = append(reasons, reason("the LiteLLM plist path is not known", "set HOME"))
	case lp.Present:
		reasons = append(reasons, reason(lp.Path+" already exists, and wt creates a LiteLLM plist only when none exists",
			"use the plist's master key or generate a new one for it instead ("+guide14+")"))
	}
	if inst.Binary == "" {
		reasons = append(reasons, reason("no litellm binary was found (in ~/.local/bin, /opt/homebrew/bin, /usr/local/bin or on PATH), and wt never installs LiteLLM",
			"install it as "+guide14+" describes, then check again"))
	}
	switch {
	case !inst.ListenerChecked || inst.Port != DefaultLiteLLMPort:
		reasons = append(reasons, reason(fmt.Sprintf("port %d was not checked for a listener", DefaultLiteLLMPort), "check again before applying"))
	case inst.ListenerPID != 0:
		reasons = append(reasons, reason(fmt.Sprintf("port %d is in use by pid %d (a LiteLLM run by hand or under another label?), so a new LaunchAgent could not listen there",
			DefaultLiteLLMPort, inst.ListenerPID),
			"stop that process, or keep it and enter its master key instead ("+guide14+")"))
	}
	if !inst.ConfigExists {
		reasons = append(reasons, reason("LiteLLM's config.yaml "+orNone(inst.ConfigPath)+" does not exist, and wt never writes a first one",
			"create it as "+guide14+" describes, then check again"))
	}
	if len(reasons) > 0 {
		return nil, nil, nil, reasons
	}
	spec := LiteLLMSpec{Binary: inst.Binary, ConfigPath: inst.ConfigPath, Path: st.Tools.Path, Home: st.Host.Home, Port: DefaultLiteLLMPort}
	if err := checkLiteLLMSpec(spec); err != nil {
		var re *RefusalError
		if errors.As(err, &re) {
			return nil, nil, nil, re.Reasons
		}
		return nil, nil, nil, []string{reason(err.Error(), "fix it, then check again")}
	}
	url := liteLLMURL(st, LiteLLMCreate)
	domain := "gui/" + strconv.Itoa(st.Tools.UID)
	steps := []Step{
		{Kind: StepLiteLLMPlist, Path: lp.Path,
			Detail: "create the LiteLLM LaunchAgent plist " + lp.Path + " (mode 0600): " + spec.Binary + " --config " + spec.ConfigPath +
				" --port " + strconv.Itoa(spec.Port) + ", PATH=" + spec.Path + ", logs " + spec.Home + "/.litellm.log and " + spec.Home +
				"/.litellm.err.log, started at login and kept alive, with a new generated LITELLM_MASTER_KEY and LITELLM_SALT_KEY (not shown)"},
		{Kind: StepBootstrap, Path: lp.Path, Detail: "load the LiteLLM LaunchAgent: launchctl bootstrap " + domain + " " + lp.Path},
		{Kind: StepWait, Action: ActionWaitLiteLLM, Value: url, Detail: "wait for LiteLLM to answer at " + url +
			"/health/liveliness, then check that the new job is what listens on port " + strconv.Itoa(spec.Port) + " and that it accepts the new master key"},
	}
	notes := []string{
		"the new LiteLLM plist leaves out DATABASE_URL, the UI login and provider keys: add them as " + guide14 +
			" describes (spend tracking and the admin UI need DATABASE_URL)",
		"the new LITELLM_SALT_KEY must never change once LiteLLM stores credentials with it: keep a copy of the plist (" + guide14 + ")",
	}
	return &spec, steps, notes, nil
}

// planLiteLLMRotate plans a new master key in the existing XML plist: backup,
// key edit (written without group or other bits), then (after the pair, which the caller
// places) a LiteLLM restart, returned last. Nothing else in the file changes.
func planLiteLLMRotate(st State) ([]Step, []string, []string) {
	lp := st.LiteLLMPlist
	path := orNone(lp.Path)
	var reasons []string
	switch {
	case !lp.Present:
		reasons = append(reasons, reason("there is no LiteLLM plist at "+path+", so there is no master key to replace",
			"create one, or set up LiteLLM as "+guide14+" describes"))
	case lp.Binary:
		reasons = append(reasons, reason(path+" is a binary property list, which wt does not edit",
			"convert it with `plutil -convert xml1 "+path+"`, then check again ("+guide14+")"))
	case lp.Err != "":
		reasons = append(reasons, reason(path+" cannot be read as an XML plist", "fix it as "+guide14+" describes, then check again"))
	case !lp.HasMasterKey:
		reasons = append(reasons, reason(path+" has no LITELLM_MASTER_KEY to replace", "add one as "+guide14+" describes"))
	case lp.JobChecked && !lp.JobLoaded:
		// The reload unloads the running job and bootstraps the edited plist;
		// with no job loaded there is no running LiteLLM to move to the new
		// key, and loading one is Create's or the user's step.
		reasons = append(reasons, reason("the LiteLLM job "+litellm.ProxyLabel+" is not loaded, so there is no running LiteLLM to move to the new key",
			"load it with `launchctl bootstrap gui/"+strconv.Itoa(st.Tools.UID)+" "+path+"`, then check again"))
	case lp.HasDatabaseURL && !lp.HasSaltKey:
		// LiteLLM encrypts stored provider credentials with the master key
		// when no salt key is set: a new one would leave them undecryptable.
		reasons = append(reasons, reason(path+" sets DATABASE_URL but no LITELLM_SALT_KEY, and LiteLLM falls back to the master key when no salt key is set (see its documentation), so a new master key could make its stored credentials unreadable",
			"first set LITELLM_SALT_KEY to the current master key's value, or confirm the database holds no stored credentials; see "+guide14+"#6-generate-the-keys-file-mode"))
	}
	if len(reasons) > 0 {
		return nil, nil, reasons
	}
	// planLocal refuses an unknown config home before any step runs.
	dir := BackupDir(st.Host.ConfigHome)
	steps := []Step{
		{Kind: StepBackup, Path: lp.Path, Value: dir, Detail: "check that the key edit applies to " + lp.Path + ", then back it up to " + dir + "/" +
			filepath.Base(lp.Path) + ".bak-<date>-<time> (a 0700 directory, outside LaunchAgents so launchd never loads the copy; its mode without group or other bits; it keeps the old key)"},
		{Kind: StepMasterKey, Path: lp.Path, Detail: "replace LITELLM_MASTER_KEY in " + lp.Path + " with a new generated key (not shown), keeping every other byte of the file; " +
			"it is written without group or other bits"},
	}
	url := liteLLMURL(st, LiteLLMRotateKey)
	steps = append(steps,
		Step{Kind: StepKickstartLiteLLM, Path: lp.Path,
			Detail: "restart LiteLLM so it uses the new key: unload it (launchctl bootout gui/" + strconv.Itoa(st.Tools.UID) + "/" + litellm.ProxyLabel +
				") and load the edited plist (launchctl bootstrap gui/" + strconv.Itoa(st.Tools.UID) + " " + lp.Path + "); a kickstart -k would restart the definition launchd already holds, with the old key"},
		Step{Kind: StepWait, Action: ActionWaitLiteLLM, Value: url,
			Detail: "wait for LiteLLM to answer at " + url + "/health/liveliness and accept the new master key"})
	cfg := ""
	if st.Config.Path != "" {
		cfg = " in " + st.Config.Path
	}
	notes := []string{
		"anything else that holds the old master key stops working once LiteLLM restarts: other clients and scripts that send it, a [litellm] api_key" +
			cfg + " if one is set there, and copies of the plist (the backup keeps the old key; remove it once the new key works)",
	}
	if lp.HasSaltKey {
		notes = append(notes, "never changed in "+lp.Path+": LITELLM_SALT_KEY, DATABASE_URL, provider keys and the UI login")
	} else {
		notes = append(notes, "never changed in "+lp.Path+": DATABASE_URL, provider keys and the UI login; LITELLM_SALT_KEY: no salt key set; nothing to preserve")
	}
	return steps, notes, nil
}

// BackupDir is where a rotation keeps its copies of the LiteLLM plist:
// <config home>/wt-model/backups, a 0700 directory. Never ~/Library/
// LaunchAgents: a copy there carries the same Label and the old key, and
// launchd or Background Task Management could pick it up.
func BackupDir(configHome string) string {
	return filepath.Join(configHome, "wt-model", "backups")
}

// planTightenLiteLLM plans one chmod of the LiteLLM plist to 0600, for a
// plist that exists and is looser; nothing else is planned or touched.
func planTightenLiteLLM(st State, want Desired) (Plan, error) {
	lp := st.LiteLLMPlist
	path := orNone(lp.Path)
	switch {
	case !lp.Present:
		return Plan{}, &RefusalError{Reasons: []string{reason("there is no LiteLLM plist at "+path+" to tighten", "nothing to do")}}
	case lp.Mode.Perm()&0o077 == 0:
		return Plan{}, &RefusalError{Reasons: []string{reason(path+" is already mode 0600 or tighter", "nothing to do")}}
	}
	return Plan{Plan: modelmode.Plan{Want: want.Desired, Steps: []Step{{Kind: StepChmod, Path: lp.Path,
		Detail: fmt.Sprintf("set %s to mode 0600 (it is %04o, readable by others); nothing else changes", lp.Path, lp.Mode.Perm())}}}, Admin: want.AdminWant}, nil
}
