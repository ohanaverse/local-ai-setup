package removal

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"unicode"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// sha40 is a well-formed full commit id for documents built by hand.
var sha40 = strings.Repeat("ab", 20)

// sampleOutcomes are a few outcomes of each shape: a worktree whose branch
// was deleted, a detached worktree, a branch-only item, a refused one.
func sampleOutcomes() []Outcome {
	wt := NewOutcome(Item{Kind: cleanup.KindWorktree, Name: "topic", Path: "/r/.worktrees/topic", Ref: "refs/heads/topic",
		Saw: cleanup.StatusProven}, WtRemoved, BrDeleted, "deleted refs/heads/topic (was "+sha40+")")
	wt.DeletedTip = sha40
	wt.AddWarning(gcCaveat)
	det := NewOutcome(Item{Kind: cleanup.KindWorktree, Name: "det", Path: "/r/.worktrees/det", Saw: cleanup.StatusProven},
		WtRemoved, BrNotAttempted, "detached HEAD: no branch to delete")
	br := NewOutcome(Item{Kind: cleanup.KindBranch, Name: "loose", Ref: "refs/heads/loose", Saw: cleanup.StatusLessSure},
		WtNotRequested, BrRetainedUnmerged, "branch refs/heads/loose kept: not proven merged; -D deletes it")
	ref := NewOutcome(Item{Kind: cleanup.KindWorktree, Name: "wip", Path: "/r/.worktrees/wip", Ref: "refs/heads/wip",
		Saw: cleanup.StatusUniqueWork}, WtRefused, BrNotAttempted, "uncommitted changes; --force required")
	ref.LossLines = []string{"uncommitted changes will be lost"}
	return []Outcome{wt, det, br, ref}
}

// encodeDoc encodes outcomes and decodes the document generically.
func encodeDoc(t *testing.T, outcomes []Outcome, meta Meta) (map[string]any, []byte) {
	t.Helper()
	b, err := Encode(outcomes, meta)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, b)
	}
	return v, b
}

// TestRemoveEnvelopeVersioned pins that every removal document, success and
// failure, carries `version` equal to EnvelopeVersion and an always-present
// boolean `dry_run`. Why: a consumer checks the version before reading
// anything else, and must never have to guess whether a run changed things.
func TestRemoveEnvelopeVersioned(t *testing.T) {
	if EnvelopeVersion != 1 {
		t.Fatalf("EnvelopeVersion = %d; bump only with the schema and fixtures", EnvelopeVersion)
	}
	okDoc, _ := encodeDoc(t, sampleOutcomes(), Meta{Repo: "/r", DryRun: true})
	errB, err := EncodeError("/r", errors.New("boom"))
	if err != nil {
		t.Fatal(err)
	}
	var errDoc map[string]any
	if err := json.Unmarshal(errB, &errDoc); err != nil {
		t.Fatal(err)
	}
	for name, d := range map[string]map[string]any{"Encode": okDoc, "EncodeError": errDoc} {
		if d["version"] != float64(EnvelopeVersion) {
			t.Errorf("%s: version %v, want %d", name, d["version"], EnvelopeVersion)
		}
		if _, ok := d["dry_run"].(bool); !ok {
			t.Errorf("%s: dry_run %v is not a boolean", name, d["dry_run"])
		}
		if d["repo"] != "/r" {
			t.Errorf("%s: repo %v", name, d["repo"])
		}
	}
	if okDoc["dry_run"] != true || errDoc["dry_run"] != false {
		t.Errorf("dry_run: Encode %v (meta true), EncodeError %v", okDoc["dry_run"], errDoc["dry_run"])
	}
}

