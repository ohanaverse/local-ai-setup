package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/removal"
)

const (
	removeFixture       = "../../../docs/contracts/remove-result.sample.json"
	removeDryRunFixture = "../../../docs/contracts/remove-result.dry-run.sample.json"
	removeErrorFixture  = "../../../docs/contracts/remove-result.error.sample.json"
	removeSchema        = "../../../docs/contracts/remove-result.schema.json"
	removalPkgDir       = "../../internal/removal"
)

// removeFixtures are the three samples by name.
var removeFixtures = map[string]string{"sample": removeFixture, "dry-run sample": removeDryRunFixture, "error sample": removeErrorFixture}

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

// TestRemoveFixturesMatchEnvelopeShape pins the three removal samples
// (docs/contracts/remove-result*.json) to removal.Envelope in both directions
// (every key a sample carries is one wt writes, with that JSON type, and
// every always-written key is present) and validates each against the
// published schema with the purpose-built validator; the dry-run sample says
// dry_run true and carries no branch_tip, the others dry_run false, and the
// error sample has items [] and error.message. Why: a script in another
// language reads this document; a renamed Go json tag must fail here until
// the schema and samples move with it.
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
		if dry := doc["dry_run"] == true; dry != (path == removeDryRunFixture) {
			t.Errorf("%s: dry_run %v", name, doc["dry_run"])
		}
		_, hasErr := doc["error"]
		if hasErr != (path == removeErrorFixture) {
			t.Errorf("%s: error present %v", name, hasErr)
		}
	}
	if items := removeItems(t, removeErrorFixture); len(items) != 0 {
		t.Errorf("error sample has %d items, want []", len(items))
	}
	for _, it := range removeItems(t, removeDryRunFixture) {
		if _, has := it["branch_tip"]; has {
			t.Errorf("dry-run sample item %v carries branch_tip; a dry run deletes nothing", it["name"])
		}
	}
}

// TestRemoveSchemaEnumsCoverEveryOutcome pins the schema's kind, saw,
// worktree and branch enums to the Go constants (read from source), and
// encodes one outcome per constant value through removal.Encode, which must
// accept it and produce a document the schema accepts. Why: a new outcome
// constant must fail here until the schema, the encoder's own enum list and
// the samples carry it.
func TestRemoveSchemaEnumsCoverEveryOutcome(t *testing.T) {
	schema := readJSONFile(t, removeSchema).(map[string]any)
	got := removeEnums(t)
	want := map[string][]string{}
	for _, k := range []string{"kind", "saw", "worktree", "branch"} {
		want[k] = schemaDefEnum(t, schema, "item", "properties", k)
	}
	checkEnumsEqual(t, got, want)

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
		t.Fatalf("Encode refuses a declared constant: %v", err)
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
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
	errDoc, err := removal.EncodeError(dir, errors.New("boom"))
	if err != nil {
		t.Fatal(err)
	}
	for name, b := range map[string][]byte{"run": runDoc, "plan": planDoc, "error": errDoc} {
		var v any
		if err := json.Unmarshal(b, &v); err != nil {
			t.Fatal(err)
		}
		if errs := checkAgainst(schema, v, name); len(errs) > 0 {
			t.Errorf("%s document violates the schema:\n%s\n%s", name, strings.Join(errs, "\n"), b)
		}
		checkJSONShape(t, name, b, reflect.TypeFor[removal.Envelope]())
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
