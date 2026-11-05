package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/removal"
)

const (
	removeFixture            = "../../../docs/contracts/remove-result.sample.json"
	removeDryRunFixture      = "../../../docs/contracts/remove-result.dry-run.sample.json"
	removeErrorFixture       = "../../../docs/contracts/remove-result.error.sample.json"
	removeDryRunErrorFixture = "../../../docs/contracts/remove-result.dry-run.error.sample.json"
	removePartialFixture     = "../../../docs/contracts/remove-result.partial.sample.json"
	removeSchema             = "../../../docs/contracts/remove-result.schema.json"
	removalPkgDir            = "../../internal/removal"
)

// removeFixtures are the samples by name.
var removeFixtures = map[string]string{"sample": removeFixture, "dry-run sample": removeDryRunFixture,
	"error sample": removeErrorFixture, "dry-run error sample": removeDryRunErrorFixture, "partial sample": removePartialFixture}

// removeFixtureFacts are what each sample must say: dry_run, whether error
// is present, and whether items is empty.
var removeFixtureFacts = map[string]struct{ dryRun, hasError, noItems bool }{
	removeFixture:            {false, false, false},
	removeDryRunFixture:      {true, false, false},
	removeErrorFixture:       {false, true, true},
	removeDryRunErrorFixture: {true, true, true},
	removePartialFixture:     {false, true, false},
}

// tipBranches are the branch outcomes a branch_tip may accompany (the ref
// may be gone): deleted, put back, or unknown after the delete was issued,
// or gone after git's error.
var tipBranches = []string{"deleted", "retained_checked_out", "retained_unknown", "retained_moved"}

// checkBranchTips fails for any item whose branch_tip is not a full 40-hex
// commit id, or that carries one beside a branch outcome no deletion can
// produce, or (dry) that carries one at all.
func checkBranchTips(t *testing.T, name string, items []map[string]any, dry bool) {
	t.Helper()
	for i, it := range items {
		tip, has := it["branch_tip"]
		if !has {
			continue
		}
		s, _ := tip.(string)
		if !fullSHA.MatchString(s) {
			t.Errorf("%s: items[%d].branch_tip %v is not a 40-hex commit id", name, i, tip)
		}
		if b, _ := it["branch"].(string); !slices.Contains(tipBranches, b) {
			t.Errorf("%s: items[%d] carries branch_tip beside branch %q", name, i, b)
		}
		if dry {
			t.Errorf("%s: items[%d] carries branch_tip in a dry run, which deletes nothing", name, i)
		}
	}
}

// fullSHA is a full commit id as git prints it.
var fullSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// removeItems are a sample's items, decoded generically.
func removeItems(t *testing.T, path string) []map[string]any {
	t.Helper()
	var items []map[string]any
	for _, it := range readJSONFile(t, path).(map[string]any)["items"].([]any) {
		items = append(items, it.(map[string]any))
	}
	return items
}

// removeEnums are the contract's enum values as the Go source declares them:
// worktree and branch from internal/removal's WorktreeOutcome and
// BranchOutcome constants, kind and saw from internal/cleanup's Kind and
// Status constants. Read from source, so a constant added later is seen.
func removeEnums(t *testing.T) map[string][]string {
	t.Helper()
	got := map[string][]string{}
	for _, c := range stringConsts(t, removalPkgDir) {
		switch c.typ {
		case "WorktreeOutcome":
			got["worktree"] = append(got["worktree"], c.val)
		case "BranchOutcome":
			got["branch"] = append(got["branch"], c.val)
		}
	}
	for _, c := range stringConsts(t, cleanupPkgDir) {
		switch c.typ {
		case "Kind":
			got["kind"] = append(got["kind"], c.val)
		case "Status":
			got["saw"] = append(got["saw"], c.val)
		}
	}
	return got
}