// TestRemoveEnvelopeItemsNeverNull pins `items`, and every item's `warnings`
// and `losses`, as arrays on every path: no outcomes (nil and empty), an
// outcome with nil Warnings and LossLines, and the error envelope with an
// error and with a nil one. Why: `null` where a consumer iterates an array
// crashes jq and most scripts.
func TestRemoveEnvelopeItemsNeverNull(t *testing.T) {
	bare := NewOutcome(Item{Kind: cleanup.KindBranch, Name: "x", Ref: "refs/heads/x", Saw: cleanup.StatusProven},
		WtNotRequested, BrRetainedUnmerged, "kept")
	docs := map[string][]byte{}
	for name, outs := range map[string][]Outcome{"nil": nil, "empty": {}, "bare item": {bare}} {
		_, b := encodeDoc(t, outs, Meta{Repo: "/r"})
		docs["Encode "+name] = b
	}
	for name, e := range map[string]error{"error": errors.New("x"), "nil error": nil} {
		b, err := EncodeError("/r", e)
		if err != nil {
			t.Fatal(err)
		}
		docs["EncodeError "+name] = b
	}
	for name, b := range docs {
		var doc struct {
			Items []map[string]json.RawMessage `json:"items"`
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(b, &raw); err != nil {
			t.Fatal(err)
		}
		if s := string(bytes.TrimSpace(raw["items"])); !strings.HasPrefix(s, "[") {
			t.Errorf("%s: items is %q, want an array", name, s)
			continue
		}
		if err := json.Unmarshal(b, &doc); err != nil {
			t.Fatal(err)
		}
		for i, it := range doc.Items {
			for _, k := range []string{"warnings", "losses"} {
				if s := string(it[k]); !strings.HasPrefix(s, "[") {
					t.Errorf("%s: items[%d].%s is %q, want an array", name, i, k, s)
				}
			}
		}
	}
}

// TestRemoveEnumsStable pins the exact wire string of every worktree and
// branch outcome constant. Why: they are the contract's enums (the schema
// lists them); a Go-side rename must fail here, not reach a script.
func TestRemoveEnumsStable(t *testing.T) {
	wt := map[WorktreeOutcome]string{
		WtRemoved: "removed", WtSkippedChanged: "skipped_changed", WtRefused: "refused",
		WtFailed: "failed", WtNotRequested: "not_requested",
	}
	br := map[BranchOutcome]string{
		BrDeleted: "deleted", BrNotAttempted: "not_attempted", BrRetainedUnmerged: "retained_unmerged",
		BrRetainedMoved: "retained_moved", BrRetainedCheckedOut: "retained_checked_out",
		BrRetainedDefault: "retained_default", BrRetainedUnknown: "retained_unknown",
	}
	for c, want := range wt {
		if string(c) != want {
			t.Errorf("worktree outcome %q, want %q", c, want)
		}
	}
	for c, want := range br {
		if string(c) != want {
			t.Errorf("branch outcome %q, want %q", c, want)
		}
	}
	if len(wt) != 5 || len(br) != 7 {
		t.Errorf("%d worktree and %d branch outcomes pinned; want 5 and 7", len(wt), len(br))
	}
}

// oneDocument decodes b as exactly one JSON value and nothing after it but
// whitespace; it also requires the trailing newline a terminal expects.
func oneDocument(t *testing.T, name string, b []byte) map[string]any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(b))
	var v map[string]any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("%s: %v\n%s", name, err, b)
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		t.Errorf("%s: more than one document (%v)", name, err)
	}
	if !bytes.HasSuffix(b, []byte("}\n")) {
		t.Errorf("%s: does not end in one newline: %q", name, b[max(0, len(b)-5):])
	}
	return v
}

// TestRemoveErrorIsOneDocument pins the failure path: EncodeError prints one
// document with `items: []` and `error.message` (one sanitised line), also
// for a nil error (a fixed message, no panic). Why: a consumer parses stdout
// whole; a second document or a panic loses the run's result.
func TestRemoveErrorIsOneDocument(t *testing.T) {
	for name, c := range map[string]struct {
		err  error
		want string
	}{
		"error":      {errors.New("could not\nread\x1b the\a registry"), "could not read the registry"},
		"nil error":  {nil, "unknown error"},
		"long error": {errors.New(strings.Repeat("x", 500)), strings.Repeat("x", 199) + "…"},
	} {
		b, err := EncodeError("/r", c.err)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		v := oneDocument(t, name, b)
		items, ok := v["items"].([]any)
		if !ok || len(items) != 0 {
			t.Errorf("%s: items %v, want []", name, v["items"])
		}
		e, _ := v["error"].(map[string]any)
		if e["message"] != c.want {
			t.Errorf("%s: message %q, want %q", name, e["message"], c.want)
		}
	}
	b, _ := Encode(sampleOutcomes(), Meta{Repo: "/r"})
	if v := oneDocument(t, "Encode", b); v["error"] != nil {
		t.Errorf("success document carries error %v", v["error"])
	}
}

// TestRemoveEncodeIsDeterministic encodes the same outcomes twice and in
// reverse: the bytes repeat exactly, items keep the input order, and keys
// follow the struct's order. Why: scripts diff runs, and the order of items is
// the order wt acted in.
func TestRemoveEncodeIsDeterministic(t *testing.T) {
	outs := sampleOutcomes()
	_, a := encodeDoc(t, outs, Meta{Repo: "/r"})
	_, b := encodeDoc(t, outs, Meta{Repo: "/r"})
	if !bytes.Equal(a, b) {
		t.Fatalf("two encodings differ:\n%s\n%s", a, b)
	}
	rev := slices.Clone(outs)
	slices.Reverse(rev)
	d, _ := encodeDoc(t, rev, Meta{Repo: "/r"})
	var names []string
	for _, it := range d["items"].([]any) {
		names = append(names, it.(map[string]any)["name"].(string))
	}
	if want := []string{"wip", "loose", "det", "topic"}; !slices.Equal(names, want) {
		t.Errorf("item order %v, want the input order %v", names, want)
	}
	keys := []string{`"version"`, `"repo"`, `"dry_run"`, `"items"`, `"kind"`, `"name"`, `"path"`, `"ref"`, `"saw"`,
		`"worktree"`, `"branch"`, `"reason"`, `"warnings"`, `"losses"`, `"branch_tip"`}
	last := -1
	for _, k := range keys {
		i := bytes.Index(a, []byte(k+":"))
		if i < last {
			t.Errorf("key %s out of order", k)
		}
		last = i
	}
}

