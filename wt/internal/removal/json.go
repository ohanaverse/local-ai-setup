package removal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"

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
// Items is empty.
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

// The contract's enums, by Go type: an outcome holding any other value is not
// encoded (Encode fails) rather than printed off-contract.
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

// itemJSON is o as the contract writes it, or an error when one of its enum
// values is not in the contract.
func itemJSON(i int, o Outcome) (ItemJSON, error) {
	it := o.Item
	switch {
	case !slices.Contains(jsonKinds, it.Kind):
		return ItemJSON{}, fmt.Errorf("removal: item %d: kind %q is not in the result contract", i, it.Kind)
	case !slices.Contains(jsonStatuses, it.Saw):
		return ItemJSON{}, fmt.Errorf("removal: item %d: status seen %q is not in the result contract", i, it.Saw)
	case !slices.Contains(jsonWorktrees, o.Worktree):
		return ItemJSON{}, fmt.Errorf("removal: item %d: worktree outcome %q is not in the result contract", i, o.Worktree)
	case !slices.Contains(jsonBranches, o.Branch):
		return ItemJSON{}, fmt.Errorf("removal: item %d: branch outcome %q is not in the result contract", i, o.Branch)
	}
	j := ItemJSON{
		Kind: string(it.Kind), Name: it.Name, Ref: it.Ref, Saw: string(it.Saw),
		Worktree: string(o.Worktree), Branch: string(o.Branch),
		// Reason and Warnings are set through SetReason/AddWarning, but an
		// Outcome is a plain struct: sanitise again, never trust the input.
		Reason:    cleanup.OneLine(o.Reason),
		Warnings:  sanitised(o.Warnings),
		Losses:    sanitised(o.LossLines),
		BranchTip: o.DeletedTip,
	}
	if it.Kind != cleanup.KindBranch {
		j.Path = it.Path
	}
	return j, nil
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

// Encode renders outcomes as the envelope, in the order given. It fails,
// printing nothing, if an outcome holds a value outside the contract's enums
// (no engine outcome does); the caller then prints EncodeError's document.
func Encode(outcomes []Outcome, meta Meta) ([]byte, error) {
	e := Envelope{Version: EnvelopeVersion, Repo: meta.Repo, DryRun: meta.DryRun, Items: make([]ItemJSON, 0, len(outcomes))}
	for i, o := range outcomes {
		j, err := itemJSON(i, o)
		if err != nil {
			return nil, err
		}
		e.Items = append(e.Items, j)
	}
	return marshal(e)
}

// EncodeError renders the failure envelope: no items, dry_run false (the
// document claims nothing was decided), and err as one sanitised line; a nil
// err is reported as "unknown error".
func EncodeError(repo string, err error) ([]byte, error) {
	msg := "unknown error" // a nil err still yields one document
	if err != nil {
		msg = cleanup.OneLine(err.Error())
	}
	return marshal(Envelope{Version: EnvelopeVersion, Repo: repo, Items: []ItemJSON{}, Error: &ErrorJSON{Message: msg}})
}
