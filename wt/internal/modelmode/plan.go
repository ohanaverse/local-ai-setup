package modelmode

import "strings"

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