// TestRemoveFixturesMatchEnvelopeShape pins the removal samples
// (docs/contracts/remove-result*.json) to removal.Envelope in both directions
// (every key a sample carries is one wt writes, with that JSON type, and
// every always-written key is present) and validates each against the
// published schema with the purpose-built validator. Each sample says what it
// is (removeFixtureFacts: dry_run, error present, items empty); every
// branch_tip is a full 40-hex id beside a branch outcome a deletion can
// produce, and none appears in a dry run. Why: a script in another language
// reads this document; a renamed Go json tag must fail here until the schema
// and samples move with it.
func TestRemoveFixturesMatchEnvelopeShape(t *testing.T) {
	schema := readJSONFile(t, removeSchema).(map[string]any)
	for name, path := range removeFixtures {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		checkJSONShape(t, name, raw, reflect.TypeFor[removal.Envelope]())
		doc := readJSONFile(t, path).(map[string]any)
		if errs := checkAgainst(schema, doc, name); len(errs) > 0 {
			t.Errorf("%s violates the schema:\n%s", name, strings.Join(errs, "\n"))
		}
		facts := removeFixtureFacts[path]
		if dry := doc["dry_run"] == true; dry != facts.dryRun {
			t.Errorf("%s: dry_run %v, want %v", name, doc["dry_run"], facts.dryRun)
		}
		if _, hasErr := doc["error"]; hasErr != facts.hasError {
			t.Errorf("%s: error present %v, want %v", name, hasErr, facts.hasError)
		}
		items := removeItems(t, path)
		if (len(items) == 0) != facts.noItems {
			t.Errorf("%s: %d items", name, len(items))
		}
		checkBranchTips(t, name, items, facts.dryRun)
	}
}

// TestRemoveSchemaEnumsCoverEveryOutcome pins the schema's kind, saw,
// worktree and branch enums to the Go constants (read from source) plus the
// reserved sentinel removal.InvalidValue, which is not an outcome constant
// and is added here explicitly. It encodes one outcome per constant value
// through removal.Encode, which must write it unchanged with no error, and
// one outcome off the contract in every field, which must come out as the
// sentinel with a partial-report error; both documents must pass the schema.
// Why: a new outcome constant must fail here until the schema, the encoder's
// own enum list and the samples carry it.
func TestRemoveSchemaEnumsCoverEveryOutcome(t *testing.T) {
	schema := readJSONFile(t, removeSchema).(map[string]any)
	got := removeEnums(t)
	want := map[string][]string{}
	withSentinel := map[string][]string{}
	for _, k := range []string{"kind", "saw", "worktree", "branch"} {
		want[k] = schemaDefEnum(t, schema, "item", "properties", k)
		withSentinel[k] = append(slices.Clone(got[k]), removal.InvalidValue)
	}
	checkEnumsEqual(t, withSentinel, want)

	bad, err := removal.Encode([]removal.Outcome{{}}, removal.Meta{Repo: "/r"})
	if err != nil {
		t.Fatal(err)
	}
	var badDoc map[string]any
	if err := json.Unmarshal(bad, &badDoc); err != nil {
		t.Fatal(err)
	}
	if errs := checkAgainst(schema, badDoc, "off-contract"); len(errs) > 0 {
		t.Errorf("off-contract document violates the schema:\n%s", strings.Join(errs, "\n"))
	}
	badItem := badDoc["items"].([]any)[0].(map[string]any)
	for _, k := range []string{"kind", "saw", "worktree", "branch"} {
		if badItem[k] != removal.InvalidValue {
			t.Errorf("off-contract %s written as %v, want %q", k, badItem[k], removal.InvalidValue)
		}
	}
	if badDoc["error"] == nil {
		t.Error("off-contract document carries no error")
	}

	base := removal.Outcome{Item: removal.Item{Kind: cleanup.KindWorktree, Name: "x", Path: "/x", Saw: cleanup.StatusProven},
		Worktree: removal.WtRemoved, Branch: removal.BrNotAttempted}
	var outs []removal.Outcome
	for key, vals := range got {
		for _, v := range vals {
			o := base
			switch key {
			case "kind":
				o.Item.Kind = cleanup.Kind(v)
			case "saw":
				o.Item.Saw = cleanup.Status(v)
			case "worktree":
				o.Worktree = removal.WorktreeOutcome(v)
			case "branch":
				o.Branch = removal.BranchOutcome(v)
			}
			outs = append(outs, o)
		}
	}
	b, err := removal.Encode(outs, removal.Meta{Repo: "/r"})
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	if v["error"] != nil {
		t.Errorf("Encode does not know a declared constant: %v", v["error"])
	}
	if errs := checkAgainst(schema, v, "every constant"); len(errs) > 0 {
		t.Errorf("every-constant document violates the schema:\n%s", strings.Join(errs, "\n"))
	}
}

