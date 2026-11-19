package modelmode

import (
	"fmt"
	"strings"
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
