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
)

const rescueSchema = "../../../docs/contracts/rescue-result.schema.json"

// rescueFixtures are the samples by name, with what each must say: the
// outcome ("" for the error sample) and whether it is a dry run.
var rescueFixtures = map[string]struct {
	path, outcome string
	dryRun        bool
}{
	"moved sample":       {"../../../docs/contracts/rescue-result.sample.json", "moved", false},
	"dry-run sample":     {"../../../docs/contracts/rescue-result.dry-run.sample.json", "would_move", true},
	"refused sample":     {"../../../docs/contracts/rescue-result.refused.sample.json", "refused", false},
	"failed sample":      {"../../../docs/contracts/rescue-result.failed.sample.json", "failed", false},
	"branch-kept sample": {"../../../docs/contracts/rescue-result.branch-kept.sample.json", "failed", false},
	"error sample":       {"../../../docs/contracts/rescue-result.error.sample.json", "", false},
}

// TestRescueFixturesMatchEnvelopeShape pins the samples
// (docs/contracts/rescue-result*.json) to rescue.Envelope in both directions
// (every key a sample carries is one wt writes, with that JSON type, and every
// always-written key is present) and validates each against the published
// schema. Each sample says what it is: the outcome, dry_run, and that a
// refused one created nothing while a failed one kept its destination. Why: a
// script in another language reads this document; a renamed Go json tag must
// fail here until the schema and samples move with it.
func TestRescueFixturesMatchEnvelopeShape(t *testing.T) {
	schema := readJSONFile(t, rescueSchema).(map[string]any)
	for name, fx := range rescueFixtures {
		raw, err := os.ReadFile(fx.path)
		if err != nil {
			t.Fatal(err)
		}
		checkJSONShape(t, name, raw, reflect.TypeFor[rescue.Envelope]())
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
			if doc["created"] != false {
				t.Errorf("%s: created %v, want false", name, doc["created"])
			}
		case "moved", "failed":
			if doc["created"] != true || doc["destination"] == nil {
				t.Errorf("%s: created %v / destination %v", name, doc["created"], doc["destination"])
			}
		case "":
			if doc["error"] == nil {
				t.Errorf("%s: no error", name)
			}
		}
	}
}

// TestRescueSchemaEnumsMatchTheSource pins the schema's outcome and code
// enums to the Go constants (read from source, so a constant added later is
// seen) and to rescue.Outcomes / rescue.Codes, which the encoder tests
// iterate. Why: a new refusal code must fail here until the schema lists it.
func TestRescueSchemaEnumsMatchTheSource(t *testing.T) {
	schema := readJSONFile(t, rescueSchema).(map[string]any)
	props := schema["properties"].(map[string]any)
	enum := func(key string) []string {
		var out []string
		for _, e := range props[key].(map[string]any)["enum"].([]any) {
			out = append(out, e.(string))
		}
		return out
	}
	got := map[string][]string{}
	for _, c := range stringConsts(t, "../../internal/rescue") {
		switch {
		case c.typ == "Status":
			got["outcome"] = append(got["outcome"], c.val)
		case strings.HasPrefix(c.name, "Code"):
			got["code"] = append(got["code"], c.val)
		}
	}
	checkEnumsEqual(t, got, map[string][]string{"outcome": enum("outcome"), "code": enum("code")})
	var outs []string
	for _, o := range rescue.Outcomes {
		outs = append(outs, string(o))
	}
	checkEnumsEqual(t, map[string][]string{"outcome": outs, "code": rescue.Codes}, map[string][]string{"outcome": got["outcome"], "code": got["code"]})
}

// TestRescueEncodersProduceSchemaValidDocuments encodes one outcome per status
// and code, and an error, through the real encoders and validates every
// document against the schema. Why: the samples are hand-kept; this proves the
// encoder itself writes only what the schema allows, including the arrays that
// must be [] and never null.
func TestRescueEncodersProduceSchemaValidDocuments(t *testing.T) {
	schema := readJSONFile(t, rescueSchema).(map[string]any)
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
	plan := rescue.Plan{Source: rescue.Source{Path: "/s", Head: strings.Repeat("a", 40)}, Branch: "rescued/x", DestPath: "/d", Start: strings.Repeat("a", 40)}
	for _, st := range rescue.Outcomes {
		for _, code := range append([]string{""}, rescue.Codes...) {
			b, err := rescue.EncodeResult(rescue.Outcome{Status: st, Code: code, Plan: plan, Reason: "r"}, rescue.Meta{Repo: "/r"})
			check(string(st)+"/"+code, b, err)
		}
	}
	b, err := rescue.EncodeResult(rescue.Outcome{Status: rescue.StatusMoved, Plan: plan, Reason: "r",
		Record: &rescue.Record{Ref: rescue.RecordRefPrefix + "0123456789abcdef", Commit: strings.Repeat("b", 40)}}, rescue.Meta{Repo: "/r"})
	check("moved with record", b, err)
	if !strings.Contains(string(b), `"record"`) {
		t.Errorf("a moved outcome with a record encodes no record:\n%s", b)
	}
	b, err = rescue.EncodeResult(rescue.Outcome{Status: rescue.StatusRefused, Code: rescue.CodePrimary}, rescue.Meta{Repo: "/r"})
	check("bare refusal", b, err)
	b, err = rescue.EncodeError(rescue.Meta{Repo: "/r"}, errors.New("boom\nline"))
	check("error", b, err)
	if strings.Contains(string(b), `\n`) {
		t.Errorf("the error message is not one line: %s", b)
	}
	b, err = rescue.EncodeError(rescue.Meta{}, nil)
	check("nil error", b, err)
}

// TestRescueEveryOptionalKeyAppears requires every key of every struct
// reachable from rescue.Envelope, the omitempty ones included, to appear in at
// least one sample. Why: a key no sample shows could be renamed unnoticed.
func TestRescueEveryOptionalKeyAppears(t *testing.T) {
	schema := readJSONFile(t, rescueSchema).(map[string]any)
	var docs []any
	for _, fx := range rescueFixtures {
		docs = append(docs, readJSONFile(t, fx.path))
	}
	checkEveryKeyAppears(t, schema, reflect.TypeFor[rescue.Envelope](), docs...)
}

// TestRescueSchemaPropertiesMatchGoTags pins, for every struct reachable from
// rescue.Envelope, the schema's properties to the Go json fields and its
// required list to the always-written keys. Why: the schema is the published
// contract.
func TestRescueSchemaPropertiesMatchGoTags(t *testing.T) {
	schema := readJSONFile(t, rescueSchema).(map[string]any)
	if visited := checkSchemaMatchesGoTags(t, schema, reflect.TypeFor[rescue.Envelope]()); visited < 5 {
		t.Errorf("only %d structs reached from Envelope; the walk is not following the schema", visited)
	}
}

// TestRescueSamplesShowEveryOutcome requires the samples together to show each
// outcome the enum lists. Why: an outcome no sample shows is one a consumer's
// author never reads about.
func TestRescueSamplesShowEveryOutcome(t *testing.T) {
	var seen []string
	for _, fx := range rescueFixtures {
		if fx.outcome != "" {
			seen = append(seen, fx.outcome)
		}
	}
	for _, o := range rescue.Outcomes {
		if !slices.Contains(seen, string(o)) {
			t.Errorf("no sample shows outcome %q", o)
		}
	}
}
