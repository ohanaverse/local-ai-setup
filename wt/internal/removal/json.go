package removal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// EnvelopeVersion is the `version` of the removal result document. Bump it
// for any change a consumer could trip on (a renamed key, a changed enum
// string); docs/contracts/remove-result.schema.json and the sample fixtures
// move with it (held by cmd/wt/remove_contract_test.go).
const EnvelopeVersion = 1

// Meta is the run-wide part of the document: the repository and whether
// the run was a dry run (nothing changed).
type Meta struct {
	Repo   string
	DryRun bool
}

// Envelope is the one JSON document a removal prints, on success and on
// failure. Items is always present (`[]`, never null) and in the order the
// items were acted on; DryRun is always written. On failure Error is set and
// Items is empty. Error beside a non-empty Items is a partial report (Encode
// met values outside the contract): every listed item is still a change the
// run made or decided.
type Envelope struct {
	Version int        `json:"version"`
	Repo    string     `json:"repo"`
	DryRun  bool       `json:"dry_run"`
	Items   []ItemJSON `json:"items"`
	Error   *ErrorJSON `json:"error,omitempty"`
}

// ItemJSON is one Outcome. Path is absent for a branch item, Ref for a
// detached worktree, BranchTip unless the run deleted (or may have deleted)
// the branch (Outcome.DeletedTip, Ruling J). Saw is the status the user saw;
// Worktree and Branch are the outcome enums. Reason, Warnings and Losses are
// sanitised single lines; Warnings and Losses are always arrays.
type ItemJSON struct {
	Kind      string   `json:"kind"`
	Name      string   `json:"name"`
	Path      string   `json:"path,omitempty"`
	Ref       string   `json:"ref,omitempty"`
	Saw       string   `json:"saw"`
	Worktree  string   `json:"worktree"`
	Branch    string   `json:"branch"`
	Reason    string   `json:"reason"`
	Warnings  []string `json:"warnings"`
	Losses    []string `json:"losses"`
	BranchTip string   `json:"branch_tip,omitempty"`
}

// ErrorJSON is the failure that ended the run.
type ErrorJSON struct {
	Message string `json:"message"`
}

// InvalidValue is the reserved sentinel the document writes in place of a
// kind, saw, worktree or branch value that is not in the contract (no engine
// outcome holds one). It is not an outcome: an item carrying it comes with
// the envelope's error naming the item and the value (a partial report).
const InvalidValue = "invalid"

// The contract's enums, by Go type. A value outside them is written as
// InvalidValue and reported in the envelope's error, never dropped.
var (
	jsonKinds     = []cleanup.Kind{cleanup.KindWorktree, cleanup.KindBranch, cleanup.KindRemote}
	jsonStatuses  = []cleanup.Status{cleanup.StatusProven, cleanup.StatusLessSure, cleanup.StatusUniqueWork, cleanup.StatusInUse}
	jsonWorktrees = []WorktreeOutcome{WtRemoved, WtSkippedChanged, WtRefused, WtFailed, WtNotRequested}
	jsonBranches  = []BranchOutcome{BrDeleted, BrNotAttempted, BrRetainedUnmerged, BrRetainedMoved,
		BrRetainedCheckedOut, BrRetainedDefault, BrRetainedUnknown}
)

// sanitised is ss as one sanitised line each (cleanup.OneLine, which leaves
// text that already is one unchanged), never nil.
func sanitised(ss []string) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		out = append(out, cleanup.OneLine(s))
	}
	return out
}

// enumValue is v when it is one of allowed, else InvalidValue and a problem
// line naming item i, what (the field) and v.
func enumValue[T ~string](i int, what string, v T, allowed []T, problems *[]string) string {
	if slices.Contains(allowed, v) {
		return string(v)
	}
	*problems = append(*problems, fmt.Sprintf("item %d: %s %q is not in the result contract", i, what, string(v)))
	return InvalidValue
}

// itemJSON is o as the contract writes it. A value outside the contract is
// written as InvalidValue (a malformed DeletedTip is left out) and described
// in problems; the item itself is never dropped.
func itemJSON(i int, o Outcome, problems *[]string) ItemJSON {
	it := o.Item
	j := ItemJSON{
		Kind: enumValue(i, "kind", it.Kind, jsonKinds, problems), Name: it.Name, Ref: it.Ref,
		Saw:      enumValue(i, "status seen", it.Saw, jsonStatuses, problems),
		Worktree: enumValue(i, "worktree outcome", o.Worktree, jsonWorktrees, problems),
		Branch:   enumValue(i, "branch outcome", o.Branch, jsonBranches, problems),
		// Reason and Warnings are set through SetReason/AddWarning, but an
		// Outcome is a plain struct: sanitise again, never trust the input.
		Reason:   cleanup.OneLine(o.Reason),
		Warnings: sanitised(o.Warnings),
		Losses:   sanitised(o.LossLines),
	}
	switch {
	case o.DeletedTip == "":
	case commitID.MatchString(o.DeletedTip):
		j.BranchTip = o.DeletedTip
	default:
		*problems = append(*problems, fmt.Sprintf("item %d: branch_tip %q is not a full commit id", i, o.DeletedTip))
	}
	if it.Kind != cleanup.KindBranch {
		j.Path = it.Path
	}
	return j
}

func marshal(e Envelope) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(e); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Encode renders outcomes as the envelope, every one of them, in the order
// given. It runs after the removals happened, so it never drops an item: a
// value outside the contract is written as InvalidValue (a malformed
// DeletedTip left out) and the envelope's error names each such item and
// value, a partial report the caller treats as having made the listed
// changes. The message is one line under the 200-rune cap: it names the first
// off-contract values that fit and states the count of the rest ("and N more
// off-contract values"). It returns an error only when marshalling fails.
func Encode(outcomes []Outcome, meta Meta) ([]byte, error) {
	e := Envelope{Version: EnvelopeVersion, Repo: meta.Repo, DryRun: meta.DryRun, Items: make([]ItemJSON, 0, len(outcomes))}
	var problems []string
	for i, o := range outcomes {
		e.Items = append(e.Items, itemJSON(i, o, &problems))
	}
	if len(problems) > 0 {
		e.Error = &ErrorJSON{Message: partialMessage(problems)}
	}
	return marshal(e)
}

// partialMessage is the partial report's error message: the first problems
// that fit on one uncut line (cleanup.OneLine's cap), then the count of the
// rest. When not even one fits, the total count leads and the first problem
// follows, cut.
func partialMessage(problems []string) string {
	for n := len(problems); n >= 1; n-- {
		msg := "partial report: " + strings.Join(problems[:n], "; ")
		if rest := len(problems) - n; rest > 0 {
			msg += fmt.Sprintf("; and %d more off-contract values", rest)
		}
		if line := cleanup.OneLine(msg); !strings.HasSuffix(line, "…") {
			return line
		}
	}
	return cleanup.OneLine(fmt.Sprintf("partial report: %d off-contract values; the first: %s", len(problems), problems[0]))
}

// EncodeError renders the failure envelope: no items, dry_run from meta (a
// failed dry run says so), and err as one sanitised line; a nil err is
// reported as "unknown error".
func EncodeError(meta Meta, err error) ([]byte, error) {
	msg := "unknown error" // a nil err still yields one document
	if err != nil {
		msg = cleanup.OneLine(err.Error())
	}
	return marshal(Envelope{Version: EnvelopeVersion, Repo: meta.Repo, DryRun: meta.DryRun, Items: []ItemJSON{}, Error: &ErrorJSON{Message: msg}})
}
