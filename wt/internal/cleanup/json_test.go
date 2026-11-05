package cleanup

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func decodeDoc(t *testing.T, raw []byte) map[string]json.RawMessage {
	t.Helper()
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("not one JSON object: %v\n%s", err, raw)
	}
	return doc
}

// TestEnvelopeVersioned pins that every document carries `version` equal to
// EnvelopeVersion (1), and that `rows` is present even for an empty report.
// A consumer branches on the version and iterates rows without a nil check,
// so neither may ever go missing. Output is also deterministic and ends in a
// newline.
func TestEnvelopeVersioned(t *testing.T) {
	raw, err := Encode(Report{Repo: "/r", GH: GHOK})
	if err != nil {
		t.Fatal(err)
	}
	doc := decodeDoc(t, raw)
	if string(doc["version"]) != "1" || EnvelopeVersion != 1 {
		t.Errorf("version = %s, EnvelopeVersion = %d; want 1", doc["version"], EnvelopeVersion)
	}
	if string(doc["rows"]) != "[]" {
		t.Errorf("rows = %s; want [] for an empty report", doc["rows"])
	}
	if !bytes.HasSuffix(raw, []byte("}\n")) {
		t.Errorf("output must end with a newline: %q", raw[max(0, len(raw)-5):])
	}
	again, _ := Encode(Report{Repo: "/r", GH: GHOK})
	if !bytes.Equal(raw, again) {
		t.Error("Encode is not deterministic")
	}
}

// TestEnvelopeAbsentVersusUnknown pins the contract's central distinction:
// a less_sure row whose merge proof failed has `unknown: true` and NO
// `evidence` key (never null, never "unknown"), while a proven row has an
// evidence string and `unknown: false`. Consumers read absence as "no proof
// found" and the boolean as "could not be determined".
func TestEnvelopeAbsentVersusUnknown(t *testing.T) {
	rep := Report{
		Repo: "/r", GH: GHOK,
		Base: Base{Default: "main", Ref: "origin/main", FetchedAt: time.Date(2026, 11, 4, 1, 2, 3, 0, time.UTC)},
		Rows: []Row{
			{Kind: KindBranch, Name: "a", Branch: "a", Status: StatusLessSure, Unknown: true,
				Reasons: []Reason{{ReasonMergeProofUnavailable, mergeProofDetail}}},
			{Kind: KindBranch, Name: "b", Branch: "b", Status: StatusProven, Evidence: EvAncestor},
		},
	}
	raw, err := Encode(rep)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Rows []map[string]json.RawMessage `json:"rows"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if _, has := doc.Rows[0]["evidence"]; has {
		t.Errorf("row with no proof must omit evidence: %s", raw)
	}
	if string(doc.Rows[0]["unknown"]) != "true" {
		t.Errorf("unknown = %s; want true", doc.Rows[0]["unknown"])
	}
	if string(doc.Rows[1]["evidence"]) != `"ancestor"` || string(doc.Rows[1]["unknown"]) != "false" {
		t.Errorf("proven row: evidence=%s unknown=%s", doc.Rows[1]["evidence"], doc.Rows[1]["unknown"])
	}
	if strings.Contains(string(raw), "null") {
		t.Errorf("no key may be null: %s", raw)
	}
	for _, r := range doc.Rows {
		for _, k := range []string{"reasons", "blockers"} {
			if !strings.HasPrefix(string(r[k]), "[") {
				t.Errorf("%s must always be an array, got %s", k, r[k])
			}
		}
	}
}

// TestEnvelopeErrorIsOneDocument pins that a failure still prints one
// envelope: version, repo, `error`, an empty `rows` array, and none of the
// result keys (base, fetch, gh). A script piping stdout into a JSON parser
// must never see anything but that single document.
func TestEnvelopeErrorIsOneDocument(t *testing.T) {
	raw, err := EncodeError("/r", errors.New("boom"))
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	var env Envelope
	if err := dec.Decode(&env); err != nil {
		t.Fatal(err)
	}
	if dec.More() {
		t.Error("more than one document")
	}
	if env.Version != EnvelopeVersion || env.Repo != "/r" || env.Error == nil || env.Error.Message != "boom" {
		t.Errorf("envelope = %+v", env)
	}
	doc := decodeDoc(t, raw)
	if string(doc["rows"]) != "[]" {
		t.Errorf("rows = %s; want []", doc["rows"])
	}
	for _, k := range []string{"base", "fetch", "gh"} {
		if _, has := doc[k]; has {
			t.Errorf("error document carries %q", k)
		}
	}
}

// TestStatusAndEvidenceEnumsStable pins the exact wire strings of the
// status, kind, evidence and gh enums. They are a published contract; a
// rename here must be a deliberate version bump, not a refactor side effect.
func TestStatusAndEvidenceEnumsStable(t *testing.T) {
	check := func(name string, got, want []string) {
		t.Helper()
		if !slices.Equal(got, want) {
			t.Errorf("%s = %v; want %v", name, got, want)
		}
	}
	check("status", []string{string(StatusProven), string(StatusLessSure), string(StatusUniqueWork), string(StatusInUse)},
		[]string{"proven", "less_sure", "unique_work", "in_use"})
	check("kind", []string{string(KindWorktree), string(KindBranch), string(KindRemote)},
		[]string{"worktree", "branch", "remote"})
	check("gh", []string{string(GHOK), string(GHUnavailable), string(GHPartial)},
		[]string{"ok", "unavailable", "partial"})
	check("evidence", []string{string(EvSameCommit), string(EvAncestor), string(EvTreesMatch), string(EvNoAddedChanges),
		string(EvMergeAddsNothing), string(EvPRTip), string(EvCherryEquivalent), string(EvPatchID)},
		[]string{"same-commit", "ancestor", "trees-match", "no-added-changes", "merge-adds-nothing", "pr-tip", "cherry-equivalent", "patch-id"})
}
