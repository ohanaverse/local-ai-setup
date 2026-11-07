package rescue

import (
	"bytes"
	"encoding/json"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// EnvelopeVersion is the `version` of the rescue result document. Bump it for
// any change a consumer could trip on; docs/contracts/rescue-result.schema.json
// and its samples move with it (held by cmd/wt/rescue_contract_test.go).
const EnvelopeVersion = 1

// Meta is the run-wide part of the document.
type Meta struct {
	Repo   string
	DryRun bool
}

// Envelope is the one JSON document `wt rescue --json` prints, on success and
// on failure (nothing else goes to stdout). Warnings and Completed are always
// arrays. On a failure before any outcome (a usage error, no match) only
// Error is set beside the always-written keys.
type Envelope struct {
	Version     int              `json:"version"`
	Repo        string           `json:"repo"`
	DryRun      bool             `json:"dry_run"`
	Outcome     string           `json:"outcome,omitempty"`
	Code        string           `json:"code,omitempty"`
	Reason      string           `json:"reason,omitempty"`
	Created     bool             `json:"created"`
	Source      *SourceJSON      `json:"source,omitempty"`
	Destination *DestinationJSON `json:"destination,omitempty"`
	Carried     *CarriedJSON     `json:"carried,omitempty"`
	LeftBehind  *LeftBehindJSON  `json:"left_behind,omitempty"`
	Completed   []string         `json:"completed"`
	Warnings    []string         `json:"warnings"`
	Error       *ErrorJSON       `json:"error,omitempty"`
}

// SourceJSON is the worktree the work came from. Ref is absent for a detached
// worktree.
type SourceJSON struct {
	Path string `json:"path"`
	Ref  string `json:"ref,omitempty"`
	Head string `json:"head"`
}

// DestinationJSON is the new branch and worktree, and the commit it starts at.
type DestinationJSON struct {
	Path   string `json:"path"`
	Branch string `json:"branch"`
	Start  string `json:"start"`
}

// CarriedJSON is what was (or in a dry run would be) copied.
type CarriedJSON struct {
	Commits        int   `json:"commits"`
	Staged         int   `json:"staged"`
	Unstaged       int   `json:"unstaged"`
	Untracked      int   `json:"untracked"`
	UntrackedBytes int64 `json:"untracked_bytes"`
}

// LeftBehindJSON is what is not carried.
type LeftBehindJSON struct {
	IgnoredEntries int `json:"ignored_entries"`
}

// ErrorJSON is the failure that ended the run before any outcome.
type ErrorJSON struct {
	Message string `json:"message"`
}

// Outcomes is the enum of Envelope.Outcome, for tests and the schema.
var Outcomes = []Status{StatusMoved, StatusWouldMove, StatusRefused, StatusFailed}

// Codes is every code an Outcome or Refusal can carry.
var Codes = []string{CodeNotWorktree, CodePrimary, CodeNothingToKeep, CodeGatesUnknown, CodeOperation,
	CodeUnmerged, CodeHiddenIndex, CodeSubmodule, CodeStagedAndEdited, CodeIntentToAdd, CodeNestedRepo,
	CodeSpecialFile, CodeTooLarge, CodeLineEndings, CodeNameUnavailable, CodeSourceChanged, CodeUnreadable, CodeVerifyFailed,
	CodeCarryFailed, CodeCreateFailed, CodeSourceChangedMid}

// EncodeResult is the document for one outcome.
func EncodeResult(o Outcome, m Meta) ([]byte, error) {
	e := Envelope{Version: EnvelopeVersion, Repo: m.Repo, DryRun: m.DryRun,
		Outcome: string(o.Status), Code: o.Code, Reason: cleanup.OneLine(o.Reason), Created: o.Created,
		Completed: sanitised(o.Completed), Warnings: sanitised(o.Plan.Warnings)}
	p := o.Plan
	if p.Source.Path != "" {
		e.Source = &SourceJSON{Path: p.Source.Path, Ref: p.Source.Ref, Head: p.Source.Head}
	}
	if p.Branch != "" {
		e.Destination = &DestinationJSON{Path: p.DestPath, Branch: p.Branch, Start: p.Start}
		c := p.Counts
		e.Carried = &CarriedJSON{Commits: c.Commits, Staged: c.Staged, Unstaged: c.Unstaged, Untracked: c.Untracked, UntrackedBytes: c.UntrackedBytes}
		e.LeftBehind = &LeftBehindJSON{IgnoredEntries: c.IgnoredLeft}
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
		Completed: []string{}, Warnings: []string{}, Error: &ErrorJSON{Message: msg}})
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
