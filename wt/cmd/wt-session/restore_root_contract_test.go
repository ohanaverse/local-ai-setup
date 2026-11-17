package main

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/rescue"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rootmove"
)

const restoreRootSchema = "../../../docs/contracts/restore-root-result.schema.json"

// restoreRootFixtures are the samples by name, with what each must say: the
// outcome ("" for the error sample), whether it is a dry run, and whether it
// is the held sample (its warnings must show both held lines).
var restoreRootFixtures = map[string]struct {
	path, outcome string
	dryRun, held  bool
}{
	"moved sample":   {"../../../docs/contracts/restore-root-result.sample.json", "moved", false, false},
	"dry-run sample": {"../../../docs/contracts/restore-root-result.dry-run.sample.json", "would_move", true, false},
	"refused sample": {"../../../docs/contracts/restore-root-result.refused.sample.json", "refused", false, false},
	"failed sample":  {"../../../docs/contracts/restore-root-result.failed.sample.json", "failed", false, false},
	"error sample":   {"../../../docs/contracts/restore-root-result.error.sample.json", "", false, false},
	"held sample":    {"../../../docs/contracts/restore-root-result.held.sample.json", "moved", false, true},
}

// TestRestoreRootFixturesMatchEnvelopeShape pins the samples
// (docs/contracts/restore-root-result*.json) to rootmove.Envelope in both
// directions (every key a sample carries is one wt writes, with that JSON
// type, and every always-written key is present) and validates each against
// the published schema. Each sample says what it is: the outcome, dry_run, and
// that a refused one created nothing while a moved or failed one created the
// worktree. Why: a script in another language reads this document; a renamed
// Go json tag must fail here until the schema and samples move with it.
func TestRestoreRootFixturesMatchEnvelopeShape(t *testing.T) {
	schema := readJSONFile(t, restoreRootSchema).(map[string]any)
	for name, fx := range restoreRootFixtures {
		raw, err := os.ReadFile(fx.path)
		if err != nil {
			t.Fatal(err)
		}
		checkJSONShape(t, name, raw, reflect.TypeFor[rootmove.Envelope]())
		doc := readJSONFile(t, fx.path).(map[string]any)
		if errs := checkAgainst(schema, doc, name); len(errs) > 0 {
			t.Errorf("%s violates the schema:\n%s", name, strings.Join(errs, "\n"))
		}
		if got, _ := doc["outcome"].(string); got != fx.outcome {
			t.Errorf("%s: outcome %q, want %q", name, got, fx.outcome)
		}
		if doc["dry_run"] != fx.dryRun {
			t.Errorf("%s: dry_run %v", name, doc["dry_run"])
		}
		switch fx.outcome {
		case "refused", "would_move":
			if doc["created"] != false || doc["record"] != nil {
				t.Errorf("%s: created %v / record %v, want false / none", name, doc["created"], doc["record"])
			}
		case "moved", "failed":
			if doc["created"] != true || doc["dest_path"] == nil {
				t.Errorf("%s: created %v / dest_path %v", name, doc["created"], doc["dest_path"])
			}
		case "":
			if doc["error"] == nil {
				t.Errorf("%s: no error", name)
			}
		}
		// The held sample shows both held lines on one held plan (R408b):
		// rescue's own and rootmove's HeldWarningPrefix line naming the
		// holder. Held warns and the move proceeds; it is never a code.
		if fx.held {
			var joined string
			if ws, ok := doc["warnings"].([]any); ok {
				lines := make([]string, 0, len(ws))
				for _, w := range ws {
					s, _ := w.(string)
					lines = append(lines, s)
				}
				joined = strings.Join(lines, "\n")
			}
			if !strings.Contains(joined, rootmove.HeldWarningPrefix) || !strings.Contains(joined, "another wt session holds this worktree") {
				t.Errorf("%s: warnings = %q, want rescue's held line and a %q line", name, joined, rootmove.HeldWarningPrefix)
			}
		}
	}
}

// restoreRootExpectedCodes is the set the contract promises: rescue's codes
// except `primary` and `nothing_to_keep` (a root is always primary; a clean
// root still moves), plus rootmove's own. Read from source, so a constant
// added later is seen.
func restoreRootExpectedCodes(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, c := range stringConsts(t, "../../internal/rescue") {
		if strings.HasPrefix(c.name, "Code") && c.val != rescue.CodePrimary && c.val != rescue.CodeNothingToKeep {
			out = append(out, c.val)
		}
	}
	for _, c := range stringConsts(t, "../../internal/rootmove") {
		if strings.HasPrefix(c.name, "Code") && !slices.Contains(out, c.val) {
			out = append(out, c.val)
		}
	}
	return out
}

// TestRestoreRootSchemaEnumsMatchTheSource pins the schema's outcome and code
// enums to the Go constants (read from source) and to rootmove.Outcomes /
// rootmove.Codes, which the encoder tests iterate. Why: a new refusal code must
// fail here until the schema lists it.
func TestRestoreRootSchemaEnumsMatchTheSource(t *testing.T) {
	schema := readJSONFile(t, restoreRootSchema).(map[string]any)
	props := schema["properties"].(map[string]any)
	enum := func(key string) []string {
		var out []string
		for _, e := range props[key].(map[string]any)["enum"].([]any) {
			out = append(out, e.(string))
		}
		return out
	}
	var outcomes []string
	for _, c := range stringConsts(t, "../../internal/rescue") {
		if c.typ == "Status" {
			outcomes = append(outcomes, c.val)
		}
	}
	codes := restoreRootExpectedCodes(t)
	checkEnumsEqual(t, map[string][]string{"outcome": outcomes, "code": codes}, map[string][]string{"outcome": enum("outcome"), "code": enum("code")})
	var outs []string
	for _, o := range rootmove.Outcomes {
		outs = append(outs, string(o))
	}
	checkEnumsEqual(t, map[string][]string{"outcome": outs, "code": rootmove.Codes}, map[string][]string{"outcome": outcomes, "code": codes})
}