// TestRemoveEveryOptionalKeyAppears requires every key of every struct
// reachable from removal.Envelope, the omitempty ones (path, ref,
// branch_tip, error) included, to appear in at least one sample. Why: a key
// no sample shows could be renamed without any test noticing.
func TestRemoveEveryOptionalKeyAppears(t *testing.T) {
	schema := readJSONFile(t, removeSchema).(map[string]any)
	var docs []any
	for _, path := range removeFixtures {
		docs = append(docs, readJSONFile(t, path))
	}
	checkEveryKeyAppears(t, schema, reflect.TypeFor[removal.Envelope](), docs...)
}

// TestRemoveSchemaPropertiesMatchGoTags pins, for every struct reachable from
// removal.Envelope, the schema's properties to the Go json fields and its
// required list to the always-written keys. Why: the schema is the published
// contract; a tag renamed with the schema edited around it, or a schema
// property outliving its field, must fail.
func TestRemoveSchemaPropertiesMatchGoTags(t *testing.T) {
	schema := readJSONFile(t, removeSchema).(map[string]any)
	if visited := checkSchemaMatchesGoTags(t, schema, reflect.TypeFor[removal.Envelope]()); visited < 3 {
		t.Errorf("only %d structs reached from Envelope; the walk is not following the schema", visited)
	}
}

// TestRemoveFixtureCoversEveryOutcome requires the main sample (a real run,
// dry_run false) to show every worktree and every branch outcome, an item
// with branch_tip, a branch-only item without path, a detached worktree item
// without ref, an item with warnings and one with losses; and the main and
// dry-run samples together to show every kind and every saw status. The
// values come from the Go source, so a new constant fails here. Why: the
// samples are what a consumer's author reads; an outcome they never show is
// an outcome their script does not handle.
func TestRemoveFixtureCoversEveryOutcome(t *testing.T) {
	enums := removeEnums(t)
	main := removeItems(t, removeFixture)
	both := append(slices.Clone(main), removeItems(t, removeDryRunFixture)...)
	seen := func(items []map[string]any, key, val string) bool {
		return slices.ContainsFunc(items, func(it map[string]any) bool { return it[key] == val })
	}
	for _, key := range []string{"worktree", "branch"} {
		for _, v := range enums[key] {
			if !seen(main, key, v) {
				t.Errorf("the sample has no item with %s %q", key, v)
			}
		}
	}
	for _, key := range []string{"kind", "saw"} {
		for _, v := range enums[key] {
			if !seen(both, key, v) {
				t.Errorf("no sample has an item with %s %q", key, v)
			}
		}
	}
	has := func(what string, f func(it map[string]any) bool) {
		if !slices.ContainsFunc(main, f) {
			t.Errorf("the sample has no %s", what)
		}
	}
	nonEmpty := func(it map[string]any, k string) bool { a, _ := it[k].([]any); return len(a) > 0 }
	absent := func(it map[string]any, k string) bool { _, ok := it[k]; return !ok }
	has("item with branch_tip", func(it map[string]any) bool { return !absent(it, "branch_tip") })
	has("branch-only item without path", func(it map[string]any) bool { return it["kind"] == "branch" && absent(it, "path") })
	has("detached worktree item without ref", func(it map[string]any) bool { return it["kind"] == "worktree" && absent(it, "ref") })
	has("item with warnings", func(it map[string]any) bool { return nonEmpty(it, "warnings") })
	has("item with losses", func(it map[string]any) bool { return nonEmpty(it, "losses") })
	for _, b := range []string{"retained_moved", "retained_unknown"} {
		has(b+" item with branch_tip (the ref may be gone)", func(it map[string]any) bool { return it["branch"] == b && !absent(it, "branch_tip") })
	}
	// remote and in_use belong to previews and plans only.
	if seen(main, "kind", string(cleanup.KindRemote)) || seen(main, "saw", string(cleanup.StatusInUse)) {
		t.Error("the run sample shows a remote kind or an in_use status, which only previews and plans carry")
	}
	// The sentinel is not an outcome constant: the partial sample shows it in
	// each enum field.
	partial := removeItems(t, removePartialFixture)
	for _, key := range []string{"kind", "saw", "worktree", "branch"} {
		if !seen(partial, key, removal.InvalidValue) {
			t.Errorf("the partial sample has no item with %s %q", key, removal.InvalidValue)
		}
	}
}

