package rootmove

import (
	"bytes"
	"encoding/json"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rescue"
)

// EnvelopeVersion is the `version` of the restore-root result document. Bump
// it for any change a consumer could trip on;
// docs/contracts/restore-root-result.schema.json and its samples move with it
// (held by cmd/wt/restore_root_contract_test.go).
const EnvelopeVersion = 1

// Meta is the run-wide part of the document.
type Meta struct {
	Repo   string
	DryRun bool
}

// Envelope is the one JSON document `wt restore-root --json` prints, on
// success and on failure (nothing else goes to stdout). Warnings and
// Completed are always arrays. On a failure before any outcome (a usage error,
// a repository that could not be read) only Error is set beside the
// always-written keys. Carried, LeftBehind, Record and ErrorJSON reuse
// rescue's shapes.
type Envelope struct {
	Version       int                    `json:"version"`
	Repo          string                 `json:"repo"`
	DryRun        bool                   `json:"dry_run"`
	Outcome       string                 `json:"outcome,omitempty"`
	Code          string                 `json:"code,omitempty"`
	Reason        string                 `json:"reason,omitempty"`
	Created       bool                   `json:"created"`
	Branch        string                 `json:"branch,omitempty"`
	Default       string                 `json:"default,omitempty"`
	DestPath      string                 `json:"dest_path,omitempty"`
	Record        *rescue.RecordJSON     `json:"record,omitempty"`
	DefaultFF     bool                   `json:"default_ff,omitempty"`
	Carried       *rescue.CarriedJSON    `json:"carried,omitempty"`
	LeftBehind    *rescue.LeftBehindJSON `json:"left_behind,omitempty"`
	FastForwarded bool                   `json:"fast_forwarded,omitempty"`
	Completed     []string               `json:"completed"`
	Warnings      []string               `json:"warnings"`
	Error         *rescue.ErrorJSON      `json:"error,omitempty"`
}

// Outcomes is the enum of Envelope.Outcome, for tests and the schema.
var Outcomes = []rescue.Status{rescue.StatusMoved, rescue.StatusWouldMove, rescue.StatusRefused, rescue.StatusFailed}

// Codes is every code an Outcome or Refusal can carry: rescue's, which reach
// the caller unchanged, except `primary` (a root is always the primary
// checkout) and `nothing_to_keep` (a clean root still moves), plus rootmove's
// own. TestCodesCoverEveryCodeRootmoveEmits holds it to the sources.
var Codes = []string{
	rescue.CodeNotWorktree, rescue.CodeGatesUnknown, rescue.CodeOperation, rescue.CodeUnmerged,
	rescue.CodeHiddenIndex, rescue.CodeSubmodule, rescue.CodeStagedAndEdited, rescue.CodeIntentToAdd,
	rescue.CodeNestedRepo, rescue.CodeSpecialFile, rescue.CodeTooLarge, rescue.CodeLineEndings,
	rescue.CodeContentFilter, rescue.CodeNameUnavailable, rescue.CodeSourceChanged, rescue.CodeUnreadable,
	rescue.CodeVerifyFailed, rescue.CodeCarryFailed, rescue.CodeCreateFailed, rescue.CodeSourceChangedMid,
	CodeDetachedRoot, CodeAlreadyDefault, CodeDefaultInWorktree, CodeRootHeld, CodeNoCommit,
	CodeReleaseRefused, CodeReleaseFailed, CodeSwitchFailed, CodeNoDefaultBranch, CodeIgnoredCollision,
	CodeRecordNotWritten, CodeWorktreesNotIgnored,
}

// EncodeResult is the document for one outcome. Plan facts (branch, default,
// counts) appear when the outcome has a plan; dest_path only once the
// directory exists (Created) or in a dry run, where it is where it would be.
func EncodeResult(o Outcome, m Meta) ([]byte, error) {
	p := o.Plan
	e := Envelope{Version: EnvelopeVersion, Repo: m.Repo, DryRun: m.DryRun,
		Outcome: string(o.Status), Code: o.Code, Reason: cleanup.OneLine(o.Reason), Created: o.Created,
		Branch: p.Branch, Default: p.Default, DefaultFF: p.DefaultFF, FastForwarded: o.FastForwarded,
		Completed: sanitised(o.Completed), Warnings: sanitised(p.Warnings)}
	if o.Created || o.Status == rescue.StatusWouldMove {
		e.DestPath = p.DestPath
	}
	if p.Branch != "" {
		c := p.Counts
		e.Carried = &rescue.CarriedJSON{Commits: c.Commits, Staged: c.Staged, Unstaged: c.Unstaged, Untracked: c.Untracked, UntrackedBytes: c.UntrackedBytes}
		e.LeftBehind = &rescue.LeftBehindJSON{IgnoredEntries: c.IgnoredLeft}
	}
	if o.Record != nil {
		e.Record = &rescue.RecordJSON{Ref: o.Record.Ref, Commit: o.Record.Commit}
	}
	return encode(e)
}

// EncodeError is the document for a run that failed before any outcome.
func EncodeError(m Meta, err error) ([]byte, error) {
	msg := "unknown error"
	if err != nil {
		msg = cleanup.OneLine(err.Error())
	}
	return encode(Envelope{Version: EnvelopeVersion, Repo: m.Repo, DryRun: m.DryRun,
		Completed: []string{}, Warnings: []string{}, Error: &rescue.ErrorJSON{Message: msg}})
}

func sanitised(ss []string) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		out = append(out, cleanup.OneLine(s))
	}
	return out
}

func encode(e Envelope) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(e); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