// TestRemoveBranchTipOnlyWhenDeleted pins `branch_tip`: present, the full
// commit id, exactly when the outcome carries a DeletedTip; absent otherwise
// (dry runs, kept branches, refusals). Why (Ruling J): the reason is capped
// at 200 runes, so for a long branch name the restore id must survive in its
// own key, and its presence must mean "this run deleted (or may have
// deleted) this commit's branch".
func TestRemoveBranchTipOnlyWhenDeleted(t *testing.T) {
	d, _ := encodeDoc(t, sampleOutcomes(), Meta{Repo: "/r"})
	for _, it := range d["items"].([]any) {
		m := it.(map[string]any)
		tip, has := m["branch_tip"]
		if want := m["name"] == "topic"; has != want {
			t.Errorf("%v: branch_tip present %v, want %v", m["name"], has, want)
		}
		if has && tip != sha40 {
			t.Errorf("branch_tip %v, want the full id %s", tip, sha40)
		}
	}
}

// TestRemoveAbsentVersusEmpty pins which keys are left out and when: `path`
// is absent for a branch item, `ref` for a detached worktree, `saw`, `reason`,
// `warnings` and `losses` are always written. Why: absent means "does not
// apply"; an empty string would read as a value.
func TestRemoveAbsentVersusEmpty(t *testing.T) {
	d, _ := encodeDoc(t, sampleOutcomes(), Meta{Repo: "/r"})
	byName := map[string]map[string]any{}
	for _, it := range d["items"].([]any) {
		m := it.(map[string]any)
		byName[m["name"].(string)] = m
	}
	if _, has := byName["loose"]["path"]; has {
		t.Error("branch item has a path")
	}
	if _, has := byName["det"]["ref"]; has {
		t.Error("detached worktree has a ref")
	}
	if byName["topic"]["path"] != "/r/.worktrees/topic" || byName["topic"]["ref"] != "refs/heads/topic" {
		t.Errorf("worktree item %v lacks path or ref", byName["topic"])
	}
	for name, m := range byName {
		for _, k := range []string{"kind", "name", "saw", "worktree", "branch", "reason", "warnings", "losses"} {
			if _, has := m[k]; !has {
				t.Errorf("%s: %s absent", name, k)
			}
		}
	}
	if l := byName["wip"]["losses"].([]any); len(l) != 1 || l[0] != "uncommitted changes will be lost" {
		t.Errorf("losses %v", l)
	}
}

// TestRemoveEncodeRefusesOffContractValues: an outcome whose kind, saw,
// worktree or branch value is not one of the contract's enums is an error,
// never a document that breaks the schema. Why: a consumer validating against
// the schema must be able to trust every document wt prints.
func TestRemoveEncodeRefusesOffContractValues(t *testing.T) {
	good := sampleOutcomes()[0]
	for name, edit := range map[string]func(o *Outcome){
		"kind":     func(o *Outcome) { o.Item.Kind = "" },
		"saw":      func(o *Outcome) { o.Item.Saw = "" },
		"worktree": func(o *Outcome) { o.Worktree = "gone" },
		"branch":   func(o *Outcome) { o.Branch = "" },
	} {
		o := good
		edit(&o)
		if b, err := Encode([]Outcome{o}, Meta{Repo: "/r"}); err == nil {
			t.Errorf("%s: off-contract value encoded:\n%s", name, b)
		}
	}
}

// TestRemoveJSONCarriesNoRawTerminalText: an Outcome whose reason, warnings
// and loss lines hold ESC, BEL, CSI (C1), bidi controls and newlines (as an
// Outcome set by hand, not through SetReason, could) produces string values
// with none of them, each one line, and the document's bytes hold no control
// byte but the pretty-printer's newlines. Why: scripts print these fields to a
// terminal; encoding/json escapes C0 controls but passes C1, DEL and bidi
// runes through, so Encode must sanitise itself rather than trust its input.
func TestRemoveJSONCarriesNoRawTerminalText(t *testing.T) {
	hostile := "a\x1b]0;pwned\x07b\nc\u009b31m‮d\x7f"
	o := Outcome{Item: Item{Kind: cleanup.KindWorktree, Name: "n", Path: "/p", Saw: cleanup.StatusProven},
		Worktree: WtFailed, Branch: BrNotAttempted, Reason: hostile, Warnings: []string{hostile}, LossLines: []string{hostile}}
	d, b := encodeDoc(t, []Outcome{o}, Meta{Repo: "/r"})
	for i, c := range b {
		if c < 0x20 && c != '\n' {
			t.Errorf("raw control byte %#x at %d", c, i)
		}
	}
	it := d["items"].([]any)[0].(map[string]any)
	vals := []any{it["reason"]}
	vals = append(vals, it["warnings"].([]any)...)
	vals = append(vals, it["losses"].([]any)...)
	if len(vals) != 3 {
		t.Fatalf("values %v", vals)
	}
	for _, v := range vals {
		s := v.(string)
		for _, r := range s {
			if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) {
				t.Errorf("value %q holds control rune %U", s, r)
			}
		}
		if s != cleanup.OneLine(hostile) {
			t.Errorf("value %q, want the sanitised line %q", s, cleanup.OneLine(hostile))
		}
	}
}