// partialSampleInput are the outcomes remove-result.partial.sample.json is
// the encoding of: a worktree removed with its branch deleted, and a zero
// Outcome (every enum field off the contract).
func partialSampleInput() []removal.Outcome {
	const tip = "3f9c2a1b7d4e5f60718293a4b5c6d7e8f9012345"
	good := removal.NewOutcome(removal.Item{Kind: cleanup.KindWorktree, Name: "topic", Path: "/Users/me/src/app/.worktrees/topic",
		Ref: "refs/heads/topic", Saw: cleanup.StatusProven}, removal.WtRemoved, removal.BrDeleted,
		"deleted refs/heads/topic (was "+tip+"; restore: git branch topic "+tip+")")
	good.AddWarning("the commit survives only until git gc prunes it, possibly the next gc")
	good.DeletedTip = tip
	return []removal.Outcome{good, {}}
}

// TestRemovePartialSampleIsEncodeOutput encodes partialSampleInput and
// requires the partial sample to be that document: error.message byte for
// byte, and the whole document equal once decoded. Why: the sample shows the
// partial-report message wt prints; hand-written, it would drift from the
// encoder's wording and its cap.
func TestRemovePartialSampleIsEncodeOutput(t *testing.T) {
	b, err := removal.Encode(partialSampleInput(), removal.Meta{Repo: "/Users/me/src/app"})
	if err != nil {
		t.Fatal(err)
	}
	var got, want map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	want = readJSONFile(t, removePartialFixture).(map[string]any)
	ge, _ := got["error"].(map[string]any)
	we, _ := want["error"].(map[string]any)
	gm, _ := ge["message"].(string)
	wm, _ := we["message"].(string)
	if gm != wm {
		t.Errorf("partial sample error.message\n%q\nEncode writes\n%q", wm, gm)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("partial sample is not Encode's output; Encode writes:\n%s", b)
	}
}