// TestRestoreRootEncodersProduceSchemaValidDocuments encodes one outcome per
// status and code, and an error, through the real encoders and validates every
// document against the schema. Why: the samples are hand-kept; this proves the
// encoder itself writes only what the schema allows, including the arrays that
// must be [] and never null.
func TestRestoreRootEncodersProduceSchemaValidDocuments(t *testing.T) {
	schema := readJSONFile(t, restoreRootSchema).(map[string]any)
	check := func(name string, b []byte, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		var v map[string]any
		if err := json.Unmarshal(b, &v); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if errs := checkAgainst(schema, v, name); len(errs) > 0 {
			t.Errorf("%s violates the schema:\n%s", name, strings.Join(errs, "\n"))
		}
		if strings.Contains(string(b), "null") {
			t.Errorf("%s holds a null:\n%s", name, b)
		}
	}
	plan := rootmove.Plan{Repo: "/r", Branch: "feat", Default: "main", Head: strings.Repeat("a", 40), DestPath: "/r/.worktrees/feat", DefaultFF: true}
	for _, st := range rootmove.Outcomes {
		for _, code := range append([]string{""}, rootmove.Codes...) {
			b, err := rootmove.EncodeResult(rootmove.Outcome{Status: st, Code: code, Plan: plan, Reason: "r", Created: true}, rootmove.Meta{Repo: "/r"})
			check(string(st)+"/"+code, b, err)
		}
	}
	b, err := rootmove.EncodeResult(rootmove.Outcome{Status: rescue.StatusMoved, Plan: plan, Reason: "r", Created: true, FastForwarded: true,
		Record: &rescue.Record{Ref: rescue.RecordRefPrefix + "0123456789abcdef", Commit: strings.Repeat("b", 40)}}, rootmove.Meta{Repo: "/r"})
	check("moved with record", b, err)
	if !strings.Contains(string(b), `"record"`) || !strings.Contains(string(b), `"fast_forwarded"`) {
		t.Errorf("a moved outcome with a record and a fast-forward encodes neither:\n%s", b)
	}
	b, err = rootmove.EncodeResult(rootmove.Outcome{Status: rescue.StatusRefused, Code: rootmove.CodeDetachedRoot}, rootmove.Meta{Repo: "/r"})
	check("bare refusal", b, err)
	b, err = rootmove.EncodeError(rootmove.Meta{Repo: "/r"}, errors.New("boom\nline"))
	check("error", b, err)
	if strings.Contains(string(b), `\n`) {
		t.Errorf("the error message is not one line: %s", b)
	}
	b, err = rootmove.EncodeError(rootmove.Meta{}, nil)
	check("nil error", b, err)
}

// TestRestoreRootWouldMoveEncodesSchemaValid encodes rootmove.WouldMove's
// outcome for a literal plan and validates it against the schema, with
// outcome would_move and created false. Why: --dry-run prints exactly this.
func TestRestoreRootWouldMoveEncodesSchemaValid(t *testing.T) {
	schema := readJSONFile(t, restoreRootSchema).(map[string]any)
	p := rootmove.Plan{Repo: "/r", Branch: "feat", Default: "main", Head: strings.Repeat("a", 40), DestPath: "/r/.worktrees/feat", Warnings: []string{"w"}}
	b, err := rootmove.EncodeResult(rootmove.WouldMove(&p), rootmove.Meta{Repo: "/r", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	if errs := checkAgainst(schema, v, "would_move"); len(errs) > 0 {
		t.Errorf("violates the schema:\n%s", strings.Join(errs, "\n"))
	}
	if v["outcome"] != "would_move" || v["created"] != false {
		t.Errorf("outcome %v created %v", v["outcome"], v["created"])
	}
}

// TestRestoreRootEveryOptionalKeyAppears requires every key of every struct
// reachable from rootmove.Envelope, the omitempty ones included, to appear in
// at least one sample. Why: a key no sample shows could be renamed unnoticed.
func TestRestoreRootEveryOptionalKeyAppears(t *testing.T) {
	schema := readJSONFile(t, restoreRootSchema).(map[string]any)
	var docs []any
	for _, fx := range restoreRootFixtures {
		docs = append(docs, readJSONFile(t, fx.path))
	}
	checkEveryKeyAppears(t, schema, reflect.TypeFor[rootmove.Envelope](), docs...)
}

// TestRestoreRootSchemaPropertiesMatchGoTags pins, for every struct reachable
// from rootmove.Envelope, the schema's properties to the Go json fields and its
// required list to the always-written keys. Why: the schema is the published
// contract.
func TestRestoreRootSchemaPropertiesMatchGoTags(t *testing.T) {
	schema := readJSONFile(t, restoreRootSchema).(map[string]any)
	if visited := checkSchemaMatchesGoTags(t, schema, reflect.TypeFor[rootmove.Envelope]()); visited < 4 {
		t.Errorf("only %d structs reached from Envelope; the walk is not following the schema", visited)
	}
}

// TestRestoreRootSamplesShowEveryOutcome requires the samples together to show
// each outcome the enum lists. Why: an outcome no sample shows is one a
// consumer's author never reads about.
func TestRestoreRootSamplesShowEveryOutcome(t *testing.T) {
	var seen []string
	for _, fx := range restoreRootFixtures {
		if fx.outcome != "" {
			seen = append(seen, fx.outcome)
		}
	}
	for _, o := range rootmove.Outcomes {
		if !slices.Contains(seen, string(o)) {
			t.Errorf("no sample shows outcome %q", o)
		}
	}
}