// TestRemoveLiveEncodeValidatesAgainstSchema drives the REAL engine in a
// scratch repository (a proven worktree, a dirty one, a locked one, a
// detached one and a branch-only item) through Preview, Confirmed and Run,
// encodes the run's outcomes and the previews' dry-run plans, and validates
// both documents, plus EncodeError's, against the published schema; the run
// document must carry the deleted branch's full tip as branch_tip. Why: the
// samples are hand-written; this is what wt really prints.
func TestRemoveLiveEncodeValidatesAgainstSchema(t *testing.T) {
	stubCleanupGH(t)
	dir := cleanupRepo(t)
	wtAt := func(name string, args ...string) string {
		p := filepath.Join(dir, ".worktrees", name)
		cgit(t, dir, append([]string{"worktree", "add"}, append(args, p)...)...)
		return p
	}
	topic := wtAt("topic", "-b", "topic")
	cleanupCommit(t, topic, "topic.txt", "topic work")
	topicTip := cgit(t, topic, "rev-parse", "HEAD")
	cgit(t, dir, "push", "-u", "origin", "topic")
	cgit(t, dir, "merge", "--squash", "topic")
	cgit(t, dir, "commit", "-m", "squash topic")
	cgit(t, dir, "push", "origin", "main")
	wip := wtAt("wip", "-b", "wip")
	if err := os.WriteFile(filepath.Join(wip, "wip.txt"), []byte("wip"), 0o644); err != nil {
		t.Fatal(err)
	}
	wtAt("held", "-b", "held")
	cgit(t, dir, "worktree", "lock", filepath.Join(dir, ".worktrees", "held"))
	wtAt("det", "--detach")
	cgit(t, dir, "branch", "loose")

	later := time.Now().Add(time.Hour)
	in := cleanup.GatherInputs{CWD: dir, OwnRepo: "o/r", Parallel: 2}
	g := cleanup.Git{Dir: dir}
	s, err := cleanup.Gather(g, in)
	if err != nil {
		t.Fatal(err)
	}
	rep := cleanup.Classify(s, cleanup.Options{Now: later})
	var items []removal.Item
	for _, r := range rep.Rows {
		if r.Kind == cleanup.KindRemote || (r.Kind == cleanup.KindWorktree && r.Path == "") {
			continue
		}
		if r.Kind == cleanup.KindBranch && r.Name != "loose" {
			continue
		}
		if r.Kind == cleanup.KindWorktree && !strings.Contains(r.Path, ".worktrees") {
			continue // the primary checkout
		}
		items = append(items, removal.ItemFromRow(r))
	}
	if len(items) != 5 {
		t.Fatalf("%d items, want 5 (topic, wip, held, det, loose)", len(items))
	}
	e := removal.Engine{G: g, Prior: s, In: in, Classify: cleanup.Options{Now: later},
		Opts: removal.Options{Now: func() time.Time { return later }}}
	previews := e.Preview(items)
	var plans []removal.Outcome
	var confirmed []removal.Item
	for _, p := range previews {
		plans = append(plans, p.Plan)
		if it, err := p.Confirmed(p.Needs); err == nil {
			confirmed = append(confirmed, it)
		}
	}
	if len(confirmed) < 3 {
		t.Fatalf("only %d items confirmed", len(confirmed))
	}
	outs := e.Run(confirmed, nil)

	schema := readJSONFile(t, removeSchema).(map[string]any)
	runDoc, err := removal.Encode(outs, removal.Meta{Repo: dir})
	if err != nil {
		t.Fatal(err)
	}
	planDoc, err := removal.Encode(plans, removal.Meta{Repo: dir, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	errDoc, err := removal.EncodeError(removal.Meta{Repo: dir, DryRun: true}, errors.New("boom"))
	if err != nil {
		t.Fatal(err)
	}
	for name, b := range map[string][]byte{"run": runDoc, "plan": planDoc, "error": errDoc} {
		var v map[string]any
		if err := json.Unmarshal(b, &v); err != nil {
			t.Fatal(err)
		}
		if errs := checkAgainst(schema, v, name); len(errs) > 0 {
			t.Errorf("%s document violates the schema:\n%s\n%s", name, strings.Join(errs, "\n"), b)
		}
		checkJSONShape(t, name, b, reflect.TypeFor[removal.Envelope]())
		var items []map[string]any
		for _, it := range v["items"].([]any) {
			items = append(items, it.(map[string]any))
		}
		checkBranchTips(t, name, items, v["dry_run"] == true)
		if name != "error" && v["error"] != nil {
			t.Errorf("%s document is a partial report: %v", name, v["error"])
		}
	}
	var run removal.Envelope
	if err := json.Unmarshal(runDoc, &run); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range run.Items {
		if it.Name == "topic" {
			found = it.Branch == string(removal.BrDeleted) && it.BranchTip == topicTip
		}
	}
	if !found {
		t.Errorf("the run document has no deleted topic with branch_tip %s:\n%s", topicTip, runDoc)
	}
}
